package tmuxx

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

type Facts struct {
	SocketPath  string
	SessionName string
	Group       string // session_group or ""
	WindowID    string
	WindowIndex int
	WindowName  string
	AutoRename  bool
	PaneID      string
	PaneTitle   string
	PaneCwd     string
	PaneCommand string
	PanePID     int
	HistorySize int
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
type SessionInfo struct{ Name, Group string }

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
