package remote

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/session"
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

// TestClaudeEnvConfigDirPresence is T26-2's second half. Real Claude Code
// picks its global config file by whether CLAUDE_CONFIG_DIR is present in
// its environment at all (task-26-report.md finding 2), so a claude this
// host starts must see the variable exactly when session.Paths says the
// variable is what put ConfigDir where it is — otherwise the destination's
// project entry is merged into $HOME/.claude.json while the Claude we then
// start reads $HOME/.claude/.claude.json, and the trust-dialog/mcp state
// the transfer carried over is silently invisible to it.
func TestClaudeEnvConfigDirPresence(t *testing.T) {
	has := func(env []string, key string) (string, bool) {
		v, ok := "", false
		for _, e := range env { // last wins, as os/exec's dedupEnv does
			if strings.HasPrefix(e, key+"=") {
				v, ok = strings.TrimPrefix(e, key+"="), true
			}
		}
		return v, ok
	}
	base := []string{"PATH=/bin", "CLAUDE_CONFIG_DIR=/inherited/cfg", "TERM=dumb"}

	// The environment is where the config dir came from: pass it on.
	p := session.Paths{Home: "/home/alice", ConfigDir: "/home/alice/.claude", ConfigDirFromEnv: true}
	if v, ok := has(claudeEnv(base, p), "CLAUDE_CONFIG_DIR"); !ok || v != "/home/alice/.claude" {
		t.Errorf("from-env config dir: CLAUDE_CONFIG_DIR = %q ok=%v, want the resolved dir", v, ok)
	}
	// Nobody set it: claude must not see it either, not even the value
	// this process happens to have inherited.
	p = session.Paths{Home: "/home/alice", ConfigDir: "/home/alice/.claude"}
	if v, ok := has(claudeEnv(base, p), "CLAUDE_CONFIG_DIR"); ok {
		t.Errorf("default config dir: CLAUDE_CONFIG_DIR = %q, want it absent so claude uses $HOME/.claude.json", v)
	}
	// A config dir that is NOT the default cannot be communicated any
	// other way, so it is still exported.
	p = session.Paths{Home: "/home/alice", ConfigDir: "/srv/cfg"}
	if v, ok := has(claudeEnv(base, p), "CLAUDE_CONFIG_DIR"); !ok || v != "/srv/cfg" {
		t.Errorf("non-default config dir: CLAUDE_CONFIG_DIR = %q ok=%v, want /srv/cfg", v, ok)
	}
	// HOME always wins over the inherited one, in every case.
	if v, ok := has(claudeEnv(base, p), "HOME"); !ok || v != "/home/alice" {
		t.Errorf("HOME = %q ok=%v", v, ok)
	}
}
