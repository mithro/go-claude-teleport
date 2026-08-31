package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/session"
)

type inspectOut struct {
	ID         string              `json:"id"`
	State      string              `json:"state"`
	Name       string              `json:"name,omitempty"`
	LaunchCwd  string              `json:"launch_cwd"`
	WorkCwd    string              `json:"work_cwd"`
	Branch     string              `json:"branch"`
	Version    string              `json:"claude_version"`
	Transcript string              `json:"transcript"`
	Registry   *session.Registry   `json:"registry,omitempty"`
	Tmux       *session.TmuxRef    `json:"tmux,omitempty"`
	Files      []session.FileEntry `json:"files"`
	Memory     []session.FileEntry `json:"memory"`
	Skipped    []session.Skipped   `json:"skipped"`
	TotalBytes int64               `json:"total_bytes"`
	Usage      *session.Usage      `json:"usage"`
}

func keys(m map[string]bool) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	if len(ks) == 0 {
		return "(none)"
	}
	return strings.Join(ks, ", ")
}

func (a *app) inspectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect [<session>]",
		Short: "show everything a teleport would move for a session",
		Long: `Resolves the session (same rules as a teleport), then lists its state,
directories, every session file that would be transferred, what the
transcript used (MCP servers, skills, plugins, sub-agents) and what would
be skipped. The configuration drift report needs a destination host: see
"claude-teleport compare-config <host> --session <session>".`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.resolveSession(args)
			if err != nil {
				return err
			}
			inv, err := session.InventoryFiles(s)
			if err != nil {
				return Exit(ExitFailed, "%v", err)
			}
			usage, err := session.ScanUsage(s)
			if err != nil {
				return Exit(ExitFailed, "%v", err)
			}
			out := inspectOut{ID: string(s.ID), State: s.State.String(), Name: s.Name, LaunchCwd: s.LaunchCwd, WorkCwd: s.WorkCwd,
				Branch: s.Branch, Version: s.Version, Transcript: s.Transcript, Registry: s.Registry, Tmux: s.Tmux,
				Files: inv.Files, Memory: inv.Memory, Skipped: inv.Skipped, Usage: usage}
			for _, f := range inv.Files {
				out.TotalBytes += f.Size
			}
			if a.json() {
				b, _ := json.MarshalIndent(out, "", "  ")
				fmt.Fprintln(a.stdout, string(b))
				return nil
			}
			w := a.stdout
			fmt.Fprintf(w, "session    %s (%s)\n", out.ID, out.State)
			if out.Name != "" {
				fmt.Fprintf(w, "name       %s\n", out.Name)
			}
			fmt.Fprintf(w, "launch cwd %s\n", out.LaunchCwd)
			if out.WorkCwd != out.LaunchCwd {
				fmt.Fprintf(w, "work cwd   %s\n", out.WorkCwd)
			}
			fmt.Fprintf(w, "branch     %s\nclaude     %s\ntranscript %s\n", out.Branch, out.Version, out.Transcript)
			if out.Registry != nil {
				fmt.Fprintf(w, "process    pid %d status %s tmux %q\n", out.Registry.PID, out.Registry.Status, out.Registry.Tmux)
			}
			fmt.Fprintf(w, "\nfiles to move (%d, %d bytes):\n", len(out.Files), out.TotalBytes)
			for _, f := range out.Files {
				if f.Mode.IsDir() {
					continue
				}
				fmt.Fprintf(w, "  %10d  %s\n", f.Size, f.Rel)
			}
			if len(out.Memory) > 0 {
				fmt.Fprintln(w, "\nproject memory (copied only if absent on the destination):")
				for _, f := range out.Memory {
					if !f.Mode.IsDir() {
						fmt.Fprintf(w, "  %10d  %s\n", f.Size, f.Rel)
					}
				}
			}
			if len(out.Skipped) > 0 {
				fmt.Fprintln(w, "\nskipped:")
				for _, sk := range out.Skipped {
					fmt.Fprintf(w, "  %s (%s)\n", sk.Path, sk.Reason)
				}
			}
			fmt.Fprintf(w, "\nused by the transcript:\n  mcp: %s\n  skills: %s\n  plugins: %s\n  sub-agents: %s\n  permission modes: %s\n",
				keys(usage.MCPServers), keys(usage.Skills), keys(usage.Plugins), keys(usage.SubagentTypes), keys(usage.PermissionModes))
			fmt.Fprintln(w, "\ndrift report: needs a destination — run: claude-teleport compare-config <host> --session", out.ID[:8])
			return nil
		},
	}
}
