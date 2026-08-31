package session

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNotFound is returned (wrapped) when no session matches.
var ErrNotFound = errors.New("session not found")

// TmuxRef is where a session's pane lives (from the registry or a pane scan).
type TmuxRef struct {
	SocketPath string
	Session    string // session name
	WindowID   string // "@N"
	PaneID     string // "%N"
}

// PaneInfo identifies one pane for ListPanes.
type PaneInfo struct {
	Session  string
	WindowID string
	PaneID   string
}

// PaneProbe lets Resolve consult tmux without importing tmuxx (Plan 03 wires
// tmuxx.Prober in; Plan 01 tests use a fake).
type PaneProbe interface {
	// PaneCommand returns the foreground command line (argv) and pid of the
	// pane; ok=false if the pane cannot be found.
	PaneCommand(paneID string) (argv []string, pid int, ok bool)
	// FindWindow resolves "<session> <window index|name>" to its pane ids.
	FindWindow(session, window string) (paneIDs []string, err error)
	// ListPanes enumerates every pane on the server (for suspended-pane discovery).
	ListPanes() ([]PaneInfo, error)
	SocketPath() string
}

// Session is a located session.
type Session struct {
	ID         ID
	Paths      Paths
	ProjectDir string // <ProjectsDir>/<munged launch cwd>
	Transcript string // <ProjectDir>/<id>.jsonl
	LaunchCwd  string // first cwd in the transcript
	WorkCwd    string // last cwd in the transcript
	Branch     string // last gitBranch
	Name       string // registry name if running, else ""
	Version    string // claude version from the transcript (last "version")
	State      State
	Registry   *Registry // non-nil iff StateRunning
	Tmux       *TmuxRef  // non-nil when a pane is known (running or suspended)
}

// FindTranscript locates <projectsDir>/*/<id>.jsonl. Exactly one must exist.
func FindTranscript(projectsDir string, id ID) (string, error) {
	hits, err := filepath.Glob(filepath.Join(projectsDir, "*", string(id)+".jsonl"))
	if err != nil {
		return "", fmt.Errorf("glob transcripts under %s: %w", projectsDir, err)
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("%w: no transcript %s.jsonl under %s", ErrNotFound, id, projectsDir)
	case 1:
		return hits[0], nil
	default:
		return "", fmt.Errorf("session %s has %d transcripts under %s: %s", id, len(hits), projectsDir, strings.Join(hits, ", "))
	}
}

// Load reads an already-known session (by id) from disk; State is Idle
// unless the registry (with a live pid) or a placeholder pane says otherwise.
func Load(p Paths, id ID, probe PaneProbe) (*Session, error) {
	transcript, err := FindTranscript(p.ProjectsDir(), id)
	if err != nil {
		return nil, err
	}
	meta, err := ReadMeta(transcript)
	if err != nil {
		return nil, err
	}
	s := &Session{ID: id, Paths: p, ProjectDir: filepath.Dir(transcript), Transcript: transcript,
		LaunchCwd: meta.LaunchCwd, WorkCwd: meta.WorkCwd, Branch: meta.Branch, Version: meta.Version, State: StateIdle}
	regs, err := ReadRegistry(p.SessionsDir())
	if err != nil {
		return nil, err
	}
	for i := range regs {
		r := regs[i]
		if r.SessionID != string(id) || !ProcAlive(p.ProcRoot, r.PID, r.ProcStart) {
			continue
		}
		s.State, s.Registry, s.Name = StateRunning, &r, r.Name
		if sess, win, pane, ok := r.TmuxParts(); ok {
			s.Tmux = &TmuxRef{Session: sess, WindowID: win, PaneID: pane}
			if probe != nil {
				s.Tmux.SocketPath = probe.SocketPath()
			}
		}
		return s, nil
	}
	if probe != nil {
		panes, err := probe.ListPanes()
		if err != nil {
			return nil, fmt.Errorf("list tmux panes: %w", err)
		}
		for _, pi := range panes {
			argv, _, ok := probe.PaneCommand(pi.PaneID)
			if !ok {
				continue
			}
			if sid, ph, ok := ArgvSessionID(argv); ok && ph && sid == string(id) {
				s.State = StateSuspended
				s.Tmux = &TmuxRef{SocketPath: probe.SocketPath(), Session: pi.Session, WindowID: pi.WindowID, PaneID: pi.PaneID}
				break
			}
		}
	}
	return s, nil
}

// Resolve turns a selector into a Session (spec §5 rules 1–4). Ambiguity is
// an error listing the candidates; not found wraps ErrNotFound.
func Resolve(p Paths, sel Selector, probe PaneProbe) (*Session, error) {
	switch {
	case sel.ID != "":
		return Load(p, sel.ID, probe)
	case sel.TmuxSess != "":
		return resolveWindow(p, sel, probe)
	case sel.Prefix != "":
		return resolvePrefix(p, sel.Prefix, probe)
	case sel.Current:
		return resolveCurrent(p, sel, probe)
	}
	return nil, fmt.Errorf("empty selector")
}

// liveRegistry returns registry entries whose pid is alive with a matching procStart.
func liveRegistry(p Paths) ([]Registry, error) {
	regs, err := ReadRegistry(p.SessionsDir())
	if err != nil {
		return nil, err
	}
	var live []Registry
	for _, r := range regs {
		if ProcAlive(p.ProcRoot, r.PID, r.ProcStart) {
			live = append(live, r)
		}
	}
	return live, nil
}

func sessionFromPane(p Paths, paneID string, live []Registry, probe PaneProbe) (*Session, error) {
	for _, r := range live {
		if _, _, pane, ok := r.TmuxParts(); ok && pane == paneID {
			return Load(p, ID(r.SessionID), probe)
		}
	}
	if probe == nil {
		return nil, fmt.Errorf("%w: no running claude in pane %s (tmux not available to inspect it)", ErrNotFound, paneID)
	}
	argv, pid, ok := probe.PaneCommand(paneID)
	if !ok {
		return nil, fmt.Errorf("%w: pane %s not found", ErrNotFound, paneID)
	}
	if sid, _, ok := ArgvSessionID(argv); ok && sid != "" {
		return Load(p, ID(sid), probe)
	}
	for _, r := range live { // a claude whose registry lacks a tmux field
		if r.PID == pid {
			return Load(p, ID(r.SessionID), probe)
		}
	}
	return nil, fmt.Errorf("%w: pane %s runs %q, not a claude or placeholder", ErrNotFound, paneID, strings.Join(argv, " "))
}

func resolveCurrent(p Paths, sel Selector, probe PaneProbe) (*Session, error) {
	live, err := liveRegistry(p)
	if err != nil {
		return nil, err
	}
	if sel.TmuxPane != "" {
		return sessionFromPane(p, sel.TmuxPane, live, probe)
	}
	var cands []string
	for _, r := range live {
		cands = append(cands, fmt.Sprintf("  %s  %-12s %s", r.SessionID, r.Name, r.Cwd))
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("%w: no session given and none running (set CLAUDE_CODE_SESSION_ID, run inside tmux, or pass a session id)", ErrNotFound)
	}
	return nil, fmt.Errorf("no session given; running sessions:\n%s", strings.Join(cands, "\n"))
}

func resolvePrefix(p Paths, prefix string, probe PaneProbe) (*Session, error) {
	live, err := liveRegistry(p)
	if err != nil {
		return nil, err
	}
	found := map[string]bool{}
	lower := strings.ToLower(prefix)
	for _, r := range live {
		if r.Name == prefix || strings.HasPrefix(r.SessionID, lower) {
			found[r.SessionID] = true
		}
	}
	if hexRe.MatchString(lower) || strings.ContainsRune(lower, '-') {
		hits, err := filepath.Glob(filepath.Join(p.ProjectsDir(), "*", lower+"*.jsonl"))
		if err != nil {
			return nil, fmt.Errorf("glob transcripts: %w", err)
		}
		for _, h := range hits {
			base := strings.TrimSuffix(filepath.Base(h), ".jsonl")
			if IsUUID(base) {
				found[base] = true
			}
		}
	}
	ids := make([]string, 0, len(found))
	for id := range found {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	switch len(ids) {
	case 0:
		return nil, fmt.Errorf("%w: nothing matches %q", ErrNotFound, prefix)
	case 1:
		return Load(p, ID(ids[0]), probe)
	default:
		return nil, fmt.Errorf("%q is ambiguous; candidates:\n  %s", prefix, strings.Join(ids, "\n  "))
	}
}

func resolveWindow(p Paths, sel Selector, probe PaneProbe) (*Session, error) {
	if probe == nil {
		return nil, fmt.Errorf("resolve %s %s: tmux is not available", sel.TmuxSess, sel.TmuxWindow)
	}
	panes, err := probe.FindWindow(sel.TmuxSess, sel.TmuxWindow)
	if err != nil {
		return nil, fmt.Errorf("resolve %s %s: %w", sel.TmuxSess, sel.TmuxWindow, err)
	}
	live, err := liveRegistry(p)
	if err != nil {
		return nil, err
	}
	var last error
	for _, pane := range panes {
		s, err := sessionFromPane(p, pane, live, probe)
		if err == nil {
			return s, nil
		}
		last = err
	}
	return nil, fmt.Errorf("%w: window %s %s has no claude or placeholder pane (%v)", ErrNotFound, sel.TmuxSess, sel.TmuxWindow, last)
}
