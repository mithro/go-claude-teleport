// Copied from github.com/mithro/go-tmux-saver internal/tmuxctl
// (Apache-2.0, same author). Keep in sync by hand; do not import.

package tmuxx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

// nopWriteCloser lets a hand-built Client accept Run's command write.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func TestClientDesyncAfterCancel(t *testing.T) {
	// White-box on purpose: against a real server the 1ns-deadline version
	// raced tmux's (fast) reply against ctx.Done() in next()'s select —
	// when both were ready, select's random choice made the test flake
	// (seen under -race). Here the reply channel simply never delivers, so
	// the cancelled context is the only ready case, deterministically.
	var sink bytes.Buffer
	c := &Client{stdin: nopWriteCloser{&sink}, replies: make(chan Reply), parseErr: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Run(ctx, "list-sessions"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context error, got %v", err)
	}
	// The stray reply to the cancelled command may still be in flight; a
	// later Run, even with a fresh context, must not read it as its own
	// answer — it must fail fast with ErrDesynced instead.
	if _, err := c.Run(context.Background(), "list-sessions"); !errors.Is(err, ErrDesynced) {
		t.Fatalf("expected ErrDesynced after a cancelled command, got %v", err)
	}
}

// TestDialControlNoServer covers a socket path with no server listening: a
// missing socket under a fresh t.TempDir() must classify as ErrNoServer.
func TestDialControlNoServer(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "nonexistent")
	c, err := DialControl(context.Background(), sockPath)
	if err == nil {
		c.Close()
		t.Fatal("expected error dialing nonexistent server")
	}
	if !errors.Is(err, ErrNoServer) {
		t.Fatalf("expected ErrNoServer, got %v", err)
	}
}

// TestNextSurfacesParseErrDetail covers issue #7's diagnostics half: when
// the control stream dies inside an open %begin block, the error must carry
// ParseReplies' detail ("ended inside block N"), not just a generic
// "control connection closed" — and repeated calls must keep returning it
// without blocking (the one-shot parseErr channel is captured once).
func TestNextSurfacesParseErrDetail(t *testing.T) {
	c := &Client{replies: make(chan Reply, 4), parseErr: make(chan error, 1)}
	r := strings.NewReader("%begin 1 7 0\npartial output\n") // EOF mid-block
	go func() {
		c.parseErr <- ParseReplies(r, c.replies)
		close(c.replies)
	}()
	for i := 0; i < 2; i++ {
		_, err := c.next(context.Background())
		if err == nil || !strings.Contains(err.Error(), "ended inside block 7") {
			t.Fatalf("call %d: err = %v, want the ended-inside-block detail", i, err)
		}
	}

	// A clean close (%exit) keeps the plain message, with no wrapped nil.
	c2 := &Client{replies: make(chan Reply, 4), parseErr: make(chan error, 1)}
	go func() {
		c2.parseErr <- ParseReplies(strings.NewReader("%exit\n"), c2.replies)
		close(c2.replies)
	}()
	_, err := c2.next(context.Background())
	if err == nil || err.Error() != "control connection closed" {
		t.Fatalf("clean close err = %v, want plain control-connection-closed", err)
	}
}

func TestProbeSocketClassifies(t *testing.T) {
	dir := t.TempDir()
	if err := probeSocket(context.Background(), filepath.Join(dir, "missing")); !errors.Is(err, ErrNoServer) {
		t.Errorf("missing socket: %v, want ErrNoServer", err)
	}
	stale := filepath.Join(dir, "stale")
	l, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	if err := probeSocket(context.Background(), stale); !errors.Is(err, ErrNoServer) {
		t.Errorf("stale socket: %v, want ErrNoServer", err)
	}
	live := filepath.Join(dir, "live")
	l2, err := net.Listen("unix", live)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if err := probeSocket(context.Background(), live); err != nil {
		t.Errorf("live socket: %v, want nil", err)
	}
}
