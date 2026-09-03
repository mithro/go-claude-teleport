package job

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

func TestStepPointerStabilitySurvivesResume(t *testing.T) {
	// Regression: step pointers must remain valid across resume + new appends.
	// Save a journal with 3 steps, Open it, take a pointer to step "a",
	// append 5 new steps, assert the old pointer still equals j.Step("a").
	data := t.TempDir()

	// Create and save initial journal with 3 steps.
	j1, err := New(data, sid)
	if err != nil {
		t.Fatal(err)
	}
	j1.Step("a").Status = Done
	j1.Step("b").Status = Running
	j1.Step("c").Status = Pending
	if err := j1.Save(); err != nil {
		t.Fatal(err)
	}

	// Open the saved journal (simulates resume).
	j2, ok, err := Open(data, sid)
	if err != nil || !ok {
		t.Fatalf("Open failed: ok=%v err=%v", ok, err)
	}

	// Get pointer to step "a".
	stepAPtr := j2.Step("a")
	if stepAPtr.Status != Done {
		t.Errorf("resumed step a: expected Done, got %s", stepAPtr.Status)
	}

	// Append 5 new steps (stress the capacity).
	for i := 1; i <= 5; i++ {
		name := fmt.Sprintf("step%d", i)
		j2.Step(name).Status = Running
	}

	// Assert old pointer still equals current Step("a") — no reallocation.
	if j2.Step("a") != stepAPtr {
		t.Errorf("step pointer invalidated after resume + append; pointer changed")
	}
	if stepAPtr.Status != Done {
		t.Errorf("step via old pointer: expected Done, got %s", stepAPtr.Status)
	}
}

// TestSaveMergedPreservesConcurrentOwnerProgress runs two goroutines
// against the same journal concurrently: one plays the runner — it owns a
// journal handle and repeatedly advances a step with a plain, blind
// whole-journal Save, exactly as orchestrate.RunJob does — the other
// plays the CLI process repeatedly amending RunnerPID via SaveMerged, as
// spawnAndFollow does once after spawning a runner. It asserts that once
// both finish, the runner's LAST step progress AND the CLI's RunnerPID
// amendment are BOTH present in the final journal: SaveMerged's locked
// re-read must never let it overwrite progress the runner has already
// saved, no matter how the two interleave. It also checks (per
// TestNewOpenSaveRoundTrip) that the contention leaves no .tmp file
// behind.
//
// The runner here sets the SAME RunnerPID value the CLI does — exactly as
// orchestrate.RunJob's os.Getpid() matches the pid spawnAndFollow just
// spawned. That agreement is what makes the runner's own blind Save safe
// to interleave with SaveMerged at all; see SaveMerged's doc comment for
// why amending a field the runner does NOT also set would not be safe.
func TestSaveMergedPreservesConcurrentOwnerProgress(t *testing.T) {
	data := t.TempDir()
	const runnerPID = 4242

	owner, err := New(data, sid)
	if err != nil {
		t.Fatal(err)
	}
	owner.Steps = []StepState{{Name: "start", Status: Done, Attempts: 1}}
	if err := owner.Save(); err != nil {
		t.Fatal(err)
	}

	const n = 100
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 1; i <= n; i++ {
			owner.RunnerPID = runnerPID // matches the CLI's amendment below
			owner.Step("thaw+exit").Status = Running
			owner.Step("thaw+exit").Attempts = i
			if err := owner.Save(); err != nil {
				t.Errorf("owner save %d: %v", i, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if _, err := SaveMerged(data, sid, func(m *Journal) { m.RunnerPID = runnerPID }); err != nil {
				t.Errorf("SaveMerged %d: %v", i, err)
				return
			}
		}
	}()
	wg.Wait()

	final, ok, err := Open(data, sid)
	if err != nil || !ok {
		t.Fatalf("Open: ok=%v err=%v", ok, err)
	}
	if final.RunnerPID != runnerPID {
		t.Errorf("RunnerPID = %d, want %d (SaveMerged's amendment lost)", final.RunnerPID, runnerPID)
	}
	if got := final.Step("thaw+exit"); got.Attempts != n || got.Status != Running {
		t.Errorf("thaw+exit = %+v, want Attempts=%d Status=Running (owner's last save lost to a concurrent SaveMerged)", got, n)
	}

	entries, err := os.ReadDir(final.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file left behind under contention: %s", e.Name())
		}
	}
}

// TestSaveMergedLockExcludesAnotherProcess proves the lock SaveMerged and
// Save take is a real OS-level exclusion (flock on <job dir>/.journal.lock),
// not merely an in-process mutex: a SEPARATE process holding that same
// lock file must make SaveMerged block until it releases. This matters
// because the fix depends on exactly that cross-process property —
// internal/cli's TestContinueDerivesBangMode shells out a stand-in runner
// that flock(1)s the identical path to correctly serialize against
// spawnAndFollow's own SaveMerged call, and that only works if the lock
// genuinely excludes a different process, not just other goroutines here.
func TestSaveMergedLockExcludesAnotherProcess(t *testing.T) {
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock(1) not installed")
	}
	data := t.TempDir()
	j, err := New(data, sid)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}

	// Hold .journal.lock in a separate process for a fixed window,
	// signalling (via a marker file) once it is actually held — so this
	// test only starts racing SaveMerged against a lock it KNOWS is
	// already held, never against a process that is still starting up.
	const hold = 400 * time.Millisecond
	acquired := filepath.Join(t.TempDir(), "acquired")
	ext := exec.Command("flock", lockPath(j.Dir), "-c", "touch "+acquired+" && sleep 0.4")
	if err := ext.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ext.Process.Kill(); _ = ext.Wait() })

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(acquired); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("external flock never signalled that it acquired the lock")
		}
		time.Sleep(5 * time.Millisecond)
	}

	start := time.Now()
	if _, err := SaveMerged(data, sid, func(m *Journal) { m.RunnerPID = 555 }); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < hold/2 {
		t.Errorf("SaveMerged returned after %s while a separate process held .journal.lock for %s; the lock did not exclude it", elapsed, hold)
	}
}
