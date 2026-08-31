package tmuxx

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Facts crosses the protocol (inventory-tmux). The json tags reproduce
// Go-name marshaling EXACTLY (field F -> "F"), so they changed no wire
// byte; they pin the names against a future rename.
type Facts struct {
	SocketPath  string `json:"SocketPath"`
	SessionName string `json:"SessionName"`
	Group       string `json:"Group"` // session_group or ""
	WindowID    string `json:"WindowID"`
	WindowIndex int    `json:"WindowIndex"`
	WindowName  string `json:"WindowName"`
	AutoRename  bool   `json:"AutoRename"`
	PaneID      string `json:"PaneID"`
	PaneTitle   string `json:"PaneTitle"`
	PaneCwd     string `json:"PaneCwd"`
	PaneCommand string `json:"PaneCommand"`
	PanePID     int    `json:"PanePID"`
	HistorySize int    `json:"HistorySize"`
}

// describeFormat: pane_title is last on purpose — it is the only field that
// may contain a literal tab (names are vis-encoded by tmux).
const describeFormat = "#{session_name}\t#{session_group}\t#{window_id}\t#{window_index}\t#{window_name}\t#{pane_id}\t#{pane_current_path}\t#{pane_current_command}\t#{pane_pid}\t#{history_size}\t#{pane_title}"

// Describe collects the spec §9 source facts for one pane.
func Describe(ctx context.Context, t Transport, paneID string) (*Facts, error) {
	lines, err := t.Run(ctx, "list-panes -a -F \""+describeFormat+"\"")
	if err != nil {
		return nil, fmt.Errorf("list-panes: %w", err)
	}
	for _, l := range lines {
		f := strings.SplitN(l, "\t", 11)
		if len(f) != 11 {
			return nil, fmt.Errorf("malformed list-panes line: %q", l)
		}
		if f[5] != paneID {
			continue
		}
		wi, err := strconv.Atoi(f[3])
		if err != nil {
			return nil, fmt.Errorf("window_index in %q: %w", l, err)
		}
		pid, err := strconv.Atoi(f[8])
		if err != nil {
			return nil, fmt.Errorf("pane_pid in %q: %w", l, err)
		}
		hs, err := strconv.Atoi(f[9])
		if err != nil {
			return nil, fmt.Errorf("history_size in %q: %w", l, err)
		}
		facts := &Facts{SessionName: f[0], Group: f[1], WindowID: f[2], WindowIndex: wi, WindowName: f[4],
			PaneID: f[5], PaneCwd: f[6], PaneCommand: f[7], PanePID: pid, HistorySize: hs, PaneTitle: f[10]}
		ar, err := t.Run(ctx, fmt.Sprintf("show-options -wv -t %s automatic-rename", Quote(facts.WindowID)))
		if err != nil {
			return nil, fmt.Errorf("show-options automatic-rename: %w", err)
		}
		facts.AutoRename = true // tmux default
		if len(ar) > 0 && strings.TrimSpace(ar[0]) == "off" {
			facts.AutoRename = false
		}
		return facts, nil
	}
	return nil, fmt.Errorf("pane %s not found on this server", paneID)
}

// SessionInfo is one row of list-sessions.
//
// CONVENTION (R-PRB-2): Name (and Group) carry tmux's STORED, vis(3)-encoded
// spelling — whatever tmux itself reports — everywhere in this codebase, as
// does session.TmuxRef.Session. That is the spelling every `-t` target needs
// (`-t '=a\\b'` resolves the session created as `a\b`; `-t '=a\b'` does not).
// UnvisName is applied in exactly two places: passing a name to a creation
// flag (`new-session -s`, `new-window -n`), which tmux re-encodes, and human
// display. Never decode a name on its way into a target or a comparison.
//
// SessionInfo crosses the protocol (tmux-sessions). The json tags reproduce
// Go-name marshaling EXACTLY (field F -> "F"), so they changed no wire
// byte; they pin the names against a future rename.
type SessionInfo struct {
	Name  string `json:"Name"`
	Group string `json:"Group"`
}

// ListSessions lists every session with its group ("" when ungrouped).
func ListSessions(ctx context.Context, t Transport) ([]SessionInfo, error) {
	lines, err := t.Run(ctx, "list-sessions -F \"#{session_name}\t#{session_group}\"")
	if err != nil {
		return nil, fmt.Errorf("list-sessions: %w", err)
	}
	var out []SessionInfo
	for _, l := range lines {
		if l == "" {
			continue
		}
		name, group, _ := strings.Cut(l, "\t")
		out = append(out, SessionInfo{Name: name, Group: group})
	}
	return out, nil
}
