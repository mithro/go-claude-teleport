package claudecfg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// Class is how serious a difference is (spec §10).
type Class int

const (
	Info Class = iota
	Warn
	Block
)

func (c Class) String() string {
	switch c {
	case Block:
		return "block"
	case Warn:
		return "warn"
	default:
		return "info"
	}
}

func (c Class) MarshalJSON() ([]byte, error) { return json.Marshal(c.String()) }

// UnmarshalJSON is MarshalJSON's inverse: without it, any *Plan carrying a
// drift Difference fails to round-trip through the job journal (PlanFromJournal
// decodes what ToJSON wrote) — every real teleport with drift diffs, and not
// only the "block" ones, persists Class as a string once per journal write.
func (c *Class) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("claudecfg.Class: %w", err)
	}
	switch s {
	case "block":
		*c = Block
	case "warn":
		*c = Warn
	case "info":
		*c = Info
	default:
		return fmt.Errorf("claudecfg.Class: unknown class %q", s)
	}
	return nil
}

// Difference is one row of the drift table.
type Difference struct {
	Class  Class  `json:"class"`
	Key    string `json:"key"` // e.g. "hooks", "mcp.playwright", "plugin.superpowers@claude-plugins-official"
	Source string `json:"source"`
	Dest   string `json:"dest"`
	Reason string `json:"reason"`
}

// Report is the classified drift between two hosts.
type Report struct {
	SourceHost string       `json:"source_host"`
	DestHost   string       `json:"dest_host"`
	Diffs      []Difference `json:"diffs"`
	Blocking   bool         `json:"blocking"` // any Block
}

// short clips a rendering for the table.
func short(s string) string {
	if s == "" {
		return "(absent)"
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 40 {
		return s[:37] + "..."
	}
	return s
}

func hashShort(h string) string {
	if h == "" {
		return "(absent)"
	}
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// configHash is the hex sha256 of a canonical config string; "" (rendered
// "(absent)" by hashShort) when the config itself is absent. Never render
// an MCP server's raw config: it may carry tokens or auth headers.
func configHash(canonicalConfig string) string {
	if canonicalConfig == "" {
		return ""
	}
	h := sha256.Sum256([]byte(canonicalConfig))
	return hex.EncodeToString(h[:])
}

// envSummary describes a settings.env map without ever rendering a value
// (env commonly carries secrets like ANTHROPIC_AUTH_TOKEN): sorted key
// names plus a short hash of the full key=value content, so drift in
// values still shows up as a differing hash.
func envSummary(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\n", k, env[k])
	}
	return fmt.Sprintf("keys%v %s", keys, hashShort(hex.EncodeToString(h.Sum(nil))))
}

func sortedKeys[V any](maps ...map[string]V) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range maps {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	sort.Strings(c)
	return c
}

func sameSet(a, b []string) bool { return slices.Equal(sortedCopy(a), sortedCopy(b)) }

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func sameBoolMap(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// mcpConfig is the effective config for a server on one host: the project
// entry wins over the user-level one.
func mcpConfig(inv *Inventory, name string) (string, bool) {
	if c, ok := inv.ProjectMCP[name]; ok {
		return c, true
	}
	c, ok := inv.MCPServers[name]
	return c, ok
}

// Compare classifies differences per spec §10. usage==nil means "everything used".
func Compare(src, dst *Inventory, usage *session.Usage) Report {
	r := Report{SourceHost: src.Host, DestHost: dst.Host}
	add := func(c Class, key, s, d, reason string) {
		r.Diffs = append(r.Diffs, Difference{Class: c, Key: key, Source: s, Dest: d, Reason: reason})
	}
	usedOr := func(m map[string]bool, name string) bool {
		if usage == nil {
			return true
		}
		return m[name]
	}
	pluginUsed := func(name string) bool {
		if usage == nil {
			return true
		}
		shortName, _, _ := strings.Cut(name, "@")
		return usage.Plugins[name] || usage.Plugins[shortName]
	}
	classIf := func(used bool) Class {
		if used {
			return Block
		}
		return Warn
	}

	if src.ClaudeVersion != dst.ClaudeVersion {
		add(Warn, "claude.version", short(src.ClaudeVersion), short(dst.ClaudeVersion), "Claude Code version differs")
	}
	if src.HooksHash != dst.HooksHash {
		add(Block, "hooks", hashShort(src.HooksHash), hashShort(dst.HooksHash), "settings.json hooks differ")
	}
	if src.Permissions.DefaultMode != dst.Permissions.DefaultMode {
		add(Block, "permissions.defaultMode", short(src.Permissions.DefaultMode), short(dst.Permissions.DefaultMode), "permission mode differs")
	}
	if !sameSet(src.Permissions.Deny, dst.Permissions.Deny) {
		add(Block, "permissions.deny", short(strings.Join(src.Permissions.Deny, ",")), short(strings.Join(dst.Permissions.Deny, ",")), "deny list differs")
	}
	if !sameSet(src.Permissions.Allow, dst.Permissions.Allow) {
		add(Warn, "permissions.allow", short(strings.Join(src.Permissions.Allow, ",")), short(strings.Join(dst.Permissions.Allow, ",")), "allow list differs")
	}

	// MCP servers: every server the source knows, then destination-only ones.
	srcNames := sortedKeys(src.MCPServers, src.ProjectMCP)
	for _, name := range srcNames {
		sc, _ := mcpConfig(src, name)
		dc, ok := mcpConfig(dst, name)
		used := usedOr(usage2map(usage), name)
		switch {
		case !ok:
			add(classIf(used), "mcp."+name, hashShort(configHash(sc)), "(absent)", "MCP server absent on destination")
		case sc != dc:
			add(classIf(used), "mcp."+name, hashShort(configHash(sc)), hashShort(configHash(dc)), "MCP server configured differently")
		}
	}
	for _, name := range sortedKeys(dst.MCPServers, dst.ProjectMCP) {
		if _, ok := mcpConfig(src, name); !ok {
			dc, _ := mcpConfig(dst, name)
			add(Warn, "mcp."+name, "(absent)", hashShort(configHash(dc)), "MCP server only on destination")
		}
	}
	if src.ProjectPresent && dst.ProjectPresent {
		if !sameSet(src.ProjectEnabledMCPJSON, dst.ProjectEnabledMCPJSON) || !sameSet(src.ProjectDisabledMCPJSON, dst.ProjectDisabledMCPJSON) {
			add(Warn, "project.mcpjson", short(strings.Join(src.ProjectEnabledMCPJSON, ",")+" / -"+strings.Join(src.ProjectDisabledMCPJSON, ",")),
				short(strings.Join(dst.ProjectEnabledMCPJSON, ",")+" / -"+strings.Join(dst.ProjectDisabledMCPJSON, ",")), "enabled/disabled .mcp.json servers differ")
		}
	}

	// Plugins.
	for _, name := range sortedKeys(src.Plugins) {
		sp := src.Plugins[name]
		dp, ok := dst.Plugins[name]
		used := pluginUsed(name)
		switch {
		case !ok:
			add(classIf(used), "plugin."+name, sp.Version, "(absent)", "plugin absent on destination")
			continue
		case sp.Version != dp.Version:
			add(classIf(used), "plugin."+name, sp.Version, dp.Version, "plugin version differs")
		}
		if sp.HooksHash != dp.HooksHash {
			add(Block, "plugin."+name+".hooks", hashShort(sp.HooksHash), hashShort(dp.HooksHash), "plugin hooks/hooks.json differs")
		}
		if sp.MCPHash != dp.MCPHash {
			add(classIf(used), "plugin."+name+".mcp", hashShort(sp.MCPHash), hashShort(dp.MCPHash), "plugin .mcp.json differs")
		}
	}
	for _, name := range sortedKeys(dst.Plugins) {
		if _, ok := src.Plugins[name]; !ok {
			add(Warn, "plugin."+name, "(absent)", dst.Plugins[name].Version, "plugin only on destination")
		}
	}
	if !sameBoolMap(src.EnabledPlugins, dst.EnabledPlugins) {
		add(Warn, "enabledPlugins", short(fmt.Sprint(sortedKeys(src.EnabledPlugins))), short(fmt.Sprint(sortedKeys(dst.EnabledPlugins))), "enabledPlugins differ")
	}

	// Skills and sub-agent types: only those the source has; a used one
	// missing on the destination blocks, an unused one warns.
	for _, name := range sortedKeys(src.Skills) {
		if !dst.Skills[name] {
			add(classIf(usedOr(usageSkills(usage), name)), "skill."+name, "present", "(absent)", "skill absent on destination")
		}
	}
	for _, name := range sortedKeys(src.Agents) {
		if !dst.Agents[name] {
			add(classIf(usedOr(usageAgents(usage), name)), "subagent."+name, "present", "(absent)", "sub-agent absent on destination")
		}
	}

	// Warn-only rows.
	if src.Model != dst.Model {
		add(Warn, "model", short(src.Model), short(dst.Model), "model differs")
	}
	if src.Effort != dst.Effort {
		add(Warn, "effortLevel", short(src.Effort), short(dst.Effort), "effortLevel differs")
	}
	if !sameMap(src.Env, dst.Env) {
		add(Warn, "env", envSummary(src.Env), envSummary(dst.Env), "settings env differs")
	}
	if src.ProjectPresent && dst.ProjectPresent && !sameSet(src.AllowedTools, dst.AllowedTools) {
		add(Warn, "allowedTools", short(strings.Join(src.AllowedTools, ",")), short(strings.Join(dst.AllowedTools, ",")), "project allowedTools differ")
	}
	if src.KeybindingsHash != dst.KeybindingsHash {
		add(Warn, "keybindings", hashShort(src.KeybindingsHash), hashShort(dst.KeybindingsHash), "keybindings.json differs")
	}
	for _, name := range []string{"CLAUDE.md", "agents", "skills", "commands"} {
		if src.TreeHashes[name] != dst.TreeHashes[name] {
			add(Warn, "tree."+name, hashShort(src.TreeHashes[name]), hashShort(dst.TreeHashes[name]), "user-level "+name+" differs")
		}
	}
	if src.ProjectPresent && !dst.ProjectPresent {
		add(Info, "project", "present", "(absent)", "project entry will be added on the destination")
	}

	sort.SliceStable(r.Diffs, func(i, j int) bool { return r.Diffs[i].Class > r.Diffs[j].Class })
	for _, d := range r.Diffs {
		if d.Class == Block {
			r.Blocking = true
		}
	}
	return r
}

func usage2map(u *session.Usage) map[string]bool {
	if u == nil {
		return nil
	}
	return u.MCPServers
}
func usageSkills(u *session.Usage) map[string]bool {
	if u == nil {
		return nil
	}
	return u.Skills
}
func usageAgents(u *session.Usage) map[string]bool {
	if u == nil {
		return nil
	}
	return u.SubagentTypes
}

// Downgrade implements --allow-config-drift: Block -> Warn.
func (r Report) Downgrade() Report {
	out := Report{SourceHost: r.SourceHost, DestHost: r.DestHost, Diffs: slices.Clone(r.Diffs)}
	for i := range out.Diffs {
		if out.Diffs[i].Class == Block {
			out.Diffs[i].Class = Warn
		}
	}
	return out
}

// Render writes an aligned table.
func (r Report) Render(w io.Writer) {
	if len(r.Diffs) == 0 {
		if r.SourceHost != "" || r.DestHost != "" {
			fmt.Fprintf(w, "configuration drift: %s -> %s\n", r.SourceHost, r.DestHost)
		}
		fmt.Fprintln(w, "no configuration differences")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CLASS\tKEY\tSOURCE\tDESTINATION\tREASON")
	for _, d := range r.Diffs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", d.Class, d.Key, d.Source, d.Dest, d.Reason)
	}
	tw.Flush()
	if r.SourceHost != "" || r.DestHost != "" {
		fmt.Fprintf(w, "configuration drift: %s -> %s\n", r.SourceHost, r.DestHost)
	}
	if r.Blocking {
		fmt.Fprintln(w, "blocking differences found (use --allow-config-drift to proceed anyway)")
	}
}

// JSON renders the report for --json.
func (r Report) JSON() ([]byte, error) {
	if r.Diffs == nil {
		r.Diffs = []Difference{}
	}
	return json.MarshalIndent(r, "", "  ")
}
