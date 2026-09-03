// internal/cli/teleport.go
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/orchestrate"
	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// parseMaps parses --map SRC=DST specs. It is a thin name for
// session.ParseMappings — do not duplicate its absolute-path/duplicate-From
// validation here.
func parseMaps(specs []string) ([]session.Mapping, error) { return session.ParseMappings(specs) }

// parseSSHOptions parses -o KEY=VALUE specs into Options.SSHOptions.
func parseSSHOptions(specs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, s := range specs {
		k, v, ok := strings.Cut(s, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("-o %q: want KEY=VALUE", s)
		}
		out[k] = v
	}
	return out, nil
}

// teleportOptions turns flags root.go's tf.validate already checked (exactly
// one of --to/--from, --state, --no-tmux/--state, --map syntax, arg count)
// into orchestrate.Options.
func (a *app) teleportOptions(f teleportFlags, args []string) (orchestrate.Options, error) {
	maps, err := parseMaps(f.Maps)
	if err != nil {
		return orchestrate.Options{}, err
	}
	sshOpts, err := parseSSHOptions(f.SSHOptions)
	if err != nil {
		return orchestrate.Options{}, err
	}
	sel, err := session.ParseSelector(args, a.selectorEnv())
	if err != nil {
		return orchestrate.Options{}, err
	}
	o := orchestrate.Options{
		Direction: "to", Target: f.To, Selector: sel, DestPath: f.DestPath, Maps: maps, State: f.State,
		AllowDrift: f.AllowDrift, Force: f.Force, TmuxSocket: f.TmuxSocket, NoTmux: f.NoTmux, Excludes: f.Excludes,
		IncludeIgnored: f.IncludeIgnored, ExitTimeout: f.ExitTimeout, StartTimeout: f.StartTimeout, Via: f.Via, SSHOptions: sshOpts,
	}
	if f.From != "" {
		o.Direction, o.Target = "from", f.From
	}
	if f.DestPath != "" && !filepath.IsAbs(f.DestPath) {
		return orchestrate.Options{}, fmt.Errorf("--dest-path %q must be absolute", f.DestPath)
	}
	return o, nil
}

// runTeleport is the root command (spec §5, §6).
func (a *app) runTeleport(ctx context.Context, f teleportFlags, args []string) int {
	if err := a.ensurePaths(); err != nil {
		return a.fail(err)
	}
	o, err := a.teleportOptions(f, args)
	if err != nil {
		fmt.Fprintln(a.stderr, "usage:", err)
		return ExitUsage
	}
	return a.teleport(ctx, o, f.DryRun)
}

// teleport is runTeleport's body once the flags are parsed: endpoints,
// session resolution, !-mode detection, the existing-job branch, preflight,
// the plan render and the dry-run gate, then the detached runner. Tests
// drive it directly for the Options no flag can express (LocalDest) instead
// of duplicating this sequence (finding A16).
func (a *app) teleport(ctx context.Context, o orchestrate.Options, dryRun bool) int {
	src, dst, closeFn, err := a.endpoints(ctx, o)
	if err != nil {
		return a.fail(err)
	}
	sess, err := src.ResolveSession(ctx, o.Selector)
	if err != nil {
		closeFn()
		return a.fail(err)
	}
	jobID := string(sess.ID)
	if bangMode(a.env, sess.Registry) {
		if o.Direction != "to" {
			closeFn()
			fmt.Fprintln(a.stderr, "usage: running inside the session being moved (!-mode) only works with --to")
			return ExitUsage
		}
		o.BangMode = true
	}
	if j, ok, err := job.Open(a.paths.DataDir, jobID); err != nil {
		closeFn()
		return a.fail(err)
	} else if ok && j.Outcome != "success" && j.Outcome != "abandoned" {
		closeFn()
		// --dry-run is evaluated FIRST here (finding A1): continuing the
		// job spawns a runner that freezes the live Claude and moves it
		// for real, which is exactly what --dry-run promises never to do.
		if dryRun {
			a.renderStoredPlan(j)
			fmt.Fprintf(a.stdout, "job %s already exists (%s at step %s, destination %s); dry run: nothing was moved. Continue it with: claude-teleport continue %s\n",
				sess.ID.Short(), stateWord(j), firstIncompleteName(j), storedDest(j), j.ID)
			return ExitOK
		}
		fmt.Fprintf(a.stdout, "job %s is %s at step %s; continuing it to %s (use `abandon` to start over)\n", sess.ID.Short(), stateWord(j), firstIncompleteName(j), storedDest(j))
		return a.continueJob(ctx, j, o.BangMode)
	}
	plan, err := orchestrate.Preflight(ctx, o, src, dst, jobID)
	if err != nil {
		closeFn()
		return a.fail(err)
	}
	plan.Render(a.stdout)
	if dryRun {
		closeFn()
		fmt.Fprintln(a.stdout, "dry run: nothing was moved")
		return ExitOK
	}
	j, err := job.New(a.paths.DataDir, jobID)
	if err != nil {
		closeFn()
		return a.fail(err)
	}
	j.SessionID, j.Direction = jobID, o.Direction
	j.SourceHost, j.DestHost = plan.SourceInfo.Hostname, plan.DestInfo.Hostname
	j.CreatedAt, j.UpdatedAt = time.Now(), time.Now()
	if j.Plan, err = plan.ToJSON(); err != nil {
		closeFn()
		return a.fail(err)
	}
	if err := j.Save(); err != nil {
		closeFn()
		return a.fail(err)
	}
	closeFn() // the detached runner re-dials for itself
	return a.spawnAndFollow(ctx, j, o.BangMode)
}

// bangMode reports whether this invocation runs INSIDE the session being
// moved (spec §6.3, Options.BangMode). Only internal/cli reads $CLAUDE_PID,
// and a match requires both the pid AND the session's own live registry
// entry naming it, so a stale or unrelated CLAUDE_PID can never trip it.
func bangMode(env map[string]string, reg *session.Registry) bool {
	pid, err := strconv.Atoi(env["CLAUDE_PID"])
	return err == nil && pid != 0 && reg != nil && reg.PID == pid
}

// continueBang derives !-mode for `continue` exactly as runTeleport does
// (finding A3): the job must have been started in !-mode AND this process
// must again be the session's own Claude. Without it the foreground
// follows to Finished, the source Claude never returns to its prompt and
// step thaw+exit burns the whole exit timeout with both Claudes alive.
func (a *app) continueBang(j *job.Journal) bool {
	p, err := orchestrate.PlanFromJournal(j)
	if err != nil || p.Session == nil || !p.Options.BangMode {
		return false
	}
	return bangMode(a.env, p.Session.Registry)
}

func stateWord(j *job.Journal) string {
	if j.Outcome == "failed" {
		return "failed"
	}
	return "in progress"
}

// storedDest names the destination the STORED job moves the session to —
// the only one a continue can use. Re-running the teleport with a different
// --to does not retarget an existing job, so every message about continuing
// one has to say where it actually goes (finding A15).
func storedDest(j *job.Journal) string {
	if j.DestHost != "" {
		return j.DestHost
	}
	if p, err := orchestrate.PlanFromJournal(j); err == nil {
		if p.DestInfo.Hostname != "" {
			return p.DestInfo.Hostname
		}
		if p.Options.Target != "" {
			return p.Options.Target
		}
	}
	return "(unrecorded)"
}

// renderStoredPlan prints the plan a stored job carries, so `--dry-run`
// over an existing job still shows what the continue would do.
func (a *app) renderStoredPlan(j *job.Journal) {
	p, err := orchestrate.PlanFromJournal(j)
	if err != nil || p.Session == nil {
		return
	}
	p.Render(a.stdout)
}

func firstIncompleteName(j *job.Journal) string {
	if n, ok := j.FirstIncomplete(); ok {
		return n
	}
	return "preflight"
}

// fail prints err and maps it to a spec §5 exit code. *ExitError (e.g. from
// dialTarget, ensurePaths) already carries the right code; the orchestrate
// sentinel error types are mapped explicitly; anything else is ExitFailed.
func (a *app) fail(err error) int {
	var ee *ExitError
	if errors.As(err, &ee) {
		fmt.Fprintln(a.stderr, "claude-teleport:", ee.Err)
		return ee.Code
	}
	var re *orchestrate.RefusedError
	var ue *orchestrate.UnreachableError
	switch {
	case errors.As(err, &re):
		fmt.Fprintln(a.stderr, "claude-teleport:", err)
		return ExitRefused
	case errors.As(err, &ue):
		fmt.Fprintln(a.stderr, "claude-teleport:", err)
		return ExitUnreachable
	case errors.Is(err, session.ErrNotFound):
		fmt.Fprintln(a.stderr, "claude-teleport:", err)
		return ExitUsage
	}
	fmt.Fprintln(a.stderr, "claude-teleport:", err)
	return ExitFailed
}

// spawnAndFollow starts the detached runner and follows its log.
func (a *app) spawnAndFollow(ctx context.Context, j *job.Journal, bang bool) int {
	// Snapshot thaw+exit BEFORE the new runner can touch it: in !-mode
	// follow returns as soon as that step starts, and the journal may
	// still carry the REPLACED run's status for it (carry 2). Only a
	// change made by the runner we are about to start counts as evidence.
	base := stepState(j, "thaw+exit")
	pid, err := procx.SpawnDetached([]string{a.selfExe, "internal-runner", j.Dir}, "/", j.LogPath(), envSlice(a.env))
	if err != nil {
		return a.fail(fmt.Errorf("start runner: %w", err))
	}
	j.RunnerPID = pid
	// The journal on disk may still say finished/failed from the run this
	// one continues. The new runner clears that itself, but not before
	// follow's first done() check can read it and report the OLD outcome
	// without waiting for anything — seen for real in the Docker
	// integration suite, where `continue` after a network drop exited 1
	// the instant it started, its freshly spawned runner still connecting.
	// Clearing it here, in the same Save that records the new runner's
	// pid, closes that window: follow only ever runs after this.
	j.Finished, j.Outcome = false, ""
	if err := j.Save(); err != nil {
		return a.fail(err)
	}
	a.logf("runner pid %d, log %s", pid, j.LogPath())
	return a.follow(ctx, j, bang, base)
}

// stepState reads a step's recorded state WITHOUT job.Journal.Step's
// side effect of appending a Pending entry for an unknown name.
func stepState(j *job.Journal, name string) job.StepState {
	for _, s := range j.Steps {
		if s.Name == name {
			return s
		}
	}
	return job.StepState{Name: name, Status: job.Pending}
}

// bangDone reports whether the runner we are following has reached the
// thaw+exit step, given how that step stood before it started (base). In
// !-mode the foreground must return the moment the source Claude is
// thawed and asked to exit — it is that Claude's own child — but the
// journal may carry a stale thaw+exit status from the run this one
// replaces, and returning on that would report success while the new
// runner is still dialling (carry 2). A fresh attempt (Attempts beyond
// the baseline) or a transition into Done is evidence from the NEW
// runner; anything unchanged is not.
func bangDone(jj *job.Journal, base job.StepState) bool {
	st := stepState(jj, "thaw+exit")
	return st.Attempts > base.Attempts || (st.Status == job.Done && base.Status != job.Done)
}

// follow streams log.txt until the job finishes (or, in !-mode, until step
// thaw+exit begins — after which the parent Claude must be free to read
// our exit), then maps the journal to an exit code.
func (a *app) follow(ctx context.Context, j *job.Journal, bang bool, base job.StepState) int {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	reload := func() *job.Journal {
		jj, ok, err := job.Open(a.paths.DataDir, j.ID)
		if err != nil || !ok {
			return j
		}
		return jj
	}
	finished := func(jj *job.Journal) bool {
		return jj.Finished || (bang && bangDone(jj, base))
	}
	runnerGone := false
	done := func() bool {
		jj := reload()
		if finished(jj) {
			return true
		}
		// A runner that died before marking the journal (crash, SIGKILL,
		// an early return that never got to save) must not hold the
		// foreground forever (finding A2). The runner's last Save
		// strictly precedes its exit, so re-reading the journal after
		// seeing the process gone can never miss a final state.
		if jj.RunnerPID > 0 && !a.runnerAlive(jj.RunnerPID) {
			if jj = reload(); finished(jj) {
				return true
			}
			runnerGone = true
			return true
		}
		return false
	}
	if bang {
		for !done() {
			select {
			case <-ctx.Done():
				fmt.Fprintf(a.stderr, "interrupted; the runner keeps going: claude-teleport status %s\n", j.ID)
				return ExitInterrupted
			case <-time.After(250 * time.Millisecond):
			}
		}
	} else if err := job.FollowLog(ctx, j.LogPath(), a.stdout, done); err != nil {
		if ctx.Err() != nil {
			fmt.Fprintf(a.stderr, "\ninterrupted; the runner keeps going. Watch it with: claude-teleport status %s\n", j.ID)
			return ExitInterrupted
		}
		return a.fail(err)
	}
	jj := reload()
	if runnerGone {
		fmt.Fprintf(a.stderr, "the teleport runner (pid %d) exited without finishing job %s; its last words are in %s:\n", jj.RunnerPID, session.ID(j.ID).Short(), jj.LogPath())
		for _, l := range tailLog(jj.LogPath(), 20) {
			fmt.Fprintln(a.stderr, l)
		}
		nextHint(a.stderr, j.ID)
		return ExitFailed
	}
	if bang && !jj.Finished {
		p, err := orchestrate.PlanFromJournal(jj)
		if err == nil {
			fmt.Fprintf(a.stdout, "teleported session %s to %s (%s); this Claude will now exit.\n", session.ID(j.ID).Short(), p.DestInfo.Hostname, p.TargetState)
		}
		return ExitOK
	}
	code := orchestrate.ExitCode(jj)
	if code != 0 {
		if name, ok := orchestrate.FailedStep(jj); ok {
			fmt.Fprintf(a.stderr, "teleport failed at step %s: %s\n", name, jj.Step(name).Error)
			if name == "start" {
				startFailureHint(a.stderr, j.ID, jj.Step(name).Error)
			} else {
				nextHint(a.stderr, j.ID)
			}
		}
		if bang {
			for _, l := range tailLog(jj.LogPath(), 20) {
				fmt.Fprintln(a.stderr, l)
			}
		}
	}
	return code
}

// startFailureHint advises on a failed start step. The standing advice
// assumes the destination Claude never came up — usually because it is
// not logged in there. A Claude waiting at its first-run TRUST dialog did
// come up, and the confirm error already names the host, the pane and the
// exact continue command (ruling R-P3-TRUST-1 item 2), so repeating the
// `/login` line over it would send the user hunting a login problem that
// does not exist — which is what the first real teleport printed.
func startFailureHint(w io.Writer, jobID, stepErr string) {
	if strings.Contains(stepErr, remote.TrustPromptWaiting) {
		nextHint(w, jobID)
		return
	}
	fmt.Fprintln(w, "the destination Claude did not resume — log in there (`claude` then /login) and run: claude-teleport continue", jobID)
}

func tailLog(path string, n int) []string {
	lines, err := job.TailLog(path, n)
	if err != nil {
		return nil
	}
	return lines
}

// runnerAlive reports whether pid is a live internal-runner process — the
// pid alone is not enough (pids are reused), so this also requires the
// cmdline to name internal-runner. It reads the process table under
// a.paths.ProcRoot, the one place the proc root is configured (tests point
// it at a fixture); an app whose paths were never resolved falls back to
// /proc. Shared by follow, continueJob and abandon (do not duplicate this
// closure elsewhere).
func (a *app) runnerAlive(pid int) bool {
	procRoot := a.paths.ProcRoot
	if procRoot == "" {
		procRoot = "/proc"
	}
	t, err := procx.Scan(procRoot)
	if err != nil {
		return false
	}
	p, ok := t.Get(pid)
	return ok && strings.Contains(strings.Join(p.Cmdline, " "), "internal-runner")
}

// continueJob attaches to a live runner or respawns a dead one.
func (a *app) continueJob(ctx context.Context, j *job.Journal, bang bool) int {
	if j.RunnerAlive(a.runnerAlive) {
		fmt.Fprintf(a.stdout, "attaching to runner %d\n", j.RunnerPID)
		return a.follow(ctx, j, bang, stepState(j, "thaw+exit"))
	}
	return a.spawnAndFollow(ctx, j, bang)
}

func newContinueCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "continue <sid>",
		Short: "resume an interrupted teleport",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := session.ParseID(args[0])
			if err != nil {
				return usageErr(err)
			}
			if err := a.ensurePaths(); err != nil {
				return err
			}
			j, ok, err := job.Open(a.paths.DataDir, string(id))
			if err != nil {
				return err
			}
			if !ok {
				return usageErr(fmt.Errorf("no job for session %s", id.Short()))
			}
			if j.Outcome == "success" {
				fmt.Fprintf(a.stdout, "job %s already finished successfully\n", id.Short())
				return nil
			}
			if j.Outcome == "abandoned" {
				return usageErr(fmt.Errorf("job %s was abandoned; start a new teleport", id.Short()))
			}
			return exitErr(a.continueJob(cmd.Context(), j, a.continueBang(j)))
		},
	}
}

func newInternalRunnerCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:    "internal-runner <job-dir>",
		Hidden: true,
		Args:   cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageErr(fmt.Errorf("internal-runner: exactly one job directory argument is required"))
			}
			jobDir := filepath.Clean(args[0])
			dataDir := filepath.Dir(filepath.Dir(jobDir))
			id := filepath.Base(jobDir)
			if _, ok, err := job.Open(dataDir, id); err != nil {
				return err
			} else if !ok {
				return usageErr(fmt.Errorf("no journal at %s", jobDir))
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, os.Interrupt)
			defer stop()
			logf := func(format string, v ...any) {
				fmt.Fprintf(a.stderr, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, v...))
			}
			a.logf = logf
			if err := orchestrate.RunJob(ctx, dataDir, id, a.endpoints, logf); err != nil {
				// internal-runner is detached (procx.SpawnDetached releases
				// it; nothing waits on its exit code in the normal flow —
				// the foreground `follow()` reports spec §5's exit code
				// from the journal instead). Map it via orchestrate.ExitCode
				// too, so a human running it directly for debugging still
				// sees the right code.
				if jj, ok, _ := job.Open(dataDir, id); ok {
					return exitErr(orchestrate.ExitCode(jj))
				}
				return exitErr(ExitFailed)
			}
			return nil
		},
	}
}

// usageErr maps a bad invocation to exit 2 through Plan 01's ExitError.
func usageErr(err error) error { return Exit(ExitUsage, "%v", err) }

// errReported marks an error whose message a command already printed
// itself (a.fail, a.runTeleport's own usage prints): Main prints ee.Err
// only when it is non-empty, so the message is never doubled.
var errReported = errors.New("")

// exitErr turns an already-reported exit code into the error Main maps.
func exitErr(code int) error {
	if code == ExitOK {
		return nil
	}
	return &ExitError{Code: code, Err: errReported}
}
