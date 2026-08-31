package remote

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildFakeClaude compiles test/fakeclaude into a temp dir and prepends it
// to PATH as `claude`. Shared by the orchestrator tests (Task 22).
func buildFakeClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/mithro/go-claude-teleport/test/fakeclaude")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakeclaude: %v\n%s", err, out)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin
}

func TestRunPtyResumeConfirmsAndExits(t *testing.T) {
	buildFakeClaude(t)
	p := testPaths(t)
	t.Setenv("CLAUDE_CONFIG_DIR", p.ConfigDir)
	cwd := filepath.Join(p.Home, "x")
	os.MkdirAll(cwd, 0o755)
	// Seed a transcript so --resume finds the session.
	proj := p.ProjectDir(cwd)
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(`{"type":"user","sessionId":"`+sid+`","cwd":"`+cwd+`","timestamp":"2026-08-27T10:00:00Z","message":{"role":"user","content":"hi"}}`+"\n"), 0o600)
	l := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := l.RunPtyResume(ctx, sid, cwd, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	// fakeclaude removes its registry file on clean exit.
	entries, _ := os.ReadDir(p.SessionsDir())
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			t.Errorf("registry entry %s left behind", e.Name())
		}
	}
}

func TestRunPtyResumeReportsLoggedOut(t *testing.T) {
	buildFakeClaude(t)
	p := testPaths(t)
	t.Setenv("CLAUDE_CONFIG_DIR", p.ConfigDir)
	t.Setenv("FAKECLAUDE_FAIL", "not-logged-in")
	cwd := filepath.Join(p.Home, "x")
	os.MkdirAll(cwd, 0o755)
	l := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc"})
	err := l.RunPtyResume(context.Background(), sid, cwd, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("err = %v, want the marker", err)
	}
}
