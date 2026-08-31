// internal/gitx/plan_test.go
package gitx

import (
	"errors"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

const (
	rootA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rootB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tip   = "cccccccccccccccccccccccccccccccccccccccc"
	older = "dddddddddddddddddddddddddddddddddddddddd"
)

func linkedInfo() *Info {
	return &Info{Root: "/home/alice/github/x/.worktrees/feat", CommonDir: "/home/alice/github/x/.git", MainDir: "/home/alice/github/x",
		IsLinked: true, WorktreeName: "feat", Branch: "feat", Head: tip, RootCommit: rootA,
		Dirty: Dirty{Modified: []string{"a.go"}, Untracked: []string{"b.go"}}}
}

func mainInfo() *Info {
	return &Info{Root: "/home/alice/github/x", CommonDir: "/home/alice/github/x/.git", MainDir: "/home/alice/github/x",
		Branch: "main", Head: tip, RootCommit: rootA}
}

func detachedInfo() *Info {
	return &Info{Root: "/home/alice/github/x/.worktrees/feat", CommonDir: "/home/alice/github/x/.git", MainDir: "/home/alice/github/x",
		IsLinked: true, WorktreeName: "feat", Head: tip, Detached: true, RootCommit: rootA}
}

func TestPlanTransferTable(t *testing.T) {
	pm := session.NewPathMap(session.Mapping{From: "/home/alice", To: "/home/bob"})
	cases := []struct {
		name   string
		src    *Info
		dst    *DestState
		mode   Mode
		ff     bool
		pack   bool
		refuse string // substring of RefuseError.Reason, "" = no refusal
	}{
		{"not a repo", nil, &DestState{}, ModeNotRepo, false, false, ""},
		{"fresh main, linked", linkedInfo(), &DestState{}, ModeFreshMain, false, false, ""},
		{"fresh main, W exists", linkedInfo(), &DestState{WorktreeExists: true}, "", false, false, "already exists"},
		{"existing main, branch absent", linkedInfo(), &DestState{MainExists: true, RootCommit: rootA, RefTips: map[string]string{"refs/heads/main": older}}, ModeExistingMain, false, true, ""},
		{"existing main, different root", linkedInfo(), &DestState{MainExists: true, RootCommit: rootB}, "", false, false, "different repository"},
		{"existing main, branch at tip", linkedInfo(), &DestState{MainExists: true, RootCommit: rootA, BranchTip: tip, RefTips: map[string]string{"refs/heads/feat": tip}}, ModeExistingMain, false, false, ""},
		{"existing main, fast-forward", linkedInfo(), &DestState{MainExists: true, RootCommit: rootA, BranchTip: older, BranchTipReachable: true, RefTips: map[string]string{"refs/heads/feat": older}}, ModeExistingMain, true, true, ""},
		{"existing main, diverged", linkedInfo(), &DestState{MainExists: true, RootCommit: rootA, BranchTip: older, BranchTipReachable: false}, "", false, false, "not a fast-forward"},
		{"existing main, W exists", linkedInfo(), &DestState{MainExists: true, RootCommit: rootA, WorktreeExists: true}, "", false, false, "already exists"},
		{"existing main, branch elsewhere", linkedInfo(), &DestState{MainExists: true, RootCommit: rootA, BranchCheckedOutElsewhere: "/home/bob/github/x/.worktrees/old"}, "", false, false, "checked out"},
		{"W==M clean same branch", mainInfo(), &DestState{MainExists: true, RootCommit: rootA, WorktreeExists: true, WorktreeBranch: "main", Clean: true, BranchTip: older, BranchTipReachable: true}, ModeExistingMain, true, true, ""},
		{"W==M dirty", mainInfo(), &DestState{MainExists: true, RootCommit: rootA, WorktreeExists: true, WorktreeBranch: "main", Clean: false, BranchTip: tip}, "", false, false, "not clean"},
		{"W==M other branch", mainInfo(), &DestState{MainExists: true, RootCommit: rootA, WorktreeExists: true, WorktreeBranch: "dev", Clean: true, BranchTip: tip}, "", false, false, "checked out"},
		{"dirty submodule", func() *Info {
			i := linkedInfo()
			i.Submodules = []string{"v/s"}
			i.DirtySubmodules = []string{"v/s"}
			return i
		}(), &DestState{}, "", false, false, "submodule"},
		{"detached, dest has tip", detachedInfo(), &DestState{MainExists: true, RootCommit: rootA, RefTips: map[string]string{"refs/heads/main": tip}}, ModeExistingMain, false, false, ""},
		{"detached, dest lacks tip", detachedInfo(), &DestState{MainExists: true, RootCommit: rootA, RefTips: map[string]string{"refs/heads/main": older}}, ModeExistingMain, false, true, ""},
		{"W==M no working tree", mainInfo(), &DestState{MainExists: true, RootCommit: rootA}, "", false, false, "no working tree"},
		{"existing main, empty dest repository", linkedInfo(), &DestState{MainExists: true, RootCommit: ""}, "", false, false, "empty repository"},
		{"W==M dest has no branch checked out", mainInfo(), &DestState{MainExists: true, RootCommit: rootA, WorktreeExists: true, WorktreeBranch: "", Clean: true, BranchTip: tip}, "", false, false, "no branch checked out"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := PlanTransfer(c.src, c.dst, pm)
			if c.refuse != "" {
				var re *RefuseError
				if !errors.As(err, &re) {
					t.Fatalf("err = %v, want *RefuseError", err)
				}
				if !containsFold(re.Reason, c.refuse) {
					t.Fatalf("Reason = %q, want it to mention %q", re.Reason, c.refuse)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.Mode != c.mode || p.FastForward != c.ff || p.NeedPack != c.pack {
				t.Errorf("mode/ff/pack = %v/%v/%v, want %v/%v/%v", p.Mode, p.FastForward, p.NeedPack, c.mode, c.ff, c.pack)
			}
			if c.src != nil {
				if p.DstMain != "/home/bob/github/x" {
					t.Errorf("DstMain = %q", p.DstMain)
				}
				if c.src.IsLinked && p.DstWorktree != "/home/bob/github/x/.worktrees/feat" {
					t.Errorf("DstWorktree = %q", p.DstWorktree)
				}
				if c.src.IsLinked && p.IndexRel != ".git/worktrees/feat/index" {
					t.Errorf("IndexRel = %q", p.IndexRel)
				}
				if !c.src.IsLinked && p.IndexRel != ".git/index" {
					t.Errorf("IndexRel = %q", p.IndexRel)
				}
			}
		})
	}
}

func TestPlanTransferHaveTipsDeduped(t *testing.T) {
	p, err := PlanTransfer(linkedInfo(), &DestState{MainExists: true, RootCommit: rootA,
		RefTips: map[string]string{"refs/heads/main": older, "refs/heads/dev": older, "refs/heads/z": rootA}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.HaveTips) != 2 {
		t.Errorf("HaveTips = %v, want 2 distinct hashes", p.HaveTips)
	}
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
