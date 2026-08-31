package gitx

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestInspectNotRepo(t *testing.T) {
	_, err := Inspect(t.TempDir())
	if !errors.Is(err, ErrNotRepo) {
		t.Fatalf("err = %v, want ErrNotRepo", err)
	}
}

func TestInspectMainCheckout(t *testing.T) {
	dir := t.TempDir()
	_, root := initRepo(t, dir)
	info, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Root != dir || info.MainDir != dir || info.CommonDir != filepath.Join(dir, ".git") {
		t.Errorf("paths = %+v", info)
	}
	if info.IsLinked || info.WorktreeName != "" || info.Detached {
		t.Errorf("flags = %+v", info)
	}
	if info.Branch != "main" || info.Head != root || info.RootCommit != root {
		t.Errorf("branch/head/root = %q %q %q, want main %q", info.Branch, info.Head, info.RootCommit, root)
	}
	if len(info.Dirty.Staged)+len(info.Dirty.Modified)+len(info.Dirty.Untracked)+len(info.Dirty.Deleted) != 0 {
		t.Errorf("clean repo reported dirty: %+v", info.Dirty)
	}
}

func TestInspectSubdirFindsRoot(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	writeFile(t, filepath.Join(dir, "a", "b", "c.txt"), "x")
	info, err := Inspect(filepath.Join(dir, "a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Root != dir {
		t.Errorf("Root = %q, want %q", info.Root, dir)
	}
}

func TestInspectLinkedWorktree(t *testing.T) {
	dir := t.TempDir()
	_, root := initRepo(t, dir)
	w := addWorktree(t, dir, "feature")
	other := addWorktree(t, dir, "other")
	info, err := Inspect(w)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsLinked || info.WorktreeName != "feature" {
		t.Errorf("linked/name = %v %q", info.IsLinked, info.WorktreeName)
	}
	if info.Root != w || info.MainDir != dir || info.CommonDir != filepath.Join(dir, ".git") {
		t.Errorf("paths = %+v", info)
	}
	if info.Branch != "feature" || info.RootCommit != root {
		t.Errorf("branch/root = %q %q", info.Branch, info.RootCommit)
	}
	if diff := cmp.Diff([]string{other}, info.OtherWorktrees); diff != "" {
		t.Errorf("OtherWorktrees (-want +got):\n%s", diff)
	}
}

func TestInspectDirtyMatchesGitStatus(t *testing.T) {
	dir := t.TempDir()
	repo, _ := initRepo(t, dir)
	writeFile(t, filepath.Join(dir, "tracked.txt"), "1\n")
	writeFile(t, filepath.Join(dir, "gone.txt"), "2\n")
	commitAll(t, repo, "second")
	writeFile(t, filepath.Join(dir, "tracked.txt"), "changed\n") // modified
	writeFile(t, filepath.Join(dir, "new.txt"), "new\n")         // untracked
	writeFile(t, filepath.Join(dir, "staged.txt"), "staged\n")   // staged (added)
	gitCLI(t, dir, "add", "staged.txt")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil { // deleted
		t.Fatal(err)
	}
	info, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := Dirty{Staged: []string{"staged.txt"}, Modified: []string{"tracked.txt"}, Untracked: []string{"new.txt"}, Deleted: []string{"gone.txt"}}
	if diff := cmp.Diff(want, info.Dirty); diff != "" {
		t.Errorf("Dirty (-want +got):\n%s", diff)
	}
	// Cross-check: every path git reports is in exactly one of our lists.
	got := map[string]bool{}
	for _, l := range porcelain(t, dir) {
		got[l[3:]] = true
	}
	var ours []string
	ours = append(ours, info.Dirty.Staged...)
	ours = append(ours, info.Dirty.Modified...)
	ours = append(ours, info.Dirty.Untracked...)
	ours = append(ours, info.Dirty.Deleted...)
	sort.Strings(ours)
	var theirs []string
	for p := range got {
		theirs = append(theirs, p)
	}
	sort.Strings(theirs)
	if diff := cmp.Diff(theirs, ours); diff != "" {
		t.Errorf("git status --porcelain vs Inspect (-git +ours):\n%s", diff)
	}
}

func TestInspectDetachedHead(t *testing.T) {
	dir := t.TempDir()
	_, root := initRepo(t, dir)
	gitCLI(t, dir, "checkout", "--detach", root)
	info, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Detached || info.Branch != "" || info.Head != root {
		t.Errorf("detached = %v branch=%q head=%q", info.Detached, info.Branch, info.Head)
	}
}

func TestInspectSubmodules(t *testing.T) {
	sub := t.TempDir()
	initRepo(t, sub)
	dir := t.TempDir()
	repo, _ := initRepo(t, dir)
	gitCLI(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", sub, "vendor/sub")
	commitAll(t, repo, "add submodule")
	info, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"vendor/sub"}, info.Submodules); diff != "" {
		t.Errorf("Submodules (-want +got):\n%s", diff)
	}
	if len(info.DirtySubmodules) != 0 {
		t.Errorf("clean submodule reported dirty: %v", info.DirtySubmodules)
	}
	writeFile(t, filepath.Join(dir, "vendor", "sub", "junk.txt"), "x")
	info, err = Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"vendor/sub"}, info.DirtySubmodules); diff != "" {
		t.Errorf("DirtySubmodules (-want +got):\n%s", diff)
	}
}
