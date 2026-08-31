package remote

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
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
		IndexRel: ".git/worktrees/feat/index", IndexEntryID: 8,
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
