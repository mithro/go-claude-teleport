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
		id, err := p.resolveWindowName(stored, window)
		if err != nil {
			return nil, fmt.Errorf("window %s %s: %w", sess, window, err)
		}
		target = id
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

// resolveWindowName maps a window NAME to its tmux window id ("@N").
//
// A tmux target is "<session>:<window>.<pane>", so tmux splits a target at
// the dot — and a name containing one can never be targeted by name. Real
// case (2026-09-17): window "tt mech f.o" in session "tt" gave
//
//	can't find window: tt mech f
//
// tmux having taken ".o" for a pane specifier. A window id is
// server-global, unambiguous and dot-free, so that is what gets targeted.
//
// The name is matched the way session names are (R-PRB-9): tmux stores the
// vis(3)-encoded spelling while a human types the plain one, so either is
// accepted. A name shared by two windows of the session is an error rather
// than a silent pick — the error names the candidates so the caller can
// re-run with an index.
func (p *prober) resolveWindowName(storedSession, typed string) (string, error) {
	// The trailing colon is load-bearing: it makes the target explicitly
	// "<session>:" so tmux stops looking for a window/pane part, which is
	// the only way a session name containing a dot can be targeted at all
	// (see TestFindWindowInSessionWhoseNameContainsADot). new-window in
	// window.go composes its target the same way.
	lines, err := p.t.Run(p.ctx, fmt.Sprintf("list-windows -t %s -F \"#{window_id}\t#{window_name}\"",
		Quote("="+storedSession+":")))
	if err != nil {
		return "", fmt.Errorf("list-windows: %w", err)
	}
	var ids []string
	for _, l := range lines {
		id, name, ok := strings.Cut(l, "\t")
		if !ok {
			continue
		}
		if name == typed || UnvisName(name) == typed {
			ids = append(ids, id)
		}
	}
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("no window named %q in session %s", typed, storedSession)
	case 1:
		return ids[0], nil
	}
	return "", fmt.Errorf("session %s has %d windows named %q (%s); use the window index instead",
		storedSession, len(ids), typed, strings.Join(ids, ", "))
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

// PaneSocket answers for the one server this prober holds: the socket when
// the pane is on it, "" otherwise. multiProber does the cross-server work.
func (p *prober) PaneSocket(paneID string) string {
	panes, err := p.ListPanes()
	if err != nil {
		return ""
	}
	for _, pi := range panes {
		if pi.PaneID == paneID {
			return p.socket
		}
	}
	return ""
}

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
		out = append(out, session.PaneInfo{Session: f[0], WindowID: f[1], PaneID: f[2], SocketPath: p.socket})
	}
	return out, nil
}
