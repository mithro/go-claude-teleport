package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
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
entirely locally.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			// Check if target is remote (hostname) before doing other operations
			info, statErr := os.Stat(target)
			if !filepath.IsAbs(target) || statErr != nil || !info.IsDir() {
				// A hostname: needs the Plan 02 transport (hello + inventory-host).
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
			if destHome == "" {
				destHome = filepath.Dir(target)
			}
			dstPaths := session.NewPaths(destHome, target, "")
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
	return cmd
}
