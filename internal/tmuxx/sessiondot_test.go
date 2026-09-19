package tmuxx

import (
	"context"
	"strings"
	"testing"
)

// The window-name form of this bug is covered by
// TestFindWindowByNameContainingADot. The same tmux target grammar bites
// one level up: a target with no colon is parsed as
// "<session>:<window>.<pane>" from the right, so a dot in the SESSION name
// is taken for a pane specifier.
//
// Real case (2026-09-18, desktop): session "fpgas.online" holds twelve
// windows, and
//
//	tmux -L main list-windows -t "=fpgas.online"
//	can't find pane: online
//
// while the same target with a trailing colon lists all twelve. Window
// creation already gets this right -- window.go builds `"="+base+":"` --
// so only resolveWindowName was left spelling the target without it.
const listWindowsDottedCmd = "list-windows -t \"=fpgas.online:\" -F \"#{window_id}\t#{window_name}\""

func TestFindWindowInSessionWhoseNameContainsADot(t *testing.T) {
	tb := fakeProc(t, nil)
	f := &Fake{Replies: map[string][]string{
		listSessionsCmd:                        {"fpgas.online\t", "netv2\t"},
		listWindowsDottedCmd:                   {"@216\tf.o pcie work", "@218\tf.o docs"},
		`list-panes -t "@218" -F "#{pane_id}"`: {"%218"},
	}}
	p := Prober(context.Background(), f, tb, "/tmp/tmux-1000/main")
	panes, err := p.FindWindow("fpgas.online", "f.o docs")
	if err != nil {
		t.Fatalf("FindWindow in a dotted session: %v", err)
	}
	if len(panes) != 1 || panes[0] != "%218" {
		t.Errorf("FindWindow = %v, want [%%218]", panes)
	}
}

// Pin the spelling, not just the outcome. Without this, a future change
// could satisfy the test above by some other route and quietly reintroduce
// a target tmux parses as a pane specifier.
func TestResolveWindowNameTargetsTheSessionWithATrailingColon(t *testing.T) {
	tb := fakeProc(t, nil)
	f := &Fake{Replies: map[string][]string{
		listSessionsCmd:                        {"fpgas.online\t"},
		listWindowsDottedCmd:                   {"@218\tf.o docs"},
		`list-panes -t "@218" -F "#{pane_id}"`: {"%218"},
	}}
	p := Prober(context.Background(), f, tb, "/tmp/tmux-1000/main")
	if _, err := p.FindWindow("fpgas.online", "f.o docs"); err != nil {
		t.Fatalf("FindWindow: %v", err)
	}
	var listWindows string
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "list-windows ") {
			listWindows = c
		}
	}
	if listWindows == "" {
		t.Fatal("no list-windows command was issued")
	}
	if !strings.Contains(listWindows, `-t "=fpgas.online:"`) {
		t.Errorf("list-windows target must end with a colon so tmux cannot read\n"+
			"the dot as a pane specifier; got:\n  %s", listWindows)
	}
}

// A session name with no dot must keep working, colon or not -- this is
// the regression guard for the sessions that were never broken.
func TestFindWindowInUndottedSessionStillWorks(t *testing.T) {
	tb := fakeProc(t, nil)
	f := &Fake{Replies: map[string][]string{
		listSessionsCmd: {"netv2\t"},
		"list-windows -t \"=netv2:\" -F \"#{window_id}\t#{window_name}\"": {"@48\trpi3-netv2 tests"},
		`list-panes -t "@48" -F "#{pane_id}"`:                             {"%48"},
	}}
	p := Prober(context.Background(), f, tb, "/tmp/tmux-1000/main")
	panes, err := p.FindWindow("netv2", "rpi3-netv2 tests")
	if err != nil || len(panes) != 1 || panes[0] != "%48" {
		t.Fatalf("FindWindow = %v %v", panes, err)
	}
}
