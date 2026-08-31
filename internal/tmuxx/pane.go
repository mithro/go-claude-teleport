package tmuxx

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/procx"
)

type PaneState struct {
	PaneID  string
	Command string
	Argv    []string
	PID     int
	Content []string // last 50 lines
}

// Capture returns the pane's whole scrollback with escapes (-e), joined
// wrapped lines (-J), trailing spaces preserved (-p prints).
func Capture(ctx context.Context, t Transport, paneID string) ([]byte, error) {
	lines, err := t.Run(ctx, fmt.Sprintf("capture-pane -epJ -S - -t %s", Quote(paneID)))
	if err != nil {
		return nil, fmt.Errorf("capture-pane %s: %w", paneID, err)
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

// SendKeys sends keys to the pane. Key names tmux knows (Enter, C-c, …)
// are passed bare; everything else is Quoted as literal text.
func SendKeys(ctx context.Context, t Transport, paneID string, keys ...string) error {
	parts := []string{"send-keys", "-t", Quote(paneID)}
	for _, k := range keys {
		if isKeyName(k) {
			parts = append(parts, k)
		} else {
			parts = append(parts, Quote(k))
		}
	}
	if _, err := t.Run(ctx, strings.Join(parts, " ")); err != nil {
		return fmt.Errorf("send-keys %s: %w", paneID, err)
	}
	return nil
}

var keyNames = map[string]bool{"Enter": true, "Escape": true, "Tab": true, "BSpace": true, "Space": true, "Up": true, "Down": true, "Left": true, "Right": true}

func isKeyName(k string) bool {
	return keyNames[k] || strings.HasPrefix(k, "C-") || strings.HasPrefix(k, "M-")
}

// ShellQuote renders argv as single-quoted words for a POSIX shell.
func ShellQuote(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(parts, " ")
}

// TypeCommand types argv into the pane's shell and presses Enter. The
// leading space keeps it out of history-ignore-space shells' history.
func TypeCommand(ctx context.Context, t Transport, paneID string, argv []string) error {
	cmd := fmt.Sprintf("send-keys -t %s %s Enter", Quote(paneID), Quote(" "+ShellQuote(argv)))
	if _, err := t.Run(ctx, cmd); err != nil {
		return fmt.Errorf("type command into %s: %w", paneID, err)
	}
	return nil
}

var shells = map[string]bool{"bash": true, "zsh": true, "sh": true, "fish": true, "dash": true}

// State reports the pane's foreground process (first non-shell process in
// the pane's subtree, else the shell) and its last 50 lines.
func State(ctx context.Context, t Transport, paneID string, procs *procx.Table) (*PaneState, error) {
	lines, err := t.Run(ctx, fmt.Sprintf(`list-panes -t %s -F "#{pane_pid}"`, Quote(paneID)))
	if err != nil {
		return nil, fmt.Errorf("list-panes %s: %w", paneID, err)
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("pane %s: no such pane", paneID)
	}
	panePID, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return nil, fmt.Errorf("pane %s: pane_pid %q: %w", paneID, lines[0], err)
	}
	st := &PaneState{PaneID: paneID}
	shell, ok := procs.Get(panePID)
	if !ok {
		return nil, fmt.Errorf("pane %s: pid %d not in the process table", paneID, panePID)
	}
	st.Command, st.Argv, st.PID = shell.Comm, shell.Cmdline, shell.PID
	for _, pid := range procs.Subtree(panePID) {
		if pid == panePID {
			continue
		}
		p, _ := procs.Get(pid)
		if shells[p.Comm] {
			continue
		}
		st.Command, st.Argv, st.PID = p.Comm, p.Cmdline, p.PID
		break
	}
	content, err := t.Run(ctx, fmt.Sprintf("capture-pane -p -S -50 -t %s", Quote(paneID)))
	if err != nil {
		return nil, fmt.Errorf("capture-pane %s: %w", paneID, err)
	}
	st.Content = content
	return st, nil
}
