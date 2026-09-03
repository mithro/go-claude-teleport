package tmuxx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// procTree writes a /proc-shaped tree whose stat lines carry an explicit
// process group AND foreground group (tpgid), the two fields
// RestoreForeground reasons about — fakeProc's fixed template cannot
// express either. Field layout is the kernel's:
// pid (comm) state ppid pgrp session tty_nr tpgid …
func procTree(t *testing.T, pids [][4]int) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range pids {
		pid, ppid, pgrp, tpgid := p[0], p[1], p[2], p[3]
		dir := filepath.Join(root, fmt.Sprint(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		stat := fmt.Sprintf("%d (job) T %d %d %d 34816 %d 0 0 0 0 0 0 0 0 0 20 0 1 0 777 0 0", pid, ppid, pgrp, pgrp, tpgid)
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// stoppedJob is the situation every test here starts from: pid 100 is the
// pane's own process (a job-control shell) and holds the terminal, pid
// 5150 is the stopped job that lost it.
func stoppedJob(t *testing.T) (procRoot string, f *Fake) {
	t.Helper()
	return procTree(t, [][4]int{{100, 1, 100, 100}, {5150, 100, 5150, 100}}),
		&Fake{Default: []string{}, Replies: map[string][]string{
			`list-panes -t "%7" -F "#{pane_pid}"`: {"100"},
		}}
}

// TestRestoreForegroundTypesLiteralFg pins what goes into the pane on the
// ordinary path (ruling R-P3-PROOF-5 item 2): literal bytes and a literal
// CR, and — because nothing has continued the job yet — no CR before them.
func TestRestoreForegroundTypesLiteralFg(t *testing.T) {
	procRoot, f := stoppedJob(t)
	restored := false
	err := RestoreForeground(context.Background(), f, "%7", 5150, ForegroundOptions{
		ProcRoot: procRoot,
		Sleep: func(time.Duration) {
			// The shell has now run `fg`: the job is the foreground again.
			writeTpgid(t, procRoot, 5150, 5150)
			restored = true
		},
	})
	if err != nil {
		t.Fatalf("RestoreForeground: %v", err)
	}
	if !restored {
		t.Error("never waited for the foreground to come back")
	}
	want := []string{
		`list-panes -t "%7" -F "#{pane_pid}"`,
		`send-keys -l -t "%7" " 'fg'"`,
		`send-keys -H -t "%7" 0d`,
	}
	if diff := cmp.Diff(want, f.Calls); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

// TestRestoreForegroundClearLineEndsThePollutedLineFirst covers the
// freezer helper's owner-died path: there the SIGCONT is already done by
// design, so the shell's line may already hold the answer to a terminal
// query the resumed job made. A literal CR of its own ends it before `fg`
// is typed — C-u and C-c are key NAMES and unusable here.
func TestRestoreForegroundClearLineEndsThePollutedLineFirst(t *testing.T) {
	procRoot, f := stoppedJob(t)
	err := RestoreForeground(context.Background(), f, "%7", 5150, ForegroundOptions{
		ProcRoot:  procRoot,
		ClearLine: true,
		Sleep:     func(time.Duration) { writeTpgid(t, procRoot, 5150, 5150) },
	})
	if err != nil {
		t.Fatalf("RestoreForeground: %v", err)
	}
	want := []string{
		`list-panes -t "%7" -F "#{pane_pid}"`,
		`send-keys -H -t "%7" 0d`, // the polluted line, terminated
		`send-keys -l -t "%7" " 'fg'"`,
		`send-keys -H -t "%7" 0d`,
	}
	if diff := cmp.Diff(want, f.Calls); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

// TestRestoreForegroundLogsThePaneWhenFgDidNotTake is ruling
// R-P3-PROOF-5 item 3: a detached runner is the only witness to a pane
// that refused the `fg`, so what the shell made of what it was given has
// to reach the log.
func TestRestoreForegroundLogsThePaneWhenFgDidNotTake(t *testing.T) {
	procRoot, f := stoppedJob(t)
	f.Replies[`capture-pane -epJ -t "%7"`] = []string{"", "$ 997;1n 'fg'", "bash: 997: command not found", ""}
	var log strings.Builder
	err := RestoreForeground(context.Background(), f, "%7", 5150, ForegroundOptions{
		ProcRoot: procRoot,
		Timeout:  time.Nanosecond, // the job never comes back
		Logf:     func(format string, a ...any) { fmt.Fprintf(&log, format+"\n", a...) },
		Sleep:    func(time.Duration) {},
	})
	if !errors.Is(err, ErrNotRestored) {
		t.Fatalf("RestoreForeground = %v, want ErrNotRestored", err)
	}
	for _, want := range []string{"997;1n 'fg'", "command not found"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the log does not show the pane's %q line:\n%s", want, log.String())
		}
	}
}

// writeTpgid rewrites pid's stat with a new foreground (tpgid) group,
// standing in for the shell having run `fg`.
func writeTpgid(t *testing.T, procRoot string, pid, tpgid int) {
	t.Helper()
	stat := fmt.Sprintf("%d (job) S %d %d %d 34816 %d 0 0 0 0 0 0 0 0 0 20 0 1 0 777 0 0", pid, 100, pid, pid, tpgid)
	if err := os.WriteFile(filepath.Join(procRoot, fmt.Sprint(pid), "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
}
