package sshx

import (
	"errors"
	"fmt"
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
// Options defaults and the resolved ServerAliveInterval /
// ServerAliveCountMax (an -o override, or the same keyword read out of
// ~/.ssh/config by Resolve — OpenSSH honours both). interval <= 0 disables
// keepalives entirely (as `-o ServerAliveInterval=0` does in OpenSSH).
//
// A ServerAliveCountMax that parses to zero or less is an error rather
// than a silently-ignored value: it reads like "tolerate no misses" but
// would leave the connection with the default 3, and quietly getting
// different liveness behaviour than asked for is the kind of thing that is
// only noticed when a job hangs.
func keepaliveSettings(o Options, opts map[string]string) (time.Duration, int, error) {
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
		n, err := strconv.Atoi(v)
		if err == nil && n <= 0 {
			return 0, 0, fmt.Errorf("ServerAliveCountMax=%s: must be 1 or more (use ServerAliveInterval=0 to turn keepalives off)", v)
		}
		if err == nil {
			count = n
		}
	}
	return interval, count, nil
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
//
// A timed-out Read reports the stall through onStall (Dial wires it to
// Client.dropLink) rather than relying on the error alone: on a link that
// died mid-transfer the error by itself never gets anywhere — see
// dropLink's note on x/crypto/ssh's locking (R-P3-NET-1).
type idleConn struct {
	net.Conn
	idle    atomic.Int64 // nanoseconds; 0 = no deadline
	onStall func(reason string)
	once    sync.Once
}

func (c *idleConn) enable(d time.Duration) { c.idle.Store(int64(d)) }

func (c *idleConn) Read(b []byte) (int, error) {
	d := time.Duration(c.idle.Load())
	if d > 0 {
		c.Conn.SetReadDeadline(time.Now().Add(d))
	}
	n, err := c.Conn.Read(b)
	var ne net.Error
	if d > 0 && errors.As(err, &ne) && ne.Timeout() && c.onStall != nil {
		c.onStall(fmt.Sprintf("nothing received for %s", d))
	}
	return n, err
}

// hardClose closes the socket itself, once. Closing the file descriptor is
// the only thing that unblocks a goroutine already sitting in write(2) on
// a dead link; Close is routed through it so a double close is not an
// error.
func (c *idleConn) hardClose() error {
	var err error
	c.once.Do(func() { err = c.Conn.Close() })
	return err
}

func (c *idleConn) Close() error { return c.hardClose() }

// startKeepalive sends keepalive@openssh.com requests every interval and
// drops the link after count consecutive unanswered ones, so a dead link
// surfaces as an ordinary ssh error within interval*count. The returned
// func stops the loop (Client.Close runs it).
//
// drop is Client.dropLink, NOT ssh.Client.Close: on the link this exists
// for, Close is itself one of the calls that hangs (see dropLink).
func startKeepalive(cl *ssh.Client, interval time.Duration, count int, desc string, logf func(string, ...any), drop func(reason string)) func() {
	stop := make(chan struct{})
	go func() {
		missed := 0
		for {
			// One request per interval, whatever happens to it — so count
			// consecutive misses really do mean interval*count of silence.
			// The reply is waited for in a goroutine because SendRequest
			// blocks until the peer answers or the connection breaks, and a
			// peer that has stopped answering does neither.
			done := make(chan error, 1)
			go func() {
				_, _, err := cl.SendRequest("keepalive@openssh.com", true, nil)
				done <- err
			}()
			select {
			case <-stop:
				return
			case err := <-done:
				// Stopped while this request was in flight (Client.Close):
				// its failure is our own doing, not the link's. Checked
				// here as well as in the select above because both cases
				// can be ready at once, and select would pick either.
				select {
				case <-stop:
					return
				default:
				}
				if err != nil {
					missed++
					logf("keepalive to %s failed (%d/%d): %v", desc, missed, count, err)
				} else {
					missed = 0
				}
				if missed < count {
					select { // answered early: wait out this interval
					case <-stop:
						return
					case <-time.After(interval):
					}
				}
			case <-time.After(interval):
				missed++ // the wait itself was this interval: send again at once
				logf("keepalive to %s unanswered after %s (%d/%d)", desc, interval, missed, count)
			}
			if missed >= count {
				logf("no keepalive answer from %s in %s: closing the connection", desc, time.Duration(count)*interval)
				drop(fmt.Sprintf("no keepalive answer in %s", time.Duration(count)*interval))
				return
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(stop) }) }
}
