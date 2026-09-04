//go:build tmuxlive

package remote

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

// TestMain turns this test binary into the freezer helper (procx.Freeze
// re-execs os.Executable(), and the helper must be the REAL one, restore
// hook included) and into the fake Claude the pane runs.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "internal-freezer":
			pid, _ := strconv.Atoi(os.Args[2])
			var restore procx.RestoreFunc
			if len(os.Args) > 5 {
				restore = tmuxx.FreezerRestore(os.Args[4], os.Args[5])
			}
			if err := procx.RunFreezerHelper(pid, os.Args[3], os.NewFile(3, "control"), restore); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		case "freeze-owner":
			// Freeze the target, announce, then hang until killed: the
			// helper is then the only one left to thaw it AND to hand it
			// the terminal back.
			pid, _ := strconv.Atoi(os.Args[2])
			self, _ := os.Executable()
			if _, err := procx.Freeze(self, pid, os.Args[3], procx.PaneRef{SocketPath: os.Args[4], PaneID: os.Args[5]}); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Println("frozen")
			select {}
		case "fake-claude":
			fakeClaude(os.Args[2])
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

// fakeClaude stands in for Claude Code in the pane. Three behaviours
// matter and all three are Claude's:
//
//   - it READS the terminal, so a bare SIGCONT is not enough — a
//     background read earns SIGTTIN and stops it again;
//   - it is not sitting INSIDE that read while idle. Claude is node/ink,
//     whose input is epoll-driven, so a SIGCONT finds it waiting for
//     readability (which no signal punishes) rather than in read(2) — the
//     query below therefore always goes out before the SIGTTIN re-stop;
//   - the moment it is continued it re-issues a terminal query, whose
//     answer tmux writes into the pane's INPUT. Claude asks for the colour
//     scheme (CSI ?996n); this tmux answers only the cursor-position
//     report (CSI 6n, probe-verified), and the damage is identical: the
//     reply is read as typed text by whoever holds the terminal — the
//     pane's shell, if the SIGCONT arrived before `fg` (FR-1).
//
// Every SIGCONT is also appended to logPath as "fg=<tpgid> pgid=<pgid>":
// who owned the terminal AT THE MOMENT the job was continued is the
// ruling's actual requirement, and unlike the pane damage it is not a race.
func fakeClaude(logPath string) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake-claude:", err)
		os.Exit(1)
	}
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGCONT)
	go func() {
		for range ch {
			tty.WriteString("\x1b[6n") // first: the query is what pollutes
			fg, _ := procx.ForegroundGroup("/proc", os.Getpid())
			pgid, _ := procx.ProcGroup("/proc", os.Getpid())
			if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				fmt.Fprintf(f, "fg=%d pgid=%d\n", fg, pgid)
				f.Close()
			}
		}
	}()
	fd := int(tty.Fd())
	buf := make([]byte, 256)
	for {
		var set syscall.FdSet
		set.Bits[fd/64] |= 1 << (uint(fd) % 64)
		syscall.Select(fd+1, &set, nil, nil, nil) // no SIGTTIN: only read(2) earns that
		syscall.Read(fd, buf)
	}
}

// TestThawFg is FR-1 against real hosts' machinery: a real tmux server, a
// real job-control bash in the pane, a real freezer helper and a real
// terminal-querying job.
//
// The v0.10 order — SIGCONT, then type `fg` — loses: the query answer
// lands on bash's line first, ` 'fg'` is appended to it, bash reports a
// syntax error and the thawed job stays stopped in the background with the
// shell owning the tty. Thaw must therefore hand the terminal over BEFORE
// anything continues the job (ruling R-P3-PROOF-5 item 1).
//
// The name is deliberately short: StartTestServer builds the socket path
// from the temp dir AND the test name, and a unix socket path over ~108
// bytes cannot be bound.
func TestThawFg(t *testing.T) {
	p := startFrozenJob(t)
	l := NewLocal(testPaths(t), p.self, LocalOptions{
		ProcRoot: "/proc", Tmux: tmuxx.DialControl,
		Logf: func(format string, a ...any) { t.Logf(format, a...) },
	})
	if err := l.Freeze(p.ctx, p.job, p.start, p.ref); err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	p.waitFrozen(t)

	if err := l.Thaw(p.ctx, p.job, p.ref); err != nil {
		t.Fatalf("Thaw: %v\npane:\n%s", err, p.pane(t))
	}
	p.assertRestored(t)
	p.assertContAfterFg(t)
}

// TestThawOwnerDied is the same ruling on the freezer helper's own path
// (R-P3-F1 + R-P3-PROOF-5 item 1): the owner is SIGKILLed without ever
// thawing, so the helper SIGCONTs on pipe EOF and only then can type. The
// job's query answer is therefore already sitting on the shell's line —
// which is exactly why that path terminates the line with a CR of its own
// before the `fg` (ForegroundOptions.ClearLine).
func TestThawOwnerDied(t *testing.T) {
	p := startFrozenJob(t)
	owner := exec.Command(p.self, "freeze-owner", strconv.Itoa(p.job), p.start, p.sock, p.ref.PaneID)
	owner.Stderr = os.Stderr
	out, err := owner.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { owner.Process.Kill(); owner.Wait() }()
	buf := make([]byte, len("frozen\n"))
	if _, err := out.Read(buf); err != nil || !strings.HasPrefix(string(buf), "frozen") {
		t.Fatalf("owner did not report the freeze: %q %v", buf, err)
	}
	p.waitFrozen(t)

	if err := owner.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	owner.Wait()
	p.assertRestored(t)
}

// livePane is one real tmux pane running a real job-control bash with a
// real fake Claude stopped in it, plus everything the thaw paths need to
// name it.
type livePane struct {
	ctx     context.Context
	tr      tmuxx.Transport
	sock    string
	self    string
	ref     *session.TmuxRef
	panePID int
	job     int
	start   string
	contLog string
}

// startFrozenJob builds that pane and leaves fake-claude running in it as
// the shell's foreground job, ready to be frozen.
func startFrozenJob(t *testing.T) *livePane {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not installed: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sock, _ := tmuxx.StartTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	tr, err := tmuxx.DialControl(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })

	// A real job-control shell with none of the developer's rc files:
	// HOME is the throwaway dir the pane starts in.
	home := t.TempDir()
	if _, err := tr.Run(ctx, fmt.Sprintf("set-option -g default-shell %s", tmuxx.Quote(bash))); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Run(ctx, fmt.Sprintf("set-environment -g HOME %s", tmuxx.Quote(home))); err != nil {
		t.Fatal(err)
	}
	ref, err := tmuxx.OpenWindow(ctx, tr, &tmuxx.Plan{SocketPath: sock, Group: "work", WindowName: "job", Cwd: home, CreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	panePID, err := tmuxx.PanePID(ctx, tr, ref.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	waitFg(t, panePID, panePID) // the shell holds its own terminal

	contLog := filepath.Join(t.TempDir(), "cont.log")
	if err := tmuxx.TypeCommand(ctx, tr, ref.PaneID, []string{self, "fake-claude", contLog}); err != nil {
		t.Fatal(err)
	}
	job := waitFgNot(t, panePID, panePID)
	start, err := procx.StartTime("/proc", job)
	if err != nil {
		t.Fatal(err)
	}
	return &livePane{ctx: ctx, tr: tr, sock: sock, self: self, ref: ref, panePID: panePID, job: job, start: start, contLog: contLog}
}

func (p *livePane) pane(t *testing.T) string {
	t.Helper()
	b, err := tmuxx.Capture(p.ctx, p.tr, p.ref.PaneID)
	if err != nil {
		return fmt.Sprintf("(capture failed: %v)", err)
	}
	return string(b)
}

// waitFrozen waits for the job to be stopped AND the shell to have taken
// the terminal back — the state every thaw path starts from.
func (p *livePane) waitFrozen(t *testing.T) {
	t.Helper()
	waitState(t, p.job, 'T')
	waitFg(t, p.panePID, p.panePID)
}

// assertRestored is FR-1's outcome, common to both thaw paths: the job has
// the terminal and KEEPS it, rather than re-stopping on SIGTTIN.
func (p *livePane) assertRestored(t *testing.T) {
	t.Helper()
	job, panePID := p.job, p.panePID
	waitFg(t, panePID, job)
	// Not a momentary window between SIGCONT and a SIGTTIN re-stop.
	time.Sleep(500 * time.Millisecond)
	if st, err := procx.ProcState("/proc", job); err != nil || st == 'T' {
		t.Fatalf("job %d re-stopped: state = %q (%v)\npane:\n%s", job, string(rune(st)), err, p.pane(t))
	}
	if fg, err := procx.ForegroundGroup("/proc", job); err != nil || fg != job {
		t.Fatalf("foreground group of job %d's terminal = %d (%v), want %d", job, fg, err, job)
	}
}

// assertContAfterFg is the ordinary path's extra, and the ruling's own
// words: nothing may continue the job until the shell has handed the
// terminal over. The job itself is the only honest witness — it records
// who owned the tty each time it was continued — and unlike the pane
// damage below, that record is not a race.
//
// The owner-died path cannot meet this: there the SIGCONT comes first by
// design, which is why it clears the shell's line before typing.
func (p *livePane) assertContAfterFg(t *testing.T) {
	t.Helper()
	cont, err := os.ReadFile(p.contLog)
	if err != nil || len(cont) == 0 {
		t.Fatalf("the job recorded no SIGCONT at all (%v)", err)
	}
	first := strings.SplitN(strings.TrimSpace(string(cont)), "\n", 2)[0]
	if first != fmt.Sprintf("fg=%d pgid=%d", p.job, p.job) {
		t.Errorf("the first SIGCONT reached the job while the shell still owned the terminal: %q, want fg=%d pgid=%d", first, p.job, p.job)
	}
	// The shell must never have been asked to run the query answer, nor a
	// `fg` glued onto it: that is exactly what the real failure looked like.
	pane := p.pane(t)
	for _, junk := range []string{"command not found", "syntax error"} {
		if strings.Contains(pane, junk) {
			t.Errorf("the pane's shell was fed terminal-reply junk (%q):\n%s", junk, pane)
		}
	}
}

// waitState waits for pid's /proc state letter to be want.
func waitState(t *testing.T, pid int, want byte) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		st, err := procx.ProcState("/proc", pid)
		if err == nil && st == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid %d state = %q (%v), want %q", pid, string(rune(st)), err, string(rune(want)))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitFg waits for pid's terminal foreground group to be want.
func waitFg(t *testing.T, pid, want int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		fg, err := procx.ForegroundGroup("/proc", pid)
		if err == nil && fg == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("foreground group of pid %d's terminal = %d (%v), want %d", pid, fg, err, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitFgNot waits for pid's terminal foreground group to be anything but
// notWant (a job took the pty) and returns it.
func waitFgNot(t *testing.T, pid, notWant int) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		fg, err := procx.ForegroundGroup("/proc", pid)
		if err == nil && fg > 0 && fg != notWant {
			return fg
		}
		if time.Now().After(deadline) {
			t.Fatalf("no job ever took pid %d's terminal (fg=%d, %v)", pid, fg, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
