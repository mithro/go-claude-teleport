package cli

import (
	"context"
	"fmt"
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
			if j.RunnerAlive(runnerAlive) {
				return fail(ExitFailed, "job %s has a live runner (pid %d); stop it first (kill %d) or let it finish", id.Short(), j.RunnerPID, j.RunnerPID)
			}

			p, err := orchestrate.PlanFromJournal(j)
			if err != nil {
				return fail(ExitFailed, "%v", err)
			}
			if p.Session == nil {
				return fail(ExitFailed, "job %s has no recorded plan (preflight never completed)", id.Short())
			}

			src, dst, closeFn, err := a.endpoints(ctx, p.Options)
			if err != nil {
				return exitErr(a.fail(err))
			}
			defer closeFn()
			if err := dst.Cleanup(ctx, j.ID); err != nil {
				return fail(ExitFailed, "remove staging on %s: %v", p.DestInfo.Hostname, err)
			}
			fmt.Fprintf(a.stdout, "removed staging for %s on %s\n", id.Short(), p.DestInfo.Hostname)

			var stepErr error
			if deleteFiles {
				stepErr = deleteInstalledFiles(ctx, a, p, dst)
			}

			j.Outcome, j.Finished = "abandoned", true
			if err := j.Save(); err != nil {
				return fail(ExitFailed, "%v", err)
			}
			if err := src.JournalPut(ctx, j); err != nil {
				a.logf("abandon: journal-put on source: %v", err)
			}
			if err := dst.JournalPut(ctx, j); err != nil {
				a.logf("abandon: journal-put on %s: %v", p.DestInfo.Hostname, err)
			}
			rec := job.HistoryRecord{At: time.Now().UTC(), SessionID: j.ID, Direction: j.Direction, From: j.SourceHost, To: j.DestHost, Outcome: "abandoned"}
			if err := src.Record(ctx, j.ID, rec); err != nil {
				a.logf("abandon: record on source: %v", err)
			}
			if err := dst.Record(ctx, j.ID, rec); err != nil {
				a.logf("abandon: record on %s: %v", p.DestInfo.Hostname, err)
			}
			fmt.Fprintf(a.stdout, "job %s abandoned; the source session is untouched\n", id.Short())
			if stepErr != nil {
				return fail(ExitFailed, "%v", stepErr)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&deleteFiles, "delete-destination-files", false, "delete the files this job installed on the destination (only those absent before this job and unchanged since)")
	return cmd
}

// deleteInstalledFiles loads the job's manifest and deletes, on dst, only
// the entries that were Absent at preflight — i.e. entries THIS job
// installed, never a file that merely already existed there with matching
// content for unrelated reasons (ruling: never delete anything outside the
// job's manifest, never anything that pre-existed).
func deleteInstalledFiles(ctx context.Context, a *app, p *orchestrate.Plan, dst remote.Endpoint) error {
	m, err := transfer.Load(p.ManifestPath)
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	var ids []int
	for _, e := range m.Entries {
		if p.Statuses[e.ID] == transfer.Absent {
			ids = append(ids, e.ID)
		}
	}
	deleted, err := dst.DeleteInstalled(ctx, m, ids)
	for _, d := range deleted {
		fmt.Fprintf(a.stdout, "deleted %s\n", d)
	}
	fmt.Fprintf(a.stdout, "%d file(s) that this job installed and were unchanged since were deleted\n", len(deleted))
	return err
}
