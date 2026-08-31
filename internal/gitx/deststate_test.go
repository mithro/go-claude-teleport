package gitx

import (
	"path/filepath"
	"testing"
)

func TestDestStateAbsent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nope")
	st, err := DestStateOf(dir, filepath.Join(dir, ".worktrees", "x"), "x")
	if err != nil {
		t.Fatal(err)
	}
	if st.MainExists || st.WorktreeExists || st.BranchTip != "" {
		t.Errorf("absent main = %+v", st)
	}
}

func TestDestStateExistingMainWithBranch(t *testing.T) {
	dir := t.TempDir()
	repo, root := initRepo(t, dir)
	writeFile(t, filepath.Join(dir, "b.txt"), "b")
	second := commitAll(t, repo, "second")
	gitCLI(t, dir, "branch", "feature", root)
	st, err := DestStateOf(dir, filepath.Join(dir, ".worktrees", "feature"), "feature")
	if err != nil {
		t.Fatal(err)
	}
	if !st.MainExists || st.RootCommit != root {
		t.Errorf("main/root = %v %q", st.MainExists, st.RootCommit)
	}
	if st.RefTips["refs/heads/main"] != second || st.RefTips["refs/heads/feature"] != root {
		t.Errorf("RefTips = %v", st.RefTips)
	}
	if st.BranchTip != root || st.WorktreeExists || st.BranchCheckedOutElsewhere != "" {
		t.Errorf("state = %+v", st)
	}
}

func TestDestStateBranchCheckedOutElsewhere(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	w := addWorktree(t, dir, "feature")
	st, err := DestStateOf(dir, filepath.Join(dir, "elsewhere"), "feature")
	if err != nil {
		t.Fatal(err)
	}
	if st.BranchCheckedOutElsewhere != w {
		t.Errorf("BranchCheckedOutElsewhere = %q, want %q", st.BranchCheckedOutElsewhere, w)
	}
	// The main checkout counts too.
	st, err = DestStateOf(dir, filepath.Join(dir, "elsewhere"), "main")
	if err != nil {
		t.Fatal(err)
	}
	if st.BranchCheckedOutElsewhere != dir {
		t.Errorf("main branch: BranchCheckedOutElsewhere = %q, want %q", st.BranchCheckedOutElsewhere, dir)
	}
}

func TestDestStateSameDirCleanAndBranch(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	st, err := DestStateOf(dir, dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !st.WorktreeExists || st.WorktreeBranch != "main" || !st.Clean || st.BranchCheckedOutElsewhere != "" {
		t.Errorf("W==M clean: %+v", st)
	}
	writeFile(t, filepath.Join(dir, "dirty.txt"), "x")
	st, err = DestStateOf(dir, dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if st.Clean {
		t.Error("untracked file should make Clean false")
	}
}

func TestDestStateUnbornHead(t *testing.T) {
	dir := t.TempDir()
	gitCLI(t, dir, "init", "-b", "main")
	st, err := DestStateOf(dir, filepath.Join(dir, ".worktrees", "x"), "main")
	if err != nil {
		t.Fatal(err)
	}
	if !st.MainExists {
		t.Error("MainExists should be true for present repo")
	}
	if st.RootCommit != "" {
		t.Errorf("RootCommit should be empty for unborn HEAD, got %q", st.RootCommit)
	}
	if len(st.RefTips) != 0 {
		t.Errorf("RefTips should be empty for unborn HEAD, got %v", st.RefTips)
	}
}

func TestIsAncestor(t *testing.T) {
	dir := t.TempDir()
	repo, root := initRepo(t, dir)
	writeFile(t, filepath.Join(dir, "b.txt"), "b")
	second := commitAll(t, repo, "second")
	for _, c := range []struct {
		a, d string
		want bool
	}{{root, second, true}, {second, root, false}, {root, root, true}} {
		got, err := IsAncestor(dir, c.a, c.d)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("IsAncestor(%s,%s) = %v, want %v", c.a[:7], c.d[:7], got, c.want)
		}
	}
	if _, err := IsAncestor(dir, "0000000000000000000000000000000000000000", second); err == nil {
		t.Error("unknown ancestor hash must be an error, not false")
	}
}
