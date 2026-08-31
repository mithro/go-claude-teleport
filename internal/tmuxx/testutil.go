//go:build tmuxlive

// Copied from github.com/mithro/go-tmux-saver internal/tmuxctl
// (Apache-2.0, same author). Keep in sync by hand; do not import.

package tmuxx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// StartTestServer starts a throwaway tmux server (session "default",
// window "h") on socket claude-teleport-test-<pid>-<test> under a private
// TMUX_TMPDIR and kills it when the test ends. Returns the socket PATH and
// the socket directory. Skips if tmux is not installed. -f /dev/null keeps
// the developer's ~/.tmux.conf (hooks, continuum) out of the test.
func StartTestServer(t testing.TB) (socketPath, socketDir string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tmp := t.TempDir()
	t.Setenv("TMUX_TMPDIR", tmp)
	socketDir = filepath.Join(tmp, fmt.Sprintf("tmux-%d", os.Getuid()))
	name := "claude-teleport-test-" + fmt.Sprint(os.Getpid()) + "-" + strings.ReplaceAll(t.Name(), "/", "_")
	if len(name) > 40 {
		name = name[:40] // unix socket paths are limited to ~108 bytes
	}
	if out, err := exec.Command("tmux", "-L", name, "-f", "/dev/null", "new-session", "-d", "-s", "default", "-n", "h", "tail -f /dev/null").CombinedOutput(); err != nil {
		t.Fatalf("start tmux: %v: %s", err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", name, "kill-server").Run() })
	return filepath.Join(socketDir, name), socketDir
}
