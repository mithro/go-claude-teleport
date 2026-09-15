package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// MEMORY.md is an INDEX: one pointer line per memory file. Keeping the
// destination's copy unchanged (what a diverging memory file otherwise
// gets) leaves every memory file the teleport just copied present on disk
// but listed nowhere -- unreachable, because the index is how they are
// found. Merging is the only outcome where the copied files are usable.
//
// The merge only ever ADDS. The destination's own lines, order included,
// are never touched: its memories are as real as the source's.
func TestMergeMemoryIndexAddsSourceLinesOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MEMORY.md")
	dstBody := "- [Alpha](alpha.md) — the alpha hook\n- [Beta](beta.md) — the beta hook\n"
	if err := os.WriteFile(path, []byte(dstBody), 0o644); err != nil {
		t.Fatal(err)
	}
	srcBody := "- [Beta](beta.md) — the beta hook\n- [Gamma](gamma.md) — the gamma hook\n"

	added, err := MergeMemoryIndex(path, []byte(srcBody))
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Errorf("added = %d, want 1 (only Gamma is new)", added)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	if !strings.HasPrefix(out, dstBody) {
		t.Errorf("destination lines were altered:\n%s", out)
	}
	for _, want := range []string{"alpha.md", "beta.md", "gamma.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("%s missing from merged index:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "beta.md"); n != 1 {
		t.Errorf("beta.md appears %d times, want 1 (no duplicates)", n)
	}
}

// Nothing new to add must leave the file byte-identical, so a teleport
// that changes nothing writes nothing.
func TestMergeMemoryIndexNoopWhenSourceAddsNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MEMORY.md")
	body := "- [Alpha](alpha.md) — hook\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	added, err := MergeMemoryIndex(path, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 {
		t.Errorf("added = %d, want 0", added)
	}
	got, _ := os.ReadFile(path)
	if string(got) != body {
		t.Errorf("file changed: %q", got)
	}
	if after, _ := os.Stat(path); !after.ModTime().Equal(before.ModTime()) {
		t.Error("file was rewritten despite having nothing to add")
	}
}

// A destination with no index yet gets the source's outright.
func TestMergeMemoryIndexCreatesWhenAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "MEMORY.md")
	src := "- [Alpha](alpha.md) — hook\n"
	added, err := MergeMemoryIndex(path, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Errorf("added = %d, want 1", added)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != src {
		t.Errorf("got %q, want %q", got, src)
	}
}

// A destination file with no trailing newline must not have the first
// appended line run onto the end of its last one.
func TestMergeMemoryIndexHandlesMissingTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "MEMORY.md")
	if err := os.WriteFile(path, []byte("- [Alpha](alpha.md) — hook"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := MergeMemoryIndex(path, []byte("- [Beta](beta.md) — hook\n")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "hook- [Beta]") {
		t.Errorf("lines were run together: %q", got)
	}
	if n := strings.Count(string(got), "\n- [Beta]"); n != 1 {
		t.Errorf("Beta not on its own line: %q", got)
	}
}
