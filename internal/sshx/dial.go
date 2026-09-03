package sshx

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
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
	// KeepaliveInterval/KeepaliveCountMax are OpenSSH's
	// ServerAliveInterval/ServerAliveCountMax: 0 takes the default
	// (DefaultKeepaliveInterval/CountMax), a negative interval disables
	// keepalives (and with them the idle read deadline). A `-o
	// ServerAliveInterval=`/`-o ServerAliveCountMax=` override wins over
	// both.
	KeepaliveInterval time.Duration
	KeepaliveCountMax int
	Logf              func(string, ...any)
	Home              string                                                            // for "~" in identity files
	NetDial           func(ctx context.Context, network, addr string) (net.Conn, error) // first hop only; nil = net.Dialer
	LocalUser         string                                                            // user for jump hops with no explicit user (falls back to r.User if empty)
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

	// link is the first hop's socket — the one file descriptor the whole
	// chain rides on, however many jumps are stacked above it.
	link *idleConn

	mu     sync.Mutex
	reason string // why the link was dropped; "" while it is healthy
}

// SSH exposes the underlying client (used by remote tests and Plan 03 pty runs).
func (c *Client) SSH() *ssh.Client { return c.ssh }

// Close stops the keepalive loop, then closes the target connection and
// each jump, innermost first.
func (c *Client) Close() error { return c.closeAll() }

// gracefulCloseTimeout bounds the polite ssh teardown in closeAll. It is
// generous for a healthy link (where the closes take microseconds) and
// irrelevant on a dead one, where dropLink has usually already run.
const gracefulCloseTimeout = 2 * time.Second

// dropLink is the only thing that reliably breaks a connection that has
// stopped moving, and it works by closing the first hop's socket.
//
// The reason is x/crypto/ssh's locking, and it is worth spelling out
// (R-P3-NET-1, CI run 33787193998). A goroutine writing a bulk stream
// holds, for the whole duration of one conn.Write, the ssh channel's
// writeMu AND the handshakeTransport's mu — of every connection in the
// chain, since a jump hop's packets are written into a channel of the hop
// below it. That Write has no deadline, so on a frozen peer it sits in
// write(2) until the peer comes back, minutes later. Everything that
// would normally end the connection needs one of the locks it is holding:
//
//   - ssh.Client.Close() on a jumped connection does not close a socket at
//     all — it writes a channel-close message, which blocks on the very
//     write that is already stuck;
//   - mux.loop's teardown (dropAll → channel.close, the thing that
//     unblocks writers via window.close) takes channel.writeMu;
//   - handshakeTransport.readLoop reports a failed read through
//     recordWriteError, which takes the transport mu.
//
// So the idle read deadline fires, and nothing happens: the reader is
// stuck reporting it and the writer is stuck holding the locks it needs.
// Closing the file descriptor is the one operation that does not need any
// of those locks — the runtime poller fails the in-flight write
// immediately, and the whole stack unwinds from there.
func (c *Client) dropLink(reason string) {
	c.mu.Lock()
	if c.reason == "" {
		c.reason = reason
	}
	c.mu.Unlock()
	if c.link != nil {
		c.link.hardClose()
	}
}

// linkError annotates an I/O failure with why the link died, so a
// transfer that ends because the connection was dropped says so in the
// journal ("connection lost to alice@dest: no keepalive answer in 45s")
// instead of leaving a bare EOF from a channel torn down underneath it.
// The original error is wrapped, so errors.Is/As still reach it.
func (c *Client) linkError(err error) error {
	if err == nil {
		return nil
	}
	c.mu.Lock()
	reason := c.reason
	c.mu.Unlock()
	if reason == "" {
		return err
	}
	return fmt.Errorf("connection lost to %s: %s: %w", c.desc, reason, err)
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
	conf := &ssh.ClientConfig{User: r.User, Auth: methods, HostKeyCallback: cb, Timeout: timeout}
	// HK-1: prefer whatever host-key algorithm(s) known_hosts already has
	// for this hop over x/crypto/ssh's fixed default order — see
	// hostKeyAlgorithms' doc comment. hopAddr(r) is exactly the address
	// string Dial passes to ssh.NewClientConn below, so the lookup and the
	// eventual hostKeyCallback invocation agree on what "this host" means.
	if algos := hostKeyAlgorithms(o.KnownHostsFile, hopAddr(r)); len(algos) > 0 {
		conf.HostKeyAlgorithms = algos
	}
	return conf, cleanup, nil
}

// Dial connects through the jump chain; each hop's hostname is resolved by
// the previous hop (client.Dial("tcp", host:port)), never locally.
//
// overrides is accepted for symmetry with Resolve but is deliberately never
// applied to jump hops here: -o overrides only ever affect the final target
// (already folded into r before Dial is called), while each jump hop in
// r.Via is re-resolved from config alone (see the Resolve(v, cfg, nil, ...)
// call below) so that -o flags aimed at the destination cannot leak into,
// and silently change, the jump hosts along the way.
func Dial(ctx context.Context, r Resolved, cfg *ssh_config.Config, overrides map[string]string, o Options) (*Client, error) {
	logf := o.logf()
	// Jump hops authenticate as the LOCAL user (o.LocalUser) when they have
	// no explicit user of their own, never as the target's resolved user
	// (r.User): the jump host and the target are different machines that
	// may have entirely different accounts for the same person.
	jumpLocalUser := o.LocalUser
	if jumpLocalUser == "" {
		jumpLocalUser = r.User
	}
	hops := make([]Resolved, 0, len(r.Via)+1)
	for _, v := range r.Via {
		rv, err := Resolve(v, cfg, nil, jumpLocalUser)
		if err != nil {
			return nil, fmt.Errorf("jump %s: %w", v.Host, err)
		}
		hops = append(hops, rv)
	}
	hops = append(hops, r)

	// The keepalive settings come from the final target: -o overrides are
	// never applied to jump hops (see above), and one loop on the end of
	// the chain notices a break anywhere along it.
	keepaliveInterval, keepaliveCount, err := keepaliveSettings(o, r.Options)
	if err != nil {
		return nil, fmt.Errorf("ssh %s: %w", r.Host, err)
	}

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
		// Only the first hop is a real socket; every later hop rides
		// inside it as a channel, so this one wrapper covers the whole
		// chain's liveness.
		var idle *idleConn
		if prev == nil {
			idle = &idleConn{Conn: raw, onStall: c.dropLink}
			c.link = idle
			raw = idle
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
		if idle != nil && keepaliveInterval > 0 {
			idle.enable(idleTimeout(keepaliveInterval, keepaliveCount))
		}
		logf("connected to %s@%s (%s)", hop.User, hop.Host, addr)
		if i < len(hops)-1 {
			c.jumps = append(c.jumps, cl)
		} else {
			c.ssh = cl
			if keepaliveInterval > 0 {
				c.closes = append(c.closes, startKeepalive(cl, keepaliveInterval, keepaliveCount, r.User+"@"+r.Host, logf, c.dropLink))
			}
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

// closeAll is the one teardown path — Close and dial's own error returns
// both use it, so the ordering below can never be got right in one and
// wrong in the other.
//
// The keepalive loop is stopped BEFORE anything is closed (B4): a request
// is very likely in flight, and closing under it makes the loop report a
// failure — "keepalive to … failed (1/3)" in log.txt — about a connection
// the caller deliberately closed. It returns the target connection's own
// close error; a half-built chain (c.ssh == nil) has none.
func (c *Client) closeAll() error {
	for _, f := range c.closes {
		f()
	}
	// The polite teardown is attempted first but can never be the last
	// word: on a link that has stopped moving these closes block forever
	// (dropLink explains why), and a Close that hangs wedges the very
	// failure path that is trying to report the dead link. So it is
	// bounded, and the socket is always closed afterwards — which also
	// releases whatever the bounded attempt is still stuck on.
	done := make(chan error, 1)
	go func() {
		var err error
		if c.ssh != nil {
			err = c.ssh.Close()
		}
		for i := len(c.jumps) - 1; i >= 0; i-- {
			c.jumps[i].Close()
		}
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(gracefulCloseTimeout):
		err = fmt.Errorf("close %s: the connection stopped responding; dropping it", c.desc)
	}
	if c.link != nil {
		c.link.hardClose()
	}
	return err
}

// Redial calls dial up to attempts times with exponential backoff
// (backoff, 2*backoff, ... capped at 30s). Every attempt's error is kept.
func Redial(ctx context.Context, attempts int, backoff time.Duration, logf func(string, ...any), dial func(ctx context.Context) (*Client, error)) (*Client, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if attempts < 1 {
		logf("redial: attempts=%d < 1, clamping to 1", attempts)
		attempts = 1
	}
	var errs []string
	var lastErr error
	wait := backoff
	for i := 1; i <= attempts; i++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("redial aborted after %d attempt(s): %w (%s)", i-1, err, strings.Join(errs, "; "))
		}
		c, err := dial(ctx)
		if err == nil {
			return c, nil
		}
		lastErr = err
		errs = append(errs, fmt.Sprintf("attempt %d: %v", i, err))
		if i == attempts {
			break
		}
		logf("ssh dial failed (attempt %d/%d): %v; retrying in %s", i, attempts, err, wait)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("redial aborted: %w (%s)", ctx.Err(), strings.Join(errs, "; "))
		case <-time.After(wait):
		}
		wait *= 2
		if wait > 30*time.Second {
			wait = 30 * time.Second
		}
	}
	// Wrap the last attempt's underlying error with %w so callers can
	// errors.Is/As into it (e.g. for exit-code classification); the other
	// attempts are still listed in the message text for diagnostics.
	prior := errs
	if len(prior) > 0 {
		prior = prior[:len(prior)-1]
	}
	msg := fmt.Sprintf("ssh dial failed after %d attempts", attempts)
	if len(prior) > 0 {
		msg += " (" + strings.Join(prior, "; ") + ")"
	}
	return nil, fmt.Errorf("%s: attempt %d: %w", msg, attempts, lastErr)
}
