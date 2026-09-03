package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/orchestrate"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// inspectReport is everything `inspect` can show: the local inventory
// (files a teleport would move, git state) always, and — with --host —
// the preflight plan and drift table against that destination, or the
// refusal reason.
type inspectReport struct {
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
	Git        *gitx.Info          `json:"git,omitempty"`
	GitError   string              `json:"git_error,omitempty"`
	Plan       *orchestrate.Plan   `json:"plan,omitempty"`
	Refused    string              `json:"refused,omitempty"`
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

// newInspectCmd subsumes Plan 01's local-only inspect and Plan 02's
// inspectRemote: --host runs the exact preflight a teleport would (spec
// §6 step 1) and renders the plan and drift table, or the refusal.
func newInspectCmd(a *app) *cobra.Command {
	var host string
	var via, opts []string
	cmd := &cobra.Command{
		Use:   "inspect [<session>]",
		Short: "show everything a teleport would move, and the drift report against --host",
		Long: `Resolves the session (same rules as a teleport), then lists its state,
directories, every session file that would be transferred, its git state
and what would be skipped.

With --host, also runs preflight against that destination (--via/-o work
as they do for a teleport) and renders the plan and drift table exactly as
a real teleport would show it, or the refusal reason (exit 3).`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sess, err := a.resolveSession(args)
			if err != nil {
				return err
			}
			inv, err := session.InventoryFiles(sess)
			if err != nil {
				return Exit(ExitFailed, "%v", err)
			}
			usage, err := session.ScanUsage(sess)
			if err != nil {
				return Exit(ExitFailed, "%v", err)
			}
			rep := &inspectReport{
				ID: string(sess.ID), State: sess.State.String(), Name: sess.Name,
				LaunchCwd: sess.LaunchCwd, WorkCwd: sess.WorkCwd, Branch: sess.Branch,
				Version: sess.Version, Transcript: sess.Transcript, Registry: sess.Registry,
				Tmux: sess.Tmux, Files: inv.Files, Memory: inv.Memory, Skipped: inv.Skipped, Usage: usage,
			}
			for _, f := range inv.Files {
				rep.TotalBytes += f.Size
			}
			if gi, gerr := gitx.Inspect(sess.LaunchCwd); gerr != nil {
				if !errors.Is(gerr, gitx.ErrNotRepo) {
					rep.GitError = gerr.Error()
				}
			} else {
				rep.Git = gi
			}

			code := ExitOK
			if host != "" {
				sshOpts, err := parseSSHOptions(opts)
				if err != nil {
					return usageErr(err)
				}
				o := orchestrate.Options{
					Direction: "to", Target: host, Via: via, SSHOptions: sshOpts,
					Selector: session.Selector{ID: sess.ID}, State: "auto",
					ExitTimeout: 30 * time.Second, StartTimeout: 90 * time.Second,
				}
				src, dst, closeFn, err := a.endpoints(ctx, o)
				if err != nil {
					return exitErr(a.fail(err))
				}
				defer closeFn()
				plan, err := orchestrate.Preflight(ctx, o, src, dst, string(sess.ID))
				var re *orchestrate.RefusedError
				switch {
				case errors.As(err, &re):
					rep.Refused, code = re.Reason, ExitRefused
				case err != nil:
					return exitErr(a.fail(err))
				default:
					rep.Plan = plan
				}
			}

			if a.json() {
				b, err := json.MarshalIndent(rep, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(a.stdout, string(b))
				return exitErr(code)
			}
			renderInspect(a.stdout, rep, host)
			return exitErr(code)
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "also run preflight against HOST and show the plan and drift")
	remoteFlags(cmd, &via, &opts)
	return cmd
}

// renderInspect writes the human-readable report; a.json() short-circuits
// to JSON before this is ever called.
func renderInspect(w io.Writer, rep *inspectReport, host string) {
	shortID := session.ID(rep.ID).Short()
	fmt.Fprintf(w, "session    %s (%s)\n", shortID, rep.State)
	if rep.Name != "" {
		fmt.Fprintf(w, "name       %s\n", rep.Name)
	}
	fmt.Fprintf(w, "launch cwd %s\n", rep.LaunchCwd)
	if rep.WorkCwd != "" && rep.WorkCwd != rep.LaunchCwd {
		fmt.Fprintf(w, "work cwd   %s\n", rep.WorkCwd)
	}
	fmt.Fprintf(w, "branch     %s\nclaude     %s\ntranscript %s\n", rep.Branch, rep.Version, rep.Transcript)
	if rep.Registry != nil {
		fmt.Fprintf(w, "process    pid %d status %s tmux %q\n", rep.Registry.PID, rep.Registry.Status, rep.Registry.Tmux)
	}

	nSession := 0
	for _, f := range rep.Files {
		if !f.Mode.IsDir() {
			nSession++
		}
	}
	fmt.Fprintf(w, "\n%d session files, %d memory files, %d bytes total, %d skipped:\n", nSession, len(rep.Memory), rep.TotalBytes, len(rep.Skipped))
	for _, f := range rep.Files {
		if f.Mode.IsDir() {
			continue
		}
		fmt.Fprintf(w, "  %10d  %s\n", f.Size, f.Rel)
	}
	if len(rep.Memory) > 0 {
		fmt.Fprintln(w, "\nproject memory (copied only if absent on the destination):")
		for _, f := range rep.Memory {
			if !f.Mode.IsDir() {
				fmt.Fprintf(w, "  %10d  %s\n", f.Size, f.Rel)
			}
		}
	}
	for _, s := range rep.Skipped {
		fmt.Fprintf(w, "  skipped %s: %s\n", s.Path, s.Reason)
	}

	switch {
	case rep.Git != nil:
		g := rep.Git
		kind := "main checkout"
		if g.IsLinked {
			kind = "linked worktree " + g.WorktreeName
		}
		head := g.Head
		if len(head) > 7 {
			head = head[:7]
		}
		fmt.Fprintf(w, "\nGit     %s of %s, branch %s at %s\n", kind, g.MainDir, g.Branch, head)
		fmt.Fprintf(w, "  dirty %d staged, %d modified, %d untracked, %d deleted; %d other worktree(s)\n",
			len(g.Dirty.Staged), len(g.Dirty.Modified), len(g.Dirty.Untracked), len(g.Dirty.Deleted), len(g.OtherWorktrees))
	case rep.GitError != "":
		fmt.Fprintf(w, "\nGit     error: %s\n", rep.GitError)
	default:
		fmt.Fprintf(w, "\nGit     not a git repository (%s is copied as plain files)\n", rep.LaunchCwd)
	}

	if rep.Usage != nil {
		fmt.Fprintf(w, "\nused by the transcript:\n  mcp: %s\n  skills: %s\n  plugins: %s\n  sub-agents: %s\n  permission modes: %s\n",
			keys(rep.Usage.MCPServers), keys(rep.Usage.Skills), keys(rep.Usage.Plugins), keys(rep.Usage.SubagentTypes), keys(rep.Usage.PermissionModes))
	}

	switch {
	case rep.Plan != nil:
		fmt.Fprintf(w, "\nPlan against %s\n", host)
		rep.Plan.Render(w)
	case rep.Refused != "":
		fmt.Fprintf(w, "\nTeleport to %s would be refused: %s\n", host, rep.Refused)
	case host == "":
		fmt.Fprintf(w, "\ndrift report: needs a destination — run: claude-teleport inspect %s --host <host>\n", shortID)
	}
}
