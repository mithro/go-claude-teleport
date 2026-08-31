package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/mithro/go-claude-teleport/internal/session"
)

func pathMap(from, to string) session.PathMap {
	return session.NewPathMap(session.Mapping{From: from, To: to})
}

// gitCLI runs the real git binary inside dir. Tests only: the tool itself
// never execs git. Skips the test when git is not installed.
func gitCLI(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=alice", "GIT_AUTHOR_EMAIL=alice@laptop.example",
		"GIT_COMMITTER_NAME=alice", "GIT_COMMITTER_EMAIL=alice@laptop.example",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

var sig = &object.Signature{Name: "alice", Email: "alice@laptop.example", When: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)}

// initRepo creates a repository at dir with one commit ("README.md") and
// returns it plus the root commit hash.
func initRepo(t *testing.T, dir string) (*git.Repository, string) {
	t.Helper()
	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{InitOptions: git.InitOptions{DefaultBranch: "refs/heads/main"}})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "README.md"), "hello\n")
	h := commitAll(t, repo, "initial")
	return repo, h
}

func commitAll(t *testing.T, repo *git.Repository, msg string) string {
	t.Helper()
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	h, err := wt.Commit(msg, &git.CommitOptions{Author: sig, Committer: sig, AllowEmptyCommits: true})
	if err != nil {
		t.Fatal(err)
	}
	return h.String()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// addWorktree uses the git CLI to create a linked worktree of main at
// <main>/.worktrees/<name> on a new branch <name>; returns its path.
func addWorktree(t *testing.T, mainDir, name string) string {
	t.Helper()
	w := filepath.Join(mainDir, ".worktrees", name)
	gitCLI(t, mainDir, "worktree", "add", "-b", name, w)
	return w
}

// porcelain returns `git status --porcelain` lines sorted by git.
func porcelain(t *testing.T, dir string) []string {
	t.Helper()
	out := strings.TrimRight(gitCLI(t, dir, "status", "--porcelain"), "\n")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}
