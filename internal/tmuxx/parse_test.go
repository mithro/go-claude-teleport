// Copied from github.com/mithro/go-tmux-saver internal/tmuxctl
// (Apache-2.0, same author). Keep in sync by hand; do not import.

package tmuxx

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func collect(t *testing.T, input string) []Reply {
	t.Helper()
	ch := make(chan Reply, 16)
	if err := ParseReplies(strings.NewReader(input), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	var out []Reply
	for r := range ch {
		out = append(out, r)
	}
	return out
}

func TestParseProbeTranscript(t *testing.T) {
	data, err := os.ReadFile("testdata/probe.transcript")
	if err != nil {
		t.Fatal(err)
	}
	got := collect(t, string(data))
	if len(got) != 4 {
		t.Fatalf("want 4 replies, got %d: %+v", len(got), got)
	}
	if len(got[0].Lines) != 0 { // attach block contains only a %session-changed notification
		t.Errorf("attach reply should have no data lines, got %q", got[0].Lines)
	}
	if got[1].Lines[0] != "probe" {
		t.Errorf("list-sessions reply = %q", got[1].Lines)
	}
	if !strings.HasPrefix(got[2].Lines[0], "probe\t0\t0\t%0\t") {
		t.Errorf("list-panes reply = %q", got[2].Lines)
	}
	if len(got[3].Lines) != 3 || !strings.Contains(got[3].Lines[0], "tim@ten64") {
		t.Errorf("capture-pane reply = %q", got[3].Lines)
	}
}

func TestParseErrorBlock(t *testing.T) {
	got := collect(t, "%begin 1 2 1\nno such session: x\n%error 1 2 1\n%exit\n")
	if len(got) != 1 || !got[0].Err || got[0].Lines[0] != "no such session: x" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseMismatchedEndIsData(t *testing.T) {
	// an %end whose number does not match the open %begin is pane data, not a terminator
	got := collect(t, "%begin 1 5 1\n%end 1 999 1\nreal\n%end 1 5 1\n")
	if len(got) != 1 || len(got[0].Lines) != 2 || got[0].Lines[0] != "%end 1 999 1" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseNotificationWhitelist(t *testing.T) {
	// Notifications matching the whitelist should be filtered.
	// Pane data like "%outputs are fine" should NOT be filtered (prefix but not exact match).
	input := "%begin 1 10 1\n%window-renamed @1 foo\n%output %0 abc\n%outputs are fine\n%end 1 10 1\n%exit\n"
	got := collect(t, input)
	if len(got) != 1 {
		t.Fatalf("want 1 reply, got %d", len(got))
	}
	if len(got[0].Lines) != 1 || got[0].Lines[0] != "%outputs are fine" {
		t.Errorf("want [\"%s\"], got %q", "%outputs are fine", got[0].Lines)
	}
}

func TestParseEOFInsideBlock(t *testing.T) {
	// EOF while a block is open should return an error wrapping io.ErrUnexpectedEOF.
	ch := make(chan Reply, 16)
	err := ParseReplies(strings.NewReader("%begin 1 7 1\npartial\n"), ch)
	close(ch)

	// Verify error wraps io.ErrUnexpectedEOF.
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("want error wrapping io.ErrUnexpectedEOF, got %v", err)
	}

	// Verify nothing was sent on the channel.
	if len(ch) > 0 {
		t.Errorf("want no replies sent, got %d", len(ch))
	}
}

// TestParseExitInsideBlock covers the issue-#8 gap from Task 2: a %exit
// arriving while a %begin block is still open means the server died before
// answering — that must surface as the ended-inside-block error, never as
// a clean nil return (which the bare-%exit path outside a block produces).
func TestParseExitInsideBlock(t *testing.T) {
	out := make(chan Reply, 4)
	err := ParseReplies(strings.NewReader("%begin 1 5 0\nsome output\n%exit\n"), out)
	if err == nil || !strings.Contains(err.Error(), "inside block 5") {
		t.Fatalf("err = %v, want ended-inside-block", err)
	}
}

// TestParseRepliesTmux35aTranscript covers issue #10: a REAL control-mode
// byte stream recorded from stock tmux 3.5a on big-storage (the fleet's
// oldest supported version), driving the exact wire commands Dial, the
// collector and the restore path send against a synthetic server layout —
// %begin/%end framing, tab-separated -F output with a quoted/backslashed
// window name, a %error block, the relocation new-window -P reply, and the
// %session-changed/%unlinked-window-* notifications, ending in %exit.
func TestParseRepliesTmux35aTranscript(t *testing.T) {
	raw, err := os.ReadFile("testdata/transcript-3.5a.txt")
	if err != nil {
		t.Fatal(err)
	}
	// Strip the provenance header (leading # comment lines).
	stream := ""
	for _, line := range strings.SplitAfter(string(raw), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		stream += line
	}

	out := make(chan Reply, 32)
	if err := ParseReplies(strings.NewReader(stream), out); err != nil {
		t.Fatalf("ParseReplies: %v", err)
	}
	close(out)
	var replies []Reply
	for r := range out {
		replies = append(replies, r)
	}

	// attach, display-message, list-clients, list-sessions, list-windows,
	// list-panes, capture-pane, %error, new-window -P, send-keys, kill-window
	if len(replies) != 11 {
		t.Fatalf("replies = %d, want 11", len(replies))
	}
	if replies[0].Err || len(replies[0].Lines) != 0 {
		t.Errorf("attach block = %+v, want empty success", replies[0])
	}
	if len(replies[4].Lines) != 3 || !strings.Contains(replies[4].Lines[2], "ti tle\"q\\uote") {
		t.Errorf("list-windows block = %q, want 3 lines incl. the hostile window name", replies[4].Lines)
	}
	// 3.5a stores new-window -n names RAW (no vis encoding) — the transcript
	// pins that: one literal backslash, exactly as passed.
	if strings.Contains(replies[4].Lines[2], `q\\uote`) {
		t.Errorf("3.5a window name unexpectedly vis-encoded: %q", replies[4].Lines[2])
	}
	if !replies[7].Err || !strings.Contains(strings.Join(replies[7].Lines, " "), "can't find session") {
		t.Errorf("block 7 = %+v, want the %%error reply", replies[7])
	}
	if len(replies[8].Lines) != 1 || replies[8].Lines[0] != "2" {
		t.Errorf("new-window -P reply = %q, want the window index", replies[8].Lines)
	}
	for i, r := range replies {
		for _, l := range r.Lines {
			if strings.HasPrefix(l, "%session-changed") || strings.HasPrefix(l, "%unlinked-window") {
				t.Errorf("notification leaked into reply %d: %q", i, l)
			}
		}
	}
}
