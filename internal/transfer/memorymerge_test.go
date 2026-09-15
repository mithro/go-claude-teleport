package transfer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A diverging MEMORY.md is merged rather than left alone, because the
// memory FILES around it were copied and the index is how they are found.
// Everything else about memory handling is unchanged: the destination's
// own lines survive, and no other memory file is ever overwritten.
func TestInstallMergesTheMemoryIndex(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(memDir, "MEMORY.md")
	dstBody := "- [Alpha](alpha.md) — alpha\n"
	if err := os.WriteFile(index, []byte(dstBody), 0o644); err != nil {
		t.Fatal(err)
	}

	stagingDir := t.TempDir()
	srcBody := "- [Alpha](alpha.md) — alpha\n- [Gamma](gamma.md) — gamma\n"
	if err := os.WriteFile(StagedPath(stagingDir, 7), []byte(srcBody), 0o644); err != nil {
		t.Fatal(err)
	}

	e := Entry{ID: 7, Dst: index}
	rep := &InstallReport{}
	merged, err := mergeMemoryEntry(stagingDir, e, rep)
	if err != nil {
		t.Fatal(err)
	}
	if !merged {
		t.Fatal("the memory index was not merged")
	}
	got, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), dstBody) {
		t.Errorf("destination lines lost:\n%s", got)
	}
	if !strings.Contains(string(got), "gamma.md") {
		t.Errorf("source line not merged in:\n%s", got)
	}
	if len(rep.MemoryIndexMerged) != 1 || rep.MemoryIndexMerged[0] != index {
		t.Errorf("MemoryIndexMerged = %v, want [%s]", rep.MemoryIndexMerged, index)
	}
}

// Any OTHER diverging memory file is a prose document; a line union would
// produce nonsense, so it keeps today's behaviour exactly -- left alone
// and reported.
func TestInstallLeavesOtherMemoryFilesAlone(t *testing.T) {
	dir := t.TempDir()
	note := filepath.Join(dir, "memory", "some-note.md")
	if err := os.MkdirAll(filepath.Dir(note), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "the destination's own prose\n"
	if err := os.WriteFile(note, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	stagingDir := t.TempDir()
	if err := os.WriteFile(StagedPath(stagingDir, 3), []byte("different prose\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep := &InstallReport{}
	merged, err := mergeMemoryEntry(stagingDir, Entry{ID: 3, Dst: note}, rep)
	if err != nil {
		t.Fatal(err)
	}
	if merged {
		t.Error("a non-index memory file must not be merged")
	}
	got, _ := os.ReadFile(note)
	if string(got) != body {
		t.Errorf("destination prose was modified: %q", got)
	}
}
