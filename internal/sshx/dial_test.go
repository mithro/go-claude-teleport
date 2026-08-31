package sshx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
