package job

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// TailLog returns the last n lines of path ("" lines trimmed at the end).
// A missing file yields nil, nil.
func TailLog(path string, n int) ([]string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("tail %s: %w", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	if n < len(lines) {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

const followPoll = 250 * time.Millisecond

// FollowLog copies bytes appended to path to w until done() reports true
// and the file has been drained, or ctx ends.
func FollowLog(ctx context.Context, path string, w io.Writer, done func() bool) error {
	var offset int64
	drain := func() error {
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("follow %s: %w", path, err)
		}
		defer f.Close()
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return fmt.Errorf("follow %s: %w", path, err)
		}
		n, err := io.Copy(w, f)
		offset += n
		if err != nil {
			return fmt.Errorf("follow %s: %w", path, err)
		}
		return nil
	}
	ticker := time.NewTicker(followPoll)
	defer ticker.Stop()
	for {
		if err := drain(); err != nil {
			return err
		}
		if done() {
			return drain()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
