package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "a.txt"},
		{"commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=alice", "GIT_AUTHOR_EMAIL=a@example",
			"GIT_COMMITTER_NAME=alice", "GIT_COMMITTER_EMAIL=a@example",
			"GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// inspect reports on the repository a teleport would move, which is the
// one at the session's PROJECT cwd (see session.ProjectCwd). Inspecting
// the LAUNCH cwd of a session that changed directory looks at a directory
// the teleport does not touch -- and when that directory is not a
// repository, inspect announces "not a git repository" for a session whose
// work is a clean git worktree, inverting the answer entirely.
func TestInspectUsesTheProjectCwdForGit(t *testing.T) {
	home := t.TempDir()
	const sid = "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"
	launch := filepath.Join(home, "launched-here") // plain directory
	work := filepath.Join(home, "works-here")      // the git repository
	for _, d := range []string{launch, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInit(t, work)

	proj := filepath.Join(home, ".claude", "projects", session.Munge(work))
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	var body string
	for _, cwd := range []string{launch, work} {
		body += `{"type":"user","sessionId":"` + sid + `","cwd":"` + cwd +
			`","gitBranch":"main","version":"2.1.247","timestamp":"2026-09-17T10:00:00Z",` +
			`"message":{"role":"user","content":"hi"}}` + "\n"
	}
	if err := os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := run(t, []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}, "inspect", sid)
	if code != ExitOK {
		t.Fatalf("inspect = %d: %s", code, stderr)
	}
	if strings.Contains(stdout, "not a git repository") {
		t.Errorf("inspect called the session's work directory a non-repository:\n%s", stdout)
	}
	if !strings.Contains(stdout, work) {
		t.Errorf("inspect does not report on %q:\n%s", work, stdout)
	}
}
