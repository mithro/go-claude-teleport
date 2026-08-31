package tmuxx

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const listSessionsCmd = `list-sessions -F "#{session_name}	#{session_group}"`

func TestOpenWindowCreatesSessionWhenGroupAbsent(t *testing.T) {
	f := &Fake{Replies: map[string][]string{
		listSessionsCmd: {"other\t"},
		`new-session -d -s "work" -n "claude" -c "/home/bob/github/x" -P -F "#{pane_id}	#{window_id}	#{session_name}"`: {"%3\t@1\twork"},
		`set-option -w -t "@1" automatic-rename off`: {},
	}}
	ref, err := OpenWindow(context.Background(), f, &Plan{SocketPath: "/tmp/tmux-1000/default", Group: "work", WindowName: "claude", AutoRename: false, Cwd: "/home/bob/github/x", CreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{listSessionsCmd,
		`new-session -d -s "work" -n "claude" -c "/home/bob/github/x" -P -F "#{pane_id}	#{window_id}	#{session_name}"`,
		`set-option -w -t "@1" automatic-rename off`}, f.Calls); diff != "" {
		t.Errorf("calls (-want +got):\n%s", diff)
	}
	if ref.Session != "work" || ref.WindowID != "@1" || ref.PaneID != "%3" || ref.SocketPath != "/tmp/tmux-1000/default" {
		t.Errorf("ref = %+v", ref)
	}
}

func TestOpenWindowUsesGroupBaseSession(t *testing.T) {
	f := &Fake{Replies: map[string][]string{
		listSessionsCmd: {"work-7\twork", "work\twork", "other\t"},
		`new-window -t "=work:" -n "claude" -c "/home/bob/github/x" -P -F "#{pane_id}	#{window_id}	#{session_name}"`: {"%9\t@4\twork"},
	}}
	ref, err := OpenWindow(context.Background(), f, &Plan{Group: "work", WindowName: "claude", AutoRename: true, Cwd: "/home/bob/github/x"})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Session != "work" || ref.WindowID != "@4" || ref.PaneID != "%9" {
		t.Errorf("ref = %+v", ref)
	}
	for _, c := range f.Calls {
		if c == `set-option -w -t "@4" automatic-rename off` {
			t.Error("automatic-rename must not be touched when AutoRename is true")
		}
	}
}

func TestOpenWindowHostileNamesAreQuoted(t *testing.T) {
	f := &Fake{Replies: map[string][]string{
		listSessionsCmd: {},
		`new-session -d -s "evil;kill-server" -n "w \"q\"" -c "/home/bob/a b" -P -F "#{pane_id}	#{window_id}	#{session_name}"`: {"%0\t@0\tevil;kill-server"},
	}}
	if _, err := OpenWindow(context.Background(), f, &Plan{Group: "evil;kill-server", WindowName: `w "q"`, AutoRename: true, Cwd: "/home/bob/a b", CreateSession: true}); err != nil {
		t.Fatal(err)
	}
}

// TestOpenWindowDecodesVisEncodedSessionNames pins the Task 9 review carry:
// tmux's control-mode replies vis(3)-encode session/group names (see
// UnvisName) — list-sessions reports a space as "\s" and a literal quote
// passes through unescaped. OpenWindow must UnvisName the reply before (a)
// comparing it against the caller-supplied plain group name in BaseSession
// and (b) handing it back into a new-window/new-session target; otherwise a
// stored "\s" would be Quoted verbatim and tmux would double-encode it.
func TestOpenWindowDecodesVisEncodedSessionNames(t *testing.T) {
	const group = `wo rk"grp`      // real chars: space and a literal quote
	const encoded = "wo\\srk\"grp" // as tmux's control mode reports it: \s for the space
	f := &Fake{Replies: map[string][]string{
		listSessionsCmd: {encoded + "\t" + encoded},
		`new-window -t "=wo rk\"grp:" -n "claude" -c "/home/bob/x" -P -F "#{pane_id}	#{window_id}	#{session_name}"`: {"%9\t@4\t" + encoded},
	}}
	ref, err := OpenWindow(context.Background(), f, &Plan{Group: group, WindowName: "claude", AutoRename: true, Cwd: "/home/bob/x"})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Session != group {
		t.Errorf("ref.Session = %q, want decoded %q", ref.Session, group)
	}
	if ref.WindowID != "@4" || ref.PaneID != "%9" {
		t.Errorf("ref = %+v", ref)
	}
}

func TestBaseSession(t *testing.T) {
	s := []SessionInfo{{"work-7", "work"}, {"work-3", "work"}, {"other", ""}}
	if got, ok := BaseSession(s, "work"); !ok || got != "work-3" {
		t.Errorf("lexically smallest: %q %v", got, ok)
	}
	s = append(s, SessionInfo{"work", "work"})
	if got, _ := BaseSession(s, "work"); got != "work" {
		t.Errorf("name == group wins: %q", got)
	}
	if got, ok := BaseSession(s, "other"); !ok || got != "other" {
		t.Errorf("ungrouped session named G: %q %v", got, ok)
	}
	if _, ok := BaseSession(s, "none"); ok {
		t.Error("absent group must not be found")
	}
}

func TestKillWindow(t *testing.T) {
	f := &Fake{Replies: map[string][]string{`kill-window -t "@4"`: {}}}
	if err := KillWindow(context.Background(), f, "@4"); err != nil {
		t.Fatal(err)
	}
}
