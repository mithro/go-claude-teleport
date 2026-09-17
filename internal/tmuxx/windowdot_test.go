package tmuxx

import (
	"context"
	"strings"
	"testing"
)

const listWindowsTTCmd = "list-windows -t \"=tt\" -F \"#{window_id}\t#{window_name}\""

// A tmux target is "<session>:<window>.<pane>", so tmux splits a target at
// the dot -- and a window NAME containing a dot can never be targeted by
// name. Window ids can: "@25" is server-global, unambiguous, and has no
// dot in it, so a named window is resolved to its id first.
//
// Real case (2026-09-17, x1c-work): window "tt mech f.o" in session "tt".
// The target "=tt:=tt mech f.o" made tmux answer
//
//	can't find window: tt mech f
//
// having taken ".o" for a pane specifier.
func TestFindWindowByNameContainingADot(t *testing.T) {
	tb := fakeProc(t, nil)
	f := &Fake{Replies: map[string][]string{
		listSessionsCmd:                       {"tt\ttt"},
		listWindowsTTCmd:                      {"@34\ttt-vga-vids", "@25\ttt mech f.o", "@38\ttt-vga-cap"},
		`list-panes -t "@25" -F "#{pane_id}"`: {"%26"},
	}}
	p := Prober(context.Background(), f, tb, "/tmp/tmux-1000/main")
	panes, err := p.FindWindow("tt", "tt mech f.o")
	if err != nil {
		t.Fatalf("FindWindow: %v", err)
	}
	if len(panes) != 1 || panes[0] != "%26" {
		t.Errorf("FindWindow = %v, want [%%26]", panes)
	}
}

// A window index is already unambiguous and carries no dot, so it keeps
// going straight to list-panes -- no extra round trip.
func TestFindWindowByIndexIsUnchanged(t *testing.T) {
	tb := fakeProc(t, nil)
	f := &Fake{Replies: map[string][]string{
		listSessionsCmd:                         {"tt\ttt"},
		`list-panes -t "=tt:2" -F "#{pane_id}"`: {"%26"},
	}}
	p := Prober(context.Background(), f, tb, "/tmp/tmux-1000/main")
	panes, err := p.FindWindow("tt", "2")
	if err != nil || len(panes) != 1 || panes[0] != "%26" {
		t.Fatalf("FindWindow by index = %v %v", panes, err)
	}
}

// A name matching no window says so, rather than reporting "no panes".
func TestFindWindowUnknownName(t *testing.T) {
	tb := fakeProc(t, nil)
	f := &Fake{Replies: map[string][]string{
		listSessionsCmd:  {"tt\ttt"},
		listWindowsTTCmd: {"@34\ttt-vga-vids"},
	}}
	p := Prober(context.Background(), f, tb, "/tmp/tmux-1000/main")
	_, err := p.FindWindow("tt", "nope")
	if err == nil {
		t.Fatal("an unknown window name must be an error")
	}
	if !strings.Contains(err.Error(), "no window named") {
		t.Errorf("error should say the name matched nothing, got: %v", err)
	}
}

// Two windows in one session can share a name; picking one at random
// would teleport whichever tmux happened to list first.
func TestFindWindowAmbiguousName(t *testing.T) {
	tb := fakeProc(t, nil)
	f := &Fake{Replies: map[string][]string{
		listSessionsCmd:  {"tt\ttt"},
		listWindowsTTCmd: {"@34\tbash", "@35\tbash"},
	}}
	p := Prober(context.Background(), f, tb, "/tmp/tmux-1000/main")
	_, err := p.FindWindow("tt", "bash")
	if err == nil {
		t.Fatal("a duplicated window name must be an error")
	}
	// Naming the candidates is what makes the error actionable: the user
	// can re-run with an index.
	for _, want := range []string{"@34", "@35"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name candidate %s, got: %v", want, err)
		}
	}
}
