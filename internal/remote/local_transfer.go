package remote

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// SessionSummary is one row of ListSessions (spec §5 `list`).
type SessionSummary struct {
	ID      session.ID `json:"id"`
	State   string     `json:"state"`
	Cwd     string     `json:"cwd"`
	Branch  string     `json:"branch"`
	Version string     `json:"version"`
	LastTS  string     `json:"last_ts"`
	Name    string     `json:"name"`
	Tmux    string     `json:"tmux"`
}

// BuildManifest hashes files on this host and saves jobs/<jobID>/manifest.json.
func (l *Local) BuildManifest(ctx context.Context, jobID string, id session.ID, srcHost, dstHost string, files []session.FileEntry, pm session.PathMap) (*transfer.Manifest, error) {
	if err := checkJobID(jobID); err != nil {
		return nil, err
	}
	m, err := transfer.Build(ctx, jobID, id, srcHost, dstHost, files, pm)
	if err != nil {
		return nil, &Error{Code: "internal", Message: fmt.Sprintf("build manifest: %v", err)}
	}
	dir := l.jobDir(jobID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := m.Save(filepath.Join(dir, "manifest.json")); err != nil {
		return nil, err
	}
	return m, nil
}

// SessionExtras collects the merge inputs of spec §7.5 with paths rewritten.
func (l *Local) SessionExtras(ctx context.Context, id session.ID, pm session.PathMap) (*transfer.InstallExtras, error) {
	s, err := session.Load(l.paths, id, l.opts.Probe)
	if err != nil {
		return nil, &Error{Code: "not-found", Message: err.Error()}
	}
	ex := &transfer.InstallExtras{ProjectCwd: pm.ApplyPath(s.LaunchCwd)}
	if ie, ok, err := session.ReadIndexEntry(s.ProjectDir, id); err != nil {
		return nil, err
	} else if ok {
		ie.FullPath = pm.ApplyPath(ie.FullPath)
		ie.ProjectPath = pm.ApplyPath(ie.ProjectPath)
		ex.IndexEntry = ie
	}
	lines, err := session.ExtractHistory(l.paths.HistoryFile(), id)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, line := range lines {
		var out bytes.Buffer
		if _, err := session.RewriteJSON(bytes.NewReader(line), &out, pm); err != nil {
			return nil, err
		}
		ex.History = append(ex.History, bytes.TrimSpace(out.Bytes()))
	}
	if pe, ok, err := session.ReadProjectEntry(l.paths.GlobalJSON, s.LaunchCwd); err != nil {
		return nil, err
	} else if ok {
		ex.ProjectEntry = pe
	}
	return ex, nil
}

// Cleanup removes staging/<jobID> after a successful job.
func (l *Local) Cleanup(ctx context.Context, jobID string) error {
	if err := checkJobID(jobID); err != nil {
		return err
	}
	return os.RemoveAll(l.stagingDir(jobID))
}

// RemoveJob removes jobs/<jobID>/ entirely (manifest.json, extras.json,
// job.json, ...) — unlike Cleanup, which only removes staging/<jobID>/.
// jobID must be a valid job id (R-P3-23n) — this method deletes a whole
// directory tree, so an id that could escape the data dir is refused here
// as well as at the wire boundary. The further "inspect-" prefix rule
// (only inspect --host's throwaway jobs are removable by a remote peer)
// stays in the wire dispatch handler (ops_plan03.go): Local is the
// low-level primitive this host's own code uses for any job.
func (l *Local) RemoveJob(ctx context.Context, jobID string) error {
	if err := checkJobID(jobID); err != nil {
		return err
	}
	return os.RemoveAll(l.jobDir(jobID))
}

// DeleteInstalled removes the manifest entries named by ids from this
// host's filesystem — the destination side of `abandon
// --delete-destination-files`. The manifest and ids only ever NARROW what
// goes: the licence to delete anything at all comes from this host's own
// jobs/<id>/installed.json, written by the Install that placed it, and the
// content must still match what was placed (ruling R-P3-B1f N3).
func (l *Local) DeleteInstalled(ctx context.Context, m *transfer.Manifest, ids []int) ([]string, error) {
	// The job id here names no directory (deletion is bounded by the
	// manifest's own entries), but a manifest that carries one at all
	// must carry a legitimate one: a wire payload whose job id is a
	// traversal is a caller this host should not be serving.
	if m == nil {
		return nil, &Error{Code: "usage", Message: "delete-installed: nil manifest"}
	}
	if m.JobID != "" {
		if err := checkJobID(m.JobID); err != nil {
			return nil, err
		}
	}
	return transfer.UninstallIDs(m, l.paths, ids)
}

// ListSessions scans the projects tree and the registry (spec §5 `list`).
//
// The three states are registry-alive => running, placeholder pane =>
// suspended, otherwise idle: exactly what the local `list` reports
// (internal/cli.listSessions) and what session.Load/ResolveSession derive
// from the same probe. `list --host` reaches this method over the wire, so
// without the probe consultation below a suspended session on the remote
// host was indistinguishable from an idle one — the one state the whole
// placeholder mechanism exists to make visible.
func (l *Local) ListSessions(ctx context.Context) ([]SessionSummary, error) {
	regs, err := session.ReadRegistry(l.paths.SessionsDir())
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	procs, err := l.procs()
	if err != nil {
		return nil, err
	}
	byID := map[string]session.Registry{}
	for _, r := range regs {
		if procs.Alive(r.PID, r.ProcStart) {
			byID[r.SessionID] = r
		}
	}
	// A live registry entry always wins: a running Claude whose pane also
	// happens to carry a placeholder argv is running, not suspended (the
	// same precedence session.Load applies by returning before it probes).
	// A nil probe means this host has no reachable tmux server, in which
	// case no session can be suspended at all.
	suspended := map[string]session.PaneInfo{}
	if l.opts.Probe != nil {
		panes, err := l.opts.Probe.ListPanes()
		if err != nil {
			return nil, fmt.Errorf("list tmux panes on %s: %w", l.Hostname, err)
		}
		for _, pi := range panes {
			argv, _, ok := l.opts.Probe.PaneCommand(pi.PaneID)
			if !ok {
				continue
			}
			sid, placeholder, ok := session.ArgvSessionID(argv)
			if !ok || !placeholder || sid == "" {
				continue
			}
			if _, running := byID[sid]; running {
				continue
			}
			suspended[sid] = pi
		}
	}
	projects, err := os.ReadDir(l.paths.ProjectsDir())
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var out []SessionSummary
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(l.paths.ProjectsDir(), p.Name()))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			name := f.Name()
			if filepath.Ext(name) != ".jsonl" {
				continue
			}
			id, err := session.ParseID(name[:len(name)-len(".jsonl")])
			if err != nil {
				continue
			}
			meta, err := session.ReadMeta(filepath.Join(l.paths.ProjectsDir(), p.Name(), name))
			if err != nil {
				return nil, err
			}
			sum := SessionSummary{ID: id, State: session.StateIdle.String(), Cwd: meta.LaunchCwd, Branch: meta.Branch, Version: meta.Version, LastTS: meta.LastTS}
			if r, ok := byID[string(id)]; ok {
				sum.State, sum.Name, sum.Tmux = session.StateRunning.String(), r.Name, r.Tmux
			} else if pi, ok := suspended[string(id)]; ok {
				sum.State = session.StateSuspended.String()
				sum.Tmux = tmuxx.RefString(&session.TmuxRef{Session: pi.Session, WindowID: pi.WindowID, PaneID: pi.PaneID})
			}
			out = append(out, sum)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastTS > out[j].LastTS })
	return out, nil
}
