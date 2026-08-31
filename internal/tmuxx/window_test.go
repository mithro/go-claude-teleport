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

// TestOpenWindowTargetsStoredSessionName replaces the Task 10 fixture that
// asserted the opposite (I1/I2). Its premise — that tmux reports a space as
// "\s" — is false, and decoding a name into a `-t` target makes the target
// unresolvable. Both fixture rows below are transcribed from a live probe
// on a throwaway socket (tmux next-3.8), recorded in the fix-wave report:
//
//	$ tmux new-session -d -s 'a b'; tmux new-session -d -s 'a\b'
//	$ tmux new-session -d -s 'a"b'
//	$ tmux list-sessions -F '#{session_name}<TAB>#{session_group}'
//	a b<TAB>
//	a"b<TAB>
//	a\\b<TAB>
//	$ tmux has-session -t '=a\b'   → can't find session: a\b
//	$ tmux has-session -t '=a\\b'  → rc=0
//
// So: a space is NOT encoded, a literal quote is NOT encoded, a backslash IS
// doubled — and the stored spelling is the one a target must carry.
func TestOpenWindowTargetsStoredSessionName(t *testing.T) {
	for _, tc := range []struct {
		name          string
		stored        string // exactly what list-sessions reports
		wantNewWindow string
	}{
		{"space", `a b`, `new-window -t "=a b:" -n "claude" -c "/home/alice/x" -P -F "#{pane_id}	#{window_id}	#{session_name}"`},
		{"backslash", `a\\b`, `new-window -t "=a\\\\b:" -n "claude" -c "/home/alice/x" -P -F "#{pane_id}	#{window_id}	#{session_name}"`},
		{"quote", `a"b`, `new-window -t "=a\"b:" -n "claude" -c "/home/alice/x" -P -F "#{pane_id}	#{window_id}	#{session_name}"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &Fake{Replies: map[string][]string{
				listSessionsCmd:  {tc.stored + "\t"},
				tc.wantNewWindow: {"%9\t@4\t" + tc.stored},
			}}
			ref, err := OpenWindow(context.Background(), f, &Plan{Group: tc.stored, WindowName: "claude", AutoRename: true, Cwd: "/home/alice/x"})
			if err != nil {
				t.Fatal(err)
			}
			if ref.Session != tc.stored {
				t.Errorf("ref.Session = %q, want the stored spelling %q", ref.Session, tc.stored)
			}
			if ref.WindowID != "@4" || ref.PaneID != "%9" {
				t.Errorf("ref = %+v", ref)
			}
		})
	}
}

// TestOpenWindowDecodesNamesForCreationFlags is the other half of the
// convention: `new-session -s` / `new-window -n` get the DECODED spelling,
// because tmux re-encodes what it is handed. Probe-verified:
//
//	$ tmux new-session -d -s 'a\b';  tmux list-sessions -F '[#{session_name}]'
//	[a\\b]
//	$ tmux new-session -d -s 'a\\b'; tmux list-sessions -F '[#{session_name}]'
//	[a\\\\b]
//	[a\\b]
//
// Quote then doubles the single backslash again for tmux's own double-quote
// parser, so the command line reads -s "a\\b" and tmux stores `a\\b`.
func TestOpenWindowDecodesNamesForCreationFlags(t *testing.T) {
	const stored = `a\\b` // as list-sessions reports it
	want := `new-session -d -s "a\\b" -n "w\\x" -c "/home/alice/x" -P -F "#{pane_id}	#{window_id}	#{session_name}"`
	f := &Fake{Replies: map[string][]string{
		listSessionsCmd: {"other\t"},
		want:            {"%9\t@4\t" + stored},
	}}
	ref, err := OpenWindow(context.Background(), f, &Plan{Group: stored, WindowName: `w\\x`, AutoRename: true, Cwd: "/home/alice/x", CreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Session != stored {
		t.Errorf("ref.Session = %q, want the stored spelling %q", ref.Session, stored)
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
