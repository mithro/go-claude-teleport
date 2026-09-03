package remote

import (
	"bytes"
	"context"
	"fmt"
	"os"
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

// ConfirmClaude implements spec §6.2: registry entry alive in our pane, no
// failure marker in NEW pane output, and status idle — or, for a `-p` run
// that never reaches idle, busy after it has produced a turn (case 3) —
// all within timeout.
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

	// M4: capture-pane -S - returns the WHOLE scrollback every time, which
	// can still hold a failure marker (a stale login prompt, ...) left by
	// an earlier, unrelated confirm attempt — this call always runs in a
	// freshly re-spawned runner process (job continue/retry) with no
	// memory of that attempt, so it cannot tell "stale" from "current" any
	// other way. The first capture of THIS call is therefore treated as
	// the pre-existing baseline and is never itself scanned; only output
	// beyond it (this attempt's own new output, appended after the start
	// keystroke that preceded this call) is checked. capture-pane's
	// history is append-only in the ordinary case, so later captures keep
	// the baseline as a byte-prefix; if that ever breaks (e.g. tmux's
	// history-limit trimmed old lines), fall back to scanning the whole
	// capture rather than silently miss real output.
	//
	// Disclosed trade-off: a failure that manifests in the fraction of a
	// second between the start keystroke and this call's first capture
	// would be folded into the baseline and missed here; confirmation
	// still fails (via timeout) rather than succeeding wrongly, just with
	// a less specific error. See task-21-report.md.
	var markerBaseline []byte
	haveMarkerBaseline := false

	// M5 (spec §6.2 case 3): a `-p` (print) run's registry never reports
	// "idle" — fakeclaude/real Claude Code do one exchange and remove the
	// registry entry on exit rather than looping back to a prompt — so
	// success for one is declared once its transcript has grown past this
	// call's own baseline size (evidence a turn actually completed) while
	// status is "busy". See task-21-report.md for the disclosed
	// detectability gap: whether a live registry entry's Kind reliably
	// distinguishes a print run from an interactive one could not be
	// verified against real Claude Code from here.
	// Baselined once, up front, rather than at the first busy+print
	// sighting: the growth that matters is growth since this call began.
	transcriptBaseline := int64(-1)
	if n, terr := l.transcriptSize(id); terr == nil {
		transcriptBaseline = n
	}
	// B11: a print run removes its registry entry the moment it finishes,
	// so the poll can miss the busy-with-a-completed-turn window entirely —
	// one iteration sees it, the next sees nothing, and the confirm then
	// burns the whole --start-timeout on a run that in fact succeeded.
	// Remembering the print entry we DID see lets the same evidence (its
	// transcript grew past the baseline) be accepted one poll late. Only a
	// run seen to be print-kind qualifies: an interactive Claude whose
	// entry disappears has not resumed, however much it wrote on the way
	// down.
	var lastPrintReg *session.Registry

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ref != nil {
			text, err := tmuxx.Capture(ctx, t, ref.PaneID)
			if err != nil {
				return nil, &Error{Code: "internal", Message: err.Error()}
			}
			if !haveMarkerBaseline {
				markerBaseline, haveMarkerBaseline = text, true
			} else {
				fresh := text
				if len(text) >= len(markerBaseline) && bytes.HasPrefix(text, markerBaseline) {
					fresh = text[len(markerBaseline):]
				}
				if m, hit := HasFailureMarker(string(fresh)); hit {
					return nil, &Error{Code: "conflict", Message: fmt.Sprintf("destination Claude did not resume: pane shows %q", m)}
				}
			}
		}
		reg, ok, err := l.ClaudeStatus(ctx, id)
		if err != nil {
			return nil, err
		}
		switch {
		case !ok:
			if lastPrintReg != nil && transcriptBaseline >= 0 {
				if n, terr := l.transcriptSize(id); terr == nil && n > transcriptBaseline {
					l.opts.Logf("confirm: the print-mode run for %s exited between polls; its transcript grew %d -> %d bytes, so its turn completed", id, transcriptBaseline, n)
					return lastPrintReg, nil
				}
			}
			last = "no live registry entry for the session"
		case wantTmux != "" && reg.Tmux != wantTmux:
			last = fmt.Sprintf("registry pane %q is not our pane %q", reg.Tmux, wantTmux)
		case reg.Status == "idle":
			return reg, nil
		case reg.Status == "busy" && strings.EqualFold(reg.Kind, "print"):
			lastPrintReg = reg
			if n, terr := l.transcriptSize(id); terr == nil {
				if transcriptBaseline < 0 {
					transcriptBaseline = n
				} else if n > transcriptBaseline {
					return reg, nil
				}
			}
			last = "print-mode run is busy; waiting for its turn to finish"
		default:
			last = fmt.Sprintf("registry status is %q, waiting for idle", reg.Status)
		}
		if time.Now().After(deadline) {
			return nil, &Error{Code: "conflict", Message: fmt.Sprintf("destination Claude not confirmed within %s: %s", timeout, last)}
		}
		l.opts.Sleep(confirmPoll)
	}
}

// transcriptSize returns id's transcript size, used only as growth
// evidence for the print-mode (`-p`) acceptance case above.
func (l *Local) transcriptSize(id session.ID) (int64, error) {
	path, err := session.FindTranscript(l.paths.ProjectsDir(), id)
	if err != nil {
		return 0, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
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
