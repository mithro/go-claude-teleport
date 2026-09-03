package sshx

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mithro/go-claude-teleport/internal/sshx/sshtest"
)

// stallRelay is a TCP relay that can be frozen: while frozen it neither
// reads from nor writes to either side and closes nothing, which is what
// `docker pause` on a jump host looks like from the other end of the wire
// (the socket stays established, the kernel keeps ACKing until the buffers
// fill, and then every byte the sender offers simply stops moving).
type stallRelay struct {
	Addr   string
	ln     net.Listener
	target string
	frozen atomic.Bool
	conns  sync.Map // net.Conn -> struct{}, closed on cleanup
}

func newStallRelay(t *testing.T, target string) *stallRelay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &stallRelay{Addr: ln.Addr().String(), ln: ln, target: target}
	go r.accept()
	t.Cleanup(func() {
		r.frozen.Store(false)
		r.ln.Close()
		r.conns.Range(func(k, _ any) bool { k.(net.Conn).Close(); return true })
	})
	return r
}

func (r *stallRelay) Freeze() { r.frozen.Store(true) }

func (r *stallRelay) accept() {
	for {
		c, err := r.ln.Accept()
		if err != nil {
			return
		}
		up, err := net.Dial("tcp", r.target)
		if err != nil {
			c.Close()
			return
		}
		smallBuffers(c)
		r.conns.Store(c, struct{}{})
		r.conns.Store(up, struct{}{})
		go r.pipe(up, c)
		go r.pipe(c, up)
	}
}

func (r *stallRelay) pipe(dst, src net.Conn) {
	buf := make([]byte, 32*1024)
	for {
		r.gate()
		n, err := src.Read(buf)
		if n > 0 {
			r.gate()
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// gate blocks (without closing anything) for as long as the relay is
// frozen. Polling is deliberate: a frozen host resumes on its own clock.
func (r *stallRelay) gate() {
	for r.frozen.Load() {
		time.Sleep(5 * time.Millisecond)
	}
}

// smallBuffers shrinks a socket's kernel buffers so that a sender fills
// them after a few tens of kilobytes instead of a few megabytes. That is
// what makes this test deterministic: the writer must end up blocked in
// the kernel write (as it is on a real link carrying a real transfer),
// not merely out of ssh channel window — the two unwind very differently
// (see the fix note in keepalive.go).
func smallBuffers(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetReadBuffer(4096)
		tc.SetWriteBuffer(4096)
	}
}

// sinkExec swallows stdin as fast as it arrives: the destination is
// healthy, so the only thing that can stop the stream is the link.
func sinkExec(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
	io.Copy(io.Discard, stdin)
	return 0
}

// TestStalledLinkFailsAnInFlightStream is R-P3-NET-1: a jump host that
// freezes mid-transfer (CI run 33787193998) must fail the in-flight stream
// within the keepalive budget, not hang until the link comes back.
//
// The topology is the failing scenario's: driver -> jump -> dest, with the
// freeze in front of the jump, so the client's writes end up blocked deep
// inside the nested connection (the dest connection's packets ride a
// direct-tcpip channel of the jump connection) exactly as they do in the
// container suite.
func TestStalledLinkFailsAnInFlightStream(t *testing.T) {
	home, pub := testHome(t)
	dest := sshtest.New(t, sshtest.Options{Authorized: []ssh.PublicKey{pub}, Exec: sinkExec})
	jump := sshtest.New(t, sshtest.Options{
		Authorized: []ssh.PublicKey{pub},
		Resolver:   map[string]string{"dest.private": dest.Addr},
	})
	relay := newStallRelay(t, jump.Addr)
	relayHost, relayPort := hostPort(t, relay.Addr)

	kh := filepath.Join(home, ".ssh", "known_hosts")
	os.WriteFile(kh, []byte(
		sshtest.KnownHostsLine(knownHostsName(relayHost, relayPort), jump.HostKey)+
			sshtest.KnownHostsLine("[dest.private]:2222", dest.HostKey)), 0o600)

	const interval = 500 * time.Millisecond
	const count = 3
	// The contract: interval x (count+1) is the idle deadline that backs
	// the keepalives up, and one more interval is generous grace.
	budget := interval*time.Duration(count+2) + 2*time.Second

	r := Resolved{
		Target:   Target{User: "alice", Host: "dest", Port: 2222, Via: []Target{{User: "alice", Host: relayHost, Port: relayPort}}},
		HostName: "dest.private",
	}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		cn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err == nil {
			smallBuffers(cn)
		}
		return cn, err
	}
	c, err := Dial(context.Background(), r, nil, nil, Options{
		KnownHostsFile: kh, Home: home, Logf: t.Logf, ConnectTimeout: 5 * time.Second,
		KeepaliveInterval: interval, KeepaliveCountMax: count, NetDial: dial,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	p, err := c.Start(context.Background(), "sink")
	if err != nil {
		t.Fatal(err)
	}

	// The pump: everything the transfer step does with a stream, in order
	// (io.Copy into the remote, then close it and reap the exit status).
	type result struct {
		err   error
		stage string
	}
	done := make(chan result, 1)
	go func() {
		_, cerr := io.Copy(p.Stdin, zeroReader{})
		if cerr == nil {
			done <- result{stage: "copy", err: io.ErrUnexpectedEOF} // copying forever cannot succeed
			return
		}
		p.Stdin.Close()
		io.Copy(io.Discard, p.Stdout)
		werr := p.Wait()
		p.Close()
		if werr == nil {
			werr = cerr
		}
		done <- result{stage: "close", err: werr}
	}()

	// Let real bytes flow first, then cut the link mid-stream.
	time.Sleep(300 * time.Millisecond)
	frozen := time.Now()
	relay.Freeze()

	select {
	case res := <-done:
		took := time.Since(frozen)
		if res.err == nil {
			t.Fatalf("pump returned no error after the link stalled (stage %s)", res.stage)
		}
		t.Logf("pump failed %s after the stall: %v", took.Round(time.Millisecond), res.err)
		if took > budget {
			t.Errorf("pump took %s to fail, want < %s", took.Round(time.Millisecond), budget)
		}
	case <-time.After(budget):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("pump still blocked %s after the link stalled (budget %s); goroutines:\n%s", time.Since(frozen).Round(time.Millisecond), budget, buf[:n])
	}
}

// zeroReader is an endless source of bytes to push at the remote.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { return len(p), nil }
