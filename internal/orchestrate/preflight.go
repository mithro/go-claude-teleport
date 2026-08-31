// internal/orchestrate/preflight.go
package orchestrate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// isCode reports whether err is a remote.Error with the given code.
func isCode(err error, code string) bool {
	var re *remote.Error
	return errors.As(err, &re) && re.Code == code
}

// Preflight is spec §6 step 1. It touches nothing outside the two hosts'
// job directories.
func Preflight(ctx context.Context, o Options, src, dst remote.Endpoint, jobID string) (*Plan, error) {
	p := &Plan{Options: o, JobID: jobID}
	var err error
	if p.SourceInfo, err = src.Hello(ctx); err != nil {
		return nil, &UnreachableError{Host: "source", Err: err}
	}
	if p.DestInfo, err = dst.Hello(ctx); err != nil {
		return nil, &UnreachableError{Host: o.Target, Err: err}
	}
	if p.SourceInfo.Version != p.DestInfo.Version {
		return nil, &UnreachableError{Host: o.Target, Err: fmt.Errorf("claude-teleport version mismatch: source %s, destination %s — install the same version on both hosts", p.SourceInfo.Version, p.DestInfo.Version)}
	}
	if !p.DestInfo.HasClaude {
		return nil, refusef("claude is not installed on %s", p.DestInfo.Hostname)
	}

	if p.Session, err = src.ResolveSession(ctx, o.Selector); err != nil {
		return nil, err
	}
	sess := p.Session
	if o.BangMode && sess.State != session.StateRunning {
		return nil, refusef("!-mode requires the session to be running (it is %s)", sess.State)
	}
	inv, usage, err := src.InventorySession(ctx, sess.ID)
	if err != nil {
		return nil, err
	}

	// Path map (spec §7.2), longest prefix first.
	var maps []session.Mapping
	maps = append(maps, o.Maps...)
	if o.DestPath != "" {
		maps = append(maps, session.Mapping{From: sess.LaunchCwd, To: o.DestPath})
	}
	if p.SourceInfo.Home != p.DestInfo.Home {
		maps = append(maps, session.Mapping{From: p.SourceInfo.Home, To: p.DestInfo.Home})
	}
	if p.SourceInfo.DataDir != p.DestInfo.DataDir {
		maps = append(maps, session.Mapping{From: p.SourceInfo.DataDir, To: p.DestInfo.DataDir})
	}
	p.PathMap = session.NewPathMap(maps...)
	p.DestCwd = p.PathMap.ApplyPath(sess.LaunchCwd)
	p.DestCapture = filepath.Join(job.Dir(p.DestInfo.DataDir, jobID), "capture.txt")

	// Drift (spec §10).
	srcVersion := p.SourceInfo.ClaudeVersion
	if sess.Registry != nil && sess.Registry.Version != "" {
		srcVersion = sess.Registry.Version
	}
	srcCfg, err := src.InventoryHost(ctx, sess.LaunchCwd, srcVersion)
	if err != nil {
		return nil, err
	}
	dstCfg, err := dst.InventoryHost(ctx, p.DestCwd, p.DestInfo.ClaudeVersion)
	if err != nil {
		return nil, err
	}
	p.Drift = claudecfg.Compare(srcCfg, dstCfg, usage)
	if o.AllowDrift {
		p.Drift = p.Drift.Downgrade()
	}
	if p.Drift.Blocking {
		var buf bytes.Buffer
		p.Drift.Render(&buf)
		return nil, refusef("configuration drift would change the session's behaviour on %s (use --allow-config-drift to proceed):\n%s", p.DestInfo.Hostname, buf.String())
	}

	// Git (spec §8).
	gi, err := src.InventoryGit(ctx, sess.LaunchCwd)
	switch {
	case isCode(err, "not-found"):
		p.Git = &gitx.Plan{Mode: gitx.ModeNotRepo, SrcWorktree: sess.LaunchCwd, DstWorktree: p.DestCwd, PackEntryID: gitx.NoEntry, IndexEntryID: gitx.NoEntry}
	case err != nil:
		return nil, err
	default:
		ds, err := dst.GitDestState(ctx, p.PathMap.ApplyPath(gi.MainDir), p.PathMap.ApplyPath(gi.Root), gi.Branch)
		if err != nil {
			return nil, err
		}
		indexRel := ".git/index"
		if gi.IsLinked {
			indexRel = ".git/worktrees/" + gi.WorktreeName + "/index"
		}
		facts, err := src.GitSourceFacts(ctx, gi.MainDir, indexRel, gi.Head, ds.BranchTip)
		if err != nil {
			return nil, err
		}
		ds.BranchTipReachable = facts.DestTipReachable
		gp, err := gitx.PlanTransfer(gi, ds, p.PathMap)
		var re *gitx.RefuseError
		if errors.As(err, &re) {
			return nil, refusef("git: %s", re.Reason)
		}
		if err != nil {
			return nil, err
		}
		gp.SetStagedBlobs(facts.StagedBlobs)
		p.Git = gp
	}
	gitFiles, err := src.GitFiles(ctx, p.Git, o.Excludes, o.IncludeIgnored)
	if err != nil {
		return nil, err
	}

	// tmux (spec §9).
	if !o.NoTmux && p.DestInfo.HasTmux {
		if sess.Tmux != nil {
			if p.SourceFacts, err = src.InventoryTmux(ctx, sess.Tmux, ""); err != nil {
				return nil, err
			}
		}
		preferred := o.TmuxSocket
		if preferred == "" && sess.Tmux != nil {
			preferred = filepath.Base(sess.Tmux.SocketPath)
		}
		dfacts, err := dst.InventoryTmux(ctx, nil, preferred)
		switch {
		case isCode(err, "unavailable"):
			p.Tmux = nil
		case err != nil:
			return nil, err
		default:
			tp := &tmuxx.Plan{SocketPath: dfacts.SocketPath, Group: "claude", WindowName: "claude", AutoRename: true, Cwd: p.DestCwd}
			if p.SourceFacts != nil {
				tp.Group, tp.WindowName, tp.AutoRename = p.SourceFacts.Group, p.SourceFacts.WindowName, p.SourceFacts.AutoRename
				if tp.Group == "" {
					tp.Group = p.SourceFacts.SessionName
				}
			}
			sessions, err := dst.TmuxSessions(ctx, dfacts.SocketPath)
			if err != nil {
				return nil, err
			}
			_, exists := tmuxx.BaseSession(sessions, tp.Group)
			tp.CreateSession = !exists
			p.Tmux = tp
		}
	}

	// Target state.
	p.TargetState = o.State
	if p.TargetState == "" || p.TargetState == "auto" {
		p.TargetState = sess.State.String()
	}
	if p.Tmux == nil && p.TargetState != "idle" {
		return nil, refusef("no usable tmux server on %s (or --no-tmux): only --state idle is possible, not %q", p.DestInfo.Hostname, p.TargetState)
	}

	// Merge inputs and manifest (spec §7).
	if p.Extras, err = src.SessionExtras(ctx, sess.ID, p.PathMap); err != nil {
		return nil, err
	}
	p.Files = append(append(append([]session.FileEntry{}, inv.Files...), inv.Memory...), gitFiles...)
	m, err := src.BuildManifest(ctx, jobID, sess.ID, p.SourceInfo.Hostname, p.DestInfo.Hostname, p.Files, p.PathMap)
	if err != nil {
		return nil, err
	}
	p.annotateManifest(m, inv)
	p.ManifestPath = filepath.Join(job.Dir(driverDataDir(src, dst, o), jobID), "manifest.json")
	if err := m.Save(p.ManifestPath); err != nil {
		return nil, err
	}
	if p.Statuses, err = dst.ManifestDiff(ctx, m, jobID); err != nil {
		return nil, err
	}
	memory := map[int]bool{}
	for _, e := range p.Extras.Memory {
		memory[e.ID] = true
	}
	for _, e := range transfer.Blocking(m, p.Statuses, o.Force) {
		if !memory[e.ID] {
			p.Collisions = append(p.Collisions, e)
		}
	}
	if len(p.Collisions) > 0 {
		var b strings.Builder
		for _, e := range p.Collisions {
			fmt.Fprintf(&b, "  %s (%s)\n", e.Dst, p.Statuses[e.ID])
		}
		return nil, refusef("%d destination file(s) already exist with different content on %s:\n%s", len(p.Collisions), p.DestInfo.Hostname, b.String())
	}
	return p, nil
}

// annotateManifest records which manifest entries the git attach step
// applies itself (existing-main) and which are memory files.
func (p *Plan) annotateManifest(m *transfer.Manifest, inv *session.Inventory) {
	memoryRoots := map[string]bool{}
	for _, e := range inv.Memory {
		memoryRoots[e.Path()] = true
	}
	p.Extras.Memory = nil
	if p.Git.Mode == gitx.ModeExistingMain {
		p.Git.DirtyEntries = map[string]int{}
	}
	indexSrc := filepath.Join(p.Git.SrcMain, filepath.FromSlash(p.Git.IndexRel))
	for _, e := range m.Entries {
		if memoryRoots[e.Src] {
			p.Extras.Memory = append(p.Extras.Memory, e)
			continue
		}
		if p.Git.Mode != gitx.ModeExistingMain {
			continue
		}
		switch {
		case e.Category == session.CatRepo && e.Src == indexSrc:
			p.Git.IndexEntryID = e.ID
		case e.Category == session.CatWorktree:
			p.Git.DirtyEntries[e.Dst] = e.ID
		}
	}
}

// driverDataDir is the local data dir: the source's for --to, the
// destination's for --from.
func driverDataDir(src, dst remote.Endpoint, o Options) string {
	if o.Direction == "from" {
		return dst.Paths().DataDir
	}
	return src.Paths().DataDir
}
