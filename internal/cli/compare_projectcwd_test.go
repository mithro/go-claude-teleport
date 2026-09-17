package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// compare-config --session must read the same cwd a teleport's preflight
// reads, or it reports drift for a different session than the one being
// moved. That cwd is the PROJECT cwd (session.ProjectCwd): the project
// entry it selects supplies the project-scoped MCP servers and the allowed
// tools that the comparison is made of, so a session that changed
// directory was compared against the entry of the directory it left --
// typically no entry at all, which silently drops every project-scoped row.
func TestCompareConfigSessionUsesTheProjectCwd(t *testing.T) {
	const sid = "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"
	home := t.TempDir()
	launch := filepath.Join(home, "launched-here")
	work := filepath.Join(home, "works-here")

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
	// Only the work cwd has a project entry -- reading the launch cwd
	// finds nothing and the project row never appears.
	if err := os.WriteFile(filepath.Join(home, ".claude.json"),
		[]byte(`{"projects":{"`+work+`":{"allowedTools":[],"hasTrustDialogAccepted":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "dstcfg")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, ".claude.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	stubClaudeVersion(t, "2.1.247")
	_, out, stderr := run(t, []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")},
		"compare-config", dst, "--session", sid)
	if !strings.Contains(out, "project") {
		t.Errorf("no project row: the source's project entry was not found at the work cwd\nstdout:\n%s\nstderr:\n%s", out, stderr)
	}
}
