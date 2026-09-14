package tmuxx

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

// fakeServer answers the handful of commands the prober sends, so a test can
// stand up several "servers" without a tmux binary.
type fakeServer struct {
	sessions []SessionInfo       // list-sessions
	panes    []session3          // list-panes -a
	windows  map[string][]string // "<session>:<window>" target -> pane ids
	runs     []string            // every command received
}

type session3 struct{ sess, win, pane string }

func (f *fakeServer) Run(_ context.Context, cmd string) ([]string, error) {
	f.runs = append(f.runs, cmd)
	switch {
	case strings.HasPrefix(cmd, "list-sessions"):
		var out []string
		for _, s := range f.sessions {
			out = append(out, s.Name+"\t"+s.Group)
		}
		return out, nil
	case strings.HasPrefix(cmd, "list-panes -a"):
		var out []string
		for _, p := range f.panes {
			out = append(out, p.sess+"\t"+p.win+"\t"+p.pane)
		}
		return out, nil
	case strings.HasPrefix(cmd, "list-panes -t"):
		for target, ids := range f.windows {
			if strings.Contains(cmd, target) {
				return ids, nil
			}
		}
		return nil, nil
	}
	return nil, nil
}

func (f *fakeServer) Close() error { return nil }

// A pane that lives on the second server must still be found: the registry
// records "<session>:@win.%pane" with no socket, so the owning server has to
// be discovered rather than assumed.
func TestMultiProberFindsPaneOnAnyServer(t *testing.T) {
	a := &fakeServer{
		sessions: []SessionInfo{{Name: "default", Group: "default"}},
		panes:    []session3{{"default", "@5", "%6"}},
	}
	b := &fakeServer{
		sessions: []SessionInfo{{Name: "pcbs"}},
		panes:    []session3{{"pcbs", "@1", "%99"}},
	}
	p := MultiProber(context.Background(), map[string]Transport{
		"/tmp/tmux-1000/main":  a,
		"/tmp/tmux-1000/other": b,
	}, nil)

	if got := p.PaneSocket("%6"); got != "/tmp/tmux-1000/main" {
		t.Errorf("PaneSocket(%%6) = %q, want the main socket", got)
	}
	if got := p.PaneSocket("%99"); got != "/tmp/tmux-1000/other" {
		t.Errorf("PaneSocket(%%99) = %q, want the other socket", got)
	}
	if got := p.PaneSocket("%1234"); got != "" {
		t.Errorf("PaneSocket of an unknown pane = %q, want empty", got)
	}
}

// Every pane on every server is enumerated, each stamped with the server it
// came from — suspended-session discovery walks this list.
func TestMultiProberListsPanesFromEveryServer(t *testing.T) {
	a := &fakeServer{panes: []session3{{"default", "@5", "%6"}}}
	b := &fakeServer{panes: []session3{{"pcbs", "@1", "%99"}, {"pcbs", "@2", "%100"}}}
	p := MultiProber(context.Background(), map[string]Transport{
		"/tmp/tmux-1000/main":  a,
		"/tmp/tmux-1000/other": b,
	}, nil)

	panes, err := p.ListPanes()
	if err != nil {
		t.Fatal(err)
	}
	if len(panes) != 3 {
		t.Fatalf("got %d panes, want 3: %+v", len(panes), panes)
	}
	sockets := map[string]string{}
	for _, pi := range panes {
		sockets[pi.PaneID] = pi.SocketPath
	}
	for pane, want := range map[string]string{
		"%6": "/tmp/tmux-1000/main", "%99": "/tmp/tmux-1000/other", "%100": "/tmp/tmux-1000/other",
	} {
		if sockets[pane] != want {
			t.Errorf("pane %s socket = %q, want %q", pane, sockets[pane], want)
		}
	}
}

// The CLI selector "<tmux-session> <window>" has to search every server, and
// say so rather than guess when the same session name exists on two.
func TestMultiProberFindWindowAcrossServers(t *testing.T) {
	a := &fakeServer{
		sessions: []SessionInfo{{Name: "default"}},
		windows:  map[string][]string{"default:0": {"%6"}},
	}
	b := &fakeServer{
		sessions: []SessionInfo{{Name: "pcbs"}},
		windows:  map[string][]string{"pcbs:0": {"%99"}},
	}
	p := MultiProber(context.Background(), map[string]Transport{
		"/tmp/tmux-1000/main":  a,
		"/tmp/tmux-1000/other": b,
	}, nil)

	got, err := p.FindWindow("pcbs", "0")
	if err != nil {
		t.Fatalf("FindWindow on the second server: %v", err)
	}
	if len(got) != 1 || got[0] != "%99" {
		t.Errorf("FindWindow = %v, want [%%99]", got)
	}

	// Same name on both servers is ambiguous, and the error must name them.
	b.sessions = []SessionInfo{{Name: "default"}}
	b.windows = map[string][]string{"default:0": {"%99"}}
	p2 := MultiProber(context.Background(), map[string]Transport{
		"/tmp/tmux-1000/main":  a,
		"/tmp/tmux-1000/other": b,
	}, nil)
	_, err = p2.FindWindow("default", "0")
	if err == nil {
		t.Fatal("want an ambiguity error when two servers have the session")
	}
	for _, want := range []string{"main", "other"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %v should name the %s socket", err, want)
		}
	}
}

// A single server behaves exactly as it did before there were several.
func TestMultiProberSingleServerIsUnchanged(t *testing.T) {
	a := &fakeServer{
		sessions: []SessionInfo{{Name: "default"}},
		panes:    []session3{{"default", "@5", "%6"}},
		windows:  map[string][]string{"default:0": {"%6"}},
	}
	p := MultiProber(context.Background(), map[string]Transport{"/tmp/tmux-1000/main": a}, nil)

	if got := p.PaneSocket("%6"); got != "/tmp/tmux-1000/main" {
		t.Errorf("PaneSocket = %q", got)
	}
	got, err := p.FindWindow("default", "0")
	if err != nil || len(got) != 1 || got[0] != "%6" {
		t.Errorf("FindWindow = %v, %v", got, err)
	}
}

// ListLiveServers reports only the sockets with a server answering on them,
// so the stale sockets a crashed or finished server leaves behind cannot
// make discovery ambiguous.
func TestListLiveServersSkipsDeadSockets(t *testing.T) {
	dir := t.TempDir()
	// Real unix sockets: ListServers looks for the socket mode bit, which is
	// the same thing a stale socket left by a dead server still has.
	for _, name := range []string{"main", "stale-1", "stale-2"} {
		ln, err := net.Listen("unix", filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
	}
	restore := Dial
	Dial = func(_ context.Context, path string) (Transport, error) {
		if filepath.Base(path) == "main" {
			return &fakeServer{}, nil
		}
		return nil, ErrNoServer
	}
	t.Cleanup(func() { Dial = restore })

	live, err := ListLiveServers(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || filepath.Base(live[0]) != "main" {
		t.Errorf("live = %v, want just main", live)
	}
}
