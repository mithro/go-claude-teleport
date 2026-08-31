package remote

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

func gitc(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=alice", "GIT_AUTHOR_EMAIL=a@laptop.example", "GIT_COMMITTER_NAME=alice", "GIT_COMMITTER_EMAIL=a@laptop.example", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestLocalInventoryGitAndDestState(t *testing.T) {
	p := testPaths(t)
	repo := filepath.Join(p.Home, "x")
	os.MkdirAll(repo, 0o755)
	gitc(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a"), 0o644)
	gitc(t, repo, "add", "a.txt")
	gitc(t, repo, "commit", "-q", "-m", "init")
	l := NewLocal(p, "/usr/local/bin/claude-teleport", LocalOptions{ProcRoot: "/proc"})
	info, err := l.InventoryGit(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if info.Branch != "main" || info.MainDir != repo {
		t.Errorf("info = %+v", info)
	}
	if _, err := l.InventoryGit(context.Background(), t.TempDir()); err == nil {
		t.Error("non-repo must return an error")
	} else if e, ok := err.(*Error); !ok || e.Code != "not-found" {
		t.Errorf("non-repo error = %v, want *Error{Code: not-found}", err)
	}
	ds, err := l.GitDestState(context.Background(), repo, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !ds.MainExists || !ds.Clean {
		t.Errorf("dest state = %+v", ds)
	}
}

func TestLocalGitAttachUsesStagingPaths(t *testing.T) {
	p := testPaths(t)
	main := filepath.Join(p.Home, "x")
	os.MkdirAll(main, 0o755)
	gitc(t, main, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(main, "a.txt"), []byte("a"), 0o644)
	gitc(t, main, "add", "a.txt")
	gitc(t, main, "commit", "-q", "-m", "init")
	tip := strings.TrimSpace(gitc(t, main, "rev-parse", "HEAD"))
	jobID := "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"
	staging := job.StagingDir(p.DataDir, jobID)
	os.MkdirAll(staging, 0o700)
	os.WriteFile(filepath.Join(staging, "7"), []byte("untracked\n"), 0o644)
	idx, _ := os.ReadFile(filepath.Join(main, ".git", "index"))
	os.WriteFile(filepath.Join(staging, "8"), idx, 0o644)

	w := filepath.Join(main, ".worktrees", "feat")
	plan := &gitx.Plan{Mode: gitx.ModeExistingMain, SrcMain: "/home/alice/x", SrcWorktree: "/home/alice/x/.worktrees/feat",
		DstMain: main, DstWorktree: w, Linked: true, WorktreeName: "feat", Branch: "feat", Tip: tip, NeedPack: false,
		IndexRel: ".git/worktrees/feat/index", IndexEntryID: 8, PackEntryID: gitx.NoEntry,
		DirtyEntries: map[string]int{filepath.Join(w, "new.txt"): 7}}
	l := NewLocal(p, "/usr/local/bin/claude-teleport", LocalOptions{ProcRoot: "/proc"})
	if err := l.GitAttach(context.Background(), plan, jobID); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(w, "new.txt")); err != nil || string(b) != "untracked\n" {
		t.Errorf("dirty file not applied: %v %q", err, b)
	}
	if got := strings.TrimSpace(gitc(t, w, "rev-parse", "--abbrev-ref", "HEAD")); got != "feat" {
		t.Errorf("worktree branch = %q", got)
	}
	if !strings.Contains(gitc(t, main, "worktree", "list"), w) {
		t.Error("git does not list the new worktree")
	}
}

// TestLocalGitAttachAppliesManifestEntryMode is task 20 point C: a dirty
// worktree file lands with the mode its manifest entry recorded, not
// whatever mode the staged copy on disk happens to carry.
func TestLocalGitAttachAppliesManifestEntryMode(t *testing.T) {
	p := testPaths(t)
	main := filepath.Join(p.Home, "x")
	os.MkdirAll(main, 0o755)
	gitc(t, main, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(main, "a.txt"), []byte("a"), 0o644)
	gitc(t, main, "add", "a.txt")
	gitc(t, main, "commit", "-q", "-m", "init")
	tip := strings.TrimSpace(gitc(t, main, "rev-parse", "HEAD"))
	jobID := "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"
	staging := job.StagingDir(p.DataDir, jobID)
	os.MkdirAll(staging, 0o700)
	content := []byte("#!/bin/sh\necho hi\n")
	os.WriteFile(filepath.Join(staging, "7"), content, 0o644) // staged with 0644...

	w := filepath.Join(main, ".worktrees", "feat")
	m := &transfer.Manifest{Version: 1, JobID: jobID, Entries: []transfer.Entry{
		{ID: 7, Dst: filepath.Join(w, "run.sh"), Size: int64(len(content)), Mode: 0o755}, // ...but the manifest says 0755 (executable)
	}}
	l := NewLocal(p, "/usr/local/bin/claude-teleport", LocalOptions{ProcRoot: "/proc"})
	if err := m.Save(filepath.Join(l.jobDir(jobID), "manifest.json")); err != nil {
		t.Fatal(err)
	}

	plan := &gitx.Plan{Mode: gitx.ModeExistingMain, SrcMain: "/home/alice/x", SrcWorktree: "/home/alice/x/.worktrees/feat",
		DstMain: main, DstWorktree: w, Linked: true, WorktreeName: "feat", Branch: "feat", Tip: tip, NeedPack: false,
		IndexRel: ".git/worktrees/feat/index", IndexEntryID: gitx.NoEntry, PackEntryID: gitx.NoEntry,
		DirtyEntries: map[string]int{filepath.Join(w, "run.sh"): 7}}
	if err := l.GitAttach(context.Background(), plan, jobID); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(w, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Errorf("run.sh mode = %o, want 0755 (from the manifest entry, not the staged copy's 0644)", st.Mode().Perm())
	}
}

// TestLocalGitAttachRestoresIndexAtManifestEntryZero is the I4 regression:
// manifest ids are 0-based (transfer.Build assigns them by slice index), so
// the index file can legitimately be entry 0. With the old `!= 0` sentinel
// that entry was silently skipped and gitx.Attach attached a worktree
// carrying whatever index git happened to create, with no error anywhere.
func TestLocalGitAttachRestoresIndexAtManifestEntryZero(t *testing.T) {
	p := testPaths(t)
	main := filepath.Join(p.Home, "x")
	os.MkdirAll(main, 0o755)
	gitc(t, main, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(main, "a.txt"), []byte("a"), 0o644)
	gitc(t, main, "add", "a.txt")
	gitc(t, main, "commit", "-q", "-m", "init")
	tip := strings.TrimSpace(gitc(t, main, "rev-parse", "HEAD"))
	// A staged-but-uncommitted file makes this index distinguishable from
	// the one git writes when it creates the linked worktree.
	os.WriteFile(filepath.Join(main, "staged.txt"), []byte("s"), 0o644)
	gitc(t, main, "add", "staged.txt")
	wantIndex, err := os.ReadFile(filepath.Join(main, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}

	jobID := "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"
	staging := job.StagingDir(p.DataDir, jobID)
	os.MkdirAll(staging, 0o700)
	os.WriteFile(filepath.Join(staging, "0"), wantIndex, 0o644) // entry 0: the index

	w := filepath.Join(main, ".worktrees", "feat")
	plan := &gitx.Plan{Mode: gitx.ModeExistingMain, SrcMain: "/home/alice/x", SrcWorktree: "/home/alice/x/.worktrees/feat",
		DstMain: main, DstWorktree: w, Linked: true, WorktreeName: "feat", Branch: "feat", Tip: tip, NeedPack: false,
		IndexRel: ".git/worktrees/feat/index", IndexEntryID: 0, PackEntryID: gitx.NoEntry}
	l := NewLocal(p, "/usr/local/bin/claude-teleport", LocalOptions{ProcRoot: "/proc"})
	if err := l.GitAttach(context.Background(), plan, jobID); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(main, ".git", "worktrees", "feat", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wantIndex) {
		t.Errorf("worktree index (%d bytes) is not the transferred one (%d bytes): manifest entry 0 was skipped", len(got), len(wantIndex))
	}
	if !strings.Contains(gitc(t, w, "status", "--porcelain"), "staged.txt") {
		t.Errorf("git in %s does not see the staged file the restored index records", w)
	}
}
