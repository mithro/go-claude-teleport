package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/remote"
)

// openRemote dials host (through any --via jump hosts and -o overrides)
// and starts `claude-teleport remote serve` there over that connection, the
// shared setup for compare-config's remote branch and inspect --host. The
// returned func closes both the remote helper and the underlying ssh
// connection; callers must defer it once err is nil.
func openRemote(cmd *cobra.Command, host string, via, opts []string) (*remote.Client, func(), error) {
	ctx := cmd.Context()
	e := envOf(cmd)
	logf := stderrLogf(e.stderr)
	sc, _, err := dialTarget(ctx, host, via, opts, e.env, logf)
	if err != nil {
		return nil, nil, err
	}
	rc, err := remote.NewClient(ctx, sc, "claude-teleport", logf)
	if err != nil {
		sc.Close()
		var pe *remote.Error
		if errors.As(err, &pe) && pe.Code == "usage" {
			return nil, nil, fail(ExitUnreachable, "%s: %v", host, err)
		}
		return nil, nil, fail(ExitUnreachable, "%s: start remote helper: %v (is claude-teleport installed there?)", host, err)
	}
	return rc, func() { rc.Close(); sc.Close() }, nil
}

// remoteFlags adds the --via/-o pair shared by compare-config's remote
// branch and inspect --host, in the same style as the root command's flags.
func remoteFlags(cmd *cobra.Command, via, opts *[]string) {
	cmd.Flags().StringArrayVar(via, "via", nil, "jump host (repeatable, outermost first)")
	cmd.Flags().StringArrayVarP(opts, "option", "o", nil, "ssh option KEY=VALUE")
}
