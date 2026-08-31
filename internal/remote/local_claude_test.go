package remote

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

// sid is declared in local_test.go and reused here.

// fakeProcRoot writes /proc/<pid>/stat + cmdline for procx.Scan.
func fakeProcRoot(t *testing.T, procs [][4]string) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range procs {
		dir := filepath.Join(root, p[0])
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "stat"), []byte(p[0]+" ("+p[2]+") S "+p[1]+" 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 777 0 0"), 0o644)
		os.WriteFile(filepath.Join(dir, "cmdline"), []byte(p[3]), 0o644)
	}
	return root
}

func writeRegistry(t *testing.T, p session.Paths, pid int, status, tmux string) {
	t.Helper()
	os.MkdirAll(p.SessionsDir(), 0o700)
	b, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": sid, "cwd": "/home/alice/x", "procStart": "777", "version": "2.1.247", "status": status, "tmux": tmux, "updatedAt": time.Now().UnixMilli()})
	os.WriteFile(filepath.Join(p.SessionsDir(), "5150.json"), b, 0o600)
}

func TestConfirmClaudeSucceedsWhenIdleInOurPane(t *testing.T) {
	p := testPaths(t)
	proc := fakeProcRoot(t, [][4]string{{"100", "1", "bash", "bash\x00"}, {"5150", "100", "claude", "claude\x00--resume\x00" + sid + "\x00"}})
	f := &tmuxx.Fake{Replies: map[string][]string{`capture-pane -epJ -S - -t "%7"`: {"╭ Welcome ╮", "> "}}}
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: fakeDialer(f), Sleep: func(time.Duration) {}})
	ref := &session.TmuxRef{SocketPath: "/s", Session: "work", WindowID: "@1", PaneID: "%7"}
	writeRegistry(t, p, 5150, "busy", "work:@1.%7")
	go func() { time.Sleep(50 * time.Millisecond); writeRegistry(t, p, 5150, "idle", "work:@1.%7") }()
	reg, err := l.ConfirmClaude(context.Background(), ref, session.ID(sid), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if reg.PID != 5150 || reg.Status != "idle" {
		t.Errorf("reg = %+v", reg)
	}
}

func TestConfirmClaudeFailsOnMarker(t *testing.T) {
	p := testPaths(t)
	proc := fakeProcRoot(t, [][4]string{{"5150", "1", "claude", "claude\x00"}})
	f := &tmuxx.Fake{Replies: map[string][]string{`capture-pane -epJ -S - -t "%7"`: {"Not logged in · Please run /login"}}}
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: fakeDialer(f), Sleep: func(time.Duration) {}})
	writeRegistry(t, p, 5150, "idle", "work:@1.%7")
	_, err := l.ConfirmClaude(context.Background(), &session.TmuxRef{SocketPath: "/s", Session: "work", WindowID: "@1", PaneID: "%7"}, session.ID(sid), time.Second)
	if err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("err = %v, want marker failure", err)
	}
}

func TestConfirmClaudeRejectsWrongPane(t *testing.T) {
	p := testPaths(t)
	proc := fakeProcRoot(t, [][4]string{{"5150", "1", "claude", "claude\x00"}})
	f := &tmuxx.Fake{Replies: map[string][]string{`capture-pane -epJ -S - -t "%7"`: {"> "}}}
	slept := 0
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: fakeDialer(f), Sleep: func(time.Duration) { slept++ }})
	writeRegistry(t, p, 5150, "idle", "other:@9.%9")
	_, err := l.ConfirmClaude(context.Background(), &session.TmuxRef{SocketPath: "/s", Session: "work", WindowID: "@1", PaneID: "%7"}, session.ID(sid), 600*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "pane") {
		t.Fatalf("err = %v, want timeout mentioning the pane mismatch", err)
	}
	if slept == 0 {
		t.Error("expected polling")
	}
}

func TestExitClaudeInTmuxTypesSlashExit(t *testing.T) {
	p := testPaths(t)
	proc := fakeProcRoot(t, [][4]string{{"100", "1", "bash", "bash\x00"}}) // 5150 already gone
	f := &tmuxx.Fake{Replies: map[string][]string{`send-keys -t "%7" "/exit"`: {}, `send-keys -t "%7" Enter`: {}}}
	var slept []time.Duration
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: fakeDialer(f), Sleep: func(d time.Duration) { slept = append(slept, d) }})
	if err := l.ExitClaude(context.Background(), &session.TmuxRef{SocketPath: "/s", PaneID: "%7"}, 5150, "777", time.Second); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 2 || f.Calls[0] != `send-keys -t "%7" "/exit"` || f.Calls[1] != `send-keys -t "%7" Enter` {
		t.Errorf("calls = %v", f.Calls)
	}
	if len(slept) == 0 || slept[0] != 500*time.Millisecond {
		t.Errorf("expected a 500ms pause between /exit and Enter, got %v", slept)
	}
}

func TestExitClaudeTimesOut(t *testing.T) {
	p := testPaths(t)
	proc := fakeProcRoot(t, [][4]string{{"5150", "1", "claude", "claude\x00"}})
	f := &tmuxx.Fake{Default: []string{}}
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: fakeDialer(f), Sleep: func(time.Duration) {}})
	err := l.ExitClaude(context.Background(), &session.TmuxRef{SocketPath: "/s", PaneID: "%7"}, 5150, "777", 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "5150") {
		t.Fatalf("err = %v, want timeout naming the pid", err)
	}
}

func TestClaudeStatus(t *testing.T) {
	p := testPaths(t)
	// pid 5150 must be alive in the SAME proc root the "present" assertion
	// below checks against: Local.procs rescans this same ProcRoot fresh
	// each call, it does not accept a per-call proc list.
	l := NewLocal(p, "x", LocalOptions{ProcRoot: fakeProcRoot(t, [][4]string{{"5150", "1", "claude", "claude\x00"}})})
	if _, ok, err := l.ClaudeStatus(context.Background(), session.ID(sid)); ok || err != nil {
		t.Fatalf("absent: %v %v", ok, err)
	}
	writeRegistry(t, p, 5150, "idle", "")
	reg, ok, err := l.ClaudeStatus(context.Background(), session.ID(sid))
	if err != nil || !ok || reg.Status != "idle" {
		t.Fatalf("present: %+v %v %v", reg, ok, err)
	}
}
