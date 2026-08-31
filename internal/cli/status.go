package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

func statusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status <sid>",
		Short: "journal and manifest of a teleport job",
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
			m, err := transfer.Load(j.ManifestPath())
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fail(ExitFailed, "%v", err)
			}
			tail, err := job.TailLog(j.LogPath(), 20)
			if err != nil {
				return fail(ExitFailed, "%v", err)
			}
			if asJSON {
				doc := map[string]any{"journal": j, "manifest": m, "log_tail": tail}
				enc := json.NewEncoder(e.stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(doc)
			}
			renderStatus(e.stdout, j, m, tail)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

func renderStatus(w io.Writer, j *job.Journal, m *transfer.Manifest, logTail []string) {
	fmt.Fprintf(w, "job %s: %s %s -> %s\n", j.ID, j.Direction, j.SourceHost, j.DestHost)
	outcome := j.Outcome
	if outcome == "" {
		outcome = "in progress"
	}
	fmt.Fprintf(w, "created %s, updated %s, outcome: %s\n", j.CreatedAt.Format("2006-01-02T15:04:05Z07:00"), j.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"), outcome)
	if j.RunnerPID > 0 {
		fmt.Fprintf(w, "runner pid %d\n", j.RunnerPID)
	}
	fmt.Fprintln(w, "steps:")
	for _, s := range j.Steps {
		line := fmt.Sprintf("  %-10s %-8s", s.Name, s.Status)
		if s.Attempts > 0 {
			line += fmt.Sprintf(" attempts %d", s.Attempts)
		}
		if s.Error != "" {
			line += " error: " + s.Error
		}
		fmt.Fprintln(w, line)
	}
	if m != nil {
		var bytes int64
		for _, e := range m.Entries {
			bytes += e.Size
		}
		fmt.Fprintf(w, "manifest: %d entries, %d bytes, %d skipped\n", len(m.Entries), bytes, len(m.Skipped))
	}
	if len(logTail) > 0 {
		fmt.Fprintln(w, "log tail:")
		for _, l := range logTail {
			fmt.Fprintln(w, "  "+l)
		}
	}
	if !j.Finished {
		fmt.Fprintln(w)
		nextHint(w, j.ID)
	}
}
