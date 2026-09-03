package sshx

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"
)

// linkReader/linkWriteCloser annotate a stream's I/O errors with the
// reason the connection died (Client.linkError), so a transfer cut short
// by a dead link reports that rather than the bare EOF an ssh channel
// returns once it has been torn down underneath the caller. On a healthy
// connection they add nothing at all.
type linkReader struct {
	r io.Reader
	c *Client
}

func (l linkReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	return n, l.c.linkError(err)
}

type linkWriteCloser struct {
	w io.WriteCloser
	c *Client
}

func (l linkWriteCloser) Write(p []byte) (int, error) {
	n, err := l.w.Write(p)
	return n, l.c.linkError(err)
}

func (l linkWriteCloser) Close() error { return l.c.linkError(l.w.Close()) }

// Process is a started remote command.
type Process struct {
	Stdin  io.WriteCloser
	Stdout io.Reader
	Stderr io.Reader
	Wait   func() error // *ssh.ExitError on non-zero exit
	Close  func() error
}

func (c *Client) start(ctx context.Context, cmd string, pty bool, rows, cols int) (*Process, error) {
	sess, err := c.ssh.NewSession()
	if err != nil {
		return nil, fmt.Errorf("%s: new session for %q: %w", c.desc, cmd, err)
	}
	if pty {
		if err := sess.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{ssh.ECHO: 0}); err != nil {
			sess.Close()
			return nil, fmt.Errorf("%s: request pty: %w", c.desc, err)
		}
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	if err := sess.Start(cmd); err != nil {
		sess.Close()
		return nil, fmt.Errorf("%s: start %q: %w", c.desc, cmd, err)
	}
	stop := context.AfterFunc(ctx, func() { sess.Close() })
	return &Process{
		Stdin:  linkWriteCloser{w: stdin, c: c},
		Stdout: linkReader{r: stdout, c: c},
		Stderr: linkReader{r: stderr, c: c},
		Wait: func() error {
			err := sess.Wait()
			stop()
			return c.linkError(err)
		},
		Close: func() error { stop(); return c.linkError(sess.Close()) },
	}, nil
}

// Start runs cmd on the remote sh without a pty.
func (c *Client) Start(ctx context.Context, cmd string) (*Process, error) {
	return c.start(ctx, cmd, false, 0, 0)
}

// StartPty runs cmd with a pty of rows x cols; stdout carries the pty output.
func (c *Client) StartPty(ctx context.Context, cmd string, rows, cols int) (*Process, error) {
	return c.start(ctx, cmd, true, rows, cols)
}

// Run is Start + drain; returns stdout, stderr, error (ExitError wrapped).
func (c *Client) Run(ctx context.Context, cmd string, stdin io.Reader) ([]byte, []byte, error) {
	p, err := c.Start(ctx, cmd)
	if err != nil {
		return nil, nil, err
	}
	defer p.Close()
	var out, errOut bytes.Buffer
	done := make(chan struct{}, 2)
	go func() { io.Copy(&out, p.Stdout); done <- struct{}{} }()
	go func() { io.Copy(&errOut, p.Stderr); done <- struct{}{} }()
	if stdin != nil {
		io.Copy(p.Stdin, stdin)
	}
	p.Stdin.Close()
	<-done
	<-done
	if err := p.Wait(); err != nil {
		return out.Bytes(), errOut.Bytes(), fmt.Errorf("%s: %q: %w", c.desc, cmd, err)
	}
	return out.Bytes(), errOut.Bytes(), nil
}
