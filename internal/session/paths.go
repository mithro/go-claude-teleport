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
	Home       string // $HOME on this host
	ConfigDir  string // ~/.claude or $CLAUDE_CONFIG_DIR
	GlobalJSON string // ~/.claude.json, or <ConfigDir>/.claude.json when CLAUDE_CONFIG_DIR is set
	DataDir    string // claude-teleport data dir (jobs/, staging/)
}

// NewPaths computes Paths from the three environment inputs. configDirEnv is
// $CLAUDE_CONFIG_DIR ("" = unset); xdgDataHome is $XDG_DATA_HOME ("" = unset).
//
// Verified against Claude Code 2.1.251 (format unchanged since 2.1.247):
// when CLAUDE_CONFIG_DIR is set, Claude Code creates and reads `.claude.json`
// INSIDE that directory ($CLAUDE_CONFIG_DIR/.claude.json) alongside
// projects/, sessions/ and backups/; $HOME/.claude.json is untouched.
// Without the variable the file is $HOME/.claude.json, next to $HOME/.claude/.
func NewPaths(home, configDirEnv, xdgDataHome string) Paths {
	p := Paths{Home: home}
	if configDirEnv != "" {
		p.ConfigDir = configDirEnv
		p.GlobalJSON = filepath.Join(configDirEnv, ".claude.json")
	} else {
		p.ConfigDir = filepath.Join(home, ".claude")
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
