package claudecfg

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

func base() *Inventory {
	return &Inventory{Host: "laptop.example", ClaudeVersion: "2.1.247", Hooks: `{"a":1}`,
		Permissions: Permissions{DefaultMode: "acceptEdits", Allow: []string{"x"}, Deny: []string{"d"}},
		Env:         map[string]string{"A": "1"}, EnabledPlugins: map[string]bool{"p@m": true}, Model: "opus", Effort: "high",
		MCPServers: map[string]string{"playwright": "cfg1", "unused": "u"}, ProjectPresent: true,
		ProjectMCP: map[string]string{"filesystem": "fs1"}, AllowedTools: []string{"t"},
		Plugins:    map[string]PluginInfo{"p@m": {Version: "1", HooksHash: "h1"}, "q@m": {Version: "2"}},
		TreeHashes: map[string]string{"CLAUDE.md": "c", "agents": "a", "skills": "s", "commands": "k"}, KeybindingsHash: "kb",
		Skills: map[string]bool{"p:tdd": true, "deploy": true}, Agents: map[string]bool{"reviewer": true}}
}

func classes(r Report) map[string]Class {
	m := map[string]Class{}
	for _, d := range r.Diffs {
		m[d.Key] = d.Class
	}
	return m
}

func TestCompareIdentical(t *testing.T) {
	r := Compare(base(), base(), nil)
	if len(r.Diffs) != 0 || r.Blocking {
		t.Fatalf("%+v", r)
	}
}

// The spec §10 classification table, one row per assertion.
func TestCompareClassification(t *testing.T) {
	src := base()
	dst := base()
	dst.Host = "big-storage.example"
	dst.ClaudeVersion = "2.1.250"
	dst.Hooks = `{"a":2}`
	dst.MCPServers = map[string]string{"playwright": "cfg2", "extra": "e"} // playwright differs, unused absent, extra only on dst
	dst.ProjectMCP = map[string]string{}                                   // filesystem absent
	dst.Plugins = map[string]PluginInfo{"p@m": {Version: "1", HooksHash: "h2"}, "q@m": {Version: "3"}}
	dst.Permissions = Permissions{DefaultMode: "default", Allow: []string{"y"}, Deny: []string{"e"}}
	dst.Model, dst.Effort = "sonnet", "medium"
	dst.Env = map[string]string{"A": "2"}
	dst.AllowedTools = []string{"u"}
	dst.KeybindingsHash = ""
	dst.TreeHashes = map[string]string{"CLAUDE.md": "c2", "agents": "a", "skills": "s", "commands": "k"}
	dst.ProjectPresent = false
	dst.Skills = map[string]bool{"deploy": true}
	dst.Agents = map[string]bool{}
	dst.EnabledPlugins = map[string]bool{}

	usage := &session.Usage{MCPServers: map[string]bool{"playwright": true, "filesystem": true},
		Plugins: map[string]bool{"p@m": true}, Skills: map[string]bool{"p:tdd": true, "project-only": true},
		SubagentTypes: map[string]bool{"reviewer": true, "Explore": true}, PermissionModes: map[string]bool{}}
	r := Compare(src, dst, usage)
	if !r.Blocking {
		t.Fatal("must block")
	}
	want := map[string]Class{
		"hooks":                   Block,
		"plugin.p@m.hooks":        Block,
		"mcp.playwright":          Block, // used, differs
		"mcp.filesystem":          Block, // used, absent
		"mcp.unused":              Warn,  // unused, absent
		"mcp.extra":               Warn,  // only on destination
		"plugin.q@m":              Warn,  // unused, version differs
		"skill.p:tdd":             Block,
		"subagent.reviewer":       Block,
		"permissions.deny":        Block,
		"permissions.defaultMode": Block,
		"permissions.allow":       Warn,
		"claude.version":          Warn,
		"model":                   Warn,
		"effortLevel":             Warn,
		"env":                     Warn,
		"keybindings":             Warn,
		"tree.CLAUDE.md":          Warn,
		"enabledPlugins":          Warn,
		"project":                 Info,
	}
	got := classes(r)
	for k, c := range want {
		if got[k] != c {
			t.Errorf("%s: got %v want %v", k, got[k], c)
		}
	}
	// allowedTools is compared only when both hosts have the project entry
	// (otherwise the entry — allow-list included — is carried over).
	for _, absent := range []string{"skill.project-only", "subagent.Explore", "skill.deploy", "tree.agents", "plugin.p@m", "allowedTools"} {
		if _, ok := got[absent]; ok {
			t.Errorf("%s must not be reported", absent)
		}
	}
	both := base()
	both.AllowedTools = []string{"other"}
	if classes(Compare(src, both, usage))["allowedTools"] != Warn {
		t.Error("allowedTools must warn when both project entries exist and differ")
	}
	// Block rows render first; Downgrade turns them into Warn.
	if r.Diffs[0].Class != Block {
		t.Fatalf("first diff = %+v", r.Diffs[0])
	}
	d := r.Downgrade()
	if d.Blocking {
		t.Fatal("downgraded report must not block")
	}
	for _, x := range d.Diffs {
		if x.Class == Block {
			t.Fatalf("still blocking: %+v", x)
		}
	}
	var buf bytes.Buffer
	r.Render(&buf)
	out := buf.String()
	if !strings.HasPrefix(out, "CLASS") || !strings.Contains(out, "block  hooks") || !strings.Contains(out, "laptop.example") {
		t.Fatalf("render:\n%s", out)
	}
	js, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Blocking bool
		Diffs    []struct{ Class, Key string }
	}
	if err := json.Unmarshal(js, &parsed); err != nil || !parsed.Blocking || parsed.Diffs[0].Class != "block" {
		t.Fatalf("json: %s %v", js, err)
	}
}

// usage == nil means everything is used (compare-config without a session).
func TestCompareNilUsageTreatsAllAsUsed(t *testing.T) {
	src, dst := base(), base()
	dst.MCPServers = map[string]string{"playwright": "cfg1"} // "unused" absent
	dst.Plugins = map[string]PluginInfo{"p@m": {Version: "1", HooksHash: "h1"}}
	r := Compare(src, dst, nil)
	got := classes(r)
	if got["mcp.unused"] != Block || got["plugin.q@m"] != Block {
		t.Fatalf("%+v", got)
	}
}

func TestCompareFixtureDirs(t *testing.T) {
	src, err := Collect(srcPaths(), cwd, "laptop.example", "2.1.247")
	if err != nil {
		t.Fatal(err)
	}
	dst, err := Collect(dstPaths(), cwd, "big-storage.example", "2.1.247")
	if err != nil {
		t.Fatal(err)
	}
	got := classes(Compare(src, dst, nil))
	for k, c := range map[string]Class{"hooks": Block, "mcp.playwright": Block, "mcp.filesystem": Block,
		"plugin.superpowers@claude-plugins-official": Block, "plugin.netgear-switch@example-marketplace": Block,
		"subagent.reviewer": Block, "model": Warn, "tree.CLAUDE.md": Warn, "keybindings": Warn, "project": Info} {
		if got[k] != c {
			t.Errorf("%s: got %v want %v", k, got[k], c)
		}
	}
	if _, ok := got["skill.deploy"]; ok {
		t.Error("identical skill reported")
	}
}

// TestCompareRedactsSecrets asserts a token value placed in settings.env or
// in an MCP server config is never present in either Render or JSON output,
// while the drift it caused is still reported (spec §10 says nothing here
// is ever copied between hosts; the drift table must not leak it either).
func TestCompareRedactsSecrets(t *testing.T) {
	const envToken = "sk-ant-oat01-topsecret-env-token"
	const mcpToken = "sk-ant-oat01-topsecret-mcp-token"

	src := base()
	src.Env = map[string]string{"ANTHROPIC_AUTH_TOKEN": envToken, "DB_PASSWORD": "hunter2"}
	src.MCPServers = map[string]string{"playwright": `{"headers":{"Authorization":"Bearer ` + mcpToken + `"}}`}
	dst := base()
	dst.Env = map[string]string{"ANTHROPIC_AUTH_TOKEN": "different-token", "DB_PASSWORD": "hunter2"}
	dst.MCPServers = map[string]string{"playwright": `{"headers":{"Authorization":"Bearer different"}}`}

	r := Compare(src, dst, nil)
	got := classes(r)
	if got["env"] != Warn {
		t.Fatalf("env drift must still be reported: %+v", got)
	}
	if _, ok := got["mcp.playwright"]; !ok {
		t.Fatalf("mcp drift must still be reported: %+v", got)
	}

	var buf bytes.Buffer
	r.Render(&buf)
	js, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	rendered, jsonStr := buf.String(), string(js)
	for _, secret := range []string{envToken, mcpToken, "hunter2", "different-token"} {
		if strings.Contains(rendered, secret) {
			t.Errorf("Render leaked secret %q:\n%s", secret, rendered)
		}
		if strings.Contains(jsonStr, secret) {
			t.Errorf("JSON leaked secret %q:\n%s", secret, jsonStr)
		}
	}
	// The Difference struct itself must be redacted, not just the rendering.
	for _, d := range r.Diffs {
		if d.Key != "env" && d.Key != "mcp.playwright" {
			continue
		}
		for _, secret := range []string{envToken, mcpToken, "hunter2", "different-token"} {
			if strings.Contains(d.Source, secret) || strings.Contains(d.Dest, secret) {
				t.Errorf("Difference %+v leaks secret %q", d, secret)
			}
		}
	}
	// The env row must name the differing keys so an operator can act on it.
	envDiff := classes2diff(r, "env")
	if !strings.Contains(envDiff.Source, "ANTHROPIC_AUTH_TOKEN") || !strings.Contains(envDiff.Source, "DB_PASSWORD") {
		t.Errorf("env row must list key names: %+v", envDiff)
	}
}

func classes2diff(r Report, key string) Difference {
	for _, d := range r.Diffs {
		if d.Key == key {
			return d
		}
	}
	return Difference{}
}

func TestRenderEmpty(t *testing.T) {
	var buf bytes.Buffer
	Report{}.Render(&buf)
	if !strings.Contains(buf.String(), "no configuration differences") {
		t.Fatal(buf.String())
	}
}
