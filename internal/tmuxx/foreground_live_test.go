//go:build tmuxlive

package tmuxx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/procx"
)

// TestMain turns this test binary into the freezer helper or a "freeze
// owner" when invoked with those argv, so procx.Freeze can re-exec
// os.Executable() and the helper runs the REAL restore hook (the same
// FreezerRestore internal/cli's internal-freezer wires in).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "internal-freezer":
			pid, _ := strconv.Atoi(os.Args[2])
			if err := procx.RunFreezerHelper(pid, os.Args[3], os.NewFile(3, "control"), FreezerRestore(os.Args[4], os.Args[5])); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		case "freeze-owner":
			// Freeze the target, announce, then hang until killed: the
			// helper must both thaw the target and hand it the pty back.
			pid, _ := strconv.Atoi(os.Args[2])
			self, _ := os.Executable()
			if _, err := procx.Freeze(self, pid, os.Args[3], procx.PaneRef{SocketPath: os.Args[4], PaneID: os.Args[5]}); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Println("frozen")
			select {}
		}
	}
	os.Exit(m.Run())
}

// TestFreezerRestoresPaneForeground is R-P3-F1 end to end,
// against a real tmux pane, a real interactive bash and a real tty reader.
//
// The job is `cat`: like Claude (and unlike `sleep`) it reads the
// terminal, so a bare SIGCONT is not enough — the pane's bash took the pty
// the moment the job stopped, and the resumed reader re-stops on SIGTTIN
// until somebody asks the shell to foreground it again. When the freeze's
// owner is SIGKILLed there is no caller left to do that, so the freezer
// helper must: it was handed the pane at Freeze time.
//
// The name is kept short on purpose: StartTestServer builds the tmux
// socket path out of the test's temp dir AND its name, and a unix socket
// path over ~108 bytes cannot be bound.
func TestFreezerRestoresPaneForeground(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not installed: %v", err)
	}
	sock, _ := StartTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	tr, err := DialControl(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	// A real job-control shell in the pane, with no rc files of the
	// developer's in it: HOME is the throwaway dir the pane starts in.
	home := t.TempDir()
	if _, err := tr.Run(ctx, fmt.Sprintf("set-option -g default-shell %s", Quote(bash))); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Run(ctx, fmt.Sprintf("set-environment -g HOME %s", Quote(home))); err != nil {
		t.Fatal(err)
	}
	ref, err := OpenWindow(ctx, tr, &Plan{SocketPath: sock, Group: "work", WindowName: "job", Cwd: home, CreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	panePID, err := PanePID(ctx, tr, ref.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	waitFg(t, panePID, panePID) // the shell holds its own terminal

	// Start the tty reader and learn the process group the shell gave the
	// pty to: that group is the freeze target.
	if err := TypeCommand(ctx, tr, ref.PaneID, []string{"cat"}); err != nil {
		t.Fatal(err)
	}
	job := waitFgNot(t, panePID, panePID)
	start, err := procx.StartTime("/proc", job)
	if err != nil {
		t.Fatal(err)
	}

	// The owner freezes it and then dies without ever thawing.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	owner := exec.Command(self, "freeze-owner", strconv.Itoa(job), start, sock, ref.PaneID)
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
	waitState(t, job, 'T')
	waitFg(t, panePID, panePID) // the shell has taken the pty back

	if err := owner.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	owner.Wait()

	// The helper's job now: SIGCONT *and* the pty.
	waitFg(t, panePID, job)
	if st, err := procx.ProcState("/proc", job); err != nil || st == 'T' {
		t.Fatalf("job %d state = %q (%v), want anything but T once it has the terminal back", job, string(rune(st)), err)
	}
	// Not a momentary window between SIGCONT and a SIGTTIN re-stop.
	time.Sleep(500 * time.Millisecond)
	st, err := procx.ProcState("/proc", job)
	if err != nil || st == 'T' {
		t.Fatalf("job %d re-stopped: state = %q (%v)", job, string(rune(st)), err)
	}
	if fg, err := procx.ForegroundGroup("/proc", job); err != nil || fg != job {
		t.Fatalf("foreground group of job %d's terminal = %d (%v), want %d", job, fg, err, job)
	}
}

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
