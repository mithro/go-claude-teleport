package cli

import (
	"fmt"
	"os"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func abandonCmd() *cobra.Command {
	var deleteDest bool
	cmd := &cobra.Command{
		Use:   "abandon <sid> [--delete-destination-files]",
		Short: "give up on a teleport job; clean staging, optionally remove installed files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := envOf(cmd)
			id, err := session.ParseID(args[0])
			if err != nil {
				return fail(ExitUsage, "%v", err)
			}
			p, err := envPaths(e.env)
			if err != nil {
				return fail(ExitUsage, "%v", err)
			}
			j, found, err := job.Open(p.DataDir, string(id))
			if err != nil {
				return fail(ExitFailed, "%v", err)
			}
			if !found {
				return fail(ExitFailed, "no job for session %s under %s", id, job.Dir(p.DataDir, string(id)))
			}
			if j.RunnerAlive(pidAlive) {
				return fail(ExitFailed, "job %s has a live runner (pid %d); stop it first", id, j.RunnerPID)
			}
			j.Outcome = "abandoned"
			j.Finished = true
			if err := j.Save(); err != nil {
				return fail(ExitFailed, "%v", err)
			}
			staging := job.StagingDir(p.DataDir, string(id))
			if err := os.RemoveAll(staging); err != nil {
				return fail(ExitFailed, "remove staging %s: %w", staging, err)
			}
			fmt.Fprintf(e.stdout, "job %s marked abandoned; staging %s removed; journal kept at %s\n", id, staging, j.Dir)
			if !deleteDest {
				return nil
			}
			if j.Direction != "from" {
				return fail(ExitFailed, "deleting files on %s over ssh arrives in Plan 03; run `claude-teleport abandon %s --delete-destination-files` on %s instead", j.DestHost, id, j.DestHost)
			}
			m, err := transfer.Load(j.ManifestPath())
			if err != nil {
				return fail(ExitFailed, "%v", err)
			}
			removed, err := transfer.Uninstall(m, p)
			for _, r := range removed {
				fmt.Fprintf(e.stdout, "removed %s\n", r)
			}
			if err != nil {
				return fail(ExitFailed, "%v", err)
			}
			fmt.Fprintf(e.stdout, "%d of %d manifest entries removed (unchanged files only)\n", len(removed), len(m.Entries))
			return nil
		},
	}
	cmd.Flags().BoolVar(&deleteDest, "delete-destination-files", false, "remove files this job installed on the destination (only those still matching the manifest)")
	return cmd
}
