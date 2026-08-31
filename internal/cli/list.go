package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/session"
)

type listRow struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Name   string `json:"name,omitempty"`
	PID    int    `json:"pid,omitempty"`
	Cwd    string `json:"cwd"`
	Branch string `json:"branch"`
	Last   string `json:"last_active"`
	Tmux   string `json:"tmux,omitempty"`
}

// listSessions enumerates every transcript under projects/ and marks the
// ones with a live registry entry as running; when probe is non-nil (Plan 03
// wires tmuxx.Prober in) it additionally scans every pane for a placeholder
// holding a session id and marks that session suspended — a live registry
// entry for the same id always wins over a suspended pane.
func listSessions(p session.Paths, probe session.PaneProbe) ([]listRow, error) {
	regs, err := session.ReadRegistry(p.SessionsDir())
	if err != nil {
		return nil, err
	}
	running := map[string]session.Registry{}
	for _, r := range regs {
		if session.ProcAlive(p.ProcRoot, r.PID, r.ProcStart) {
			running[r.SessionID] = r
		}
	}
	suspended := map[string]session.PaneInfo{}
	if probe != nil {
		panes, err := probe.ListPanes()
		if err != nil {
			return nil, fmt.Errorf("list tmux panes: %w", err)
		}
		for _, pi := range panes {
			argv, _, ok := probe.PaneCommand(pi.PaneID)
			if !ok {
				continue
			}
			sid, placeholder, ok := session.ArgvSessionID(argv)
			if !ok || !placeholder || sid == "" {
				continue
			}
			if _, isRunning := running[sid]; isRunning {
				continue
			}
			suspended[sid] = pi
		}
	}
	transcripts, err := filepath.Glob(filepath.Join(p.ProjectsDir(), "*", "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("glob transcripts: %w", err)
	}
	var rows []listRow
	for _, t := range transcripts {
		id := strings.TrimSuffix(filepath.Base(t), ".jsonl")
		if !session.IsUUID(id) {
			continue
		}
		m, err := session.ReadMeta(t)
		if err != nil {
			return nil, err
		}
		row := listRow{ID: id, State: session.StateIdle.String(), Cwd: m.LaunchCwd, Branch: m.Branch, Last: m.LastTS}
		if r, ok := running[id]; ok {
			row.State, row.Name, row.PID, row.Tmux = session.StateRunning.String(), r.Name, r.PID, r.Tmux
		} else if pi, ok := suspended[id]; ok {
			row.State = session.StateSuspended.String()
			row.Tmux = fmt.Sprintf("%s:%s.%s", pi.Session, pi.WindowID, pi.PaneID)
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].State != rows[j].State {
			return rows[i].State == "running"
		}
		return rows[i].Last > rows[j].Last
	})
	return rows, nil
}

func (a *app) listCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{
		Use:   "list [--host <host>]",
		Short: "list sessions on this host (running / suspended / idle)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if host != "" {
				// Remote listing rides on the Plan 02 helper protocol.
				return Exit(ExitUsage, "--host: remote listing not implemented yet")
			}
			p, err := a.resolvePaths()
			if err != nil {
				return err
			}
			rows, err := listSessions(p, a.probe())
			if err != nil {
				return Exit(ExitFailed, "%v", err)
			}
			if a.json() {
				if rows == nil {
					rows = []listRow{}
				}
				b, _ := json.MarshalIndent(rows, "", "  ")
				fmt.Fprintln(a.stdout, string(b))
				return nil
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTATE\tNAME\tPID\tCWD\tBRANCH\tLAST ACTIVE")
			for _, r := range rows {
				pid := ""
				if r.PID != 0 {
					pid = fmt.Sprint(r.PID)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID[:8], r.State, r.Name, pid, r.Cwd, r.Branch, r.Last)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "list sessions on a remote host")
	return cmd
}
