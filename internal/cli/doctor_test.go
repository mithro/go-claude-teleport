package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/test/fakeclaude/harness"
)

func TestDoctorPassesWithFakeClaude(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, ".claude")
	os.MkdirAll(filepath.Join(cfg, "projects"), 0o700)
	env := harness.Env(t, root, cfg, "XDG_DATA_HOME="+filepath.Join(root, "data"))
	code, out, stderr := run(t, env, "doctor")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s%s", code, out, stderr)
	}
	for _, w := range []string{"ok    claude on PATH", "2.1.247", "ok    config dir", "ok    data dir writable"} {
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
	localEnv := []string{"HOME=" + localHome, "USER=alice", "CLAUDE_CONFIG_DIR=" + cfg, "PATH=" + os.Getenv("PATH")}

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
