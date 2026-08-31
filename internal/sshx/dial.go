package sshx

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/kevinburke/ssh_config"
	"golang.org/x/crypto/ssh"
)

// Options controls authentication, host verification and timeouts.
type Options struct {
	KnownHostsFile string
	AgentSocket    string        // $SSH_AUTH_SOCK
	StrictHostKey  string        // "yes" (default) | "accept-new" | "no"
	ConnectTimeout time.Duration // 0 = 15s
	Logf           func(string, ...any)
	Home           string                                                            // for "~" in identity files
	NetDial        func(ctx context.Context, network, addr string) (net.Conn, error) // first hop only; nil = net.Dialer
}

func (o Options) logf() func(string, ...any) {
	if o.Logf != nil {
		return o.Logf
	}
	return func(string, ...any) {}
}

// Client wraps the final *ssh.Client and every jump client under it.
type Client struct {
	ssh    *ssh.Client
	jumps  []*ssh.Client // outermost first
	desc   string
	closes []func()
}

// SSH exposes the underlying client (used by remote tests and Plan 03 pty runs).
func (c *Client) SSH() *ssh.Client { return c.ssh }

// Close closes the target connection, then each jump, innermost first.
func (c *Client) Close() error {
	err := c.ssh.Close()
	for i := len(c.jumps) - 1; i >= 0; i-- {
		c.jumps[i].Close()
	}
	for _, f := range c.closes {
		f()
	}
	return err
}

// String renders user@host (via a, b).
func (c *Client) String() string { return c.desc }

func hopAddr(r Resolved) string { return net.JoinHostPort(r.HostName, strconv.Itoa(r.Port)) }

func clientConfig(r Resolved, o Options) (*ssh.ClientConfig, func(), error) {
	strict := o.StrictHostKey
	if v, ok := r.Options["StrictHostKeyChecking"]; ok {
		strict = v
	}
	cb, err := hostKeyCallback(o.KnownHostsFile, strict, o.logf())
	if err != nil {
		return nil, nil, err
	}
	methods, cleanup, err := authMethods(o.AgentSocket, r.IdentityFiles, o.Home, o.logf())
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", r.Host, err)
	}
	timeout := o.ConnectTimeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	return &ssh.ClientConfig{User: r.User, Auth: methods, HostKeyCallback: cb, Timeout: timeout}, cleanup, nil
}

// Dial connects through the jump chain; each hop's hostname is resolved by
// the previous hop (client.Dial("tcp", host:port)), never locally.
func Dial(ctx context.Context, r Resolved, cfg *ssh_config.Config, overrides map[string]string, o Options) (*Client, error) {
	logf := o.logf()
	hops := make([]Resolved, 0, len(r.Via)+1)
	for _, v := range r.Via {
		rv, err := Resolve(v, cfg, nil, r.User)
		if err != nil {
			return nil, fmt.Errorf("jump %s: %w", v.Host, err)
		}
		hops = append(hops, rv)
	}
	hops = append(hops, r)

	c := &Client{}
	var prev *ssh.Client
	for i, hop := range hops {
		conf, cleanup, err := clientConfig(hop, o)
		if err != nil {
			c.closeAll()
			return nil, err
		}
		c.closes = append(c.closes, cleanup)
		addr := hopAddr(hop)
		var raw net.Conn
		if prev == nil {
			dial := o.NetDial
			if dial == nil {
				d := &net.Dialer{Timeout: conf.Timeout}
				dial = d.DialContext
			}
			raw, err = dial(ctx, "tcp", addr)
		} else {
			raw, err = prev.Dial("tcp", addr)
		}
		if err != nil {
			c.closeAll()
			return nil, fmt.Errorf("dial %s (%s): %w", hop.Host, addr, err)
		}
		if dl, ok := ctx.Deadline(); ok {
			raw.SetDeadline(dl)
		}
		cc, chans, reqs, err := ssh.NewClientConn(raw, addr, conf)
		if err != nil {
			raw.Close()
			c.closeAll()
			return nil, fmt.Errorf("ssh %s@%s (%s): %w", hop.User, hop.Host, addr, err)
		}
		raw.SetDeadline(time.Time{})
		cl := ssh.NewClient(cc, chans, reqs)
		logf("connected to %s@%s (%s)", hop.User, hop.Host, addr)
		if i < len(hops)-1 {
			c.jumps = append(c.jumps, cl)
		} else {
			c.ssh = cl
		}
		prev = cl
	}
	c.desc = r.User + "@" + r.Host
	if len(r.Via) > 0 {
		names := make([]string, len(r.Via))
		for i, v := range r.Via {
			names[i] = v.Host
		}
		c.desc += " (via " + strings.Join(names, ", ") + ")"
	}
	return c, nil
}

func (c *Client) closeAll() {
	if c.ssh != nil {
		c.ssh.Close()
	}
	for i := len(c.jumps) - 1; i >= 0; i-- {
		c.jumps[i].Close()
	}
	for _, f := range c.closes {
		f()
	}
}
