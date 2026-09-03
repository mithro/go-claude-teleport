package cli

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/orchestrate"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// planSummary is the JSON-safe subset of orchestrate.Plan that inspect
// --host exposes (ruling R-P3-23d): never Plan.Extras (the raw
// ~/.claude.json project entry — including mcpServers env/auth headers —
// and the transcript History lines PutInstallExtras carries) or Plan.Files
// (redundant with the top-level inspectReport.Files/Memory, and no safer
// to expose raw) — only destination/target facts, file-transfer COUNTS and
// the drift report. claudecfg.Report.Diffs are already hash/short-redacted
// at construction (see claudecfg.configHash) so the full Report is safe to
// include as-is.
type planSummary struct {
	DestHost         string           `json:"dest_host"`
	TargetState      string           `json:"target_state"`
	FilesToSend      int              `json:"files_to_send"`
	FilesPresent     int              `json:"files_present"`
	FilesFastForward int              `json:"files_fast_forward"`
	FilesStaged      int              `json:"files_already_staged"`
	Drift            claudecfg.Report `json:"drift"`
}

func summarizePlan(p *orchestrate.Plan) *planSummary {
	s := &planSummary{DestHost: p.DestInfo.Hostname, TargetState: p.TargetState, Drift: p.Drift}
	for _, st := range p.Statuses {
		switch st {
		case transfer.Absent, transfer.StagedMismatch:
			s.FilesToSend++
		case transfer.FFCandidate:
			s.FilesToSend++
			s.FilesFastForward++
		case transfer.PresentSame:
			s.FilesPresent++
		case transfer.StagedSame:
			s.FilesStaged++
		}
	}
	return s
}

// inspectReport is everything `inspect` can show: the local inventory
// (files a teleport would move, git state) always, and — with --host —
// a safe summary of the preflight plan and drift table against that
// destination, or the refusal reason.
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
	Plan       *planSummary        `json:"plan,omitempty"`
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

// throwawayJobID returns a fresh, collision-free id for inspect --host's
// own preflight run (ruling R-P3-23b): Preflight writes
// jobs/<jobID>/manifest.json on BOTH hosts (via ManifestDiff on the
// destination, and directly on the driver side), so running it under the
// session's REAL id would clobber an interrupted job's manifest that
// `continue`'s git-attach and `abandon` depend on.
func throwawayJobID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("throwaway job id: %w", err)
	}
	return fmt.Sprintf("inspect-%x", b), nil
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
a real teleport would show it, or the refusal reason (exit 3). This uses a
throwaway job id, not the session's own — the destination need not have
anything to do with this session's teleport history, and any interrupted
job's own manifest is left untouched.`,
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
			// Memory files are counted in the same summary line ("%d
			// session files, %d memory files, %d bytes total"), so their
			// bytes belong in the total too (earlier deferred carry).
			for _, f := range append(append([]session.FileEntry{}, inv.Files...), inv.Memory...) {
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
			var plan *orchestrate.Plan // kept for Render(); never marshaled raw (ruling R-P3-23d)
			if host != "" {
				sshOpts, err := parseSSHOptions(opts)
				if err != nil {
					return usageErr(err)
				}
				jobID, err := throwawayJobID()
				if err != nil {
					return Exit(ExitFailed, "%v", err)
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
				// Best-effort clean-up of the throwaway job dir on both
				// hosts, regardless of preflight's outcome below — a
				// failure here is a warning, never a reason to change
				// inspect's own exit code.
				defer func() {
					if cerr := dst.Cleanup(ctx, jobID); cerr != nil {
						a.logf("inspect --host: destination staging clean-up of throwaway job %s: %v", jobID, cerr)
					}
					// R-P3-23i: Cleanup above only removes staging — the
					// job dir itself (manifest.json, ...) needs its own
					// removal, which only inspect's own throwaway
					// "inspect-"-prefixed ids are ever allowed to reach
					// (the wire dispatch handler refuses anything else).
					if jerr := dst.RemoveJob(ctx, jobID); jerr != nil {
						a.logf("inspect --host: destination job-dir clean-up of throwaway job %s: %v", jobID, jerr)
					}
					if rerr := os.RemoveAll(job.Dir(a.paths.DataDir, jobID)); rerr != nil {
						a.logf("inspect --host: local clean-up of throwaway job %s: %v", jobID, rerr)
					}
				}()
				var perr error
				plan, perr = orchestrate.Preflight(ctx, o, src, dst, jobID)
				var re *orchestrate.RefusedError
				switch {
				case errors.As(perr, &re):
					rep.Refused, code = re.Reason, ExitRefused
					plan = nil
				case perr != nil:
					return exitErr(a.fail(perr))
				default:
					rep.Plan = summarizePlan(plan)
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
			renderInspect(a.stdout, rep, host, plan)
			return exitErr(code)
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "also run preflight against HOST and show the plan and drift")
	remoteFlags(cmd, &via, &opts)
	return cmd
}

// renderInspect writes the human-readable report; a.json() short-circuits
// to JSON before this is ever called. plan (the real, un-redacted
// orchestrate.Plan — safe here since Render only ever prints counts and
// claudecfg's own already-redacted drift table, never raw Extras/Files)
// is nil unless preflight against --host actually succeeded.
func renderInspect(w io.Writer, rep *inspectReport, host string, plan *orchestrate.Plan) {
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
	case plan != nil:
		fmt.Fprintf(w, "\nPlan against %s\n", host)
		plan.Render(w)
	case rep.Refused != "":
		fmt.Fprintf(w, "\nTeleport to %s would be refused: %s\n", host, rep.Refused)
	case host == "":
		fmt.Fprintf(w, "\ndrift report: needs a destination — run: claude-teleport inspect %s --host <host>\n", shortID)
	}
}
