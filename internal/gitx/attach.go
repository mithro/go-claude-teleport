// internal/gitx/attach.go
package gitx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
)

// DirtyFile is one dirty worktree file (or the index) staged on the
// destination host: Src is the staged copy, Mode the mode it must land
// with. A zero Mode means "keep the staged file's own mode".
type DirtyFile struct {
	Src  string
	Mode fs.FileMode
}

// Attach performs the destination side of spec §8. dirtyFiles maps the
// absolute destination path to the staged copy to install there.
func Attach(ctx context.Context, p *Plan, packPath string, dirtyFiles map[string]DirtyFile) error {
	switch p.Mode {
	case ModeNotRepo:
		return nil
	case ModeFreshMain:
		if !p.Linked {
			return nil
		}
		return repairLinkedMetadata(p)
	case ModeExistingMain:
		return attachExisting(ctx, p, packPath, dirtyFiles)
	}
	return fmt.Errorf("gitx.Attach: unknown mode %q", p.Mode)
}

func worktreeGitDir(p *Plan) string {
	return filepath.Join(p.DstMain, ".git", "worktrees", p.WorktreeName)
}

// indexDestPath is where the transferred index file lands on the
// destination (M/.git/index, or M/.git/worktrees/<n>/index when linked).
func indexDestPath(p *Plan) string {
	return filepath.Join(p.DstMain, filepath.FromSlash(p.IndexRel))
}

// writeIfDifferent writes content to path unless it already holds it.
func writeIfDifferent(path, content string, perm os.FileMode) error {
	if cur, err := os.ReadFile(path); err == nil && string(cur) == content {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), perm)
}

// repairLinkedMetadata is what `git worktree repair` does: the two
// absolute-path files are rewritten for the destination paths.
func repairLinkedMetadata(p *Plan) error {
	gd := worktreeGitDir(p)
	if err := writeIfDifferent(filepath.Join(p.DstWorktree, ".git"), "gitdir: "+gd+"\n", 0o644); err != nil {
		return fmt.Errorf("write %s/.git: %w", p.DstWorktree, err)
	}
	if err := writeIfDifferent(filepath.Join(gd, "gitdir"), filepath.Join(p.DstWorktree, ".git")+"\n", 0o644); err != nil {
		return fmt.Errorf("write %s/gitdir: %w", gd, err)
	}
	return nil
}

// checkDirtyContainment refuses a dirty map that names anything other than
// the transferred index or a path under the destination worktree, before
// any of it is copied: a hostile or corrupt plan must not be able to write
// outside the directories the transfer owns.
func checkDirtyContainment(p *Plan, dirtyFiles map[string]DirtyFile) error {
	idx := indexDestPath(p)
	prefix := filepath.Clean(p.DstWorktree) + string(filepath.Separator)
	dsts := make([]string, 0, len(dirtyFiles))
	for dst := range dirtyFiles {
		dsts = append(dsts, dst)
	}
	sort.Strings(dsts)
	for _, dst := range dsts {
		c := filepath.Clean(dst)
		if c == idx || strings.HasPrefix(c, prefix) {
			continue
		}
		return &RefuseError{Reason: fmt.Sprintf("dirty file %s is outside the destination worktree %s", dst, p.DstWorktree)}
	}
	return nil
}

func attachExisting(ctx context.Context, p *Plan, packPath string, dirtyFiles map[string]DirtyFile) error {
	if err := checkDirtyContainment(p, dirtyFiles); err != nil {
		return err
	}
	repo, err := git.PlainOpenWithOptions(p.DstMain, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return fmt.Errorf("open %s: %w", p.DstMain, err)
	}
	// Snapshot the W==M checkout state before ensureBranch below advances
	// refs/heads/<Branch> to Tip (Reset's setHEADCommit needs an existing
	// ref to update, so that has to happen first). Deriving "already at
	// tip"/clean/branch from disk after that mutation would either treat an
	// untouched destination as already fast-forwarded (ref alone matches
	// Tip) or a genuinely clean one as dirty (index still at the old tree
	// looks like a staged deletion against the moved ref).
	var ffState *fastForwardState
	if p.Linked {
		// Preflight's DestState can be stale by now; re-check before the
		// pack is indexed and before anything is written.
		if err := checkLinkedDestination(p); err != nil {
			return err
		}
	}
	if !p.Linked {
		ffState, err = snapshotFastForwardState(p)
		if err != nil {
			return err
		}
		// Refusal must be atomic: decide before the pack is indexed and
		// before ensureBranch moves refs/heads/<Branch>.
		if err := checkFastForwardState(p, ffState); err != nil {
			return err
		}
	}
	if packPath != "" {
		if err := indexPack(repo, packPath); err != nil {
			return err
		}
	}
	tip := plumbing.NewHash(p.Tip)
	if err := repo.Storer.HasEncodedObject(tip); err != nil {
		return fmt.Errorf("tip %s not present in %s after pack: %w", short(p.Tip), p.DstMain, err)
	}
	// The transferred index names these blobs; installing it while they are
	// absent would leave the destination repository corrupt.
	for _, h := range p.StagedBlobs {
		err := repo.Storer.HasEncodedObject(plumbing.NewHash(h))
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			return &RefuseError{Reason: fmt.Sprintf("staged blob %s the index references is not present in %s after the pack", short(h), p.DstMain)}
		}
		if err != nil {
			return fmt.Errorf("check staged blob %s in %s: %w", short(h), p.DstMain, err)
		}
	}
	if !p.Detached {
		if err := ensureBranch(repo, p, tip); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var gitdir string
	if p.Linked {
		gitdir = worktreeGitDir(p)
		if err := createLinkedWorktree(p, tip); err != nil {
			return err
		}
	} else {
		gitdir = filepath.Join(p.DstMain, ".git")
		if err := fastForwardMainCheckout(p, tip, ffState); err != nil {
			return err
		}
	}
	indexDst := indexDestPath(p)
	if df, ok := dirtyFiles[indexDst]; ok {
		if err := copyFile(df, filepath.Join(gitdir, "index")); err != nil {
			return fmt.Errorf("apply index: %w", err)
		}
	}
	for dst, df := range dirtyFiles {
		if dst == indexDst {
			continue
		}
		if err := copyFile(df, dst); err != nil {
			return fmt.Errorf("apply dirty file %s: %w", dst, err)
		}
	}
	return nil
}

func indexPack(repo *git.Repository, packPath string) error {
	st, err := os.Stat(packPath)
	if err != nil {
		return err
	}
	if st.Size() == 0 {
		return nil // WritePack found nothing missing
	}
	f, err := os.Open(packPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := packfile.UpdateObjectStorage(repo.Storer, f); err != nil {
		return fmt.Errorf("index pack %s: %w", packPath, err)
	}
	return nil
}

func ensureBranch(repo *git.Repository, p *Plan, tip plumbing.Hash) error {
	name := plumbing.NewBranchReferenceName(p.Branch)
	cur, err := repo.Reference(name, false)
	switch {
	case errors.Is(err, plumbing.ErrReferenceNotFound):
		return repo.Storer.SetReference(plumbing.NewHashReference(name, tip))
	case err != nil:
		return fmt.Errorf("ref %s: %w", name, err)
	case cur.Hash() == tip:
		return nil
	}
	if !p.FastForward {
		return &RefuseError{Reason: fmt.Sprintf("branch %s moved on the destination since preflight (%s), not a fast-forward", p.Branch, short(cur.Hash().String()))}
	}
	ok, err := IsAncestor(p.DstMain, cur.Hash().String(), p.Tip)
	if err != nil {
		return err
	}
	if !ok {
		return &RefuseError{Reason: fmt.Sprintf("branch %s on the destination (%s) is no longer an ancestor of %s", p.Branch, short(cur.Hash().String()), short(p.Tip))}
	}
	return repo.Storer.CheckAndSetReference(plumbing.NewHashReference(name, tip), cur)
}

// linkedRerun recognises the signature attachExisting itself leaves behind
// for a linked worktree: the metadata index plus W/.git. It is the one
// reason a destination worktree may already exist.
func linkedRerun(p *Plan) bool {
	if _, err := os.Stat(filepath.Join(worktreeGitDir(p), "index")); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(p.DstWorktree, ".git"))
	return err == nil
}

// checkLinkedDestination re-reads the destination just before mutating it:
// a worktree directory or a checkout of the session branch that appeared
// since preflight is refused, exactly as PlanTransfer would have.
func checkLinkedDestination(p *Plan) error {
	ds, err := DestStateOf(p.DstMain, p.DstWorktree, p.Branch)
	if err != nil {
		return err
	}
	if ds.WorktreeExists && !linkedRerun(p) {
		return &RefuseError{Reason: fmt.Sprintf("destination worktree directory %s already exists", p.DstWorktree)}
	}
	if ds.BranchCheckedOutElsewhere != "" {
		return &RefuseError{Reason: fmt.Sprintf("branch %s is already checked out on the destination at %s", p.Branch, ds.BranchCheckedOutElsewhere)}
	}
	return nil
}

// createLinkedWorktree writes the metadata git keeps for a linked worktree
// and populates the working tree from tip. A second call (re-run) finds the
// index already present and leaves the tree alone.
func createLinkedWorktree(p *Plan, tip plumbing.Hash) error {
	gd := worktreeGitDir(p)
	if linkedRerun(p) {
		return nil
	}
	if err := os.MkdirAll(gd, 0o755); err != nil {
		return err
	}
	head := tip.String() + "\n"
	if !p.Detached {
		head = "ref: refs/heads/" + p.Branch + "\n"
	}
	if err := writeIfDifferent(filepath.Join(gd, "HEAD"), head, 0o644); err != nil {
		return err
	}
	if err := writeIfDifferent(filepath.Join(gd, "commondir"), "../..\n", 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(p.DstWorktree, 0o755); err != nil {
		return err
	}
	if err := repairLinkedMetadata(p); err != nil {
		return err
	}
	wrepo, err := git.PlainOpenWithOptions(p.DstWorktree, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return fmt.Errorf("open new worktree %s: %w", p.DstWorktree, err)
	}
	wt, err := wrepo.Worktree()
	if err != nil {
		return err
	}
	if err := wt.Reset(&git.ResetOptions{Mode: git.HardReset, Commit: tip}); err != nil {
		return fmt.Errorf("populate %s at %s: %w", p.DstWorktree, short(p.Tip), err)
	}
	return nil
}

// fastForwardState is the W == M checkout state as it stood before
// attachExisting mutated anything (see the snapshot comment above its call).
type fastForwardState struct {
	alreadyAtTip bool
	clean        bool
	branch       string
}

func snapshotFastForwardState(p *Plan) (*fastForwardState, error) {
	cur, err := Inspect(p.DstMain)
	if err != nil {
		return nil, err
	}
	ds, err := DestStateOf(p.DstMain, p.DstMain, p.Branch)
	if err != nil {
		return nil, err
	}
	return &fastForwardState{alreadyAtTip: cur.Head == p.Tip, clean: ds.Clean, branch: ds.WorktreeBranch}, nil
}

// checkFastForwardState is the W == M gate: the destination checkout must
// still be clean and on the session branch. It is evaluated before
// attachExisting mutates anything.
func checkFastForwardState(p *Plan, st *fastForwardState) error {
	if st.branch != p.Branch {
		if st.branch == "" {
			return &RefuseError{Reason: fmt.Sprintf("destination checkout %s has no branch checked out, session branch is %q", p.DstMain, p.Branch)}
		}
		return &RefuseError{Reason: fmt.Sprintf("destination checkout %s is on %q, not %q", p.DstMain, st.branch, p.Branch)}
	}
	if !st.clean {
		return &RefuseError{Reason: fmt.Sprintf("destination checkout %s is not clean", p.DstMain)}
	}
	return nil
}

// fastForwardMainCheckout handles W == M: HEAD moves to tip. The gate
// above has already run.
func fastForwardMainCheckout(p *Plan, tip plumbing.Hash, st *fastForwardState) error {
	if st.alreadyAtTip {
		return nil // already there (re-run after the dirty state was applied)
	}
	repo, err := git.PlainOpenWithOptions(p.DstMain, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return err
	}
	if err := wt.Reset(&git.ResetOptions{Mode: git.HardReset, Commit: tip}); err != nil {
		return fmt.Errorf("fast-forward %s to %s: %w", p.DstMain, short(p.Tip), err)
	}
	return nil
}

// copyFile installs df.Src at dst with df.Mode (the staged file's own mode
// when df.Mode is zero).
func copyFile(df DirtyFile, dst string) error {
	src := df.Src
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	mode := df.Mode.Perm()
	if df.Mode == 0 {
		mode = st.Mode().Perm()
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".claude-teleport.tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
