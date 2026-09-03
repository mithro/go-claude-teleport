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

// TestFastForwardedEntryNotRecordedAsInstalledUnlessAlreadyOurs is ruling
// R-P3-23h: an FFCandidate entry by definition already existed on the
// destination (here: a stale prefix left by an EARLIER, unrelated
// teleport of this same session) — Uninstall's hash check no longer
// protects it once install has fast-forwarded it to match the manifest,
// so InstalledIDs must never gain it unless the id was already there
// (this job's own earlier partial placement being extended on a retry).
//
// Two runners (the same lower-level pattern as the rest of this file, not
// a subprocess or a full RunJob — deliberately never reaching the "start"
// step, whose pty-resume would append to the destination's transcript and
// so change the very bytes this test compares) drive the same session to
// two different destinations: the first (fresh) is stopped right after
// "transfer" — its STAGED bytes are the real, path-rewritten content this
// session ever produces for this destination pairing, before anything is
// installed. A one-line prefix of those bytes is pre-seeded on the SECOND
// destination — a stale leftover from an EARLIER, unrelated teleport of
// this session — before that job's own preflight/transfer/install run,
// so its manifest-diff classifies the transcript ff-candidate and install
// extends it in place.
func TestFastForwardedEntryNotRecordedAsInstalledUnlessAlreadyOurs(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := filepath.Join(src.paths.Home, "x")
	seedSession(t, src, cwd)
	o := baseOptions()
	o.State = "idle"

	// Pass 1: preflight+transfer only (never install) against the SAME
	// destination the real job below will use, purely to learn the real,
	// path-rewritten bytes this session produces for THIS destination
	// pairing (rewriting embeds the destination's own home path, so
	// bytes captured for any OTHER destination would never match).
	p1, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatal(err)
	}
	r1 := &runner{p: p1, j: &job.Journal{ID: sid}, src: src.ep, dst: dst.ep, selfExe: selfExe(t), logf: t.Logf}
	if err := r1.runPreflight(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r1.runTransfer(context.Background()); err != nil {
		t.Fatal(err)
	}
	m1, err := r1.manifest()
	if err != nil {
		t.Fatal(err)
	}
	transcriptID, transcriptDst := -1, ""
	for _, e := range m1.Entries {
		if e.Category == session.CatSession && strings.HasSuffix(e.Dst, sid+".jsonl") {
			transcriptID, transcriptDst = e.ID, e.Dst
		}
	}
	if transcriptID < 0 {
		t.Fatal("no transcript entry in the manifest")
	}
	staging := job.StagingDir(dst.paths.DataDir, sid)
	full, err := os.ReadFile(transfer.StagedPath(staging, transcriptID))
	if err != nil {
		t.Fatal(err)
	}
	nl := strings.IndexByte(string(full), '\n')
	if nl < 0 {
		t.Fatalf("transcript has no newline to prefix on: %q", full)
	}
	prefix := full[:nl+1]

	// Simulate a stale leftover from an EARLIER, unrelated teleport of
	// this session: the destination already has a PREFIX of the real
	// content, before the job under test (pass 2) has done anything.
	// Pass 1's staged (full, correct) copy is left in place, so pass 2's
	// manifest-diff — even at preflight, before its own transfer runs —
	// sees a verified staged copy to ff-prefix-check the seeded prefix
	// against.
	if err := os.MkdirAll(filepath.Dir(transcriptDst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcriptDst, prefix, 0o600); err != nil {
		t.Fatal(err)
	}

	// Pass 2: the real job under test, fresh Plan/runner, same physical
	// destination.
	p2, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatal(err)
	}
	r2 := &runner{p: p2, j: &job.Journal{ID: sid}, src: src.ep, dst: dst.ep, selfExe: selfExe(t), logf: t.Logf}
	if err := r2.runPreflight(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r2.runTransfer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r2.runInstall(context.Background()); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(transcriptDst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(full) {
		t.Fatalf("transcript was not fast-forwarded to the full content (test setup problem, not the ruling under test): got %d bytes, want %d", len(got), len(full))
	}
	for _, id := range r2.p.InstalledIDs {
		if id == transcriptID {
			t.Fatalf("InstalledIDs = %v: a fast-forwarded entry that pre-existed from an unrelated earlier teleport must never be recorded as installed by THIS job", r2.p.InstalledIDs)
		}
	}
}

// TestFastForwardedEntryStaysInstalledWhenItIsAlreadyOurs is the positive
// half of ruling R-P3-23h: the id filter protects entries this job did NOT
// place, and must not cost it the ones it did. A first install places the
// transcript (recording its id in Plan.InstalledIDs); the destination copy
// is then cut back to a prefix — the shape a crash partway through this
// job's own write leaves behind — so the retry's diff classifies the very
// same entry as an ff-candidate. The retry fast-forwards it in place, and
// because the id was already recorded it must STILL be recorded
// afterwards: abandon has to be able to delete what this job installed.
func TestFastForwardedEntryStaysInstalledWhenItIsAlreadyOurs(t *testing.T) {
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
	r := &runner{p: p, j: &job.Journal{ID: sid}, src: src.ep, dst: dst.ep, selfExe: selfExe(t), logf: t.Logf}
	if err := r.runPreflight(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.runTransfer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.runInstall(context.Background()); err != nil {
		t.Fatal(err)
	}

	m, err := r.manifest()
	if err != nil {
		t.Fatal(err)
	}
	transcriptID, transcriptDst := -1, ""
	for _, e := range m.Entries {
		if e.Category == session.CatSession && strings.HasSuffix(e.Dst, sid+".jsonl") {
			transcriptID, transcriptDst = e.ID, e.Dst
		}
	}
	if transcriptID < 0 {
		t.Fatal("no transcript entry in the manifest")
	}
	if !containsID(r.p.InstalledIDs, transcriptID) {
		t.Fatalf("InstalledIDs = %v: the first install must record the transcript it placed (id %d)", r.p.InstalledIDs, transcriptID)
	}
	full, err := os.ReadFile(transcriptDst)
	if err != nil {
		t.Fatal(err)
	}
	nl := strings.IndexByte(string(full), '\n')
	if nl < 0 {
		t.Fatalf("transcript has no newline to cut back to: %q", full)
	}
	// This job's own placement, half-written: the destination now holds a
	// strict prefix of what it installed a moment ago.
	if err := os.WriteFile(transcriptDst, full[:nl+1], 0o600); err != nil {
		t.Fatal(err)
	}
	// A retry re-runs the transfer step first (install consumes the
	// staged copy), Verify included: that is what re-diffs against
	// reality and persists the statuses the source's send stream is
	// driven from — exactly what job.Run does on a resumed job.
	if done, err := r.verifyTransfer(context.Background()); err != nil {
		t.Fatal(err)
	} else if done {
		t.Fatal("test setup: the retry's transfer verify sees nothing to do")
	}
	if err := r.runTransfer(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err := transfer.Diff(context.Background(), m, job.StagingDir(dst.paths.DataDir, sid))
	if err != nil {
		t.Fatal(err)
	}
	if st[transcriptID] != transfer.FFCandidate {
		t.Fatalf("transcript status = %q, want ff-candidate (test setup problem, not the ruling under test)", st[transcriptID])
	}

	if err := r.runInstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(transcriptDst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(full) {
		t.Fatalf("the retry did not fast-forward the transcript back to the full content: %d bytes, want %d", len(got), len(full))
	}
	if !containsID(r.p.InstalledIDs, transcriptID) {
		t.Errorf("InstalledIDs = %v: fast-forwarding THIS job's own earlier placement (id %d) must keep it recorded", r.p.InstalledIDs, transcriptID)
	}
	if n := countID(r.p.InstalledIDs, transcriptID); n != 1 {
		t.Errorf("InstalledIDs = %v: id %d recorded %d times, want exactly once", r.p.InstalledIDs, transcriptID, n)
	}
}

func containsID(ids []int, want int) bool { return countID(ids, want) > 0 }

func countID(ids []int, want int) int {
	n := 0
	for _, id := range ids {
		if id == want {
			n++
		}
	}
	return n
}

// TestRunInstallPersistsPartialInstalledIDsBeforeFailing is ruling
// R-P3-23j: transfer.Install returns its accumulated InstallReport
// alongside an error at every failure point (it never discards what it
// already placed), so runInstall must fold and persist that partial
// InstalledIDs BEFORE returning the error — otherwise a job that fails
// partway through install would make its already-placed files
// undeletable by abandon even though they really are this job's own.
// The manifest's LAST entry is corrupted (pre-seeded with content that
// does not match, forcing Install's default: case) so every entry before
// it in iteration order is placed successfully first.
func TestRunInstallPersistsPartialInstalledIDsBeforeFailing(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := filepath.Join(src.paths.Home, "x")
	seedSession(t, src, cwd)
	// A plain untracked file (ModeNotRepo copies the whole cwd as plain
	// files): a THIRD manifest entry so the corrupted one below is not
	// the only regular file — at least one other entry must legitimately
	// succeed before Install reaches (and fails on) the corrupted one.
	if err := os.WriteFile(filepath.Join(cwd, "keep.txt"), []byte("kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := baseOptions()
	o.State = "idle"
	p, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatal(err)
	}
	j := &job.Journal{ID: sid}
	r := &runner{p: p, j: j, src: src.ep, dst: dst.ep, selfExe: selfExe(t), logf: t.Logf}
	if err := r.runPreflight(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.runTransfer(context.Background()); err != nil {
		t.Fatal(err)
	}

	m, err := r.manifest()
	if err != nil {
		t.Fatal(err)
	}
	var corrupt *transfer.Entry
	for i := range m.Entries {
		if strings.HasSuffix(m.Entries[i].Dst, "keep.txt") {
			corrupt = &m.Entries[i]
			break
		}
	}
	if corrupt == nil {
		t.Fatal("test setup: no keep.txt entry in the manifest")
	}
	if err := os.MkdirAll(filepath.Dir(corrupt.Dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corrupt.Dst, []byte("pre-existing content that matches nothing"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := r.runInstall(context.Background()); err == nil {
		t.Fatal("expected runInstall to fail on the corrupted entry")
	}
	if len(r.p.InstalledIDs) == 0 {
		t.Fatal("a partially-succeeded install must still fold and persist whatever it DID place, even though it then failed")
	}
	for _, id := range r.p.InstalledIDs {
		if id == corrupt.ID {
			t.Errorf("the corrupted (failed) entry itself must not be in InstalledIDs: %v", r.p.InstalledIDs)
		}
	}
	// The journal (not just the in-memory Plan) must carry the partial
	// InstalledIDs too — persist() marshals r.p into r.j.Plan.
	jp, err := PlanFromJournal(r.j)
	if err != nil {
		t.Fatal(err)
	}
	if len(jp.InstalledIDs) != len(r.p.InstalledIDs) {
		t.Errorf("journal Plan.InstalledIDs = %v, want %v", jp.InstalledIDs, r.p.InstalledIDs)
	}
}
