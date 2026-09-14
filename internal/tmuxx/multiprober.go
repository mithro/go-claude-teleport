package tmuxx

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"context"

	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// ListLiveServers returns the sockets under socketDir that a server is
// actually answering on, sorted. A socket file outlives the server that made
// it — a crashed tmux, or this project's own integration tests — so "which
// files are here" and "which servers exist" are different questions, and
// only the second one may decide anything.
func ListLiveServers(socketDir string) ([]string, error) {
	all, err := ListServers(socketDir)
	if err != nil {
		return nil, err
	}
	var live []string
	for _, p := range all {
		if alive(p) {
			live = append(live, p)
		}
	}
	return live, nil
}

// multiProber spans every live tmux server on a host.
//
// A machine may run several tmux servers at once — `tmux -L main` alongside
// the default socket is an ordinary setup — and the session registry records
// a pane as "<session>:@win.%pane" with no socket in it. Binding one server
// and attributing every session to it is therefore a guess: right by luck
// where there is one server, and silently wrong where there are two. This
// asks each server which panes it has and answers from that.
type multiProber struct {
	probers []*prober // sorted by socket path, so answers are deterministic
	once    sync.Once
	owner   map[string]*prober // pane id -> the server holding it
}

// MultiProber adapts one Transport per socket to session.PaneProbe.
func MultiProber(ctx context.Context, transports map[string]Transport, procs *procx.Table) session.PaneProbe {
	sockets := make([]string, 0, len(transports))
	for s := range transports {
		sockets = append(sockets, s)
	}
	sort.Strings(sockets)
	m := &multiProber{}
	for _, s := range sockets {
		m.probers = append(m.probers, &prober{ctx: ctx, t: transports[s], procs: procs, socket: s})
	}
	return m
}

// index maps every pane on every server to the server holding it, once. A
// server that fails to answer is skipped rather than fatal: one broken
// server must not hide the sessions on the others.
func (m *multiProber) index() map[string]*prober {
	m.once.Do(func() {
		m.owner = map[string]*prober{}
		for _, p := range m.probers {
			panes, err := p.ListPanes()
			if err != nil {
				continue
			}
			for _, pi := range panes {
				if _, seen := m.owner[pi.PaneID]; !seen {
					m.owner[pi.PaneID] = p
				}
			}
		}
	})
	return m.owner
}

// PaneSocket reports which server holds paneID, or "" when no server does.
func (m *multiProber) PaneSocket(paneID string) string {
	if p := m.index()[paneID]; p != nil {
		return p.socket
	}
	return ""
}

func (m *multiProber) PaneCommand(paneID string) ([]string, int, bool) {
	if p := m.index()[paneID]; p != nil {
		return p.PaneCommand(paneID)
	}
	return nil, 0, false
}

func (m *multiProber) ListPanes() ([]session.PaneInfo, error) {
	var out []session.PaneInfo
	for _, p := range m.probers {
		panes, err := p.ListPanes()
		if err != nil {
			return nil, err
		}
		out = append(out, panes...)
	}
	return out, nil
}

// FindWindow resolves the CLI's "<tmux-session> <window>" selector against
// every server. A session name that exists on two of them is ambiguous and
// is reported as such, the way an ambiguous session-id prefix is: picking
// one would open the window on a server the user did not mean.
func (m *multiProber) FindWindow(sess, window string) ([]string, error) {
	var (
		hits     [][]string
		sockets  []string
		firstErr error
	)
	for _, p := range m.probers {
		panes, err := p.FindWindow(sess, window)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		hits = append(hits, panes)
		sockets = append(sockets, p.socket)
	}
	switch len(hits) {
	case 0:
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, fmt.Errorf("window %s %s: no panes", sess, window)
	case 1:
		return hits[0], nil
	}
	return nil, fmt.Errorf("window %s %s is on more than one tmux server: %s (use --tmux-socket NAME to choose)",
		sess, window, strings.Join(sockets, ", "))
}
