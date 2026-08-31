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

// TestInstallVerifyKeepsFullManifestOnDestForGitAttachModes is controller
// ruling R-P3-20c, superseding the first fix-round's approach (relocated,
// not deleted): remote.Local.GitAttach reads each dirty/index entry's Mode
// off jobs/<jobID>/manifest.json (task 20 point C). verifyInstall now diffs
// and persists the FULL manifest (filtering deferred()/memory entries out
// IN MEMORY when deciding done) instead of a manifest reduced to just the
// non-deferred entries, so that file never loses the entries git-attach
// needs Mode for in the first place — no separate restore step in
// runGitAttach is needed any more.
func TestInstallVerifyKeepsFullManifestOnDestForGitAttachModes(t *testing.T) {
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

	if _, err := r.verifyInstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	saved, err := transfer.Load(filepath.Join(job.Dir(dst.paths.DataDir, jobID), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Entries) != 2 {
		t.Fatalf("saved manifest has %d entries, want 2 (install's diff must keep the deferred entries, not evict them)", len(saved.Entries))
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

// TestGitAttachFreshMainLinkedDetachedAlwaysRepairsMetadata is controller
// ruling R-P3-20a (CRITICAL, overrides the brief's table): a fresh-main
// linked worktree's only work is repairLinkedMetadata (gitx.Attach), which
// rewrites the worktree's ".git" file and its "gitdir" backlink to the
// DESTINATION's own absolute paths. GitDestState's WorktreeExists/MainExists
// are satisfied by install having placed the transferred files — not by
// that repair having run — so the pre-fix Verify (case Detached:
// WorktreeExists && MainExists) falsely reported done and skipped the
// repair entirely whenever those two directories already existed, which is
// exactly the state after a resumed job's install step. This reproduces
// that: dstMain differs from the source's path (spec's "differing dest
// paths"), and the worktree's ".git" file still carries the SOURCE's path
// (as a raw file-copy transfer would leave it) until repaired.
func TestGitAttachFreshMainLinkedDetachedAlwaysRepairsMetadata(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)

	dstMain := filepath.Join(dst.paths.Home, "repo") // differs from the source's /home/alice/repo
	dstWorktree := filepath.Join(dstMain, ".worktrees", "feat")
	// A real (if unpopulated) repo, so gitx.DestStateOf's git.PlainOpen
	// succeeds and reports MainExists/WorktreeExists — exactly what a
	// resumed job sees after install has placed a fresh-main transfer's
	// files but repairLinkedMetadata has not run yet.
	if err := os.MkdirAll(dstMain, 0o755); err != nil {
		t.Fatal(err)
	}
	gitc(t, dstMain, "init", "-q", "-b", "main")
	if err := os.MkdirAll(dstWorktree, 0o755); err != nil {
		t.Fatal(err)
	}
	staleGitFile := filepath.Join(dstWorktree, ".git")
	if err := os.WriteFile(staleGitFile, []byte("gitdir: /home/alice/repo/.git/worktrees/feat\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gp := &gitx.Plan{Mode: gitx.ModeFreshMain, SrcMain: "/home/alice/repo", SrcWorktree: "/home/alice/repo/.worktrees/feat",
		DstMain: dstMain, DstWorktree: dstWorktree, Linked: true, WorktreeName: "feat", Branch: "feat", Detached: true,
		PackEntryID: gitx.NoEntry, IndexEntryID: gitx.NoEntry}
	p := &Plan{JobID: sid, Git: gp}
	j := &job.Journal{ID: sid}
	r := &runner{p: p, j: j, src: src.ep, dst: dst.ep, selfExe: selfExe(t), logf: t.Logf}

	done, err := r.verifyGitAttach(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("verifyGitAttach reported done for a linked+detached fresh-main worktree before repairLinkedMetadata ever ran (both WorktreeExists and MainExists are true only because install already placed the files)")
	}

	if err := r.runGitAttach(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(staleGitFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "gitdir: " + filepath.Join(dstMain, ".git", "worktrees", "feat") + "\n"
	if string(got) != want {
		t.Errorf(".git file = %q, want %q — repairLinkedMetadata must run and rewrite it to the destination's own paths", got, want)
	}
}

// TestInstallVerifyRequiresMergePhaseNotJustFilePlacement is controller
// ruling R-P3-20b: file placement alone does not mean "installed" (spec
// §7.5) — the sessions-index merge must also have reached the destination.
// This drives a real transfer (through the real preflight/transfer steps,
// two Local endpoints) and then calls dst.Install directly WITHOUT ever
// calling PutInstallExtras first, reproducing "every file landed but the
// merge phase never completed" (e.g. a crash between the two, or — as
// here — between a first partial run and `continue`): remote.Local.Install
// reads jobs/<jobID>/extras.json, which is simply absent, so the merges
// (index entry, project entry, history) are silently skipped by design
// (spec's own "not found" tolerance) while every manifest entry still
// lands PresentSame. Before this ruling, verifyInstall only checked file
// placement and would have reported this done, so `continue` would skip
// install and its merges would never run at all.
func TestInstallVerifyRequiresMergePhaseNotJustFilePlacement(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := filepath.Join(src.paths.Home, "x")
	seedSession(t, src, cwd)
	srcProj := src.paths.ProjectDir(cwd)
	if err := session.MergeIndexEntry(srcProj, session.IndexEntry{SessionID: sid, FullPath: filepath.Join(srcProj, sid+".jsonl"), ProjectPath: cwd}); err != nil {
		t.Fatal(err)
	}
	o := baseOptions()
	o.State = "idle"
	p, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatal(err)
	}
	if p.Extras == nil || p.Extras.IndexEntry == nil {
		t.Fatal("test setup: expected the seeded sessions-index entry to produce Extras.IndexEntry")
	}

	j := &job.Journal{ID: sid}
	r := &runner{p: p, j: j, src: src.ep, dst: dst.ep, selfExe: selfExe(t), logf: t.Logf}
	if err := r.runPreflight(context.Background()); err != nil { // OpenStream needs a journal on both hosts first
		t.Fatal(err)
	}
	if err := r.runTransfer(context.Background()); err != nil {
		t.Fatal(err)
	}
	im, err := r.installManifest()
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately skip PutInstallExtras: places every file, merges nothing.
	if _, err := r.dst.Install(context.Background(), im, r.p.JobID); err != nil {
		t.Fatal(err)
	}

	done, err := r.verifyInstall(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("install.Verify reported done with every file placed but the sessions-index merge never having reached the destination")
	}
}
