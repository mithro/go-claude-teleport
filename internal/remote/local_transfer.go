package remote

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/mithro/go-claude-teleport/internal/session"
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
	return os.RemoveAll(l.stagingDir(jobID))
}

// ListSessions scans the projects tree and the registry (spec §5 `list`).
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
			}
			out = append(out, sum)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastTS > out[j].LastTS })
	return out, nil
}
