package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/placeholder"
)

func (a *app) placeholderCmd() *cobra.Command {
	var o placeholder.Options
	cmd := &cobra.Command{
		Use:   "placeholder --resume <sid> [--saved-output F] [--now] [--teleported-to HOST] [--teleported-at TS]",
		Short: "confirm, then resume a specific Claude session (typed into a pane by a teleport)",
		Long: `Shows which conversation the pane held (project, branch, title, last
active) and waits for Enter before exec'ing "claude --resume <sid>" from the
session's launch directory. Ctrl-C leaves a shell in the pane. When stdin is
not a terminal the resume happens immediately.

--saved-output prints a pane capture above the banner so the pane looks as
it did before; --now skips the wait; --teleported-to/--teleported-at say
where the session went and warn that resuming here forks it.

The argv contains "--resume <uuid>", so go-tmux-saver's process resolver
classifies the pane as a Claude pane and saves/restores it as such.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.SessionID == "" {
				return Exit(ExitUsage, "placeholder: --resume <sid> is required")
			}
			p, err := a.resolvePaths()
			if err != nil {
				return err
			}
			o.ProjectsDir, o.Home = p.ProjectsDir(), p.Home
			d := placeholder.Decide(a.stdout, o, stdoutTTYFn(), stdinTTYFn(),
				func() (string, error) { return readLineInterruptible(a.stdin) })
			if d.Skip {
				return nil
			}
			if d.Chdir != "" {
				if err := chdirFn(d.Chdir); err != nil {
					fmt.Fprintln(a.stderr, "placeholder: chdir:", err)
					return Exit(ExitFailed, "")
				}
			}
			path, err := lookPathFn(d.Argv[0])
			if err != nil {
				return Exit(ExitFailed, "placeholder: `claude` not found on PATH")
			}
			if err := execveFn(path, d.Argv, os.Environ()); err != nil {
				return Exit(ExitFailed, "placeholder: exec %s: %v", path, err)
			}
			return nil // unreachable in production: exec replaced the process
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.SessionID, "resume", "", "session id to resume (required)")
	f.StringVar(&o.SavedOutput, "saved-output", "", "print this file's content above the banner")
	f.BoolVar(&o.Now, "now", false, "do not wait for Enter")
	f.StringVar(&o.TeleportedTo, "teleported-to", "", "host the session was teleported to")
	f.StringVar(&o.TeleportedAt, "teleported-at", "", "when it was teleported (ISO 8601)")
	return cmd
}
