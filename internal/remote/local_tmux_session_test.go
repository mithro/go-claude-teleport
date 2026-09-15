package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

const listSessionsCmd = "list-sessions -F \"#{session_name}\t#{session_group}\""

// liveSockets makes each named socket a live tmux server answering
// list-sessions with the given session names, and points tmuxx.Dial — the
// package-level probe server discovery uses — at them.
func liveSockets(t *testing.T, dir string, byName map[string][]string) {
	t.Helper()
	fakes := map[string]*tmuxx.Fake{}
	for name, sessions := range byName {
		lines := make([]string, 0, len(sessions))
		for _, s := range sessions {
			lines = append(lines, s+"\t")
		}
		p := filepath.Join(dir, name)
		l, err := net.Listen("unix", p)
		if err != nil {
			t.Fatal(err)
		}
		l.(*net.UnixListener).SetUnlinkOnClose(false)
		l.Close()
		fakes[p] = &tmuxx.Fake{Replies: map[string][]string{listSessionsCmd: lines}}
	}
	restore := tmuxx.Dial
	tmuxx.Dial = func(_ context.Context, p string) (tmuxx.Transport, error) {
		if f, ok := fakes[p]; ok {
			return f, nil
		}
		return nil, tmuxx.ErrNoServer
	}
	t.Cleanup(func() { tmuxx.Dial = restore })
}

// The destination-side half of the multi-server work: told which session
// the window is for, discovery picks the server that already holds it
// rather than refusing because no server is named like the source's
// socket.
func TestLocalInventoryTmuxPrefersTheSessionsServer(t *testing.T) {
	dir := t.TempDir()
	liveSockets(t, dir, map[string][]string{
		"main": {"default", "bmc"},
		"work": {"pcbs"},
	})
	l := NewLocal(testPaths(t), "x", LocalOptions{ProcRoot: "/proc",
		Tmux: fakeDialer(&tmuxx.Fake{}), TmuxSocketDir: dir})

	facts, err := l.InventoryTmux(context.Background(), nil, "laptop", "pcbs")
	if err != nil {
		t.Fatalf("InventoryTmux: %v", err)
	}
	if want := filepath.Join(dir, "work"); facts.SocketPath != want {
		t.Errorf("SocketPath = %q, want %q", facts.SocketPath, want)
	}

	// Without the target it is the old name-only discovery, which cannot
	// choose here — proving the target is what did the work above.
	if _, err := l.InventoryTmux(context.Background(), nil, "laptop", ""); err == nil {
		t.Error("want the several-servers refusal with no target session")
	}
}

// The field has to survive the wire, or the destination silently falls
// back to name-only discovery while the source thinks it asked.
func TestInventoryTmuxArgsCarryTargetSessionOverTheWire(t *testing.T) {
	var got InventoryTmuxArgs
	ep := targetCapturingEndpoint{seen: &got}

	req, err := json.Marshal(map[string]any{
		"id": 1, "op": OpInventoryTmux,
		"args": InventoryTmuxArgs{PreferredSocket: "main", TargetSession: "pcbs"},
	})
	if err != nil {
		t.Fatal(err)
	}

	in := strings.NewReader(string(req) + "\n")
	pr, pw := io.Pipe()
	go func() { _ = Serve(context.Background(), in, pw, ep); pw.Close() }()
	sc := bufio.NewScanner(pr)
	for sc.Scan() {
	}

	if got.TargetSession != "pcbs" {
		t.Errorf("endpoint saw TargetSession %q, want \"pcbs\"", got.TargetSession)
	}
	if got.PreferredSocket != "main" {
		t.Errorf("endpoint saw PreferredSocket %q, want \"main\"", got.PreferredSocket)
	}
}

type targetCapturingEndpoint struct {
	Endpoint
	seen *InventoryTmuxArgs
}

func (e targetCapturingEndpoint) InventoryTmux(_ context.Context, ref *session.TmuxRef, preferred, target string) (*tmuxx.Facts, error) {
	*e.seen = InventoryTmuxArgs{Ref: ref, PreferredSocket: preferred, TargetSession: target}
	return &tmuxx.Facts{SocketPath: "/tmp/x"}, nil
}
