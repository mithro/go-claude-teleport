package remote

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

	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

// writeProcStat writes a /proc/<pid>/stat + cmdline pair with an explicit
// process group and foreground (tpgid) group, which fakeProcRoot's fixed
// template cannot express. Field layout is the kernel's:
// pid (comm) state ppid pgrp session tty_nr tpgid …
func writeProcStat(t *testing.T, root string, pid, ppid, pgrp, tpgid int, comm string) {
	t.Helper()
	dir := filepath.Join(root, fmt.Sprint(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := fmt.Sprintf("%d (%s) T %d %d %d 34816 %d 0 0 0 0 0 0 0 0 0 20 0 1 0 777 0 0", pid, comm, ppid, pgrp, pgrp, tpgid)
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(comm+"\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestThawRestoresForegroundWhenThisLocalNeverFroze covers B2. A dropped
// link, a killed runner or a plain `continue` means the Local that thaws is
// a DIFFERENT process from the one that froze: its freezers map is empty.
// The old code returned nil right there, so the pane shell kept the
// terminal, the SIGCONT'd Claude re-stopped on SIGTTIN, and the "/exit" of
// spec §6.3 was typed into the shell instead. Thaw must therefore attempt
// the foreground restore unconditionally — its own guards no-op when there
// is genuinely nothing to restore.
func TestThawRestoresForegroundWhenThisLocalNeverFroze(t *testing.T) {
	p := testPaths(t)
	proc := t.TempDir()
	writeProcStat(t, proc, 100, 1, 100, 100, "bash")     // the pane's shell, holding the pty
	writeProcStat(t, proc, 5150, 100, 5150, 100, "node") // thawed claude, still not the foreground

	f := &tmuxx.Fake{Default: []string{}, Replies: map[string][]string{
		`list-panes -t "%7" -F "#{pane_pid}"`: {"100"},
	}}
	restored := false
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: fakeDialer(f), Sleep: func(time.Duration) {
		// The shell has now run `fg`: claude is the foreground group again.
		writeProcStat(t, proc, 5150, 100, 5150, 5150, "node")
		restored = true
	}})
	// This Local never froze 5150 — freezers is empty, exactly as it is in
	// a fresh runner/serve process.
	if len(l.freezers) != 0 {
		t.Fatalf("fixture: freezers = %v, want empty", l.freezers)
	}

	ref := &session.TmuxRef{SocketPath: "/s", Session: "work", WindowID: "@1", PaneID: "%7"}
	if err := l.Thaw(context.Background(), 5150, ref); err != nil {
		t.Fatalf("Thaw: %v", err)
	}
	var typed []string
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "send-keys ") {
			typed = append(typed, c)
		}
	}
	want := []string{`send-keys -l -t "%7" " 'fg'"`, `send-keys -H -t "%7" 0d`}
	if diff := cmp.Diff(want, typed); diff != "" {
		t.Errorf("Thaw typed the wrong thing into the pane (-want +got):\n%s\ncalls: %v", diff, f.Calls)
	}
	if !restored {
		t.Errorf("Thaw never waited for the foreground to come back")
	}
}

// TestThawNoRefIsStillANoop keeps the unconditional restore honest: with no
// tmux ref (or no tmux at all) there is nothing to restore and Thaw must
// still succeed silently rather than erroring for a pid it never froze.
func TestThawNoRefIsStillANoop(t *testing.T) {
	l := NewLocal(testPaths(t), "x", LocalOptions{ProcRoot: t.TempDir(), Sleep: func(time.Duration) {}})
	if err := l.Thaw(context.Background(), 5150, nil); err != nil {
		t.Fatalf("Thaw(nil ref): %v", err)
	}
}

// TestThawLeavesAForeignPaneAlone pins the pane_pid check: the ref must
// name the pane whose terminal the stopped job actually lost, or `fg`
// would be typed at somebody else's shell. Matching the holder's comm
// against a hardcoded shell list could not tell the two apart.
func TestThawLeavesAForeignPaneAlone(t *testing.T) {
	p := testPaths(t)
	proc := t.TempDir()
	writeProcStat(t, proc, 100, 1, 100, 100, "bash")
	writeProcStat(t, proc, 5150, 100, 5150, 100, "node")

	f := &tmuxx.Fake{Default: []string{}, Replies: map[string][]string{
		// %7 is a different pane: its own process is pid 999, not the 100
		// that holds pid 5150's terminal.
		`list-panes -t "%7" -F "#{pane_pid}"`: {"999"},
	}}
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: fakeDialer(f), Sleep: func(time.Duration) {
		t.Error("must not poll: nothing was typed")
	}})
	ref := &session.TmuxRef{SocketPath: "/s", Session: "work", WindowID: "@1", PaneID: "%7"}
	if err := l.Thaw(context.Background(), 5150, ref); err != nil {
		t.Fatalf("Thaw: %v", err)
	}
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "send-keys ") {
			t.Errorf("typed %q into a pane that does not hold the terminal", c)
		}
	}
}

// TestThawForegroundPollHonoursContext keeps a cancelled job from sitting
// out the whole foregroundTimeout: the poll must return as soon as the
// context is done.
func TestThawForegroundPollHonoursContext(t *testing.T) {
	p := testPaths(t)
	proc := t.TempDir()
	writeProcStat(t, proc, 100, 1, 100, 100, "bash")
	writeProcStat(t, proc, 5150, 100, 5150, 100, "node") // never regains the terminal

	f := &tmuxx.Fake{Default: []string{}, Replies: map[string][]string{
		`list-panes -t "%7" -F "#{pane_pid}"`: {"100"},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: fakeDialer(f), Sleep: func(time.Duration) { cancel() }})
	ref := &session.TmuxRef{SocketPath: "/s", Session: "work", WindowID: "@1", PaneID: "%7"}
	err := l.Thaw(ctx, 5150, ref)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Thaw = %v, want context.Canceled", err)
	}
}
