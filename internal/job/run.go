package job

import (
	"context"
	"fmt"
	"time"
)

// Step is one unit of the state machine: Verify re-checks reality (done=true
// skips Run); Run does the work.
type Step struct {
	Name   string
	Verify func(ctx context.Context) (done bool, err error)
	Run    func(ctx context.Context) error
}

// Run executes steps in order, persisting state before/after each; returns
// the first error (journal marked Failed for that step).
func Run(ctx context.Context, j *Journal, steps []Step, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	for _, s := range steps {
		st := j.Step(s.Name)
		if s.Verify != nil {
			done, err := s.Verify(ctx)
			if err != nil {
				return j.fail(st, s.Name, fmt.Errorf("verify: %w", err))
			}
			if done {
				if st.Status != Done {
					logf("step %s: already satisfied", s.Name)
					st.Status = Done
					st.FinishedAt = time.Now().UTC()
					if err := j.Save(); err != nil {
						return err
					}
				} else {
					logf("step %s: verified done", s.Name)
				}
				continue
			}
		} else if st.Status == Done {
			logf("step %s: done (journal)", s.Name)
			continue
		}
		if err := ctx.Err(); err != nil {
			return j.fail(st, s.Name, err)
		}
		st.Status = Running
		st.Attempts++
		st.StartedAt = time.Now().UTC()
		st.Error = ""
		if err := j.Save(); err != nil {
			return err
		}
		logf("step %s: starting (attempt %d)", s.Name, st.Attempts)
		if err := s.Run(ctx); err != nil {
			return j.fail(st, s.Name, err)
		}
		st.Status = Done
		st.FinishedAt = time.Now().UTC()
		if err := j.Save(); err != nil {
			return err
		}
		logf("step %s: done", s.Name)
	}
	j.Finished = true
	j.Outcome = "success"
	return j.Save()
}

// fail records the step's own error text in the journal (the test asserts
// Error == "simulated crash") and returns it wrapped with the step name.
func (j *Journal) fail(st *StepState, name string, inner error) error {
	st.Status = Failed
	st.Error = inner.Error()
	st.FinishedAt = time.Now().UTC()
	j.Outcome = "failed"
	err := fmt.Errorf("step %s: %w", name, inner)
	if serr := j.Save(); serr != nil {
		return fmt.Errorf("%w (and saving the journal failed: %v)", err, serr)
	}
	return err
}
