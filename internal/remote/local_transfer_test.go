package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// TestLocalDeleteInstalledOnlyNamedIDs covers abandon's destination-side
// deletion (Task 23, ruling E): DeleteInstalled must remove only the
// manifest entries named by ids, never an entry that happens to match the
// manifest hash but was not named — the id list is how abandon restricts
// deletion to files THIS job actually installed (preflight status Absent).
func TestLocalDeleteInstalledOnlyNamedIDs(t *testing.T) {
	p := testPaths(t)
	l := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc"})
	dir := filepath.Join(p.ConfigDir, "projects", "-home-alice-proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "keep.jsonl")
	gone := filepath.Join(dir, "gone.jsonl")
	if err := os.WriteFile(keep, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gone, []byte("gone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sha := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	m := &transfer.Manifest{Version: 1, JobID: sid, SessionID: sid, Entries: []transfer.Entry{
		{ID: 0, Category: session.CatSession, Dst: dir, Mode: uint32(os.ModeDir | 0o700)},
		{ID: 1, Category: session.CatSession, Dst: keep, Size: 5, Mode: 0o600, SHA256: sha("keep\n")},
		{ID: 2, Category: session.CatSession, Dst: gone, Size: 5, Mode: 0o600, SHA256: sha("gone\n")},
	}}
	// Both files must have been installed BY THIS JOB for deletion to be
	// possible at all (ruling R-P3-B1f N3), so the fixture installs them
	// for real — writing the destination's own jobs/<id>/installed.json —
	// instead of just dropping matching bytes on disk.
	staging := job.StagingDir(p.DataDir, sid)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	for id, content := range map[int]string{1: "keep\n", 2: "gone\n"} {
		if err := os.WriteFile(transfer.StagedPath(staging, id), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(keep); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Install(context.Background(), m, sid); err != nil {
		t.Fatal(err)
	}
	deleted, err := l.DeleteInstalled(context.Background(), m, []int{2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("named entry must be removed")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("unnamed entry must survive: %v", err)
	}
	var found bool
	for _, d := range deleted {
		if d == gone {
			found = true
		}
	}
	if !found {
		t.Errorf("deleted = %v, want it to include %s", deleted, gone)
	}
}

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
	//
	// Both idA and idC also have a placeholder pane. idA's must be
	// ignored — a live registry entry means the session is running, and
	// the pane it runs in can legitimately still show a placeholder argv
	// mid-handover — while idC's is the only evidence it exists at all,
	// so idC is the suspended one (same precedence session.Load applies).
	probe := &paneProbe{
		socket: "/run/tmux/default",
		panes: map[string][]string{
			"%1": {"claude-teleport", "placeholder", "--resume", sid},
			"%3": {"claude-teleport", "placeholder", "--resume", string(idC)},
		},
		infos: []session.PaneInfo{
			{Session: "work", WindowID: "@1", PaneID: "%1"},
			{Session: "work", WindowID: "@3", PaneID: "%3"},
		},
	}
	l := NewLocal(p, "x", LocalOptions{
		ProcRoot: fakeProcRoot(t, [][4]string{{"5150", "1", "claude", "claude\x00"}}),
		Probe:    probe,
	})
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
	if c.State != "suspended" || c.Cwd != "/home/alice/proj-c" || c.Tmux != "work:@3.%3" {
		t.Errorf("idC (no registry entry, held by a placeholder pane) summary = %+v", c)
	}
}
