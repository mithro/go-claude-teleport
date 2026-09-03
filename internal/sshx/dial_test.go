package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mithro/go-claude-teleport/internal/sshx/sshtest"
)

// testHome creates a fake $HOME with one client key and returns it plus the key's public half.
func testHome(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, signer := sshtest.WriteKeyFile(t, sshDir, "id_ed25519", "")
	return home, signer.PublicKey()
}

func hostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, c := range p {
		n = n*10 + int(c-'0')
	}
	return h, n
}

func echoExec(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
	in, _ := io.ReadAll(stdin)
	io.WriteString(stdout, "cmd="+cmd+" stdin="+string(in))
	if strings.HasPrefix(cmd, "fail") {
		io.WriteString(stderr, "boom")
		return 3
	}
	return 0
}

func TestDialRunAndExitError(t *testing.T) {
	home, pub := testHome(t)
	srv := sshtest.New(t, sshtest.Options{Authorized: []ssh.PublicKey{pub}, Exec: echoExec})
	host, port := hostPort(t, srv.Addr)
	kh := filepath.Join(home, ".ssh", "known_hosts")
	os.WriteFile(kh, []byte(sshtest.KnownHostsLine(knownHostsName(host, port), srv.HostKey)), 0o600)

	r := Resolved{Target: Target{User: "alice", Host: host, Port: port}, HostName: host}
	c, err := Dial(context.Background(), r, nil, nil, Options{KnownHostsFile: kh, Home: home, Logf: t.Logf, ConnectTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.String() != "alice@"+host {
		t.Errorf("String = %q", c.String())
	}

	out, errOut, err := c.Run(context.Background(), "echo hi", strings.NewReader("input"))
	if err != nil {
		t.Fatalf("Run: %v (stderr %q)", err, errOut)
	}
	if string(out) != "cmd=echo hi stdin=input" {
		t.Errorf("stdout = %q", out)
	}

	_, errOut, err = c.Run(context.Background(), "fail now", nil)
	var ee *ssh.ExitError
	if !errors.As(err, &ee) || ee.ExitStatus() != 3 {
		t.Fatalf("Run fail: err=%v, want ExitError 3", err)
	}
	if string(errOut) != "boom" {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestStartStreamsAndClose(t *testing.T) {
	home, pub := testHome(t)
	srv := sshtest.New(t, sshtest.Options{Authorized: []ssh.PublicKey{pub}, Exec: echoExec})
	host, port := hostPort(t, srv.Addr)
	kh := filepath.Join(home, ".ssh", "known_hosts")
	os.WriteFile(kh, []byte(sshtest.KnownHostsLine(knownHostsName(host, port), srv.HostKey)), 0o600)
	r := Resolved{Target: Target{User: "alice", Host: host, Port: port}, HostName: host}
	c, err := Dial(context.Background(), r, nil, nil, Options{KnownHostsFile: kh, Home: home, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	p, err := c.Start(context.Background(), "cat")
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(p.Stdin, "abc")
	p.Stdin.Close()
	var buf bytes.Buffer
	io.Copy(&buf, p.Stdout)
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "cmd=cat stdin=abc" {
		t.Errorf("stdout = %q", buf.String())
	}
	p.Close()

	pty, err := c.StartPty(context.Background(), "tty", 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	pty.Stdin.Close()
	data, _ := io.ReadAll(pty.Stdout)
	if !strings.HasPrefix(string(data), "cmd=tty") {
		t.Errorf("pty stdout = %q", data)
	}
	pty.Close()
}

func TestDialUnknownHostRefused(t *testing.T) {
	home, pub := testHome(t)
	srv := sshtest.New(t, sshtest.Options{Authorized: []ssh.PublicKey{pub}})
	host, port := hostPort(t, srv.Addr)
	r := Resolved{Target: Target{User: "alice", Host: host, Port: port}, HostName: host}
	_, err := Dial(context.Background(), r, nil, nil, Options{KnownHostsFile: filepath.Join(home, ".ssh", "known_hosts"), Home: home, Logf: t.Logf})
	if err == nil || !strings.Contains(err.Error(), "unknown host") {
		t.Fatalf("err = %v, want unknown host", err)
	}
}

func knownHostsName(host string, port int) string {
	if port == 22 {
		return host
	}
	return "[" + host + "]:" + itoa(port)
}
func itoa(n int) string { return strconv.Itoa(n) }

// TestKeepaliveClosesAConnectionThatStopsAnswering pins the liveness rule
// spec §4.2's "a lost connection is re-dialled and the step re-verified"
// depends on: a peer that is still connected but has stopped answering
// must surface as an ssh error, not as a transfer that hangs forever.
// SilentGlobalRequests is exactly what a frozen host looks like from here.
func TestKeepaliveClosesAConnectionThatStopsAnswering(t *testing.T) {
	home, pub := testHome(t)
	srv := sshtest.New(t, sshtest.Options{Authorized: []ssh.PublicKey{pub}, Exec: echoExec, SilentGlobalRequests: true})
	host, port := hostPort(t, srv.Addr)
	kh := filepath.Join(home, ".ssh", "known_hosts")
	os.WriteFile(kh, []byte(sshtest.KnownHostsLine(knownHostsName(host, port), srv.HostKey)), 0o600)

	var mu sync.Mutex
	var lines []string
	logf := func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(f, a...))
		t.Logf(f, a...)
	}

	r := Resolved{Target: Target{User: "alice", Host: host, Port: port}, HostName: host}
	c, err := Dial(context.Background(), r, nil, nil, Options{
		KnownHostsFile: kh, Home: home, Logf: logf, ConnectTimeout: 5 * time.Second,
		KeepaliveInterval: 150 * time.Millisecond, KeepaliveCountMax: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// 150ms x 3 unanswered keepalives ~= 450ms; well inside this bound and
	// nowhere near it if the keepalives are not running at all.
	done := make(chan error, 1)
	go func() { done <- c.SSH().Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("connection closed cleanly; want a keepalive failure")
		}
		t.Logf("connection died as expected: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("connection to a server that stopped answering never failed")
	}
	// The idle read deadline (interval x (count+1)) is only one interval
	// behind the keepalives here and would kill the connection too, so the
	// timing alone does not pin WHICH mechanism fired. The log does.
	mu.Lock()
	defer mu.Unlock()
	closed := false
	for _, l := range lines {
		if strings.Contains(l, "no keepalive answer from") {
			closed = true
		}
	}
	if !closed {
		t.Errorf("nothing in the log says the keepalive loop closed it: %q", lines)
	}
}

// TestKeepaliveSurvivesABriefHiccup is the other side of the same rule: a
// pause SHORTER than interval x countMax is a hiccup, not a dead link, and
// must not cost the job its connection. Only the countMax-th consecutive
// miss may close it.
func TestKeepaliveSurvivesABriefHiccup(t *testing.T) {
	home, pub := testHome(t)
	const interval = 200 * time.Millisecond
	// One pause of 300ms: longer than an interval (so a keepalive really
	// is missed and the recovery path is exercised) but well short of the
	// 600ms of silence that 3 misses would take.
	srv := sshtest.New(t, sshtest.Options{
		Authorized: []ssh.PublicKey{pub}, Exec: echoExec,
		GlobalRequestDelay: func(n int) time.Duration {
			if n == 1 {
				return 300 * time.Millisecond
			}
			return 0
		},
	})
	host, port := hostPort(t, srv.Addr)
	kh := filepath.Join(home, ".ssh", "known_hosts")
	os.WriteFile(kh, []byte(sshtest.KnownHostsLine(knownHostsName(host, port), srv.HostKey)), 0o600)

	r := Resolved{Target: Target{User: "alice", Host: host, Port: port}, HostName: host}
	c, err := Dial(context.Background(), r, nil, nil, Options{
		KnownHostsFile: kh, Home: home, Logf: t.Logf, ConnectTimeout: 5 * time.Second,
		KeepaliveInterval: interval, KeepaliveCountMax: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	done := make(chan error, 1)
	go func() { done <- c.SSH().Wait() }()
	select {
	case err := <-done:
		t.Fatalf("connection died over a %s hiccup (%s x 3 = %s of silence is the limit): %v",
			300*time.Millisecond, interval, 3*interval, err)
	case <-time.After(2 * time.Second):
	}
	// Still usable, not merely still open.
	sess, err := c.SSH().NewSession()
	if err != nil {
		t.Fatalf("NewSession after the hiccup: %v", err)
	}
	sess.Close()
}

// TestKeepaliveSettings pins the OpenSSH-named -o overrides and defaults.
func TestKeepaliveSettings(t *testing.T) {
	for _, tc := range []struct {
		name     string
		o        Options
		opts     map[string]string
		interval time.Duration
		count    int
		wantErr  string
	}{
		{name: "defaults", interval: DefaultKeepaliveInterval, count: DefaultKeepaliveCountMax},
		{name: "option fields", o: Options{KeepaliveInterval: time.Second, KeepaliveCountMax: 5}, interval: time.Second, count: 5},
		{name: "-o wins", o: Options{KeepaliveInterval: time.Second, KeepaliveCountMax: 5},
			opts:     map[string]string{"ServerAliveInterval": "30", "ServerAliveCountMax": "2"},
			interval: 30 * time.Second, count: 2},
		{name: "-o 0 disables", opts: map[string]string{"ServerAliveInterval": "0"}, interval: 0, count: DefaultKeepaliveCountMax},
		{name: "junk -o is ignored", opts: map[string]string{"ServerAliveInterval": "soon"}, interval: DefaultKeepaliveInterval, count: DefaultKeepaliveCountMax},
		// A count of 0 reads like "tolerate no misses" but used to leave
		// the default 3 in place; it is refused instead of silently
		// meaning something else.
		{name: "-o ServerAliveCountMax=0 is refused", opts: map[string]string{"ServerAliveCountMax": "0"}, wantErr: "must be 1 or more"},
		{name: "-o ServerAliveCountMax=-1 is refused", opts: map[string]string{"ServerAliveCountMax": "-1"}, wantErr: "must be 1 or more"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gi, gc, err := keepaliveSettings(tc.o, tc.opts)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("keepaliveSettings: %v", err)
			}
			if gi != tc.interval || gc != tc.count {
				t.Errorf("keepaliveSettings = %s/%d, want %s/%d", gi, gc, tc.interval, tc.count)
			}
		})
	}
	if got, want := idleTimeout(15*time.Second, 3), time.Minute; got != want {
		t.Errorf("idleTimeout = %s, want %s", got, want)
	}
}

// TestIdleConnReadDeadline pins the backstop under the keepalives: a socket
// that is open but delivers nothing must fail a Read rather than block for
// TCP's own many-minute zero-window schedule.
func TestIdleConnReadDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept() // accepted and then left silent, never closed
		if err == nil {
			t.Cleanup(func() { c.Close() })
		}
	}()
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	ic := &idleConn{Conn: raw}
	ic.enable(200 * time.Millisecond)
	start := time.Now()
	if _, err := ic.Read(make([]byte, 8)); err == nil {
		t.Fatal("read from a silent connection returned no error")
	} else if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read error = %v, want a deadline error", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("read blocked for %s", elapsed)
	}
}

// TestCloseStopsKeepaliveBeforeClosingTheConnection covers B4. Close used
// to close the ssh connections first and stop the keepalive loop
// afterwards, so the request that was in flight at that moment failed and
// the loop logged "keepalive to … failed (1/3)" about a connection the
// caller had deliberately closed — an alarming line in log.txt with no
// problem behind it. A silent server keeps the very first keepalive
// pending, which is exactly that window.
func TestCloseStopsKeepaliveBeforeClosingTheConnection(t *testing.T) {
	home, pub := testHome(t)
	srv := sshtest.New(t, sshtest.Options{Authorized: []ssh.PublicKey{pub}, Exec: echoExec, SilentGlobalRequests: true})
	host, port := hostPort(t, srv.Addr)
	kh := filepath.Join(home, ".ssh", "known_hosts")
	os.WriteFile(kh, []byte(sshtest.KnownHostsLine(knownHostsName(host, port), srv.HostKey)), 0o600)

	var mu sync.Mutex
	var lines []string
	logf := func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(f, a...))
	}
	keepaliveLines := func() []string {
		mu.Lock()
		defer mu.Unlock()
		var out []string
		for _, l := range lines {
			if strings.Contains(l, "keepalive") {
				out = append(out, l)
			}
		}
		return out
	}

	r := Resolved{Target: Target{User: "alice", Host: host, Port: port}, HostName: host}
	c, err := Dial(context.Background(), r, nil, nil, Options{
		KnownHostsFile: kh, Home: home, Logf: logf, ConnectTimeout: 5 * time.Second,
		// Long enough that nothing times out on its own: the only thing
		// that can make the pending request fail is Close itself.
		KeepaliveInterval: 30 * time.Second, KeepaliveCountMax: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Give the keepalive goroutine time to react to the closed connection.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(keepaliveLines()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := keepaliveLines(); len(got) > 0 {
		t.Errorf("Close logged keepalive noise: %q", got)
	}
}
