package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// State is where a session is right now (spec §5, §9).
type State int

const (
	StateIdle      State = iota // transcript on disk, no process, no placeholder pane
	StateRunning                // live claude process (registry entry)
	StateSuspended              // a pane whose foreground command is a placeholder
)

func (s State) String() string {
	switch s {
	case StateRunning:
		return "running"
	case StateSuspended:
		return "suspended"
	default:
		return "idle"
	}
}

// Registry is ~/.claude/sessions/<pid>.json (only the fields we use).
type Registry struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	ProcStart string `json:"procStart"` // string OR number in the file; normalised to string
	Version   string `json:"version"`
	Kind      string `json:"kind"`
	Status    string `json:"status"` // "busy" | "idle"
	Tmux      string `json:"tmux"`   // "<session>:@<win>.%<pane>" or ""
	Name      string `json:"name"`
	UpdatedAt int64  `json:"updatedAt"`
	File      string `json:"-"` // path it was read from
}

// registryFile is the on-disk shape; procStart may be a string or a number.
type registryFile struct {
	PID       int             `json:"pid"`
	SessionID string          `json:"sessionId"`
	Cwd       string          `json:"cwd"`
	ProcStart json.RawMessage `json:"procStart"`
	Version   string          `json:"version"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	Tmux      string          `json:"tmux"`
	Name      string          `json:"name"`
	UpdatedAt int64           `json:"updatedAt"`
}

// ReadRegistryFile reads one sessions/<pid>.json. *.key files are never opened.
func ReadRegistryFile(path string) (Registry, error) {
	if !strings.HasSuffix(path, ".json") {
		return Registry{}, fmt.Errorf("registry file %s: not a .json file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Registry{}, fmt.Errorf("read registry %s: %w", path, err)
	}
	var f registryFile
	if err := json.Unmarshal(data, &f); err != nil {
		return Registry{}, fmt.Errorf("parse registry %s: %w", path, err)
	}
	start, err := normaliseProcStart(f.ProcStart)
	if err != nil {
		return Registry{}, fmt.Errorf("parse registry %s: %w", path, err)
	}
	return Registry{PID: f.PID, SessionID: f.SessionID, Cwd: f.Cwd, ProcStart: start, Version: f.Version,
		Kind: f.Kind, Status: f.Status, Tmux: f.Tmux, Name: f.Name, UpdatedAt: f.UpdatedAt, File: path}, nil
}

// normaliseProcStart accepts a JSON string or number (older writers) and
// fails closed on any other type, so a reused pid can never match by accident.
func normaliseProcStart(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String(), nil
	}
	return "", fmt.Errorf("procStart has unsupported JSON type: %s", string(raw))
}

// ReadRegistry reads every *.json in sessionsDir, sorted by pid. A missing
// directory is an empty registry; a malformed file is an error naming it.
func ReadRegistry(sessionsDir string) ([]Registry, error) {
	entries, err := os.ReadDir(sessionsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read registry dir %s: %w", sessionsDir, err)
	}
	var out []Registry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue // *.key (messaging tokens) and anything else are never opened
		}
		r, err := ReadRegistryFile(filepath.Join(sessionsDir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

// TmuxParts splits "<session>:@<win>.%<pane>".
func (r Registry) TmuxParts() (sess, windowID, paneID string, ok bool) {
	i := strings.LastIndex(r.Tmux, ":@")
	if i < 0 {
		return "", "", "", false
	}
	sess = r.Tmux[:i]
	rest := r.Tmux[i+1:] // "@3.%7"
	j := strings.Index(rest, ".%")
	if j < 0 || sess == "" {
		return "", "", "", false
	}
	return sess, rest[:j], rest[j+1:], true
}
