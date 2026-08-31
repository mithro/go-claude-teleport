package claudecfg

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mithro/go-claude-teleport/internal/session"
)

const cwd = "/home/alice/github/example/widget"

func srcPaths() session.Paths { return session.NewPaths("/home/alice", "testdata/src", "/tmp/x") }
func dstPaths() session.Paths { return session.NewPaths("/home/alice", "testdata/dst", "/tmp/x") }

func TestCollectSrc(t *testing.T) {
	inv, err := Collect(srcPaths(), cwd, "laptop.example", "2.1.247")
	if err != nil {
		t.Fatal(err)
	}
	want := &Inventory{
		Host: "laptop.example", ClaudeVersion: "2.1.247",
		Hooks:          `{"PreToolUse":[{"hooks":[{"command":"/home/alice/bin/guard.sh","type":"command"}],"matcher":"Bash"}]}`,
		Permissions:    Permissions{DefaultMode: "acceptEdits", Allow: []string{"Bash(go test:*)", "Bash(go vet:*)"}, Deny: []string{"Read(./.env)"}},
		Env:            map[string]string{"GOFLAGS": "-mod=mod"},
		EnabledPlugins: map[string]bool{"superpowers@claude-plugins-official": true},
		Model:          "opus", Effort: "high",
		MCPServers:     map[string]string{"playwright": `{"args":["@playwright/mcp@latest"],"command":"npx","type":"stdio"}`},
		ProjectPresent: true,
		ProjectMCP:     map[string]string{"filesystem": `{"args":["-y","@modelcontextprotocol/server-filesystem","/home/alice/github/example/widget"],"command":"npx","type":"stdio"}`},
		ProjectEnabledMCPJSON:  []string{"repo-tools"},
		ProjectDisabledMCPJSON: []string{},
		AllowedTools:   []string{"Bash(go test:*)"},
		Plugins: map[string]PluginInfo{
			"superpowers@claude-plugins-official": {Version: "6.3.0"},
			"netgear-switch@example-marketplace":  {Version: "0.4.1"},
		},
		Skills: map[string]bool{"deploy": true},
		Agents: map[string]bool{"reviewer": true},
	}
	got := *inv
	if len(got.TreeHashes) != 4 || got.TreeHashes["CLAUDE.md"] == "" || got.TreeHashes["agents"] == "" ||
		got.TreeHashes["skills"] == "" || got.TreeHashes["commands"] == "" || got.KeybindingsHash == "" {
		t.Fatalf("hashes: %+v %q", got.TreeHashes, got.KeybindingsHash)
	}
	got.TreeHashes, got.KeybindingsHash = nil, ""
	if diff := cmp.Diff(want, &got); diff != "" {
		t.Fatal(diff)
	}
}

func TestCollectDstMissingFilesAreNotErrors(t *testing.T) {
	inv, err := Collect(dstPaths(), cwd, "big-storage.example", "2.1.250")
	if err != nil {
		t.Fatal(err)
	}
	if inv.ProjectPresent || inv.Model != "sonnet" || inv.KeybindingsHash != "" || inv.TreeHashes["agents"] != "" ||
		inv.Plugins["superpowers@claude-plugins-official"].Version != "6.2.0" || len(inv.Env) != 0 {
		t.Fatalf("%+v", inv)
	}
	empty, err := Collect(session.NewPaths("/home/nobody", t.TempDir(), "/tmp/x"), cwd, "h", "")
	if err != nil || empty.Hooks != "" || empty.ProjectPresent {
		t.Fatalf("%+v %v", empty, err)
	}
}

func TestCollectMalformedIsError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{nope"), 0o600)
	if _, err := Collect(session.NewPaths("/home/alice", dir, "/tmp/x"), cwd, "h", ""); err == nil {
		t.Fatal("malformed settings.json must be an error")
	}
}

func TestCollectPluginHashesAndSkills(t *testing.T) {
	dir := t.TempDir()
	plug := filepath.Join(dir, "plugins", "cache", "m", "p", "1.0.0")
	os.MkdirAll(filepath.Join(plug, "hooks"), 0o700)
	os.MkdirAll(filepath.Join(plug, "skills", "tdd"), 0o700)
	os.MkdirAll(filepath.Join(plug, "agents"), 0o700)
	os.WriteFile(filepath.Join(plug, "hooks", "hooks.json"), []byte(`{"hooks":{}}`), 0o600)
	os.WriteFile(filepath.Join(plug, ".mcp.json"), []byte(`{"mcpServers":{}}`), 0o600)
	os.WriteFile(filepath.Join(plug, "skills", "tdd", "SKILL.md"), []byte("# tdd"), 0o600)
	os.WriteFile(filepath.Join(plug, "agents", "explorer.md"), []byte("# explorer"), 0o600)
	os.WriteFile(filepath.Join(dir, "plugins", "installed_plugins.json"),
		[]byte(`{"version":2,"plugins":{"p@m":[{"version":"1.0.0","installPath":"`+plug+`"}]}}`), 0o600)
	inv, err := Collect(session.NewPaths("/home/alice", dir, "/tmp/x"), cwd, "h", "")
	if err != nil {
		t.Fatal(err)
	}
	pi := inv.Plugins["p@m"]
	wantHooks, _ := FileHash(filepath.Join(plug, "hooks", "hooks.json"))
	wantMCP, _ := FileHash(filepath.Join(plug, ".mcp.json"))
	if pi.Version != "1.0.0" || pi.HooksHash != wantHooks || pi.MCPHash != wantMCP || wantHooks == "" {
		t.Fatalf("%+v", pi)
	}
	if !inv.Skills["p:tdd"] || !inv.Agents["p:explorer"] {
		t.Fatalf("plugin skills/agents: %+v %+v", inv.Skills, inv.Agents)
	}
}

func TestTreeHash(t *testing.T) {
	a, err := TreeHash("testdata/src/skills")
	if err != nil || a == "" {
		t.Fatal(err)
	}
	b, _ := TreeHash("testdata/dst/skills")
	if a != b {
		t.Fatal("identical trees must hash equal")
	}
	c, _ := TreeHash("testdata/src/CLAUDE.md")
	d, _ := TreeHash("testdata/dst/CLAUDE.md")
	if c == d || c == "" {
		t.Fatal("different files must hash differently")
	}
	if h, err := TreeHash(filepath.Join(t.TempDir(), "missing")); err != nil || h != "" {
		t.Fatalf("missing = %q %v", h, err)
	}
}
