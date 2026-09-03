package remote

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

// claudeEnv is the environment a claude process this host starts must see,
// derived from base (normally os.Environ()).
//
// HOME is appended rather than conditionally replaced so it wins over
// whatever base carries: os/exec deduplicates Cmd.Env before the exec and
// keeps the LAST entry for a name (dedupEnv in os/exec), so appending wins.
// The kernel and libc have no say in it — execve passes the array through
// verbatim and getenv would return the FIRST match — which is why this
// relies on os/exec's behaviour specifically. Overriding it matters for the
// in-process, two-host test fixtures where paths.Home differs from the real
// process $HOME; in production they are already equal.
//
// CLAUDE_CONFIG_DIR is the T26-2 case and is NOT unconditional. Claude Code
// chooses its global config file by whether that variable is present at all
// — set (to anything, the default path included) it reads and writes
// $CLAUDE_CONFIG_DIR/.claude.json, absent it reads $HOME/.claude.json — so
// exporting a variable nobody set would send the Claude we start to a file
// the destination's project entry (session.Paths.GlobalJSON, where install
// merged it) was never written to, silently losing the trust-dialog and
// mcpServers state the teleport carried over. It is therefore exported only
// when the environment is what put ConfigDir where it is, or when ConfigDir
// is not the $HOME/.claude default and so cannot be communicated any other
// way; otherwise any inherited value is dropped, since the claude we start
// must not see one.
func claudeEnv(base []string, p session.Paths) []string {
	export := p.ConfigDirFromEnv || p.ConfigDir != filepath.Join(p.Home, ".claude")
	env := make([]string, 0, len(base)+2)
	for _, e := range base {
		if !export && strings.HasPrefix(e, "CLAUDE_CONFIG_DIR=") {
			continue
		}
		env = append(env, e)
	}
	env = append(env, "HOME="+p.Home)
	if export {
		env = append(env, "CLAUDE_CONFIG_DIR="+p.ConfigDir)
	}
	return env
}

// RunPtyResume runs `claude --resume <id>` under a pty in cwd, confirms
// per spec §6.2, then exits it (spec §9, no-tmux destination).
func (l *Local) RunPtyResume(ctx context.Context, id session.ID, cwd string, timeout time.Duration) error {
	cmd := exec.CommandContext(ctx, "claude", "--resume", string(id))
	cmd.Dir = cwd
	cmd.Env = append(claudeEnv(os.Environ(), l.paths), "TERM=xterm-256color")
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
