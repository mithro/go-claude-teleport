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
