package procx

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// SpawnDetached starts argv in its own session (setsid) with stdin from
// /dev/null and stdout+stderr appended to logPath (created 0600); returns
// the child pid. The child is released: it is never waited for, so it
// outlives the caller (spec §6: the runner is never a child of Claude).
func SpawnDetached(argv []string, dir, logPath string, env []string) (int, error) {
	if len(argv) == 0 {
		return 0, fmt.Errorf("spawn: empty argv")
	}
	logf, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return 0, fmt.Errorf("spawn: open log %s: %w", logPath, err)
	}
	defer logf.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return 0, fmt.Errorf("spawn: %w", err)
	}
	defer devnull.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = devnull
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("spawn %q: %w", argv[0], err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return pid, fmt.Errorf("spawn: release pid %d: %w", pid, err)
	}
	return pid, nil
}
