package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Usage is what the session actually used (spec §10).
type Usage struct {
	MCPServers      map[string]bool // from tool_use names mcp__<server>__<tool>
	Skills          map[string]bool // Skill tool "skill" arg + attributionSkill
	Plugins         map[string]bool // attributionPlugin
	SubagentTypes   map[string]bool // Agent tool subagent_type
	PermissionModes map[string]bool // permission-mode records
}

func newUsage() *Usage {
	return &Usage{MCPServers: map[string]bool{}, Skills: map[string]bool{}, Plugins: map[string]bool{},
		SubagentTypes: map[string]bool{}, PermissionModes: map[string]bool{}}
}

// ScanUsage walks every record of the main transcript and the sub-agent
// transcripts generically (any nesting), so it keeps working when Claude
// moves fields around.
func ScanUsage(s *Session) (*Usage, error) {
	u := newUsage()
	files := []string{s.Transcript}
	sub, err := filepath.Glob(filepath.Join(s.ProjectDir, string(s.ID), "subagents", "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("glob subagents: %w", err)
	}
	files = append(files, sub...)
	for _, f := range files {
		if err := scanUsageFile(f, u); err != nil {
			return nil, err
		}
	}
	return u, nil
}

func scanUsageFile(path string, u *Usage) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("scan usage: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), scannerBuf)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec any
		if json.Unmarshal(line, &rec) != nil {
			continue // unparseable lines carry no usage we can read
		}
		walkUsage(rec, u)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("scan usage %s: %w", path, err)
	}
	return nil
}

func walkUsage(v any, u *Usage) {
	switch x := v.(type) {
	case map[string]any:
		typ, _ := x["type"].(string)
		if typ == "tool_use" {
			name, _ := x["name"].(string)
			input, _ := x["input"].(map[string]any)
			switch {
			case strings.HasPrefix(name, "mcp__"):
				if parts := strings.SplitN(name, "__", 3); len(parts) >= 2 && parts[1] != "" {
					u.MCPServers[parts[1]] = true
				}
			case name == "Skill":
				if sk, _ := input["skill"].(string); sk != "" {
					u.Skills[sk] = true
				}
			case name == "Agent" || name == "Task":
				if st, _ := input["subagent_type"].(string); st != "" {
					u.SubagentTypes[st] = true
				}
			}
		}
		if typ == "permission-mode" {
			if m, _ := x["permissionMode"].(string); m != "" {
				u.PermissionModes[m] = true
			}
		}
		if s, _ := x["attributionSkill"].(string); s != "" {
			u.Skills[s] = true
		}
		if s, _ := x["attributionPlugin"].(string); s != "" {
			u.Plugins[s] = true
		}
		for _, c := range x {
			walkUsage(c, u)
		}
	case []any:
		for _, c := range x {
			walkUsage(c, u)
		}
	}
}
