package job

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestTailLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log.txt")
	if lines, err := TailLog(p, 3); err != nil || lines != nil {
		t.Errorf("missing file: %v %v", lines, err)
	}
	os.WriteFile(p, []byte("a\nb\nc\nd\n"), 0o600)
	lines, err := TailLog(p, 3)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"b", "c", "d"}, lines); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	lines, _ = TailLog(p, 10)
	if len(lines) != 4 {
		t.Errorf("n > lines: %v", lines)
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestFollowLogUntilDone(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log.txt")
	var out syncBuf
	var finished sync.Mutex
	isDone := false
	done := func() bool { finished.Lock(); defer finished.Unlock(); return isDone }

	errc := make(chan error, 1)
	go func() { errc <- FollowLog(context.Background(), p, &out, done) }()

	time.Sleep(300 * time.Millisecond) // file does not exist yet
	os.WriteFile(p, []byte("step one\n"), 0o600)
	time.Sleep(400 * time.Millisecond)
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("step two\n")
	f.Close()
	finished.Lock()
	isDone = true
	finished.Unlock()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("FollowLog did not return after done()")
	}
	if out.String() != "step one\nstep two\n" {
		t.Errorf("followed = %q", out.String())
	}
}

func TestFollowLogContextCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := FollowLog(ctx, filepath.Join(t.TempDir(), "nope"), &bytes.Buffer{}, func() bool { return false })
	if err == nil {
		t.Fatal("expected ctx error")
	}
}
