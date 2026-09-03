// internal/orchestrate/runner_test.go
package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/remote"
)

func TestExitCodeMapping(t *testing.T) {
	j := &job.Journal{Outcome: "success", Finished: true}
	if ExitCode(j) != 0 {
		t.Error("success -> 0")
	}
	j = &job.Journal{Outcome: "failed", Finished: true, Steps: []job.StepState{{Name: "transfer", Status: job.Failed}}}
	if ExitCode(j) != 1 {
		t.Error("failed transfer -> 1")
	}
	j = &job.Journal{Outcome: "failed", Finished: true, Steps: []job.StepState{{Name: "start", Status: job.Failed, Error: "Not logged in"}}}
	if ExitCode(j) != 5 {
		t.Error("failed start -> 5")
	}
	if name, ok := FailedStep(j); !ok || name != "start" {
		t.Error("FailedStep")
	}
}

func TestRunJobMarksOutcomeAndThawsOnFailure(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := src.paths.Home + "/x"
	seedSession(t, src, cwd)
	o := baseOptions()
	o.State = "idle"
	p, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatal(err)
	}
	j, err := job.New(src.paths.DataDir, sid)
	if err != nil {
		t.Fatal(err)
	}
	j.Plan, _ = p.ToJSON()
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("simulated dropped connection")
	factory := func(context.Context, Options) (remote.Endpoint, remote.Endpoint, func(), error) {
		return src.ep, &failingEndpoint{Endpoint: dst.ep, failOpenStream: boom}, func() {}, nil
	}
	err = RunJob(context.Background(), src.paths.DataDir, sid, factory, t.Logf)
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("RunJob err = %v, want the stream failure", err)
	}
	j2, _, _ := job.Open(src.paths.DataDir, sid)
	if j2.Outcome != "failed" || !j2.Finished {
		t.Errorf("journal after failure = %+v", j2)
	}
	if name, _ := FailedStep(j2); name != "transfer" {
		t.Errorf("failed step = %q", name)
	}
	// continue: a factory without the fault finishes the job.
	factory = func(context.Context, Options) (remote.Endpoint, remote.Endpoint, func(), error) {
		return src.ep, dst.ep, func() {}, nil
	}
	if err := RunJob(context.Background(), src.paths.DataDir, sid, factory, t.Logf); err != nil {
		t.Fatal(err)
	}
	j3, _, _ := job.Open(src.paths.DataDir, sid)
	if j3.Outcome != "success" || ExitCode(j3) != 0 {
		t.Errorf("journal after continue = %+v", j3)
	}
}

// failingEndpoint wraps an Endpoint and fails OpenStream once.
type failingEndpoint struct {
	remote.Endpoint
	failOpenStream error
}

func (f *failingEndpoint) OpenStream(ctx context.Context, kind remote.StreamKind, jobID, streamID string) (io.ReadWriteCloser, error) {
	if f.failOpenStream != nil {
		err := f.failOpenStream
		f.failOpenStream = nil
		return nil, err
	}
	return f.Endpoint.OpenStream(ctx, kind, jobID, streamID)
}

// TestRunJobMarksTheJournalOnEarlyFailure pins the runner half of finding
// A2: RunJob's early returns (a plan that will not decode, a factory that
// cannot dial) happen before the journal is ever marked, and
// internal-runner is detached — nothing reports them. The foreground
// `follow` ends on Finished, so an unmarked journal hangs it forever.
func TestRunJobMarksTheJournalOnEarlyFailure(t *testing.T) {
	boom := errors.New("dial big-storage.example: connection refused")
	for _, tc := range []struct {
		name    string
		plan    string
		factory EndpointFactory
		want    string
	}{
		{
			name: "factory cannot dial",
			plan: `{"options":{"direction":"to","target":"big-storage.example"}}`,
			factory: func(context.Context, Options) (remote.Endpoint, remote.Endpoint, func(), error) {
				return nil, nil, nil, boom
			},
			want: "connection refused",
		},
		{
			name: "stored plan will not decode",
			plan: `{"options":"not an object"}`,
			factory: func(context.Context, Options) (remote.Endpoint, remote.Endpoint, func(), error) {
				panic("must not dial")
			},
			want: "decode plan",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), "share", "claude-teleport")
			j, err := job.New(dataDir, sid)
			if err != nil {
				t.Fatal(err)
			}
			j.Plan = json.RawMessage(tc.plan)
			if err := j.Save(); err != nil {
				t.Fatal(err)
			}
			var logged strings.Builder
			logf := func(f string, v ...any) { fmt.Fprintf(&logged, f+"\n", v...) }
			err = RunJob(context.Background(), dataDir, sid, tc.factory, logf)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RunJob err = %v, want one mentioning %q", err, tc.want)
			}
			j2, ok, oerr := job.Open(dataDir, sid)
			if oerr != nil || !ok {
				t.Fatalf("job.Open = %v %v", ok, oerr)
			}
			if !j2.Finished || j2.Outcome != "failed" {
				t.Errorf("journal after an early failure = finished %v outcome %q, want a finished failure (the foreground would hang)", j2.Finished, j2.Outcome)
			}
			if !strings.Contains(logged.String(), tc.want) {
				t.Errorf("the reason never reached log.txt:\n%s", logged.String())
			}
		})
	}
}

// TestRunJobRoutesPostDialSaveFailureThroughFailEarly covers the wave A
// re-review minor: RunJob's post-dial j.Save() (RunnerPID/Finished/Outcome
// reset, before any step runs) is an early return exactly like the two
// cases in TestRunJobMarksTheJournalOnEarlyFailure above, so it too must go
// through failEarly rather than a bare `return err` — otherwise the
// journal is left looking in-progress and `follow` hangs forever (finding
// A2). j.Dir is made read-only only AFTER the setup Save above succeeds,
// so job.Save's internal os.CreateTemp fails on exactly RunJob's first
// internal save attempt (the post-dial one), not the setup write; that
// same unwritable directory also makes failEarly's own recovery Save fail
// (swallowed, logged, not returned) — a case failEarly already tolerates —
// so this test checks the FAILED-before-any-step-ran banner in the log
// rather than the on-disk journal, which this specific failure mode can
// never actually persist.
func TestRunJobRoutesPostDialSaveFailureThroughFailEarly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permission bits do not gate writes")
	}
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := src.paths.Home + "/x"
	seedSession(t, src, cwd)
	o := baseOptions()
	o.State = "idle"
	p, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatal(err)
	}
	j, err := job.New(src.paths.DataDir, sid)
	if err != nil {
		t.Fatal(err)
	}
	j.Plan, _ = p.ToJSON()
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(j.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(j.Dir, 0o700) // let TempDir cleanup remove it either way

	factory := func(context.Context, Options) (remote.Endpoint, remote.Endpoint, func(), error) {
		return src.ep, dst.ep, func() {}, nil
	}
	var logged strings.Builder
	logf := func(f string, v ...any) { fmt.Fprintf(&logged, f+"\n", v...) }
	err = RunJob(context.Background(), src.paths.DataDir, sid, factory, logf)
	if err == nil || !strings.Contains(err.Error(), "save journal") {
		t.Fatalf("RunJob err = %v, want the post-dial save failure", err)
	}
	if !strings.Contains(logged.String(), "FAILED before any step ran") {
		t.Errorf("post-dial save failure was not routed through failEarly:\n%s", logged.String())
	}
}
