package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
	t.Cleanup(func() { sc.Close() })

	c, err := NewClient(context.Background(), sc, "claude-teleport", t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, dest, destPaths
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
