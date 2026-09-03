// internal/remote/attachcheck.go
package remote

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// checkAttachPlan is ruling R-P3-B1f N1: git-attach is the destination's
// SECOND write path, and until this check existed it took the wire Plan at
// face value. gitx's own containment (checkDirtyContainment) is measured
// against the plan's DstWorktree, so a plan naming $HOME as the worktree
// contained nothing at all; repairLinkedMetadata wrote its two files with
// no preconditions whatsoever; and WorktreeName was joined straight into a
// path under .git/worktrees/.
//
// So before gitx.Attach ever runs, the DESTINATION validates the plan's
// own paths with the same rules transfer.Install applies to a manifest
// entry — resolved (EvalSymlinks) containment under $HOME, never $HOME
// itself, never inside the config dir or the data dir, no dot-prefixed
// first component — plus what only git can say:
//
//   - fresh-main: both destination roots must be directories THIS job's
//     install created (jobs/<id>/roots.json). Fresh-main's whole payload
//     arrives through transfer.Install, so anything else means the plan is
//     pointing git-attach at somebody else's directory.
//   - existing-main: DstMain must already be a git repository root, and
//     DstWorktree must be that same repository (the W == M shape) or a
//     linked worktree — one that already exists, or one gitx is about to
//     create in a place that is empty or absent (createLinkedWorktree
//     refuses anything else, and re-checks the branch).
//   - WorktreeName must be a single safe path component, so it cannot
//     climb out of <DstMain>/.git/worktrees/.
//   - IndexRel must be exactly the index path the mode implies.
//   - no control character (a newline above all) may reach the single-line
//     `.git`/`gitdir` files repairLinkedMetadata writes.
//   - every DirtyEntries destination must RESOLVE under the resolved
//     DstWorktree and outside its .git.
//
// Accepted residual, ledgered: for existing-main the destination applies
// the source's dirty working-tree files onto a matching destination
// repository. That is the feature — a teleport carries uncommitted work —
// and the checks above bound it to a real repository the user already has.
func (l *Local) checkAttachPlan(p *gitx.Plan, jobID string) error {
	if p.Mode == gitx.ModeNotRepo {
		return nil // gitx.Attach does nothing at all in this mode
	}
	for _, f := range []struct{ name, value string }{
		{"DstMain", p.DstMain},
		{"DstWorktree", p.DstWorktree},
		{"WorktreeName", p.WorktreeName},
		{"Branch", p.Branch},
		{"IndexRel", p.IndexRel},
	} {
		if i := strings.IndexFunc(f.value, func(r rune) bool { return r < 0x20 || r == 0x7f }); i >= 0 {
			return transfer.Refuse("", "git plan %s contains a control character at byte %d", f.name, i)
		}
	}
	for _, d := range []struct{ name, value string }{{"DstMain", p.DstMain}, {"DstWorktree", p.DstWorktree}} {
		if d.value == "" || !filepath.IsAbs(d.value) || d.value != filepath.Clean(d.value) {
			return transfer.Refuse(d.value, "git plan %s is not a clean absolute path", d.name)
		}
		if err := transfer.CheckDestDir(l.paths, d.value); err != nil {
			return err
		}
	}
	if p.Linked {
		if err := job.ValidateID(p.WorktreeName); err != nil {
			return transfer.Refuse(p.WorktreeName, "git plan WorktreeName is not a single safe path component: %v", err)
		}
	} else if filepath.Clean(p.DstWorktree) != filepath.Clean(p.DstMain) {
		return transfer.Refuse(p.DstWorktree, "git plan is not linked, so the destination worktree must be the main checkout %s", p.DstMain)
	}
	wantIndex := ".git/index"
	if p.Linked {
		wantIndex = ".git/worktrees/" + p.WorktreeName + "/index"
	}
	if p.IndexRel != "" && p.IndexRel != wantIndex {
		return transfer.Refuse(p.IndexRel, "git plan IndexRel must be %q for this shape", wantIndex)
	}
	switch p.Mode {
	case gitx.ModeFreshMain:
		for _, dir := range []string{p.DstMain, p.DstWorktree} {
			ours, err := transfer.JobCreatedRoot(l.paths, jobID, dir)
			if err != nil {
				return err
			}
			if !ours {
				return transfer.Refuse(dir, "fresh-main git-attach may only touch a directory this job's own install created")
			}
		}
	case gitx.ModeExistingMain:
		if !isDir(filepath.Join(p.DstMain, ".git")) {
			return transfer.Refuse(p.DstMain, "existing-main git-attach needs an existing git repository there (no .git directory)")
		}
		// A linked worktree that gitx is about to create legitimately does
		// not exist yet (createLinkedWorktree refuses a non-empty one, and
		// checkLinkedDestination re-reads the destination first); one that
		// does exist must really be a worktree root.
		if _, err := os.Lstat(p.DstWorktree); err == nil {
			if !exists(filepath.Join(p.DstWorktree, ".git")) {
				return transfer.Refuse(p.DstWorktree, "existing-main git-attach needs a git worktree root there (no .git)")
			}
		} else if !os.IsNotExist(err) {
			return transfer.Refuse(p.DstWorktree, "%v", err)
		}
	}
	resWorktree, err := transfer.ResolveRealPath(p.DstWorktree)
	if err != nil {
		return transfer.Refuse(p.DstWorktree, "resolve: %v", err)
	}
	dotGit := filepath.Join(resWorktree, ".git")
	for dst := range p.DirtyEntries {
		if dst == "" || !filepath.IsAbs(dst) || dst != filepath.Clean(dst) {
			return transfer.Refuse(dst, "git plan dirty file is not a clean absolute path")
		}
		res, err := transfer.ResolveRealPath(dst)
		if err != nil {
			return transfer.Refuse(dst, "resolve: %v", err)
		}
		if !underDirPath(res, resWorktree) || res == resWorktree {
			return transfer.Refuse(dst, "git plan dirty file resolves to %s, outside the destination worktree %s", res, resWorktree)
		}
		if res == dotGit || underDirPath(res, dotGit) {
			return transfer.Refuse(dst, "git plan dirty file resolves inside %s; only the index is written there", dotGit)
		}
	}
	return nil
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// underDirPath reports whether cleanPath is dir or lexically nested under
// it; both must already be clean absolute paths.
func underDirPath(cleanPath, dir string) bool {
	return cleanPath == dir || strings.HasPrefix(cleanPath, dir+string(filepath.Separator))
}
