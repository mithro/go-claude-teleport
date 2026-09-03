package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// staged builds two hosts, streams everything into staging and returns the
// dest Paths (config dir = <destHome>/.claude).
func staged(t *testing.T) (*Manifest, string, session.Paths) {
	t.Helper()
	m, staging := newTwoHosts(t)
	st, _ := Diff(context.Background(), m, staging)
	var buf bytes.Buffer
	if err := Send(context.Background(), m, Need(m, st), &buf, nil); err != nil {
		t.Fatal(err)
	}
	if err := Receive(context.Background(), m, &buf, staging, nil); err != nil {
		t.Fatal(err)
	}
	cfg := m.Entries[0].Dst
	for filepath.Base(cfg) != ".claude" {
		cfg = filepath.Dir(cfg)
	}
	home := filepath.Dir(cfg)
	p := session.Paths{Home: home, ConfigDir: cfg, GlobalJSON: filepath.Join(home, ".claude.json"), DataDir: filepath.Join(home, ".local", "share", "claude-teleport")}
	return m, staging, p
}

func TestInstallFreshDestination(t *testing.T) {
	m, staging, p := staged(t)
	st, _ := Diff(context.Background(), m, staging)
	extra := InstallExtras{
		IndexEntry:   &session.IndexEntry{SessionID: sid, FullPath: m.Entries[1].Dst, ProjectPath: "/home/bob/work", FirstPrompt: "hello"},
		History:      []json.RawMessage{json.RawMessage(`{"display":"hi","timestamp":1,"project":"/home/bob/work","sessionId":"` + sid + `"}`)},
		ProjectCwd:   "/home/bob/work",
		ProjectEntry: session.ProjectEntry{"hasTrustDialogAccepted": true},
	}
	rep, err := Install(context.Background(), m, st, staging, p, extra)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Installed != 5 || rep.FastForwarded != 0 || rep.SkippedSame != 0 {
		t.Errorf("report = %+v", rep)
	}
	if rep.IndexMerged != 1 || rep.HistoryAdded != 1 || !rep.ProjectEntryAdded {
		t.Errorf("merges = %+v", rep)
	}
	for _, e := range m.Entries {
		fi, err := os.Lstat(e.Dst)
		if err != nil {
			t.Errorf("%s not installed: %v", e.Dst, err)
			continue
		}
		if e.IsRegular() && fi.Mode().Perm() != os.FileMode(e.Mode).Perm() {
			t.Errorf("%s mode %v want %v", e.Dst, fi.Mode().Perm(), os.FileMode(e.Mode).Perm())
		}
		if e.IsRegular() && !fi.ModTime().Equal(e.ModTime) {
			t.Errorf("%s mtime %v want %v", e.Dst, fi.ModTime(), e.ModTime)
		}
	}
	parent, _ := os.Stat(filepath.Dir(m.Entries[3].Dst)) // todos/ created by Install
	if parent.Mode().Perm() != 0o700 {
		t.Errorf("session parent dir mode = %v, want 0700", parent.Mode().Perm())
	}
	if _, err := os.Stat(StagedPath(staging, 1)); !os.IsNotExist(err) {
		t.Errorf("staged copy must be moved, not copied")
	}
	idx, _ := os.ReadFile(filepath.Join(p.ProjectDir("/home/bob/work"), "sessions-index.json"))
	if !strings.Contains(string(idx), sid) {
		t.Errorf("sessions-index.json not merged: %s", idx)
	}
	hist, _ := os.ReadFile(p.HistoryFile())
	if !strings.Contains(string(hist), `"display":"hi"`) {
		t.Errorf("history not appended: %s", hist)
	}
	gj, _ := os.ReadFile(p.GlobalJSON)
	if !strings.Contains(string(gj), `"/home/bob/work"`) {
		t.Errorf("project entry not added: %s", gj)
	}

	// idempotent: a second Install after a fresh Diff is all present-same
	st, _ = Diff(context.Background(), m, staging)
	rep, err = Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err != nil || rep.SkippedSame != 5 || rep.Installed != 0 {
		t.Errorf("second install: rep=%+v err=%v", rep, err)
	}
}

func TestInstallFastForwardOverwritesOnlyPrefix(t *testing.T) {
	m, staging, p := staged(t)
	full, _ := os.ReadFile(StagedPath(staging, 1))
	// Controller ruling 1: the authoritative ff-check for a .jsonl Dst is
	// session.IsRecordPrefix (record-wise, not raw bytes), so the dest copy
	// must end on a record boundary to be a valid ff-candidate — truncate
	// after the first complete JSONL line, not at an arbitrary byte offset.
	nl := bytes.IndexByte(full, '\n')
	if nl < 0 || nl+1 >= len(full) {
		t.Fatalf("fixture transcript has no second record to fast-forward into: %q", full)
	}
	partial := full[:nl+1]
	os.MkdirAll(filepath.Dir(m.Entries[1].Dst), 0o700)
	os.WriteFile(m.Entries[1].Dst, partial, 0o600)
	st, _ := Diff(context.Background(), m, staging)
	if st[1] != FFCandidate {
		t.Fatalf("status = %s", st[1])
	}
	rep, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.FastForwarded != 1 {
		t.Errorf("report = %+v", rep)
	}
	got, _ := os.ReadFile(m.Entries[1].Dst)
	if !bytes.Equal(got, full) {
		t.Errorf("fast-forward did not replace the prefix")
	}
}

func TestInstallRefusesCollision(t *testing.T) {
	m, staging, p := staged(t)
	os.MkdirAll(filepath.Dir(m.Entries[3].Dst), 0o700)
	os.WriteFile(m.Entries[3].Dst, []byte("unrelated"), 0o600)
	st, _ := Diff(context.Background(), m, staging)
	_, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err == nil || !strings.Contains(err.Error(), m.Entries[3].Dst) || !strings.Contains(err.Error(), "present-different") {
		t.Fatalf("err = %v, want refusal naming the path", err)
	}
	got, _ := os.ReadFile(m.Entries[3].Dst)
	if string(got) != "unrelated" {
		t.Errorf("collision file was modified")
	}
	// entries before the collision were installed (idempotent), the rest untouched
	if _, err := os.Stat(m.Entries[1].Dst); err != nil {
		t.Errorf("entry 1 should have been installed before the stop: %v", err)
	}
	if _, err := os.Lstat(m.Entries[4].Dst); !os.IsNotExist(err) {
		t.Errorf("entry 4 must not be installed after the stop")
	}
}

func TestInstallRefusesNotStaged(t *testing.T) {
	m, staging, p := staged(t)
	os.Remove(StagedPath(staging, 2))
	st, _ := Diff(context.Background(), m, staging)
	_, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("err = %v, want absent refusal", err)
	}
}

func TestInstallMemoryCopyIfAbsent(t *testing.T) {
	m, staging, p := staged(t)
	memSrc := filepath.Join(t.TempDir(), "memory.md")
	os.WriteFile(memSrc, []byte("# notes\n"), 0o600)
	memDst := filepath.Join(p.ConfigDir, "projects", "-home-bob-work", "memory", "MEMORY.md")
	mem := Entry{ID: 5, Category: session.CatSession, Src: memSrc, Dst: memDst, Size: 8, Mode: 0o600, SHA256: sha("# notes\n")}
	m.Entries = append(m.Entries, mem)
	writeFile(t, StagedPath(staging, 5), "# notes\n")
	st, _ := Diff(context.Background(), m, staging)
	rep, err := Install(context.Background(), m, st, staging, p, InstallExtras{Memory: []Entry{mem}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.MemoryCopied) != 1 || rep.MemoryCopied[0] != memDst {
		t.Errorf("MemoryCopied = %v", rep.MemoryCopied)
	}
	// second run with different dest content: reported, not overwritten
	os.WriteFile(memDst, []byte("# edited locally\n"), 0o600)
	writeFile(t, StagedPath(staging, 5), "# notes\n")
	st, _ = Diff(context.Background(), m, staging)
	rep, err = Install(context.Background(), m, st, staging, p, InstallExtras{Memory: []Entry{mem}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.MemoryDiffers) != 1 || rep.MemoryDiffers[0] != memDst {
		t.Errorf("MemoryDiffers = %v", rep.MemoryDiffers)
	}
	got, _ := os.ReadFile(memDst)
	if string(got) != "# edited locally\n" {
		t.Errorf("memory file overwritten")
	}
}

func TestInstallRefusesMemoryEntryWithMismatchedID(t *testing.T) {
	m, staging, p := staged(t)
	memSrc := filepath.Join(t.TempDir(), "memory.md")
	os.WriteFile(memSrc, []byte("# notes\n"), 0o600)
	memDst := filepath.Join(p.ConfigDir, "projects", "-home-bob-work", "memory", "MEMORY.md")
	mem := Entry{ID: 5, Category: session.CatSession, Src: memSrc, Dst: memDst, Size: 8, Mode: 0o600, SHA256: sha("# notes\n")}
	m.Entries = append(m.Entries, mem)
	writeFile(t, StagedPath(staging, 5), "# notes\n")
	st, _ := Diff(context.Background(), m, staging)

	// The Dst does not match manifest entry ID 5 (foreign/hand-built Entry
	// reusing an unrelated ID) — Install must refuse before touching disk.
	foreign := Entry{ID: 5, Category: session.CatSession, Src: memSrc, Dst: memDst + ".evil", Size: 8, Mode: 0o600, SHA256: sha("# notes\n")}
	_, err := Install(context.Background(), m, st, staging, p, InstallExtras{Memory: []Entry{foreign}})
	if err == nil || !strings.Contains(err.Error(), "not a manifest entry") {
		t.Fatalf("err = %v, want refusal naming the mismatched memory entry", err)
	}
	if _, err := os.Stat(memDst); !os.IsNotExist(err) {
		t.Errorf("mismatched memory entry must not be installed")
	}

	// An ID that does not exist in the manifest at all is refused too.
	unknown := Entry{ID: 999, Category: session.CatSession, Src: memSrc, Dst: memDst, Size: 8, Mode: 0o600, SHA256: sha("# notes\n")}
	_, err = Install(context.Background(), m, st, staging, p, InstallExtras{Memory: []Entry{unknown}})
	if err == nil || !strings.Contains(err.Error(), "not a manifest entry") {
		t.Fatalf("err = %v, want refusal for unknown memory id", err)
	}
}

// TestInstallRefusesSmuggledCredentialsDst covers the defense-in-depth
// destination re-check (controller item 5): even though a well-behaved
// Build never emits a manifest entry inside session.Forbidden, Install must
// independently refuse one anyway — e.g. a manifest that smuggles a session
// entry's Dst into <ConfigDir>/.credentials.json — before touching disk.
func TestInstallRefusesSmuggledCredentialsDst(t *testing.T) {
	m, staging, p := staged(t)
	victim := 1 // the transcript entry: a plain session-category file
	if m.Entries[victim].Category != session.CatSession || m.Entries[victim].IsDir() {
		t.Fatalf("fixture entry %d is not a plain session file: %+v", victim, m.Entries[victim])
	}
	credPath := filepath.Join(p.ConfigDir, ".credentials.json")
	m.Entries[victim].Dst = credPath
	st, _ := Diff(context.Background(), m, staging)
	_, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err == nil || !strings.Contains(err.Error(), "forbidden") || !strings.Contains(err.Error(), credPath) {
		t.Fatalf("err = %v, want a forbidden-path refusal naming %s", err, credPath)
	}
	if _, statErr := os.Stat(credPath); !os.IsNotExist(statErr) {
		t.Errorf(".credentials.json must not have been written")
	}
	for i, e := range m.Entries {
		if i == victim || e.IsDir() {
			continue
		}
		if _, err := os.Lstat(e.Dst); err == nil {
			t.Errorf("entry %d (%s) must not have been installed before the smuggled entry was rejected", i, e.Dst)
		}
	}
}

// TestInstallRefusesDstOutsideHome covers the second half of the same
// defense-in-depth check: a Dst that lexically escapes p.Home altogether
// (any category, not just session) is refused before touching disk.
func TestInstallRefusesDstOutsideHome(t *testing.T) {
	m, staging, p := staged(t)
	victim := 3 // the todos/<sid>.json entry
	if m.Entries[victim].IsDir() {
		t.Fatalf("fixture entry %d is a dir, want a plain file", victim)
	}
	outside := filepath.Join(filepath.Dir(p.Home), "evil-outside", "payload.txt")
	m.Entries[victim].Dst = outside
	st, _ := Diff(context.Background(), m, staging)
	_, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err == nil || !strings.Contains(err.Error(), "not under home") || !strings.Contains(err.Error(), outside) {
		t.Fatalf("err = %v, want a not-under-home refusal naming %s", err, outside)
	}
	if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
		t.Errorf("payload must not have been written outside home")
	}
}

// TestUninstallRefusesSmuggledCredentialsDst mirrors the Install case for
// Uninstall: a manifest entry whose Dst has been retargeted at
// <ConfigDir>/.credentials.json (with a matching hash, so nothing else
// would stop the removal) must be refused before anything is deleted —
// including the OTHER, legitimate entries the same manifest lists.
func TestUninstallRefusesSmuggledCredentialsDst(t *testing.T) {
	m, staging, p := staged(t)
	st, _ := Diff(context.Background(), m, staging)
	if _, err := Install(context.Background(), m, st, staging, p, InstallExtras{}); err != nil {
		t.Fatal(err)
	}
	credPath := filepath.Join(p.ConfigDir, ".credentials.json")
	os.WriteFile(credPath, []byte("super-secret"), 0o600)
	sum, _, err := HashFile(credPath)
	if err != nil {
		t.Fatal(err)
	}
	victim := 1
	m.Entries[victim].Dst = credPath
	m.Entries[victim].SHA256 = sum

	removed, err := Uninstall(m, p)
	if err == nil || !strings.Contains(err.Error(), "forbidden") || !strings.Contains(err.Error(), credPath) {
		t.Fatalf("err = %v removed = %v, want a forbidden-path refusal naming %s", err, removed, credPath)
	}
	if removed != nil {
		t.Errorf("nothing should have been removed: %v", removed)
	}
	if got, err := os.ReadFile(credPath); err != nil || string(got) != "super-secret" {
		t.Errorf(".credentials.json must survive untouched: %q err=%v", got, err)
	}
	for i, e := range m.Entries {
		if i == victim || e.IsDir() {
			continue
		}
		if _, err := os.Lstat(e.Dst); err != nil {
			t.Errorf("entry %d (%s) must not have been removed either: %v", i, e.Dst, err)
		}
	}
}

// TestUninstallRefusesDstOutsideHome mirrors the Install case: a manifest
// entry whose Dst lexically escapes p.Home is refused before any deletion.
func TestUninstallRefusesDstOutsideHome(t *testing.T) {
	m, staging, p := staged(t)
	st, _ := Diff(context.Background(), m, staging)
	if _, err := Install(context.Background(), m, st, staging, p, InstallExtras{}); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum, _, err := HashFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	victim := 3
	m.Entries[victim].Dst = outside
	m.Entries[victim].SHA256 = sum

	removed, err := Uninstall(m, p)
	if err == nil || !strings.Contains(err.Error(), "not under home") || !strings.Contains(err.Error(), outside) {
		t.Fatalf("err = %v removed = %v, want a not-under-home refusal naming %s", err, removed, outside)
	}
	if removed != nil {
		t.Errorf("nothing should have been removed: %v", removed)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("payload outside home must survive untouched: %v", err)
	}
}

func TestUninstallRemovesOnlyMatching(t *testing.T) {
	m, staging, p := staged(t)
	st, _ := Diff(context.Background(), m, staging)
	if _, err := Install(context.Background(), m, st, staging, p, InstallExtras{}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(m.Entries[3].Dst, []byte("changed after install"), 0o600)
	removed, err := Uninstall(m, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range removed {
		if r == m.Entries[3].Dst {
			t.Errorf("modified file must not be removed")
		}
	}
	if _, err := os.Stat(m.Entries[3].Dst); err != nil {
		t.Errorf("modified file gone")
	}
	if _, err := os.Stat(m.Entries[1].Dst); !os.IsNotExist(err) {
		t.Errorf("matching transcript should be removed")
	}
	if _, err := os.Lstat(m.Entries[4].Dst); !os.IsNotExist(err) {
		t.Errorf("symlink should be removed")
	}
	if _, err := os.Stat(m.Entries[0].Dst); !os.IsNotExist(err) {
		t.Errorf("emptied project dir should be removed")
	}
}

// TestUninstallIDsOnlyNamedEntries covers Task 23 (abandon's
// destination-side deletion): UninstallIDs must restrict deletion to the
// given ids even when another, unnamed entry's current content also still
// matches the manifest hash.
func TestUninstallIDsOnlyNamedEntries(t *testing.T) {
	m, staging, p := staged(t)
	st, _ := Diff(context.Background(), m, staging)
	if _, err := Install(context.Background(), m, st, staging, p, InstallExtras{}); err != nil {
		t.Fatal(err)
	}
	removed, err := UninstallIDs(m, p, []int{m.Entries[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.Entries[1].Dst); !os.IsNotExist(err) {
		t.Errorf("named entry should be removed")
	}
	if _, err := os.Stat(m.Entries[3].Dst); err != nil {
		t.Errorf("entry not named in ids must survive: %v", err)
	}
	for _, r := range removed {
		if r == m.Entries[3].Dst {
			t.Errorf("unnamed entry must not be reported removed")
		}
	}
}

// TestUninstallIDsNeverRemovesDirNotInIDs is folded minor M1: a directory
// entry is a cleanup candidate only when its OWN id is in ids (i.e. the
// job itself created it) — never merely because every file under it also
// happened to be named. Here every non-dir entry is named, emptying the
// project directory, but its own id (0) is not — it must survive.
func TestUninstallIDsNeverRemovesDirNotInIDs(t *testing.T) {
	m, staging, p := staged(t)
	st, _ := Diff(context.Background(), m, staging)
	if _, err := Install(context.Background(), m, st, staging, p, InstallExtras{}); err != nil {
		t.Fatal(err)
	}
	var ids []int
	for _, e := range m.Entries {
		if !e.IsDir() {
			ids = append(ids, e.ID)
		}
	}
	if _, err := UninstallIDs(m, p, ids); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.Entries[0].Dst); err != nil {
		t.Errorf("directory whose id is not in ids must survive even once empty: %v", err)
	}
}

// TestUninstallRemovesEmptiedNestedDirs covers controller ruling 2: a
// manifest whose dir entries include a nested "<sid>/subagents/" tree must
// leave no empty directories behind once every file under it is removed —
// Uninstall has to walk the manifest's dir entries deepest-first, not just
// the top-level project dir.
func TestUninstallRemovesEmptiedNestedDirs(t *testing.T) {
	m, staging, p := staged(t)
	st, _ := Diff(context.Background(), m, staging)
	if _, err := Install(context.Background(), m, st, staging, p, InstallExtras{}); err != nil {
		t.Fatal(err)
	}
	// entry 2 is proj/<sid>/subagents/agent-1.jsonl; its parent dirs
	// (proj/<sid>/subagents and proj/<sid>) are not manifest dir entries
	// themselves, but the sidecar structure must not leave empties behind
	// when everything manifest-listed under proj/ is removed.
	sidecarSubagents := filepath.Dir(m.Entries[2].Dst)
	sidecarDir := filepath.Dir(sidecarSubagents)
	if _, err := os.Stat(sidecarSubagents); err != nil {
		t.Fatalf("fixture missing sidecar subagents dir: %v", err)
	}
	removed, err := Uninstall(m, p)
	if err != nil {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if _, err := os.Stat(m.Entries[0].Dst); !os.IsNotExist(err) {
		t.Errorf("project dir (manifest entry 0) should be removed, leaving no empty tree: %v", err)
	}
	if _, err := os.Stat(sidecarDir); !os.IsNotExist(err) {
		t.Errorf("sidecar dir left behind: %v", err)
	}
}
