package cli

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"unsafe"
)

// Swappable in tests: CI has no `claude` on PATH, and a test must never
// really exec or chdir. On success execveFn never returns — the placeholder
// becomes the claude process.
var (
	execveFn    = syscall.Exec
	lookPathFn  = exec.LookPath
	chdirFn     = os.Chdir
	stdinTTYFn  = func() bool { return isTTY(os.Stdin) }
	stdoutTTYFn = func() bool { return isTTY(os.Stdout) }
)

// isTTY reports whether f is a terminal, decided by the TCGETS ioctl like
// isatty(3) — a ModeCharDevice check would misclassify /dev/null.
func isTTY(f *os.File) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}

// readLineInterruptible reads one line from r, treating SIGINT (Ctrl-C at
// the pane prompt) as an error, which Decide turns into "skip — leave a
// shell in this pane".
func readLineInterruptible(r io.Reader) (string, error) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	type lineResult struct {
		s   string
		err error
	}
	ch := make(chan lineResult, 1)
	go func() {
		s, err := bufio.NewReader(r).ReadString('\n')
		ch <- lineResult{s, err}
	}()
	select {
	case <-sig:
		return "", errors.New("interrupted")
	case res := <-ch:
		return res.s, res.err
	}
}
