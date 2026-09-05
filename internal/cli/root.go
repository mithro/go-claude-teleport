package cli

import (
	"errors"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// teleportFlags holds every option of the teleport command (spec §5).
// validate applies the cross-flag rules below; teleportOptions (teleport.go,
// Task 21) turns a validated set into orchestrate.Options.
type teleportFlags struct {
	To, From       string
	Via            []string
	SSHOptions     []string // -o KEY=VALUE
	DestPath       string
	Maps           []string
	State          string
	AllowDrift     bool
	Force          bool
	TmuxSocket     string
	NoTmux         bool
	NoBootstrap    bool
	Excludes       []string
	IncludeIgnored bool
	DryRun         bool
	ExitTimeout    time.Duration
	StartTimeout   time.Duration
	LogFile        string
	JSON           bool
	Verbose        bool
	Quiet          bool
}

var validStates = map[string]bool{"auto": true, "running": true, "suspended": true, "idle": true}

// validate applies the cross-flag rules; it returns usage errors. bare is
// true only for the literal zero-argument invocation (`claude-teleport`,
// nothing else) — see helpPointer's doc comment.
func (f *teleportFlags) validate(args []string, bare bool) error {
	if (f.To == "") == (f.From == "") {
		msg := "exactly one of --teleport-to/--to or --teleport-from/--from is required"
		if bare {
			msg += helpPointer()
		}
		return Exit(ExitUsage, "%s", msg)
	}
	if !validStates[f.State] {
		return Exit(ExitUsage, "--state must be auto, running, suspended or idle (got %q)", f.State)
	}
	if f.NoTmux && f.State != "idle" && f.State != "auto" {
		return Exit(ExitUsage, "--no-tmux allows only --state idle (got %q)", f.State)
	}
	if f.Verbose && f.Quiet {
		return Exit(ExitUsage, "--verbose and --quiet are mutually exclusive")
	}
	if _, err := session.ParseMappings(f.Maps); err != nil {
		return Exit(ExitUsage, "%v", err)
	}
	if len(args) > 2 {
		return Exit(ExitUsage, "too many arguments: expected [<session>] or <tmux-session> <window>")
	}
	return nil
}

// flagAliases maps --to/--from to the canonical spellings.
func flagAliases(f *pflag.FlagSet, name string) pflag.NormalizedName {
	switch name {
	case "to":
		name = "teleport-to"
	case "from":
		name = "teleport-from"
	}
	return pflag.NormalizedName(name)
}

func (a *app) rootCmd() *cobra.Command {
	var tf teleportFlags
	root := &cobra.Command{
		Use:           "claude-teleport [<session>] --to|--from <host> [options]",
		Short:         "move an in-progress Claude Code session to another machine",
		Long:          rootLong,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			bare := len(args) == 0 && cmd.Flags().NFlag() == 0
			if err := tf.validate(args, bare); err != nil {
				return err
			}
			return exitErr(a.runTeleport(cmd.Context(), tf, args))
		},
	}
	// R-P3-28b: this template is inherited by every child command (cobra
	// walks up to the nearest ancestor with one set), so it must work for
	// all of them, not only root. Root keeps its old, exact behaviour — a
	// hand-written Long that already documents everything, with nothing
	// auto-appended. Every other command falls back to Short (if any) plus
	// cobra's own UsageString (Use line, its own flags, subcommands) when
	// it has no Long, and gets UsageString appended after its Long when it
	// does — otherwise a command with no Long (continue, status, abandon,
	// list, doctor, version, and the hidden internal-* / remote commands)
	// printed nothing at all for `--help`.
	root.SetHelpTemplate(`{{if not .HasParent}}{{.Long}}
{{else}}{{if .Long}}{{.Long}}

{{else if .Short}}{{.Short}}

{{end}}{{.UsageString}}{{end}}`)
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return Exit(ExitUsage, "%v", err)
	})
	f := root.Flags()
	f.SetNormalizeFunc(flagAliases)
	f.StringVar(&tf.To, "teleport-to", "", "destination host (alias --to)")
	f.StringVar(&tf.From, "teleport-from", "", "source host (alias --from)")
	f.StringArrayVar(&tf.Via, "via", nil, "jump host, repeatable")
	f.StringArrayVarP(&tf.SSHOptions, "option", "o", nil, "ssh option KEY=VALUE")
	f.StringVar(&tf.DestPath, "dest-path", "", "destination cwd")
	f.StringArrayVar(&tf.Maps, "map", nil, "path prefix rewrite SRC=DST")
	f.StringVar(&tf.State, "state", "auto", "destination end state")
	f.BoolVar(&tf.AllowDrift, "allow-config-drift", false, "downgrade blocking drift to warnings")
	f.BoolVar(&tf.Force, "force", false, "allow non-fast-forward replacement of this session")
	f.StringVar(&tf.TmuxSocket, "tmux-socket", "", "destination tmux socket name")
	f.BoolVar(&tf.NoTmux, "no-tmux", false, "do not use tmux on the destination")
	f.BoolVar(&tf.NoBootstrap, "no-bootstrap", false, "require claude-teleport pre-installed on the remote (do not auto-install it)")
	f.StringArrayVar(&tf.Excludes, "exclude", nil, "exclude glob, repeatable")
	f.BoolVar(&tf.IncludeIgnored, "include-ignored", false, "also transfer gitignored files")
	f.BoolVar(&tf.DryRun, "dry-run", false, "preflight only")
	f.DurationVar(&tf.ExitTimeout, "exit-timeout", 30*time.Second, "source exit wait")
	f.DurationVar(&tf.StartTimeout, "start-timeout", 90*time.Second, "destination start wait")
	f.StringVar(&tf.LogFile, "log", "", "additional log file")
	root.PersistentFlags().BoolVar(&tf.JSON, "json", false, "machine-readable output")
	root.PersistentFlags().BoolVarP(&tf.Verbose, "verbose", "v", false, "verbose logging")
	root.PersistentFlags().BoolVarP(&tf.Quiet, "quiet", "q", false, "quiet logging")
	root.PersistentFlags().StringVar(&a.configDir, "config-dir", "", "local CLAUDE_CONFIG_DIR override")
	a.flags = &tf

	root.AddCommand(a.versionCmd(), a.internalFreezerCmd(), a.placeholderCmd(), newInspectCmd(a), a.listCmd(), a.compareConfigCmd(), newDoctorCmd(a))
	AddTransportCommands(root)
	root.AddCommand(newContinueCmd(a), newInternalRunnerCmd(a), newAbandonCmd(a))
	return root
}

// selectorEnv is the session-related environment (spec §3).
func (a *app) selectorEnv() session.Env {
	return session.Env{SessionID: a.env["CLAUDE_CODE_SESSION_ID"], PID: a.env["CLAUDE_PID"],
		TmuxPane: a.env["TMUX_PANE"], Tmux: a.env["TMUX"]}
}

// probe returns the tmux pane probe. Plan 03 returns tmuxx.Prober when a
// tmux server is reachable; in this plan there is no tmux client, so
// suspended panes and the two-word selector are not resolvable yet.
func (a *app) probe() session.PaneProbe { return nil }

// resolveSession applies the spec §5 selector rules locally.
func (a *app) resolveSession(args []string) (*session.Session, error) {
	p, err := a.resolvePaths()
	if err != nil {
		return nil, err
	}
	sel, err := session.ParseSelector(args, a.selectorEnv())
	if err != nil {
		return nil, Exit(ExitUsage, "%v", err)
	}
	s, err := session.Resolve(p, sel, a.probe())
	if errors.Is(err, session.ErrNotFound) {
		return nil, Exit(ExitRefused, "%v", err)
	}
	if err != nil {
		return nil, Exit(ExitUsage, "%v", err)
	}
	return s, nil
}

func (a *app) json() bool { return a.flags != nil && a.flags.JSON }
