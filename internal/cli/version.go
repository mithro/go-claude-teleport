package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/version"
)

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print the claude-teleport version and protocol number",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintf(a.stdout, "claude-teleport %s (protocol %d) %s/%s\n",
				version.Version, version.Protocol, runtime.GOOS, runtime.GOARCH)
			return err
		},
	}
}
