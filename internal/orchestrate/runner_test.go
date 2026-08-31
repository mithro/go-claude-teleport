// internal/orchestrate/runner_test.go
package orchestrate

import (
	"context"
	"errors"
	"io"
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
	err = RunJob(context.Background(), src.paths.DataDir, sid, factory, selfExe(t), t.Logf)
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
	if err := RunJob(context.Background(), src.paths.DataDir, sid, factory, selfExe(t), t.Logf); err != nil {
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
