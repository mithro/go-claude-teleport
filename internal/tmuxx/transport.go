// Copied from github.com/mithro/go-tmux-saver internal/tmuxctl
// (Apache-2.0, same author). Keep in sync by hand; do not import.

// Package tmuxx talks to a tmux server over a single control-mode
// connection, discovers servers, and opens/captures/types into panes.
package tmuxx

import (
	"context"
	"fmt"
	"strings"
)

// Transport runs tmux commands and returns reply lines.
type Transport interface {
	Run(ctx context.Context, cmd string) ([]string, error)
	Close() error
}

// Dialer opens a Transport to the server on socketPath.
type Dialer func(ctx context.Context, socketPath string) (Transport, error)

// CmdError is returned when tmux answers a command with %error.
type CmdError struct {
	Cmd   string
	Lines []string
}

func (e *CmdError) Error() string {
	return fmt.Sprintf("tmux %q: %s", e.Cmd, strings.Join(e.Lines, " "))
}

// Fake is a Transport backed by a command→reply map, for tests.
type Fake struct {
	Replies map[string][]string
	Default []string // reply for commands not in Replies (nil = error)
	Calls   []string
}

var _ Transport = (*Fake)(nil)

// Run answers from Replies, then Default. A command with neither fails with
// a plain error that is deliberately NOT a *CmdError: production code
// degrades gracefully on tmux %error, so a forgotten stub returning
// *CmdError would let a test silently exercise the degraded path.
func (f *Fake) Run(_ context.Context, cmd string) ([]string, error) {
	f.Calls = append(f.Calls, cmd)
	if r, ok := f.Replies[cmd]; ok {
		return append([]string(nil), r...), nil
	}
	if f.Default != nil {
		return append([]string(nil), f.Default...), nil
	}
	return nil, fmt.Errorf("tmuxx.Fake: no reply configured for %q — add it to Replies or set Default", cmd)
}

func (f *Fake) Close() error { return nil }
