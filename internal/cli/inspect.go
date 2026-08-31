package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/remote"
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
	var host string
	var via, opts []string
	cmd := &cobra.Command{
		Use:   "inspect [<session>]",
		Short: "show everything a teleport would move for a session",
		Long: `Resolves the session (same rules as a teleport), then lists its state,
directories, every session file that would be transferred, what the
transcript used (MCP servers, skills, plugins, sub-agents) and what would
be skipped. The configuration drift report needs a destination host: see
"claude-teleport compare-config <host> --session <session>".

With --host, the session is resolved and inventoried on that host instead
of locally (--via/-o work as they do for a teleport); the report is
otherwise identical.`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if host != "" {
				return a.inspectRemote(cmd, args, host, via, opts)
			}
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
			return a.renderInspect(s, inv, usage)
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "resolve and inventory the session on this host instead of locally")
	remoteFlags(cmd, &via, &opts)
	return cmd
}

// inspectRemote resolves the selector on host over the Plan 02 remote
// transport and renders the same report renderInspect produces locally,
// sourced from that host's inventory instead of this machine's.
func (a *app) inspectRemote(cmd *cobra.Command, args []string, host string, via, opts []string) error {
	ctx := cmd.Context()
	sel, err := session.ParseSelector(args, a.selectorEnv())
	if err != nil {
		return Exit(ExitUsage, "%v", err)
	}
	rc, closeRemote, err := openRemote(cmd, host, via, opts)
	if err != nil {
		return err
	}
	defer closeRemote()
	s, err := rc.ResolveSession(ctx, sel)
	if err != nil {
		var pe *remote.Error
		if errors.As(err, &pe) && pe.Code == "not-found" {
			return Exit(ExitRefused, "%s: %v", host, err)
		}
		return Exit(ExitFailed, "%s: %v", host, err)
	}
	inv, usage, err := rc.InventorySession(ctx, s.ID)
	if err != nil {
		return Exit(ExitFailed, "%s: %v", host, err)
	}
	return a.renderInspect(s, inv, usage)
}

// renderInspect writes the inspect report for s/inv/usage; the single
// rendering path shared by the local branch and inspect --host.
func (a *app) renderInspect(s *session.Session, inv *session.Inventory, usage *session.Usage) error {
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
}
