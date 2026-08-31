package procx

import (
	"errors"
	"fmt"
	"time"
)

// ErrTimeout is returned (wrapped) when a bounded wait expires.
var ErrTimeout = errors.New("timeout")

// WaitGone polls until pid (with startTime) is no longer alive. t scans the
// table (procx.Scan("/proc") in production); sleep is injectable for tests.
func WaitGone(t func() (*Table, error), pid int, startTime string, timeout, poll time.Duration, sleep func(time.Duration)) error {
	var waited time.Duration
	for {
		tb, err := t()
		if err != nil {
			return fmt.Errorf("wait for pid %d to exit: %w", pid, err)
		}
		if !tb.Alive(pid, startTime) {
			return nil
		}
		if waited >= timeout {
			return fmt.Errorf("pid %d still alive after %s: %w", pid, timeout, ErrTimeout)
		}
		sleep(poll)
		waited += poll
	}
}
