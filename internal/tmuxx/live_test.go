//go:build tmuxlive

package tmuxx

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/procx"
)

func TestLiveOpenCaptureTypeKill(t *testing.T) {
	sock, dir := StartTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	found, err := FindServer(dir, "nope", "")
	if err != nil || found != sock {
		t.Fatalf("FindServer = %q %v, want %q", found, err, sock)
	}
	tr, err := DialControl(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	cwd := t.TempDir()

	// 1. New group → new-session.
	ref, err := OpenWindow(ctx, tr, &Plan{SocketPath: sock, Group: "work", WindowName: "claude", AutoRename: false, Cwd: cwd, CreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Session != "work" {
		t.Fatalf("ref = %+v", ref)
	}
	facts, err := Describe(ctx, tr, ref.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	if facts.WindowName != "claude" || facts.AutoRename || facts.PaneCwd != cwd {
		t.Errorf("facts = %+v", facts)
	}

	// 2. Existing grouped session → new-window in the base session.
	if _, err := tr.Run(ctx, `new-session -d -t "work" -s "work-2"`); err != nil {
		t.Fatal(err)
	}
	ref2, err := OpenWindow(ctx, tr, &Plan{SocketPath: sock, Group: "work", WindowName: "second", AutoRename: true, Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if ref2.Session != "work" || ref2.WindowID == ref.WindowID {
		t.Errorf("ref2 = %+v", ref2)
	}

	// 3. Type a command, capture it, inspect state.
	if err := TypeCommand(ctx, tr, ref.PaneID, []string{"printf", "teleport-marker-%s\\n", "ok"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, err := Capture(ctx, tr, ref.PaneID)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "teleport-marker-ok") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("typed command did not run; pane:\n%s", out)
		}
		time.Sleep(100 * time.Millisecond)
	}
	tb, err := procx.Scan("/proc")
	if err != nil {
		t.Fatal(err)
	}
	st, err := State(ctx, tr, ref.PaneID, tb)
	if err != nil {
		t.Fatal(err)
	}
	if !shells[st.Command] {
		t.Errorf("idle pane foreground = %q, want a shell", st.Command)
	}
	if len(st.Content) == 0 || !strings.Contains(strings.Join(st.Content, "\n"), "teleport-marker-ok") {
		t.Errorf("State.Content = %q", st.Content)
	}

	// 4. Kill the second window; the first survives.
	if err := KillWindow(ctx, tr, ref2.WindowID); err != nil {
		t.Fatal(err)
	}
	if _, err := Describe(ctx, tr, ref2.PaneID); err == nil {
		t.Error("killed pane still described")
	}
	if _, err := Describe(ctx, tr, ref.PaneID); err != nil {
		t.Errorf("first pane gone: %v", err)
	}
}

// TestLiveVisNames is R-PRB-4: the encoded-name
// convention (see SessionInfo.Name) proved against a real tmux instead of
// reasoned about. It creates sessions whose names carry a space, a
// backslash and a double quote, then round-trips them through ListSessions,
// prober.ListPanes and both OpenWindow branches. Before the I1/I2 fix the
// backslash session's group-reuse target was undecodable ("can't find
// session") and the space session's panes vanished from ListPanes.
func TestLiveVisNames(t *testing.T) {
	sock, _ := StartTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tr, err := DialControl(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	cwd := t.TempDir()

	// Names as a human writes them, and the spelling tmux stores for each.
	cases := []struct{ decoded, stored string }{
		{`a b`, `a b`},  // space is printable: vis leaves it alone
		{`a\b`, `a\\b`}, // backslash is always doubled
		{`a"b`, `a"b`},  // a literal quote passes through
	}
	for _, c := range cases {
		if _, err := tr.Run(ctx, `new-session -d -s `+Quote(c.decoded)+` "tail -f /dev/null"`); err != nil {
			t.Fatalf("create session %q: %v", c.decoded, err)
		}
	}

	sessions, err := ListSessions(ctx, tr)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range sessions {
		got[s.Name] = true
	}
	for _, c := range cases {
		if !got[c.stored] {
			t.Errorf("ListSessions has no session stored as %q; got %v", c.stored, got)
		}
		if UnvisName(c.stored) != c.decoded {
			t.Errorf("UnvisName(%q) = %q, want %q", c.stored, UnvisName(c.stored), c.decoded)
		}
	}

	tb, err := procx.Scan("/proc")
	if err != nil {
		t.Fatal(err)
	}
	panes, err := Prober(ctx, tr, tb, sock).ListPanes()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, p := range panes {
		seen[p.Session] = true
	}
	for _, c := range cases {
		if !seen[c.stored] {
			t.Errorf("ListPanes dropped session %q; got %v", c.stored, seen)
		}
	}

	// Group reuse: the stored name goes into the -t target verbatim. The
	// backslash name is the one that regressed, so assert on it explicitly;
	// the others prove the convention is not backslash-specific.
	for _, c := range cases {
		ref, err := OpenWindow(ctx, tr, &Plan{SocketPath: sock, Group: c.stored, WindowName: "claude", AutoRename: true, Cwd: cwd})
		if err != nil {
			t.Fatalf("OpenWindow into existing session %q: %v", c.stored, err)
		}
		if ref.Session != c.stored {
			t.Errorf("ref.Session = %q, want the stored spelling %q", ref.Session, c.stored)
		}
		facts, err := Describe(ctx, tr, ref.PaneID)
		if err != nil {
			t.Fatal(err)
		}
		if facts.SessionName != c.stored || facts.WindowName != "claude" {
			t.Errorf("new window landed in %+v, want session %q", facts, c.stored)
		}
	}

	// Creation branch: a group with no session yet. Group carries the STORED
	// spelling, so OpenWindow must decode it for -s or tmux double-encodes.
	ref, err := OpenWindow(ctx, tr, &Plan{SocketPath: sock, Group: `n\\g`, WindowName: `w\\x`, AutoRename: false, Cwd: cwd, CreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Session != `n\\g` {
		t.Errorf("created session stored as %q, want %q (a double-encode would give %q)", ref.Session, `n\\g`, `n\\\\g`)
	}
	facts, err := Describe(ctx, tr, ref.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	if facts.WindowName != `w\\x` {
		t.Errorf("created window stored as %q, want %q", facts.WindowName, `w\\x`)
	}
}
