package sshx

import (
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// Keepalive defaults, in OpenSSH's ServerAliveInterval/ServerAliveCountMax
// terms. Unlike OpenSSH they are ON by default: a teleport runs unattended
// for minutes with nobody at a terminal to notice a wedged link, and a
// half-open connection that never errors would hang the job forever
// instead of failing it into a continuable journal (spec §4.2).
const (
	DefaultKeepaliveInterval = 15 * time.Second
	DefaultKeepaliveCountMax = 3
)

// keepaliveSettings resolves the interval/count for a connection from the
// Options defaults and any -o ServerAliveInterval / ServerAliveCountMax
// override. interval <= 0 disables keepalives entirely (as `-o
// ServerAliveInterval=0` does in OpenSSH).
func keepaliveSettings(o Options, opts map[string]string) (time.Duration, int) {
	interval := o.KeepaliveInterval
	if interval == 0 {
		interval = DefaultKeepaliveInterval
	}
	count := o.KeepaliveCountMax
	if count <= 0 {
		count = DefaultKeepaliveCountMax
	}
	if v, ok := opts["ServerAliveInterval"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			interval = time.Duration(n) * time.Second
		}
	}
	if v, ok := opts["ServerAliveCountMax"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			count = n
		}
	}
	return interval, count
}

// idleTimeout is the read deadline that backs the keepalives up: with a
// request going out every interval, a connection that delivers nothing at
// all for one more interval than the keepalives themselves would tolerate
// is dead, however open its socket still looks.
func idleTimeout(interval time.Duration, count int) time.Duration {
	return interval * time.Duration(count+1)
}

// idleConn refuses to block forever on a socket that has gone quiet.
//
// A frozen or partitioned peer (a paused jump host, a severed link) does
// not close the connection: the kernel just stops getting answers, the
// sender's window fills and TCP settles into zero-window probing, which by
// default takes many minutes to give up — for that whole time a Read here
// simply never returns and the transfer neither progresses nor fails.
// Refreshing a read deadline around every Read turns that silence into an
// error, which the ssh mux propagates to every channel on the connection.
//
// The deadline is armed only after the handshake (enable), and only when
// keepalives are on to guarantee the traffic that keeps it refreshed.
type idleConn struct {
	net.Conn
	idle atomic.Int64 // nanoseconds; 0 = no deadline
}

func (c *idleConn) enable(d time.Duration) { c.idle.Store(int64(d)) }

func (c *idleConn) Read(b []byte) (int, error) {
	if d := c.idle.Load(); d > 0 {
		c.Conn.SetReadDeadline(time.Now().Add(time.Duration(d)))
	}
	return c.Conn.Read(b)
}

// startKeepalive sends keepalive@openssh.com requests every interval and
// closes the connection after count consecutive unanswered ones, so a dead
// link surfaces as an ordinary ssh error within interval*count. The
// returned func stops the loop (Client.Close runs it).
func startKeepalive(cl *ssh.Client, interval time.Duration, count int, desc string, logf func(string, ...any)) func() {
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		missed := 0
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			// The reply is waited for in a goroutine: SendRequest blocks
			// until the peer answers or the connection breaks, and a peer
			// that has stopped answering does neither.
			done := make(chan error, 1)
			go func() {
				_, _, err := cl.SendRequest("keepalive@openssh.com", true, nil)
				done <- err
			}()
			select {
			case <-stop:
				return
			case err := <-done:
				if err == nil {
					missed = 0
					continue
				}
				missed++
				logf("keepalive to %s failed (%d/%d): %v", desc, missed, count, err)
			case <-t.C:
				missed++
				logf("keepalive to %s unanswered after %s (%d/%d)", desc, interval, missed, count)
			}
			if missed >= count {
				logf("no keepalive answer from %s in %s: closing the connection", desc, time.Duration(count)*interval)
				cl.Close()
				return
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(stop) }) }
}
