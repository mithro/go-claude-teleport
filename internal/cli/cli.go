// Package cli is the cobra command tree. It is the only package that reads
// the environment; every other package receives explicit directories.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/orchestrate"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// Exit codes (spec §5). The three a job journal decides are orchestrate's
// (ExitCode maps outcome -> code there); the rest are decided before or
// outside any job. Re-exported, not re-numbered, so the numbers exist in
// exactly one place (finding A13).
const (
	ExitOK          = orchestrate.ExitOK
	ExitFailed      = orchestrate.ExitFailed
	ExitUsage       = 2
	ExitRefused     = 3
	ExitUnreachable = 4
	ExitNotResumed  = orchestrate.ExitNotResumed
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
	configDir string         // --config-dir (persistent flag)
	flags     *teleportFlags // root flags incl. the persistent --json/-v/-q

	// Additions (Task 21): paths is resolved lazily by ensurePaths() (so
	// commands that never need it, like `version`, still work with no
	// HOME); selfExe and logf are set once by Main; closers collects
	// release functions (ssh connections, a dialled tmux control
	// connection) that Main runs after root.Execute() regardless of
	// outcome.
	paths   session.Paths
	selfExe string
	logf    func(string, ...any)
	closers []func() error
}

// ensurePaths resolves a.paths (honouring --config-dir) the first time a
// command needs it; a command that never touches paths (version, a bad
// --help invocation, ...) is unaffected by a missing HOME.
func (a *app) ensurePaths() error {
	if a.paths.Home != "" {
		return nil
	}
	p, err := a.resolvePaths()
	if err != nil {
		return err
	}
	a.paths = p
	return nil
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
	a.selfExe = selfExe()
	a.logf = stderrLogf(stderr)
	root := a.rootCmd()
	root.SetContext(context.WithValue(context.Background(), cmdEnvKey{}, cmdEnv{env: env, stdin: stdin, stdout: stdout, stderr: stderr}))
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.Execute()
	for _, c := range a.closers {
		_ = c()
	}
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		// errReported (teleport.go) marks an error a command already
		// printed itself (a.fail); an empty message here would otherwise
		// print a bare "claude-teleport: " a second time.
		if ee.Err != nil && ee.Err.Error() != "" {
			fmt.Fprintln(stderr, "claude-teleport:", ee.Err)
		}
		return ee.Code
	}
	fmt.Fprintln(stderr, "claude-teleport:", err)
	return ExitFailed
}

// nextHint prints the canonical "what to do next" line for a job that has
// not finished successfully (status/continue/abandon) — the one place
// that spells it out, so no call site hand-rolls its own hint string.
func nextHint(w io.Writer, id string) {
	fmt.Fprintf(w, "next: claude-teleport status %s | claude-teleport continue %s | claude-teleport abandon %s\n", id, id, id)
}

// helpPointer is nextHint's counterpart for someone who hasn't told us
// anything yet: the bare `claude-teleport` invocation (root.go's
// teleportFlags.validate, bare==true) gets the usual "exactly one of --to/
// --from is required" message, but that alone doesn't point anywhere —
// unlike every other usage mistake, which at least named a flag the user
// can go look up. This is the one case that's genuinely lost, so it's the
// one that gets a pointer to --help appended.
func helpPointer() string { return " (see 'claude-teleport --help')" }

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
