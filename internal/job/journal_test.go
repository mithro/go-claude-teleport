package job

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const sid = "6f1c2a4e-9b3d-4c7a-8e5f-1a2b3c4d5e6f"

func TestNewOpenSaveRoundTrip(t *testing.T) {
	data := t.TempDir()
	if _, ok, err := Open(data, sid); err != nil || ok {
		t.Fatalf("Open before New: ok=%v err=%v", ok, err)
	}
	j, err := New(data, sid)
	if err != nil {
		t.Fatal(err)
	}
	if j.Dir != filepath.Join(data, "jobs", sid) || j.ID != sid || j.SessionID != sid {
		t.Errorf("New: %+v", j)
	}
	st, err := os.Stat(j.Dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("job dir mode = %v err=%v, want 0700", st.Mode(), err)
	}
	if j.LogPath() != filepath.Join(j.Dir, "log.txt") || j.ManifestPath() != filepath.Join(j.Dir, "manifest.json") || j.CapturePath() != filepath.Join(j.Dir, "capture.txt") {
		t.Errorf("paths: %s %s %s", j.LogPath(), j.ManifestPath(), j.CapturePath())
	}
	if StagingDir(data, sid) != filepath.Join(data, "staging", sid) {
		t.Errorf("StagingDir = %s", StagingDir(data, sid))
	}

	j.Direction = "to"
	j.SourceHost = "laptop.example"
	j.DestHost = "big-storage.example"
	j.Step("preflight").Status = Done
	j.Step("freeze").Status = Running
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(j.Dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}

	got, ok, err := Open(data, sid)
	if err != nil || !ok {
		t.Fatalf("Open: ok=%v err=%v", ok, err)
	}
	if diff := cmp.Diff(j, got); diff != "" {
		t.Errorf("round trip (-want +got):\n%s", diff)
	}
}

func TestStepFindOrAppendAndFirstIncomplete(t *testing.T) {
	j := &Journal{}
	if name, ok := j.FirstIncomplete(); ok || name != "" {
		t.Errorf("empty journal FirstIncomplete = %q,%v", name, ok)
	}
	a := j.Step("a")
	a.Status = Done
	j.Step("b").Status = Failed
	j.Step("c")
	if j.Step("a") != a || len(j.Steps) != 3 {
		t.Errorf("Step must find, not duplicate: %+v", j.Steps)
	}
	name, ok := j.FirstIncomplete()
	if !ok || name != "b" {
		t.Errorf("FirstIncomplete = %q,%v want b", name, ok)
	}
	j.Step("b").Status = Done
	j.Step("c").Status = Done
	if _, ok := j.FirstIncomplete(); ok {
		t.Errorf("all done must report none")
	}
}

func TestRunnerAlive(t *testing.T) {
	j := &Journal{RunnerPID: 4242}
	if !j.RunnerAlive(func(pid int) bool { return pid == 4242 }) {
		t.Errorf("alive runner not detected")
	}
	if j.RunnerAlive(func(pid int) bool { return false }) {
		t.Errorf("dead runner reported alive")
	}
	if (&Journal{}).RunnerAlive(func(int) bool { return true }) {
		t.Errorf("pid 0 must never be alive")
	}
}

func TestOpenMalformedIsError(t *testing.T) {
	data := t.TempDir()
	dir := Dir(data, sid)
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "job.json"), []byte("{not json"), 0o600)
	_, _, err := Open(data, sid)
	if err == nil {
		t.Fatal("malformed job.json must be an error")
	}
}
