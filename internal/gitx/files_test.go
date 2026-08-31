package gitx

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mithro/go-claude-teleport/internal/session"
)

func rels(entries []session.FileEntry, cat session.Category) []string {
	var out []string
	for _, e := range entries {
		if cat == "" || e.Category == cat {
			out = append(out, e.Rel)
		}
	}
	sort.Strings(out)
	return out
}

func has(entries []session.FileEntry, rel string) bool {
	for _, e := range entries {
		if e.Rel == rel {
			return true
		}
	}
	return false
}

func TestFilesFreshMainLinked(t *testing.T) {
	dir := t.TempDir()
	repo, _ := initRepo(t, dir)
	writeFile(t, filepath.Join(dir, ".gitignore"), "*.log\nbuild/\n")
	commitAll(t, repo, "ignore")
	w := addWorktree(t, dir, "feat")
	addWorktree(t, dir, "other")
	writeFile(t, filepath.Join(w, "src.go"), "package x")
	writeFile(t, filepath.Join(w, "debug.log"), "ignored")
	writeFile(t, filepath.Join(w, "build", "out.bin"), "ignored dir")
	writeFile(t, filepath.Join(w, "secret.pem"), "excluded by glob")
	writeFile(t, filepath.Join(dir, "main.log"), "ignored in main")

	info, err := Inspect(w)
	if err != nil {
		t.Fatal(err)
	}
	p, err := PlanTransfer(info, &DestState{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := Files(p, []string{"*.pem"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range files {
		if e.Root != dir && e.Root != w {
			t.Errorf("entry %q has unexpected root %q", e.Rel, e.Root)
		}
	}
	// Main checkout content and git metadata travel as "repo".
	for _, want := range []string{"README.md", ".git/HEAD", ".git/config", ".git/worktrees/feat/HEAD", ".git/worktrees/feat/index", ".git/worktrees/feat/gitdir", ".git/worktrees/feat/commondir"} {
		if !has(files, want) {
			t.Errorf("missing repo entry %q; have %v", want, rels(files, session.CatRepo))
		}
	}
	// Our worktree travels as "worktree", including its .git file and .gitignore.
	if diff := cmp.Diff([]string{"", ".git", ".gitignore", "README.md", "src.go"}, rels(files, session.CatWorktree)); diff != "" {
		t.Errorf("worktree entries (-want +got):\n%s", diff)
	}
	// Excluded: other worktree dir + metadata, ignored files, glob.
	for _, no := range []string{".worktrees/other", ".worktrees/other/README.md", ".git/worktrees/other/HEAD", "main.log", "debug.log", "build", "build/out.bin", "secret.pem"} {
		if has(files, no) {
			t.Errorf("entry %q must not be transferred", no)
		}
	}
	// .gitignore must be present in repo entries (transferred with repository content).
	if !has(files, ".gitignore") {
		t.Errorf("missing .gitignore in repo entries")
	}
	// includeIgnored brings the ignored files back but never other worktrees.
	files, err = Files(p, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !has(files, "debug.log") || !has(files, "build/out.bin") || has(files, ".worktrees/other/README.md") {
		t.Errorf("includeIgnored: %v", rels(files, ""))
	}
}

func TestFilesEntryMetadata(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	if err := os.Symlink("README.md", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "README.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	info, _ := Inspect(dir)
	p, _ := PlanTransfer(info, &DestState{}, nil)
	files, err := Files(p, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range files {
		switch e.Rel {
		case "link":
			if e.Symlink != "README.md" || e.Mode&os.ModeSymlink == 0 {
				t.Errorf("symlink entry = %+v", e)
			}
		case "README.md":
			if e.Mode.Perm() != 0o755 || e.Size != 6 || e.Category != session.CatRepo || e.Rewrite {
				t.Errorf("README entry = %+v", e)
			}
		}
	}
}

func TestFilesExistingMainOnlyDirtyPlusIndex(t *testing.T) {
	dir := t.TempDir()
	repo, _ := initRepo(t, dir)
	writeFile(t, filepath.Join(dir, "gone.txt"), "x")
	commitAll(t, repo, "second")
	w := addWorktree(t, dir, "feat")
	writeFile(t, filepath.Join(w, "README.md"), "modified\n")
	writeFile(t, filepath.Join(w, "new.txt"), "untracked\n")
	writeFile(t, filepath.Join(w, "staged.txt"), "staged\n")
	gitCLI(t, w, "add", "staged.txt")
	if err := os.Remove(filepath.Join(w, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	info, _ := Inspect(w)
	p, err := PlanTransfer(info, &DestState{MainExists: true, RootCommit: info.RootCommit, RefTips: map[string]string{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := Files(p, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"README.md", "new.txt", "staged.txt"}, rels(files, session.CatWorktree)); diff != "" {
		t.Errorf("dirty worktree entries (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{".git/worktrees/feat/index"}, rels(files, session.CatRepo)); diff != "" {
		t.Errorf("repo entries (-want +got):\n%s", diff)
	}
	for _, e := range files {
		if e.Category == session.CatWorktree && e.Root != w || e.Category == session.CatRepo && e.Root != dir {
			t.Errorf("bad root for %+v", e)
		}
	}
}

func TestFilesNotRepo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "notes.txt"), "plain")
	writeFile(t, filepath.Join(dir, "skip.tmp"), "excluded")
	p := &Plan{Mode: ModeNotRepo, SrcWorktree: dir, DstWorktree: dir, PackEntryID: NoEntry, IndexEntryID: NoEntry}
	files, err := Files(p, []string{"*.tmp"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"", "notes.txt"}, rels(files, session.CatWorktree)); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

// TestFilesFreshMainSkipsRelativeGitdirWorktrees: the other-worktree skip
// list is derived from linkedWorktrees, so it works for relatively linked
// worktrees too (and never silently skips a worktree whose links it failed
// to read).
func TestFilesFreshMainSkipsRelativeGitdirWorktrees(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	w := addWorktreeRelative(t, dir, "feat")
	addWorktreeRelative(t, dir, "other")
	writeFile(t, filepath.Join(w, "src.go"), "package x")

	info, err := Inspect(w)
	if err != nil {
		t.Fatal(err)
	}
	p, err := PlanTransfer(info, &DestState{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := Files(p, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, no := range []string{".worktrees/other", ".worktrees/other/README.md", ".git/worktrees/other/HEAD", ".git/worktrees/other/gitdir"} {
		if has(files, no) {
			t.Errorf("entry %q must not be transferred", no)
		}
	}
	for _, want := range []string{".git/worktrees/feat/HEAD", ".git/worktrees/feat/gitdir"} {
		if !has(files, want) {
			t.Errorf("missing repo entry %q", want)
		}
	}
}
