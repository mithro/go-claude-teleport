package remote

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"time"

	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

const confirmPoll = 250 * time.Millisecond

// StartClaude types the placeholder/claude argv into the destination pane.
func (l *Local) StartClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, jobID string, argv []string) error {
	if ref == nil {
		return &Error{Code: "usage", Message: "start-claude: nil pane ref"}
	}
	l.opts.Logf("start: typing %q into %s", strings.Join(argv, " "), ref.PaneID)
	return l.TypeCommand(ctx, ref, argv)
}

// ClaudeStatus returns the registry entry for id when its pid is alive.
func (l *Local) ClaudeStatus(ctx context.Context, id session.ID) (*session.Registry, bool, error) {
	reg, ok, err := procx.RegistryForSession(l.paths.SessionsDir(), id)
	if err != nil || !ok {
		return nil, false, err
	}
	procs, err := l.procs()
	if err != nil {
		return nil, false, err
	}
	if !procs.Alive(reg.PID, reg.ProcStart) {
		return nil, false, nil
	}
	return reg, true, nil
}

// ConfirmClaude implements spec §6.2: registry entry alive in our pane,
// no failure marker in the pane, status idle — all within timeout.
func (l *Local) ConfirmClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, timeout time.Duration) (*session.Registry, error) {
	// Both sides of this comparison are tmux's STORED session-name spelling
	// (R-PRB-2): Claude Code writes registry.tmux from #{session_name}, and
	// TmuxRef.Session carries the same spelling — so no decoding here, on
	// either side, or an escaped name could never match.
	wantTmux := ""
	if ref != nil {
		wantTmux = tmuxx.RefString(ref)
	}
	// M3: one control connection for the whole poll, not one per 250ms
	// iteration — the old shape spawned a `tmux -C attach-session` process
	// per poll, ~240 of them over a 60s --start-timeout. Run is serialised
	// and the transport is reusable; a Capture error still aborts, so there
	// is nothing to re-dial for.
	var t tmuxx.Transport
	if ref != nil {
		var err error
		if t, err = l.dial(ctx, ref.SocketPath); err != nil {
			return nil, err
		}
		defer t.Close()
	}
	deadline := time.Now().Add(timeout)
	last := "no registry entry for the session yet"
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ref != nil {
			text, err := tmuxx.Capture(ctx, t, ref.PaneID)
			if err != nil {
				return nil, &Error{Code: "internal", Message: err.Error()}
			}
			if m, hit := HasFailureMarker(string(text)); hit {
				return nil, &Error{Code: "conflict", Message: fmt.Sprintf("destination Claude did not resume: pane shows %q", m)}
			}
		}
		reg, ok, err := l.ClaudeStatus(ctx, id)
		if err != nil {
			return nil, err
		}
		switch {
		case !ok:
			last = "no live registry entry for the session"
		case wantTmux != "" && reg.Tmux != wantTmux:
			last = fmt.Sprintf("registry pane %q is not our pane %q", reg.Tmux, wantTmux)
		case reg.Status != "idle":
			last = fmt.Sprintf("registry status is %q, waiting for idle", reg.Status)
		default:
			return reg, nil
		}
		if time.Now().After(deadline) {
			return nil, &Error{Code: "conflict", Message: fmt.Sprintf("destination Claude not confirmed within %s: %s", timeout, last)}
		}
		l.opts.Sleep(confirmPoll)
	}
}

// ExitClaude implements spec §6.3: in tmux, /exit + Enter then wait for
// the pid to go; without a pane, SIGTERM then wait.
func (l *Local) ExitClaude(ctx context.Context, ref *session.TmuxRef, pid int, startTime string, timeout time.Duration) error {
	if ref != nil {
		// A pane can still be sent keys after its foreground process has
		// already exited (they just land on the shell) — no alive check
		// needed, WaitGone below is what actually confirms the exit.
		t, err := l.dial(ctx, ref.SocketPath)
		if err != nil {
			return err
		}
		defer t.Close()
		if err := tmuxx.SendKeys(ctx, t, ref.PaneID, "/exit"); err != nil {
			return err
		}
		l.opts.Sleep(500 * time.Millisecond)
		if err := tmuxx.SendKeys(ctx, t, ref.PaneID, "Enter"); err != nil {
			return err
		}
	} else {
		// No pane: signal the pid directly. Guard against ESRCH on an
		// already-gone pid — WaitGone below still confirms either way.
		procs, err := l.procs()
		if err != nil {
			return err
		}
		if procs.Alive(pid, startTime) {
			if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
				return fmt.Errorf("SIGTERM pid %d: %w", pid, err)
			}
		}
	}
	if err := procx.WaitGone(l.procs, pid, startTime, timeout, confirmPoll, l.opts.Sleep); err != nil {
		return &Error{Code: "conflict", Message: fmt.Sprintf("claude (pid %d) still running after %s: %v", pid, timeout, err)}
	}
	return nil
}
