package cli

import (
	"strings"
	"testing"
)

func TestTeleportFlagsParseButTransportIsStubbed(t *testing.T) {
	code, _, stderr := run(t, []string{"HOME=/home/alice"}, "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13", "--to", "big-storage.example",
		"--via", "jump.example", "-o", "User=alice", "--dest-path", "/srv/w", "--map", "/home/alice=/home/bob",
		"--state", "idle", "--allow-config-drift", "--force", "--tmux-socket", "main", "--exclude", "*.log",
		"--dry-run", "--exit-timeout", "10s", "--start-timeout", "1m", "--log", "/tmp/x.log", "-v")
	if code != ExitUsage || !strings.Contains(stderr, "transport not implemented yet") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
	// canonical spellings and --from
	code, _, stderr = run(t, []string{"HOME=/home/alice"}, "--teleport-from", "laptop.example")
	if code != ExitUsage || !strings.Contains(stderr, "transport not implemented yet") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func TestTeleportFlagValidation(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--to", "a", "--from", "b"}, "exactly one of"},
		{[]string{"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"}, "exactly one of"},
		{[]string{"--to", "a", "--state", "bogus"}, "--state"},
		{[]string{"--to", "a", "--map", "nope"}, "--map"},
		{[]string{"--to", "a", "--no-tmux", "--state", "running"}, "--no-tmux"},
		{[]string{"--to", "a", "-v", "-q"}, "--verbose and --quiet"},
		{[]string{"--to", "a", "x", "y", "z"}, "too many"},
	}
	for _, c := range cases {
		code, _, stderr := run(t, []string{"HOME=/home/alice"}, c.args...)
		if code != ExitUsage || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: exit %d stderr %q (want %q)", c.args, code, stderr, c.want)
		}
	}
}

func TestHelpDocumentsEverything(t *testing.T) {
	code, out, _ := run(t, nil, "--help")
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	for _, w := range []string{
		"--teleport-to", "--teleport-from", "--to", "--from", "--via", "-o KEY=VALUE", "--dest-path", "--map", "--state",
		"--allow-config-drift", "--force", "--tmux-socket", "--no-tmux", "--exclude", "--dry-run", "--exit-timeout",
		"--start-timeout", "--config-dir", "--log", "--json", "--verbose", "--quiet",
		"continue <sid>", "status", "abandon", "inspect", "list", "compare-config", "doctor", "placeholder", "version",
		"CLAUDE_CODE_SESSION_ID", "Exit codes", "claude --teleport",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("help lacks %q", w)
		}
	}
	if strings.Contains(out, "Plan 02") || strings.Contains(out, "Plan 03") {
		t.Error("help must not mention implementation plans")
	}
}

func TestInspectFixture(t *testing.T) {
	env := []string{"HOME=/home/alice", "CLAUDE_CONFIG_DIR=../session/testdata/config"}
	code, out, stderr := run(t, env, "inspect", "3f9c")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, w := range []string{"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13", "idle", "/home/alice/github/example/widget", "feature/teleport",
		"2.1.247", "tool-results/toolu_01Ab3.txt", "mcp: filesystem, playwright", "skills: superpowers:test-driven-development",
		"memory/MEMORY.md", "skipped", ".lock"} {
		if !strings.Contains(out, w) {
			t.Errorf("inspect lacks %q:\n%s", w, out)
		}
	}
	code, out, _ = run(t, env, "inspect", "--json", "3f9c")
	if code != ExitOK || !strings.HasPrefix(out, "{") || !strings.Contains(out, `"launch_cwd": "/home/alice/github/example/widget"`) {
		t.Fatalf("json: %d %s", code, out)
	}
	if code, _, stderr := run(t, env, "inspect", "zzzz"); code != ExitRefused || !strings.Contains(stderr, "session not found") {
		t.Fatalf("not found: %d %q", code, stderr)
	}
}
