package cli

import (
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

func TestCompareConfigRemoteNotYet(t *testing.T) {
	if code, _, stderr := run(t, []string{"HOME=/home/alice"}, "compare-config", "big-storage.example"); code != ExitUsage || !strings.Contains(stderr, "not implemented yet") {
		t.Fatalf("%d %q", code, stderr)
	}
}
