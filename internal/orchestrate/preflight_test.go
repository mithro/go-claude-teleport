// internal/orchestrate/preflight_test.go
package orchestrate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

func gitc(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=alice", "GIT_AUTHOR_EMAIL=a@laptop.example", "GIT_COMMITTER_NAME=alice", "GIT_COMMITTER_EMAIL=a@laptop.example", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func makeRepo(t *testing.T, dir string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	gitc(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644)
	gitc(t, dir, "add", ".")
	gitc(t, dir, "commit", "-q", "-m", "init")
}

func baseOptions() Options {
	return Options{Direction: "to", Selector: session.Selector{ID: session.ID(sid)}, State: "auto", ExitTimeout: 10 * time.Second, StartTimeout: 20 * time.Second, Target: "bob@big-storage.example"}
}

func TestPreflightIdleSessionFreshMainWithHomeRewrite(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := filepath.Join(src.paths.Home, "github", "x")
	makeRepo(t, cwd)
	os.WriteFile(filepath.Join(cwd, "scratch.txt"), []byte("untracked"), 0o644)
	seedSession(t, src, cwd)
	srcProj := src.paths.ProjectDir(cwd)
	if err := session.MergeIndexEntry(srcProj, session.IndexEntry{SessionID: sid, FullPath: filepath.Join(srcProj, sid+".jsonl"), ProjectPath: cwd}); err != nil {
		t.Fatal(err)
	}

	o := baseOptions()
	o.State = "idle"
	p, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatal(err)
	}
	if p.Git == nil || p.Git.Mode != gitx.ModeFreshMain {
		t.Fatalf("git plan = %+v", p.Git)
	}
	if p.Git.DstMain != filepath.Join(dst.paths.Home, "github", "x") {
		t.Errorf("DstMain = %q", p.Git.DstMain)
	}
	if p.Tmux != nil || p.TargetState != "idle" {
		t.Errorf("tmux/target = %+v %q", p.Tmux, p.TargetState)
	}
	if len(p.Statuses) == 0 || len(p.Collisions) != 0 {
		t.Errorf("statuses=%d collisions=%v", len(p.Statuses), p.Collisions)
	}
	if _, err := os.Stat(p.ManifestPath); err != nil {
		t.Errorf("manifest not saved on the driver: %v", err)
	}
	if p.Extras == nil || p.Extras.ProjectCwd != filepath.Join(dst.paths.Home, "github", "x") {
		t.Errorf("extras = %+v", p.Extras)
	}
	if !p.PathMap.Empty() && p.PathMap.ApplyPath(src.paths.Home+"/a") != dst.paths.Home+"/a" {
		t.Errorf("path map = %+v", p.PathMap)
	}

	// spec §7.2 ruling R-P3-18a: a plain Home-prefix mapping cannot rewrite
	// the Munge()'d project-directory path component (Munge flattens the
	// whole cwd into one path segment); Preflight must add a dedicated
	// source-project-dir -> dest-project-dir mapping so the manifest's
	// CatSession entries, and the rewritten sessions-index fullPath, land
	// under the DESTINATION's own munged project directory rather than
	// carrying over the source's.
	dstProj := dst.paths.ProjectDir(filepath.Join(dst.paths.Home, "github", "x"))
	m, err := transfer.Load(p.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range m.Entries {
		if e.Category == session.CatSession && strings.HasSuffix(e.Dst, "/"+sid+".jsonl") {
			found = true
			if filepath.Dir(e.Dst) != dstProj {
				t.Errorf("transcript Dst = %q, want under dest munged project dir %q", e.Dst, dstProj)
			}
		}
	}
	if !found {
		t.Fatalf("manifest has no session entry for %s.jsonl", sid)
	}
	if p.Extras.IndexEntry == nil {
		t.Fatalf("Extras.IndexEntry missing (sessions-index entry was seeded)")
	}
	if want := filepath.Join(dstProj, sid+".jsonl"); p.Extras.IndexEntry.FullPath != want {
		t.Errorf("IndexEntry.FullPath = %q, want %q", p.Extras.IndexEntry.FullPath, want)
	}
}

func TestPreflightRefusesWithoutTmuxUnlessIdle(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := filepath.Join(src.paths.Home, "x")
	seedSession(t, src, cwd)
	o := baseOptions()
	o.State = "running"
	_, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	var re *RefusedError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want RefusedError", err)
	}
}

func TestPreflightDriftRefusalAndOverride(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := filepath.Join(src.paths.Home, "x")
	seedSession(t, src, cwd)
	writeJSON(t, filepath.Join(src.paths.ConfigDir, "settings.json"), map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": "echo hi"}}}}}})
	o := baseOptions()
	o.State = "idle"
	_, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	var re *RefusedError
	if !errors.As(err, &re) {
		t.Fatalf("hooks drift must refuse, got %v", err)
	}
	o.AllowDrift = true
	p, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatal(err)
	}
	if p.Drift.Blocking || len(p.Drift.Diffs) == 0 {
		t.Errorf("drift after override = %+v", p.Drift)
	}
}

// TestPreflightCollisionRefusal exercises the collision path for a
// same-session transcript. Adaptation from the brief: transfer.Diff only
// classifies an FFAllowed (same-session) entry as PresentDifferent once a
// *verified staged copy* exists to run the record-prefix check against —
// with nothing staged it is unconditionally FFCandidate, which
// transfer.Blocking allows through (see TestDiffFastForwardAndCollision in
// internal/transfer/diff_test.go: "ff-candidate is allowed", and the
// "controller ruling 1" comment in internal/transfer/diff.go). So this test
// first runs Preflight with nothing on the destination (must succeed) to
// learn the manifest's real destination path for the transcript — a raw
// session.PathMap.ApplyPath prefix-rewrite of the source's Munge()'d
// project directory does not re-munge that trailing path component against
// the destination cwd, so it is not simply dst's own ProjectDir(cwd) — then
// writes a conflicting file there and stages the real (post path-rewrite)
// transcript content, exactly as a real transfer would before install,
// which is what makes the mismatch authoritative.
func TestPreflightCollisionRefusal(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := filepath.Join(src.paths.Home, "x")
	seedSession(t, src, cwd)

	o := baseOptions()
	o.State = "idle"
	p, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatalf("preflight before any destination content must succeed: %v", err)
	}
	m, err := transfer.Load(p.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var dstTranscript string
	entryID := -1
	for _, e := range m.Entries {
		if e.Category == session.CatSession && strings.HasSuffix(e.Dst, "/"+sid+".jsonl") {
			dstTranscript, entryID = e.Dst, e.ID
			break
		}
	}
	if entryID < 0 {
		t.Fatalf("manifest has no session entry for %s.jsonl", sid)
	}

	// A different transcript with the same id already on the destination.
	os.MkdirAll(filepath.Dir(dstTranscript), 0o700)
	if err := os.WriteFile(dstTranscript, []byte(`{"type":"user","sessionId":"`+sid+`","cwd":"/elsewhere"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Stage the real (post path-rewrite) transcript content under the
	// destination's staging dir, exactly as a real transfer would before
	// install — this is what makes the mismatch authoritative.
	srcTranscript := filepath.Join(src.paths.ProjectDir(cwd), sid+".jsonl")
	raw, err := os.ReadFile(srcTranscript)
	if err != nil {
		t.Fatal(err)
	}
	var staged bytes.Buffer
	if _, err := session.RewriteJSONL(bytes.NewReader(raw), &staged, p.PathMap); err != nil {
		t.Fatal(err)
	}
	stagingDir := job.StagingDir(dst.paths.DataDir, sid)
	os.MkdirAll(stagingDir, 0o700)
	if err := os.WriteFile(transfer.StagedPath(stagingDir, entryID), staged.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = Preflight(context.Background(), o, src.ep, dst.ep, sid)
	var re *RefusedError
	if !errors.As(err, &re) || !containsStr(re.Reason, sid+".jsonl") {
		t.Fatalf("err = %v, want collision refusal naming the transcript", err)
	}
	o.Force = true
	if _, err := Preflight(context.Background(), o, src.ep, dst.ep, sid); err != nil {
		t.Fatalf("--force must allow same-session overwrite: %v", err)
	}
}

func TestPreflightGitDivergenceRefusal(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := filepath.Join(src.paths.Home, "x")
	makeRepo(t, cwd)
	seedSession(t, src, cwd)
	dstRepo := filepath.Join(dst.paths.Home, "x")
	gitc(t, filepath.Dir(dstRepo), "clone", "-q", cwd, dstRepo)
	os.WriteFile(filepath.Join(dstRepo, "other.txt"), []byte("diverge\n"), 0o644)
	gitc(t, dstRepo, "add", ".")
	gitc(t, dstRepo, "commit", "-q", "-m", "diverged")
	o := baseOptions()
	o.State = "idle"
	_, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	var re *RefusedError
	if !errors.As(err, &re) || !containsStr(re.Reason, "fast-forward") {
		t.Fatalf("err = %v, want non-fast-forward refusal", err)
	}
}

func containsStr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestAnnotateManifestSkipsWorktreeDirs covers B10. Every CatWorktree
// manifest entry was registered in Git.DirtyEntries, directory entries
// included — but git-attach reads that map as "dst path -> the staged FILE
// to place here", so a directory landed in it as a file to copy. A
// directory needs no dirty-file treatment: the ordinary install path
// creates it.
func TestAnnotateManifestSkipsWorktreeDirs(t *testing.T) {
	p := &Plan{
		Git:    &gitx.Plan{Mode: gitx.ModeExistingMain, IndexEntryID: gitx.NoEntry, PackEntryID: gitx.NoEntry},
		Extras: &transfer.InstallExtras{},
	}
	dir := filepath.Join("/home", "bob", "repo", "sub")
	file := filepath.Join(dir, "dirty.txt")
	m := &transfer.Manifest{Version: 1, Entries: []transfer.Entry{
		{ID: 0, Category: session.CatWorktree, Dst: dir, Mode: uint32(os.ModeDir | 0o755)},
		{ID: 1, Category: session.CatWorktree, Dst: file, Mode: 0o644},
	}}
	p.annotateManifest(m, nil)

	if _, ok := p.Git.DirtyEntries[dir]; ok {
		t.Errorf("DirtyEntries = %v, want the directory entry left out", p.Git.DirtyEntries)
	}
	if id, ok := p.Git.DirtyEntries[file]; !ok || id != 1 {
		t.Errorf("DirtyEntries = %v, want the dirty file registered as id 1", p.Git.DirtyEntries)
	}
	// The directory must still be installed by the ordinary path, so it
	// must NOT be deferred either.
	if m.Entries[0].Deferred {
		t.Error("a worktree directory entry must not be deferred: the plain install path creates it")
	}
	if !m.Entries[1].Deferred {
		t.Error("the dirty worktree file is git-attach's to place, so it stays deferred")
	}
}
