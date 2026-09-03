package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/sshx"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
	"github.com/mithro/go-claude-teleport/internal/transfer"
	"github.com/mithro/go-claude-teleport/internal/version"
)

// Client implements Endpoint over a request/response line connection.
type Client struct {
	conn       io.ReadWriteCloser
	openStream func(ctx context.Context, kind StreamKind, jobID, streamID string) (io.ReadWriteCloser, error)
	logf       func(string, ...any)
	wait       func() error // remote process wait (nil for raw conns)

	wmu     sync.Mutex
	mu      sync.Mutex
	nextID  int
	pending map[int]chan Response
	closed  bool
	readErr error

	info  HostInfo
	paths session.Paths
}

var _ Endpoint = (*Client)(nil)

// NewClientConn builds a Client on any line connection, performs hello and
// paths. openStream may be nil when streams are not needed (tests).
func NewClientConn(ctx context.Context, conn io.ReadWriteCloser, openStream func(ctx context.Context, kind StreamKind, jobID, streamID string) (io.ReadWriteCloser, error), logf func(string, ...any)) (*Client, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	c := &Client{conn: conn, openStream: openStream, logf: logf, pending: map[int]chan Response{}}
	go c.readLoop()
	if err := c.call(ctx, OpHello, HelloArgs{Version: version.Version, Protocol: version.Protocol}, &c.info); err != nil {
		c.Close()
		return nil, fmt.Errorf("hello: %w", err)
	}
	var pr PathsResult
	if err := c.call(ctx, OpPaths, struct{}{}, &pr); err != nil {
		c.Close()
		return nil, fmt.Errorf("paths: %w", err)
	}
	c.paths = pr.Paths
	return c, nil
}

// sshConn adapts an sshx.Process to io.ReadWriteCloser.
type sshConn struct {
	p *sshx.Process
}

func (s sshConn) Read(b []byte) (int, error)  { return s.p.Stdout.Read(b) }
func (s sshConn) Write(b []byte) (int, error) { return s.p.Stdin.Write(b) }
func (s sshConn) Close() error                { s.p.Stdin.Close(); return s.p.Close() }

// remoteBinFallbacks lists the well-known install locations checked, in
// order, when `command -v` does not find the remote binary on the ssh
// session's PATH. HK-2 (real-world bug): `claude-teleport remote serve`
// exec'd through the target's NON-interactive login shell failed with
// "zsh:1: command not found: claude-teleport" — Hello then saw nothing but
// "connection closed: EOF" — because the native installer places the
// binary under $HOME/.local/bin, which a non-interactive shell's PATH often
// lacks (only the interactive rc file adds it, and ssh exec always runs
// `<login-shell> -c <command>` non-interactively, whichever shell that is).
var remoteBinFallbacks = []string{"$HOME/.local/bin", "$HOME/bin", "/usr/local/bin", "/usr/bin"}

// remoteCommand builds the ssh exec payload that starts exe with argv on
// the far side. It always runs through `/bin/sh -c`, never the account's
// own login shell directly: that is what makes the fallback logic
// predictable POSIX syntax no matter which shell the account actually uses
// (zsh, bash, dash, ...) — the whole point, since the login shell choice is
// exactly what caused HK-2. It tries `command -v exe` first (the ordinary
// case), then each of remoteBinFallbacks in turn; if none exist it prints
// ONE line naming exe and every location tried to stderr and exits 127, so
// a missing install surfaces as that message (NewClient below turns it into
// the returned error) instead of a bare "connection closed: EOF".
func remoteCommand(exe string, argv []string) string {
	rest := sshx.Quote(argv)
	exeQ := sshx.Quote([]string{exe})
	var b strings.Builder
	fmt.Fprintf(&b, "if command -v %s >/dev/null; then exec %s %s; fi; ", exeQ, exeQ, rest)
	var tried []string
	for _, dir := range remoteBinFallbacks {
		cand := dir + "/" + exe
		tried = append(tried, cand)
		fmt.Fprintf(&b, `if [ -x "%s" ]; then exec "%s" %s; fi; `, cand, cand, rest)
	}
	fmt.Fprintf(&b, `echo "claude-teleport: %s not found on PATH or in %s" 1>&2; exit 127`, exe, strings.Join(tried, ", "))
	return "/bin/sh -c " + sshx.Quote([]string{b.String()})
}

// NewClient runs `<exe> remote serve` over ssh (via remoteCommand's PATH
// fallback, HK-2) and performs hello.
func NewClient(ctx context.Context, ssh *sshx.Client, remoteExe string, logf func(string, ...any)) (*Client, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	p, err := ssh.Start(ctx, remoteCommand(remoteExe, []string{"remote", "serve"}))
	if err != nil {
		return nil, fmt.Errorf("start remote helper on %s: %w", ssh, err)
	}
	// Captured (not just logged) so a failure to even start the remote
	// helper — the HK-2 "binary not found anywhere" case chief among them —
	// can surface its one explanatory stderr line as the actual returned
	// error below, rather than leaving the caller to infer a PATH problem
	// from readLoop's bare "connection closed: EOF". stderrDone is closed
	// once the pipe reports EOF, which p.Close() below forces; waiting on
	// it (only on the error path) avoids a race between reading the buffer
	// and the scanner goroutine still filling it in.
	var stderrMu sync.Mutex
	var stderrLines []string
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		sc := bufio.NewScanner(p.Stderr)
		for sc.Scan() {
			line := sc.Text()
			logf("remote %s: %s", ssh, line)
			stderrMu.Lock()
			stderrLines = append(stderrLines, line)
			stderrMu.Unlock()
		}
	}()
	openStream := func(ctx context.Context, kind StreamKind, jobID, streamID string) (io.ReadWriteCloser, error) {
		// streamID carries the direction (streams.go's table): "send:<n>"
		// means the REMOTE host produces data (tar/pack/capture/log send —
		// the driver here only reads), "recv:<n>" means the driver writes
		// into the remote's runStream (tar/pack recv). Validate up front,
		// before starting the remote process, so a malformed streamID never
		// leaks an ssh session.
		dir, err := splitStreamID(streamID)
		if err != nil {
			return nil, fmt.Errorf("open stream %s/%s on %s: %w", kind, streamID, ssh, err)
		}
		sp, err := ssh.Start(ctx, remoteCommand(remoteExe, []string{"remote", "stream", string(kind), jobID, streamID}))
		if err != nil {
			return nil, fmt.Errorf("open stream %s/%s on %s: %w", kind, streamID, ssh, err)
		}
		st := &sshStream{p: sp, kind: kind, id: streamID}
		// Half-close contract (documented on ServeStream in server.go): for
		// send-direction streams (the remote host produces data; this driver
		// has nothing to write) the client half-closes stdin immediately,
		// before any read, signalling the remote's runStream it has no
		// inbound payload to wait for. Doing this here means every caller of
		// OpenStream gets it for free instead of having to know the
		// contract. recv-direction streams (the driver writes into the
		// stream, e.g. a tar or pack push) are left open: their Close
		// half-closes stdin only once writing is done.
		if dir == "send" {
			if err := st.CloseWrite(); err != nil {
				st.Close()
				return nil, fmt.Errorf("half-close stream %s/%s on %s: %w", kind, streamID, ssh, err)
			}
		}
		return st, nil
	}
	c, err := NewClientConn(ctx, sshConn{p}, openStream, logf)
	if err != nil {
		p.Close()
		<-stderrDone
		stderrMu.Lock()
		tail := strings.Join(stderrLines, "; ")
		stderrMu.Unlock()
		if tail != "" {
			// The remote helper never started (HK-2's not-found case, or
			// any other early stderr): that one line explains the failure
			// far better than err's own text (readLoop's generic
			// "connection closed: EOF"), so it replaces it here rather
			// than being appended alongside it.
			return nil, fmt.Errorf("start remote helper on %s: %s", ssh, tail)
		}
		return nil, err
	}
	c.wait = p.Wait
	return c, nil
}

// sshStream is a bulk channel on its own ssh session.
type sshStream struct {
	p    *sshx.Process
	kind StreamKind
	id   string

	cwOnce sync.Once
	cwErr  error

	closeOnce sync.Once
	closeErr  error
}

func (s *sshStream) Read(b []byte) (int, error)  { return s.p.Stdout.Read(b) }
func (s *sshStream) Write(b []byte) (int, error) { return s.p.Stdin.Write(b) }

// CloseWrite half-closes stdin, signalling end-of-inbound to the remote
// ServeStream. Idempotent; safe to call before Close (Close calls it too).
//
// io.EOF from the underlying Close is swallowed, not an error: since Task
// 16, a send-direction runStream (tar/pack/capture/log send) never reads
// stdin at all and can finish and tear down its ssh channel entirely on
// its own — this call can race a remote that already exited, in which
// case the channel is already gone and Close reports EOF. That is exactly
// the outcome CloseWrite is trying to produce anyway (the remote no longer
// wants any more input), so it must not surface as a failure.
func (s *sshStream) CloseWrite() error {
	s.cwOnce.Do(func() {
		err := s.p.Stdin.Close()
		if errors.Is(err, io.EOF) {
			err = nil
		}
		s.cwErr = err
	})
	return s.cwErr
}

// Close half-closes stdin (a no-op if CloseWrite already ran), drains any
// trailing stdout and the remote stderr, then waits for the remote process:
// a non-zero exit becomes the error, with the drained remote stderr text.
// Idempotent.
func (s *sshStream) Close() error {
	s.closeOnce.Do(func() {
		s.CloseWrite()
		var stderr strings.Builder
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); io.Copy(io.Discard, s.p.Stdout) }()
		go func() { defer wg.Done(); io.Copy(&stderr, s.p.Stderr) }()
		wg.Wait()
		err := s.p.Wait()
		s.p.Close()
		if err != nil {
			s.closeErr = fmt.Errorf("stream %s/%s: %w: %s", s.kind, s.id, err, strings.TrimSpace(stderr.String()))
		}
	})
	return s.closeErr
}

func (c *Client) readLoop() {
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 1<<20), maxLine)
	for sc.Scan() {
		var resp Response
		if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
			c.logf("remote: bad response line: %v", err)
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		delete(c.pending, resp.ID)
		c.mu.Unlock()
		if ok {
			ch <- resp
		} else {
			c.logf("remote: response for unknown id %d", resp.ID)
		}
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	c.mu.Lock()
	c.readErr = err
	for id, ch := range c.pending {
		ch <- Response{ID: id, Error: &Error{Code: "internal", Message: "connection closed: " + err.Error()}}
		delete(c.pending, id)
	}
	c.mu.Unlock()
}

func (c *Client) call(ctx context.Context, op string, args any, result any) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("%s: encode args: %w", op, err)
	}
	c.mu.Lock()
	if c.closed || c.readErr != nil {
		c.mu.Unlock()
		return &Error{Code: "internal", Message: op + ": client closed"}
	}
	c.nextID++
	id := c.nextID
	ch := make(chan Response, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	line, _ := json.Marshal(Request{ID: id, Op: op, Args: raw})
	c.wmu.Lock()
	_, werr := c.conn.Write(append(line, '\n'))
	c.wmu.Unlock()
	if werr != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("%s: send: %w", op, werr)
	}
	select {
	case resp := <-ch:
		if !resp.OK {
			if resp.Error == nil {
				return &Error{Code: "internal", Message: op + ": no error in failed response"}
			}
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("%s: decode result: %w", op, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("%s: %w", op, ctx.Err())
	}
}

// Close ends the helper (EOF on its stdin) and fails pending calls.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	err := c.conn.Close()
	if c.wait != nil {
		if werr := c.wait(); werr != nil && !errors.Is(werr, io.EOF) {
			c.logf("remote helper exit: %v", werr)
		}
	}
	return err
}

// Info is the Hello result.
func (c *Client) Info() HostInfo { return c.info }

func (c *Client) Hello(ctx context.Context) (HostInfo, error) {
	var info HostInfo
	err := c.call(ctx, OpHello, HelloArgs{Version: version.Version, Protocol: version.Protocol}, &info)
	return info, err
}

func (c *Client) Paths() session.Paths { return c.paths }

func (c *Client) ResolveSession(ctx context.Context, sel session.Selector) (*session.Session, error) {
	var r ResolveSessionResult
	err := c.call(ctx, OpResolveSession, ResolveSessionArgs{Selector: sel}, &r)
	return r.Session, err
}

func (c *Client) InventorySession(ctx context.Context, id session.ID) (*session.Inventory, *session.Usage, error) {
	var r InventorySessionResult
	err := c.call(ctx, OpInventorySession, InventorySessionArgs{ID: id}, &r)
	return r.Inventory, r.Usage, err
}

func (c *Client) InventoryHost(ctx context.Context, cwd, claudeVersion string) (*claudecfg.Inventory, error) {
	var r InventoryHostResult
	err := c.call(ctx, OpInventoryHost, InventoryHostArgs{Cwd: cwd, ClaudeVersion: claudeVersion}, &r)
	return r.Inventory, err
}

func (c *Client) InventoryGit(ctx context.Context, cwd string) (*gitx.Info, error) {
	var r InventoryGitResult
	err := c.call(ctx, OpInventoryGit, InventoryGitArgs{Cwd: cwd}, &r)
	return r.Info, err
}

func (c *Client) GitDestState(ctx context.Context, mainDir, worktreeDir, branch string) (*gitx.DestState, error) {
	var r GitDestStateResult
	err := c.call(ctx, OpGitDestState, GitDestStateArgs{MainDir: mainDir, WorktreeDir: worktreeDir, Branch: branch}, &r)
	return r.State, err
}

func (c *Client) InventoryTmux(ctx context.Context, ref *session.TmuxRef, preferredSocket string) (*tmuxx.Facts, error) {
	var r InventoryTmuxResult
	err := c.call(ctx, OpInventoryTmux, InventoryTmuxArgs{Ref: ref, PreferredSocket: preferredSocket}, &r)
	return r.Facts, err
}

func (c *Client) ManifestDiff(ctx context.Context, m *transfer.Manifest, jobID string) (map[int]transfer.Status, error) {
	var r ManifestDiffResult
	err := c.call(ctx, OpManifestDiff, ManifestDiffArgs{Manifest: m, JobID: jobID}, &r)
	return r.Statuses, err
}

func (c *Client) PutInstallExtras(ctx context.Context, jobID string, extra transfer.InstallExtras) error {
	return c.call(ctx, OpInstallExtras, InstallExtrasArgs{JobID: jobID, Extra: extra}, nil)
}

func (c *Client) OpenStream(ctx context.Context, kind StreamKind, jobID, streamID string) (io.ReadWriteCloser, error) {
	if c.openStream == nil {
		return nil, &Error{Code: "unavailable", Message: "streams not configured on this client"}
	}
	return c.openStream(ctx, kind, jobID, streamID)
}

func (c *Client) Install(ctx context.Context, m *transfer.Manifest, jobID string) (*transfer.InstallReport, error) {
	var r InstallResult
	err := c.call(ctx, OpInstall, InstallArgs{Manifest: m, JobID: jobID}, &r)
	return r.Report, err
}

func (c *Client) GitAttach(ctx context.Context, plan *gitx.Plan, jobID string) error {
	return c.call(ctx, OpGitAttach, GitAttachArgs{Plan: plan, JobID: jobID}, nil)
}

func (c *Client) Freeze(ctx context.Context, pid int, startTime string, ref *session.TmuxRef) error {
	return c.call(ctx, OpFreeze, FreezeArgs{PID: pid, StartTime: startTime, Ref: ref}, nil)
}

func (c *Client) Thaw(ctx context.Context, pid int, ref *session.TmuxRef) error {
	return c.call(ctx, OpThaw, ThawArgs{PID: pid, Ref: ref}, nil)
}

func (c *Client) Capture(ctx context.Context, ref *session.TmuxRef, jobID string) error {
	return c.call(ctx, OpCapture, CaptureArgs{Ref: ref, JobID: jobID}, nil)
}

func (c *Client) OpenWindow(ctx context.Context, p *tmuxx.Plan) (*session.TmuxRef, error) {
	var r OpenWindowResult
	err := c.call(ctx, OpOpenWindow, OpenWindowArgs{Plan: p}, &r)
	return r.Ref, err
}

func (c *Client) StartClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, jobID string, argv []string) error {
	return c.call(ctx, OpStartClaude, StartClaudeArgs{Ref: ref, ID: id, JobID: jobID, Argv: argv}, nil)
}

func (c *Client) ConfirmClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, timeout time.Duration) (*session.Registry, error) {
	var r ConfirmClaudeResult
	err := c.call(ctx, OpConfirmClaude, ConfirmClaudeArgs{Ref: ref, ID: id, Timeout: timeout}, &r)
	return r.Registry, err
}

func (c *Client) ExitClaude(ctx context.Context, ref *session.TmuxRef, pid int, startTime string, timeout time.Duration) error {
	return c.call(ctx, OpExitClaude, ExitClaudeArgs{Ref: ref, PID: pid, StartTime: startTime, Timeout: timeout}, nil)
}

func (c *Client) TypeCommand(ctx context.Context, ref *session.TmuxRef, argv []string) error {
	return c.call(ctx, OpTypeCommand, TypeCommandArgs{Ref: ref, Argv: argv}, nil)
}

func (c *Client) PaneState(ctx context.Context, ref *session.TmuxRef) (*tmuxx.PaneState, error) {
	var r PaneStateResult
	err := c.call(ctx, OpPaneState, PaneStateArgs{Ref: ref}, &r)
	return r.State, err
}

func (c *Client) RunPtyResume(ctx context.Context, id session.ID, cwd string, timeout time.Duration) error {
	return c.call(ctx, OpRunPtyResume, RunPtyResumeArgs{ID: id, Cwd: cwd, Timeout: timeout}, nil)
}

func (c *Client) JournalGet(ctx context.Context, jobID string) (*job.Journal, bool, error) {
	var r JournalGetResult
	err := c.call(ctx, OpJournalGet, JournalGetArgs{JobID: jobID}, &r)
	if r.Journal != nil {
		r.Journal.Dir = job.Dir(c.paths.DataDir, jobID)
	}
	return r.Journal, r.Found, err
}

func (c *Client) JournalPut(ctx context.Context, j *job.Journal) error {
	return c.call(ctx, OpJournalPut, JournalPutArgs{Journal: j}, nil)
}

func (c *Client) Record(ctx context.Context, jobID string, rec job.HistoryRecord) error {
	return c.call(ctx, OpRecord, RecordArgs{JobID: jobID, Record: rec}, nil)
}
