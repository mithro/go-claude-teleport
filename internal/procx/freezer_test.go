package procx

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// startSleep spawns `sleep 60` and returns its pid and start time.
func startSleep(t *testing.T) (int, string) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	st, err := StartTime("/proc", cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid, st
}

func waitState(t *testing.T, pid int, want byte) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s, err := ProcState("/proc", pid); err == nil && s == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	s, err := ProcState("/proc", pid)
	t.Fatalf("pid %d state = %c (%v), want %c", pid, s, err, want)
}

func TestFreezeThaw(t *testing.T) {
	pid, st := startSleep(t)
	self, _ := os.Executable()
	f, err := Freeze(self, pid, st)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, pid, 'T')
	if err := f.Thaw(); err != nil {
		t.Fatal(err)
	}
	waitState(t, pid, 'S')
}

func TestThawIsIdempotent(t *testing.T) {
	pid, st := startSleep(t)
	self, _ := os.Executable()
	f, err := Freeze(self, pid, st)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, pid, 'T')
	if err := f.Thaw(); err != nil {
		t.Fatal(err)
	}
	if err := f.Thaw(); err != nil {
		t.Fatalf("second Thaw: %v, want nil", err)
	}
	waitState(t, pid, 'S')
}

func TestFreezeRefusesWrongStartTime(t *testing.T) {
	pid, _ := startSleep(t)
	self, _ := os.Executable()
	if _, err := Freeze(self, pid, "1"); err == nil {
		t.Fatal("wrong start time must refuse")
	}
	if _, err := Freeze(self, pid, ""); err == nil {
		t.Fatal("empty start time must refuse")
	}
	if s, _ := ProcState("/proc", pid); s == 'T' {
		t.Fatal("target must not have been stopped")
	}
}

// The guarantee: when the owner dies (SIGKILL, no cleanup possible) the
// helper sees pipe EOF and SIGCONTs the target.
func TestHelperThawsWhenOwnerDies(t *testing.T) {
	pid, st := startSleep(t)
	self, _ := os.Executable()
	owner := exec.Command(self, "freeze-owner", strconv.Itoa(pid), st)
	owner.Stderr = os.Stderr
	out, err := owner.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "frozen\n" {
		owner.Process.Kill()
		t.Fatalf("owner said %q (%v)", line, err)
	}
	waitState(t, pid, 'T')
	if err := owner.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	owner.Wait()
	waitState(t, pid, 'S')
}

// startGroup spawns `sh` in its own process group (never this test
// binary's) with one background child in the same group, and returns the
// leader's pid and start time plus the child's pid.
func startGroup(t *testing.T) (leader int, startTime string, child int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 60 & echo $!; wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGCONT)
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cmd.Wait()
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	child, err = strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("child pid %q: %v", line, err)
	}
	st, err := StartTime("/proc", cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid, st, child
}

// TestFreezeStopsTheWholeProcessGroup pins spec §6.1: freeze/thaw move the
// target's process GROUP, not just its leader — that group is what an
// interactive shell made of the job and what the pty's foreground is
// tracked as, and a still-running group member could keep writing the very
// transcript the freeze exists to hold still.
func TestFreezeStopsTheWholeProcessGroup(t *testing.T) {
	leader, st, child := startGroup(t)
	if pg, err := ProcGroup("/proc", child); err != nil || pg != leader {
		t.Fatalf("child %d process group = %d (%v), want the leader %d", child, pg, err, leader)
	}
	self, _ := os.Executable()
	f, err := Freeze(self, leader, st)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, leader, 'T')
	waitState(t, child, 'T')
	if err := f.Thaw(); err != nil {
		t.Fatal(err)
	}
	waitState(t, leader, 'S')
	waitState(t, child, 'S')
}

// TestGroupToSignal pins the refusals that keep a group signal safe.
// Deliberately a pure-function test: exercising them for real would mean
// SIGSTOPping this test binary's own process group, which no timeout can
// recover from.
func TestGroupToSignal(t *testing.T) {
	const selfPgid, ownerPgid = 4242, 5252
	silent := func(string, ...any) {}
	for _, tc := range []struct {
		name string
		pgid int
		err  error
		want int
	}{
		{"a job of its own", 900, nil, 900},
		{"unreadable group", 900, syscall.ESRCH, 0},
		{"group 0", 0, nil, 0},
		{"init's group", 1, nil, 0},
		{"the freezer's own group", selfPgid, nil, 0},
		{"the owner's group", ownerPgid, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := groupToSignal(900, selfPgid, ownerPgid, func(int) (int, error) { return tc.pgid, tc.err }, silent)
			if got != tc.want {
				t.Errorf("groupToSignal = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestForegroundGroupTracksTheShell: an interactive shell hands the pty to
// its foreground job, and takes it back the moment that job stops — which
// is what ForegroundGroup exists to report (spec §6.1).
func TestForegroundGroupTracksTheShell(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not installed: %v", err)
	}
	cmd := exec.Command(bash, "--norc", "--noprofile", "-i")
	cmd.Env = append(os.Environ(), "PS1=$ ")
	master, err := pty.Start(cmd) // pty.Start: setsid + the pty as our controlling terminal
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	shell := cmd.Process.Pid
	t.Cleanup(func() { syscall.Kill(-shell, syscall.SIGKILL); cmd.Wait() })
	go io.Copy(io.Discard, master)
	if _, err := io.WriteString(master, "sleep 60\n"); err != nil {
		t.Fatal(err)
	}
	// The job's own process group, given the pty by the shell.
	var job int
	deadline := time.Now().Add(10 * time.Second)
	for {
		fg, err := ForegroundGroup("/proc", shell)
		if err == nil && fg > 0 && fg != shell {
			job = fg
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("shell %d never gave the pty to a job (fg=%d, %v)", shell, fg, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	st, err := StartTime("/proc", job)
	if err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	f, err := Freeze(self, job, st)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, job, 'T')
	// The shell has taken the terminal back: this is the state a bare
	// SIGCONT cannot undo (the resumed job re-stops on SIGTTIN), and the
	// reason remote.Local.Thaw has to ask the shell to foreground it again.
	waitForeground(t, shell, shell)
	if err := f.Thaw(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(master, "fg\n"); err != nil {
		t.Fatal(err)
	}
	waitForeground(t, shell, job)
	waitState(t, job, 'S')
}

func waitForeground(t *testing.T, pid, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		fg, err := ForegroundGroup("/proc", pid)
		if err == nil && fg == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("foreground group of pid %d's terminal = %d (%v), want %d", pid, fg, err, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
