// internal/cli/endpoints.go
package cli

import (
	"context"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/orchestrate"
	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

// envSlice reconstructs a "KEY=VALUE" slice from a.env, for the free
// functions in transport.go (dialTarget, tmuxSocketDir, ...) that predate
// app and still take []string.
func envSlice(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// localEndpoint builds this host's Local with a pane probe when a tmux
// server is reachable ($TMUX first, then spec §9 discovery). Any dialled
// control connection is released by a.closers (Main runs them after
// root.Execute()).
func (a *app) localEndpoint(ctx context.Context, p session.Paths) *remote.Local {
	env := envSlice(a.env)
	opts := remote.LocalOptions{ProcRoot: "/proc", Tmux: tmuxx.DialControl, TmuxSocketDir: tmuxSocketDir(env), Logf: a.logf}
	sock := ""
	if t := a.env["TMUX"]; t != "" {
		sock = strings.SplitN(t, ",", 2)[0]
	} else if s, err := tmuxx.FindServer(opts.TmuxSocketDir, "", ""); err == nil {
		sock = s
	}
	if sock != "" {
		if tr, err := tmuxx.DialControl(ctx, sock); err == nil {
			if procs, err := procx.Scan("/proc"); err == nil {
				opts.Probe = tmuxx.Prober(ctx, tr, procs, sock)
				a.closers = append(a.closers, tr.Close)
			}
		}
	}
	return remote.NewLocal(p, a.selfExe, opts)
}

// dialRemote resolves and dials o.Target through o.Via, returning the
// remote endpoint (a `claude-teleport remote serve` on the far side). It
// is a thin wrapper over dialTarget (transport.go) — the ssh resolve/dial/
// jump-chain logic lives there exactly once.
func (a *app) dialRemote(ctx context.Context, o orchestrate.Options) (*remote.Client, func(), error) {
	specs := make([]string, 0, len(o.SSHOptions))
	for k, v := range o.SSHOptions {
		specs = append(specs, k+"="+v)
	}
	c, _, err := dialTarget(ctx, o.Target, o.Via, specs, envSlice(a.env), a.logf)
	if err != nil {
		return nil, nil, err
	}
	ep, err := remote.NewClient(ctx, c, "claude-teleport", a.logf)
	if err != nil {
		c.Close()
		return nil, nil, fail(ExitUnreachable, "%s: %v", o.Target, err)
	}
	return ep, func() { ep.Close(); c.Close() }, nil
}

// endpoints is the orchestrate.EndpointFactory: local on this side, ssh
// (or Options.LocalDest, tests only) on the other, ordered by Direction.
func (a *app) endpoints(ctx context.Context, o orchestrate.Options) (remote.Endpoint, remote.Endpoint, func(), error) {
	if err := a.ensurePaths(); err != nil {
		return nil, nil, nil, err
	}
	local := a.localEndpoint(ctx, a.paths)
	var other remote.Endpoint
	closeFn := func() {}
	if o.LocalDest != nil {
		other = a.localEndpoint(ctx, *o.LocalDest)
	} else {
		c, cl, err := a.dialRemote(ctx, o)
		if err != nil {
			return nil, nil, nil, err
		}
		other, closeFn = c, cl
	}
	if o.Direction == "from" {
		return other, local, closeFn, nil
	}
	return local, other, closeFn, nil
}
