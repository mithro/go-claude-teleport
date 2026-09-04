//go:build !tmuxlive

package remote

// R-P3-PROOF-7: ExitClaude's pane branch must fall back to SIGTERM then
// SIGKILL when a typed /exit does not take, exactly as the no-pane branch
// already signals a pid directly. These tests exercise that against REAL
// child processes (this file's own TestMain re-execs the test binary as a
// tiny helper, mirroring internal/procx/main_test.go and
// thaw_live_test.go's own pattern) so WaitGone observes a real pid really
// going away — never a fake /proc entry that a real syscall.Kill could
// land on an unrelated system process.
//
// Guarded to the default (non-tmuxlive) build: thaw_live_test.go already
// owns TestMain under -tags tmuxlive, and a package may define only one.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

// exitFallbackTimeout is the ExitClaude timeout cases (b)-(d) below pass
// for the graceful /exit wait, which they need to expire (the child never
// receives /exit, or is already gone). With a no-op opts.Sleep, WaitGone's
// "waited" counter advances without any real delay, so whether the target
// state changes for real within this window is irrelevant to how long the
// test takes — it is only a bound on how many procx.Scan iterations run,
// which stays fast regardless of the nominal value. Case (a), where the
// target DOES need to actually change state inside the window, uses its
// own real-Sleep options and a longer timeout instead — see
// gracefulExitPane's doc comment for why a no-op Sleep is wrong there.
const exitFallbackTimeout = 2 * time.Second

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "exit-helper" {
		runExitHelper(os.Args[2:])
		return
	}
	os.Exit(m.Run())
}

// runExitHelper is the real child process cases (a)-(c) below exercise
// ExitClaude's pane-branch escalation against:
//   - "--exit-immediately": exits(0) at once (used to build a real, safely
//     signallable "still alive forever" zombie — see
//     TestExitClaudeExhaustsEscalationThenErrors).
//   - "--ignore-term": ignores SIGTERM (signal.Ignore), so only SIGKILL can
//     ever end it.
//   - otherwise: reads stdin lines and exits(0) the moment one reads
//     exactly "/exit" — the stand-in for a typed /exit + Enter actually
//     landing. If stdin is never written to (or is closed without that
//     line ever appearing), it blocks forever instead of exiting on EOF,
//     so only a signal can end it.
func runExitHelper(args []string) {
	if len(args) > 0 && args[0] == "--exit-immediately" {
		os.Exit(0)
	}
	if len(args) > 0 && args[0] == "--ignore-term" {
		signal.Ignore(syscall.SIGTERM)
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		if sc.Text() == "/exit" {
			os.Exit(0)
		}
	}
	select {}
}

// startExitHelper spawns this test binary as `exit-helper` (TestMain above
// re-execs it). Reaped by a background goroutine the instant it exits —
// mirrors local_claude_test.go's own startSleep comment: an unreaped
// zombie would otherwise look "alive" forever to procx.Table.Alive, which
// would wrongly keep case (a)/(b)/(c) below from ever seeing it go. Only
// ever signalled/waited by this test's own cleanup.
func startExitHelper(t *testing.T, ignoreTerm bool) (pid int, startTime string, stdin io.WriteCloser) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	argv := []string{"exit-helper"}
	if ignoreTerm {
		argv = append(argv, "--ignore-term")
	}
	cmd := exec.Command(self, argv...)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn exit-helper: %v", err)
	}
	reaped := make(chan struct{})
	go func() { cmd.Wait(); close(reaped) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		in.Close()
		<-reaped
	})
	st, err := procx.StartTime("/proc", cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid, st, in
}

// keyRelayTransport wraps tmuxx.Fake and, for case (a) only, forwards the
// exact `send-keys ... "/exit"` and `send-keys ... Enter` commands
// ExitClaude issues into a real child's stdin — standing in for a real
// tmux pane actually delivering those keystrokes, without needing a real
// tmux server. Every other command is answered exactly as tmuxx.Fake would.
type keyRelayTransport struct {
	*tmuxx.Fake
	stdin io.Writer
}

func (k *keyRelayTransport) Run(ctx context.Context, cmd string) ([]string, error) {
	switch cmd {
	case `send-keys -t "%7" "/exit"`:
		if _, err := k.stdin.Write([]byte("/exit")); err != nil {
			return nil, err
		}
	case `send-keys -t "%7" Enter`:
		if _, err := k.stdin.Write([]byte("\n")); err != nil {
			return nil, err
		}
	}
	return k.Fake.Run(ctx, cmd)
}

// capturedLogs collects LocalOptions.Logf output into lines, for asserting
// which escalation steps ExitClaude actually took.
func capturedLogs() (logf func(string, ...any), lines func() []string) {
	var got []string
	return func(format string, a ...any) { got = append(got, fmt.Sprintf(format, a...)) },
		func() []string { return got }
}

func anyContains(lines []string, substr string) bool {
	for _, l := range lines {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

func exitFallbackPane(f tmuxx.Dialer, logf func(string, ...any)) LocalOptions {
	return LocalOptions{ProcRoot: "/proc", Tmux: f, Sleep: func(time.Duration) {}, Logf: logf}
}

func exitFallbackFake() *tmuxx.Fake {
	return &tmuxx.Fake{Replies: map[string][]string{`send-keys -t "%7" "/exit"`: {}, `send-keys -t "%7" Enter`: {}}}
}

// gracefulExitTimeout and gracefulExitPane back ONLY the graceful-exit
// case (a) test below, and deliberately differ from exitFallbackPane's
// no-op Sleep: unlike SIGTERM/SIGKILL (an externally delivered signal that
// this file's own timing probes measured ending a race-instrumented
// exit-helper child in ~5ms), a race-instrumented binary voluntarily
// exiting via os.Exit(0) was measured taking just over 1s in real wall
// time before WaitGone's Alive check stops seeing it (present but a
// zombie, or the race runtime's own shutdown work — either way, real
// time). A no-op Sleep decouples WaitGone's "waited" counter from real
// time, so it can (and did, reliably reproducing under -race) exhaust the
// nominal timeout in a handful of real milliseconds — long before that
// real delay elapses — and wrongly declare /exit "did not take". Real
// Sleep plus a timeout with generous margin over the measured ~1.05s
// avoids that.
const gracefulExitTimeout = 6 * time.Second

func gracefulExitPane(f tmuxx.Dialer, logf func(string, ...any)) LocalOptions {
	return LocalOptions{ProcRoot: "/proc", Tmux: f, Sleep: time.Sleep, Logf: logf}
}

// TestExitClaudeGracefulExitNoSignalNeeded is case (a): /exit typed and the
// real child exits promptly on reading it — no signal must ever be sent.
func TestExitClaudeGracefulExitNoSignalNeeded(t *testing.T) {
	p := testPaths(t)
	pid, startTime, stdin := startExitHelper(t, false)
	relay := &keyRelayTransport{Fake: exitFallbackFake(), stdin: stdin}
	logf, logs := capturedLogs()
	l := NewLocal(p, "x", gracefulExitPane(func(context.Context, string) (tmuxx.Transport, error) { return relay, nil }, logf))

	if err := l.ExitClaude(context.Background(), &session.TmuxRef{SocketPath: "/s", PaneID: "%7"}, pid, startTime, gracefulExitTimeout); err != nil {
		t.Fatalf("ExitClaude: %v", err)
	}
	if alive(pid) {
		t.Errorf("pid %d still alive after a graceful /exit", pid)
	}
	if anyContains(logs(), "SIGTERM") || anyContains(logs(), "SIGKILL") {
		t.Errorf("escalation logged even though /exit took: %v", logs())
	}
}

// TestExitClaudeFallsBackToSIGTERMWhenExitDoesNotTake is case (b): the
// child never receives (and so ignores) the typed keys — SIGTERM must end
// it within its grace, and that escalation must be logged; SIGKILL must
// never be needed.
func TestExitClaudeFallsBackToSIGTERMWhenExitDoesNotTake(t *testing.T) {
	p := testPaths(t)
	pid, startTime, _ := startExitHelper(t, false) // nothing written to stdin: /exit never lands
	logf, logs := capturedLogs()
	l := NewLocal(p, "x", exitFallbackPane(fakeDialer(exitFallbackFake()), logf))

	if err := l.ExitClaude(context.Background(), &session.TmuxRef{SocketPath: "/s", PaneID: "%7"}, pid, startTime, exitFallbackTimeout); err != nil {
		t.Fatalf("ExitClaude: %v", err)
	}
	if alive(pid) {
		t.Errorf("pid %d still alive after ExitClaude returned", pid)
	}
	if !anyContains(logs(), "SIGTERM") {
		t.Errorf("no SIGTERM escalation logged: %v", logs())
	}
	if anyContains(logs(), "SIGKILL") {
		t.Errorf("SIGKILL escalation logged when SIGTERM alone should have ended it: %v", logs())
	}
}

// TestExitClaudeFallsBackToSIGKILLWhenSIGTERMIsIgnored is case (c): the
// child ignores both the typed keys and SIGTERM — only SIGKILL ends it,
// and both escalation steps must be logged.
func TestExitClaudeFallsBackToSIGKILLWhenSIGTERMIsIgnored(t *testing.T) {
	p := testPaths(t)
	pid, startTime, _ := startExitHelper(t, true) // ignores SIGTERM; /exit never lands either
	logf, logs := capturedLogs()
	l := NewLocal(p, "x", exitFallbackPane(fakeDialer(exitFallbackFake()), logf))

	if err := l.ExitClaude(context.Background(), &session.TmuxRef{SocketPath: "/s", PaneID: "%7"}, pid, startTime, exitFallbackTimeout); err != nil {
		t.Fatalf("ExitClaude: %v", err)
	}
	if alive(pid) {
		t.Errorf("pid %d still alive after ExitClaude returned", pid)
	}
	if !anyContains(logs(), "SIGTERM") || !anyContains(logs(), "SIGKILL") {
		t.Errorf("expected both SIGTERM and SIGKILL escalation logged: %v", logs())
	}
}

// TestExitClaudeAlreadyGoneSignalsNothing is case (d): the pid is already
// gone (fully exited and reaped) before ExitClaude is ever called — no
// signal may be sent to it (the start-time guard would in any case refuse
// a recycled pid, but here there is no process at all to recycle onto).
func TestExitClaudeAlreadyGoneSignalsNothing(t *testing.T) {
	p := testPaths(t)
	pid, startTime, stdin := startExitHelper(t, false)
	if _, err := stdin.Write([]byte("/exit\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("pid %d never exited", pid)
		}
		time.Sleep(5 * time.Millisecond)
	}

	logf, logs := capturedLogs()
	l := NewLocal(p, "x", exitFallbackPane(fakeDialer(exitFallbackFake()), logf))
	if err := l.ExitClaude(context.Background(), &session.TmuxRef{SocketPath: "/s", PaneID: "%7"}, pid, startTime, exitFallbackTimeout); err != nil {
		t.Fatalf("ExitClaude: %v", err)
	}
	if len(logs()) != 0 {
		t.Errorf("signalled an already-gone pid: %v", logs())
	}
}

// TestExitClaudeExhaustsEscalationThenErrors is R-P3-PROOF-7 item 2's
// negative case: a target that survives even SIGKILL still only errors
// once EVERY step — /exit, SIGTERM, SIGKILL — has actually been tried,
// never before. An unreaped zombie is the safe, deterministic way to build
// that target from a real process: kill(2) on one succeeds but has no
// effect (verified against the real kernel — see fix-exit-report.md), and
// only this test's own spawned process is ever touched.
func TestExitClaudeExhaustsEscalationThenErrors(t *testing.T) {
	p := testPaths(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "exit-helper", "--exit-immediately")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn exit-helper: %v", err)
	}
	pid := cmd.Process.Pid
	startTime, err := procx.StartTime("/proc", pid)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately left unreaped until cleanup: an unreaped zombie's /proc
	// entry (and startTime) stays exactly as it was while running, but
	// signalling it is a harmless no-op.
	t.Cleanup(func() { cmd.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, serr := procx.ProcState("/proc", pid)
		if serr == nil && st == 'Z' {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid %d never became a zombie (state=%q err=%v)", pid, string(rune(st)), serr)
		}
		time.Sleep(5 * time.Millisecond)
	}

	logf, logs := capturedLogs()
	l := NewLocal(p, "x", exitFallbackPane(fakeDialer(exitFallbackFake()), logf))
	// A small graceful window: the zombie is never going to go away, so
	// there is nothing to gain from waiting exitFallbackTimeout for it.
	err = l.ExitClaude(context.Background(), &session.TmuxRef{SocketPath: "/s", PaneID: "%7"}, pid, startTime, 100*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(pid)) {
		t.Fatalf("err = %v, want a conflict naming pid %d", err, pid)
	}
	if !anyContains(logs(), "SIGTERM") || !anyContains(logs(), "SIGKILL") {
		t.Errorf("expected both SIGTERM and SIGKILL escalation logged before giving up: %v", logs())
	}
}
