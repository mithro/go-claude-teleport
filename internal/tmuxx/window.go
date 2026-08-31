package tmuxx

import (
	"context"
	"fmt"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/session"
)

type Plan struct {
	SocketPath    string
	Group         string
	WindowName    string
	AutoRename    bool
	Cwd           string
	CreateSession bool // no session in Group exists
}

// BaseSession picks the session to add windows to for group G: the member
// named G if present, else the lexically smallest member; a session named
// G that is not grouped also counts.
func BaseSession(sessions []SessionInfo, group string) (string, bool) {
	best := ""
	for _, s := range sessions {
		if s.Name == group {
			return s.Name, true
		}
		if s.Group == group && (best == "" || s.Name < best) {
			best = s.Name
		}
	}
	return best, best != ""
}

const newWindowFormat = "#{pane_id}\t#{window_id}\t#{session_name}"

// OpenWindow creates the destination window (spec §9). The live session
// list decides between new-session and new-window; p.CreateSession is the
// preflight expectation and only affects logging by the caller.
func OpenWindow(ctx context.Context, t Transport, p *Plan) (*session.TmuxRef, error) {
	sessions, err := ListSessions(ctx, t)
	if err != nil {
		return nil, err
	}
	// list-sessions replies are vis(3)-encoded by tmux (see UnvisName) —
	// decode before comparing against the caller-supplied plain group name
	// and before handing a name back into a new-session/new-window target,
	// otherwise a stored escape (e.g. "\s" for a space) would be Quoted
	// verbatim and tmux would double-encode it.
	decoded := make([]SessionInfo, len(sessions))
	for i, s := range sessions {
		decoded[i] = SessionInfo{Name: UnvisName(s.Name), Group: UnvisName(s.Group)}
	}
	var cmd string
	if base, ok := BaseSession(decoded, p.Group); ok {
		cmd = fmt.Sprintf(`new-window -t %s -n %s -c %s -P -F "%s"`, Quote("="+base+":"), Quote(p.WindowName), Quote(p.Cwd), newWindowFormat)
	} else {
		cmd = fmt.Sprintf(`new-session -d -s %s -n %s -c %s -P -F "%s"`, Quote(p.Group), Quote(p.WindowName), Quote(p.Cwd), newWindowFormat)
	}
	lines, err := t.Run(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("open window: %w", err)
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("open window: empty reply to %q", cmd)
	}
	f := strings.SplitN(lines[0], "\t", 3)
	if len(f) != 3 || !strings.HasPrefix(f[0], "%") || !strings.HasPrefix(f[1], "@") {
		return nil, fmt.Errorf("open window: unexpected reply %q", lines[0])
	}
	ref := &session.TmuxRef{SocketPath: p.SocketPath, Session: UnvisName(f[2]), WindowID: f[1], PaneID: f[0]}
	if !p.AutoRename {
		if _, err := t.Run(ctx, fmt.Sprintf("set-option -w -t %s automatic-rename off", Quote(ref.WindowID))); err != nil {
			return nil, fmt.Errorf("automatic-rename off: %w", err)
		}
	}
	return ref, nil
}

// KillWindow removes a window by id.
func KillWindow(ctx context.Context, t Transport, windowID string) error {
	if _, err := t.Run(ctx, fmt.Sprintf("kill-window -t %s", Quote(windowID))); err != nil {
		return fmt.Errorf("kill-window %s: %w", windowID, err)
	}
	return nil
}
