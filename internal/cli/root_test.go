package cli

import (
	"strings"
	"testing"
	"time"
)

// TestTeleportOptionsFromFlags replaces the old
// TestTeleportFlagsParseButTransportIsStubbed (Plan 01's transport stub is
// gone as of Task 21): it checks every flag reaches orchestrate.Options
// correctly, at the a.teleportOptions unit level rather than through
// Main()'s full dial path — dialing "big-storage.example"/"jump.example"
// for real would make this test network-dependent (see
// TestTeleportE2E-style tests elsewhere for the dial path itself, exercised
// against in-process fixtures via Options.LocalDest).
func TestTeleportOptionsFromFlags(t *testing.T) {
	a := &app{env: parseEnv([]string{"HOME=/home/alice"})}
	tf := teleportFlags{
		To: "big-storage.example", Via: []string{"jump.example"}, SSHOptions: []string{"User=alice"},
		DestPath: "/srv/w", Maps: []string{"/home/alice=/home/bob"}, State: "idle", AllowDrift: true, Force: true,
		TmuxSocket: "main", Excludes: []string{"*.log"}, IncludeIgnored: true,
		ExitTimeout: 10 * time.Second, StartTimeout: time.Minute,
	}
	o, err := a.teleportOptions(tf, []string{"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Direction != "to" || o.Target != "big-storage.example" || len(o.Via) != 1 || o.Via[0] != "jump.example" {
		t.Errorf("direction/target/via = %+v", o)
	}
	if o.SSHOptions["User"] != "alice" {
		t.Errorf("ssh options = %+v", o.SSHOptions)
	}
	if o.DestPath != "/srv/w" || o.State != "idle" || !o.AllowDrift || !o.Force || o.TmuxSocket != "main" {
		t.Errorf("options = %+v", o)
	}
	if len(o.Maps) != 1 || o.Maps[0].From != "/home/alice" || o.Maps[0].To != "/home/bob" {
		t.Errorf("maps = %+v", o.Maps)
	}
	if len(o.Excludes) != 1 || o.Excludes[0] != "*.log" || !o.IncludeIgnored {
		t.Errorf("excludes/include-ignored = %+v", o)
	}
	if o.ExitTimeout != 10*time.Second || o.StartTimeout != time.Minute {
		t.Errorf("timeouts = %+v", o)
	}
	if string(o.Selector.ID) != "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13" {
		t.Errorf("selector = %+v", o.Selector)
	}

	// --from flips Direction/Target (canonical spellings/--to alias are
	// cobra flag parsing, exercised by TestTeleportFlagValidation using
	// --to directly, and by TestHelpDocumentsEverything for --teleport-*).
	o2, err := a.teleportOptions(teleportFlags{From: "laptop.example", State: "auto"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if o2.Direction != "from" || o2.Target != "laptop.example" {
		t.Errorf("from direction = %+v", o2)
	}
}

func TestTeleportOptionsRejectsRelativeDestPath(t *testing.T) {
	a := &app{env: parseEnv([]string{"HOME=/home/alice"})}
	tf := teleportFlags{To: "a.example", State: "auto", DestPath: "relative/path"}
	if _, err := a.teleportOptions(tf, nil); err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Errorf("err = %v, want an absolute-path error", err)
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
		"--allow-config-drift", "--force", "--tmux-socket", "--no-tmux", "--exclude", "--include-ignored", "--dry-run", "--exit-timeout",
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
