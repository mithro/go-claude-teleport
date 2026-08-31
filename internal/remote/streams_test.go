package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

func TestPipeStreamPropagatesRunError(t *testing.T) {
	s := PipeStream(func(r io.Reader, w io.Writer) error { io.Copy(io.Discard, r); return io.ErrUnexpectedEOF })
	s.Write([]byte("x"))
	if err := s.Close(); err == nil {
		t.Fatal("Close must return the run error")
	}
}

func TestTarStreamSourceToDestInProcess(t *testing.T) {
	src, dst := testPaths(t), testPaths(t)
	jobID := sid
	// One session file on the source. The transcript's "cwd" must literally
	// be prefixed by src.Home (the mapping's From) for RewriteJSON to touch
	// it — session.PathMap.Apply only rewrites a literal prefix match, so a
	// hardcoded "/home/alice/x" that no mapping rooted in a t.TempDir()
	// could ever match would silently pass through unrewritten (see the
	// identical note on sourceManifest in local_test.go).
	cwd := filepath.Join(src.Home, "x")
	proj := src.ProjectDir(cwd)
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(`{"cwd":"`+cwd+`"}`+"\n"), 0o600)
	files := []session.FileEntry{{Root: src.ConfigDir, Rel: "projects/" + session.Munge(cwd) + "/" + sid + ".jsonl", Category: session.CatSession, Size: 24, Mode: 0o600, Rewrite: true}}
	srcEP := NewLocal(src, "x", LocalOptions{ProcRoot: "/proc"})
	dstEP := NewLocal(dst, "x", LocalOptions{ProcRoot: "/proc"})
	pm := session.NewPathMap(session.Mapping{From: src.Home, To: dst.Home})
	m, err := srcEP.BuildManifest(context.Background(), jobID, session.ID(sid), "laptop.example", "big-storage.example", files, pm)
	if err != nil {
		t.Fatal(err)
	}
	st, err := dstEP.ManifestDiff(context.Background(), m, jobID)
	if err != nil {
		t.Fatal(err)
	}
	// Both journals carry the statuses in the plan projection.
	for _, ep := range []Endpoint{srcEP, dstEP} {
		j, err := job.New(ep.Paths().DataDir, jobID)
		if err != nil {
			t.Fatal(err)
		}
		j.Plan, _ = json.Marshal(map[string]any{"statuses": st})
		if err := ep.JournalPut(context.Background(), j); err != nil {
			t.Fatal(err)
		}
	}
	r, err := srcEP.OpenStream(context.Background(), StreamTar, jobID, "send:1")
	if err != nil {
		t.Fatal(err)
	}
	w, err := dstEP.OpenStream(context.Background(), StreamTar, jobID, "recv:1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(w, r); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// The lone file entry is manifest entry 0 (transfer.Build assigns IDs by
	// slice index, 0-based).
	staged := filepath.Join(job.StagingDir(dst.DataDir, jobID), "0")
	b, err := os.ReadFile(staged)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(dst.Home)) {
		t.Errorf("staged content not rewritten: %q", b)
	}
	st2, _ := dstEP.ManifestDiff(context.Background(), m, jobID)
	if st2[0] != transfer.StagedSame {
		t.Errorf("after receive status = %v, want staged-same", st2[0])
	}
}

func TestPackStreamRecvWritesObjectsPack(t *testing.T) {
	dst := testPaths(t)
	dstEP := NewLocal(dst, "x", LocalOptions{ProcRoot: "/proc"})
	w, err := dstEP.OpenStream(context.Background(), StreamPack, sid, "recv:1")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("PACKdata"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(job.StagingDir(dst.DataDir, sid), "objects.pack"))
	if err != nil || string(b) != "PACKdata" {
		t.Errorf("objects.pack = %q %v", b, err)
	}
}
