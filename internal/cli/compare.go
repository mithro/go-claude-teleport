package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// lookInPath finds bin in a PATH string (exec.LookPath consults the process
// environment; we must honour the environment handed to Main).
func lookInPath(pathEnv, bin string) (string, error) {
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, bin)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: %w", bin, exec.ErrNotFound)
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// claudeVersionFn runs `claude --version` (the one subprocess besides tmux,
// spec §10) with the given environment; swappable in tests.
var claudeVersionFn = func(env []string) (string, error) {
	bin, err := lookInPath(envValue(env, "PATH"), "claude")
	if err != nil {
		return "", err
	}
	cmd := exec.Command(bin, "--version")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude --version: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (a *app) envSlice() []string {
	out := make([]string, 0, len(a.env))
	for k, v := range a.env {
		out = append(out, k+"="+v)
	}
	return out
}

// localClaudeVersion prefers the running session's registry, then the
// transcript, then `claude --version`.
func (a *app) localClaudeVersion(s *session.Session) (string, error) {
	if s != nil && s.Registry != nil && s.Registry.Version != "" {
		return s.Registry.Version, nil
	}
	if s != nil && s.Version != "" {
		return s.Version, nil
	}
	return claudeVersionFn(a.envSlice())
}

func (a *app) compareConfigCmd() *cobra.Command {
	var sel, destHome string
	var allowDrift bool
	var via, opts []string
	cmd := &cobra.Command{
		Use:   "compare-config <host> [--session <session>]",
		Short: "compare Claude configuration here with a destination and classify the drift",
		Long: `Prints the configuration drift table (hooks, permissions, MCP servers,
plugins, skills, sub-agents, model, CLAUDE.md, ...) between this host and
the destination. With --session only what that session used can block;
without it everything counts as used. Exit 3 when anything blocks unless
--allow-config-drift is given.

<host> may also be an absolute path to a Claude config directory on this
machine (with --dest-home for its home directory); the comparison then runs
entirely locally. Anything else is dialled as an ssh target (--via/-o work
as they do for a teleport).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			// Check if target is remote (hostname) before doing other operations.
			info, statErr := os.Stat(target)
			switch {
			case !filepath.IsAbs(target):
				// A hostname: dial it over the Plan 02 remote transport.
				return a.compareConfigRemote(cmd, target, via, opts, sel, allowDrift)
			case statErr != nil && !errors.Is(statErr, fs.ErrNotExist):
				// The path exists as far as the filesystem is concerned but Stat
				// itself failed (permissions, a symlink loop, ...): surface that,
				// don't silently reinterpret it as "must be a remote hostname".
				return Exit(ExitFailed, "stat %s: %v", target, statErr)
			case statErr != nil || !info.IsDir():
				return Exit(ExitUsage, "compare-config %s: remote comparison not implemented yet (an absolute config-dir path works locally)", target)
			}
			p, err := a.resolvePaths()
			if err != nil {
				return err
			}
			var s *session.Session
			cwd := a.env["PWD"]
			if sel != "" {
				if s, err = a.resolveSession(strings.Fields(sel)); err != nil {
					return err
				}
				cwd = s.LaunchCwd
			} else if cwd == "" {
				if cwd, err = os.Getwd(); err != nil {
					return Exit(ExitFailed, "getwd: %v", err)
				}
			}
			var usage *session.Usage
			if s != nil {
				if usage, err = session.ScanUsage(s); err != nil {
					return Exit(ExitFailed, "%v", err)
				}
			}
			ver, err := a.localClaudeVersion(s)
			if err != nil {
				return Exit(ExitFailed, "%v", err)
			}
			src, err := claudecfg.Collect(p, cwd, "local", ver)
			if err != nil {
				return Exit(ExitFailed, "%v", err)
			}
			// Build the destination Paths explicitly: NewPaths' CLAUDE_CONFIG_DIR
			// heuristic (GlobalJSON always inside ConfigDir) is wrong for a normal
			// home layout, where ~/.claude.json sits next to ~/.claude. With
			// --dest-home, prefer <destHome>/.claude.json when it exists; without
			// it (or when that file is absent) fall back to <target>/.claude.json,
			// as before.
			destHomeGiven := destHome != ""
			effectiveHome := destHome
			if effectiveHome == "" {
				effectiveHome = filepath.Dir(target)
			}
			dstPaths := session.Paths{Home: effectiveHome, ConfigDir: target, GlobalJSON: filepath.Join(target, ".claude.json")}
			if destHomeGiven {
				if fi, err := os.Stat(filepath.Join(effectiveHome, ".claude.json")); err == nil && !fi.IsDir() {
					dstPaths.GlobalJSON = filepath.Join(effectiveHome, ".claude.json")
				}
			}
			dst, err := claudecfg.Collect(dstPaths, cwd, target, ver)
			if err != nil {
				return Exit(ExitFailed, "%v", err)
			}
			rep := claudecfg.Compare(src, dst, usage)
			if allowDrift {
				rep = rep.Downgrade()
			}
			if a.json() {
				b, err := rep.JSON()
				if err != nil {
					return Exit(ExitFailed, "%v", err)
				}
				fmt.Fprintln(a.stdout, string(b))
			} else {
				rep.Render(a.stdout)
			}
			if rep.Blocking {
				return Exit(ExitRefused, "configuration drift would block a teleport")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&sel, "session", "", "session selector; limits blocking to what the session used")
	cmd.Flags().StringVar(&destHome, "dest-home", "", "home directory for a local destination config dir")
	cmd.Flags().BoolVar(&allowDrift, "allow-config-drift", false, "downgrade blocking drift to warnings")
	remoteFlags(cmd, &via, &opts)
	return cmd
}

// compareConfigRemote is the compareConfigCmd branch for a <host> that
// isn't a local config-dir path: it dials host over the Plan 02 remote
// transport and compares this machine's inventory with the remote's,
// keeping --session usage analysis, --allow-config-drift, --json and
// ExitRefused-on-blocking identical to the local branch above.
func (a *app) compareConfigRemote(cmd *cobra.Command, host string, via, opts []string, sel string, allowDrift bool) error {
	ctx := cmd.Context()
	p, err := a.resolvePaths()
	if err != nil {
		return err
	}
	var s *session.Session
	cwd := a.env["PWD"]
	if sel != "" {
		if s, err = a.resolveSession(strings.Fields(sel)); err != nil {
			return err
		}
		cwd = s.LaunchCwd
	} else if cwd == "" {
		if cwd, err = os.Getwd(); err != nil {
			return Exit(ExitFailed, "getwd: %v", err)
		}
	}
	var usage *session.Usage
	if s != nil {
		if usage, err = session.ScanUsage(s); err != nil {
			return Exit(ExitFailed, "%v", err)
		}
	}
	local := remote.NewLocal(p, selfExe(), remote.LocalOptions{ProcRoot: "/proc", Logf: stderrLogf(a.stderr)})
	localInfo, _ := local.Hello(ctx)
	src, err := local.InventoryHost(ctx, cwd, localInfo.ClaudeVersion)
	if err != nil {
		return Exit(ExitFailed, "local inventory: %v", err)
	}
	rc, closeRemote, err := openRemote(cmd, host, via, opts)
	if err != nil {
		return err
	}
	defer closeRemote()
	dst, err := rc.InventoryHost(ctx, cwd, rc.Info().ClaudeVersion)
	if err != nil {
		return Exit(ExitFailed, "%s inventory: %v", host, err)
	}
	rep := claudecfg.Compare(src, dst, usage)
	if allowDrift {
		rep = rep.Downgrade()
	}
	if a.json() {
		b, err := rep.JSON()
		if err != nil {
			return Exit(ExitFailed, "%v", err)
		}
		fmt.Fprintln(a.stdout, string(b))
	} else {
		rep.Render(a.stdout)
	}
	if rep.Blocking {
		return Exit(ExitRefused, "configuration drift would block a teleport")
	}
	return nil
}
