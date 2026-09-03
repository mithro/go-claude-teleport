package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/test/fakeclaude/harness"
)

func TestDoctorPassesWithFakeClaude(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, ".claude")
	os.MkdirAll(filepath.Join(cfg, "projects"), 0o700)
	// SSH_AUTH_SOCK is deliberately left UNSET: ruling R-P3-23g requires a
	// missing agent to be a warning only, never a reason doctor fails —
	// this is the direct regression test for that (exit must still be OK).
	env := harness.Env(t, root, cfg, "XDG_DATA_HOME="+filepath.Join(root, "data"))
	code, out, stderr := run(t, env, "doctor")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s%s", code, out, stderr)
	}
	for _, w := range []string{"ok    claude on PATH", "2.1.247", "ok    config dir", "ok    data dir writable", "ok    tmux servers", "ok    SSH_AUTH_SOCK", "fine if every remote uses -o IdentityFile"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q:\n%s", w, out)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "data", "claude-teleport")); err != nil {
		t.Fatal("doctor must create the data dir")
	}
}

func TestDoctorFailsWithoutClaude(t *testing.T) {
	root := t.TempDir()
	code, out, _ := run(t, []string{"HOME=" + root, "PATH=" + t.TempDir()}, "doctor")
	if code != ExitFailed || !strings.Contains(out, "FAIL  claude on PATH") || !strings.Contains(out, "FAIL  config dir") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	// ruling R-P3-23c: tmux servers and SSH_AUTH_SOCK are brief-mandated
	// local checks. SSH_AUTH_SOCK is genuinely unset in this env, but per
	// ruling R-P3-23g that must show as "ok" with an explanatory note —
	// never FAIL — even while other checks (claude, config dir) do fail.
	if !strings.Contains(out, "tmux servers") || !strings.Contains(out, "ok    SSH_AUTH_SOCK") || strings.Contains(out, "FAIL  SSH_AUTH_SOCK") {
		t.Fatalf("doctor output missing the tmux-servers check, or SSH_AUTH_SOCK is not a warning:\n%s", out)
	}
	// big-storage.example (IANA reserved, guaranteed not to resolve/accept a
	// connection) also has no ssh key/agent available here, so the dial
	// itself fails before any network I/O — deterministic exit 4.
	if code, _, stderr := run(t, []string{"HOME=" + root}, "doctor", "big-storage.example"); code != ExitUnreachable {
		t.Fatalf("remote unreachable: %d %q", code, stderr)
	}
}

// TestDoctorRemoteHostReportsChecks drives `doctor <host>` against a real
// (loopback) remote over the sshtest harness: local checks pass (fake
// claude on PATH), then the remote branch dials, prints its own checks
// (claude-teleport version parity, remote claude, tmux, sessions listable)
// and the command succeeds when nothing fails.
func TestDoctorRemoteHostReportsChecks(t *testing.T) {
	claudeDir := harness.Build(t)
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", claudeDir+string(os.PathListSeparator)+oldPath)

	remoteEnv, _ := testEnv(t)
	target, opts, localHome := remoteHost(t, remoteEnv)
	cfg := filepath.Join(localHome, ".claude")
	os.MkdirAll(filepath.Join(cfg, "projects"), 0o700)
	localEnv := []string{"HOME=" + localHome, "USER=alice", "CLAUDE_CONFIG_DIR=" + cfg, "PATH=" + os.Getenv("PATH"), "SSH_AUTH_SOCK=/nonexistent/agent.sock"}

	args := append([]string{"doctor", target}, opts...)
	code, out, stderr := run(t, localEnv, args...)
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, stderr)
	}
	for _, want := range []string{"remote claude-teleport", "remote claude", "remote tmux", "remote sessions"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor %s output missing %q:\n%s", target, want, out)
		}
	}
}

// TestRemoteChecksSurfacesClaudeVersionErrAndListSessionsError is folded
// minor M2: hi.ClaudeVersionErr and a ListSessions failure must be
// surfaced in the check detail, not swallowed into a bare pass/fail.
func TestRemoteChecksSurfacesClaudeVersionErrAndListSessionsError(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A "claude" that exists (HasClaude=true) but fails --version.
	claudePath := filepath.Join(binDir, "claude")
	if err := os.WriteFile(claudePath, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)

	home := filepath.Join(dir, "home", "bob")
	cfg := filepath.Join(home, ".claude")
	if err := os.MkdirAll(filepath.Join(cfg, "projects", "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	// projects/ itself unreadable so ListSessions' os.ReadDir fails with
	// something other than "not exist".
	if err := os.Chmod(filepath.Join(cfg, "projects"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(cfg, "projects"), 0o700) })

	paths := session.Paths{Home: home, ConfigDir: cfg, GlobalJSON: filepath.Join(home, ".claude.json"), DataDir: filepath.Join(home, ".local", "share", "claude-teleport"), ProcRoot: "/proc"}
	ep := remote.NewLocal(paths, "x", remote.LocalOptions{ProcRoot: "/proc"})

	cs, _, err := remoteChecks(context.Background(), ep, "bob@dest.example")
	if err != nil {
		t.Fatal(err)
	}
	var claudeDetail, sessDetail string
	var claudeOK, sessOK bool
	for _, c := range cs {
		switch c.name {
		case "remote claude":
			claudeDetail, claudeOK = c.detail, c.ok
		case "remote sessions":
			sessDetail, sessOK = c.detail, c.ok
		}
	}
	if claudeOK || !strings.Contains(claudeDetail, "version failed") {
		t.Errorf("remote claude check = %q ok=%v, want the --version failure surfaced", claudeDetail, claudeOK)
	}
	if sessOK || sessDetail == "" {
		t.Errorf("remote sessions check = %q ok=%v, want the ListSessions error surfaced, not swallowed", sessDetail, sessOK)
	}
}

// claudeDetailFor drives remoteChecks against a fresh remote.Local rooted at
// home and returns the "remote claude" row's detail text — shared by the
// two HK-3 display tests below.
func claudeDetailFor(t *testing.T, home string) string {
	t.Helper()
	cfg := filepath.Join(home, ".claude")
	if err := os.MkdirAll(filepath.Join(cfg, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	paths := session.Paths{Home: home, ConfigDir: cfg, GlobalJSON: filepath.Join(home, ".claude.json"), DataDir: filepath.Join(home, ".local", "share", "claude-teleport"), ProcRoot: "/proc"}
	ep := remote.NewLocal(paths, "x", remote.LocalOptions{ProcRoot: "/proc"})
	cs, _, err := remoteChecks(context.Background(), ep, "bob@dest.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.name == "remote claude" {
			return c.detail
		}
	}
	t.Fatal("no \"remote claude\" row")
	return ""
}

// TestRemoteChecksReportsSearchLocationsWhenClaudeIsNowhere pins HK-3's
// doctor display: when claude isn't found at all (not on PATH, not under
// any fallback resolveExe checks), the row must name every location tried
// rather than showing a blank detail.
func TestRemoteChecksReportsSearchLocationsWhenClaudeIsNowhere(t *testing.T) {
	home := t.TempDir()
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty")) // no claude anywhere PATH reaches
	defer os.Setenv("PATH", oldPath)

	got := claudeDetailFor(t, home)
	want := "not found (tried " + remote.ClaudeSearchLocations() + ")"
	if got != want {
		t.Errorf("remote claude detail = %q, want %q", got, want)
	}
}

// TestRemoteChecksReportsClaudePathWhenFoundViaFallback pins HK-3's other
// half: a claude found only via resolveExe's $HOME/.local/bin fallback
// (PATH excludes it) must render its resolved path in parentheses.
func TestRemoteChecksReportsClaudePathWhenFoundViaFallback(t *testing.T) {
	home := t.TempDir()
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	claudePath := filepath.Join(localBin, "claude")
	if err := os.WriteFile(claudePath, []byte("#!/bin/sh\necho 2.1.999\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", "/usr/bin:/bin") // deliberately excludes localBin
	defer os.Setenv("PATH", oldPath)

	got := claudeDetailFor(t, home)
	if !strings.Contains(got, "("+claudePath+")") {
		t.Errorf("remote claude detail = %q, want it to end with (%s)", got, claudePath)
	}
}

// TestDoctorRemoteVersionMismatchIsUnreachable pins A12: `doctor <host>`
// printed FAIL for a peer running a different claude-teleport and exited
// 1 ("doctor found problems"), but spec §5 reserves exit 4 for a version
// mismatch — the same code preflight and compare-config return for that
// very peer, and the one a caller keys "you cannot work with this host"
// off.
func TestDoctorRemoteVersionMismatchIsUnreachable(t *testing.T) {
	remoteEnv, _ := testEnv(t)
	target, opts, localHome := remoteHostExec(t, func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
		p, err := envPaths(remoteEnv)
		if err != nil {
			io.WriteString(stderr, err.Error())
			return 1
		}
		lopts, closeProbe := serverLocalOptions(context.Background(), remoteEnv, func(string, ...any) {})
		defer closeProbe()
		ep := &otherVersionEndpoint{Endpoint: remote.NewLocal(p, "claude-teleport", lopts), version: "0.0.0-other"}
		if err := remote.Serve(context.Background(), stdin, stdout, ep); err != nil {
			io.WriteString(stderr, err.Error())
			return 1
		}
		return 0
	})
	cfg := filepath.Join(localHome, ".claude")
	if err := os.MkdirAll(filepath.Join(cfg, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := harness.Env(t, localHome, cfg, "USER=alice")
	code, out, stderr := run(t, env, append([]string{"doctor", target}, opts...)...)
	if code != ExitUnreachable {
		t.Fatalf("doctor against a mismatched peer = %d, want %d\n%s\n%s", code, ExitUnreachable, out, stderr)
	}
	if !strings.Contains(stderr, "0.0.0-other") {
		t.Errorf("the failure must name the remote version:\n%s", stderr)
	}
}
