package session

import (
	"errors"
	"strings"
	"testing"
)

// fakeProbe is the Plan 01 stand-in for tmuxx.Prober (Plan 03).
type fakeProbe struct {
	panes map[string]struct {
		argv []string
		pid  int
	} // pane id -> foreground command
	windows map[string][]PaneInfo // "sess window" -> panes
	socket  string
}

func (f *fakeProbe) PaneCommand(paneID string) ([]string, int, bool) {
	p, ok := f.panes[paneID]
	return p.argv, p.pid, ok
}
func (f *fakeProbe) FindWindow(sess, win string) ([]string, error) {
	infos, ok := f.windows[sess+" "+win]
	if !ok {
		return nil, errors.New("window not found: " + sess + " " + win)
	}
	var ids []string
	for _, i := range infos {
		ids = append(ids, i.PaneID)
	}
	return ids, nil
}
func (f *fakeProbe) ListPanes() ([]PaneInfo, error) {
	var out []PaneInfo
	for _, infos := range f.windows {
		out = append(out, infos...)
	}
	return out, nil
}
func (f *fakeProbe) SocketPath() string { return f.socket }

func fixturePaths() Paths {
	p := NewPaths("/home/alice", "testdata/config", "/tmp/xdg")
	p.ProcRoot = "testdata/proc"
	return p
}

func TestProcAlive(t *testing.T) {
	if !ProcAlive("testdata/proc", 41234, "123456") {
		t.Fatal("41234 should be alive")
	}
	if ProcAlive("testdata/proc", 41300, "234567") {
		t.Fatal("41300 has a different start time: stale")
	}
	if ProcAlive("testdata/proc", 1, "1") {
		t.Fatal("pid 1 is not in the fixture")
	}
	if ProcAlive("testdata/proc", 41234, "") {
		t.Fatal("empty procStart must never match")
	}
}

func TestLoadRunning(t *testing.T) {
	probe := &fakeProbe{socket: "/tmp/tmux-1000/default"}
	s, err := Load(fixturePaths(), sidA, probe)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != StateRunning || s.Registry == nil || s.Registry.PID != 41234 || s.Name != "widget" {
		t.Fatalf("%+v", s)
	}
	if s.Tmux == nil || s.Tmux.Session != "main" || s.Tmux.WindowID != "@3" || s.Tmux.PaneID != "%7" || s.Tmux.SocketPath != probe.socket {
		t.Fatalf("tmux = %+v", s.Tmux)
	}
	if s.LaunchCwd != "/home/alice/github/example/widget" || s.WorkCwd != "/home/alice/github/example/widget/cmd" ||
		s.Branch != "feature/teleport" || s.Version != "2.1.247" || !strings.HasSuffix(s.Transcript, "/"+string(sidA)+".jsonl") {
		t.Fatalf("%+v", s)
	}
}

func TestLoadStaleRegistryIsIdle(t *testing.T) {
	s, err := Load(fixturePaths(), sidB, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != StateIdle || s.Registry != nil {
		t.Fatalf("stale registry (pid reused) must not count as running: %+v", s)
	}
}

func TestLoadSuspendedViaPlaceholderPane(t *testing.T) {
	probe := &fakeProbe{socket: "/tmp/tmux-1000/default",
		panes: map[string]struct {
			argv []string
			pid  int
		}{"%9": {[]string{"claude-teleport", "placeholder", "--resume", string(sidB)}, 500}},
		windows: map[string][]PaneInfo{"main 4": {{Session: "main", WindowID: "@4", PaneID: "%9"}}}}
	s, err := Load(fixturePaths(), sidB, probe)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != StateSuspended || s.Tmux == nil || s.Tmux.PaneID != "%9" || s.Tmux.WindowID != "@4" {
		t.Fatalf("%+v tmux=%+v", s, s.Tmux)
	}
}

func TestLoadNotFound(t *testing.T) {
	_, err := Load(fixturePaths(), ID("00000000-0000-4000-8000-000000000000"), nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolve(t *testing.T) {
	p := fixturePaths()
	probe := &fakeProbe{socket: "/tmp/tmux-1000/default",
		panes: map[string]struct {
			argv []string
			pid  int
		}{
			"%7": {[]string{"claude"}, 41234},
			"%9": {[]string{"claude-resume", string(sidB)}, 500},
		},
		windows: map[string][]PaneInfo{
			"main 3": {{Session: "main", WindowID: "@3", PaneID: "%7"}},
			"main 4": {{Session: "main", WindowID: "@4", PaneID: "%9"}},
		}}

	if s, err := Resolve(p, Selector{ID: sidA}, probe); err != nil || s.ID != sidA {
		t.Fatalf("by id: %v %v", s, err)
	}
	if s, err := Resolve(p, Selector{Prefix: "3f9c"}, probe); err != nil || s.ID != sidA {
		t.Fatalf("by prefix: %v %v", s, err)
	}
	if s, err := Resolve(p, Selector{Prefix: "widget"}, probe); err != nil || s.ID != sidA {
		t.Fatalf("by name: %v %v", s, err)
	}
	if _, err := Resolve(p, Selector{Prefix: "zzzz"}, probe); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown prefix: %v", err)
	}
	if s, err := Resolve(p, Selector{Current: true, TmuxPane: "%7"}, probe); err != nil || s.ID != sidA || s.State != StateRunning {
		t.Fatalf("current pane: %v %v", s, err)
	}
	if s, err := Resolve(p, Selector{Current: true, TmuxPane: "%9"}, probe); err != nil || s.ID != sidB || s.State != StateSuspended {
		t.Fatalf("current placeholder pane: %v %v", s, err)
	}
	if s, err := Resolve(p, Selector{TmuxSess: "main", TmuxWindow: "4"}, probe); err != nil || s.ID != sidB {
		t.Fatalf("window: %v %v", s, err)
	}
	if _, err := Resolve(p, Selector{Current: true}, probe); err == nil || !strings.Contains(err.Error(), "3f9c2b7e") {
		t.Fatalf("no selector must list candidates: %v", err)
	}
}

func TestResolveAmbiguousPrefix(t *testing.T) {
	// both fixture transcripts live in the same project dir; a prefix that
	// matches neither uuid but matches two names is simulated with a
	// common hex prefix: none exists, so craft the ambiguity via ID prefixes
	// of the two fixtures' first char? They differ ("3f" vs "a1"), so use the
	// registry name path: not ambiguous either. Ambiguity is exercised by a
	// temp project dir with two sessions sharing a prefix.
	dir := t.TempDir()
	p := NewPaths("/home/alice", dir, dir)
	proj := p.ProjectDir("/home/alice/x")
	mustMkdir(t, proj)
	for _, id := range []string{"deadbeef-0000-4000-8000-000000000001", "deadbeef-0000-4000-8000-000000000002"} {
		mustWrite(t, proj+"/"+id+".jsonl", `{"type":"user","cwd":"/home/alice/x","sessionId":"`+id+`","message":{"content":"hi"}}`+"\n")
	}
	_, err := Resolve(p, Selector{Prefix: "deadbeef"}, nil)
	if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "000000000001") || !strings.Contains(err.Error(), "000000000002") {
		t.Fatalf("ambiguity must list both candidates: %v", err)
	}
}
