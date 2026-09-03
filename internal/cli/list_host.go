package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/mithro/go-claude-teleport/internal/orchestrate"
	"github.com/mithro/go-claude-teleport/internal/remote"
)

// renderRemoteSessions writes ep.ListSessions(ctx) in the same table/JSON
// shape `list` uses locally, with an extra HOST column. It is factored out
// from the ssh dial (listRemote, below) so it is directly testable against
// an in-process Local standing in for "the remote host" — the same pattern
// the rest of Plan 03 uses (Options.LocalDest, a.endpoints).
func renderRemoteSessions(ctx context.Context, w io.Writer, ep remote.Endpoint, host string, asJSON bool) error {
	rows, err := ep.ListSessions(ctx)
	if err != nil {
		return err
	}
	if asJSON {
		out := make([]listRow, len(rows))
		for i, r := range rows {
			out[i] = listRow{ID: string(r.ID), State: r.State, Name: r.Name, Cwd: r.Cwd, Branch: r.Branch, Last: r.LastTS, Tmux: r.Tmux, Host: host}
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(w, string(b))
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tID\tSTATE\tNAME\tCWD\tBRANCH\tLAST ACTIVE")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", host, r.ID.Short(), r.State, r.Name, r.Cwd, r.Branch, r.LastTS)
	}
	return tw.Flush()
}

// listRemote dials host (spec §5 `list --host`; --via/-o work as they do
// for a teleport) and renders its sessions.
func (a *app) listRemote(ctx context.Context, host string, via, opts []string, asJSON bool) error {
	sshOpts, err := parseSSHOptions(opts)
	if err != nil {
		return usageErr(err)
	}
	ep, closeFn, err := a.dialRemote(ctx, orchestrate.Options{Target: host, Via: via, SSHOptions: sshOpts})
	if err != nil {
		return exitErr(a.fail(err))
	}
	defer closeFn()
	if err := renderRemoteSessions(ctx, a.stdout, ep, host, asJSON); err != nil {
		return Exit(ExitFailed, "%s: %v", host, err)
	}
	return nil
}
