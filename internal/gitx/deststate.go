package gitx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// DestState crosses the protocol (git-dest-state). The json tags reproduce
// Go-name marshaling EXACTLY (field F -> "F"), so they changed no wire
// byte; they pin the names against a future rename.
type DestState struct {
	MainExists                bool              `json:"MainExists"`
	RootCommit                string            `json:"RootCommit"`
	RefTips                   map[string]string `json:"RefTips"`   // refs/heads/x -> hex
	BranchTip                 string            `json:"BranchTip"` // "" if absent
	WorktreeExists            bool              `json:"WorktreeExists"`
	WorktreeBranch            string            `json:"WorktreeBranch"`            // branch checked out at worktreeDir if it exists
	WorktreeDetached          bool              `json:"WorktreeDetached"`          // worktreeDir is a checkout with a detached HEAD
	Clean                     bool              `json:"Clean"`                     // for W==M case
	BranchCheckedOutElsewhere string            `json:"BranchCheckedOutElsewhere"` // path, if the branch is checked out in another worktree
	// BranchTipReachable is NOT computed by DestStateOf: the orchestrator
	// sets it on the source (IsAncestor(srcMain, BranchTip, Tip)) before
	// PlanTransfer, which needs it for the fast-forward decision.
	BranchTipReachable bool `json:"BranchTipReachable"`
}

// DestStateOf describes what the destination already has (spec §8).
// mainDir/worktreeDir are destination paths; branch the session branch.
func DestStateOf(mainDir, worktreeDir, branch string) (*DestState, error) {
	st := &DestState{RefTips: map[string]string{}}
	if _, err := os.Stat(filepath.Join(mainDir, ".git")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			exists, err := worktreeDirExists(worktreeDir)
			if err != nil {
				return nil, err
			}
			st.WorktreeExists = exists
			return st, nil
		}
		return nil, err
	}
	st.MainExists = true
	repo, err := git.PlainOpenWithOptions(mainDir, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", mainDir, err)
	}
	if head, err := repo.Head(); err == nil {
		rc, err := firstParentRoot(repo, head.Hash())
		if err != nil {
			return nil, err
		}
		st.RootCommit = rc
	} else if !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, fmt.Errorf("HEAD of %s: %w", mainDir, err)
	}
	refs, err := repo.References()
	if err != nil {
		return nil, err
	}
	err = refs.ForEach(func(r *plumbing.Reference) error {
		if r.Name().IsBranch() && r.Type() == plumbing.HashReference {
			st.RefTips[r.Name().String()] = r.Hash().String()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if branch != "" {
		st.BranchTip = st.RefTips["refs/heads/"+branch]
	}

	exists, err := worktreeDirExists(worktreeDir)
	if err != nil {
		return nil, err
	}
	if exists {
		st.WorktreeExists = true
		// A directory that is not a repository, or is one whose HEAD is
		// still unborn, leaves WorktreeBranch empty for PlanTransfer to
		// refuse on; anything else is a real failure.
		if wi, err := Inspect(worktreeDir); err != nil {
			if !errors.Is(err, ErrNotRepo) && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, plumbing.ErrReferenceNotFound) {
				return nil, fmt.Errorf("inspect %s: %w", worktreeDir, err)
			}
		} else if wi.Root == filepath.Clean(worktreeDir) {
			st.WorktreeBranch = wi.Branch
			st.WorktreeDetached = wi.Detached
			if wi.Root == wi.MainDir {
				st.Clean = len(wi.Dirty.Staged)+len(wi.Dirty.Modified)+len(wi.Dirty.Untracked)+len(wi.Dirty.Deleted) == 0
			}
		}
	}

	// Where is the branch checked out? The main checkout and every linked
	// worktree other than worktreeDir itself count.
	if branch != "" {
		want := "ref: refs/heads/" + branch
		commonDir := filepath.Join(mainDir, ".git")
		if filepath.Clean(worktreeDir) != filepath.Clean(mainDir) {
			h, err := os.ReadFile(filepath.Join(commonDir, "HEAD"))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("read %s HEAD: %w", mainDir, err)
			}
			if err == nil && strings.TrimSpace(string(h)) == want {
				st.BranchCheckedOutElsewhere = filepath.Clean(mainDir)
			}
		}
		others, err := linkedWorktrees(commonDir)
		if err != nil {
			return nil, err
		}
		for name, path := range others {
			if filepath.Clean(path) == filepath.Clean(worktreeDir) {
				continue
			}
			h, err := os.ReadFile(filepath.Join(commonDir, "worktrees", name, "HEAD"))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("read worktree %s HEAD: %w", name, err)
			}
			if err == nil && strings.TrimSpace(string(h)) == want {
				st.BranchCheckedOutElsewhere = path
			}
		}
	}
	return st, nil
}

// worktreeDirExists reports whether worktreeDir is present. Only ENOENT
// counts as absent: a stat that fails for any other reason (an unreadable
// parent, say) must not be mistaken for a free destination.
func worktreeDirExists(worktreeDir string) (bool, error) {
	if _, err := os.Lstat(worktreeDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", worktreeDir, err)
	}
	return true, nil
}

// IsAncestor reports whether ancestor is reachable from descendant in the
// repository at repoDir. Both hashes must exist there.
func IsAncestor(repoDir, ancestor, descendant string) (bool, error) {
	repo, err := git.PlainOpenWithOptions(repoDir, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return false, fmt.Errorf("open %s: %w", repoDir, err)
	}
	a, err := repo.CommitObject(plumbing.NewHash(ancestor))
	if err != nil {
		return false, fmt.Errorf("commit %s: %w", ancestor, err)
	}
	d, err := repo.CommitObject(plumbing.NewHash(descendant))
	if err != nil {
		return false, fmt.Errorf("commit %s: %w", descendant, err)
	}
	return a.IsAncestor(d)
}
