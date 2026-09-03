package tmuxx

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/procx"
)

// Target-id sigils. tmux ids are stable and always carry one.
const (
	paneSigil   = "%"
	windowSigil = "@"
)

// checkTargetID rejects an id that does not carry its sigil ("%" for a
// pane, "@" for a window), the empty string included.
//
// tmux resolves an EMPTY -t target to the CURRENT pane/window — probe
// verified: `kill-window -t ”` returns 0 having killed the window the
// tool never named, and `send-keys -t ” 'echo HOSTILE' Enter` types into
// whatever pane is current. TmuxRef crosses the JSON wire into a
// long-lived `remote serve` on the destination, so an id that arrives
// empty or mangled must fail here rather than act on someone else's pane.
func checkTargetID(op, sigil, id string) error {
	kind := "pane"
	if sigil == windowSigil {
		kind = "window"
	}
	if !strings.HasPrefix(id, sigil) {
		return fmt.Errorf("%s: %q is not a tmux %s id (it must start with %q)", op, id, kind, sigil)
	}
	return nil
}

// PaneState crosses the protocol (shape-state). The json tags reproduce
// Go-name marshaling EXACTLY (field F -> "F"), so they changed no wire
// byte; they pin the names against a future rename.
type PaneState struct {
	PaneID  string   `json:"PaneID"`
	Command string   `json:"Command"`
	Argv    []string `json:"Argv"`
	PID     int      `json:"PID"`
	Content []string `json:"Content"` // last 50 lines
}

// Capture returns the pane's whole scrollback with escapes (-e), joined
// wrapped lines (-J), trailing spaces preserved (-p prints).
func Capture(ctx context.Context, t Transport, paneID string) ([]byte, error) {
	if err := checkTargetID("capture-pane", paneSigil, paneID); err != nil {
		return nil, err
	}
	lines, err := t.Run(ctx, fmt.Sprintf("capture-pane -epJ -S - -t %s", Quote(paneID)))
	if err != nil {
		return nil, fmt.Errorf("capture-pane %s: %w", paneID, err)
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

// SendKeys sends keys to the pane. Key names tmux knows (Enter, C-c, …)
// are passed bare; everything else is Quoted as literal text.
func SendKeys(ctx context.Context, t Transport, paneID string, keys ...string) error {
	if err := checkTargetID("send-keys", paneSigil, paneID); err != nil {
		return err
	}
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
	if err := checkTargetID("type command", paneSigil, paneID); err != nil {
		return err
	}
	cmd := fmt.Sprintf("send-keys -t %s %s Enter", Quote(paneID), Quote(" "+ShellQuote(argv)))
	if _, err := t.Run(ctx, cmd); err != nil {
		return fmt.Errorf("type command into %s: %w", paneID, err)
	}
	return nil
}

var shells = map[string]bool{"bash": true, "zsh": true, "sh": true, "fish": true, "dash": true}

// IsShell reports whether comm (a /proc comm) is one of the interactive
// shells a pane may run.
func IsShell(comm string) bool { return shells[comm] }

// PanePID is the pid of the process tmux itself started in the pane — the
// shell, in an ordinary pane. It leads its own process group, so it is
// also the group the pty's foreground reverts to when a job-control shell
// takes the terminal back from a stopped job.
func PanePID(ctx context.Context, t Transport, paneID string) (int, error) {
	if err := checkTargetID("pane pid", paneSigil, paneID); err != nil {
		return 0, err
	}
	lines, err := t.Run(ctx, fmt.Sprintf(`list-panes -t %s -F "#{pane_pid}"`, Quote(paneID)))
	if err != nil {
		return 0, fmt.Errorf("list-panes %s: %w", paneID, err)
	}
	if len(lines) == 0 {
		return 0, fmt.Errorf("pane %s: no such pane", paneID)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0, fmt.Errorf("pane %s: pane_pid %q: %w", paneID, lines[0], err)
	}
	return pid, nil
}

// State reports the pane's foreground process (first non-shell process in
// the pane's subtree, else the shell) and its last 50 lines.
func State(ctx context.Context, t Transport, paneID string, procs *procx.Table) (*PaneState, error) {
	if err := checkTargetID("pane state", paneSigil, paneID); err != nil {
		return nil, err
	}
	panePID, err := PanePID(ctx, t, paneID)
	if err != nil {
		return nil, err
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
		p, ok := procs.Get(pid)
		if !ok {
			// The pid vanished between Subtree and Get; a zero Proc would be
			// reported as the foreground process (empty comm, no argv).
			continue
		}
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
