// internal/transfer/roots.go
package transfer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// Root is one destination directory a CatRepo/CatWorktree entry may be
// installed under (ruling R-P3-B1d, reshaped by R-P3-B1e). Path is
// gitx.Plan's own DstMain/DstWorktree. MayPreExist marks not-a-repo mode,
// whose single Root is the destination cwd — the DRIVER user's chosen
// directory, which legitimately already exists and holds unrelated files
// (ruling R-P3-B1e item 4). It relaxes ONLY the provenance rule below; every
// containment rule (under $HOME, not $HOME, outside ConfigDir/DataDir, no
// dot-prefixed first component, real-path resolution) still applies, and the
// ordinary per-file collision rules do the rest: an absent file is created,
// a present-same one skipped, a present-different one blocks the teleport.
type Root struct {
	Path        string `json:"path"`
	MayPreExist bool   `json:"may_pre_exist,omitempty"`
}

// GitRoots builds Manifest.Roots from a gitx.Plan's own already-computed
// destination paths: DstMain and DstWorktree are both declared in every git
// repo mode (fresh-main, existing-main), and DstWorktree alone (== the
// destination cwd) in not-a-repo mode, where DstMain is "" and mayPreExist
// is true. Deduplicated (DstMain == DstWorktree for an unlinked W==M
// checkout) and empty strings dropped. Callers pass gitx.Plan fields
// directly rather than this package importing gitx, which would be a
// dependency cycle.
func GitRoots(dstMain, dstWorktree string, mayPreExist bool) []Root {
	var out []Root
	seen := map[string]bool{}
	for _, r := range []string{dstMain, dstWorktree} {
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, Root{Path: r, MayPreExist: mayPreExist})
	}
	return out
}

// gitRootCategory reports whether cat is one of the two categories a
// declared Root governs.
func gitRootCategory(cat session.Category) bool {
	return cat == session.CatRepo || cat == session.CatWorktree
}

// entryRoot returns the declared root that dst equals or is nested under,
// preferring the LONGEST match (a linked worktree's root nests inside the
// main repo's, and the more specific one is the one whose own rules must
// govern the entry). ok is false when no declared root contains dst —
// including when roots is empty, which makes a manifest that carries a
// CatRepo/CatWorktree entry but never declared a Root exactly as untrusted
// as one naming a bogus Dst.
func entryRoot(cleanDst string, roots []Root) (Root, bool) {
	var best Root
	found := false
	for _, r := range roots {
		if r.Path == "" {
			continue
		}
		rc := filepath.Clean(r.Path)
		if !underDir(cleanDst, rc) {
			continue
		}
		if !found || len(rc) > len(filepath.Clean(best.Path)) {
			best, found = Root{Path: rc, MayPreExist: r.MayPreExist}, true
		}
	}
	return best, found
}

// resolveExisting resolves path's longest EXISTING prefix with
// filepath.EvalSymlinks and rejoins the components that do not exist yet,
// so containment can be judged on where a path REALLY lands rather than on
// its spelling (ruling R-P3-B1e item 2: a symlinked ancestor must not be
// able to smuggle a Root — or a write — out of its declared boundary).
// A component that exists but is not a directory (ENOTDIR while walking
// up) surfaces as an error rather than a silently truncated resolution.
func resolveExisting(path string) (string, error) {
	clean := filepath.Clean(path)
	var rest []string
	for cur := clean; ; {
		res, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				res = filepath.Join(res, rest[i])
			}
			return res, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return clean, nil // not even the filesystem root resolves: nothing to do
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

// jobRootsFile is jobs/<id>/roots.json on the DESTINATION: the repo roots
// this job itself created here (ruling R-P3-B1e item 3). It is the
// destination's own record — never wire data — and it is what makes
// "freshness" a question of provenance rather than of a directory's
// current contents: Install claims a Root the moment it finds it absent
// (before placing anything under it), so every later Diff/Install of the
// same job — verifyInstall's re-diff, a resumed job's repeated steps —
// still sees a legitimately non-empty Root as its own.
type jobRootsFile struct {
	Version int      `json:"version"`
	Roots   []string `json:"roots"`
}

func jobRootsPath(dataDir, jobID string) (string, error) {
	if err := job.ValidateID(jobID); err != nil {
		return "", fmt.Errorf("job roots: %w", err)
	}
	return filepath.Join(job.Dir(dataDir, jobID), "roots.json"), nil
}

// loadJobRoots reads the recorded roots; a missing file is no roots.
func loadJobRoots(dataDir, jobID string) (map[string]bool, error) {
	path, err := jobRootsPath(dataDir, jobID)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f jobRootsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := make(map[string]bool, len(f.Roots))
	for _, r := range f.Roots {
		out[filepath.Clean(r)] = true
	}
	return out, nil
}

// recordJobRoot adds root to jobs/<id>/roots.json (temp + rename, 0600).
func recordJobRoot(dataDir, jobID string, recorded map[string]bool, root string) error {
	path, err := jobRootsPath(dataDir, jobID)
	if err != nil {
		return err
	}
	recorded[filepath.Clean(root)] = true
	all := make([]string, 0, len(recorded))
	for r := range recorded {
		all = append(all, r)
	}
	sort.Strings(all)
	raw, err := json.MarshalIndent(jobRootsFile{Version: 1, Roots: all}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("record job root: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("record job root: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("record job root: %w", err)
	}
	return nil
}

// firstComponentDotted reports whether the first path component of dir
// relative to home is dot-prefixed — the shape of every config dir,
// dotfile and dot-directory a shell or tool reads on login. A Root has no
// fixed name list to check against the way session.Forbidden does for
// CatSession, so the shape itself is refused.
func firstComponentDotted(home, dir string) (bool, error) {
	rel, err := filepath.Rel(home, dir)
	if err != nil {
		return false, err
	}
	first := rel
	if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
		first = rel[:i]
	}
	return strings.HasPrefix(first, "."), nil
}
