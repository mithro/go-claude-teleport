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

// ListPanes implements session.PaneProbe.ListPanes (Plan 01 addition) for
// suspended-pane discovery in session.Load: every pane on the server.
func (p *prober) ListPanes() ([]session.PaneInfo, error) {
	lines, err := p.t.Run(p.ctx, `list-panes -a -F "#{session_name} #{window_id} #{pane_id}"`)
	if err != nil {
		return nil, fmt.Errorf("list-panes -a: %w", err)
	}
	var out []session.PaneInfo
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) != 3 {
			continue
		}
		out = append(out, session.PaneInfo{Session: UnvisName(f[0]), WindowID: f[1], PaneID: f[2]})
	}
	return out, nil
}
