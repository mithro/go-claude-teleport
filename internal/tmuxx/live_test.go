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
//
// PR #8 CI (ubuntu-latest, tmux 3.4) found a second version dependency:
// that tmux vis-encodes session names but stores window names raw (`w\x`
// stays `w\x`, never re-encoded to `w\\x`) — the opposite of next-3.8/3.5a,
// which encode both. So nothing here pins a specific vis(3) encoding; every
// "stored spelling" is read back from this server (list-sessions /
// list-windows), and the assertions are the round-trip properties that
// hold regardless: ListSessions/ListPanes report the stored spelling,
// OpenWindow (both branches) targets/creates it and UnvisName decodes it
// back to the name a human typed.
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

	// Names as a human writes them; the stored spelling for each is
	// whatever this server actually reports back.
	names := []string{`a b`, `a\b`, `a"b`}
	stored := make(map[string]string, len(names))
	for _, name := range names {
		stored[name] = deriveSessionStored(t, ctx, tr, name)
		if got := UnvisName(stored[name]); got != name {
			t.Errorf("UnvisName(%q) = %q, want the original %q", stored[name], got, name)
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
	for _, name := range names {
		if !got[stored[name]] {
			t.Errorf("ListSessions has no session stored as %q; got %v", stored[name], got)
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
	for _, name := range names {
		if !seen[stored[name]] {
			t.Errorf("ListPanes dropped session %q; got %v", stored[name], seen)
		}
	}

	// Group reuse: the stored name goes into the -t target verbatim. The
	// backslash name is the one that regressed, so assert on it explicitly;
	// the others prove the convention is not backslash-specific.
	for _, name := range names {
		grp := stored[name]
		ref, err := OpenWindow(ctx, tr, &Plan{SocketPath: sock, Group: grp, WindowName: "claude", AutoRename: true, Cwd: cwd})
		if err != nil {
			t.Fatalf("OpenWindow into existing session %q: %v", grp, err)
		}
		if ref.Session != grp {
			t.Errorf("ref.Session = %q, want the stored spelling %q", ref.Session, grp)
		}
		facts, err := Describe(ctx, tr, ref.PaneID)
		if err != nil {
			t.Fatal(err)
		}
		if facts.SessionName != grp || facts.WindowName != "claude" {
			t.Errorf("new window landed in %+v, want session %q", facts, grp)
		}
	}

	// Creation branch: a group with no session yet, and a window name that
	// also carries a backslash. Group/WindowName carry the STORED spelling
	// per the Plan contract, so OpenWindow must decode both for -s/-n or
	// a tmux that does re-encode would double-encode. Derive the expected
	// stored spelling for each from this server directly (a throwaway
	// session, and a throwaway window in an already-live session) instead
	// of assuming any one tmux version's encoding rule.
	groupDecoded := `n\g`
	groupStored := deriveSessionStored(t, ctx, tr, groupDecoded)
	if got := UnvisName(groupStored); got != groupDecoded {
		t.Errorf("UnvisName(%q) = %q, want %q", groupStored, got, groupDecoded)
	}
	if _, err := tr.Run(ctx, `kill-session -t `+Quote("="+groupStored)); err != nil {
		t.Fatalf("kill throwaway group session %q: %v", groupStored, err)
	}

	windowDecoded := `w\x`
	windowStored := deriveWindowStored(t, ctx, tr, stored[`a b`], windowDecoded)
	if got := UnvisName(windowStored); got != windowDecoded {
		t.Errorf("UnvisName(%q) = %q, want %q", windowStored, got, windowDecoded)
	}

	ref, err := OpenWindow(ctx, tr, &Plan{SocketPath: sock, Group: groupStored, WindowName: windowStored, AutoRename: false, Cwd: cwd, CreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Session != groupStored {
		t.Errorf("created session stored as %q, want the observed spelling %q", ref.Session, groupStored)
	}
	facts, err := Describe(ctx, tr, ref.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	if facts.WindowName != windowStored {
		t.Errorf("created window stored as %q, want the observed spelling %q", facts.WindowName, windowStored)
	}
}

// deriveSessionStored creates a throwaway session named decoded and returns
// whatever spelling tmux actually stored it as — the ground truth for this
// tmux binary's session-name vis-encoding, read back rather than assumed
// (see TestLiveVisNames).
func deriveSessionStored(t *testing.T, ctx context.Context, tr Transport, decoded string) string {
	t.Helper()
	before, err := ListSessions(ctx, tr)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, s := range before {
		seen[s.Name] = true
	}
	if _, err := tr.Run(ctx, `new-session -d -s `+Quote(decoded)+` "tail -f /dev/null"`); err != nil {
		t.Fatalf("create session %q: %v", decoded, err)
	}
	after, err := ListSessions(ctx, tr)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range after {
		if !seen[s.Name] {
			return s.Name
		}
	}
	t.Fatalf("no new session appeared after creating %q; before=%v after=%v", decoded, before, after)
	return ""
}

// deriveWindowStored creates a throwaway window (named decoded) in the
// already-live baseSession and returns whatever spelling tmux actually
// stored the window name as.
func deriveWindowStored(t *testing.T, ctx context.Context, tr Transport, baseSession, decoded string) string {
	t.Helper()
	lines, err := tr.Run(ctx, `new-window -d -t `+Quote("="+baseSession+":")+` -n `+Quote(decoded)+` -P -F "#{window_id}\t#{window_name}"`)
	if err != nil {
		t.Fatalf("create window %q in session %q: %v", decoded, baseSession, err)
	}
	if len(lines) == 0 {
		t.Fatalf("create window %q: empty reply", decoded)
	}
	f := strings.SplitN(lines[0], "\t", 2)
	if len(f) != 2 {
		t.Fatalf("create window %q: unexpected reply %q", decoded, lines[0])
	}
	return f[1]
}
