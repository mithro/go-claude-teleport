// Package gitx inspects git repositories and worktrees with go-git and
// performs the destination-side attach (spec §8). It never execs git.
package gitx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// Dirty is nested in Info and Plan, so it crosses the protocol too; tags
// pin Go-name marshaling exactly (no wire change), as on Info and Plan.
type Dirty struct {
	Staged    []string `json:"Staged"`
	Modified  []string `json:"Modified"`
	Untracked []string `json:"Untracked"`
	Deleted   []string `json:"Deleted"`
}

// Info crosses the protocol (inventory-git). The json tags reproduce
// Go-name marshaling EXACTLY (field F -> "F"), so they changed no wire
// byte; they pin the names against a future rename.
type Info struct {
	Root            string   `json:"Root"`      // worktree root (W)
	CommonDir       string   `json:"CommonDir"` // M/.git
	MainDir         string   `json:"MainDir"`   // M
	IsLinked        bool     `json:"IsLinked"`
	WorktreeName    string   `json:"WorktreeName"` // basename under .git/worktrees
	Branch          string   `json:"Branch"`       // "" if detached
	Head            string   `json:"Head"`         // hex
	Detached        bool     `json:"Detached"`
	RootCommit      string   `json:"RootCommit"`
	Dirty           Dirty    `json:"Dirty"`
	Submodules      []string `json:"Submodules"`
	DirtySubmodules []string `json:"DirtySubmodules"` // subset of Submodules whose checkout is not clean
	OtherWorktrees  []string `json:"OtherWorktrees"`  // absolute paths of other linked worktrees
}

var ErrNotRepo = errors.New("not a git repository")

// findRoot walks up from cwd to the nearest directory containing ".git"
// (a directory for a main checkout, a file for a linked worktree).
func findRoot(cwd string) (root, dotGit string, err error) {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return "", "", err
	}
	for {
		p := filepath.Join(dir, ".git")
		if _, err := os.Lstat(p); err == nil {
			return dir, p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", fmt.Errorf("%s: %w", cwd, ErrNotRepo)
		}
		dir = parent
	}
}

// readGitdirFile parses a ".git" FILE ("gitdir: <path>") and returns the
// absolute path it points at.
func readGitdirFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir:") {
		return "", fmt.Errorf("%s: not a gitdir file: %q", path, line)
	}
	gd := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(filepath.Dir(path), gd)
	}
	return filepath.Clean(gd), nil
}

// Inspect describes the repository containing cwd (spec §8 inventory).
func Inspect(cwd string) (*Info, error) {
	root, dotGit, err := findRoot(cwd)
	if err != nil {
		return nil, err
	}
	info := &Info{Root: root}
	st, err := os.Lstat(dotGit)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		info.CommonDir = dotGit
		info.MainDir = root
	} else {
		gd, err := readGitdirFile(dotGit)
		if err != nil {
			return nil, err
		}
		cd, err := os.ReadFile(filepath.Join(gd, "commondir"))
		if err != nil {
			return nil, fmt.Errorf("read commondir of %s: %w", gd, err)
		}
		common := strings.TrimSpace(string(cd))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gd, common)
		}
		info.CommonDir = filepath.Clean(common)
		info.MainDir = filepath.Dir(info.CommonDir)
		info.IsLinked = true
		info.WorktreeName = filepath.Base(gd)
	}

	repo, err := git.PlainOpenWithOptions(root, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", root, err)
	}
	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("HEAD of %s: %w", root, err)
	}
	info.Head = head.Hash().String()
	if head.Name().IsBranch() {
		info.Branch = head.Name().Short()
	} else {
		info.Detached = true
	}
	rootCommit, err := firstParentRoot(repo, head.Hash())
	if err != nil {
		return nil, err
	}
	info.RootCommit = rootCommit

	wt, err := repo.Worktree()
	if err != nil {
		return nil, err
	}
	status, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("status of %s: %w", root, err)
	}
	info.Dirty = dirtyFromStatus(status)

	subs, err := wt.Submodules()
	if err != nil {
		return nil, fmt.Errorf("submodules of %s: %w", root, err)
	}
	for _, s := range subs {
		info.Submodules = append(info.Submodules, s.Config().Path)
		dirty, err := submoduleDirty(s)
		if err != nil {
			return nil, fmt.Errorf("submodule %s status: %w", s.Config().Path, err)
		}
		if dirty {
			info.DirtySubmodules = append(info.DirtySubmodules, s.Config().Path)
		}
	}
	sort.Strings(info.Submodules)
	sort.Strings(info.DirtySubmodules)

	others, err := linkedWorktrees(info.CommonDir)
	if err != nil {
		return nil, err
	}
	for name, path := range others {
		if name != info.WorktreeName {
			info.OtherWorktrees = append(info.OtherWorktrees, path)
		}
	}
	sort.Strings(info.OtherWorktrees)
	return info, nil
}

// firstParentRoot walks first parents from h to the parentless commit.
func firstParentRoot(repo *git.Repository, h plumbing.Hash) (string, error) {
	c, err := repo.CommitObject(h)
	if err != nil {
		return "", fmt.Errorf("commit %s: %w", h, err)
	}
	for len(c.ParentHashes) > 0 {
		c, err = repo.CommitObject(c.ParentHashes[0])
		if err != nil {
			return "", fmt.Errorf("commit %s: %w", c.Hash, err)
		}
	}
	return c.Hash.String(), nil
}

// submoduleDirty reports whether a submodule's checkout differs from the
// commit the parent repository expects (SubmoduleStatus.IsClean) or has
// uncommitted changes of its own (its own worktree status).
func submoduleDirty(s *git.Submodule) (bool, error) {
	sst, err := s.Status()
	if err != nil {
		return false, err
	}
	if !sst.IsClean() {
		return true, nil
	}
	subRepo, err := s.Repository()
	if err != nil {
		if errors.Is(err, git.ErrSubmoduleNotInitialized) {
			return false, nil
		}
		return false, err
	}
	subWT, err := subRepo.Worktree()
	if err != nil {
		return false, err
	}
	subStatus, err := subWT.Status()
	if err != nil {
		return false, err
	}
	return !subStatus.IsClean(), nil
}

func dirtyFromStatus(st git.Status) Dirty {
	var d Dirty
	for path, fs := range st {
		switch {
		case fs.Worktree == git.Untracked:
			d.Untracked = append(d.Untracked, path)
			continue
		case fs.Staging == git.Deleted || fs.Worktree == git.Deleted:
			d.Deleted = append(d.Deleted, path)
			continue
		}
		if fs.Staging != git.Unmodified && fs.Staging != git.Untracked {
			d.Staged = append(d.Staged, path)
		}
		if fs.Worktree == git.Modified {
			d.Modified = append(d.Modified, path)
		}
	}
	sort.Strings(d.Staged)
	sort.Strings(d.Modified)
	sort.Strings(d.Untracked)
	sort.Strings(d.Deleted)
	return d
}

// linkedWorktrees maps worktree name -> worktree directory by reading
// <common>/worktrees/<name>/gitdir (which holds "<W>/.git"). git writes
// that value relative to the directory holding it when
// worktree.useRelativePaths is set, so it is resolved the same way
// readGitdirFile resolves a relative W/.git.
func linkedWorktrees(commonDir string) (map[string]string, error) {
	out := map[string]string{}
	base := filepath.Join(commonDir, "worktrees")
	entries, err := os.ReadDir(base)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(base, e.Name())
		gd, err := os.ReadFile(filepath.Join(dir, "gitdir"))
		if err != nil {
			return nil, fmt.Errorf("worktree %s: %w", e.Name(), err)
		}
		v := strings.TrimSpace(string(gd))
		if !filepath.IsAbs(v) {
			v = filepath.Join(dir, v)
		}
		out[e.Name()] = filepath.Dir(filepath.Clean(v))
	}
	return out, nil
}
