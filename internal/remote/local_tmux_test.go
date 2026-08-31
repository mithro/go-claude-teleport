package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

const describeCmd = `list-panes -a -F "#{session_name}	#{session_group}	#{window_id}	#{window_index}	#{window_name}	#{pane_id}	#{pane_current_path}	#{pane_current_command}	#{pane_pid}	#{history_size}	#{pane_title}"`

func fakeDialer(f *tmuxx.Fake) tmuxx.Dialer {
	return func(context.Context, string) (tmuxx.Transport, error) { return f, nil }
}

func TestLocalInventoryTmuxDescribesPane(t *testing.T) {
	p := testPaths(t)
	f := &tmuxx.Fake{Replies: map[string][]string{
		describeCmd: {"main\tmain\t@3\t2\tclaude\t%7\t/home/alice/x\tclaude\t5150\t9\tt"},
		`show-options -wv -t "@3" automatic-rename`: {"off"},
	}}
	l := NewLocal(p, "/usr/local/bin/claude-teleport", LocalOptions{ProcRoot: "/proc", Tmux: fakeDialer(f), TmuxSocketDir: t.TempDir()})
	facts, err := l.InventoryTmux(context.Background(), &session.TmuxRef{SocketPath: "/tmp/tmux-1000/default", Session: "main", WindowID: "@3", PaneID: "%7"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if facts.SocketPath != "/tmp/tmux-1000/default" || facts.Group != "main" || facts.AutoRename {
		t.Errorf("facts = %+v", facts)
	}
}

func TestLocalInventoryTmuxUnavailable(t *testing.T) {
	l := NewLocal(testPaths(t), "x", LocalOptions{ProcRoot: "/proc"})
	_, err := l.InventoryTmux(context.Background(), nil, "")
	if e, ok := err.(*Error); !ok || e.Code != "unavailable" {
		t.Fatalf("err = %v, want unavailable", err)
	}
}

func TestLocalCaptureWritesJobFile(t *testing.T) {
	p := testPaths(t)
	f := &tmuxx.Fake{Replies: map[string][]string{`capture-pane -epJ -S - -t "%7"`: {"hello", "world"}}}
	l := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc", Tmux: fakeDialer(f)})
	jobID := "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"
	if err := l.Capture(context.Background(), &session.TmuxRef{SocketPath: "/s", PaneID: "%7"}, jobID); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(job.Dir(p.DataDir, jobID), "capture.txt"))
	if err != nil || string(b) != "hello\nworld\n" {
		t.Errorf("capture.txt = %q %v", b, err)
	}
}

func TestLocalOpenWindowAndKill(t *testing.T) {
	p := testPaths(t)
	f := &tmuxx.Fake{Replies: map[string][]string{
		`list-sessions -F "#{session_name}	#{session_group}"`:                                                     {},
		`new-session -d -s "work" -n "claude" -c "/home/alice/x" -P -F "#{pane_id}	#{window_id}	#{session_name}"`: {"%1\t@1\twork"},
		`kill-window -t "@1"`: {},
	}}
	l := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc", Tmux: fakeDialer(f)})
	ref, err := l.OpenWindow(context.Background(), &tmuxx.Plan{SocketPath: "/s", Group: "work", WindowName: "claude", AutoRename: true, Cwd: "/home/alice/x", CreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if ref.PaneID != "%1" || ref.SocketPath != "/s" {
		t.Errorf("ref = %+v", ref)
	}
	if err := l.KillWindow(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
}
