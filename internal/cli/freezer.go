package cli

import (
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

// internalFreezerCmd is re-exec'd by procx.Freeze with the control pipe on fd 3.
func (a *app) internalFreezerCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "internal-freezer <pid> <start-time> [tmux-socket pane-id]",
		Short:  "internal: SIGSTOP a pid until fd 3 reports thaw or EOF",
		Hidden: true,
		// The optional pair is the pane the target runs in: with it, the
		// helper restores that pane's foreground after the SIGCONT it
		// sends when its owner dies (R-P3-F1). Without it (no tmux) it
		// only ever thaws.
		Args: cobra.RangeArgs(2, 4),
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := strconv.Atoi(args[0])
			if err != nil {
				return Exit(ExitUsage, "internal-freezer: bad pid %q", args[0])
			}
			if len(args) == 3 {
				return Exit(ExitUsage, "internal-freezer: a pane id must accompany the tmux socket path")
			}
			var restore procx.RestoreFunc
			if len(args) == 4 {
				restore = tmuxx.FreezerRestore(args[2], args[3])
			}
			if err := procx.RunFreezerHelper(pid, args[1], os.NewFile(3, "control"), restore); err != nil {
				return Exit(ExitFailed, "%v", err)
			}
			return nil
		},
	}
}
