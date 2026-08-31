package gitx

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
)

func TestWritePackMissingObjectsOnly(t *testing.T) {
	src := t.TempDir()
	repo, root := initRepo(t, src)
	writeFile(t, filepath.Join(src, "b.txt"), "b\n")
	second := commitAll(t, repo, "second")
	writeFile(t, filepath.Join(src, "c.txt"), "c\n")
	third := commitAll(t, repo, "third")

	// Destination has only the root commit.
	dst := t.TempDir()
	initRepoAt(t, dst, src, root)

	var buf bytes.Buffer
	if err := WritePack(context.Background(), src, []string{third}, []string{root}, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected a non-empty pack")
	}
	dstRepo, err := git.PlainOpen(dst)
	if err != nil {
		t.Fatal(err)
	}
	if err := packfile.UpdateObjectStorage(dstRepo.Storer, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{second, third} {
		if _, err := dstRepo.CommitObject(plumbing.NewHash(h)); err != nil {
			t.Errorf("commit %s missing on dest after pack: %v", h[:7], err)
		}
	}
	gitCLI(t, dst, "cat-file", "-e", third) // the real git can read it too
	gitCLI(t, dst, "fsck", "--no-dangling")
}

func TestWritePackNothingMissingWritesNothing(t *testing.T) {
	src := t.TempDir()
	_, root := initRepo(t, src)
	var buf bytes.Buffer
	if err := WritePack(context.Background(), src, []string{root}, []string{root}, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected zero bytes, got %d", buf.Len())
	}
}

func TestWritePackIgnoresUnknownHaveTips(t *testing.T) {
	src := t.TempDir()
	_, root := initRepo(t, src)
	var buf bytes.Buffer
	// A destination-only commit the source has never seen must not error.
	if err := WritePack(context.Background(), src, []string{root}, []string{"1111111111111111111111111111111111111111"}, &buf); err != nil {
		t.Fatal(err)
	}
}

func TestStagedBlobsOf(t *testing.T) {
	src := t.TempDir()
	_, root := initRepo(t, src)
	writeFile(t, filepath.Join(src, "staged.txt"), "staged content\n")
	gitCLI(t, src, "add", "staged.txt")
	blobs, err := StagedBlobsOf(src, filepath.Join(src, ".git", "index"), root)
	if err != nil {
		t.Fatal(err)
	}
	want := gitCLI(t, src, "hash-object", "staged.txt")
	if len(blobs) != 1 || blobs[0] != want[:40] {
		t.Errorf("StagedBlobs = %v, want [%s]", blobs, want[:40])
	}
	// The pack for tip+staged carries the staged blob.
	var buf bytes.Buffer
	if err := WritePack(context.Background(), src, append([]string{root}, blobs...), []string{root}, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Error("pack with a staged blob must not be empty")
	}
}

func TestSourceFactsOfDivergedDestTipIsUnreachable(t *testing.T) {
	src := t.TempDir()
	repo, root := initRepo(t, src)
	writeFile(t, filepath.Join(src, "b.txt"), "b\n")
	second := commitAll(t, repo, "second")
	f, err := SourceFactsOf(src, ".git/index", second, root)
	if err != nil || !f.DestTipReachable {
		t.Fatalf("ancestor dest tip: %+v %v", f, err)
	}
	f, err = SourceFactsOf(src, ".git/index", second, "2222222222222222222222222222222222222222")
	if err != nil || f.DestTipReachable {
		t.Fatalf("unknown dest tip must be unreachable, not an error: %+v %v", f, err)
	}
}

// initRepoAt creates a destination clone of src at dir, truncated to commit
// `at` (a shared history prefix), using the git CLI (tests only).
func initRepoAt(t *testing.T, dir, src, at string) {
	t.Helper()
	gitCLI(t, filepath.Dir(dir), "clone", "-q", src, dir)
	gitCLI(t, dir, "reset", "-q", "--hard", at)
	gitCLI(t, dir, "reflog", "expire", "--expire=now", "--all")
	gitCLI(t, dir, "gc", "-q", "--prune=now")
	gitCLI(t, dir, "remote", "remove", "origin")
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
}
