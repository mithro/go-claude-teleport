package tmuxx

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/session"
)

type prober struct {
	ctx    context.Context
	t      Transport
	procs  *procx.Table
	socket string
}

// Prober adapts a Transport to session.PaneProbe.
func Prober(ctx context.Context, t Transport, procs *procx.Table, socketPath string) session.PaneProbe {
	return &prober{ctx: ctx, t: t, procs: procs, socket: socketPath}
}

func (p *prober) PaneCommand(paneID string) ([]string, int, bool) {
	st, err := State(p.ctx, p.t, paneID, p.procs)
	if err != nil {
		return nil, 0, false
	}
	return st.Argv, st.PID, true
}

// FindWindow targets "<session>:<window>". sess is tmux's stored spelling
// (see SessionInfo.Name) and is NOT decoded: `-t` resolves stored names.
func (p *prober) FindWindow(sess, window string) ([]string, error) {
	target := "=" + sess + ":" + window
	if _, err := strconv.Atoi(window); err != nil {
		target = "=" + sess + ":=" + window // exact window-name match
	}
	lines, err := p.t.Run(p.ctx, fmt.Sprintf(`list-panes -t %s -F "#{pane_id}"`, Quote(target)))
	if err != nil {
		return nil, fmt.Errorf("window %s %s: %w", sess, window, err)
	}
	var out []string
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("window %s %s: no panes", sess, window)
	}
	return out, nil
}

func (p *prober) SocketPath() string { return p.socket }

// listPanesFormat: tab-separated for the same reason describeFormat is —
// tmux does NOT vis-encode a space in a session name (probe-verified on
// next-3.8: a session created as "a b" is reported as "a b"), so a
// space-separated format splits such a name into two fields and the pane
// vanishes from suspended-pane discovery.
const listPanesFormat = "#{session_name}\t#{window_id}\t#{pane_id}"

// ListPanes implements session.PaneProbe.ListPanes (Plan 01 addition) for
// suspended-pane discovery in session.Load: every pane on the server.
// Session names keep tmux's stored spelling (see SessionInfo.Name).
func (p *prober) ListPanes() ([]session.PaneInfo, error) {
	lines, err := p.t.Run(p.ctx, `list-panes -a -F "`+listPanesFormat+`"`)
	if err != nil {
		return nil, fmt.Errorf("list-panes -a: %w", err)
	}
	var out []session.PaneInfo
	for _, l := range lines {
		f := strings.SplitN(l, "\t", 3)
		if len(f) != 3 {
			// Never skip: a dropped pane is an invisible session, which is
			// exactly the failure this format change fixes.
			return nil, fmt.Errorf("list-panes -a: malformed line %q", l)
		}
		out = append(out, session.PaneInfo{Session: f[0], WindowID: f[1], PaneID: f[2]})
	}
	return out, nil
}
