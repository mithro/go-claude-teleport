// internal/orchestrate/runner.go
package orchestrate

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// EndpointFactory builds (source, destination) for the options; closeFn
// releases ssh connections. The cli supplies it; tests supply Locals.
type EndpointFactory func(ctx context.Context, o Options) (src, dst remote.Endpoint, closeFn func(), err error)

// RunJob is the detached runner's main: load the journal and plan, build
// the endpoints, run the steps, record the outcome. On failure the source
// is thawed (the freezer would also thaw on our death).
func RunJob(ctx context.Context, dataDir, jobID string, factory EndpointFactory, logf func(string, ...any)) error {
	j, ok, err := job.Open(dataDir, jobID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no job %s under %s", jobID, dataDir)
	}
	// From here on a journal exists, so every early return must leave it
	// FINISHED and failed (finding A2). internal-runner is detached and its
	// exit code is discarded; the foreground `follow` ends on Finished and
	// nothing else reports these errors, so an early return that left the
	// journal untouched hung the foreground forever and Ctrl-C then
	// claimed "the runner keeps going" about a process that had died
	// before its first step.
	failEarly := func(err error) error {
		logf("FAILED before any step ran: %v", err)
		logf("next: claude-teleport status %s | claude-teleport continue %s | claude-teleport abandon %s", jobID, jobID, jobID)
		j.Outcome, j.Finished = "failed", true
		j.UpdatedAt = time.Now()
		if serr := j.Save(); serr != nil {
			logf("saving the failed journal: %v", serr)
		}
		return err
	}
	p, err := PlanFromJournal(j)
	if err != nil {
		return failEarly(err)
	}
	src, dst, closeFn, err := factory(ctx, p.Options)
	if err != nil {
		return failEarly(err)
	}
	defer closeFn()
	j.RunnerPID = os.Getpid()
	j.Finished, j.Outcome = false, ""
	if err := j.Save(); err != nil {
		return err
	}
	logf("runner %d: job %s (%s -> %s), continuing at %s", os.Getpid(), jobID, p.SourceInfo.Hostname, p.DestInfo.Hostname, firstIncomplete(j))
	runErr := job.Run(ctx, j, Steps(p, j, src, dst, logf), logf)
	if runErr != nil {
		j.Outcome = "failed"
		if p.sourceState() == session.StateRunning && p.Session.Registry != nil {
			if terr := src.Thaw(context.Background(), p.Session.Registry.PID, p.Session.Tmux); terr != nil {
				logf("thaw after failure: %v", terr)
			} else {
				logf("thawed source claude (pid %d) after failure", p.Session.Registry.PID)
			}
		}
		if name, _ := FailedStep(j); name != "" {
			logf("FAILED at step %s: %v", name, runErr)
			logf("next: claude-teleport status %s | claude-teleport continue %s | claude-teleport abandon %s", jobID, jobID, jobID)
		}
	} else {
		j.Outcome = "success"
		logf("done: session %s is now on %s (%s)", p.Session.ID.Short(), p.DestInfo.Hostname, p.TargetState)
	}
	j.Finished = true
	j.UpdatedAt = time.Now()
	if err := j.Save(); err != nil {
		return err
	}
	_ = src.JournalPut(context.Background(), j)
	_ = dst.JournalPut(context.Background(), j)
	return runErr
}

func firstIncomplete(j *job.Journal) string {
	if name, ok := j.FirstIncomplete(); ok {
		return name
	}
	return "preflight"
}

// FailedStep names the step marked Failed, if any.
func FailedStep(j *job.Journal) (string, bool) {
	for _, s := range j.Steps {
		if s.Status == job.Failed {
			return s.Name, true
		}
	}
	return "", false
}

// ExitCode maps a finished journal to the spec §5 exit code.
func ExitCode(j *job.Journal) int {
	switch j.Outcome {
	case "success":
		return 0
	case "failed":
		if name, _ := FailedStep(j); name == "start" {
			return 5
		}
		return 1
	}
	return 1
}
