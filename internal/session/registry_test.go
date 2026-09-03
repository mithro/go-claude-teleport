package session

import (
	"os"
	"path/filepath"
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

// TestReadRegistryEntrypoint pins the field that actually distinguishes a
// print-mode (`claude -p`) run from an interactive one, using the registry
// entry a real Claude Code 2.1.247 `claude -p` wrote (captured verbatim by
// the layer-2 suite, task-26-report.md; 2.1.259 wrote the same shape):
// "kind" is "interactive" for BOTH, and only "entrypoint" differs
// ("sdk-cli" for -p, "cli" for a terminal session).
func TestReadRegistryEntrypoint(t *testing.T) {
	dir := t.TempDir()
	const sample = `{"pid":279,"sessionId":"f85e1932-9d7c-4b3a-8e21-0c5d6a7b8e14","cwd":"/home/alice/proj",` +
		`"startedAt":1788439163760,"procStart":"520225803","version":"2.1.247","peerProtocol":1,` +
		`"peerFeatures":["notify_idle","artifact_yield"],"kind":"interactive","entrypoint":"sdk-cli",` +
		`"pidDomain":"linux:0","name":"proj-be","nameSource":"derived","nameSince":1788439163770}`
	if err := os.WriteFile(filepath.Join(dir, "279.json"), []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	regs, err := ReadRegistry(dir)
	if err != nil || len(regs) != 1 {
		t.Fatalf("regs = %+v, err = %v", regs, err)
	}
	if regs[0].Kind != "interactive" {
		t.Errorf("Kind = %q, want the real value %q", regs[0].Kind, "interactive")
	}
	if regs[0].Entrypoint != "sdk-cli" {
		t.Errorf("Entrypoint = %q, want %q — a print-mode run is only recognisable by this field", regs[0].Entrypoint, "sdk-cli")
	}
}
