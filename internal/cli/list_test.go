package cli

import (
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

func TestListFixture(t *testing.T) {
	env := []string{"HOME=/home/alice", "CLAUDE_CONFIG_DIR=../session/testdata/config"}
	code, out, stderr := run(t, env, "list")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	// the fixture registry pids are not alive on this machine: both sessions are idle
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "ID") {
		t.Fatalf("%q", out)
	}
	for _, w := range []string{"3f9c2b7e  idle", "a1b2c3d4  idle", "feature/teleport", "2026-08-27T11:00:05.000Z"} {
		if !strings.Contains(out, w) {
			t.Errorf("list lacks %q:\n%s", w, out)
		}
	}
	code, out, _ = run(t, env, "list", "--json")
	if code != ExitOK || !strings.Contains(out, `"state": "idle"`) {
		t.Fatalf("json: %d %s", code, out)
	}
	if code, _, stderr := run(t, env, "list", "--host", "big-storage.example"); code != ExitUsage || !strings.Contains(stderr, "not implemented yet") {
		t.Fatalf("--host: %d %q", code, stderr)
	}
}

// fakeProbe is the Plan 01 stand-in for tmuxx.Prober (Plan 03), matching the
// pattern in internal/session/resolve_test.go's fakeProbe.
type fakeProbe struct {
	panes map[string]struct {
		argv []string
		pid  int
	}
	windows map[string][]session.PaneInfo
	socket  string
}

func (f *fakeProbe) PaneCommand(paneID string) ([]string, int, bool) {
	p, ok := f.panes[paneID]
	return p.argv, p.pid, ok
}
func (f *fakeProbe) FindWindow(sess, win string) ([]string, error) { return nil, nil }
func (f *fakeProbe) ListPanes() ([]session.PaneInfo, error) {
	var out []session.PaneInfo
	for _, infos := range f.windows {
		out = append(out, infos...)
	}
	return out, nil
}
func (f *fakeProbe) SocketPath() string { return f.socket }

// TestListSuspendedViaProbe covers the fix for Task 20 review round 1: a
// placeholder pane must mark its session suspended, but a live registry
// entry for the same session id still wins.
func TestListSuspendedViaProbe(t *testing.T) {
	p := session.NewPaths("/home/alice", "../session/testdata/config", "/tmp/xdg")
	p.ProcRoot = "../session/testdata/proc"

	// pid 41234 (session 3f9c...) is alive in the fixture /proc, so its
	// registry entry marks it running even though a pane also claims it via
	// a placeholder; pid 41300 (session a1b2c3d4...) is a stale registry
	// entry (different start time), so its placeholder pane is what marks
	// it suspended.
	probe := &fakeProbe{
		socket: "/tmp/tmux-1000/default",
		panes: map[string]struct {
			argv []string
			pid  int
		}{
			"%7": {argv: []string{"claude-teleport", "placeholder", "--resume", "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"}, pid: 41234},
			"%9": {argv: []string{"claude-teleport", "placeholder", "--resume", "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"}, pid: 51000},
		},
		windows: map[string][]session.PaneInfo{
			"main 0": {
				{Session: "main", WindowID: "@3", PaneID: "%7"},
				{Session: "main", WindowID: "@4", PaneID: "%9"},
			},
		},
	}

	rows, err := listSessions(p, probe)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]listRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if got := byID["3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"].State; got != "running" {
		t.Errorf("registry-alive session should stay running despite its placeholder pane, got %q", got)
	}
	if got := byID["a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"].State; got != "suspended" {
		t.Errorf("session with only a placeholder pane should be suspended, got %q", got)
	}
	if got := byID["a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"].Tmux; got != "main:@4.%9" {
		t.Errorf("suspended row tmux ref = %q", got)
	}
}
