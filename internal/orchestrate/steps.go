// internal/orchestrate/steps.go
package orchestrate

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// StepNames is the spec §6 order.
var StepNames = []string{"preflight", "freeze", "capture", "transfer", "install", "git-attach", "start", "shape", "thaw+exit", "record"}

var shells = map[string]bool{"bash": true, "zsh": true, "sh": true, "fish": true, "dash": true}

type runner struct {
	p        *Plan
	j        *job.Journal
	src, dst remote.Endpoint
	selfExe  string
	logf     func(string, ...any)
}

// Steps builds the job.Step list for the plan (spec §6 table).
func Steps(p *Plan, j *job.Journal, src, dst remote.Endpoint, selfExe string, logf func(string, ...any)) []job.Step {
	r := &runner{p: p, j: j, src: src, dst: dst, selfExe: selfExe, logf: logf}
	return []job.Step{
		{Name: "preflight", Verify: r.verifyPreflight, Run: r.runPreflight},
		{Name: "freeze", Verify: r.verifyFreeze, Run: r.runFreeze},
		{Name: "capture", Verify: r.verifyCapture, Run: r.runCapture},
		{Name: "transfer", Verify: r.verifyTransfer, Run: r.runTransfer},
		{Name: "install", Verify: r.verifyInstall, Run: r.runInstall},
		{Name: "git-attach", Verify: r.verifyGitAttach, Run: r.runGitAttach},
		{Name: "start", Verify: r.verifyStart, Run: r.runStart},
		{Name: "shape", Verify: r.verifyShape, Run: r.runShape},
		{Name: "thaw+exit", Verify: r.verifyThawExit, Run: r.runThawExit},
		{Name: "record", Verify: r.verifyRecord, Run: r.runRecord},
	}
}

// persist saves the plan into the journal here and on both hosts.
func (r *runner) persist(ctx context.Context) error {
	raw, err := r.p.ToJSON()
	if err != nil {
		return err
	}
	r.j.Plan = raw
	r.j.UpdatedAt = time.Now()
	if r.j.Dir != "" {
		if err := r.j.Save(); err != nil {
			return err
		}
	}
	if err := r.src.JournalPut(ctx, r.j); err != nil {
		return fmt.Errorf("journal put (source): %w", err)
	}
	if err := r.dst.JournalPut(ctx, r.j); err != nil {
		return fmt.Errorf("journal put (destination): %w", err)
	}
	return nil
}

func (r *runner) manifest() (*transfer.Manifest, error) { return transfer.Load(r.p.ManifestPath) }

func (r *runner) id() session.ID { return r.p.Session.ID }

func (r *runner) attempt(step string) string { return strconv.Itoa(r.j.Step(step).Attempts) }

// ---- 1 preflight --------------------------------------------------------

func (r *runner) verifyPreflight(ctx context.Context) (bool, error) {
	_, okS, err := r.src.JournalGet(ctx, r.p.JobID)
	if err != nil {
		return false, err
	}
	_, okD, err := r.dst.JournalGet(ctx, r.p.JobID)
	if err != nil {
		return false, err
	}
	return okS && okD, nil
}

func (r *runner) runPreflight(ctx context.Context) error { return r.persist(ctx) }

// ---- 2 freeze -----------------------------------------------------------

func (r *runner) verifyFreeze(ctx context.Context) (bool, error) {
	return r.p.sourceState() != session.StateRunning, nil
}

func (r *runner) runFreeze(ctx context.Context) error {
	reg := r.p.Session.Registry
	if _, ok, err := r.src.ClaudeStatus(ctx, r.id()); err != nil {
		return err
	} else if !ok {
		r.logf("freeze: source claude (pid %d) is no longer running; nothing to stop", reg.PID)
		return nil
	}
	r.logf("freeze: SIGSTOP pid %d", reg.PID)
	return r.src.Freeze(ctx, reg.PID, reg.ProcStart)
}

// ---- 3 capture ----------------------------------------------------------

func (r *runner) verifyCapture(ctx context.Context) (bool, error) {
	return r.p.Session.Tmux == nil, nil
}

func (r *runner) runCapture(ctx context.Context) error {
	if err := r.src.Capture(ctx, r.p.Session.Tmux, r.p.JobID); err != nil {
		return err
	}
	files := append([]session.FileEntry{}, r.p.Files...)
	capture := session.FileEntry{Root: job.Dir(r.p.SourceInfo.DataDir, r.p.JobID), Rel: "capture.txt", Category: session.CatCapture, Mode: 0o600}
	// CaptureEntryID < 0 (gitx.NoEntry's convention: -1 = "no entry yet", 0
	// is a real manifest id) means the capture entry has never been added;
	// this step re-runs on every pass while the source has a pane (Verify
	// is never done, spec §6 table), so only append once or a resumed job
	// would grow Files with a duplicate capture.txt entry each time.
	if r.p.CaptureEntryID < 0 {
		files = append(files, capture)
		r.p.Files = files
	}
	m, err := r.src.BuildManifest(ctx, r.p.JobID, r.id(), r.p.SourceInfo.Hostname, r.p.DestInfo.Hostname, files, r.p.PathMap)
	if err != nil {
		return err
	}
	memorySrcs := map[string]bool{}
	for _, e := range r.p.Extras.Memory {
		memorySrcs[e.Src] = true
	}
	r.p.annotateManifest(m, memorySrcs)
	for _, e := range m.Entries {
		if e.Category == session.CatCapture {
			r.p.CaptureEntryID = e.ID
			r.p.DestCapture = e.Dst
		}
	}
	if err := m.Save(r.p.ManifestPath); err != nil {
		return err
	}
	if r.p.Statuses, err = r.dst.ManifestDiff(ctx, m, r.p.JobID); err != nil {
		return err
	}
	return r.persist(ctx)
}

// ---- 4 transfer ---------------------------------------------------------

func (r *runner) verifyTransfer(ctx context.Context) (bool, error) {
	m, err := r.manifest()
	if err != nil {
		return false, err
	}
	if r.p.Statuses, err = r.dst.ManifestDiff(ctx, m, r.p.JobID); err != nil {
		return false, err
	}
	if err := r.persist(ctx); err != nil {
		return false, err
	}
	need := transfer.Need(m, r.p.Statuses)
	r.logf("transfer: %d of %d entries still needed", len(need), len(m.Entries))
	return len(need) == 0, nil
}

func (r *runner) pump(ctx context.Context, kind remote.StreamKind, n string) error {
	rd, err := r.src.OpenStream(ctx, kind, r.p.JobID, "send:"+n)
	if err != nil {
		return fmt.Errorf("open %s stream on source: %w", kind, err)
	}
	wr, err := r.dst.OpenStream(ctx, kind, r.p.JobID, "recv:"+n)
	if err != nil {
		rd.Close()
		return fmt.Errorf("open %s stream on destination: %w", kind, err)
	}
	// Both streams are always Closed, and the first error among the copy
	// and the two Closes wins: pack/tar stream integrity comes from the
	// stream's exit status surfaced by Close (task 20 point D), so a
	// Close error must never be swallowed just because io.Copy succeeded.
	_, copyErr := io.Copy(wr, rd)
	srcErr := rd.Close()
	dstErr := wr.Close()
	for _, e := range []error{copyErr, srcErr, dstErr} {
		if e != nil {
			return fmt.Errorf("%s stream: %w", kind, e)
		}
	}
	return nil
}

func (r *runner) runTransfer(ctx context.Context) error {
	if err := r.pump(ctx, remote.StreamTar, r.attempt("transfer")); err != nil {
		return err
	}
	m, err := r.manifest()
	if err != nil {
		return err
	}
	if r.p.Statuses, err = r.dst.ManifestDiff(ctx, m, r.p.JobID); err != nil {
		return err
	}
	if need := transfer.Need(m, r.p.Statuses); len(need) > 0 {
		return fmt.Errorf("%d entries still missing on the destination after the transfer (first: %s)", len(need), mustEntry(m, need[0]).Dst)
	}
	return r.persist(ctx)
}

func mustEntry(m *transfer.Manifest, id int) transfer.Entry {
	e, _ := m.ByID(id)
	return e
}

// ---- 5 install ----------------------------------------------------------

// deferred lists manifest ids that git-attach applies itself.
func (r *runner) deferred() map[int]bool {
	d := map[int]bool{}
	if r.p.Git != nil && r.p.Git.Mode == gitx.ModeExistingMain {
		for _, id := range r.p.Git.DirtyEntries {
			d[id] = true
		}
		// gitx.NoEntry (-1) means "no index entry"; manifest ids are
		// 0-based, so 0 is a real id and must not be treated as absent.
		if r.p.Git.IndexEntryID >= 0 {
			d[r.p.Git.IndexEntryID] = true
		}
	}
	return d
}

func (r *runner) installManifest() (*transfer.Manifest, error) {
	m, err := r.manifest()
	if err != nil {
		return nil, err
	}
	d := r.deferred()
	im := *m
	im.Entries = nil
	for _, e := range m.Entries {
		if !d[e.ID] {
			im.Entries = append(im.Entries, e)
		}
	}
	return &im, nil
}

func (r *runner) verifyInstall(ctx context.Context) (bool, error) {
	im, err := r.installManifest()
	if err != nil {
		return false, err
	}
	st, err := r.dst.ManifestDiff(ctx, im, r.p.JobID)
	if err != nil {
		return false, err
	}
	memory := map[int]bool{}
	for _, e := range r.p.Extras.Memory {
		memory[e.ID] = true
	}
	for _, e := range im.Entries {
		if memory[e.ID] {
			continue
		}
		if st[e.ID] != transfer.PresentSame {
			return false, nil
		}
	}
	return true, nil
}

func (r *runner) runInstall(ctx context.Context) error {
	im, err := r.installManifest()
	if err != nil {
		return err
	}
	if r.p.Extras != nil {
		if err := r.dst.PutInstallExtras(ctx, r.p.JobID, *r.p.Extras); err != nil {
			return fmt.Errorf("install: put extras: %w", err)
		}
	}
	rep, err := r.dst.Install(ctx, im, r.p.JobID)
	if err != nil {
		return err
	}
	r.logf("install: %d installed, %d already present, %d fast-forwarded, index merged %d, history +%d, project entry added %v, memory copied %d (differs %d)",
		rep.Installed, rep.SkippedSame, rep.FastForwarded, rep.IndexMerged, rep.HistoryAdded, rep.ProjectEntryAdded, len(rep.MemoryCopied), len(rep.MemoryDiffers))
	for _, m := range rep.MemoryDiffers {
		r.logf("install: memory file differs on the destination and was left alone: %s", m)
	}
	return nil
}

// ---- 6 git-attach -------------------------------------------------------

func (r *runner) verifyGitAttach(ctx context.Context) (bool, error) {
	g := r.p.Git
	switch g.Mode {
	case gitx.ModeNotRepo:
		return true, nil
	case gitx.ModeFreshMain:
		ds, err := r.dst.GitDestState(ctx, g.DstMain, g.DstWorktree, g.Branch)
		if err != nil {
			return false, err
		}
		if g.Detached {
			return ds.WorktreeExists && ds.MainExists, nil
		}
		return ds.WorktreeExists && ds.WorktreeBranch == g.Branch, nil
	}
	return false, nil
}

func (r *runner) runGitAttach(ctx context.Context) error {
	if r.p.Git.NeedPack {
		if err := r.pump(ctx, remote.StreamPack, r.attempt("git-attach")); err != nil {
			return err
		}
	}
	// The install step's own verify/run (above) diffs and persists
	// jobs/<jobID>/manifest.json on the destination using the REDUCED
	// manifest that excludes the entries git-attach applies itself
	// (deferred(): the dirty worktree files and the index, existing-main
	// only) — see verifyInstall -> installManifest -> ManifestDiff. That
	// overwrite evicts exactly the entries whose Mode remote.Local's
	// GitAttach now reads off the saved manifest (task 20 point C), so
	// the destination's manifest.json is NOT reliably complete by the
	// time we get here. Re-diff the FULL manifest first (discarding the
	// resulting statuses — install already used the reduced diff to
	// decide completion) purely to restore those entries on disk before
	// GitAttach reads them. No wire change: ManifestDiff already exists
	// and is already called this way elsewhere in this runner.
	if len(r.deferred()) > 0 {
		m, err := r.manifest()
		if err != nil {
			return err
		}
		if _, err := r.dst.ManifestDiff(ctx, m, r.p.JobID); err != nil {
			return err
		}
	}
	return r.dst.GitAttach(ctx, r.p.Git, r.p.JobID)
}

// ---- 7 start ------------------------------------------------------------

func refString(ref *session.TmuxRef) string {
	return fmt.Sprintf("%s:%s.%s", ref.Session, ref.WindowID, ref.PaneID)
}

func (r *runner) captureOnDest() string {
	// CaptureEntryID < 0 (gitx.NoEntry's convention) means the capture
	// step never ran (no source pane): 0 is a real manifest id.
	if r.p.CaptureEntryID < 0 {
		return ""
	}
	return r.p.DestCapture
}

func (r *runner) verifyStart(ctx context.Context) (bool, error) {
	if r.p.Tmux == nil {
		return r.p.DestRegistry != nil, nil
	}
	if r.p.DestRef == nil {
		return false, nil
	}
	reg, ok, err := r.dst.ClaudeStatus(ctx, r.id())
	if err != nil {
		return false, err
	}
	if !ok || reg.Tmux != refString(r.p.DestRef) {
		return false, nil
	}
	r.logf("start: session already alive in %s; confirming only", reg.Tmux)
	if r.p.DestRegistry, err = r.dst.ConfirmClaude(ctx, r.p.DestRef, r.id(), r.p.Options.StartTimeout); err != nil {
		return false, err
	}
	return true, r.persist(ctx)
}

func (r *runner) runStart(ctx context.Context) error {
	if r.p.Tmux == nil {
		r.logf("start: no tmux on the destination; resuming under a pty in %s", r.p.DestCwd)
		if err := r.dst.RunPtyResume(ctx, r.id(), r.p.DestCwd, r.p.Options.StartTimeout); err != nil {
			return err
		}
		r.p.DestRegistry = &session.Registry{SessionID: string(r.id()), Status: "exited"}
		return r.persist(ctx)
	}
	if r.p.DestRef != nil {
		st, err := r.dst.PaneState(ctx, r.p.DestRef)
		switch {
		case isCode(err, "not-found"):
			r.p.DestRef = nil
		case err != nil:
			return err
		case !shells[st.Command]:
			return fmt.Errorf("destination pane %s we opened earlier now runs %q; refusing to type over it", r.p.DestRef.PaneID, st.Command)
		}
	}
	if r.p.DestRef == nil {
		sessions, err := r.dst.TmuxSessions(ctx, r.p.Tmux.SocketPath)
		if err != nil {
			return err
		}
		_, exists := tmuxx.BaseSession(sessions, r.p.Tmux.Group)
		ref, err := r.dst.OpenWindow(ctx, r.p.Tmux)
		if err != nil {
			return err
		}
		r.p.DestRef, r.p.CreatedWindow, r.p.CreatedSession = ref, true, !exists
		r.logf("start: opened %s (new session: %v)", refString(ref), !exists)
		if err := r.persist(ctx); err != nil {
			return err
		}
	}
	argv := PlaceholderArgv(r.id(), r.captureOnDest(), true, "", "")
	if err := r.dst.StartClaude(ctx, r.p.DestRef, r.id(), r.p.JobID, argv); err != nil {
		return err
	}
	reg, err := r.dst.ConfirmClaude(ctx, r.p.DestRef, r.id(), r.p.Options.StartTimeout)
	if err != nil {
		return err
	}
	r.logf("start: confirmed pid %d (claude %s) in %s", reg.PID, reg.Version, reg.Tmux)
	r.p.DestRegistry = reg
	r.p.StartedAt = time.Now()
	return r.persist(ctx)
}

// ---- 8 shape ------------------------------------------------------------

func (r *runner) verifyShape(ctx context.Context) (bool, error) {
	switch r.p.TargetState {
	case "running":
		return true, nil
	case "suspended":
		if r.p.DestRef == nil {
			return false, nil
		}
		st, err := r.dst.PaneState(ctx, r.p.DestRef)
		if err != nil {
			return false, err
		}
		_, ok := procx.IsPlaceholderArgv(st.Argv)
		return ok, nil
	case "idle":
		if r.p.Tmux == nil {
			return true, nil
		}
		st, err := r.dst.PaneState(ctx, r.p.DestRef)
		if isCode(err, "not-found") {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return !r.p.CreatedWindow && shells[st.Command], nil
	}
	return false, fmt.Errorf("unknown target state %q", r.p.TargetState)
}

func (r *runner) runShape(ctx context.Context) error {
	reg := r.p.DestRegistry
	if _, ok, err := r.dst.ClaudeStatus(ctx, r.id()); err != nil {
		return err
	} else if ok {
		r.logf("shape: /exit on the destination (pid %d)", reg.PID)
		if err := r.dst.ExitClaude(ctx, r.p.DestRef, reg.PID, reg.ProcStart, r.p.Options.ExitTimeout); err != nil {
			return err
		}
	}
	switch r.p.TargetState {
	case "suspended":
		argv := SuspendArgv(r.id(), r.captureOnDest(), r.p.DestInfo.HasClaudeResume)
		r.logf("shape: typing %v", argv)
		return r.dst.TypeCommand(ctx, r.p.DestRef, argv)
	case "idle":
		if !r.p.CreatedWindow {
			return nil
		}
		st, err := r.dst.PaneState(ctx, r.p.DestRef)
		if err != nil {
			return err
		}
		if !shells[st.Command] {
			r.logf("shape: window %s still runs %q; leaving it", r.p.DestRef.WindowID, st.Command)
			return nil
		}
		r.logf("shape: killing window %s we created", r.p.DestRef.WindowID)
		return r.dst.KillWindow(ctx, r.p.DestRef)
	}
	return nil
}

// ---- 9 thaw+exit --------------------------------------------------------

func (r *runner) sourceAlive(ctx context.Context) (bool, error) {
	reg, ok, err := r.src.ClaudeStatus(ctx, r.id())
	if err != nil || !ok {
		return false, err
	}
	return reg.PID == r.p.Session.Registry.PID, nil
}

func (r *runner) verifyThawExit(ctx context.Context) (bool, error) {
	if r.p.sourceState() != session.StateRunning {
		return true, nil
	}
	alive, err := r.sourceAlive(ctx)
	if err != nil || alive {
		return false, err
	}
	if r.p.Session.Tmux == nil {
		return true, nil
	}
	st, err := r.src.PaneState(ctx, r.p.Session.Tmux)
	if isCode(err, "not-found") {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	_, ok := procx.IsPlaceholderArgv(st.Argv)
	return ok, nil
}

func (r *runner) runThawExit(ctx context.Context) error {
	reg := r.p.Session.Registry
	alive, err := r.sourceAlive(ctx)
	if err != nil {
		return err
	}
	if alive {
		if err := r.src.Thaw(ctx, reg.PID); err != nil && !isCode(err, "not-found") {
			return err
		}
		r.logf("thaw: SIGCONT pid %d", reg.PID)
		if r.p.Options.BangMode {
			if err := r.waitSourceIdle(ctx); err != nil {
				return err
			}
		}
		r.logf("exit: asking the source claude (pid %d) to exit", reg.PID)
		if err := r.src.ExitClaude(ctx, r.p.Session.Tmux, reg.PID, reg.ProcStart, r.p.Options.ExitTimeout); err != nil {
			return err
		}
	}
	if r.p.Session.Tmux == nil {
		return nil
	}
	st, err := r.src.PaneState(ctx, r.p.Session.Tmux)
	if err != nil {
		return err
	}
	if _, ok := procx.IsPlaceholderArgv(st.Argv); ok {
		return nil
	}
	if !shells[st.Command] {
		r.logf("exit: source pane runs %q, not typing the placeholder", st.Command)
		return nil
	}
	capture := ""
	if r.p.CaptureEntryID >= 0 {
		capture = filepath.Join(job.Dir(r.p.SourceInfo.DataDir, r.p.JobID), "capture.txt")
	}
	argv := PlaceholderArgv(r.id(), capture, false, r.p.DestInfo.Hostname, time.Now().UTC().Format(time.RFC3339))
	return r.src.TypeCommand(ctx, r.p.Session.Tmux, argv)
}

// waitSourceIdle (!-mode): the foreground exits once this step starts,
// Claude records the command's result and returns to the prompt.
func (r *runner) waitSourceIdle(ctx context.Context) error {
	deadline := time.Now().Add(r.p.Options.ExitTimeout)
	for {
		reg, ok, err := r.src.ClaudeStatus(ctx, r.id())
		if err != nil {
			return err
		}
		if !ok || reg.Status == "idle" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("source claude did not return to the prompt within %s (status %q)", r.p.Options.ExitTimeout, reg.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// ---- 10 record ----------------------------------------------------------

func (r *runner) verifyRecord(ctx context.Context) (bool, error) {
	return r.j.Step("record").Status == job.Done, nil
}

func (r *runner) runRecord(ctx context.Context) error {
	rec := job.HistoryRecord{At: time.Now().UTC(), SessionID: string(r.id()), Direction: r.p.Options.Direction,
		From: r.p.SourceInfo.Hostname, To: r.p.DestInfo.Hostname, Outcome: "success", Note: "end state " + r.p.TargetState}
	if err := r.src.Record(ctx, r.p.JobID, rec); err != nil {
		return err
	}
	if err := r.dst.Record(ctx, r.p.JobID, rec); err != nil {
		return err
	}
	if err := r.dst.Cleanup(ctx, r.p.JobID); err != nil {
		return err
	}
	return r.src.Cleanup(ctx, r.p.JobID)
}
