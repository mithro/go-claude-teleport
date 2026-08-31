package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
)

func TestLocalCleanupRemovesStaging(t *testing.T) {
	p := testPaths(t)
	l := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc"})
	const jobID = "cleanup-job"
	staging := job.StagingDir(p.DataDir, jobID)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "0"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.Cleanup(context.Background(), jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Errorf("staging dir still exists after Cleanup: %v", err)
	}
	// A job that was never staged is a harmless no-op (os.RemoveAll on an
	// absent path is not an error).
	if err := l.Cleanup(context.Background(), "never-staged-job"); err != nil {
		t.Errorf("Cleanup on absent staging dir: %v", err)
	}
}

// TestLocalListSessionsScansRegistryAndProjects exercises every moving part
// of ListSessions against real fixtures: registry scan, the alive-pid
// filter (a registry row for a dead pid must not report "running"),
// walking every project dir, skipping non-.jsonl files and non-uuid
// .jsonl names, and the LastTS-descending sort.
func TestLocalListSessionsScansRegistryAndProjects(t *testing.T) {
	p := testPaths(t)
	idB := session.ID("1a2b3c4d-5e6f-4a1b-8c2d-3e4f5a6b7c8d")
	idC := session.ID("2b3c4d5e-6f7a-4b1c-9d3e-4f5a6b7c8d9e")

	writeTranscript := func(cwd string, id session.ID, branch, version, ts string) {
		proj := p.ProjectDir(cwd)
		if err := os.MkdirAll(proj, 0o700); err != nil {
			t.Fatal(err)
		}
		line := fmt.Sprintf(`{"type":"user","cwd":%q,"timestamp":%q,"gitBranch":%q,"version":%q}`+"\n", cwd, ts, branch, version)
		if err := os.WriteFile(filepath.Join(proj, string(id)+".jsonl"), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// idA is the package const `sid` (writeRegistry below is hardcoded to
	// it) and gets the middle timestamp; idB the oldest; idC the newest.
	writeTranscript("/home/alice/proj-a", session.ID(sid), "main", "2.1.247", "2026-01-02T00:00:00Z")
	writeTranscript("/home/alice/proj-b", idB, "feature-x", "2.1.0", "2026-01-01T00:00:00Z")
	writeTranscript("/home/alice/proj-c", idC, "", "2.1.5", "2026-01-03T00:00:00Z")

	// A stray non-.jsonl file and a non-uuid .jsonl file in the same
	// project dir must both be skipped, not counted or errored on.
	projA := p.ProjectDir("/home/alice/proj-a")
	if err := os.WriteFile(filepath.Join(projA, "sessions-index.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projA, "not-a-uuid.jsonl"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// idA (sid) has a live registry entry: pid 5150 is alive in this
	// ProcRoot, matching writeRegistry's hardcoded procStart "777".
	l := NewLocal(p, "x", LocalOptions{ProcRoot: fakeProcRoot(t, [][4]string{{"5150", "1", "claude", "claude\x00"}})})
	writeRegistry(t, p, 5150, "busy", "work:@1.%1")

	// idB also has a registry entry, but its pid (6161) is not in the proc
	// table at all — the alive filter must exclude it from "running".
	deadReg, err := json.Marshal(map[string]any{
		"pid": 6161, "sessionId": string(idB), "cwd": "/home/alice/proj-b",
		"procStart": "777", "version": "2.1.0", "status": "idle", "tmux": "",
		"updatedAt": time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.WriteFileAtomic(filepath.Join(p.SessionsDir(), "6161.json"), deadReg, 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := l.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d sessions (stray files must be skipped), want 3: %+v", len(out), out)
	}
	// Sorted by LastTS descending: idC (...-03) > idA/sid (...-02) > idB (...-01).
	if out[0].ID != idC || out[1].ID != session.ID(sid) || out[2].ID != idB {
		t.Fatalf("order = [%v, %v, %v], want [idC, sid, idB]", out[0].ID, out[1].ID, out[2].ID)
	}

	a := out[1]
	if a.State != "running" || a.Cwd != "/home/alice/proj-a" || a.Branch != "main" || a.Tmux != "work:@1.%1" {
		t.Errorf("idA (registry-matched, alive pid) summary = %+v", a)
	}
	b := out[2]
	if b.State != "idle" || b.Cwd != "/home/alice/proj-b" || b.Branch != "feature-x" || b.Tmux != "" {
		t.Errorf("idB (registry row for a dead pid must not count as running) summary = %+v", b)
	}
	c := out[0]
	if c.State != "idle" || c.Cwd != "/home/alice/proj-c" || c.Tmux != "" {
		t.Errorf("idC (no registry entry at all) summary = %+v", c)
	}
}
