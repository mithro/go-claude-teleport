// Package claudecfg inventories a host's Claude Code configuration and
// classifies differences between two hosts (spec §10). Nothing here is ever
// copied between hosts — only compared.
package claudecfg

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// PluginInfo describes one installed plugin.
type PluginInfo struct {
	Version   string
	HooksHash string // sha256 of hooks/hooks.json, "" if none
	MCPHash   string // sha256 of .mcp.json, "" if none
}

// Permissions is settings.permissions (the parts that change behaviour).
type Permissions struct {
	DefaultMode string
	Allow, Deny []string
}

// Inventory is everything Compare looks at on one host.
type Inventory struct {
	Host                                          string
	ClaudeVersion                                 string
	Hooks                                         string // canonical JSON of settings.hooks ("" if absent)
	Permissions                                   Permissions
	Env                                           map[string]string
	EnabledPlugins                                map[string]bool
	Model, Effort                                 string
	MCPServers                                    map[string]string // name -> canonical JSON config (user level)
	ProjectPresent                                bool
	ProjectMCP                                    map[string]string // projects[cwd].mcpServers
	ProjectEnabledMCPJSON, ProjectDisabledMCPJSON []string
	AllowedTools                                  []string
	Plugins                                       map[string]PluginInfo // "name@marketplace" -> info
	TreeHashes                                    map[string]string     // "CLAUDE.md", "agents", "skills", "commands"
	KeybindingsHash                               string
	Skills                                        map[string]bool // user skills/<name>/SKILL.md and plugin "<plugin>:<skill>"
	Agents                                        map[string]bool // user agents/<name>.md and plugin "<plugin>:<agent>"
}

// canonical renders a decoded JSON value deterministically (sorted keys,
// no HTML escaping, no trailing newline).
func canonical(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprintf("<unencodable: %v>", err)
	}
	return strings.TrimRight(buf.String(), "\n")
}

// readObject decodes a JSON object file; absent -> (nil,false,nil).
func readObject(path string) (map[string]any, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("parse %s: top level is not an object", path)
	}
	return obj, true, nil
}

func stringSlice(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func stringMap(v any) map[string]string {
	obj, _ := v.(map[string]any)
	out := make(map[string]string, len(obj))
	for k, val := range obj {
		out[k] = fmt.Sprint(val)
	}
	return out
}

func canonicalMap(v any) map[string]string {
	obj, _ := v.(map[string]any)
	out := make(map[string]string, len(obj))
	for k, val := range obj {
		out[k] = canonical(val)
	}
	return out
}

func stringOf(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// FileHash is the hex sha256 of a file; "" if it does not exist.
func FileHash(path string) (string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// TreeHash hashes a file, or every regular file under a directory (relative
// path + content, sorted). "" if path does not exist.
func TreeHash(path string) (string, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("tree hash %s: %w", path, err)
	}
	if !info.IsDir() {
		return FileHash(path)
	}
	var files []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("tree hash %s: %w", path, err)
	}
	sort.Strings(files)
	h := sha256.New()
	for _, f := range files {
		rel, _ := filepath.Rel(path, f)
		fh, err := FileHash(f)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\n%s\n", filepath.ToSlash(rel), fh)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// namesUnder lists the names of skills (dir/<name>/SKILL.md) or agents
// (dir/<name>.md) under dir, prefixed with prefix.
func namesUnder(dir, prefix string, skills bool, into map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	for _, e := range entries {
		switch {
		case skills && e.IsDir():
			if _, err := os.Stat(filepath.Join(dir, e.Name(), "SKILL.md")); err == nil {
				into[prefix+e.Name()] = true
			}
		case !skills && !e.IsDir() && strings.HasSuffix(e.Name(), ".md"):
			into[prefix+strings.TrimSuffix(e.Name(), ".md")] = true
		}
	}
	return nil
}

// Collect reads the host's configuration. claudeVersion is supplied by the
// caller (registry or `claude --version`). Missing files are not errors;
// malformed files are. Only the keys named in spec §10 are inspected.
func Collect(p session.Paths, cwd, host, claudeVersion string) (*Inventory, error) {
	inv := &Inventory{Host: host, ClaudeVersion: claudeVersion,
		Env: map[string]string{}, EnabledPlugins: map[string]bool{}, MCPServers: map[string]string{},
		ProjectMCP: map[string]string{}, Plugins: map[string]PluginInfo{}, TreeHashes: map[string]string{},
		Skills: map[string]bool{}, Agents: map[string]bool{}}

	// settings.json
	if s, ok, err := readObject(filepath.Join(p.ConfigDir, "settings.json")); err != nil {
		return nil, err
	} else if ok {
		if hooks, present := s["hooks"]; present {
			inv.Hooks = canonical(hooks)
		}
		if perm, _ := s["permissions"].(map[string]any); perm != nil {
			inv.Permissions = Permissions{DefaultMode: stringOf(perm["defaultMode"]),
				Allow: stringSlice(perm["allow"]), Deny: stringSlice(perm["deny"])}
		}
		inv.Env = stringMap(s["env"])
		for k, v := range stringMap(s["enabledPlugins"]) {
			inv.EnabledPlugins[k] = v == "true"
		}
		inv.Model = stringOf(s["model"])
		inv.Effort = stringOf(s["effortLevel"])
	}

	// ~/.claude.json (or <configdir>/.claude.json)
	if g, ok, err := readObject(p.GlobalJSON); err != nil {
		return nil, err
	} else if ok {
		inv.MCPServers = canonicalMap(g["mcpServers"])
		projects, _ := g["projects"].(map[string]any)
		if proj, present := projects[cwd].(map[string]any); present {
			inv.ProjectPresent = true
			inv.ProjectMCP = canonicalMap(proj["mcpServers"])
			inv.ProjectEnabledMCPJSON = stringSlice(proj["enabledMcpjsonServers"])
			inv.ProjectDisabledMCPJSON = stringSlice(proj["disabledMcpjsonServers"])
			inv.AllowedTools = stringSlice(proj["allowedTools"])
		}
	}

	// plugins/installed_plugins.json
	if ip, ok, err := readObject(filepath.Join(p.ConfigDir, "plugins", "installed_plugins.json")); err != nil {
		return nil, err
	} else if ok {
		plugins, _ := ip["plugins"].(map[string]any)
		for name, raw := range plugins {
			var entry map[string]any
			switch x := raw.(type) {
			case []any: // v2: a list of installs, newest first
				if len(x) > 0 {
					entry, _ = x[0].(map[string]any)
				}
			case map[string]any: // v1: a single object
				entry = x
			}
			if entry == nil {
				continue
			}
			pi := PluginInfo{Version: stringOf(entry["version"])}
			if install := stringOf(entry["installPath"]); install != "" {
				var err error
				if pi.HooksHash, err = FileHash(filepath.Join(install, "hooks", "hooks.json")); err != nil {
					return nil, err
				}
				if pi.MCPHash, err = FileHash(filepath.Join(install, ".mcp.json")); err != nil {
					return nil, err
				}
				short, _, _ := strings.Cut(name, "@")
				if err := namesUnder(filepath.Join(install, "skills"), short+":", true, inv.Skills); err != nil {
					return nil, err
				}
				if err := namesUnder(filepath.Join(install, "agents"), short+":", false, inv.Agents); err != nil {
					return nil, err
				}
			}
			inv.Plugins[name] = pi
		}
	}

	// user-level trees, skills, agents, keybindings
	for _, name := range []string{"CLAUDE.md", "agents", "skills", "commands"} {
		h, err := TreeHash(filepath.Join(p.ConfigDir, name))
		if err != nil {
			return nil, err
		}
		inv.TreeHashes[name] = h
	}
	if err := namesUnder(filepath.Join(p.ConfigDir, "skills"), "", true, inv.Skills); err != nil {
		return nil, err
	}
	if err := namesUnder(filepath.Join(p.ConfigDir, "agents"), "", false, inv.Agents); err != nil {
		return nil, err
	}
	var err error
	if inv.KeybindingsHash, err = FileHash(filepath.Join(p.ConfigDir, "keybindings.json")); err != nil {
		return nil, err
	}
	return inv, nil
}
