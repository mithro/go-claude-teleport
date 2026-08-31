// Package cli is the cobra command tree. It is the only package that reads
// the environment; every other package receives explicit directories.
package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// Exit codes (spec §5).
const (
	ExitOK          = 0
	ExitFailed      = 1
	ExitUsage       = 2
	ExitRefused     = 3
	ExitUnreachable = 4
	ExitNotResumed  = 5
	ExitInterrupted = 6
)

// ExitError carries the process exit code for an error.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// Exit builds an ExitError with a formatted message.
func Exit(code int, format string, a ...any) error {
	return &ExitError{Code: code, Err: fmt.Errorf(format, a...)}
}

func asExit(err error, target **ExitError) bool { return errors.As(err, target) }

// app is the per-invocation state shared by every command.
type app struct {
	stdin     io.Reader
	stdout    io.Writer
	stderr    io.Writer
	env       map[string]string
	configDir string // --config-dir (persistent flag)
}

func parseEnv(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// Main runs the CLI and returns the process exit code. It never calls
// os.Exit, so tests drive it directly.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, env []string) int {
	a := &app{stdin: stdin, stdout: stdout, stderr: stderr, env: parseEnv(env)}
	root := a.rootCmd()
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.Execute()
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		fmt.Fprintln(stderr, "claude-teleport:", ee.Err)
		return ee.Code
	}
	fmt.Fprintln(stderr, "claude-teleport:", err)
	return ExitUsage
}

// resolvePaths computes the local session.Paths from HOME, CLAUDE_CONFIG_DIR
// (overridden by --config-dir) and XDG_DATA_HOME.
func (a *app) resolvePaths() (session.Paths, error) {
	home := a.env["HOME"]
	if home == "" {
		return session.Paths{}, Exit(ExitUsage, "HOME is not set")
	}
	cfg := a.env["CLAUDE_CONFIG_DIR"]
	if a.configDir != "" {
		cfg = a.configDir
	}
	return session.NewPaths(home, cfg, a.env["XDG_DATA_HOME"]), nil
}

// rootCmd builds the command tree. Task 20 replaces the bare root with the
// full teleport command; every other task adds one subcommand here.
func (a *app) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "claude-teleport",
		Short:         "move an in-progress Claude Code session to another machine",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	root.PersistentFlags().StringVar(&a.configDir, "config-dir", "", "local CLAUDE_CONFIG_DIR override")
	root.AddCommand(a.versionCmd())
	root.AddCommand(a.internalFreezerCmd())
	root.AddCommand(a.placeholderCmd())
	return root
}
