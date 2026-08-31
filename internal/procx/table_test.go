package procx

import (
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

func TestScanAndSubtree(t *testing.T) {
	tb, err := Scan("testdata/proc")
	if err != nil {
		t.Fatal(err)
	}
	p, ok := tb.Get(101)
	if !ok || p.Comm != "claude" || p.PPID != 100 || p.StartTime != "6000" || len(p.Cmdline) != 1 {
		t.Fatalf("proc 101 = %+v ok=%v", p, ok)
	}
	if p400, _ := tb.Get(400); p400.Comm != "a (b) c" || p400.StartTime != "9500" {
		t.Fatalf("embedded parens: %+v", p400)
	}
	if got := tb.Children(100); len(got) != 1 || got[0] != 101 {
		t.Fatalf("children = %v", got)
	}
	if got := tb.Subtree(100); len(got) != 3 || got[0] != 100 || got[1] != 101 || got[2] != 102 {
		t.Fatalf("subtree = %v", got)
	}
	if !tb.Alive(101, "6000") || tb.Alive(101, "6001") || tb.Alive(101, "") || tb.Alive(4242, "1") {
		t.Fatal("Alive")
	}
	if st, err := StartTime("testdata/proc", 102); err != nil || st != "6100" {
		t.Fatalf("StartTime = %q %v", st, err)
	}
	if _, err := Scan(t.TempDir() + "/none"); err == nil {
		t.Fatal("missing proc root must be an error")
	}
}

func TestRegistryLookups(t *testing.T) {
	r, ok, err := RegistryForPID("testdata/sessions", 101, "6000")
	if err != nil || !ok || r.SessionID != "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13" {
		t.Fatalf("%+v %v %v", r, ok, err)
	}
	if _, ok, err := RegistryForPID("testdata/sessions", 101, "9999"); err != nil || ok {
		t.Fatalf("stale start time matched: %v %v", ok, err)
	}
	if _, ok, err := RegistryForPID("testdata/sessions", 777, "1"); err != nil || ok {
		t.Fatalf("missing file: %v %v", ok, err)
	}
	if _, ok, err := RegistryForPID("testdata/sessions", 200, "8000"); err != nil || ok {
		t.Fatalf("numeric stale procStart matched: %v %v", ok, err)
	}
	r, ok, err = RegistryForSession("testdata/sessions", session.ID("a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"))
	if err != nil || !ok || r.PID != 200 || r.ProcStart != "7000" {
		t.Fatalf("%+v %v %v", r, ok, err)
	}
	if _, ok, _ := RegistryForSession("testdata/sessions", session.ID("00000000-0000-4000-8000-000000000000")); ok {
		t.Fatal("unknown session found")
	}
}

func TestArgvRecognisers(t *testing.T) {
	tb, _ := Scan("testdata/proc")
	p200, _ := tb.Get(200)
	p300, _ := tb.Get(300)
	p101, _ := tb.Get(101)
	if sid, ok := IsPlaceholderArgv(p200.Cmdline); !ok || sid != "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d" {
		t.Fatalf("claude-resume: %q %v", sid, ok)
	}
	if sid, ok := IsPlaceholderArgv(p300.Cmdline); !ok || sid != "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13" {
		t.Fatalf("placeholder: %q %v", sid, ok)
	}
	if _, ok := IsPlaceholderArgv(p101.Cmdline); ok {
		t.Fatal("claude is not a placeholder")
	}
	if id, ok := IsClaudeArgv(p101.Cmdline); !ok || id != "" {
		t.Fatalf("claude: %q %v", id, ok)
	}
	if id, ok := IsClaudeArgv([]string{"/usr/bin/claude", "--resume", "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"}); !ok || id != "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13" {
		t.Fatalf("claude --resume: %q %v", id, ok)
	}
	if _, ok := IsClaudeArgv(p300.Cmdline); ok {
		t.Fatal("a placeholder is not claude")
	}
}
