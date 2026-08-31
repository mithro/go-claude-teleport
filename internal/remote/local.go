package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
	"github.com/mithro/go-claude-teleport/internal/version"
)

type LocalOptions struct {
	// ProcRoot is where /proc is mounted; "" defaults to "/proc". NewLocal
	// wires it into the session.Paths it stores (session.Paths.ProcRoot is
	// what session.Resolve/Load consult; the package no longer has a global
	// default, so Local is the one place that must set it).
	ProcRoot string

	// TmuxSocketDir is reported by Hello verbatim; "" means unknown. Local
	// must not read the environment itself (TMUX_TMPDIR, etc.) — internal/cli
	// resolves it and passes it in here.
	TmuxSocketDir string

	Probe session.PaneProbe
	Tmux  TmuxDialer // Plan 03; nil = tmux unavailable
	Logf  func(string, ...any)
}

// Local is the in-process implementation used on whichever side is local
// and by Server.
type Local struct {
	paths    session.Paths
	selfExe  string
	opts     LocalOptions
	Hostname string

	mu       sync.Mutex
	freezers map[int]*procx.Freezer
}

func NewLocal(p session.Paths, selfExe string, opts LocalOptions) *Local {
	if opts.ProcRoot == "" {
		opts.ProcRoot = "/proc"
	}
	p.ProcRoot = opts.ProcRoot
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	host, _ := os.Hostname()
	return &Local{paths: p, selfExe: selfExe, opts: opts, Hostname: host, freezers: map[int]*procx.Freezer{}}
}

var _ Endpoint = (*Local)(nil)

func (l *Local) Hello(ctx context.Context) (HostInfo, error) {
	info := HostInfo{
		Version: version.Version, Protocol: version.Protocol, Hostname: l.Hostname,
		OS: runtime.GOOS, Arch: runtime.GOARCH, UID: os.Getuid(),
		Home: l.paths.Home, ConfigDir: l.paths.ConfigDir, DataDir: l.paths.DataDir,
		// Controller ruling: Local must not read the environment. The
		// socket dir is whatever internal/cli resolved and passed in;
		// "" means unknown, reported verbatim.
		TmuxSocketDir: l.opts.TmuxSocketDir,
	}
	_, err := exec.LookPath("tmux")
	info.HasTmux = err == nil
	_, err = exec.LookPath("claude-resume")
	info.HasClaudeResume = err == nil
	if claudePath, err := exec.LookPath("claude"); err == nil {
		info.HasClaude = true
		cmd := exec.CommandContext(ctx, claudePath, "--version")
		out, verr := cmd.Output()
		if verr != nil {
			// Controller ruling: `claude --version` is the one allowed
			// subprocess here, and its failure must not be swallowed.
			info.ClaudeVersionErr = verr.Error()
		} else {
			info.ClaudeVersion = firstLine(string(out))
		}
	}
	return info, nil
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	return s
}

func (l *Local) Paths() session.Paths { return l.paths }

func (l *Local) ResolveSession(ctx context.Context, sel session.Selector) (*session.Session, error) {
	return session.Resolve(l.paths, sel, l.opts.Probe)
}

func (l *Local) InventorySession(ctx context.Context, id session.ID) (*session.Inventory, *session.Usage, error) {
	s, err := session.Load(l.paths, id, l.opts.Probe)
	if err != nil {
		return nil, nil, err
	}
	inv, err := session.InventoryFiles(s)
	if err != nil {
		return nil, nil, err
	}
	usage, err := session.ScanUsage(s)
	if err != nil {
		return nil, nil, err
	}
	return inv, usage, nil
}

func (l *Local) InventoryHost(ctx context.Context, cwd, claudeVersion string) (*claudecfg.Inventory, error) {
	return claudecfg.Collect(l.paths, cwd, l.Hostname, claudeVersion)
}

func (l *Local) InventoryGit(ctx context.Context, cwd string) (*GitInfo, error) {
	return nil, Unavailable(OpInventoryGit)
}
func (l *Local) GitDestState(ctx context.Context, mainDir, worktreeDir, branch string) (*GitDestState, error) {
	return nil, Unavailable(OpGitDestState)
}
func (l *Local) InventoryTmux(ctx context.Context, ref *session.TmuxRef, preferredSocket string) (*TmuxFacts, error) {
	return nil, Unavailable(OpInventoryTmux)
}

func (l *Local) jobDir(jobID string) string     { return job.Dir(l.paths.DataDir, jobID) }
func (l *Local) stagingDir(jobID string) string { return job.StagingDir(l.paths.DataDir, jobID) }
func (l *Local) extrasPath(jobID string) string { return filepath.Join(l.jobDir(jobID), "extras.json") }

// ManifestDiff persists the manifest under jobs/<id>/ (the tar stream
// receiver needs it) and classifies every entry.
func (l *Local) ManifestDiff(ctx context.Context, m *transfer.Manifest, jobID string) (map[int]transfer.Status, error) {
	if m == nil {
		return nil, &Error{Code: "usage", Message: "manifest-diff: nil manifest"}
	}
	if err := os.MkdirAll(l.jobDir(jobID), 0o700); err != nil {
		return nil, err
	}
	if err := m.Save(filepath.Join(l.jobDir(jobID), "manifest.json")); err != nil {
		return nil, err
	}
	return transfer.Diff(ctx, m, l.stagingDir(jobID))
}

// PutInstallExtras stores the merge inputs for Install under jobs/<id>/extras.json.
func (l *Local) PutInstallExtras(ctx context.Context, jobID string, extra transfer.InstallExtras) error {
	raw, err := json.Marshal(extra)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(l.jobDir(jobID), 0o700); err != nil {
		return err
	}
	return os.WriteFile(l.extrasPath(jobID), raw, 0o600)
}

// tarStream is the write side of a receive: bytes written go to Receive;
// Close waits for it and returns its verdict.
//
// Contract (controller ruling, Task 15): a tarStream is send-direction only
// — the driver writes the tar payload into it, nothing flows back out. Its
// Read must therefore never block: it always returns (0, io.EOF) so that
// ServeStream's concurrent "copy the stream to stdout" goroutine (which runs
// unconditionally for every stream kind) finishes immediately instead of
// hanging on a direction nobody uses.
type tarStream struct {
	pw   *io.PipeWriter
	done chan error
	once sync.Once
	err  error
}

func (t *tarStream) Read(p []byte) (int, error)  { return 0, io.EOF }
func (t *tarStream) Write(p []byte) (int, error) { return t.pw.Write(p) }
func (t *tarStream) Close() error {
	t.once.Do(func() {
		t.pw.Close()
		t.err = <-t.done
	})
	return t.err
}

// readStream is the read side of a receive-direction stream (capture, log):
// this host produces data, the client only reads. Its Write must never
// block: it fails immediately instead of hanging on a direction nobody uses
// (see tarStream's contract note above; ServeStream always runs both
// directions concurrently regardless of stream kind).
type readStream struct{ io.ReadCloser }

func (r readStream) Write(p []byte) (int, error) { return 0, errors.New("read-only stream") }

func (l *Local) OpenStream(ctx context.Context, kind StreamKind, jobID, streamID string) (io.ReadWriteCloser, error) {
	switch kind {
	case StreamTar:
		m, err := transfer.Load(filepath.Join(l.jobDir(jobID), "manifest.json"))
		if err != nil {
			return nil, fmt.Errorf("stream tar %s: manifest-diff must run first: %w", jobID, err)
		}
		pr, pw := io.Pipe()
		t := &tarStream{pw: pw, done: make(chan error, 1)}
		go func() {
			err := transfer.Receive(ctx, m, pr, l.stagingDir(jobID), func(e transfer.Entry, n int64) {
				l.opts.Logf("received entry %d %s (%d bytes total)", e.ID, e.Dst, n)
			})
			pr.CloseWithError(err)
			t.done <- err
		}()
		return t, nil
	case StreamCapture:
		f, err := os.Open(filepath.Join(l.jobDir(jobID), "capture.txt"))
		if err != nil {
			return nil, fmt.Errorf("stream capture %s: %w", jobID, err)
		}
		return readStream{f}, nil
	case StreamLog:
		f, err := os.Open(filepath.Join(l.jobDir(jobID), "log.txt"))
		if err != nil {
			return nil, fmt.Errorf("stream log %s: %w", jobID, err)
		}
		return readStream{f}, nil
	case StreamPack:
		return nil, Unavailable("stream pack")
	}
	return nil, &Error{Code: "usage", Message: "unknown stream kind " + string(kind)}
}

func (l *Local) Install(ctx context.Context, m *transfer.Manifest, jobID string) (*transfer.InstallReport, error) {
	st, err := transfer.Diff(ctx, m, l.stagingDir(jobID))
	if err != nil {
		return nil, err
	}
	var extra transfer.InstallExtras
	raw, err := os.ReadFile(l.extrasPath(jobID))
	if err == nil {
		if err := json.Unmarshal(raw, &extra); err != nil {
			return nil, fmt.Errorf("install %s: extras.json: %w", jobID, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return transfer.Install(ctx, m, st, l.stagingDir(jobID), l.paths, extra)
}

func (l *Local) GitAttach(ctx context.Context, plan *GitPlan, jobID string) error {
	return Unavailable(OpGitAttach)
}

func (l *Local) Freeze(ctx context.Context, pid int, startTime string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.freezers[pid]; ok {
		return nil
	}
	f, err := procx.Freeze(l.selfExe, pid, startTime)
	if err != nil {
		return err
	}
	l.freezers[pid] = f
	return nil
}

func (l *Local) Thaw(ctx context.Context, pid int) error {
	l.mu.Lock()
	f, ok := l.freezers[pid]
	delete(l.freezers, pid)
	l.mu.Unlock()
	if !ok {
		return nil // not frozen by us: no-op (spec §6 step 9)
	}
	return f.Thaw()
}

func (l *Local) Capture(ctx context.Context, ref *session.TmuxRef, jobID string) error {
	return Unavailable(OpCapture)
}
func (l *Local) OpenWindow(ctx context.Context, p *TmuxPlan) (*session.TmuxRef, error) {
	return nil, Unavailable(OpOpenWindow)
}
func (l *Local) StartClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, jobID string, argv []string) error {
	return Unavailable(OpStartClaude)
}
func (l *Local) ConfirmClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, timeout time.Duration) (*session.Registry, error) {
	return nil, Unavailable(OpConfirmClaude)
}
func (l *Local) ExitClaude(ctx context.Context, ref *session.TmuxRef, pid int, startTime string, timeout time.Duration) error {
	return Unavailable(OpExitClaude)
}
func (l *Local) TypeCommand(ctx context.Context, ref *session.TmuxRef, argv []string) error {
	return Unavailable(OpTypeCommand)
}
func (l *Local) PaneState(ctx context.Context, ref *session.TmuxRef) (*TmuxPaneState, error) {
	return nil, Unavailable(OpPaneState)
}
func (l *Local) RunPtyResume(ctx context.Context, id session.ID, cwd string, timeout time.Duration) error {
	return Unavailable(OpRunPtyResume)
}

func (l *Local) JournalGet(ctx context.Context, jobID string) (*job.Journal, bool, error) {
	return job.Open(l.paths.DataDir, jobID)
}

func (l *Local) JournalPut(ctx context.Context, j *job.Journal) error {
	if j == nil {
		return &Error{Code: "usage", Message: "journal-put: nil journal"}
	}
	if err := os.MkdirAll(l.jobDir(j.ID), 0o700); err != nil {
		return err
	}
	j.Dir = l.jobDir(j.ID)
	return j.Save()
}

func (l *Local) Record(ctx context.Context, jobID string, rec job.HistoryRecord) error {
	return job.AppendHistory(l.jobDir(jobID), rec)
}
