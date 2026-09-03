package session

import "testing"

func TestMunge(t *testing.T) {
	cases := map[string]string{
		"/home/alice/github/x/.worktrees/y": "-home-alice-github-x--worktrees-y",
		"/home/alice/github/example/widget": "-home-alice-github-example-widget",
		"/":                                 "-",
	}
	for in, want := range cases {
		if got := Munge(in); got != want {
			t.Errorf("Munge(%q) = %q want %q", in, got, want)
		}
	}
}

func TestNewPathsDefaults(t *testing.T) {
	p := NewPaths("/home/alice", "", "", false)
	if p.ConfigDir != "/home/alice/.claude" || p.GlobalJSON != "/home/alice/.claude.json" ||
		p.DataDir != "/home/alice/.local/share/claude-teleport" {
		t.Fatalf("%+v", p)
	}
	if p.ProjectsDir() != "/home/alice/.claude/projects" || p.SessionsDir() != "/home/alice/.claude/sessions" ||
		p.HistoryFile() != "/home/alice/.claude/history.jsonl" {
		t.Fatalf("%+v", p)
	}
	if got := p.ProjectDir("/home/alice/github/example/widget"); got != "/home/alice/.claude/projects/-home-alice-github-example-widget" {
		t.Fatalf("ProjectDir = %q", got)
	}
}

// Verified against Claude Code 2.1.251: with CLAUDE_CONFIG_DIR set, Claude
// Code creates and uses <CLAUDE_CONFIG_DIR>/.claude.json, not $HOME/.claude.json.
func TestNewPathsWithConfigDir(t *testing.T) {
	p := NewPaths("/home/alice", "/srv/cfg", "/srv/xdg", true)
	if p.ConfigDir != "/srv/cfg" || p.GlobalJSON != "/srv/cfg/.claude.json" || p.DataDir != "/srv/xdg/claude-teleport" {
		t.Fatalf("%+v", p)
	}
}

// TestNewPathsConfigDirPresenceDecidesGlobalJSON is T26-2. Real Claude
// Code picks the global config file by whether CLAUDE_CONFIG_DIR is
// PRESENT in its environment, not by what the value is: exported at all
// (even naming the very directory $HOME/.claude already resolves to) it
// reads and writes $CLAUDE_CONFIG_DIR/.claude.json; absent, plain
// $HOME/.claude.json. Verified against real Claude Code 2.1.247/2.1.259
// in isolation — identical run, only the presence of that one variable
// changed the file it touched (task-26-report.md, finding 2).
func TestNewPathsConfigDirPresenceDecidesGlobalJSON(t *testing.T) {
	// Set in the environment to exactly the default location: still
	// "inside", because Claude Code sees the variable.
	p := NewPaths("/home/alice", "/home/alice/.claude", "", true)
	if !p.ConfigDirFromEnv || p.ConfigDir != "/home/alice/.claude" || p.GlobalJSON != "/home/alice/.claude/.claude.json" {
		t.Fatalf("CLAUDE_CONFIG_DIR set to the default location: %+v", p)
	}
	// Not set at all: the same directory, but the sibling file.
	p = NewPaths("/home/alice", "", "", false)
	if p.ConfigDirFromEnv || p.ConfigDir != "/home/alice/.claude" || p.GlobalJSON != "/home/alice/.claude.json" {
		t.Fatalf("CLAUDE_CONFIG_DIR unset: %+v", p)
	}
	// Present but empty: Claude Code's own fallback treats an empty value
	// as no value, so this must resolve exactly like "unset".
	p = NewPaths("/home/alice", "", "", true)
	if p.ConfigDirFromEnv || p.ConfigDir != "/home/alice/.claude" || p.GlobalJSON != "/home/alice/.claude.json" {
		t.Fatalf("CLAUDE_CONFIG_DIR present but empty: %+v", p)
	}
}
