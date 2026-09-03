package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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

// TestBareInvocationPointsToHelp: the literal zero-argument invocation
// (`claude-teleport`, nothing else) is the one usage mistake that doesn't
// itself name a flag to go look up — every other case in
// TestTeleportFlagValidation at least mentions --state, --map, etc. — so
// it alone gets a pointer to --help appended (root.go's bare-detection,
// cli.go's helpPointer). A real usage mistake like passing both --to and
// --from is not "lost" the same way, so it must not carry the same hint.
func TestBareInvocationPointsToHelp(t *testing.T) {
	code, _, stderr := run(t, []string{"HOME=/home/alice"})
	if code != ExitUsage || !strings.Contains(stderr, "exactly one of") || !strings.Contains(stderr, "--help") {
		t.Fatalf("bare invocation: exit %d stderr %q", code, stderr)
	}
	code2, _, stderr2 := run(t, []string{"HOME=/home/alice"}, "--to", "a", "--from", "b")
	if code2 != ExitUsage || strings.Contains(stderr2, "--help") {
		t.Fatalf("--to and --from together: exit %d stderr %q (must not carry the bare-invocation hint)", code2, stderr2)
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

// TestSubcommandHelpNeverEmpty is R-P3-28b: root.go's SetHelpTemplate
// ("{{.Long}}\n") is inherited by every child command (cobra walks up to
// the nearest ancestor with a template set), so any command without its
// own Long — continue, status, abandon, list, doctor, version among the
// public ones — printed nothing at all for `<cmd> --help`. This walks the
// real command tree (every command registered under root, including the
// hidden internal ones) and asserts `--help` on each one is never empty:
// it must show the command's own name (from Use) and, for a command that
// defines its own flags, every one of those flag names.
func TestSubcommandHelpNeverEmpty(t *testing.T) {
	a := &app{env: parseEnv(nil)}
	root := a.rootCmd()

	type target struct {
		path      []string
		wantToken string
		wantFlags []string
	}
	var targets []target
	var walk func(cmd *cobra.Command, path []string)
	walk = func(cmd *cobra.Command, path []string) {
		if cmd.Name() == "help" { // cobra's own builtin "help" command
			return
		}
		var flags []string
		cmd.LocalFlags().VisitAll(func(f *pflag.Flag) { flags = append(flags, "--"+f.Name) })
		targets = append(targets, target{path: path, wantToken: strings.Fields(cmd.Use)[0], wantFlags: flags})
		for _, sub := range cmd.Commands() {
			walk(sub, append(append([]string{}, path...), sub.Name()))
		}
	}
	for _, sub := range root.Commands() {
		walk(sub, []string{sub.Name()})
	}
	if len(targets) < 10 {
		t.Fatalf("only found %d commands under root; tree walk is broken", len(targets))
	}

	for _, tgt := range targets {
		args := append(append([]string{}, tgt.path...), "--help")
		code, out, stderr := run(t, nil, args...)
		if code != ExitOK {
			t.Errorf("%v: exit %d, stderr %q", args, code, stderr)
			continue
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("%v: --help printed nothing", args)
			continue
		}
		if !strings.Contains(out, tgt.wantToken) {
			t.Errorf("%v: --help missing Use token %q:\n%s", args, tgt.wantToken, out)
		}
		for _, fn := range tgt.wantFlags {
			if !strings.Contains(out, fn) {
				t.Errorf("%v: --help missing flag %q:\n%s", args, fn, out)
			}
		}
	}
}
