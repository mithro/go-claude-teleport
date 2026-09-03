// internal/transfer/roots.go
package transfer

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// GitRoots builds Manifest.Roots (ruling R-P3-B1d) from a gitx.Plan's own
// already-computed destination paths: DstMain and DstWorktree are both
// declared in every git repo mode (fresh-main, existing-main), and
// DstWorktree alone (== the destination cwd) in not-a-repo mode, where
// DstMain is "". Deduplicated (DstMain == DstWorktree for an unlinked W==M
// checkout) and empty strings dropped. Callers pass gitx.Plan fields
// directly rather than this package importing gitx, which would be a
// dependency cycle (gitx has no reason to import transfer, but the
// convention elsewhere in this codebase — dataDirFromStagingDir, the
// canonicalCaptureDst jobID parameter — is that transfer takes exactly the
// primitive values it needs rather than a caller's richer type).
func GitRoots(dstMain, dstWorktree string) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range []string{dstMain, dstWorktree} {
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

// gitRootCategory reports whether cat is one of the two categories a
// declared Root governs.
func gitRootCategory(cat session.Category) bool {
	return cat == session.CatRepo || cat == session.CatWorktree
}

// entryRoot returns the first of roots that dst equals or is lexically
// nested under, or ("", false) if none contains it — including when roots
// is empty, which makes a manifest that carries a CatRepo/CatWorktree
// entry but never declared any Root exactly as untrusted as one naming a
// bogus Dst.
func entryRoot(dst string, roots []string) (string, bool) {
	clean := filepath.Clean(dst)
	for _, r := range roots {
		if r == "" {
			continue
		}
		if rc := filepath.Clean(r); underDir(clean, rc) {
			return rc, true
		}
	}
	return "", false
}

// validRoot is ruling R-P3-B1d's containment check for a declared
// CatRepo/CatWorktree Root, independent of anything the manifest itself
// claims: it must be a real boundary under $HOME — not $HOME itself (a
// Root that IS Home would let a CatWorktree/CatRepo entry land literally
// anywhere under Home, defeating the whole point of declaring Roots),
// outside the config dir and the data dir (both already have their own,
// separate and stricter, checks elsewhere), and its first path component
// under Home must not be dot-prefixed — a bare ~/.something is exactly the
// shape of the config dirs, dotfiles and dot-directories a shell or tool
// reads on login; a Root has no fixed name list to check against the way
// session.Forbidden does for CatSession, so the dot-prefix shape itself is
// refused instead.
func validRoot(root string, p session.Paths) error {
	r := filepath.Clean(root)
	home := filepath.Clean(p.Home)
	if !underDir(r, home) {
		return fmt.Errorf("is not under home %s", p.Home)
	}
	if r == home {
		return fmt.Errorf("must not be $HOME itself")
	}
	if underDir(r, filepath.Clean(p.ConfigDir)) {
		return fmt.Errorf("is under the config dir %s", p.ConfigDir)
	}
	if underDir(r, filepath.Clean(p.DataDir)) {
		return fmt.Errorf("is under the data dir %s", p.DataDir)
	}
	rel, err := filepath.Rel(home, r)
	if err != nil {
		return fmt.Errorf("relative to home: %w", err)
	}
	first := rel
	if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
		first = rel[:i]
	}
	if strings.HasPrefix(first, ".") {
		return fmt.Errorf("first path component %q under home is dot-prefixed", first)
	}
	return nil
}

// rootForeignContent walks root (which may not exist) and returns the
// first path found that is neither root itself, nor one of owned, nor a
// lexical ancestor directory of one of owned — i.e. content this
// manifest's own CatRepo/CatWorktree entries do not account for. An
// absent root has none ("", nil).
//
// This is a subset check ("is everything under root something this job's
// OWN entries would put there"), not a bare emptiness check, because
// Diff/Install run more than once against the very same destination
// across one teleport's lifetime: verifyInstall re-Diffs after a
// successful install to confirm present-same, and a crashed job is
// resumed by re-running the very same steps. A plain "root must be empty"
// check would refuse the destination's OWN files the instant install
// placed the first one — breaking every fresh-main/not-a-repo teleport of
// more than trivial size at its own verify step, not just a crash-retry
// edge case. Only content that predates (or is unrelated to) this job —
// a hostile Root claim, or an incidentally non-empty directory — fails.
func rootForeignContent(root string, owned []string) (string, error) {
	fi, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return root, nil // a non-directory standing in for the root is never fresh
	}
	allowed := map[string]bool{filepath.Clean(root): true}
	for _, d := range owned {
		for p := filepath.Clean(d); !allowed[p]; {
			allowed[p] = true
			parent := filepath.Dir(p)
			if parent == p {
				break
			}
			p = parent
		}
	}
	var foreign string
	walkErr := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !allowed[filepath.Clean(path)] {
			foreign = path
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return "", walkErr
	}
	return foreign, nil
}

// rootChecker precomputes, once per Diff/Install call, which of a
// manifest's declared Roots are safe to place CatRepo/CatWorktree content
// under — memoized per root so a Diff/Install call that touches many
// entries sharing one root only walks that root's tree once. p is nil for
// Diff (which has no session.Paths to check validRoot's Home/ConfigDir/
// DataDir containment against — see the package-level note by
// dataDirFromStagingDir on why this package avoids threading one through
// every caller); Diff's mirror therefore checks only what it CAN derive
// without one (declared-root membership, root freshness), while Install's
// validateDst — always given a real p, and the actual placement gate
// regardless of what Diff/a status map claimed — is authoritative for the
// full check.
type rootChecker struct {
	declared []string
	p        *session.Paths
	owned    map[string][]string
	bad      map[string]string // root -> reason ("" once checked and fine)
}

func newRootChecker(m *Manifest, p *session.Paths) *rootChecker {
	rc := &rootChecker{declared: m.Roots, p: p, owned: map[string][]string{}, bad: map[string]string{}}
	for _, e := range m.Entries {
		if !gitRootCategory(e.Category) || e.Deferred {
			continue
		}
		if root, ok := entryRoot(e.Dst, m.Roots); ok {
			rc.owned[root] = append(rc.owned[root], e.Dst)
		}
	}
	return rc
}

// roots returns the manifest's own declared Roots, for a caller (Install's
// validateDst) that needs to re-run entryRoot per entry itself.
func (rc *rootChecker) roots() []string { return rc.declared }

// check returns "" if root is fine to place non-Deferred CatRepo/
// CatWorktree content under, else the reason it is not.
func (rc *rootChecker) check(root string) (string, error) {
	if reason, ok := rc.bad[root]; ok {
		return reason, nil
	}
	if rc.p != nil {
		if err := validRoot(root, *rc.p); err != nil {
			rc.bad[root] = err.Error()
			return rc.bad[root], nil
		}
	}
	foreign, err := rootForeignContent(root, rc.owned[root])
	if err != nil {
		return "", err
	}
	reason := ""
	if foreign != "" {
		reason = fmt.Sprintf("contains content this job does not own: %s", foreign)
	}
	rc.bad[root] = reason
	return reason, nil
}
