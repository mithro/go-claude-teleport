// Package job is the teleport job journal (jobs/<sid>/job.json), its log
// and the resumable step runner (spec §6).
package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type StepStatus string

const (
	Pending StepStatus = "pending"
	Running StepStatus = "running"
	Done    StepStatus = "done"
	Failed  StepStatus = "failed"
)

type StepState struct {
	Name       string     `json:"name"`
	Status     StepStatus `json:"status"`
	StartedAt  time.Time  `json:"started_at,omitempty"`
	FinishedAt time.Time  `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
	Attempts   int        `json:"attempts"`
}

type Journal struct {
	ID         string          `json:"id"` // == session id
	SessionID  string          `json:"session_id"`
	Direction  string          `json:"direction"` // "to" | "from"
	SourceHost string          `json:"source_host"`
	DestHost   string          `json:"dest_host"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Plan       json.RawMessage `json:"plan"` // orchestrate.Plan (opaque here)
	Steps      []StepState     `json:"steps"`
	Finished   bool            `json:"finished"`
	Outcome    string          `json:"outcome"` // "" | "success" | "failed" | "abandoned"
	RunnerPID  int             `json:"runner_pid"`
	Dir        string          `json:"-"`
}

// Dir is <dataDir>/jobs/<id>.
func Dir(dataDir, id string) string { return filepath.Join(dataDir, "jobs", id) }

// StagingDir is <dataDir>/staging/<id>.
func StagingDir(dataDir, id string) string { return filepath.Join(dataDir, "staging", id) }

func journalPath(dir string) string { return filepath.Join(dir, "job.json") }

// Open loads jobs/<id>/job.json if it exists.
// Ensures Steps capacity has 64-step headroom to maintain pointer stability when resumed.
func Open(dataDir, id string) (*Journal, bool, error) {
	dir := Dir(dataDir, id)
	raw, err := os.ReadFile(journalPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open journal %s: %w", journalPath(dir), err)
	}
	var j Journal
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, false, fmt.Errorf("parse journal %s: %w", journalPath(dir), err)
	}
	j.Dir = dir
	// Re-slice Steps to have 64-step headroom for resume safety.
	// json.Unmarshal produces tight cap≈len; appending after resume would
	// reallocate and invalidate earlier *StepState pointers from Step().
	if len(j.Steps) > 0 {
		grown := make([]StepState, len(j.Steps), len(j.Steps)+64)
		copy(grown, j.Steps)
		j.Steps = grown
	}
	return &j, true, nil
}

// New creates the job directory (0700) and an empty journal (not yet saved).
// Steps are pre-allocated with 64-step headroom (see Step() invariant).
func New(dataDir, id string) (*Journal, error) {
	dir := Dir(dataDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create job dir %s: %w", dir, err)
	}
	now := time.Now().UTC()
	return &Journal{
		ID: id, SessionID: id, CreatedAt: now, UpdatedAt: now, Dir: dir,
		Plan:  json.RawMessage("null"),
		Steps: make([]StepState, 0, 64),
	}, nil
}

// lockPath is the advisory lock every Save (and SaveMerged) holds for its
// critical section. Tests that stand in for a real runner by writing
// job.json directly (bypassing Save) must flock this same path around
// their write to stay correctly serialized against the app process — see
// SaveMerged.
func lockPath(dir string) string { return filepath.Join(dir, ".journal.lock") }

// withLock runs fn with an exclusive advisory lock (flock) held on
// <dir>/.journal.lock, serializing it against any other process's Save or
// SaveMerged on the same job — including a runner and the CLI process
// that spawned it recording that runner's pid, which otherwise race with
// no ordering guarantee at all.
func withLock(dir string, fn func() error) error {
	lf, err := os.OpenFile(lockPath(dir), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("lock journal %s: %w", dir, err)
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock journal %s: %w", dir, err)
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	return fn()
}

// Save writes job.json atomically (temp file + rename) and bumps UpdatedAt,
// under the per-job lock (see withLock).
func (j *Journal) Save() error {
	if j.Dir == "" {
		return errors.New("journal has no Dir")
	}
	return withLock(j.Dir, j.saveLocked)
}

// saveLocked is Save's body, run by both Save and SaveMerged from inside
// withLock — never call it without the lock already held.
func (j *Journal) saveLocked() error {
	j.UpdatedAt = time.Now().UTC()
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return fmt.Errorf("encode journal: %w", err)
	}
	tmp, err := os.CreateTemp(j.Dir, "job.json.*.tmp")
	if err != nil {
		return fmt.Errorf("save journal %s: %w", j.Dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("save journal %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("save journal %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("save journal %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("save journal %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, journalPath(j.Dir)); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("save journal %s: %w", journalPath(j.Dir), err)
	}
	return nil
}

// SaveMerged locks dataDir/jobs/id, re-reads the journal fresh from disk,
// applies mutate to that fresh copy and saves it — all inside the one
// critical section. Use it instead of Open-then-Save whenever the caller
// is not the process that owns the journal's progress (e.g. the CLI
// process recording the pid of a runner it just spawned): a plain
// Open-then-Save can read the journal, have the runner concurrently save
// its own progress, and then overwrite that progress with the caller's
// now-stale snapshot. Under CPU contention this is not merely
// theoretical — it reproduces reliably (see internal/cli spawnAndFollow).
func SaveMerged(dataDir, id string, mutate func(*Journal)) (*Journal, error) {
	dir := Dir(dataDir, id)
	var out *Journal
	err := withLock(dir, func() error {
		jj, ok, err := Open(dataDir, id)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no journal at %s", journalPath(dir))
		}
		mutate(jj)
		if err := jj.saveLocked(); err != nil {
			return err
		}
		out = jj
		return nil
	})
	return out, err
}

func (j *Journal) LogPath() string      { return filepath.Join(j.Dir, "log.txt") }
func (j *Journal) ManifestPath() string { return filepath.Join(j.Dir, "manifest.json") }
func (j *Journal) CapturePath() string  { return filepath.Join(j.Dir, "capture.txt") }

// Step finds the named step or appends a Pending one.
// Invariant: returned *StepState pointers remain valid while total steps ≤ cap(Steps).
// Open() and New() ensure 64-step headroom; with spec's fixed 10-step plan, pointers
// survive any reasonable resume path without reallocation.
func (j *Journal) Step(name string) *StepState {
	// Lazy-initialize capacity on fresh Journal.
	if cap(j.Steps) == 0 {
		j.Steps = make([]StepState, 0, 64)
	}
	for i := range j.Steps {
		if j.Steps[i].Name == name {
			return &j.Steps[i]
		}
	}
	j.Steps = append(j.Steps, StepState{Name: name, Status: Pending})
	return &j.Steps[len(j.Steps)-1]
}

// FirstIncomplete names the first step that is not Done.
func (j *Journal) FirstIncomplete() (string, bool) {
	for _, s := range j.Steps {
		if s.Status != Done {
			return s.Name, true
		}
	}
	return "", false
}

// RunnerAlive reports whether the recorded runner pid is alive per alive().
func (j *Journal) RunnerAlive(alive func(pid int) bool) bool {
	return j.RunnerPID > 0 && alive(j.RunnerPID)
}
