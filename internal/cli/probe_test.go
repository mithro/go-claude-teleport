package cli

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

// selectorTmux answers the commands the selector's probe sends.
type selectorTmux struct{ sessions, panes, windowPanes []string }

func (s *selectorTmux) Run(_ context.Context, cmd string) ([]string, error) {
	switch {
	case strings.HasPrefix(cmd, "list-sessions"):
		return s.sessions, nil
	case strings.HasPrefix(cmd, "list-panes -a"):
		return s.panes, nil
	case strings.HasPrefix(cmd, "list-panes -t"):
		return s.windowPanes, nil
	}
	return nil, fmt.Errorf("selectorTmux: unexpected %q", cmd)
}

func (s *selectorTmux) Close() error { return nil }

// The local CLI had no tmux probe at all: a.probe() returned nil, so spec §5
// rule 4 (`<tmux-session> <window>`) answered "tmux is not available" on a
// machine plainly running tmux, and `list` could not see a suspended session
// in any pane.
func TestAppProbeSpansLocalServers(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "main")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	stub := &selectorTmux{
		sessions:    []string{"work\t"},
		panes:       []string{"work\t@3\t%7"},
		windowPanes: []string{"%7"},
	}
	restore := tmuxx.Dial
	tmuxx.Dial = func(_ context.Context, path string) (tmuxx.Transport, error) {
		if path == sock {
			return stub, nil
		}
		return nil, tmuxx.ErrNoServer
	}
	t.Cleanup(func() { tmuxx.Dial = restore })

	a := &app{env: map[string]string{"HOME": t.TempDir(), "TMUX_TMPDIR": dir}, logf: t.Logf}
	p := a.probe()
	if p == nil {
		t.Fatal("probe is nil with a live tmux server present")
	}
	panes, err := p.FindWindow("work", "0")
	if err != nil {
		t.Fatalf("FindWindow: %v", err)
	}
	if len(panes) != 1 || panes[0] != "%7" {
		t.Errorf("FindWindow = %v, want [%%7]", panes)
	}
	if got := p.PaneSocket("%7"); got != sock {
		t.Errorf("PaneSocket = %q, want %q", got, sock)
	}
	for _, c := range a.closers {
		_ = c()
	}
}

// With no server running there is nothing to probe, and that must stay a
// quiet nil rather than an error: plenty of hosts have no tmux.
func TestAppProbeIsNilWithoutAServer(t *testing.T) {
	a := &app{env: map[string]string{"HOME": t.TempDir(), "TMUX_TMPDIR": t.TempDir()}, logf: t.Logf}
	if p := a.probe(); p != nil {
		t.Errorf("probe = %v, want nil with no server", p)
	}
}
