package remote

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// ring keeps the last n bytes written to it.
type ring struct {
	mu  sync.Mutex
	buf []byte
	n   int
}

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.n {
		r.buf = r.buf[len(r.buf)-r.n:]
	}
	return len(p), nil
}

func (r *ring) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

// RunPtyResume runs `claude --resume <id>` under a pty in cwd, confirms
// per spec §6.2, then exits it (spec §9, no-tmux destination).
func (l *Local) RunPtyResume(ctx context.Context, id session.ID, cwd string, timeout time.Duration) error {
	cmd := exec.CommandContext(ctx, "claude", "--resume", string(id))
	cmd.Dir = cwd
	// HOME and CLAUDE_CONFIG_DIR are appended (not merely conditionally
	// added) so they win over whatever this process's own os.Environ()
	// carries: a later duplicate entry in exec's env slice overrides an
	// earlier one (verified against this platform's execve/getenv).
	// Un-conditioning this also fixes an in-process, two-host test fixture
	// (Task 21's RunJob test) where l.paths.Home differs from the real
	// process $HOME on both the "source" and "destination" Local — the old
	// conditional compared l.paths.ConfigDir to l.paths.Home+"/.claude"
	// (always equal, since both derive from the same paths.Home), so it
	// never actually fired and `claude` always resumed against the real
	// machine's $HOME/.claude instead of the fixture's. In production
	// l.paths.Home already equals the process's real $HOME, so this is a
	// no-op there.
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "HOME="+l.paths.Home, "CLAUDE_CONFIG_DIR="+l.paths.ConfigDir)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return &Error{Code: "internal", Message: fmt.Sprintf("pty-resume: start claude: %v", err)}
	}
	defer f.Close()
	out := &ring{n: 64 * 1024}
	done := make(chan error, 1)
	go func() { _, _ = io.Copy(out, f); done <- cmd.Wait() }()
	l.opts.Logf("pty-resume: started claude --resume %s (pid %d) in %s", id, cmd.Process.Pid, cwd)

	deadline := time.Now().Add(timeout)
	for {
		if m, hit := HasFailureMarker(out.String()); hit {
			cmd.Process.Kill()
			<-done
			return &Error{Code: "conflict", Message: fmt.Sprintf("destination Claude did not resume: output shows %q", m)}
		}
		select {
		case err := <-done:
			return &Error{Code: "conflict", Message: fmt.Sprintf("claude exited before confirming (%v); last output:\n%s", err, tail(out.String(), 20))}
		default:
		}
		if err := ctx.Err(); err != nil {
			cmd.Process.Kill()
			<-done
			return &Error{Code: "conflict", Message: fmt.Sprintf("pty-resume: context cancelled while confirming: %v", err)}
		}
		reg, ok, err := l.ClaudeStatus(ctx, id)
		if err != nil {
			cmd.Process.Kill()
			<-done
			return err
		}
		if ok && reg.PID == cmd.Process.Pid && reg.Status == "idle" {
			break
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			<-done
			return &Error{Code: "conflict", Message: fmt.Sprintf("destination Claude not confirmed within %s; last output:\n%s", timeout, tail(out.String(), 20))}
		}
		l.opts.Sleep(confirmPoll)
	}
	if _, err := f.Write([]byte("/exit\r")); err != nil {
		cmd.Process.Kill()
		<-done
		return &Error{Code: "conflict", Message: fmt.Sprintf("pty-resume: write /exit: %v", err)}
	}
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		cmd.Process.Kill()
		<-done
		return &Error{Code: "conflict", Message: fmt.Sprintf("claude (pid %d) did not exit within %s after /exit", cmd.Process.Pid, timeout)}
	}
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
