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
	p := NewPaths("/home/alice", "", "")
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
	p := NewPaths("/home/alice", "/srv/cfg", "/srv/xdg")
	if p.ConfigDir != "/srv/cfg" || p.GlobalJSON != "/srv/cfg/.claude.json" || p.DataDir != "/srv/xdg/claude-teleport" {
		t.Fatalf("%+v", p)
	}
}
