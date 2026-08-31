package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stubClaudeVersion(t *testing.T, v string) {
	t.Helper()
	old := claudeVersionFn
	claudeVersionFn = func([]string) (string, error) { return v, nil }
	t.Cleanup(func() { claudeVersionFn = old })
}

func TestCompareConfigLocalDirs(t *testing.T) {
	stubClaudeVersion(t, "2.1.247")
	dst, _ := filepath.Abs("../claudecfg/testdata/dst")
	env := []string{"HOME=/home/alice", "CLAUDE_CONFIG_DIR=../claudecfg/testdata/src", "PWD=/home/alice/github/example/widget"}
	code, out, stderr := run(t, env, "compare-config", dst)
	if code != ExitRefused {
		t.Fatalf("blocking drift must exit 3: %d %s %s", code, out, stderr)
	}
	for _, w := range []string{"block  hooks", "block  mcp.playwright", "block  plugin.superpowers@claude-plugins-official", "warn   model", "info   project"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q:\n%s", w, out)
		}
	}
	code, out, _ = run(t, env, "compare-config", "--allow-config-drift", dst)
	if code != ExitOK || strings.Contains(out, "block ") {
		t.Fatalf("downgraded: %d\n%s", code, out)
	}
	code, out, _ = run(t, env, "compare-config", "--json", dst)
	if code != ExitRefused || !strings.Contains(out, `"blocking": true`) {
		t.Fatalf("json: %d %s", code, out)
	}
}

func TestCompareConfigWithSessionUsesUsage(t *testing.T) {
	stubClaudeVersion(t, "2.1.247")
	dst, _ := filepath.Abs("../claudecfg/testdata/dst")
	// the session fixture's cwd is the same project; its usage names playwright, filesystem, superpowers
	env := []string{"HOME=/home/alice", "CLAUDE_CONFIG_DIR=../session/testdata/config"}
	code, out, _ := run(t, env, "compare-config", "--session", "3f9c", dst)
	if code != ExitRefused || !strings.Contains(out, "block  mcp.playwright") {
		t.Fatalf("%d\n%s", code, out)
	}
	if strings.Contains(out, "block  mcp.unused") {
		t.Fatal("unused servers must not block when a session is given")
	}
}

// TestCompareConfigDestHomeNormalLayout covers the fix for the review
// finding that --dest-home was inert: a normal (non-CLAUDE_CONFIG_DIR)
// install keeps ~/.claude.json next to ~/.claude, not inside it, so the
// destination inventory must be read from <dest-home>/.claude.json rather
// than always from <target-config-dir>/.claude.json.
func TestCompareConfigDestHomeNormalLayout(t *testing.T) {
	stubClaudeVersion(t, "2.1.247")
	srcHome, dstHome := t.TempDir(), t.TempDir()
	mcp := `{"mcpServers": {"playwright": {"type": "stdio", "command": "npx", "args": ["@playwright/mcp@latest"]}}}`
	if err := os.WriteFile(filepath.Join(srcHome, ".claude.json"), []byte(mcp), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstHome, ".claude.json"), []byte(mcp), 0o644); err != nil {
		t.Fatal(err)
	}
	dstConfigDir := filepath.Join(dstHome, ".claude")
	if err := os.MkdirAll(dstConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + srcHome, "PWD=/home/alice/github/example/widget"}
	code, out, stderr := run(t, env, "compare-config", "--dest-home", dstHome, dstConfigDir)
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, stderr)
	}
	if strings.Contains(out, "mcp.") {
		t.Fatalf("identical mcpServers must produce no mcp drift:\n%s", out)
	}
}

// TestCompareConfigHostnameDialsRemote covers the transition from Plan 01's
// "not implemented yet" stub to the real Plan 02 remote transport: a
// hostname target is now dialled over ssh (and fails unreachable here,
// since there is nothing listening) rather than rejected as a usage error.
// internal/cli/remotecfg_test.go covers the live remote path end to end.
func TestCompareConfigHostnameDialsRemote(t *testing.T) {
	code, _, stderr := run(t, []string{"HOME=/home/alice", "USER=alice"}, "compare-config", "big-storage.example", "-o", "StrictHostKeyChecking=no", "-o", "ConnectTimeout=1")
	if code != ExitUnreachable {
		t.Fatalf("%d %q", code, stderr)
	}
	if strings.Contains(stderr, "not implemented yet") {
		t.Fatalf("hostname targets must dial the remote transport, not the old stub: %q", stderr)
	}
}

// TestCompareConfigAbsolutePathStatErrorSurfaces covers the classification
// bug where an absolute target that errors on Stat (as opposed to simply
// not existing) was silently reinterpreted as a remote hostname instead of
// surfacing the error.
func TestCompareConfigAbsolutePathStatErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	loop := filepath.Join(dir, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := run(t, []string{"HOME=/home/alice"}, "compare-config", loop)
	if code == ExitUsage && strings.Contains(stderr, "not implemented yet") {
		t.Fatalf("Stat error must not be reinterpreted as a remote hostname: %d\nstdout:%s\nstderr:%s", code, out, stderr)
	}
	if code != ExitFailed || !strings.Contains(stderr, "stat") {
		t.Fatalf("%d %q", code, stderr)
	}
}
