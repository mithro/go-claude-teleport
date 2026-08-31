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

// addWorktreeRelative is addWorktree with git's relative worktree links.
// The two link files are written by hand rather than with
// worktree.useRelativePaths=true, byte for byte what that config produces
// (verified against git 2.55): the config also records the
// extensions.relativeworktrees repository extension, which go-git refuses
// to open, and the parsing under test is the point here.
func addWorktreeRelative(t *testing.T, mainDir, name string) string {
	t.Helper()
	w := addWorktree(t, mainDir, name)
	gd := filepath.Join(mainDir, ".git", "worktrees", name)
	rel, err := filepath.Rel(gd, filepath.Join(w, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(gd, "gitdir"), rel+"\n")
	relBack, err := filepath.Rel(w, gd)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(w, ".git"), "gitdir: "+relBack+"\n")
	return w
}

// commitAllCLI stages everything with the real git (which handles symlinks,
// mode bits and deletions exactly as the fixtures need) and returns the new
// commit hash.
func commitAllCLI(t *testing.T, dir, msg string) string {
	t.Helper()
	gitCLI(t, dir, "add", "-A")
	gitCLI(t, dir, "commit", "-q", "--allow-empty", "-m", msg)
	return strings.TrimSpace(gitCLI(t, dir, "rev-parse", "HEAD"))
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
