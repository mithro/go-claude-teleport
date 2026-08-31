package cli

import (
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/procx"
)

// internalFreezerCmd is re-exec'd by procx.Freeze with the control pipe on fd 3.
func (a *app) internalFreezerCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "internal-freezer <pid> <start-time>",
		Short:  "internal: SIGSTOP a pid until fd 3 reports thaw or EOF",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := strconv.Atoi(args[0])
			if err != nil {
				return Exit(ExitUsage, "internal-freezer: bad pid %q", args[0])
			}
			if err := procx.RunFreezerHelper(pid, args[1], os.NewFile(3, "control")); err != nil {
				return Exit(ExitFailed, "%v", err)
			}
			return nil
		},
	}
}
