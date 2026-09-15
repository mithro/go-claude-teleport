package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MemoryIndexName is the auto-memory index inside a project's memory/
// directory: one pointer line per memory file.
const MemoryIndexName = "MEMORY.md"

// IsMemoryIndex reports whether dst names a project's memory index.
func IsMemoryIndex(dst string) bool {
	return filepath.Base(dst) == MemoryIndexName
}

// MergeMemoryIndex adds to the memory index at path every line of src that
// it does not already carry, and returns how many it added.
//
// A diverging memory file is otherwise KEPT as the destination has it, on
// the sound principle that a project's memories belong to every session of
// that project and the destination's are as real as the source's. For the
// index that rule has a sting: memory FILES absent on the destination are
// copied, so refusing to touch MEMORY.md leaves those new files on disk
// and listed nowhere -- and the index is how they are found. Merging is
// what makes them reachable.
//
// It only ever adds. Destination lines, and their order, are untouched;
// duplicates are not introduced; and a merge with nothing to add does not
// rewrite the file at all. Nothing here can lose a memory: the worst case
// is an index listing something twice, and the best is the copied files
// being usable.
func MergeMemoryIndex(path string, src []byte) (int, error) {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("read memory index %s: %w", path, err)
	}

	have := map[string]bool{}
	for _, l := range strings.Split(string(existing), "\n") {
		if t := strings.TrimSpace(l); t != "" {
			have[t] = true
		}
	}

	var add []string
	for _, l := range strings.Split(string(src), "\n") {
		t := strings.TrimSpace(l)
		if t == "" || have[t] {
			continue
		}
		have[t] = true // a source repeating itself adds one line, not two
		add = append(add, strings.TrimRight(l, "\r\n"))
	}
	if len(add) == 0 {
		return 0, nil
	}

	var b strings.Builder
	b.Write(existing)
	// Never run the first appended line onto an unterminated last one.
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		b.WriteString("\n")
	}
	for _, l := range add {
		b.WriteString(l)
		b.WriteString("\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("memory index dir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return 0, fmt.Errorf("write memory index %s: %w", path, err)
	}
	return len(add), nil
}
