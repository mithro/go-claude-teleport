package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/procx"
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
	writeRegistryKind(t, p, pid, status, tmux, "interactive")
}

// writeRegistryKind is writeRegistry with an explicit "kind" (M5: a `-p`
// registry entry's kind, per fakeclaude's "interactive"/real Claude Code's
// hypothesised "print" — see task-21-report.md's disclosed gap).
func writeRegistryKind(t *testing.T, p session.Paths, pid int, status, tmux, kind string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": sid, "cwd": "/home/alice/x", "procStart": "777", "version": "2.1.247", "kind": kind, "status": status, "tmux": tmux, "updatedAt": time.Now().UnixMilli()})
	// Atomic (temp file + rename) so a concurrent reader (e.g. ConfirmClaude's
	// poll loop, especially with a no-op Sleep) never observes a partial
	// write — plain os.WriteFile raced procx.RegistryForSession here.
	if err := session.WriteFileAtomic(filepath.Join(p.SessionsDir(), "5150.json"), b, 0o600); err != nil {
		t.Fatalf("writeRegistry: %v", err)
	}
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

// growingCapture answers ConfirmClaude's repeated `capture-pane` calls with
// pane content that only grows, appending extraLines starting from
// appendFrom (a 1-based call count) — a stand-in for a real tmux pane
// where NEW output can appear on a later poll than the first. It errors on
// any other command since it is only ever used for this one capture.
type growingCapture struct {
	target     string // the exact capture-pane command it answers
	extraLines []string
	appendFrom int
	calls      int
}

func (g *growingCapture) Run(_ context.Context, cmd string) ([]string, error) {
	if cmd != g.target {
		return nil, fmt.Errorf("growingCapture: unexpected cmd %q", cmd)
	}
	g.calls++
	lines := []string{"> "}
	if g.calls >= g.appendFrom {
		lines = append(lines, g.extraLines...)
	}
	return lines, nil
}

func (g *growingCapture) Close() error { return nil }

// TestConfirmClaudeFailsOnMarkerAppearingDuringThisAttempt covers a
// failure marker that appears WHILE this confirmation attempt is polling
// (as opposed to M4's stale-scrollback-from-an-earlier-attempt case,
// below) — it must still be caught.
func TestConfirmClaudeFailsOnMarkerAppearingDuringThisAttempt(t *testing.T) {
	p := testPaths(t)
	proc := fakeProcRoot(t, [][4]string{{"5150", "1", "claude", "claude\x00"}})
	f := &growingCapture{target: `capture-pane -epJ -S - -t "%7"`, extraLines: []string{"Not logged in · Please run /login"}, appendFrom: 2}
	dial := func(context.Context, string) (tmuxx.Transport, error) { return f, nil }
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: dial, Sleep: func(time.Duration) {}})
	writeRegistry(t, p, 5150, "busy", "work:@1.%7") // never idle: the marker must trip first
	_, err := l.ConfirmClaude(context.Background(), &session.TmuxRef{SocketPath: "/s", Session: "work", WindowID: "@1", PaneID: "%7"}, session.ID(sid), time.Second)
	if err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("err = %v, want marker failure", err)
	}
	if f.calls < 2 {
		t.Fatalf("marker only appears on call 2+; got %d calls", f.calls)
	}
}

// TestConfirmClaudeIgnoresStaleMarkerFromEarlierAttempt is M4: a failure
// marker already sitting in the pane's scrollback at the START of this
// confirm attempt (as if left there by an earlier, unrelated attempt — a
// previous job resume, or a user's own shell history) must not
// permanently abort confirmation. Here the marker is present in EVERY
// capture (a real tmux pane's scrollback never un-writes itself), so
// pre-fix code would fail on the very first poll; the fix must instead
// let the job succeed once the registry catches up to idle.
func TestConfirmClaudeIgnoresStaleMarkerFromEarlierAttempt(t *testing.T) {
	p := testPaths(t)
	proc := fakeProcRoot(t, [][4]string{{"5150", "1", "claude", "claude\x00"}})
	f := &tmuxx.Fake{Replies: map[string][]string{`capture-pane -epJ -S - -t "%7"`: {"stale scrollback: Not logged in · Please run /login", "> "}}}
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: fakeDialer(f), Sleep: func(time.Duration) {}})
	writeRegistry(t, p, 5150, "busy", "work:@1.%7")
	go func() { time.Sleep(50 * time.Millisecond); writeRegistry(t, p, 5150, "idle", "work:@1.%7") }()
	reg, err := l.ConfirmClaude(context.Background(), &session.TmuxRef{SocketPath: "/s", Session: "work", WindowID: "@1", PaneID: "%7"}, session.ID(sid), 5*time.Second)
	if err != nil {
		t.Fatalf("err = %v, want the stale marker to be ignored", err)
	}
	if reg.Status != "idle" {
		t.Errorf("reg = %+v", reg)
	}
}

// TestConfirmClaudeAcceptsBusyPrintModeAfterTurn is M5 (spec §6.2 case 3):
// a `-p` run's registry status never becomes "idle" (fakeclaude/real
// Claude Code remove the entry entirely on a clean exit instead), so
// success must be declared once the session's transcript has grown past
// this call's own baseline while status stays "busy".
func TestConfirmClaudeAcceptsBusyPrintModeAfterTurn(t *testing.T) {
	p := testPaths(t)
	proc := fakeProcRoot(t, [][4]string{{"5150", "1", "claude", "claude\x00"}})
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Sleep: func(time.Duration) {}})
	proj := filepath.Join(p.ProjectsDir(), "-home-alice-x")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(proj, sid+".jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"user"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRegistryKind(t, p, 5150, "busy", "", "print")
	go func() {
		time.Sleep(50 * time.Millisecond)
		f, err := os.OpenFile(transcript, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return
		}
		defer f.Close()
		f.WriteString(`{"type":"assistant"}` + "\n")
	}()
	reg, err := l.ConfirmClaude(context.Background(), nil, session.ID(sid), 5*time.Second)
	if err != nil {
		t.Fatalf("err = %v, want the busy print-mode run to be accepted once its turn lands", err)
	}
	if reg.Status != "busy" || reg.Kind != "print" {
		t.Errorf("reg = %+v", reg)
	}
}

// TestConfirmClaudeBusyPrintModeTimesOutWithoutATurn pins the negative
// case: busy+print alone (no transcript growth ever observed) must not be
// treated as success — it must still time out like any other stuck run.
func TestConfirmClaudeBusyPrintModeTimesOutWithoutATurn(t *testing.T) {
	p := testPaths(t)
	proc := fakeProcRoot(t, [][4]string{{"5150", "1", "claude", "claude\x00"}})
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Sleep: func(time.Duration) {}})
	proj := filepath.Join(p.ProjectsDir(), "-home-alice-x")
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(`{"type":"user"}`+"\n"), 0o600)
	writeRegistryKind(t, p, 5150, "busy", "", "print")
	_, err := l.ConfirmClaude(context.Background(), nil, session.ID(sid), 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "not confirmed within") {
		t.Fatalf("err = %v, want a timeout", err)
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

// startSleep spawns a real, short-lived `sleep 60` process this test owns
// (mirrors internal/procx/freezer_test.go's startSleep) so ExitClaude's
// ref==nil / SIGTERM branch can be exercised against a real pid+startTime
// instead of the fake /proc trees the rest of this file uses. Only ever
// signalled/killed by this test's own cleanup — never touches anything the
// test did not spawn.
//
// A background goroutine reaps the process the instant it exits: without
// it, a SIGTERM'd child sits as a zombie — still a live /proc/<pid> entry
// with its startTime unchanged — until something calls Wait(), which would
// make procx.Table.Alive see it as "alive" forever and hang ExitClaude's
// WaitGone poll.
func startSleep(t *testing.T) (pid int, startTime string) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn sleep: %v", err)
	}
	reaped := make(chan struct{})
	go func() { cmd.Wait(); close(reaped) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-reaped
	})
	st, err := procx.StartTime("/proc", cmd.Process.Pid)
	if err != nil {
		t.Fatalf("StartTime: %v", err)
	}
	return cmd.Process.Pid, st
}

// alive reports whether pid still exists (signal 0 probe).
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func TestExitClaudeSIGTERMTerminatesRealProcess(t *testing.T) {
	p := testPaths(t)
	pid, startTime := startSleep(t)
	// ProcRoot must be the real /proc: this exercises ExitClaude's ref==nil
	// (non-tmux) branch against a real signalled process, not the fake
	// /proc trees the other tests in this file use.
	l := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc", Sleep: func(time.Duration) {}})
	if err := l.ExitClaude(context.Background(), nil, pid, startTime, 5*time.Second); err != nil {
		t.Fatalf("ExitClaude: %v", err)
	}
	if alive(pid) {
		t.Errorf("pid %d still alive after ExitClaude returned", pid)
	}
}

func TestExitClaudeSIGTERMWrongStartTimeIsNoop(t *testing.T) {
	p := testPaths(t)
	pid, startTime := startSleep(t)
	l := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc", Sleep: func(time.Duration) {}})
	// A startTime that does not match the real process is "not our
	// process" by procx.Table.Alive's pid-reuse protection: ExitClaude
	// must not signal it. That same mismatch also makes WaitGone's own
	// Alive check immediately report "not alive" against the (wrong)
	// startTime it was given, so — per this implementation — the call
	// no-ops and returns nil rather than erroring or timing out.
	wrong := startTime + "1"
	if err := l.ExitClaude(context.Background(), nil, pid, wrong, 300*time.Millisecond); err != nil {
		t.Fatalf("ExitClaude with wrong startTime = %v, want no-op nil", err)
	}
	if !alive(pid) {
		t.Fatal("pid was signalled despite a startTime mismatch")
	}
}

func TestStartClaudeRejectsNilRef(t *testing.T) {
	l := NewLocal(testPaths(t), "x", LocalOptions{ProcRoot: "/proc"})
	err := l.StartClaude(context.Background(), nil, session.ID(sid), sid, []string{"claude"})
	if e, ok := err.(*Error); !ok || e.Code != "usage" {
		t.Fatalf("err = %v, want Error{Code: usage}", err)
	}
}

func TestTypeCommandRejectsNilRef(t *testing.T) {
	l := NewLocal(testPaths(t), "x", LocalOptions{ProcRoot: "/proc"})
	err := l.TypeCommand(context.Background(), nil, []string{"claude"})
	if e, ok := err.(*Error); !ok || e.Code != "usage" {
		t.Fatalf("err = %v, want Error{Code: usage}", err)
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

// TestConfirmClaudeDialsTmuxOnce is M3: the old loop opened and closed a
// control connection — i.e. spawned a `tmux -C attach-session` process —
// on every 250ms poll, ~240 of them over a 60s --start-timeout.
func TestConfirmClaudeDialsTmuxOnce(t *testing.T) {
	p := testPaths(t)
	proc := fakeProcRoot(t, [][4]string{{"5150", "1", "claude", "claude\x00"}})
	f := &tmuxx.Fake{Replies: map[string][]string{`capture-pane -epJ -S - -t "%7"`: {"> "}}}
	dials := 0
	dialer := func(context.Context, string) (tmuxx.Transport, error) { dials++; return f, nil }
	slept := 0
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: dialer, Sleep: func(time.Duration) { slept++ }})
	writeRegistry(t, p, 5150, "idle", "other:@9.%9") // never our pane: polls to the deadline
	if _, err := l.ConfirmClaude(context.Background(), &session.TmuxRef{SocketPath: "/s", Session: "work", WindowID: "@1", PaneID: "%7"}, session.ID(sid), 300*time.Millisecond); err == nil {
		t.Fatal("expected a timeout")
	}
	if slept < 2 {
		t.Fatalf("polled %d times, want several so the dial count means something", slept)
	}
	if dials != 1 {
		t.Errorf("dialed tmux %d times over %d polls, want exactly 1", dials, slept)
	}
}
