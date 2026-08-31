// internal/orchestrate/steps_test.go
package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

func TestStepNamesMatchSpecOrder(t *testing.T) {
	want := []string{"preflight", "freeze", "capture", "transfer", "install", "git-attach", "start", "shape", "thaw+exit", "record"}
	if len(StepNames) != len(want) {
		t.Fatalf("StepNames = %v", StepNames)
	}
	for i := range want {
		if StepNames[i] != want[i] {
			t.Errorf("step %d = %q, want %q", i, StepNames[i], want[i])
		}
	}
}

func TestFreezeAndThawVerifySkipWhenSourceNotRunning(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	p := &Plan{Session: &session.Session{ID: session.ID(sid), State: session.StateIdle}, TargetState: "idle"}
	j := &job.Journal{ID: sid}
	steps := Steps(p, j, src.ep, dst.ep, selfExe(t), t.Logf)
	byName := map[string]job.Step{}
	for _, s := range steps {
		byName[s.Name] = s
	}
	for _, name := range []string{"freeze", "capture", "thaw+exit"} {
		done, err := byName[name].Verify(context.Background())
		if err != nil || !done {
			t.Errorf("%s.Verify on an idle source = %v %v, want done", name, done, err)
		}
	}
	done, err := byName["shape"].Verify(context.Background())
	if err != nil || !done {
		t.Errorf("shape.Verify with no tmux and target idle = %v %v, want done", done, err)
	}
}

func TestRecordVerifyUsesJournal(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	p := &Plan{Session: &session.Session{ID: session.ID(sid)}}
	j := &job.Journal{ID: sid}
	steps := Steps(p, j, src.ep, dst.ep, selfExe(t), t.Logf)
	rec := steps[len(steps)-1]
	if done, _ := rec.Verify(context.Background()); done {
		t.Error("record must run when the journal has no done record step")
	}
	j.Step("record").Status = job.Done
	if done, _ := rec.Verify(context.Background()); !done {
		t.Error("record must be skipped once the journal says done")
	}
}

// TestInstallVerifyDistrustsStaleJournalDoneAgainstDestReality is controller
// ruling A(2): a journal that claims a step is Done must not be trusted
// blindly — install.Verify always re-diffs the destination, so a journal
// marked Done for a destination that has nothing installed yet must still
// report not-done (job.Run then re-Runs the step; job.Run itself never
// special-cases a journal-Done step when a Verify func is present, see
// internal/job/run.go).
func TestInstallVerifyDistrustsStaleJournalDoneAgainstDestReality(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := filepath.Join(src.paths.Home, "x")
	seedSession(t, src, cwd)
	o := baseOptions()
	o.State = "idle"
	p, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatal(err)
	}
	j := &job.Journal{ID: sid}
	j.Step("install").Status = job.Done // stale: nothing has actually been installed on dst
	steps := Steps(p, j, src.ep, dst.ep, selfExe(t), t.Logf)
	var install job.Step
	for _, s := range steps {
		if s.Name == "install" {
			install = s
		}
	}
	done, err := install.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("install.Verify trusted a stale journal Done instead of checking the destination")
	}
}

// TestJobRunStopsAtNextStepBoundaryOnCancelledContext is controller ruling
// A(1): job.Run checks ctx.Err() at every step boundary (after a Verify
// that returns not-done, before that step's Run — see internal/job/run.go).
// This exercises that guarantee through a REAL orchestrate step (capture),
// whose Verify (r.p.Session.Tmux == nil) is a pure Plan-field check that
// never returns done while a pane is known — spec §6 table's "capture:
// else never (cheap)" — so it reliably reaches the ctx.Err() check without
// needing a live tmux/claude process.
func TestJobRunStopsAtNextStepBoundaryOnCancelledContext(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	p := &Plan{
		Session: &session.Session{
			ID: session.ID(sid), State: session.StateIdle,
			Tmux: &session.TmuxRef{Session: "s", WindowID: "@1", PaneID: "%1"}, // non-nil: capture.Verify never returns done
		},
		TargetState: "idle",
	}
	j, err := job.New(t.TempDir(), sid) // job.Run's Save() at each step boundary needs a real Dir
	if err != nil {
		t.Fatal(err)
	}
	all := Steps(p, j, src.ep, dst.ep, selfExe(t), t.Logf)
	var capture job.Step
	for _, s := range all {
		if s.Name == "capture" {
			capture = s
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	warmup := job.Step{
		Name:   "warmup",
		Verify: func(context.Context) (bool, error) { return false, nil },
		Run:    func(context.Context) error { cancel(); return nil }, // cancels between the two steps
	}

	err = job.Run(ctx, j, []job.Step{warmup, capture}, t.Logf)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := j.Step("warmup").Status; got != job.Done {
		t.Errorf("warmup status = %v, want Done (it ran before cancellation)", got)
	}
	if got := j.Step("capture").Status; got != job.Failed {
		t.Errorf("capture status = %v, want Failed (the journal must record where the run stopped)", got)
	}
	// capture.Run must never have been reached: it would call r.src.Capture
	// against a tmux ref that does not exist on src's fake tmux server and
	// fail loudly, which this test would surface as a wrong error above.
}

// TestGitAttachRestoresDeferredEntryModesAfterInstallEvictsManifest is
// controller ruling C: remote.Local.GitAttach now reads each dirty/index
// entry's Mode off jobs/<jobID>/manifest.json (task 20 point C). But the
// install step's own Verify (verifyInstall -> installManifest ->
// dst.ManifestDiff) persists a REDUCED manifest on the destination that
// excludes exactly those deferred entries (existing-main git-attach applies
// them itself) — so by the time git-attach runs, that file is missing the
// entries whose Mode it needs. runGitAttach re-diffs the FULL manifest
// (discarding the resulting statuses) before calling GitAttach specifically
// to restore them; this test fails without that restore; see steps.go's
// runGitAttach comment for the full account.
func TestGitAttachRestoresDeferredEntryModesAfterInstallEvictsManifest(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)

	main := filepath.Join(dst.paths.Home, "x")
	makeRepo(t, main)
	tip := strings.TrimSpace(gitc(t, main, "rev-parse", "HEAD"))
	w := filepath.Join(main, ".worktrees", "feat")

	jobID := sid
	staging := job.StagingDir(dst.paths.DataDir, jobID)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "7"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := os.ReadFile(filepath.Join(main, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "8"), idx, 0o644); err != nil {
		t.Fatal(err)
	}

	gp := &gitx.Plan{Mode: gitx.ModeExistingMain, SrcMain: "/home/alice/x", SrcWorktree: "/home/alice/x/.worktrees/feat",
		DstMain: main, DstWorktree: w, Linked: true, WorktreeName: "feat", Branch: "feat", Tip: tip, NeedPack: false,
		IndexRel: ".git/worktrees/feat/index", IndexEntryID: 8, PackEntryID: gitx.NoEntry,
		DirtyEntries: map[string]int{filepath.Join(w, "new.txt"): 7}}

	// The driver's own manifest copy (p.ManifestPath) records the dirty
	// file's real mode as 0755, distinct from the 0644 the staged copy
	// above was written with, so the mode assertion below can only pass
	// if GitAttach reads Mode off the manifest rather than the staged
	// file's own on-disk mode.
	m := &transfer.Manifest{Version: 1, JobID: jobID, Entries: []transfer.Entry{
		// Size must match the staged copy's actual size: transfer.Diff's
		// stagedState (called by the restore ManifestDiff below) removes a
		// staged file outright when its size disagrees with the manifest.
		{ID: 7, Category: session.CatWorktree, Dst: filepath.Join(w, "new.txt"), Mode: 0o755, Size: int64(len("untracked\n"))},
		{ID: 8, Category: session.CatRepo, Dst: filepath.Join(main, ".git", "worktrees", "feat", "index"), Mode: 0o644, Size: int64(len(idx))},
	}}
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	if err := m.Save(manifestPath); err != nil {
		t.Fatal(err)
	}

	p := &Plan{JobID: jobID, ManifestPath: manifestPath, Git: gp, Extras: &transfer.InstallExtras{}}
	j := &job.Journal{ID: jobID}
	r := &runner{p: p, j: j, src: src.ep, dst: dst.ep, selfExe: selfExe(t), logf: t.Logf}

	// Reproduce the eviction: install's Verify diffs and persists the
	// REDUCED manifest (no deferred entries) before git-attach ever runs.
	if _, err := r.verifyInstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	saved, err := transfer.Load(filepath.Join(job.Dir(dst.paths.DataDir, jobID), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Entries) != 0 {
		t.Fatalf("test setup: expected install's diff to evict the deferred entries from the saved manifest, got %d entries", len(saved.Entries))
	}

	if err := r.runGitAttach(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(w, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Errorf("dirty file mode = %o, want 0755 (from the manifest, not the staged copy's 0644)", st.Mode().Perm())
	}
}
