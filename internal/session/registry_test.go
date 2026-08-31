package session

import (
	"strings"
	"testing"
)

func TestReadRegistry(t *testing.T) {
	regs, err := ReadRegistry("testdata/config/sessions")
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 2 {
		t.Fatalf("got %d entries (the .key file must be ignored): %+v", len(regs), regs)
	}
	a, b := regs[0], regs[1]
	if a.PID != 41234 || a.SessionID != "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13" || a.ProcStart != "123456" ||
		a.Status != "idle" || a.Name != "widget" || a.Tmux != "main:@3.%7" || a.Version != "2.1.247" ||
		!strings.HasSuffix(a.File, "/41234.json") {
		t.Fatalf("a = %+v", a)
	}
	if b.PID != 41300 || b.ProcStart != "234567" || b.Tmux != "" {
		t.Fatalf("numeric procStart not normalised: %+v", b)
	}
	sess, win, pane, ok := a.TmuxParts()
	if !ok || sess != "main" || win != "@3" || pane != "%7" {
		t.Fatalf("TmuxParts = %q %q %q %v", sess, win, pane, ok)
	}
	if _, _, _, ok := b.TmuxParts(); ok {
		t.Fatal("empty tmux field must not parse")
	}
}

func TestReadRegistryMissingDirIsEmpty(t *testing.T) {
	regs, err := ReadRegistry(t.TempDir() + "/nope")
	if err != nil || len(regs) != 0 {
		t.Fatalf("%v %v", regs, err)
	}
}

func TestReadRegistryWrongTypedProcStartIsError(t *testing.T) {
	_, err := ReadRegistry("testdata/sessions-bad")
	if err == nil || !strings.Contains(err.Error(), "sessions-bad/9.json") {
		t.Fatalf("err = %v", err)
	}
}

func TestStateString(t *testing.T) {
	if StateIdle.String() != "idle" || StateRunning.String() != "running" || StateSuspended.String() != "suspended" {
		t.Fatal("State.String")
	}
}
