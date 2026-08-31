package cli

import (
	"strings"
	"testing"
)

func TestListFixture(t *testing.T) {
	env := []string{"HOME=/home/alice", "CLAUDE_CONFIG_DIR=../session/testdata/config"}
	code, out, stderr := run(t, env, "list")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	// the fixture registry pids are not alive on this machine: both sessions are idle
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "ID") {
		t.Fatalf("%q", out)
	}
	for _, w := range []string{"3f9c2b7e  idle", "a1b2c3d4  idle", "feature/teleport", "2026-08-27T11:00:05.000Z"} {
		if !strings.Contains(out, w) {
			t.Errorf("list lacks %q:\n%s", w, out)
		}
	}
	code, out, _ = run(t, env, "list", "--json")
	if code != ExitOK || !strings.Contains(out, `"state": "idle"`) {
		t.Fatalf("json: %d %s", code, out)
	}
	if code, _, stderr := run(t, env, "list", "--host", "big-storage.example"); code != ExitUsage || !strings.Contains(stderr, "not implemented yet") {
		t.Fatalf("--host: %d %q", code, stderr)
	}
}
