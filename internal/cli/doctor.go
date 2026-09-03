package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/orchestrate"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/version"
)

type check struct {
	name, detail string
	ok           bool
}

// localChecks runs the doctor checks that need no remote host. It never
// reads .credentials.json or sessions/*.key, and never prints a secret
// VALUE — "logged in" is inferred from file existence only (ruling: doctor
// must not surface credential contents).
func (a *app) localChecks() []check {
	var cs []check
	env := a.envSlice()
	pathLookup := func(bin string) (string, error) { return lookInPath(a.env["PATH"], bin) }
	if p, err := pathLookup("claude"); err != nil {
		cs = append(cs, check{"claude on PATH", "not found (install Claude Code and log in)", false})
	} else if v, err := claudeVersionFn(env); err != nil {
		cs = append(cs, check{"claude on PATH", p + " but --version failed: " + err.Error(), false})
	} else {
		cs = append(cs, check{"claude on PATH", p + " (" + v + ")", true})
	}
	if p, err := pathLookup("tmux"); err != nil {
		cs = append(cs, check{"tmux on PATH", "not found (optional: needed to move tmux windows)", true})
	} else {
		cs = append(cs, check{"tmux on PATH", p, true})
	}
	paths, err := a.resolvePaths()
	if err != nil {
		return append(cs, check{"config dir", err.Error(), false})
	}
	if fi, err := os.Stat(paths.ProjectsDir()); err != nil || !fi.IsDir() {
		cs = append(cs, check{"config dir", paths.ConfigDir + " has no projects/ (has Claude ever run here?)", false})
	} else {
		cs = append(cs, check{"config dir", paths.ConfigDir, true})
	}
	if _, err := os.Stat(paths.GlobalJSON); err != nil {
		cs = append(cs, check{"global json", paths.GlobalJSON + " absent (Claude has not run, or a different CLAUDE_CONFIG_DIR)", true})
	} else {
		cs = append(cs, check{"global json", paths.GlobalJSON, true})
	}
	if err := os.MkdirAll(paths.DataDir, 0o700); err != nil {
		cs = append(cs, check{"data dir writable", err.Error(), false})
	} else if f, err := os.CreateTemp(paths.DataDir, ".doctor-*"); err != nil {
		cs = append(cs, check{"data dir writable", err.Error(), false})
	} else {
		f.Close()
		os.Remove(f.Name())
		cs = append(cs, check{"data dir writable", paths.DataDir, true})
	}
	return cs
}

// remoteChecks dials host and runs the same kind of checks against it: it
// takes an already-connected remote.Endpoint so the logic is testable
// against an in-process Local standing in for "the remote host" (as the
// rest of Plan 03 does), with the real ssh dial living entirely in
// newDoctorCmd's RunE.
func remoteChecks(ctx context.Context, ep remote.Endpoint, host string) ([]check, error) {
	hi, err := ep.Hello(ctx)
	if err != nil {
		return nil, fmt.Errorf("hello %s: %w", host, err)
	}
	var cs []check
	cs = append(cs, check{"remote claude-teleport", fmt.Sprintf("%s (local %s)", hi.Version, version.Version), hi.Version == version.Version})
	cs = append(cs, check{"remote claude", hi.ClaudeVersion, hi.HasClaude})
	cs = append(cs, check{"remote tmux", fmt.Sprintf("present: %v", hi.HasTmux), true})
	cs = append(cs, check{"remote claude-resume", fmt.Sprintf("present: %v; home %s; config %s", hi.HasClaudeResume, hi.Home, hi.ConfigDir), true})
	rows, err := ep.ListSessions(ctx)
	cs = append(cs, check{"remote sessions", fmt.Sprintf("%d listable", len(rows)), err == nil})
	return cs, nil
}

// newDoctorCmd checks local prerequisites and, with a host, the same kind
// of thing there (spec §5 `doctor`): exit 4 when the host is unreachable,
// 1 when any check (local or remote) fails.
func newDoctorCmd(a *app) *cobra.Command {
	var via, opts []string
	cmd := &cobra.Command{
		Use:   "doctor [<host>]",
		Short: "check local (and remote) prerequisites",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			failed := false
			print := func(cs []check) {
				for _, c := range cs {
					status := "ok  "
					if !c.ok {
						status, failed = "FAIL", true
					}
					fmt.Fprintf(a.stdout, "%s  %-18s %s\n", status, c.name, c.detail)
				}
			}
			print(a.localChecks())
			if len(args) == 1 {
				host := args[0]
				sshOpts, err := parseSSHOptions(opts)
				if err != nil {
					return usageErr(err)
				}
				ep, closeFn, err := a.dialRemote(ctx, orchestrate.Options{Target: host, Via: via, SSHOptions: sshOpts})
				if err != nil {
					return exitErr(a.fail(err))
				}
				defer closeFn()
				cs, err := remoteChecks(ctx, ep, host)
				if err != nil {
					return Exit(ExitUnreachable, "%v", err)
				}
				print(cs)
			}
			if failed {
				return Exit(ExitFailed, "doctor found problems")
			}
			return nil
		},
	}
	remoteFlags(cmd, &via, &opts)
	return cmd
}
