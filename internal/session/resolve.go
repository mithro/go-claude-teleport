package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNotFound is returned (wrapped) when no session matches.
var ErrNotFound = errors.New("session not found")

// TmuxRef is where a session's pane lives (from the registry or a pane scan).
//
// CONVENTION (R-PRB-2): Session carries tmux's STORED, vis(3)-encoded
// spelling — the name tmux itself reports and the only one a `-t` target
// resolves (a session created as `a\b` is stored, and must be targeted, as
// `a\\b`). Decode with tmuxx.UnvisName only when feeding a creation flag
// (`new-session -s`, `new-window -n`) or when displaying a name to a human.
type TmuxRef struct {
	SocketPath string
	Session    string // session name, in tmux's stored (vis-encoded) spelling
	WindowID   string // "@N"
	PaneID     string // "%N"
}

// PaneInfo identifies one pane for ListPanes. Session follows TmuxRef's
// convention: tmux's stored (vis-encoded) spelling.
type PaneInfo struct {
	Session  string
	WindowID string
	PaneID   string
	// SocketPath is the tmux server this pane is on. A host may run
	// several, so a pane is only located once this is known.
	SocketPath string
}

// PaneProbe lets Resolve consult tmux without importing tmuxx (Plan 03 wires
// tmuxx.Prober in; Plan 01 tests use a fake).
type PaneProbe interface {
	// PaneCommand returns the foreground command line (argv) and pid of the
	// pane; ok=false if the pane cannot be found.
	PaneCommand(paneID string) (argv []string, pid int, ok bool)
	// FindWindow resolves "<session> <window index|name>" to its pane ids.
	FindWindow(session, window string) (paneIDs []string, err error)
	// ListPanes enumerates every pane on every server (for suspended-pane
	// discovery); each carries the socket it was found on.
	ListPanes() ([]PaneInfo, error)
	// PaneSocket reports which tmux server holds paneID, "" if none does.
	// The registry records a pane with no socket in it, so this is the
	// lookup that turns that into a located pane.
	PaneSocket(paneID string) string
}

// Session is a located session.
type Session struct {
	ID         ID
	Paths      Paths
	ProjectDir string // the directory the transcript is really in (see ProjectCwd)
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

// ProjectCwd is the cwd whose project directory actually holds this
// session's transcript.
//
// Claude Code files a transcript under projects/Munge(cwd), but a session
// that changed directory records two cwds -- the first (LaunchCwd) and the
// last (WorkCwd) -- and only one of them names the directory the
// transcript is really in. ProjectDir is found by glob, so it is always
// the truth; this reports the cwd that agrees with it.
//
// Everything doing project-directory arithmetic must use THIS rather than
// LaunchCwd. On a session where the two differ, Munge(LaunchCwd) names a
// project directory that does not exist, so a path mapping built from it
// silently fails to apply and a destination asked to corroborate a root
// against it can never succeed.
//
// The fallback is LaunchCwd -- what every caller used before this existed
// -- so a layout matching neither (a hand-renamed project directory)
// behaves exactly as it did.
func (s *Session) ProjectCwd() string {
	return ProjectCwdOf(Meta{LaunchCwd: s.LaunchCwd, WorkCwd: s.WorkCwd}, s.ProjectDir)
}

// ProjectCwdOf is ProjectCwd for callers that have a Meta and the project
// directory but never built a Session -- `list`, which reads one Meta per
// transcript and must not pay for a full resolve of every session on the
// host. The rule lives here once so the two cannot drift apart.
func ProjectCwdOf(m Meta, projectDir string) string {
	base := filepath.Base(projectDir)
	if m.LaunchCwd != "" && Munge(m.LaunchCwd) == base {
		return m.LaunchCwd
	}
	if m.WorkCwd != "" && Munge(m.WorkCwd) == base {
		return m.WorkCwd
	}
	return m.LaunchCwd
}

// FindTranscript locates <projectsDir>/*/<id>.jsonl. Exactly one FILE must
// exist; see findTranscripts for why that is not the same as one path.
func FindTranscript(projectsDir string, id ID) (string, error) {
	hits, err := findTranscripts(projectsDir, id)
	if err != nil {
		return "", err
	}
	return hits[0], nil
}

// findTranscripts returns every path under projectsDir that names the
// session's transcript, sorted, and errors unless they all name the same
// file.
//
// One directory can be reached by several names. Claude Code files a
// transcript under projects/Munge(cwd), so a session whose work moves to
// another repository would start a second transcript -- unless the new
// munged name is a symlink to the directory already holding the first, at
// which point the one file is reachable by both spellings and the session
// keeps its history.
//
// filepath.Glob expands path components textually and never resolves a
// symlink, so it returns that file once per spelling. Counting the hits
// called this an ambiguous session and refused to touch it; identity is
// what matters, so compare with os.SameFile (dev+inode) instead. Two
// genuinely different files sharing an id stay an error -- collapsing
// those would pick one session's history at random.
func findTranscripts(projectsDir string, id ID) ([]string, error) {
	hits, err := filepath.Glob(filepath.Join(projectsDir, "*", string(id)+".jsonl"))
	if err != nil {
		return nil, fmt.Errorf("glob transcripts under %s: %w", projectsDir, err)
	}

	// One group per distinct file, each holding that file's spellings.
	var infos []os.FileInfo
	var groups [][]string
	for _, h := range hits {
		fi, err := os.Stat(h)
		if err != nil {
			continue // vanished, or a dangling symlink: not a transcript
		}
		placed := false
		for i, seen := range infos {
			if os.SameFile(fi, seen) {
				groups[i] = append(groups[i], h)
				placed = true
				break
			}
		}
		if !placed {
			infos = append(infos, fi)
			groups = append(groups, []string{h})
		}
	}

	switch len(groups) {
	case 0:
		return nil, fmt.Errorf("%w: no transcript %s.jsonl under %s", ErrNotFound, id, projectsDir)
	case 1:
		return groups[0], nil
	default:
		return nil, fmt.Errorf("session %s has %d transcripts under %s: %s", id, len(groups), projectsDir, strings.Join(hits, ", "))
	}
}

// pickSpelling chooses which of several names for one transcript roots the
// session.
//
// It decides ProjectDir, and everything downstream follows: ProjectCwd
// reads it back, and from that come the destination cwd, the repository
// transferred, and the directory the resumed Claude is launched in. The
// work cwd is where the session actually is -- its pane is there and
// Claude Code is appending there -- so that spelling wins. Rooting at the
// launch cwd would move the repository the session has left.
func pickSpelling(paths []string, meta Meta) string {
	for _, cwd := range []string{meta.WorkCwd, meta.LaunchCwd} {
		if cwd == "" {
			continue
		}
		want := Munge(cwd)
		for _, p := range paths {
			if filepath.Base(filepath.Dir(p)) == want {
				return p
			}
		}
	}
	return paths[0]
}

// Load reads an already-known session (by id) from disk; State is Idle
// unless the registry (with a live pid) or a placeholder pane says otherwise.
func Load(p Paths, id ID, probe PaneProbe) (*Session, error) {
	spellings, err := findTranscripts(p.ProjectsDir(), id)
	if err != nil {
		return nil, err
	}
	// Every spelling is the same file, so the metadata is read once and
	// only then decides which of them roots the session.
	meta, err := ReadMeta(spellings[0])
	if err != nil {
		return nil, err
	}
	transcript := pickSpelling(spellings, meta)
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
				s.Tmux.SocketPath = probe.PaneSocket(pane)
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
				s.Tmux = &TmuxRef{SocketPath: pi.SocketPath, Session: pi.Session, WindowID: pi.WindowID, PaneID: pi.PaneID}
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
