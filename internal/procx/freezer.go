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

// statField returns field n (1-based, as proc(5) numbers them) of
// /proc/<pid>/stat as an int. Fields are counted after the comm field, so
// a comm containing spaces or parens cannot shift them.
func statField(procRoot string, pid, n int) (int, error) {
	path := filepath.Join(procRoot, strconv.Itoa(pid), "stat")
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	rp := bytes.LastIndexByte(data, ')')
	if rp < 0 {
		return 0, fmt.Errorf("parse %s: malformed", path)
	}
	fields := strings.Fields(string(data[rp+1:])) // fields[0] is field 3 (state)
	i := n - 3
	if i < 0 || i >= len(fields) {
		return 0, fmt.Errorf("parse %s: no field %d", path, n)
	}
	v, err := strconv.Atoi(fields[i])
	if err != nil {
		return 0, fmt.Errorf("parse %s field %d: %w", path, n, err)
	}
	return v, nil
}

// ProcGroup returns pid's process group id (field 5 of /proc/<pid>/stat).
func ProcGroup(procRoot string, pid int) (int, error) { return statField(procRoot, pid, 5) }

// ForegroundGroup returns the foreground process group of pid's controlling
// terminal (tpgid, field 8), or -1 when it has no controlling terminal.
//
// It is how a caller tells a process that merely got SIGCONT from one that
// actually has the terminal back: a job-control shell takes the pty away
// from its foreground job the moment that job stops, and the resumed job
// then re-stops on SIGTTIN at its next read (spec §6.1 freeze/thaw).
func ForegroundGroup(procRoot string, pid int) (int, error) { return statField(procRoot, pid, 8) }

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
	// The owner's group, read once while it is still alive: after it dies
	// (the pipe EOF that thaws) getppid() is 1 and the guard would be lost.
	ownerPgid, err := syscall.Getpgid(os.Getppid())
	if err != nil {
		ownerPgid = 0
	}
	if err := signalTarget(pid, startTime, syscall.SIGSTOP, ownerPgid); err != nil {
		return fmt.Errorf("freezer: %w", err)
	}
	fmt.Fprintln(os.Stdout, "stopped")
	buf := make([]byte, 16)
	control.Read(buf) // data ("thaw") or EOF (owner died): either way, thaw
	if err := checkStart(pid, startTime); err != nil {
		return nil // the target is gone or replaced: nothing to thaw
	}
	if err := signalTarget(pid, startTime, syscall.SIGCONT, ownerPgid); err != nil {
		return fmt.Errorf("freezer: %w", err)
	}
	return nil
}

// groupToSignal returns the process group freeze/thaw should signal for
// pid, or 0 meaning "signal the bare pid instead".
//
// ownerPgid is the process group of whoever owns the freeze (the freezer
// helper's parent). Refused, in order: an unreadable group, pgid 0 or 1
// (never a real job), the caller's own group and the owner's group —
// stopping either of those would stop the only processes that can ever
// thaw the target.
func groupToSignal(pid, selfPgid, ownerPgid int, getpgid func(int) (int, error), warnf func(string, ...any)) int {
	pgid, err := getpgid(pid)
	switch {
	case err != nil:
		warnf("freezer: getpgid %d: %v; signalling the pid alone", pid, err)
	case pgid <= 1:
		warnf("freezer: pid %d has process group %d; signalling the pid alone", pid, pgid)
	case pgid == selfPgid:
		warnf("freezer: pid %d shares the freezer's process group %d; signalling the pid alone", pid, pgid)
	case pgid == ownerPgid:
		warnf("freezer: pid %d shares its owner's process group %d; signalling the pid alone", pid, pgid)
	default:
		return pgid
	}
	return 0
}

// signalTarget sends sig to pid's whole process group, falling back to the
// bare pid when the group cannot be determined or would be unsafe to
// signal (see groupToSignal).
//
// Claude is a job: an interactive shell puts it in its own process group
// and gives that group the pty. SIGSTOPping only the leader leaves the
// rest of the group running (a child could still write the transcript we
// are copying), and the group is what the shell and the terminal both
// think in — so the group is what freeze and thaw must move, together
// (spec §6.1).
//
// checkStart is re-run before every signal and the group is derived from
// the pid only after that check passes: a group is never signalled unless
// the pid that names it was verified in the same breath.
func signalTarget(pid int, startTime string, sig syscall.Signal, ownerPgid int) error {
	if err := checkStart(pid, startTime); err != nil {
		return err
	}
	warnf := func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	if pgid := groupToSignal(pid, syscall.Getpgrp(), ownerPgid, syscall.Getpgid, warnf); pgid > 0 {
		if err := syscall.Kill(-pgid, sig); err != nil {
			return fmt.Errorf("%v process group %d (pid %d): %w", sig, pgid, pid, err)
		}
		return nil
	}
	if err := syscall.Kill(pid, sig); err != nil {
		return fmt.Errorf("%v %d: %w", sig, pid, err)
	}
	return nil
}
