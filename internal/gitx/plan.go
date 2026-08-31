// internal/gitx/plan.go
package gitx

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/session"
)

type Mode string

const (
	ModeNotRepo      Mode = "not-repo"
	ModeFreshMain    Mode = "fresh-main"    // M absent: transfer everything
	ModeExistingMain Mode = "existing-main" // M present: pack + attach
)

type Plan struct {
	Mode                 Mode
	SrcMain, SrcWorktree string
	DstMain, DstWorktree string
	Linked               bool
	WorktreeName         string
	Branch               string
	Tip                  string
	Detached             bool
	NeedPack             bool
	HaveTips             []string // destination tips to exclude from the pack
	FastForward          bool     // branch exists on dest and is an ancestor of Tip
	Dirty                Dirty

	// Additions (Plan 03):
	IndexRel     string         // ".git/index" or ".git/worktrees/<n>/index", relative to SrcMain/DstMain
	StagedBlobs  []string       // blob hashes referenced by the index but not by Tip; set with SetStagedBlobs, never directly
	PackEntryID  int            // manifest entry id of the pack, 0 = none (existing-main only)
	IndexEntryID int            // manifest entry id of the index file (existing-main only)
	DirtyEntries map[string]int // dst path -> manifest entry id of dirty worktree files (existing-main only)
}

// SetStagedBlobs records the blob hashes the transferred index references
// that Tip does not carry (StagedBlobsOf computes them). Callers MUST use
// this rather than assigning StagedBlobs: staged blobs force a pack even
// when the destination branch already sits at Tip, otherwise the index
// would land referencing objects the destination never received.
func (p *Plan) SetStagedBlobs(blobs []string) {
	p.StagedBlobs = blobs
	p.NeedPack = p.NeedPack || len(blobs) > 0
}

type RefuseError struct{ Reason string }

func (e *RefuseError) Error() string { return "refused: " + e.Reason }

func refuse(format string, a ...any) error { return &RefuseError{Reason: fmt.Sprintf(format, a...)} }

// PlanTransfer decides the mode or returns a *RefuseError (spec §8).
// src == nil means the session cwd is not a repository; the caller fills
// SrcWorktree/DstWorktree from the session cwd in that case.
func PlanTransfer(src *Info, dst *DestState, pm session.PathMap) (*Plan, error) {
	if src == nil {
		return &Plan{Mode: ModeNotRepo}, nil
	}
	if len(src.DirtySubmodules) > 0 {
		return nil, refuse("submodule(s) with uncommitted changes: %s", strings.Join(src.DirtySubmodules, ", "))
	}
	p := &Plan{
		SrcMain: src.MainDir, SrcWorktree: src.Root,
		DstMain: pm.ApplyPath(src.MainDir), DstWorktree: pm.ApplyPath(src.Root),
		Linked: src.IsLinked, WorktreeName: src.WorktreeName,
		Branch: src.Branch, Tip: src.Head, Detached: src.Detached, Dirty: src.Dirty,
		IndexRel: ".git/index",
	}
	if src.IsLinked {
		p.IndexRel = path.Join(".git/worktrees", src.WorktreeName, "index")
	}
	if !dst.MainExists {
		if dst.WorktreeExists {
			return nil, refuse("destination worktree directory %s already exists", p.DstWorktree)
		}
		p.Mode = ModeFreshMain
		return p, nil
	}
	p.Mode = ModeExistingMain
	if dst.RootCommit == "" {
		return nil, refuse("%s on the destination is an empty repository (no commits); populate it first, e.g. git fetch", p.DstMain)
	}
	if dst.RootCommit != src.RootCommit {
		return nil, refuse("%s on the destination is a different repository (root commit %s, source %s)", p.DstMain, short(dst.RootCommit), short(src.RootCommit))
	}
	seen := map[string]bool{}
	for _, h := range dst.RefTips {
		if !seen[h] {
			seen[h] = true
			p.HaveTips = append(p.HaveTips, h)
		}
	}
	sort.Strings(p.HaveTips)
	switch {
	case src.Detached:
		p.NeedPack = !seen[src.Head]
	case dst.BranchTip == "":
		p.NeedPack = true
	case dst.BranchTip == src.Head:
		p.NeedPack = false
	case dst.BranchTipReachable:
		p.FastForward, p.NeedPack = true, true
	default:
		return nil, refuse("branch %s on the destination (%s) is not a fast-forward of the source (%s)", src.Branch, short(dst.BranchTip), short(src.Head))
	}
	if src.IsLinked {
		if dst.WorktreeExists {
			return nil, refuse("destination worktree directory %s already exists", p.DstWorktree)
		}
		if dst.BranchCheckedOutElsewhere != "" {
			return nil, refuse("branch %s is already checked out on the destination at %s", src.Branch, dst.BranchCheckedOutElsewhere)
		}
		return p, nil
	}
	// W == M
	if !dst.WorktreeExists {
		return nil, refuse("destination main checkout %s has no working tree", p.DstMain)
	}
	if dst.WorktreeBranch == "" {
		return nil, refuse("destination checkout %s has no branch checked out (detached HEAD, or not a checkout of this repository), session branch is %q", p.DstMain, src.Branch)
	}
	if dst.WorktreeBranch != src.Branch {
		return nil, refuse("destination checkout %s has branch %q checked out, session branch is %q", p.DstMain, dst.WorktreeBranch, src.Branch)
	}
	if !dst.Clean {
		return nil, refuse("destination checkout %s is not clean", p.DstMain)
	}
	return p, nil
}

func short(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}
