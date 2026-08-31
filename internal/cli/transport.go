package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kevinburke/ssh_config"
	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/sshx"
)

// RunnerSteps is registered by Plan 03's orchestrator; nil means the
// detached runner cannot run any job yet.
var RunnerSteps func(j *job.Journal, logf func(string, ...any)) ([]job.Step, error)

// fail is shorthand for Plan 01's cli.Exit: it returns a *cli.ExitError
// carrying a spec §5 exit code out of a cobra RunE (Plan 01's Main maps it).
func fail(code int, format string, args ...any) error {
	return Exit(code, format, args...)
}

// envValue(env []string, key string) string is defined by Plan 01
// (internal/cli/compare.go, Task 21); do not redefine it here.

// envPaths derives session.Paths from an environment slice using Plan 01's
// session.NewPaths (the one place the CLAUDE_CONFIG_DIR / .claude.json rule
// lives); missing $HOME is an error.
func envPaths(env []string) (session.Paths, error) {
	home := envValue(env, "HOME")
	if home == "" {
		return session.Paths{}, errors.New("HOME is not set")
	}
	return session.NewPaths(home, envValue(env, "CLAUDE_CONFIG_DIR"), envValue(env, "XDG_DATA_HOME")), nil
}

// cmdEnv is how Plan 01's Main hands the env slice and stdio to commands:
// it stores them in the root command's context under this key (Main is
// modified in this task to do so; see the wiring note below).
type cmdEnvKey struct{}

type cmdEnv struct {
	env    []string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func envOf(cmd *cobra.Command) cmdEnv {
	if v, ok := cmd.Context().Value(cmdEnvKey{}).(cmdEnv); ok {
		return v
	}
	return cmdEnv{env: os.Environ(), stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}
}

func stderrLogf(w io.Writer) func(string, ...any) {
	return func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }
}

func selfExe() string {
	exe, err := os.Executable()
	if err != nil {
		return "claude-teleport"
	}
	return exe
}

// tmuxSocketDir resolves $TMUX_TMPDIR, defaulting to /tmp/tmux-<uid> when
// unset (internal/cli is the one place allowed to read this).
func tmuxSocketDir(env []string) string {
	if d := envValue(env, "TMUX_TMPDIR"); d != "" {
		return d
	}
	return fmt.Sprintf("/tmp/tmux-%d", os.Getuid())
}

// dialTarget resolves target (with --via hops and -o overrides) through
// ~/.ssh/config and dials it with 3 attempts. Honours -o UserKnownHostsFile
// and -o StrictHostKeyChecking (via Resolved.Options). Exit code 2 for bad
// input, 4 when the host cannot be reached or authenticated.
func dialTarget(ctx context.Context, target string, via []string, opts []string, env []string, logf func(string, ...any)) (*sshx.Client, sshx.Resolved, error) {
	t, err := sshx.ParseTarget(target)
	if err != nil {
		return nil, sshx.Resolved{}, fail(ExitUsage, "%v", err)
	}
	for _, v := range via {
		vt, err := sshx.ParseTarget(v)
		if err != nil {
			return nil, sshx.Resolved{}, fail(ExitUsage, "--via %q: %v", v, err)
		}
		t.Via = append(t.Via, vt)
	}
	overrides := map[string]string{}
	for _, o := range opts {
		k, v, ok := strings.Cut(o, "=")
		if !ok || k == "" {
			return nil, sshx.Resolved{}, fail(ExitUsage, "-o %q: want KEY=VALUE", o)
		}
		overrides[k] = v
	}
	home := envValue(env, "HOME")
	var cfg *ssh_config.Config
	if f, err := os.Open(filepath.Join(home, ".ssh", "config")); err == nil {
		cfg, err = ssh_config.Decode(f)
		f.Close()
		if err != nil {
			return nil, sshx.Resolved{}, fail(ExitUsage, "~/.ssh/config: %v", err)
		}
	}
	localUser := envValue(env, "USER")
	if localUser == "" {
		localUser = "root"
	}
	r, err := sshx.Resolve(t, cfg, overrides, localUser)
	if err != nil {
		return nil, sshx.Resolved{}, fail(ExitUsage, "%v", err)
	}
	o := sshx.Options{
		KnownHostsFile: filepath.Join(home, ".ssh", "known_hosts"),
		AgentSocket:    envValue(env, "SSH_AUTH_SOCK"),
		Home:           home,
		Logf:           logf,
	}
	if kh, ok := r.Options["UserKnownHostsFile"]; ok {
		o.KnownHostsFile = kh
	}
	c, err := sshx.Redial(ctx, 3, 500*time.Millisecond, logf, func(ctx context.Context) (*sshx.Client, error) {
		return sshx.Dial(ctx, r, cfg, overrides, o)
	})
	if err != nil {
		return nil, r, fail(ExitUnreachable, "%v", err)
	}
	return c, r, nil
}

// AddTransportCommands registers remote serve|stream and internal-runner.
func AddTransportCommands(root *cobra.Command) {
	root.AddCommand(statusCmd())
	root.AddCommand(abandonCmd())

	remoteCmd := &cobra.Command{Use: "remote", Short: "internal: remote helper", Hidden: true}
	remoteCmd.AddCommand(&cobra.Command{
		Use:  "serve",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := envOf(cmd)
			p, err := envPaths(e.env)
			if err != nil {
				return fail(ExitUsage, "%v", err)
			}
			local := remote.NewLocal(p, selfExe(), remote.LocalOptions{ProcRoot: "/proc", TmuxSocketDir: tmuxSocketDir(e.env), Logf: stderrLogf(e.stderr)})
			if err := remote.Serve(cmd.Context(), e.stdin, e.stdout, local); err != nil {
				return fail(ExitFailed, "remote serve: %v", err)
			}
			return nil
		},
	})
	remoteCmd.AddCommand(&cobra.Command{
		Use:  "stream <kind> <job> <stream-id>",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := envOf(cmd)
			p, err := envPaths(e.env)
			if err != nil {
				return fail(ExitUsage, "%v", err)
			}
			local := remote.NewLocal(p, selfExe(), remote.LocalOptions{ProcRoot: "/proc", TmuxSocketDir: tmuxSocketDir(e.env), Logf: stderrLogf(e.stderr)})
			if err := remote.ServeStream(cmd.Context(), remote.StreamKind(args[0]), args[1], args[2], e.stdin, e.stdout, local); err != nil {
				return fail(ExitFailed, "%v", err)
			}
			return nil
		},
	})
	root.AddCommand(remoteCmd)

	root.AddCommand(&cobra.Command{
		Use:    "internal-runner <job-dir>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := envOf(cmd)
			jobDir := filepath.Clean(args[0])
			id := filepath.Base(jobDir)
			dataDir := filepath.Dir(filepath.Dir(jobDir))
			j, found, err := job.Open(dataDir, id)
			if err != nil {
				return fail(ExitFailed, "%v", err)
			}
			if !found {
				return fail(ExitFailed, "no journal at %s", jobDir)
			}
			logf := stderrLogf(e.stderr)
			if RunnerSteps == nil {
				return fail(ExitFailed, "internal-runner: no steps registered for job %s (orchestrator arrives in Plan 03)", id)
			}
			steps, err := RunnerSteps(j, logf)
			if err != nil {
				return fail(ExitFailed, "%v", err)
			}
			j.RunnerPID = os.Getpid()
			if err := j.Save(); err != nil {
				return fail(ExitFailed, "%v", err)
			}
			if err := job.Run(cmd.Context(), j, steps, logf); err != nil {
				return fail(ExitFailed, "%v", err)
			}
			return nil
		},
	})
}
