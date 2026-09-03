package remote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	var typed string
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "send-keys ") {
			typed = c
		}
	}
	if !strings.Contains(typed, "fg") || !strings.Contains(typed, `%7`) {
		t.Errorf("Thaw typed %q into the pane; want an `fg` sent to %%7 (calls: %v)", typed, f.Calls)
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
