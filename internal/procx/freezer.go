package procx

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// ProcState returns the state letter (R, S, D, T, Z, …) of /proc/<pid>/stat.
func ProcState(procRoot string, pid int) (byte, error) {
	path := filepath.Join(procRoot, strconv.Itoa(pid), "stat")
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	rp := bytes.LastIndexByte(data, ')')
	if rp < 0 || rp+2 >= len(data) {
		return 0, fmt.Errorf("parse %s: malformed", path)
	}
	return data[rp+2], nil
}

// Freezer holds a stopped pid; Thaw releases it. If the owning process dies
// first, the helper releases it on pipe EOF (spec §6.1).
type Freezer struct {
	cmd      *exec.Cmd
	control  *os.File // write end; helper holds the read end on fd 3
	stderr   *bytes.Buffer
	pid      int
	thawOnce sync.Once
	thawErr  error
}

// checkStart hardcodes "/proc": Freeze/RunFreezerHelper act on the live
// kernel via syscall.Kill, so a configurable procRoot would not make them
// meaningfully testable (ProcState, the pure-I/O helper, takes procRoot).
func checkStart(pid int, startTime string) error {
	if startTime == "" {
		return fmt.Errorf("pid %d: empty start time (refusing to signal an unverified pid)", pid)
	}
	st, err := StartTime("/proc", pid)
	if err != nil {
		return fmt.Errorf("pid %d: %w", pid, err)
	}
	if st != startTime {
		return fmt.Errorf("pid %d: start time %s != expected %s (pid reused)", pid, st, startTime)
	}
	return nil
}

// Freeze re-execs selfExe as `internal-freezer <pid> <start>` and waits for
// its "stopped" acknowledgement.
func Freeze(selfExe string, pid int, startTime string) (*Freezer, error) {
	if err := checkStart(pid, startTime); err != nil {
		return nil, fmt.Errorf("freeze: %w", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("freeze: pipe: %w", err)
	}
	cmd := exec.Command(selfExe, "internal-freezer", strconv.Itoa(pid), startTime)
	cmd.ExtraFiles = []*os.File{r} // fd 3 in the child
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		r.Close()
		w.Close()
		return nil, fmt.Errorf("freeze: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		r.Close()
		w.Close()
		return nil, fmt.Errorf("freeze: start helper %s: %w", selfExe, err)
	}
	r.Close() // only the helper holds the read end now
	line, rerr := bufio.NewReader(stdout).ReadString('\n')
	if rerr != nil || strings.TrimSpace(line) != "stopped" {
		w.Close()
		cmd.Wait()
		return nil, fmt.Errorf("freeze pid %d: helper did not stop it: %q %s", pid, strings.TrimSpace(line), strings.TrimSpace(stderr.String()))
	}
	return &Freezer{cmd: cmd, control: w, stderr: stderr, pid: pid}, nil
}

// Thaw writes "thaw\n", closes the pipe and waits for the helper to exit.
// It is idempotent: a repeat call is a no-op that returns the first call's
// result, so callers can freely pair an explicit Thaw with a deferred one.
func (f *Freezer) Thaw() error {
	f.thawOnce.Do(func() {
		if _, err := f.control.Write([]byte("thaw\n")); err != nil && !errors.Is(err, os.ErrClosed) && !errors.Is(err, syscall.EPIPE) {
			f.thawErr = fmt.Errorf("thaw pid %d: %w", f.pid, err)
			return
		}
		f.control.Close()
		if err := f.cmd.Wait(); err != nil {
			f.thawErr = fmt.Errorf("thaw pid %d: helper: %w: %s", f.pid, err, strings.TrimSpace(f.stderr.String()))
		}
	})
	return f.thawErr
}

// RunFreezerHelper is the helper's main: SIGSTOP, ack on stdout, block on
// control, SIGCONT on data or EOF. It ignores terminal signals so it cannot
// die before thawing. The start time is re-checked before every kill.
func RunFreezerHelper(pid int, startTime string, control *os.File) error {
	signal.Ignore(syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGPIPE, syscall.SIGQUIT)
	if err := checkStart(pid, startTime); err != nil {
		return fmt.Errorf("freezer: %w", err)
	}
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		return fmt.Errorf("freezer: SIGSTOP %d: %w", pid, err)
	}
	fmt.Fprintln(os.Stdout, "stopped")
	buf := make([]byte, 16)
	control.Read(buf) // data ("thaw") or EOF (owner died): either way, thaw
	if err := checkStart(pid, startTime); err != nil {
		return nil // the target is gone or replaced: nothing to thaw
	}
	if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
		return fmt.Errorf("freezer: SIGCONT %d: %w", pid, err)
	}
	return nil
}
