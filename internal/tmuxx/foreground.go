package tmuxx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mithro/go-claude-teleport/internal/procx"
)

// Bounds for the wait after `fg` is typed: how long the shell gets to hand
// the pty back, and how often that is checked.
const (
	ForegroundPoll    = 100 * time.Millisecond
	ForegroundTimeout = 10 * time.Second
)

// freezerBudget bounds the freezer helper's whole restore attempt (dial,
// pane lookup, `fg`, wait). The helper is nobody's child by then, so it
// must never hang around holding a tmux control client open.
const freezerBudget = 30 * time.Second

// ErrNotRestored is wrapped when `fg` was typed but the pty's foreground
// never came back within the timeout.
var ErrNotRestored = errors.New("the thawed process did not get its terminal back")

// ForegroundOptions tunes RestoreForeground; the zero value is the
// production configuration (real /proc, real sleep, no logging).
type ForegroundOptions struct {
	ProcRoot string               // "" = "/proc"
	Logf     func(string, ...any) // nil = discard
	Sleep    func(time.Duration)  // nil = time.Sleep
	Timeout  time.Duration        // 0 = ForegroundTimeout
	Poll     time.Duration        // 0 = ForegroundPoll

	// ClearLine sends a literal CR before the `fg`, terminating whatever
	// the shell already has on its line.
	//
	// The ordinary thaw path does not need it: it types `fg` BEFORE
	// anything continues the job, so the job has had no chance to make
	// the terminal answer a query into the shell's input. The freezer
	// helper's owner-died path does, because there the SIGCONT has
	// already happened by design (ruling R-P3-PROOF-5 item 1).
	ClearLine bool
}

// RestoreForeground gives a stopped job the pty of pane paneID back.
//
// An interactive shell is a job-control shell: when its foreground job
// stops — by SIGSTOP from the freezer just as much as by ^Z — it takes the
// terminal back for itself and prints "[1]+ Stopped". SIGCONT resumes the
// job's execution but not its claim on the terminal, so its next read gets
// SIGTTIN and stops it again: the thawed Claude is left in state T for
// good, and everything typed at the pane afterwards (the "/exit" of spec
// §6.3, or a user's own keystrokes) lands on the shell instead. Only a
// process whose controlling terminal this is may tcsetpgrp it back, which
// rules out the freezer, the runner and the remote helper alike — so the
// one process that can fix it is the shell itself, and the way to ask is
// its own `fg`.
//
// CALL THIS BEFORE THE SIGCONT, not after (ruling R-P3-PROOF-5 item 1).
// `fg` hands the terminal over AND continues the job, so it needs no help
// — and the SIGCONT-first order loses a real teleport: on SIGCONT Claude
// re-issues its colour-scheme query (CSI ?996n), tmux answers CSI ?997;1n
// into the pane's INPUT, the shell — still the foreground — reads that as
// typed text, and the ` 'fg'` that arrives next lands on the polluted
// line. The pane a real return leg was left with:
//
//	alice@laptop:~$ 997;1n997;1n 'fg'
//	bash: 997: command not found
//
// with Claude still stopped and the shell still owning the tty. Typing
// `fg` first leaves the query no window: by the time the job runs again
// it is already the foreground and reads its own answer.
//
// The caller keeps the explicit SIGCONT as the fallback for the two cases
// this cannot cover — the pane's process is not a job-control shell (this
// no-ops), and `fg` did not take within the budget (ErrNotRestored).
//
// Nothing is typed unless the pty's foreground really has moved to a
// process group led by the pane's own process: with job control off (or
// with Claude as the pane's own command) the job never lost the terminal
// and there is nothing to restore.
//
// This is the ONE implementation of that restore. remote.Local.Thaw calls
// it for the ordinary thaw, and FreezerRestore calls it from the freezer
// helper when the owner died and no thawing caller is left (R-P3-F1).
func RestoreForeground(ctx context.Context, t Transport, paneID string, pid int, opts ForegroundOptions) error {
	procRoot := opts.ProcRoot
	if procRoot == "" {
		procRoot = "/proc"
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = ForegroundTimeout
	}
	poll := opts.Poll
	if poll == 0 {
		poll = ForegroundPoll
	}

	pgid, err := procx.ProcGroup(procRoot, pid)
	if err != nil {
		logf("foreground: pid %d has no process group any more (%v); nothing to restore", pid, err)
		return nil // the target is gone: nothing to foreground
	}
	fg, err := procx.ForegroundGroup(procRoot, pid)
	if err != nil || fg <= 0 || fg == pgid {
		logf("foreground: pid %d's terminal foreground is %d (its own group is %d, %v); nothing to restore", pid, fg, pgid, err)
		return nil
	}
	// The process tmux started in this pane leads its own process group,
	// so that group is what the pty's foreground reverts to when a
	// job-control shell takes the terminal back. Comparing fg against it
	// beats matching the holder's comm against a hardcoded shell list on
	// two counts: it works for any shell (or any other job-control
	// program) a pane may run, and it proves that paneID actually names
	// the pane whose terminal this is — a ref pointing somewhere else can
	// no longer have `fg` typed into it.
	panePID, err := PanePID(ctx, t, paneID)
	if err != nil {
		return err
	}
	if fg != panePID {
		logf("foreground: pid %d is not the foreground of its terminal (group %d), and pane %s's own process is pid %d, so this pane's shell is not what holds it; leaving it alone", pid, fg, paneID, panePID)
		return nil
	}
	logf("foreground: the pane process (pid %d) took the terminal back while pid %d was stopped; typing fg into pane %s", fg, pid, paneID)
	if opts.ClearLine {
		// The shell's line is not assumed empty: a CR of its own ends
		// whatever is on it (at worst one "command not found") so the
		// `fg` below starts from a fresh prompt.
		if err := SendReturn(ctx, t, paneID); err != nil {
			return err
		}
	}
	if err := TypeCommand(ctx, t, paneID, []string{"fg"}); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return err // a cancelled job must not sit out the whole timeout
		}
		if fg, err := procx.ForegroundGroup(procRoot, pid); err != nil || fg == pgid {
			logf("foreground: pid %d has its terminal back (group %d, %v)", pid, fg, err)
			return nil // restored, or the process is gone
		}
		if time.Now().After(deadline) {
			// What the shell made of what it was given is the only clue
			// to why `fg` did not take, and nobody can see the pane once
			// a detached runner has failed on it.
			for _, l := range paneTail(ctx, t, paneID, 10) {
				logf("foreground: pane %s: %s", paneID, l)
			}
			return fmt.Errorf("%w: pid %d, pane %s, within %s", ErrNotRestored, pid, paneID, timeout)
		}
		sleep(poll)
	}
}

// FreezerRestore builds the procx.RestoreFunc the freezer helper runs after
// the SIGCONT of its owner-died path: it dials the pane's own tmux server
// and performs exactly the restore above, into that pane and no other.
//
// procx must not import tmuxx (tmuxx imports procx for /proc parsing), so
// the helper is handed this hook by whoever builds it — internal/cli for
// the real binary. Everything it decides goes to stderr, since by then the
// helper has no other log and no living owner to return an error to.
func FreezerRestore(socketPath, paneID string) procx.RestoreFunc {
	return func(pid int) error {
		logf := func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "freezer: "+format+"\n", args...)
		}
		if (procx.PaneRef{SocketPath: socketPath, PaneID: paneID}).Empty() {
			logf("no pane was recorded at freeze time; pid %d was SIGCONT'd and its terminal left alone", pid)
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), freezerBudget)
		defer cancel()
		t, err := DialControl(ctx, socketPath)
		if err != nil {
			return fmt.Errorf("dial %s: %w", socketPath, err)
		}
		defer t.Close()
		logf("owner died: pid %d was SIGCONT'd; checking whether pane %s's shell holds its terminal", pid, paneID)
		// ClearLine: this is the one path where the SIGCONT is already
		// done before anything can type `fg`, so the shell's line may
		// already hold the answer to a query the resumed job made.
		return RestoreForeground(ctx, t, paneID, pid, ForegroundOptions{Logf: logf, ClearLine: true})
	}
}

// paneTail returns the last n non-blank lines of the pane's visible
// screen, for logging why a restore did not take. Errors are folded into
// the returned lines: this only ever runs on a path that is already
// failing, and a capture that fails is itself worth saying out loud.
func paneTail(ctx context.Context, t Transport, paneID string, n int) []string {
	screen, err := CaptureScreen(ctx, t, paneID)
	if err != nil {
		return []string{fmt.Sprintf("(capture failed: %v)", err)}
	}
	var out []string
	for _, l := range strings.Split(string(screen), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}
