package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// list's CWD column is how a person picks a session out of a long list, so
// it has to name the directory the session actually works in -- the
// PROJECT cwd (session.ProjectCwd), the same one inspect, compare-config,
// the plan summary and the placeholder were moved to.
//
// Real case (2026-09-18, ten64): ten sessions were all launched from
// /home/tim/github/fpgas-online, a container of sibling repositories, and
// two of them then worked in linked worktrees three levels below it.
// `list` printed the identical launch cwd for all ten, so the two that
// carry a git repository were indistinguishable from the eight that do
// not -- which is exactly the distinction that decides how they must be
// teleported.
func TestListReportsTheProjectCwdNotTheLaunchCwd(t *testing.T) {
	home := t.TempDir()
	const sid = "7c1b4e2a-3d5f-4a6b-8c9d-0e1f2a3b4c5d"
	launch := filepath.Join(home, "launched-here")
	work := filepath.Join(home, "works-here")
	for _, d := range []string{launch, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// The transcript lives under the project directory named after the
	// WORK cwd, which is what makes that cwd the project one.
	proj := filepath.Join(home, ".claude", "projects", session.Munge(work))
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	var body string
	for _, cwd := range []string{launch, work} {
		body += `{"type":"user","sessionId":"` + sid + `","cwd":"` + cwd +
			`","gitBranch":"main","version":"2.1.247","timestamp":"2026-09-18T10:00:00Z",` +
			`"message":{"role":"user","content":"hi"}}` + "\n"
	}
	if err := os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}

	code, stdout, stderr := run(t, env, "list")
	if code != ExitOK {
		t.Fatalf("list = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, work) {
		t.Errorf("list does not name the project cwd %q:\n%s", work, stdout)
	}
	if strings.Contains(stdout, launch) {
		t.Errorf("list names the launch cwd %q, which the teleport does not touch:\n%s", launch, stdout)
	}

	code, stdout, stderr = run(t, env, "list", "--json")
	if code != ExitOK {
		t.Fatalf("list --json = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, `"cwd": "`+work+`"`) {
		t.Errorf("list --json cwd is not the project cwd %q:\n%s", work, stdout)
	}
}
