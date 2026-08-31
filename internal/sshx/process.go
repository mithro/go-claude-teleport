package sshx

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"
)

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
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Wait: func() error {
			err := sess.Wait()
			stop()
			return err
		},
		Close: func() error { stop(); return sess.Close() },
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
