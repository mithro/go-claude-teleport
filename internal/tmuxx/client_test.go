//go:build tmuxlive

// Copied from github.com/mithro/go-tmux-saver internal/tmuxctl
// (Apache-2.0, same author). Keep in sync by hand; do not import.

package tmuxx

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestClientRoundTrip(t *testing.T) {
	sockPath, _ := StartTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := DialControl(ctx, sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lines, err := c.Run(ctx, `list-windows -t default -F "#{window_index}\t#{window_name}"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != "0\th" {
		t.Fatalf("list-windows = %q", lines)
	}
	if _, err := c.Run(ctx, "list-windows -t nosuchsession"); err == nil {
		t.Fatal("expected error for bad target")
	} else if !strings.Contains(err.Error(), "nosuchsession") {
		t.Fatalf("error should carry tmux message, got %v", err)
	}
	// second command after an error still works on the same connection
	if _, err := c.Run(ctx, "list-sessions"); err != nil {
		t.Fatal(err)
	}
}

func TestClientContextTimeout(t *testing.T) {
	sockPath, _ := StartTestServer(t)
	c, err := DialControl(context.Background(), sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if _, err := c.Run(ctx, "list-sessions"); err == nil {
		t.Fatal("expected context deadline error")
	}
}
