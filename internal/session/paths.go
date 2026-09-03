package session

import (
	"path/filepath"
	"strings"
)

// Munge mirrors Claude Code's project-dir naming: '/' and '.' become '-'.
func Munge(absPath string) string {
	return strings.NewReplacer("/", "-", ".", "-").Replace(absPath)
}

// Paths resolves the on-disk locations for one config dir / home pair.
type Paths struct {
	Home      string // $HOME on this host
	ConfigDir string // ~/.claude or $CLAUDE_CONFIG_DIR
	// ConfigDirFromEnv records that CLAUDE_CONFIG_DIR was actually SET in
	// the environment (to a non-empty value), as opposed to ConfigDir
	// merely defaulting to ~/.claude. Claude Code decides where its global
	// config lives by the variable's presence, not its value (see
	// NewPaths), so this is the difference between $HOME/.claude.json and
	// $HOME/.claude/.claude.json even when ConfigDir is identical — and it
	// is what tells a claude this host starts whether to see the variable
	// at all (remote.claudeEnv). internal/cli is the only reader of the
	// environment and therefore the only place that can set this.
	ConfigDirFromEnv bool
	GlobalJSON       string // ~/.claude.json, or <ConfigDir>/.claude.json when CLAUDE_CONFIG_DIR is set
	DataDir          string // claude-teleport data dir (jobs/, staging/)
	ProcRoot         string // where /proc is mounted; tests point it at a fixture tree
}

// NewPaths computes Paths from the environment inputs. configDir is the
// config dir the caller resolved (from $CLAUDE_CONFIG_DIR, or --config-dir
// overriding it); configDirFromEnv says whether that came from the
// environment rather than defaulting; xdgDataHome is $XDG_DATA_HOME
// ("" = unset).
//
// Verified against real Claude Code 2.1.247, 2.1.251 and 2.1.259: Claude
// Code decides where its global `.claude.json` lives by whether
// CLAUDE_CONFIG_DIR is PRESENT in its environment, not by what the value
// is (task-26-report.md finding 2 — the same run, with the variable
// exported to the very path $HOME/.claude already resolves to, moved the
// file). Set: $CLAUDE_CONFIG_DIR/.claude.json, alongside projects/,
// sessions/ and backups/, and $HOME/.claude.json is untouched. Absent:
// $HOME/.claude.json, next to $HOME/.claude/. An empty value is no value —
// Claude Code's own fallback treats it as unset, and so does this.
func NewPaths(home, configDir, xdgDataHome string, configDirFromEnv bool) Paths {
	p := Paths{Home: home, ProcRoot: "/proc"}
	if configDir == "" {
		configDirFromEnv = false
	}
	p.ConfigDirFromEnv = configDirFromEnv
	if configDir != "" {
		p.ConfigDir = configDir
	} else {
		p.ConfigDir = filepath.Join(home, ".claude")
	}
	if configDirFromEnv {
		p.GlobalJSON = filepath.Join(p.ConfigDir, ".claude.json")
	} else {
		p.GlobalJSON = filepath.Join(home, ".claude.json")
	}
	if xdgDataHome == "" {
		xdgDataHome = filepath.Join(home, ".local", "share")
	}
	p.DataDir = filepath.Join(xdgDataHome, "claude-teleport")
	return p
}

func (p Paths) ProjectsDir() string { return filepath.Join(p.ConfigDir, "projects") }

// SessionsDir is the registry directory (read-only for us; never transferred).
func (p Paths) SessionsDir() string { return filepath.Join(p.ConfigDir, "sessions") }

func (p Paths) HistoryFile() string { return filepath.Join(p.ConfigDir, "history.jsonl") }

// ProjectDir is ProjectsDir()/Munge(cwd).
func (p Paths) ProjectDir(cwd string) string { return filepath.Join(p.ProjectsDir(), Munge(cwd)) }
