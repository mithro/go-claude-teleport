package remote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/sshx"
	"github.com/mithro/go-claude-teleport/internal/sshx/sshtest"
	"github.com/mithro/go-claude-teleport/internal/transfer"
	"github.com/mithro/go-claude-teleport/internal/version"
)

// pipeClient wires a Client to a Local through net.Pipe; streams are opened
// directly on the Local.
func pipeClient(t *testing.T, l *Local) *Client {
	t.Helper()
	a, b := net.Pipe()
	go func() { Serve(context.Background(), b, b, l); b.Close() }()
	c, err := NewClientConn(context.Background(), a, l.OpenStream, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestClientHelloAndCallsOverPipe(t *testing.T) {
	p := testPaths(t)
	l := NewLocal(p, "self", LocalOptions{Logf: t.Logf})
	c := pipeClient(t, l)
	if c.Info().Protocol != version.Protocol || c.Info().ConfigDir != p.ConfigDir {
		t.Errorf("Info = %+v", c.Info())
	}
	// NewLocal defaults LocalOptions.ProcRoot to "/proc" when unset (see
	// TestLocalProcRootDefaultsToProc); the Paths that cross the wire carry
	// that default, so compare against p with the same default applied.
	wantPaths := p
	wantPaths.ProcRoot = "/proc"
	if c.Paths() != wantPaths {
		t.Errorf("Paths = %+v, want %+v", c.Paths(), wantPaths)
	}
	ctx := context.Background()
	// InventoryGit is a real op now (local_git.go): "/x" is not a git repo,
	// so this exercises gitx.ErrNotRepo crossing the wire as "not-found".
	if _, err := c.InventoryGit(ctx, "/x"); err == nil {
		t.Errorf("expected not-found for a non-repo path")
	} else if pe := new(Error); !errors.As(err, &pe) || pe.Code != "not-found" {
		t.Errorf("err = %v", err)
	}
	if _, err := c.ResolveSession(ctx, session.Selector{ID: session.ID(sid)}); err == nil {
		t.Errorf("expected not-found")
	} else if pe := new(Error); !errors.As(err, &pe) || pe.Code != "not-found" {
		t.Errorf("err = %v", err)
	}

	// journal round trip
	j := &job.Journal{ID: sid, SessionID: sid, Direction: "to"}
	j.Step("preflight").Status = job.Done
	if err := c.JournalPut(ctx, j); err != nil {
		t.Fatal(err)
	}
	got, found, err := c.JournalGet(ctx, sid)
	if err != nil || !found || got.Step("preflight").Status != job.Done {
		t.Errorf("journal: %+v %v %v", got, found, err)
	}

	// concurrent calls are multiplexed by id
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Hello(ctx); err != nil {
				t.Errorf("concurrent hello: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestClientProtocolMismatchIsUsageError(t *testing.T) {
	a, b := net.Pipe()
	ep := stubEndpoint{hello: func() (HostInfo, error) { return HostInfo{Protocol: version.Protocol + 1, Version: "v9.9"}, nil }}
	go func() { Serve(context.Background(), b, b, ep); b.Close() }()
	_, err := NewClientConn(context.Background(), a, nil, t.Logf)
	if err == nil || !strings.Contains(err.Error(), "protocol mismatch") || !strings.Contains(err.Error(), "v9.9") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientTransferOverSSHTest(t *testing.T) {
	// "dest" host: a Local behind an in-process sshd whose exec handler runs
	// `<exe> remote serve` and `<exe> remote stream ...` in-process.
	destPaths := testPaths(t)
	dest := NewLocal(destPaths, "claude-teleport", LocalOptions{Logf: t.Logf})
	exec := func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
		args := strings.Fields(cmd)
		switch {
		case len(args) == 3 && args[1] == "remote" && args[2] == "serve":
			if err := Serve(context.Background(), stdin, stdout, dest); err != nil {
				io.WriteString(stderr, err.Error())
				return 1
			}
			return 0
		case len(args) == 6 && args[1] == "remote" && args[2] == "stream":
			if err := ServeStream(context.Background(), StreamKind(args[3]), args[4], args[5], stdin, stdout, dest); err != nil {
				io.WriteString(stderr, err.Error())
				return 1
			}
			return 0
		}
		io.WriteString(stderr, "unexpected command "+cmd)
		return 127
	}
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	_, signer := sshtest.WriteKeyFile(t, filepath.Join(home, ".ssh"), "id_ed25519", "")
	srv := sshtest.New(t, sshtest.Options{Authorized: []ssh.PublicKey{signer.PublicKey()}, Exec: exec})
	host, portStr, _ := net.SplitHostPort(srv.Addr)
	port := 0
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}
	kh := filepath.Join(home, ".ssh", "known_hosts")
	os.WriteFile(kh, []byte(sshtest.KnownHostsLine("["+host+"]:"+portStr, srv.HostKey)), 0o600)
	sc, err := sshx.Dial(context.Background(), sshx.Resolved{Target: sshx.Target{User: "bob", Host: host, Port: port}, HostName: host}, nil, nil,
		sshx.Options{KnownHostsFile: kh, Home: home, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	c, err := NewClient(context.Background(), sc, "claude-teleport", t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Info().Hostname == "" {
		t.Errorf("hello over ssh: %+v", c.Info())
	}

	ctx := context.Background()
	m := sourceManifest(t, destPaths)
	st, err := c.ManifestDiff(ctx, m, sid)
	if err != nil {
		t.Fatal(err)
	}
	if st[0] != transfer.Absent {
		t.Fatalf("diff = %v", st)
	}
	s, err := c.OpenStream(ctx, StreamTar, sid, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := transfer.Send(ctx, m, transfer.Need(m, st), s, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("stream close: %v", err)
	}
	st, _ = c.ManifestDiff(ctx, m, sid)
	if st[0] != transfer.StagedSame {
		t.Fatalf("after stream: %v", st)
	}
	rep, err := c.Install(ctx, m, sid)
	if err != nil || rep.Installed != 1 {
		t.Fatalf("install: %+v %v", rep, err)
	}
	if _, err := os.Stat(m.Entries[0].Dst); err != nil {
		t.Errorf("not installed on dest: %v", err)
	}

	// a failing stream surfaces on Close with the remote stderr
	bad, _ := c.OpenStream(ctx, StreamTar, sid, "s2")
	io.WriteString(bad, "garbage")
	if err := bad.Close(); err == nil || !strings.Contains(err.Error(), "gzip") {
		t.Errorf("bad stream Close = %v, want remote gzip error", err)
	}

	// log stream reads the remote job log. StreamLog is a receive-direction
	// kind: per ServeStream's documented half-close contract (server.go),
	// the client must signal "nothing to send" by half-closing stdin before
	// it starts reading stdout, or both sides deadlock (server stuck
	// waiting for stdin EOF; client stuck waiting for data). Client.OpenStream
	// performs that half-close itself for receive-direction kinds before
	// returning the stream, so the read-then-close below is safe as written.
	os.WriteFile(filepath.Join(job.Dir(destPaths.DataDir, sid), "log.txt"), []byte("remote log\n"), 0o600)
	lg, err := c.OpenStream(ctx, StreamLog, sid, "l")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	io.Copy(&buf, lg)
	lg.Close()
	if buf.String() != "remote log\n" {
		t.Errorf("log stream = %q", buf.String())
	}

	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if _, err := c.Hello(ctx); err == nil {
		t.Errorf("calls after Close must fail")
	}
}
