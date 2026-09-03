package cli

import (
	"context"
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

	cs, err := remoteChecks(context.Background(), ep, "bob@dest.example")
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
