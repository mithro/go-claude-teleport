package cli

import "testing"

func TestResolvePaths(t *testing.T) {
	a := &app{env: parseEnv([]string{"HOME=/home/alice"})}
	p, err := a.resolvePaths()
	if err != nil || p.ConfigDir != "/home/alice/.claude" || p.GlobalJSON != "/home/alice/.claude.json" {
		t.Fatalf("%+v %v", p, err)
	}
	a = &app{env: parseEnv([]string{"HOME=/home/alice", "CLAUDE_CONFIG_DIR=/srv/cfg", "XDG_DATA_HOME=/srv/data"})}
	p, _ = a.resolvePaths()
	if p.ConfigDir != "/srv/cfg" || p.GlobalJSON != "/srv/cfg/.claude.json" || p.DataDir != "/srv/data/claude-teleport" {
		t.Fatalf("%+v", p)
	}
	a.configDir = "/flag/cfg"
	if p, _ = a.resolvePaths(); p.ConfigDir != "/flag/cfg" || p.GlobalJSON != "/flag/cfg/.claude.json" {
		t.Fatalf("--config-dir must win: %+v", p)
	}
	if _, err := (&app{env: map[string]string{}}).resolvePaths(); err == nil {
		t.Fatal("missing HOME must be an error")
	}
}
