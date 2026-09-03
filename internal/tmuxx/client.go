// Copied from github.com/mithro/go-tmux-saver internal/tmuxctl
// (Apache-2.0, same author). Keep in sync by hand; do not import.

package tmuxx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
)

// utf8Env returns env with LC_ALL forced to a UTF-8 value unless it already
// names one.
//
// tmux decides whether a client is UTF-8-capable by string-matching
// "UTF-8"/"UTF8" in LC_ALL, then LC_CTYPE, then LANG (tmux.c) — the locale
// itself is never looked up, so the value need not exist on the host. A
// client without that flag has every line the server sends it run through
// utf8_sanitize(), which replaces each non-printable byte — the literal tab
// this package uses as its `-F` field separator included — with "_" (seen on
// tmux 3.5a and 3.6b). Under the plain C/POSIX locale of a container or a
// non-interactive ssh session, list-panes output then comes back as
// "main__@0_0_claude_%0_..." and every Describe/Prober parse fails with
// "malformed list-panes line".
//
// Appending wins: os/exec keeps the LAST value of a duplicated key.
func utf8Env(env []string) []string {
	for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		v := ""
		for _, kv := range env {
			if strings.HasPrefix(kv, key+"=") {
				v = kv[len(key)+1:]
			}
		}
		if v == "" {
			continue // unset/empty: tmux falls through to the next one
		}
		if strings.Contains(strings.ToUpper(v), "UTF-8") || strings.Contains(strings.ToUpper(v), "UTF8") {
			return env
		}
		break // set but not UTF-8: tmux stops here, so LC_ALL must override
	}
	return append(append([]string(nil), env...), "LC_ALL=C.UTF-8")
}

// Client is a live control-mode connection to one tmux server.
type Client struct {
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	stderr        *bytes.Buffer
	replies       chan Reply
	parseErr      chan error
	mu            sync.Mutex
	desynced      atomic.Bool
	parseOnce     sync.Once
	finalParseErr error
}

var _ Transport = (*Client)(nil)

// ErrDesynced is returned by Run once a previous command's context was
// cancelled before its reply arrived; the connection cannot be recovered.
var ErrDesynced = errors.New("control connection desynchronised after a cancelled command; dial again")

// ErrNoServer is wrapped by DialControl when no server listens on the socket.
var ErrNoServer = errors.New("no tmux server")

// DialControl starts `tmux -S <socketPath> -C attach-session -f no-output`
// and consumes the initial attach block. It never starts a server: the
// socket is probed first and a missing/stale socket returns ErrNoServer.
func DialControl(ctx context.Context, socketPath string) (Transport, error) {
	if err := probeSocket(ctx, socketPath); err != nil {
		return nil, err
	}
	cmd := exec.Command("tmux", "-S", socketPath, "-C", "attach-session", "-f", "no-output")
	cmd.Env = utf8Env(os.Environ())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start tmux control client: %w", err)
	}
	c := &Client{cmd: cmd, stdin: stdin, stderr: &stderr, replies: make(chan Reply, 64), parseErr: make(chan error, 1)}
	go func() {
		c.parseErr <- ParseReplies(stdout, c.replies)
		close(c.replies)
	}()
	reply, err := c.next(ctx)
	if err != nil {
		c.Close()
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("attach on socket %s: %w: %s", socketPath, err, msg)
		}
		return nil, fmt.Errorf("attach on socket %s: %w", socketPath, err)
	}
	if reply.Err {
		c.Close()
		return nil, fmt.Errorf("attach on socket %s: %s", socketPath, strings.Join(reply.Lines, " "))
	}
	return c, nil
}

// probeSocket classifies the socket by errno, never by tmux's text.
func probeSocket(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("socket %s does not exist: %w", path, ErrNoServer)
		}
		return fmt.Errorf("stat socket %s: %w", path, err)
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stale socket %s: %w", path, ErrNoServer)
		}
		return fmt.Errorf("connect socket %s: %w", path, err)
	}
	conn.Close()
	return nil
}

func (c *Client) next(ctx context.Context) (Reply, error) {
	select {
	case r, ok := <-c.replies:
		if !ok {
			c.parseOnce.Do(func() { c.finalParseErr = <-c.parseErr })
			if c.finalParseErr != nil {
				return Reply{}, fmt.Errorf("control connection closed: %w", c.finalParseErr)
			}
			return Reply{}, fmt.Errorf("control connection closed")
		}
		return r, nil
	case <-ctx.Done():
		c.desynced.Store(true)
		return Reply{}, ctx.Err()
	}
}

// Run sends one command and returns its reply lines. Commands are serialised.
func (c *Client) Run(ctx context.Context, cmd string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.desynced.Load() {
		return nil, fmt.Errorf("run %q: %w", cmd, ErrDesynced)
	}
	if _, err := io.WriteString(c.stdin, cmd+"\n"); err != nil {
		return nil, fmt.Errorf("write %q: %w", cmd, err)
	}
	r, err := c.next(ctx)
	if err != nil {
		return nil, err
	}
	if r.Err {
		return nil, &CmdError{Cmd: cmd, Lines: r.Lines}
	}
	return r.Lines, nil
}

// Close detaches (stdin EOF → %exit) and waits for the client to exit.
func (c *Client) Close() error {
	c.stdin.Close()
	return c.cmd.Wait()
}
