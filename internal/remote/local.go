package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
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

	// Sleep is used for every bounded poll (ConfirmClaude, ExitClaude);
	// nil defaults to time.Sleep. Tests inject a no-op / counting stub.
	Sleep func(time.Duration)

	Probe session.PaneProbe
	Tmux  tmuxx.Dialer // nil = tmux unavailable
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

	// procs scans the process table (opts.ProcRoot); shared by the tmux
	// and Claude ops so a poll loop's every iteration rescans fresh.
	procs func() (*procx.Table, error)
}

func NewLocal(p session.Paths, selfExe string, opts LocalOptions) *Local {
	if opts.ProcRoot == "" {
		opts.ProcRoot = "/proc"
	}
	p.ProcRoot = opts.ProcRoot
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	if opts.Sleep == nil {
		opts.Sleep = time.Sleep
	}
	host, _ := os.Hostname()
	return &Local{paths: p, selfExe: selfExe, opts: opts, Hostname: host, freezers: map[int]*procx.Freezer{},
		procs: func() (*procx.Table, error) { return procx.Scan(opts.ProcRoot) }}
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

// InventoryGit, GitDestState, GitFiles, GitSourceFacts and GitAttach are
// implemented in local_git.go.

// InventoryTmux, TmuxSessions, OpenWindow, Capture, TypeCommand, PaneState
// and KillWindow are implemented in local_tmux.go. StartClaude,
// ConfirmClaude, ExitClaude and ClaudeStatus are implemented in
// local_claude.go. RunPtyResume is implemented in local_pty.go.

// jobDir/stagingDir/extrasPath join a job id straight into the data dir,
// so every exported method that reaches them checks the id first with
// checkJobID (R-P3-23n) — the wire dispatch validates too, but Local is
// reachable without it.
func (l *Local) jobDir(jobID string) string     { return job.Dir(l.paths.DataDir, jobID) }
func (l *Local) stagingDir(jobID string) string { return job.StagingDir(l.paths.DataDir, jobID) }
func (l *Local) extrasPath(jobID string) string { return filepath.Join(l.jobDir(jobID), "extras.json") }

// ManifestDiff persists the manifest under jobs/<id>/ (the tar stream
// receiver needs it) and classifies every entry.
func (l *Local) ManifestDiff(ctx context.Context, m *transfer.Manifest, jobID string) (map[int]transfer.Status, error) {
	if err := checkJobID(jobID); err != nil {
		return nil, err
	}
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
	if err := checkJobID(jobID); err != nil {
		return err
	}
	raw, err := json.Marshal(extra)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(l.jobDir(jobID), 0o700); err != nil {
		return err
	}
	return os.WriteFile(l.extrasPath(jobID), raw, 0o600)
}

// OpenStream is implemented in streams.go (runStream drives all four kinds,
// keyed by the direction streamID carries).

func (l *Local) Install(ctx context.Context, m *transfer.Manifest, jobID string) (*transfer.InstallReport, error) {
	if err := checkJobID(jobID); err != nil {
		return nil, err
	}
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

func (l *Local) Thaw(ctx context.Context, pid int, ref *session.TmuxRef) error {
	l.mu.Lock()
	f, ok := l.freezers[pid]
	delete(l.freezers, pid)
	l.mu.Unlock()
	// A Local that never froze this pid is the NORMAL case for a re-dial:
	// the freezer belongs to the process that ran step 4, and a dropped
	// link, a killed runner or a plain `continue` all thaw from a fresh
	// Local whose freezers map is empty. Releasing the SIGSTOP is then
	// somebody else's job (the freezer helper exits with its owner and
	// SIGCONTs on pipe EOF), but handing the terminal back is still ours:
	// skipping restoreForeground here left the pane shell holding the pty,
	// so the resumed Claude re-stopped on SIGTTIN and spec §6.3's "/exit"
	// was typed into the shell instead (B2). restoreForeground's own
	// guards no-op when there is genuinely nothing to restore.
	if ok {
		if err := f.Thaw(); err != nil {
			return err
		}
	}
	return l.restoreForeground(ctx, pid, ref)
}

// foregroundPoll bounds the wait for the shell to hand the pty back.
const (
	foregroundPoll    = 100 * time.Millisecond
	foregroundTimeout = 10 * time.Second
)

// restoreForeground gives the thawed job the pty back.
//
// An interactive shell is a job-control shell: when its foreground job
// stops — by SIGSTOP from the freezer just as much as by ^Z — it takes the
// terminal back for itself and prints "[1]+ Stopped". SIGCONT resumes the
// job's execution but not its claim on the terminal, so its next read gets
// SIGTTIN and stops it again: the thawed Claude is left in state T for
// good, and everything typed at the pane afterwards (the "/exit" of spec
// §6.3, or a user's own keystrokes) lands on the shell instead. Only a
// process whose controlling terminal this is may tcsetpgrp it back, which
// rules out the freezer, the runner and the remote helper alike — so the
// one process that can fix it is the shell itself, and the way to ask is
// its own `fg`.
//
// Nothing is typed unless the pty's foreground really has moved to a
// process group led by a shell: with job control off (or with Claude as
// the pane's own command) the job never lost the terminal and there is
// nothing to restore.
func (l *Local) restoreForeground(ctx context.Context, pid int, ref *session.TmuxRef) error {
	if ref == nil || l.opts.Tmux == nil {
		return nil
	}
	pgid, err := procx.ProcGroup(l.opts.ProcRoot, pid)
	if err != nil {
		return nil // the target is gone: nothing to foreground
	}
	fg, err := procx.ForegroundGroup(l.opts.ProcRoot, pid)
	if err != nil || fg <= 0 || fg == pgid {
		return nil
	}
	procs, err := l.procs()
	if err != nil {
		return err
	}
	holder, ok := procs.Get(fg)
	if !ok || !tmuxx.IsShell(holder.Comm) {
		l.opts.Logf("thaw: pid %d is not the foreground of its terminal (group %d, held by %q) and cannot be restored", pid, fg, holder.Comm)
		return nil
	}
	l.opts.Logf("thaw: the pane shell (pid %d) took the terminal back while pid %d was stopped; typing fg", fg, pid)
	t, err := l.dial(ctx, ref.SocketPath)
	if err != nil {
		return err
	}
	defer t.Close()
	if err := tmuxx.TypeCommand(ctx, t, ref.PaneID, []string{"fg"}); err != nil {
		return err
	}
	deadline := time.Now().Add(foregroundTimeout)
	for {
		if fg, err := procx.ForegroundGroup(l.opts.ProcRoot, pid); err != nil || fg == pgid {
			return nil // restored, or the process is gone
		}
		if time.Now().After(deadline) {
			return &Error{Code: "conflict", Message: fmt.Sprintf("thawed claude (pid %d) did not get its terminal back within %s", pid, foregroundTimeout)}
		}
		l.opts.Sleep(foregroundPoll)
	}
}

func (l *Local) JournalGet(ctx context.Context, jobID string) (*job.Journal, bool, error) {
	if err := checkJobID(jobID); err != nil {
		return nil, false, err
	}
	return job.Open(l.paths.DataDir, jobID)
}

func (l *Local) JournalPut(ctx context.Context, j *job.Journal) error {
	if j == nil {
		return &Error{Code: "usage", Message: "journal-put: nil journal"}
	}
	if err := checkJobID(j.ID); err != nil {
		return err
	}
	if err := os.MkdirAll(l.jobDir(j.ID), 0o700); err != nil {
		return err
	}
	j.Dir = l.jobDir(j.ID)
	return j.Save()
}

func (l *Local) Record(ctx context.Context, jobID string, rec job.HistoryRecord) error {
	if err := checkJobID(jobID); err != nil {
		return err
	}
	return job.AppendHistory(l.jobDir(jobID), rec)
}
