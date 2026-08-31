// internal/gitx/attach_test.go
package gitx

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// copyTree copies src to dst recursively (tests only) to simulate the tar
// transfer of a fresh-main teleport.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			l, _ := os.Readlink(p)
			return os.Symlink(l, target)
		default:
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, b, info.Mode().Perm())
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAttachFreshMainRepairsWorktreeMetadata(t *testing.T) {
	srcHome := filepath.Join(t.TempDir(), "home", "alice")
	dstHome := filepath.Join(t.TempDir(), "home", "bob")
	srcMain := filepath.Join(srcHome, "x")
	initRepo(t, srcMain)
	w := addWorktree(t, srcMain, "feat")
	writeFile(t, filepath.Join(w, "staged.txt"), "s")
	gitCLI(t, w, "add", "staged.txt")
	writeFile(t, filepath.Join(w, "untracked.txt"), "u")

	dstMain := filepath.Join(dstHome, "x")
	copyTree(t, srcMain, dstMain) // as the tar transfer would (metadata still points at srcHome)

	info, _ := Inspect(w)
	pm := pathMap(srcHome, dstHome)
	p, err := PlanTransfer(info, &DestState{}, pm)
	if err != nil {
		t.Fatal(err)
	}
	if err := Attach(context.Background(), p, "", nil); err != nil {
		t.Fatal(err)
	}
	dstW := filepath.Join(dstMain, ".worktrees", "feat")
	gotDotGit, _ := os.ReadFile(filepath.Join(dstW, ".git"))
	if want := "gitdir: " + filepath.Join(dstMain, ".git", "worktrees", "feat") + "\n"; string(gotDotGit) != want {
		t.Errorf("W/.git = %q, want %q", gotDotGit, want)
	}
	gotGitdir, _ := os.ReadFile(filepath.Join(dstMain, ".git", "worktrees", "feat", "gitdir"))
	if want := filepath.Join(dstW, ".git") + "\n"; string(gotGitdir) != want {
		t.Errorf("gitdir = %q, want %q", gotGitdir, want)
	}
	// The real git agrees: worktree list shows the destination path, and
	// status is identical to the source's.
	list := gitCLI(t, dstMain, "worktree", "list", "--porcelain")
	if !strings.Contains(list, "worktree "+dstW+"\n") {
		t.Errorf("git worktree list:\n%s", list)
	}
	if diff := cmp.Diff(porcelain(t, w), porcelain(t, dstW)); diff != "" {
		t.Errorf("status differs (-src +dst):\n%s", diff)
	}
	// Idempotent.
	if err := Attach(context.Background(), p, "", nil); err != nil {
		t.Fatal(err)
	}
}

func TestAttachExistingMainCreatesWorktreeAndAppliesDirty(t *testing.T) {
	srcMain := t.TempDir()
	repo, root := initRepo(t, srcMain)
	w := addWorktree(t, srcMain, "feat")
	writeFile(t, filepath.Join(w, "b.txt"), "b\n")
	gitCLI(t, w, "add", "b.txt")
	gitCLI(t, w, "commit", "-q", "-m", "feat work")
	writeFile(t, filepath.Join(w, "README.md"), "modified\n")
	writeFile(t, filepath.Join(w, "new.txt"), "untracked\n")
	writeFile(t, filepath.Join(w, "staged.txt"), "staged\n")
	gitCLI(t, w, "add", "staged.txt")
	_ = repo

	dstMain := filepath.Join(t.TempDir(), "x")
	initRepoAt(t, dstMain, srcMain, root)

	info, _ := Inspect(w)
	ds, err := DestStateOf(dstMain, filepath.Join(dstMain, ".worktrees", "feat"), "feat")
	if err != nil {
		t.Fatal(err)
	}
	pm := pathMap(srcMain, dstMain)
	p, err := PlanTransfer(info, ds, pm)
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != ModeExistingMain || !p.NeedPack {
		t.Fatalf("plan = %+v", p)
	}
	sb, err := StagedBlobsOf(srcMain, filepath.Join(srcMain, p.IndexRel), p.Tip)
	if err != nil {
		t.Fatal(err)
	}
	p.SetStagedBlobs(sb)
	var pack bytes.Buffer
	if err := WritePack(context.Background(), srcMain, append([]string{p.Tip}, p.StagedBlobs...), p.HaveTips, &pack); err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	packPath := filepath.Join(staging, "objects.pack")
	if err := os.WriteFile(packPath, pack.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty := map[string]DirtyFile{}
	for i, rel := range []string{"README.md", "new.txt", "staged.txt"} {
		staged := filepath.Join(staging, string(rune('a'+i)))
		b, _ := os.ReadFile(filepath.Join(w, rel))
		if err := os.WriteFile(staged, b, 0o600); err != nil {
			t.Fatal(err)
		}
		dirty[filepath.Join(p.DstWorktree, rel)] = DirtyFile{Src: staged, Mode: 0o644}
	}
	idxStaged := filepath.Join(staging, "index")
	b, _ := os.ReadFile(filepath.Join(srcMain, p.IndexRel))
	if err := os.WriteFile(idxStaged, b, 0o644); err != nil {
		t.Fatal(err)
	}
	dirty[filepath.Join(p.DstMain, p.IndexRel)] = DirtyFile{Src: idxStaged}

	if err := Attach(context.Background(), p, packPath, dirty); err != nil {
		t.Fatal(err)
	}
	dstW := p.DstWorktree
	if diff := cmp.Diff(porcelain(t, w), porcelain(t, dstW)); diff != "" {
		t.Errorf("status differs (-src +dst):\n%s", diff)
	}
	if got := strings.TrimSpace(gitCLI(t, dstW, "rev-parse", "HEAD")); got != p.Tip {
		t.Errorf("dest HEAD = %s, want %s", got, p.Tip)
	}
	if got := strings.TrimSpace(gitCLI(t, dstW, "rev-parse", "--abbrev-ref", "HEAD")); got != "feat" {
		t.Errorf("dest branch = %s", got)
	}
	if got := gitCLI(t, dstW, "diff", "--cached", "--name-only"); strings.TrimSpace(got) != "staged.txt" {
		t.Errorf("staged diff = %q", got)
	}
	// The recorded DirtyFile.Mode wins over the staged copy's own mode.
	for _, rel := range []string{"README.md", "new.txt", "staged.txt"} {
		st, err := os.Stat(filepath.Join(dstW, rel))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o644 {
			t.Errorf("%s mode = %o, want 644 (DirtyFile.Mode)", rel, st.Mode().Perm())
		}
	}
	gitCLI(t, dstMain, "fsck", "--no-dangling")
	// Re-running attach after success is a no-op that does not clobber the dirty state.
	if err := Attach(context.Background(), p, packPath, dirty); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(porcelain(t, w), porcelain(t, dstW)); diff != "" {
		t.Errorf("status after re-run (-src +dst):\n%s", diff)
	}
}

func TestAttachSameDirFastForward(t *testing.T) {
	srcMain := t.TempDir()
	repo, root := initRepo(t, srcMain)
	writeFile(t, filepath.Join(srcMain, "b.txt"), "b\n")
	second := commitAll(t, repo, "second")
	writeFile(t, filepath.Join(srcMain, "new.txt"), "untracked\n")

	dstMain := filepath.Join(t.TempDir(), "x")
	initRepoAt(t, dstMain, srcMain, root)

	info, _ := Inspect(srcMain)
	ds, _ := DestStateOf(dstMain, dstMain, "main")
	ds.BranchTipReachable, _ = IsAncestor(srcMain, ds.BranchTip, info.Head)
	p, err := PlanTransfer(info, ds, pathMap(srcMain, dstMain))
	if err != nil {
		t.Fatal(err)
	}
	if !p.FastForward {
		t.Fatalf("expected fast-forward plan, got %+v", p)
	}
	var pack bytes.Buffer
	if err := WritePack(context.Background(), srcMain, []string{p.Tip}, p.HaveTips, &pack); err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	packPath := filepath.Join(staging, "objects.pack")
	os.WriteFile(packPath, pack.Bytes(), 0o600)
	newStaged := filepath.Join(staging, "n")
	os.WriteFile(newStaged, []byte("untracked\n"), 0o644)
	idxStaged := filepath.Join(staging, "index")
	b, _ := os.ReadFile(filepath.Join(srcMain, ".git", "index"))
	os.WriteFile(idxStaged, b, 0o644)
	dirty := map[string]DirtyFile{
		filepath.Join(dstMain, "new.txt"):       {Src: newStaged},
		filepath.Join(dstMain, ".git", "index"): {Src: idxStaged},
	}
	if err := Attach(context.Background(), p, packPath, dirty); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(gitCLI(t, dstMain, "rev-parse", "HEAD")); got != second {
		t.Errorf("HEAD = %s, want %s", got, second)
	}
	if diff := cmp.Diff(porcelain(t, srcMain), porcelain(t, dstMain)); diff != "" {
		t.Errorf("status differs (-src +dst):\n%s", diff)
	}
}

// TestAttachSameDirRefusesWhenDirtiedSincePreflight uses a destination that
// is BEHIND the tip, so a fast-forward would move refs/heads/main. A
// refusal must be atomic: the gates are evaluated before the pack is
// indexed and before the branch ref is touched, so nothing moved.
func TestAttachSameDirRefusesWhenDirtiedSincePreflight(t *testing.T) {
	srcMain := t.TempDir()
	repo, root := initRepo(t, srcMain)
	writeFile(t, filepath.Join(srcMain, "b.txt"), "b\n")
	second := commitAll(t, repo, "second")
	dstMain := filepath.Join(t.TempDir(), "x")
	initRepoAt(t, dstMain, srcMain, root)
	info, _ := Inspect(srcMain)
	ds, _ := DestStateOf(dstMain, dstMain, "main")
	ds.BranchTipReachable, _ = IsAncestor(srcMain, ds.BranchTip, info.Head)
	p, err := PlanTransfer(info, ds, pathMap(srcMain, dstMain))
	if err != nil {
		t.Fatal(err)
	}
	if !p.FastForward || p.Tip != second {
		t.Fatalf("fixture wants a fast-forward plan to %s, got %+v", second[:7], p)
	}
	var pack bytes.Buffer
	if err := WritePack(context.Background(), srcMain, []string{p.Tip}, p.HaveTips, &pack); err != nil {
		t.Fatal(err)
	}
	packPath := filepath.Join(t.TempDir(), "objects.pack")
	if err := os.WriteFile(packPath, pack.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(dstMain, "sneaky.txt"), "x") // dirtied after preflight
	err = Attach(context.Background(), p, packPath, nil)
	if err == nil || !strings.Contains(err.Error(), "not clean") {
		t.Fatalf("err = %v, want not-clean refusal", err)
	}
	if got := strings.TrimSpace(gitCLI(t, dstMain, "rev-parse", "refs/heads/main")); got != root {
		t.Errorf("refs/heads/main moved to %s on a refused attach, want %s", got[:7], root[:7])
	}
	if got := strings.TrimSpace(gitCLI(t, dstMain, "rev-parse", "HEAD")); got != root {
		t.Errorf("HEAD moved to %s on a refused attach, want %s", got[:7], root[:7])
	}
}

// TestAttachRefusesDirtyPathOutsideWorktree is the containment invariant:
// a plan whose dirty map names a path outside the destination worktree
// (and that is not the index) is refused before anything is written.
func TestAttachRefusesDirtyPathOutsideWorktree(t *testing.T) {
	srcMain := t.TempDir()
	repo, root := initRepo(t, srcMain)
	w := addWorktree(t, srcMain, "feat")
	writeFile(t, filepath.Join(w, "b.txt"), "b\n")
	gitCLI(t, w, "add", "b.txt")
	gitCLI(t, w, "commit", "-q", "-m", "feat work")
	_ = repo

	dstMain := filepath.Join(t.TempDir(), "x")
	initRepoAt(t, dstMain, srcMain, root)

	info, _ := Inspect(w)
	ds, err := DestStateOf(dstMain, filepath.Join(dstMain, ".worktrees", "feat"), "feat")
	if err != nil {
		t.Fatal(err)
	}
	p, err := PlanTransfer(info, ds, pathMap(srcMain, dstMain))
	if err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	payload := filepath.Join(staging, "payload")
	writeFile(t, payload, "pwned\n")
	outside := filepath.Join(filepath.Dir(dstMain), "escape.txt")
	dirty := map[string]DirtyFile{outside: {Src: payload, Mode: 0o644}}

	var re *RefuseError
	err = Attach(context.Background(), p, "", dirty)
	if !errors.As(err, &re) || !strings.Contains(re.Reason, "escape.txt") {
		t.Fatalf("err = %v, want a *RefuseError naming escape.txt", err)
	}
	if _, err := os.Lstat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s was written despite the refusal (%v)", outside, err)
	}
	// Nothing else was mutated either: the worktree was never created.
	if _, err := os.Lstat(p.DstWorktree); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("destination worktree %s created despite the refusal (%v)", p.DstWorktree, err)
	}
}

// TestAttachVerifiesStagedBlobs covers the "index references objects that
// were never sent" hole: when the destination branch already sits at Tip
// the plan needs no pack, yet the transferred index still names the staged
// blobs. SetStagedBlobs forces the pack; an attach handed a plan whose
// staged blobs are absent must refuse rather than install a corrupt index.
func TestAttachVerifiesStagedBlobs(t *testing.T) {
	srcMain := t.TempDir()
	_, root := initRepo(t, srcMain)
	w := addWorktree(t, srcMain, "feat")
	writeFile(t, filepath.Join(w, "b.txt"), "b\n")
	gitCLI(t, w, "add", "b.txt")
	gitCLI(t, w, "commit", "-q", "-m", "feat work")
	featTip := strings.TrimSpace(gitCLI(t, w, "rev-parse", "HEAD"))
	// Staged but never committed: its blob lives only in the index.
	writeFile(t, filepath.Join(w, "staged.txt"), "staged only\n")
	gitCLI(t, w, "add", "staged.txt")

	info, err := Inspect(w)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := StagedBlobsOf(srcMain, filepath.Join(srcMain, indexRelOf(info)), info.Head)
	if err != nil {
		t.Fatal(err)
	}
	if len(blobs) != 1 {
		t.Fatalf("StagedBlobs = %v, want exactly the staged blob", blobs)
	}

	// newPlan builds a fresh destination that already has refs/heads/feat
	// at Tip, so PlanTransfer decides NeedPack = false.
	newPlan := func(t *testing.T) *Plan {
		t.Helper()
		dstMain := filepath.Join(t.TempDir(), "x")
		initRepoAt(t, dstMain, srcMain, root)
		gitCLI(t, dstMain, "branch", "feat", featTip)
		ds, err := DestStateOf(dstMain, filepath.Join(dstMain, ".worktrees", "feat"), "feat")
		if err != nil {
			t.Fatal(err)
		}
		p, err := PlanTransfer(info, ds, pathMap(srcMain, dstMain))
		if err != nil {
			t.Fatal(err)
		}
		if p.NeedPack {
			t.Fatalf("fixture wants a dest already at tip (NeedPack false), got %+v", p)
		}
		return p
	}

	t.Run("unsent staged blob is refused", func(t *testing.T) {
		p := newPlan(t)
		p.StagedBlobs = blobs // the old, unguarded assignment: no pack follows
		var re *RefuseError
		err := Attach(context.Background(), p, "", nil)
		if !errors.As(err, &re) || !strings.Contains(re.Reason, blobs[0][:7]) {
			t.Fatalf("err = %v, want a *RefuseError naming %s", err, blobs[0][:7])
		}
		if _, err := os.Lstat(p.DstWorktree); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("worktree created despite the refusal (%v)", err)
		}
	})

	t.Run("SetStagedBlobs forces the pack that carries them", func(t *testing.T) {
		p := newPlan(t)
		p.SetStagedBlobs(blobs)
		if !p.NeedPack {
			t.Fatal("SetStagedBlobs must force NeedPack")
		}
		var pack bytes.Buffer
		if err := WritePack(context.Background(), srcMain, append([]string{p.Tip}, p.StagedBlobs...), p.HaveTips, &pack); err != nil {
			t.Fatal(err)
		}
		if pack.Len() == 0 {
			t.Fatal("pack for the staged blob must not be empty")
		}
		packPath := filepath.Join(t.TempDir(), "objects.pack")
		if err := os.WriteFile(packPath, pack.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Attach(context.Background(), p, packPath, nil); err != nil {
			t.Fatal(err)
		}
		gitCLI(t, p.DstMain, "cat-file", "-e", blobs[0])
	})
}

// indexRelOf is the index path a plan would use for info (linked or not).
func indexRelOf(info *Info) string {
	if info.IsLinked {
		return filepath.Join(".git", "worktrees", info.WorktreeName, "index")
	}
	return filepath.Join(".git", "index")
}
