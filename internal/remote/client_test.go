package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/sshx"
	"github.com/mithro/go-claude-teleport/internal/sshx/sshtest"
	"github.com/mithro/go-claude-teleport/internal/transfer"
	"github.com/mithro/go-claude-teleport/internal/version"
)

// pipeClient wires a Client to a Local through net.Pipe; streams are opened
// directly on the Local.
func pipeClient(t *testing.T, l *Local) *Client { return pipeEndpointClient(t, l) }

// pipeEndpointClient is pipeClient over ANY Endpoint — a Client included,
// which is how the chained-server case is tested (I5).
func pipeEndpointClient(t *testing.T, ep Endpoint) *Client {
	t.Helper()
	a, b := net.Pipe()
	go func() { Serve(context.Background(), b, b, ep); b.Close() }()
	c, err := NewClientConn(context.Background(), a, ep.OpenStream, t.Logf)
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

// TestNewClientErrorPreservesUnderlyingErrorTypeThroughNoisyStderr is the
// regression test for review finding R-1: NewClient's stderr-replacement
// path used to build a brand-new error from the captured stderr text
// alone, discarding the underlying error entirely — so a genuine *Error
// (e.g. Hello's protocol-mismatch usage error) reaching the client
// alongside ANY stderr noise (an rc-file banner, a stray warning, ...) lost
// its type, and remotecfg.go's `errors.As(err, &pe)` check — which reads
// pe.Code to pick an exit code — could never see it, and its own message
// text was discarded too. The wrapped error must survive both ways.
func TestNewClientErrorPreservesUnderlyingErrorTypeThroughNoisyStderr(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	_, signer := sshtest.WriteKeyFile(t, filepath.Join(home, ".ssh"), "id_ed25519", "")
	exec := func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
		// Noise unrelated to the real failure — stands in for anything a
		// remote shell might write to stderr before the protocol takes
		// over (a warning, a banner, ...), which must never mask the real
		// error the wire protocol goes on to report.
		io.WriteString(stderr, "sh: warning: setlocale: LC_ALL: cannot change locale\n")
		ep := stubEndpoint{hello: func() (HostInfo, error) {
			return HostInfo{Protocol: version.Protocol + 1, Version: "v9.9"}, nil
		}}
		if err := Serve(context.Background(), stdin, stdout, ep); err != nil {
			io.WriteString(stderr, err.Error())
			return 1
		}
		return 0
	}
	srv := sshtest.New(t, sshtest.Options{Authorized: []ssh.PublicKey{signer.PublicKey()}, Exec: exec})
	host, portStr, err := net.SplitHostPort(srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	kh := filepath.Join(home, ".ssh", "known_hosts")
	os.WriteFile(kh, []byte(sshtest.KnownHostsLine("["+host+"]:"+portStr, srv.HostKey)), 0o600)
	sc, err := sshx.Dial(context.Background(), sshx.Resolved{Target: sshx.Target{User: "bob", Host: host, Port: port}, HostName: host}, nil, nil,
		sshx.Options{KnownHostsFile: kh, Home: home, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Close() })

	_, err = NewClient(context.Background(), sc, "claude-teleport", t.Logf)
	if err == nil {
		t.Fatal("want an error")
	}
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != "usage" {
		t.Fatalf("err = %v, want it to unwrap (errors.As) to a *remote.Error{Code:\"usage\"} despite the stderr noise", err)
	}
	if !strings.Contains(err.Error(), "protocol mismatch") {
		t.Errorf("err = %v, want the protocol-mismatch message text preserved too", err)
	}
}

// newSSHTestClient wires a Client to a fresh Local ("dest") through an
// in-process sshd whose exec handler runs `<exe> remote serve` and
// `<exe> remote stream ...` in-process — the same harness
// TestClientTransferOverSSHTest used inline, factored out so other tests
// (the pack round trip below) can reuse it without duplicating the ssh
// dial boilerplate.
func newSSHTestClient(t *testing.T) (*Client, *Local, session.Paths) {
	t.Helper()
	return newSSHTestClientOpts(t, nil)
}

// newSSHTestClientOpts is newSSHTestClient with the far side's
// LocalOptions adjustable — the server-side Local is built here, so a test
// that needs the peer to have a tmux probe (suspended-pane discovery) has
// no other seam to reach it through.
func newSSHTestClientOpts(t *testing.T, tweak func(*LocalOptions)) (*Client, *Local, session.Paths) {
	t.Helper()
	destPaths := testPaths(t)
	destOpts := LocalOptions{Logf: t.Logf}
	if tweak != nil {
		tweak(&destOpts)
	}
	dest := NewLocal(destPaths, "claude-teleport", destOpts)
	exec := func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
		// cmd is now client.remoteCommand's PATH-fallback wrapper (HK-2),
		// not the bare "claude-teleport remote serve"/"... stream ..." line
		// — recover the intended argv rather than requiring an exact token
		// count. remoteSubcommand's own real-shell correctness is covered
		// separately (TestNewClient*PATH* below); this fixture only needs
		// to know WHICH op to dispatch to, same as it always has.
		args, ok := remoteSubcommand(cmd)
		if !ok {
			io.WriteString(stderr, "unexpected command "+cmd)
			return 127
		}
		switch {
		case len(args) == 2 && args[0] == "remote" && args[1] == "serve":
			if err := Serve(context.Background(), stdin, stdout, dest); err != nil {
				io.WriteString(stderr, err.Error())
				return 1
			}
			return 0
		case len(args) == 5 && args[0] == "remote" && args[1] == "stream":
			if err := ServeStream(context.Background(), StreamKind(args[2]), args[3], args[4], stdin, stdout, dest); err != nil {
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
	t.Cleanup(func() { sc.Close() })

	c, err := NewClient(context.Background(), sc, "claude-teleport", t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, dest, destPaths
}

// remoteSubcommandRE recovers the ["remote","serve"] or
// ["remote","stream",kind,jobID,streamID] argv embedded in a
// client.remoteCommand payload (HK-2): the wrapper script repeats
// `exec <bin> remote serve` (or `... remote stream ...`) verbatim in every
// branch it tries, so the first occurrence names the intended op — every
// value these tests ever pass (uuids, stream kinds, "kind:n" ids) is a
// sshx.Quote "safe word", so it always appears unquoted here.
var remoteSubcommandRE = regexp.MustCompile(`\bremote (serve|stream \S+ \S+ \S+)\b`)

func remoteSubcommand(cmd string) ([]string, bool) {
	m := remoteSubcommandRE.FindString(cmd)
	if m == "" {
		return nil, false
	}
	return strings.Fields(m), true
}

// buildTestRemoteExe builds the real cmd/claude-teleport binary, so the
// HK-2 PATH-fallback test below installs and runs the genuine article —
// proving client.remoteCommand's generated /bin/sh script is correct
// against a real POSIX shell and a real binary, not a Go-level stand-in
// for either. It has one caller; no cross-test memoization is attempted
// (a cached path under an earlier test's t.TempDir() would dangle once
// that test's cleanup ran).
func buildTestRemoteExe(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "claude-teleport")
	cmd := osexec.Command("go", "build", "-o", out, "github.com/mithro/go-claude-teleport/cmd/claude-teleport")
	if o, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build claude-teleport: %v\n%s", err, o)
	}
	return out
}

// realLoginShellExec is a sshtest.Options.Exec that runs cmd through a REAL
// /bin/sh -c with env, standing in for whatever login shell a real sshd
// would hand the command to: client.remoteCommand (HK-2) always targets
// /bin/sh itself precisely so it does not matter which shell that is, and
// this fixture exercises that real interpreter rather than a Go-level
// simulation of one.
//
// stdin is wired via StdinPipe + a detached copy goroutine rather than
// assigning the ssh.Channel reader straight to Cmd.Stdin: os/exec's
// automatic io.Reader-Stdin mode makes Cmd.Wait (and so Run) block until
// that copy goroutine's own Read returns, and an ssh.Channel never does
// that on its own — nothing closes the CLIENT's write side until it gets a
// response, which for the "binary not found" script (it never reads stdin
// at all, exec'ing straight to `echo ...; exit 127`) is a real deadlock:
// Run never returns, so this Exec call — and the whole ssh exec request —
// never completes. Managing the copy ourselves lets Wait return the moment
// the child actually exits, matching what a real sshd session does.
func realLoginShellExec(env []string) func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
	return func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
		c := osexec.Command("/bin/sh", "-c", cmd)
		c.Stdout, c.Stderr = stdout, stderr
		c.Env = env
		inPipe, err := c.StdinPipe()
		if err != nil {
			io.WriteString(stderr, err.Error())
			return 1
		}
		if err := c.Start(); err != nil {
			io.WriteString(stderr, err.Error())
			return 1
		}
		go func() { io.Copy(inPipe, stdin); inPipe.Close() }()
		if err := c.Wait(); err != nil {
			var ee *osexec.ExitError
			if errors.As(err, &ee) {
				return ee.ExitCode()
			}
			io.WriteString(stderr, err.Error())
			return 1
		}
		return 0
	}
}

// dialOverRealShellFixture wires an sshx.Client to a fresh in-process sshd
// whose exec handler is realLoginShellExec(remoteEnv) — a real /bin/sh, not
// the Go-level Serve/ServeStream dispatch newSSHTestClientOpts uses.
func dialOverRealShellFixture(t *testing.T, remoteEnv []string) *sshx.Client {
	t.Helper()
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	_, signer := sshtest.WriteKeyFile(t, filepath.Join(home, ".ssh"), "id_ed25519", "")
	srv := sshtest.New(t, sshtest.Options{
		Authorized: []ssh.PublicKey{signer.PublicKey()},
		Exec:       realLoginShellExec(remoteEnv),
	})
	host, portStr, err := net.SplitHostPort(srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	kh := filepath.Join(home, ".ssh", "known_hosts")
	os.WriteFile(kh, []byte(sshtest.KnownHostsLine("["+host+"]:"+portStr, srv.HostKey)), 0o600)
	sc, err := sshx.Dial(context.Background(), sshx.Resolved{Target: sshx.Target{User: "bob", Host: host, Port: port}, HostName: host}, nil, nil,
		sshx.Options{KnownHostsFile: kh, Home: home, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Close() })
	return sc
}

// TestNewClientFindsRemoteExeUnderHomeLocalBinWhenPATHLacksIt is HK-2's
// regression test for the real-world bug: `claude-teleport remote serve`
// exec'd through the user's NON-interactive login shell failed with
// "zsh:1: command not found: claude-teleport" (and Hello then saw only
// "connection closed: EOF") because the native installer puts the binary
// under $HOME/.local/bin, which many shells only add to PATH for
// interactive sessions. A real claude-teleport is installed ONLY there —
// PATH here deliberately excludes it, mirroring that non-interactive
// shell — and NewClient must still find it and complete Hello.
func TestNewClientFindsRemoteExeUnderHomeLocalBinWhenPATHLacksIt(t *testing.T) {
	built := buildTestRemoteExe(t)
	remoteHome := t.TempDir()
	localBin := filepath.Join(remoteHome, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localBin, "claude-teleport"), data, 0o755); err != nil {
		t.Fatal(err)
	}
	noTmux := filepath.Join(t.TempDir(), "no-tmux-here")
	remoteEnv := []string{"HOME=" + remoteHome, "PATH=/usr/bin:/bin", "TMUX_TMPDIR=" + noTmux}

	sc := dialOverRealShellFixture(t, remoteEnv)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := NewClient(ctx, sc, "claude-teleport", t.Logf)
	if err != nil {
		t.Fatalf("NewClient must fall back to $HOME/.local/bin when PATH lacks claude-teleport: %v", err)
	}
	defer c.Close()
	if c.Info().Hostname == "" {
		t.Errorf("Hello over the PATH-fallback path returned an empty HostInfo: %+v", c.Info())
	}
}

// TestNewClientReportsClearErrorWhenRemoteExeIsNowhere is HK-2's negative
// case: no claude-teleport anywhere the fallback looks. NewClient must fail
// with a message naming the binary and that it was not found LEADING the
// message, never a bare "connection closed: EOF" up front the way the bug
// report actually saw it (which gave no hint the problem was PATH-related
// at all). readLoop's underlying error text is still chained on afterward
// via %w (R-1: errors.As must keep working, see
// TestNewClientErrorPreservesUnderlyingErrorTypeThroughNoisyStderr) — the
// fix is that it no longer REPLACES the explanation, not that it disappears.
func TestNewClientReportsClearErrorWhenRemoteExeIsNowhere(t *testing.T) {
	// Hermetic: swap the two absolute fallbacks (the real /usr/local/bin,
	// /usr/bin — outside this test's control, and a real claude-teleport
	// COULD theoretically live there) for fresh, guaranteed-empty temp
	// dirs, so "not found" can never depend on the test machine's actual
	// filesystem. The $HOME-based fallbacks are already hermetic (remoteHome
	// below is a fresh t.TempDir()).
	origFallbacks := remoteBinFallbacks
	remoteBinFallbacks = []string{"$HOME/.local/bin", "$HOME/bin", filepath.Join(t.TempDir(), "usr-local-bin"), filepath.Join(t.TempDir(), "usr-bin")}
	t.Cleanup(func() { remoteBinFallbacks = origFallbacks })

	remoteHome := t.TempDir()
	noTmux := filepath.Join(t.TempDir(), "no-tmux-here")
	remoteEnv := []string{"HOME=" + remoteHome, "PATH=/usr/bin:/bin", "TMUX_TMPDIR=" + noTmux}

	sc := dialOverRealShellFixture(t, remoteEnv)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := NewClient(ctx, sc, "claude-teleport", t.Logf)
	if err == nil {
		t.Fatal("want an error naming the missing binary and the paths tried")
	}
	msg := err.Error()
	for _, want := range []string{"claude-teleport", "not found", ".local/bin"} {
		if !strings.Contains(msg, want) {
			t.Errorf("err = %v, missing %q", err, want)
		}
	}
	if i, j := strings.Index(msg, "not found"), strings.Index(msg, "connection closed"); i < 0 || (j >= 0 && j < i) {
		t.Errorf("err = %v: the clear explanation must lead, not follow, any bare connection-closed text", err)
	}
}

func TestClientTransferOverSSHTest(t *testing.T) {
	c, _, destPaths := newSSHTestClient(t)
	if c.Info().Hostname == "" {
		t.Errorf("hello over ssh: %+v", c.Info())
	}

	// Task 16: InventoryGit over the wire, dispatched through the same
	// persistent `remote serve` connection (not a new exec).
	repo := t.TempDir()
	gitc(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a"), []byte("a"), 0o644)
	gitc(t, repo, "add", "a")
	gitc(t, repo, "commit", "-q", "-m", "i")
	if info, err := c.InventoryGit(context.Background(), repo); err != nil || info.Branch != "main" {
		t.Errorf("InventoryGit over ssh: info=%+v err=%v", info, err)
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
	s, err := c.OpenStream(ctx, StreamTar, sid, "recv:1")
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
	bad, _ := c.OpenStream(ctx, StreamTar, sid, "recv:2")
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
	lg, err := c.OpenStream(ctx, StreamLog, sid, "send:1")
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

// TestClientOpenStreamPackBothDirectionsOverSSH is the regression test for
// the half-close bug fixed in this round: Client.openStream used to gate
// its eager half-close on `kind != StreamTar`, which closed stdin on EVERY
// pack stream immediately (pack didn't exist as a kind when that check was
// written) — breaking a pack PUSH (recv-direction: the driver writes into
// the stream) even though a pack PULL (send-direction: the driver only
// reads) happened to still work by accident. The fix gates on
// splitStreamID's parsed direction instead of the kind, so both directions
// are exercised here over the real ssh round trip (in-process sshd).
func TestClientOpenStreamPackBothDirectionsOverSSH(t *testing.T) {
	c, dest, destPaths := newSSHTestClient(t)
	ctx := context.Background()

	t.Run("push (recv): driver writes pack bytes into the dest", func(t *testing.T) {
		const jobID = "pack-push-job"
		s, err := c.OpenStream(ctx, StreamPack, jobID, "recv:1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(s, "FAKE-PACK-BYTES"); err != nil {
			// Under the bug, stdin was half-closed before this write ever
			// happened: it would fail here with a broken-pipe-shaped error.
			t.Fatalf("write pack push: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(job.StagingDir(destPaths.DataDir, jobID), "objects.pack"))
		if err != nil || string(got) != "FAKE-PACK-BYTES" {
			t.Errorf("objects.pack = %q, err = %v", got, err)
		}
	})

	t.Run("pull (send): driver reads a real packfile out of the source", func(t *testing.T) {
		const jobID = "pack-pull-job"
		repo := t.TempDir()
		gitc(t, repo, "init", "-q", "-b", "main")
		os.WriteFile(filepath.Join(repo, "a"), []byte("a"), 0o644)
		gitc(t, repo, "add", "a")
		gitc(t, repo, "commit", "-q", "-m", "i")
		tip := strings.TrimSpace(gitc(t, repo, "rev-parse", "HEAD"))

		// dest doubles as the pack-send "source" host here: any Local can
		// serve either direction, keyed only by the journal's plan.
		planBytes, err := json.Marshal(planView{Git: &gitx.Plan{SrcMain: repo, Tip: tip, PackEntryID: gitx.NoEntry, IndexEntryID: gitx.NoEntry}})
		if err != nil {
			t.Fatal(err)
		}
		j, err := job.New(destPaths.DataDir, jobID)
		if err != nil {
			t.Fatal(err)
		}
		j.Plan = planBytes
		if err := dest.JournalPut(ctx, j); err != nil {
			t.Fatal(err)
		}

		s, err := c.OpenStream(ctx, StreamPack, jobID, "send:1")
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, s); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if buf.Len() == 0 || !bytes.HasPrefix(buf.Bytes(), []byte("PACK")) {
			t.Errorf("pack pull = %d bytes, prefix %q, want a git packfile (PACK magic)", buf.Len(), buf.Bytes()[:min(4, buf.Len())])
		}
	})
}

// paneProbe is a session.PaneProbe stand-in for the server side of the
// ssh harness (the real one is tmuxx.Prober, which needs a live tmux
// server): panes maps a pane id to the foreground argv ListSessions
// classifies with session.ArgvSessionID.
type paneProbe struct {
	panes  map[string][]string
	infos  []session.PaneInfo
	socket string
}

func (p *paneProbe) PaneCommand(paneID string) ([]string, int, bool) {
	argv, ok := p.panes[paneID]
	return argv, 0, ok
}
func (p *paneProbe) FindWindow(string, string) ([]string, error) { return nil, nil }
func (p *paneProbe) ListPanes() ([]session.PaneInfo, error)      { return p.infos, nil }
func (p *paneProbe) SocketPath() string                          { return p.socket }

// TestListSessionsOverSSHReportsSuspended is the wire half of the
// suspended-state fix: `list --host` dispatches to Local.ListSessions on
// the far side, so a session whose only trace there is a placeholder pane
// has to arrive as "suspended". Until Local.ListSessions consulted
// opts.Probe (as session.Load and ResolveSession already did) it arrived
// as "idle" — indistinguishable from a session nobody had ever suspended,
// which is the one state the placeholder mechanism exists to make visible.
func TestListSessionsOverSSHReportsSuspended(t *testing.T) {
	suspendedID := session.ID("1a2b3c4d-5e6f-4a1b-8c2d-3e4f5a6b7c8d")
	idleID := session.ID("2b3c4d5e-6f7a-4b1c-9d3e-4f5a6b7c8d9e")
	probe := &paneProbe{
		socket: "/run/tmux/default",
		panes: map[string][]string{
			// A placeholder holding suspendedID, and — in the same
			// window — an ordinary shell that must be ignored rather
			// than misread as a session.
			"%4": {"claude-teleport", "placeholder", "--resume", string(suspendedID)},
			"%5": {"-bash"},
		},
		infos: []session.PaneInfo{
			{Session: "work", WindowID: "@2", PaneID: "%4"},
			{Session: "work", WindowID: "@2", PaneID: "%5"},
		},
	}
	c, _, destPaths := newSSHTestClientOpts(t, func(o *LocalOptions) { o.Probe = probe })

	for _, tc := range []struct {
		id  session.ID
		cwd string
	}{{suspendedID, "/home/bob/suspended"}, {idleID, "/home/bob/idle"}} {
		proj := destPaths.ProjectDir(tc.cwd)
		if err := os.MkdirAll(proj, 0o700); err != nil {
			t.Fatal(err)
		}
		line := `{"type":"user","cwd":"` + tc.cwd + `","timestamp":"2026-01-01T00:00:00Z"}` + "\n"
		if err := os.WriteFile(filepath.Join(proj, string(tc.id)+".jsonl"), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	sums, err := c.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[session.ID]SessionSummary{}
	for _, s := range sums {
		byID[s.ID] = s
	}
	got, ok := byID[suspendedID]
	if !ok {
		t.Fatalf("list over ssh returned no row for %s: %+v", suspendedID, sums)
	}
	if got.State != session.StateSuspended.String() {
		t.Errorf("session held by a placeholder pane over the wire = %q, want %q", got.State, session.StateSuspended)
	}
	if got.Tmux != "work:@2.%4" {
		t.Errorf("suspended row tmux ref = %q, want the placeholder's pane", got.Tmux)
	}
	if got := byID[idleID]; got.State != session.StateIdle.String() || got.Tmux != "" {
		t.Errorf("session with no pane and no registry entry = %+v, want idle with no tmux ref", got)
	}
}
