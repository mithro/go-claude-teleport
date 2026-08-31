package procx

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
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
