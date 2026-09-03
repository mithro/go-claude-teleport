package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/orchestrate"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// newAbandonCmd gives up on a job: it marks the journal abandoned, removes
// the destination's staging directory and, with --delete-destination-files,
// removes what this job installed there (manifest-bounded — see
// transfer.UninstallIDs). Unlike Plan 02's abandonCmd, the destination need
// not be this host: a.endpoints dials it exactly as a teleport would, so
// deletion works whether this host is the source or the destination.
//
// Re-running abandon on an already-abandoned job is allowed (ruling
// R-P3-23f): the local side (marking the journal, above) only happens once,
// but the destination side (Cleanup / DeleteInstalled) is idempotent and is
// retried every time — the intended way to finish destination clean-up
// after an earlier attempt found the destination unreachable.
func newAbandonCmd(a *app) *cobra.Command {
	var deleteFiles bool
	cmd := &cobra.Command{
		Use:   "abandon <sid> [--delete-destination-files]",
		Short: "give up on an interrupted teleport; optionally delete what it installed on the destination",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
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
			if j.RunnerAlive(a.runnerAlive) {
				return fail(ExitFailed, "job %s has a live runner (pid %d); stop it first (kill %d) or let it finish", id.Short(), j.RunnerPID, j.RunnerPID)
			}

			p, err := orchestrate.PlanFromJournal(j)
			if err != nil {
				return fail(ExitFailed, "%v", err)
			}
			if p.Session == nil {
				return fail(ExitFailed, "job %s has no recorded plan (preflight never completed)", id.Short())
			}
			alreadyAbandoned := j.Outcome == "abandoned" && j.Finished
			if err := checkDriverHost(p); err != nil {
				return err
			}

			src, dst, closeFn, err := a.endpoints(ctx, p.Options)
			if err != nil {
				// R-P3-23k: classify via a.fail exactly like every other
				// command does (protocol mismatch, a bad stored target,
				// etc. keep their own code — usage stays usage) — only
				// when that classification is genuinely ExitUnreachable
				// does R-P3-23f's "local side completes, clean-up
				// pending" special case apply; any other failure is just
				// a normal failure, and the journal is left untouched.
				code := a.fail(err)
				if code == ExitUnreachable {
					if !alreadyAbandoned {
						j.Outcome, j.Finished = "abandoned", true
						if serr := j.Save(); serr != nil {
							return fail(ExitFailed, "%v", serr)
						}
						fmt.Fprintf(a.stdout, "job %s abandoned locally; the source session is untouched\n", id.Short())
					}
					fmt.Fprintf(a.stderr, "destination clean-up is pending — re-run `abandon %s` once it is reachable\n", id.Short())
				}
				return exitErr(code)
			}
			defer closeFn()

			if deleteFiles {
				// R-P3-23e: never delete files a live destination session
				// might still be using. No override — the user must exit
				// it there first.
				reg, live, serr := dst.ClaudeStatus(ctx, session.ID(j.ID))
				if serr != nil {
					return fail(ExitFailed, "%v", serr)
				}
				if live {
					return fail(ExitRefused, "the destination session is still running on %s (pid %d); exit it there (/exit) and re-run `abandon %s --delete-destination-files`", p.DestInfo.Hostname, reg.PID, id.Short())
				}
			}

			if err := dst.Cleanup(ctx, j.ID); err != nil {
				return fail(ExitFailed, "remove staging on %s: %v", p.DestInfo.Hostname, err)
			}
			fmt.Fprintf(a.stdout, "removed staging for %s on %s\n", id.Short(), p.DestInfo.Hostname)

			var stepErr error
			if deleteFiles {
				stepErr = deleteInstalledFiles(ctx, a, p, dst)
			}

			// R-P3-23l: src.Record/dst.Record append a history row each —
			// only run them on the transition to abandoned (never on a
			// retry of an already-abandoned job), or every re-run of
			// abandon (e.g. retrying destination clean-up) would append a
			// duplicate "abandoned" row.
			if !alreadyAbandoned {
				j.Outcome, j.Finished = "abandoned", true
				if err := j.Save(); err != nil {
					return fail(ExitFailed, "%v", err)
				}
				rec := job.HistoryRecord{At: time.Now().UTC(), SessionID: j.ID, Direction: j.Direction, From: j.SourceHost, To: j.DestHost, Outcome: "abandoned"}
				if err := src.Record(ctx, j.ID, rec); err != nil {
					a.logf("abandon: record on source: %v", err)
				}
				if err := dst.Record(ctx, j.ID, rec); err != nil {
					a.logf("abandon: record on %s: %v", p.DestInfo.Hostname, err)
				}
			}
			if err := src.JournalPut(ctx, j); err != nil {
				a.logf("abandon: journal-put on source: %v", err)
			}
			if err := dst.JournalPut(ctx, j); err != nil {
				a.logf("abandon: journal-put on %s: %v", p.DestInfo.Hostname, err)
			}
			fmt.Fprintf(a.stdout, "job %s abandoned; the source session is untouched\n", id.Short())
			if stepErr != nil {
				return fail(ExitFailed, "%v", stepErr)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&deleteFiles, "delete-destination-files", false, "delete the files this job itself installed on the destination (unchanged since); refuses if the destination session is still running")
	return cmd
}

// checkDriverHost refuses an abandon run on the job's OTHER host. A job is
// driven from one side — the source for --to, the destination for --from —
// and everything stored in the plan is written from there:
// p.Options.Target names the far end AS SEEN FROM THE DRIVER, so re-dialling
// it from the other end reaches the wrong machine (often the very host
// running the command). Only the unambiguous case is refused: this host's
// name is exactly the job's non-driver hostname (earlier deferred carry).
func checkDriverHost(p *orchestrate.Plan) error {
	here, err := os.Hostname()
	if err != nil || here == "" {
		return nil
	}
	driver, other := p.SourceInfo.Hostname, p.DestInfo.Hostname
	if p.Options.Direction == "from" {
		driver, other = other, driver
	}
	if here == driver || here != other {
		return nil
	}
	return fail(ExitUsage, "this host (%s) is the job's %s end; it was driven from %s — run `claude-teleport abandon %s` there (the stored target %q is only reachable from that host)",
		here, otherEndWord(p.Options.Direction), driver, p.Session.ID.Short(), p.Options.Target)
}

// otherEndWord names the end an abandon must NOT be run from.
func otherEndWord(direction string) string {
	if direction == "from" {
		return "source"
	}
	return "destination"
}

// deleteInstalledFiles loads the job's manifest and deletes, on dst, only
// the entries in p.InstalledIDs — the ids the install step (spec §6 step
// 5) itself recorded as placed (ruling R-P3-23a: Plan.Statuses is
// overwritten by later manifest-diffs and can no longer answer "what did
// this job install" once the job has run past transfer). DeleteInstalled
// (transfer.UninstallIDs) re-verifies each entry's hash before removing
// anything, so a file that changed since install is left alone regardless.
func deleteInstalledFiles(ctx context.Context, a *app, p *orchestrate.Plan, dst remote.Endpoint) error {
	m, err := transfer.Load(p.ManifestPath)
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	deleted, err := dst.DeleteInstalled(ctx, m, p.InstalledIDs)
	for _, d := range deleted {
		fmt.Fprintf(a.stdout, "deleted %s\n", d)
	}
	fmt.Fprintf(a.stdout, "%d file(s) that this job installed and were unchanged since were deleted\n", len(deleted))
	return err
}
