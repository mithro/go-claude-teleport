package session

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
)

// scanLines opens a transcript file and calls fn for each non-blank JSONL line.
// Errors during file opening or scanning are wrapped with the path; parse
// failures within fn are the caller's responsibility and may be skipped there.
func scanLines(path string, fn func(line []byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), scannerBuf)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("scan %s: %w", path, err)
	}
	return nil
}
