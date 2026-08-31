// internal/cli/teleport.go
package cli

import (
	"context"
	"errors"
	"fmt"
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
	// !-mode (spec §6.3, BangMode): only internal/cli reads $CLAUDE_PID —
	// running inside the very session being moved. A match requires both
	// the pid AND the live registry entry for that same session, so a
	// stale/unrelated CLAUDE_PID can never trip it.
	if pid, _ := strconv.Atoi(a.env["CLAUDE_PID"]); pid != 0 && sess.Registry != nil && sess.Registry.PID == pid {
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
		fmt.Fprintf(a.stdout, "job %s is %s at step %s; continuing it (use `abandon` to start over)\n", sess.ID.Short(), stateWord(j), firstIncompleteName(j))
		return a.continueJob(ctx, j, o.BangMode)
	}
	plan, err := orchestrate.Preflight(ctx, o, src, dst, jobID)
	if err != nil {
		closeFn()
		return a.fail(err)
	}
	plan.Render(a.stdout)
	if f.DryRun {
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

func stateWord(j *job.Journal) string {
	if j.Outcome == "failed" {
		return "failed"
	}
	return "in progress"
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
	pid, err := procx.SpawnDetached([]string{a.selfExe, "internal-runner", j.Dir}, "/", j.LogPath(), envSlice(a.env))
	if err != nil {
		return a.fail(fmt.Errorf("start runner: %w", err))
	}
	j.RunnerPID = pid
	if err := j.Save(); err != nil {
		return a.fail(err)
	}
	a.logf("runner pid %d, log %s", pid, j.LogPath())
	return a.follow(ctx, j, bang)
}

// follow streams log.txt until the job finishes (or, in !-mode, until step
// thaw+exit begins — after which the parent Claude must be free to read
// our exit), then maps the journal to an exit code.
func (a *app) follow(ctx context.Context, j *job.Journal, bang bool) int {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	reload := func() *job.Journal {
		jj, ok, err := job.Open(a.paths.DataDir, j.ID)
		if err != nil || !ok {
			return j
		}
		return jj
	}
	done := func() bool {
		jj := reload()
		if bang {
			st := jj.Step("thaw+exit")
			return jj.Finished || st.Status != job.Pending
		}
		return jj.Finished
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
				fmt.Fprintln(a.stderr, "the destination Claude did not resume — log in there (`claude` then /login) and run: claude-teleport continue", j.ID)
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

func tailLog(path string, n int) []string {
	lines, err := job.TailLog(path, n)
	if err != nil {
		return nil
	}
	return lines
}

// continueJob attaches to a live runner or respawns a dead one.
func (a *app) continueJob(ctx context.Context, j *job.Journal, bang bool) int {
	alive := func(pid int) bool {
		t, err := procx.Scan("/proc")
		if err != nil {
			return false
		}
		p, ok := t.Get(pid)
		return ok && strings.Contains(strings.Join(p.Cmdline, " "), "internal-runner")
	}
	if j.RunnerAlive(alive) {
		fmt.Fprintf(a.stdout, "attaching to runner %d\n", j.RunnerPID)
		return a.follow(ctx, j, bang)
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
			return exitErr(a.continueJob(cmd.Context(), j, false))
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
			if err := orchestrate.RunJob(ctx, dataDir, id, a.endpoints, a.selfExe, logf); err != nil {
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
