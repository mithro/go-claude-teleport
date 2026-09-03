package tmuxx

import (
	"context"
	"fmt"
	"sort"
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

// FindWindow targets "<session>:<window>". sess is what a HUMAN typed at
// the CLI selector (spec §5 rule 4: `<tmux-session> <window>`) — it is
// resolved against tmux's actual session list first (R-PRB-9, below)
// before being used as a `-t` target.
func (p *prober) FindWindow(sess, window string) ([]string, error) {
	stored, err := p.resolveSessionName(sess)
	if err != nil {
		return nil, fmt.Errorf("window %s %s: %w", sess, window, err)
	}
	target := "=" + stored + ":" + window
	if _, err := strconv.Atoi(window); err != nil {
		target = "=" + stored + ":=" + window // exact window-name match
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

// resolveSessionName maps a human-typed tmux session name to tmux's
// stored, vis(3)-encoded spelling (R-PRB-9, ruling D): the CLI selector's
// two-word form (`<tmux-session> <window>`) is typed by a person, who may
// spell a session with special characters either as the plain text they
// read or as the raw vis-encoded form tmux itself would report — so a
// match is accepted against EITHER spelling of each session tmux lists.
// It is an error if the two spellings resolve to different sessions
// (ambiguous), matching resolvePrefix's ambiguity handling in
// session.Resolve. TmuxRef/SessionInfo keep the stored spelling everywhere
// else (R-PRB-2); only this human-input boundary decodes for comparison.
func (p *prober) resolveSessionName(typed string) (string, error) {
	sessions, err := ListSessions(p.ctx, p.t)
	if err != nil {
		return "", fmt.Errorf("list-sessions: %w", err)
	}
	found := map[string]bool{}
	for _, s := range sessions {
		if s.Name == typed || UnvisName(s.Name) == typed {
			found[s.Name] = true
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no session named %q", typed)
	case 1:
		for name := range found {
			return name, nil
		}
	}
	// The STORED spellings, deliberately not decoded (B7): decoding is what
	// makes these two indistinguishable, so a decoded list would repeat the
	// typed text back at the user. The stored form is also the one that
	// works as a `-t` target.
	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	return "", fmt.Errorf("%q is ambiguous between sessions: %s", typed, strings.Join(names, ", "))
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
