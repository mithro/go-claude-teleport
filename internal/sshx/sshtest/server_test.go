package sshtest

import (
	"bytes"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestExecAndForward(t *testing.T) {
	clientSigner, clientPub := GenKey(t)
	dest := New(t, Options{
		Authorized: []ssh.PublicKey{clientPub},
		Exec: func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
			io.WriteString(stdout, "dest ran: "+cmd+"\n")
			return 0
		},
	})
	jump := New(t, Options{
		Authorized: []ssh.PublicKey{clientPub},
		Resolver:   map[string]string{"dest.private": dest.Addr},
		Exec: func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
			io.WriteString(stderr, "no exec on jump\n")
			return 7
		},
	})

	cfg := &ssh.ClientConfig{
		User:            "alice",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: ssh.FixedHostKey(jump.HostKey),
	}
	jc, err := ssh.Dial("tcp", jump.Addr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer jc.Close()

	// exec on the jump: exit status 7 and stderr text
	sess, err := jc.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	err = sess.Run("true")
	var ee *ssh.ExitError
	if err == nil || !errorsAs(err, &ee) || ee.ExitStatus() != 7 {
		t.Fatalf("Run on jump: err=%v, want ExitError 7", err)
	}
	if !strings.Contains(stderr.String(), "no exec on jump") {
		t.Errorf("stderr = %q", stderr.String())
	}

	// direct-tcpip to a name only the jump can resolve
	conn, err := jc.Dial("tcp", "dest.private:22")
	if err != nil {
		t.Fatal(err)
	}
	dcfg := &ssh.ClientConfig{
		User:            "alice",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: ssh.FixedHostKey(dest.HostKey),
	}
	cc, chans, reqs, err := ssh.NewClientConn(conn, "dest.private:22", dcfg)
	if err != nil {
		t.Fatal(err)
	}
	dc := ssh.NewClient(cc, chans, reqs)
	defer dc.Close()
	out, err := func() ([]byte, error) {
		s, err := dc.NewSession()
		if err != nil {
			return nil, err
		}
		defer s.Close()
		return s.Output("echo hi")
	}()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "dest ran: echo hi\n" {
		t.Errorf("output = %q", out)
	}
	if got := jump.Forwarded(); len(got) != 1 || got[0] != "dest.private:22" {
		t.Errorf("Forwarded = %v", got)
	}

	// unknown name is refused
	if _, err := jc.Dial("tcp", "nowhere.private:22"); err == nil {
		t.Errorf("expected refusal for unknown host")
	}
	_ = net.Dial
}

func errorsAs(err error, target **ssh.ExitError) bool {
	e, ok := err.(*ssh.ExitError)
	if ok {
		*target = e
	}
	return ok
}

func TestKeyFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path, signer := WriteKeyFile(t, dir, "id_ed25519", "")
	raw, err := readFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(parsed.PublicKey().Marshal(), signer.PublicKey().Marshal()) {
		t.Errorf("public keys differ")
	}
	_, s2 := WriteKeyFile(t, dir, "id_locked", "secret")
	raw2, _ := readFile(dir + "/id_locked")
	if _, err := ssh.ParsePrivateKey(raw2); err == nil {
		t.Errorf("passphrase-protected key parsed without passphrase")
	}
	line := KnownHostsLine("[127.0.0.1]:2222", s2.PublicKey())
	if !strings.HasPrefix(line, "[127.0.0.1]:2222 ssh-ed25519 ") {
		t.Errorf("KnownHostsLine = %q", line)
	}
}

func readFile(p string) ([]byte, error) { return os.ReadFile(p) }
