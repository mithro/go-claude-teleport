package remote

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

func (l *Local) InventoryGit(ctx context.Context, cwd string) (*gitx.Info, error) {
	info, err := gitx.Inspect(cwd)
	if errors.Is(err, gitx.ErrNotRepo) {
		return nil, &Error{Code: "not-found", Message: err.Error()}
	}
	if err != nil {
		return nil, &Error{Code: "internal", Message: fmt.Sprintf("inventory-git %s: %v", cwd, err)}
	}
	return info, nil
}

func (l *Local) GitDestState(ctx context.Context, mainDir, worktreeDir, branch string) (*gitx.DestState, error) {
	ds, err := gitx.DestStateOf(mainDir, worktreeDir, branch)
	if err != nil {
		return nil, &Error{Code: "internal", Message: fmt.Sprintf("git-dest-state %s: %v", mainDir, err)}
	}
	return ds, nil
}

func (l *Local) GitFiles(ctx context.Context, p *gitx.Plan, excludes []string, includeIgnored bool) ([]session.FileEntry, error) {
	files, err := gitx.Files(p, excludes, includeIgnored)
	if err != nil {
		return nil, &Error{Code: "internal", Message: fmt.Sprintf("git-files: %v", err)}
	}
	return files, nil
}

// GitSourceFacts answers the two source-side questions PlanTransfer and
// the pack need: is the destination's branch tip an ancestor of ours, and
// which staged blobs are not in the tip's tree.
func (l *Local) GitSourceFacts(ctx context.Context, mainDir, indexRel, tip, destTip string) (*gitx.SourceFacts, error) {
	f, err := gitx.SourceFactsOf(mainDir, indexRel, tip, destTip)
	if err != nil {
		return nil, &Error{Code: "internal", Message: fmt.Sprintf("git-source-facts: %v", err)}
	}
	return f, nil
}

// entryModes reads jobs/<jobID>/manifest.json (already saved there by
// ManifestDiff) and returns each entry's Mode by id. A missing manifest is
// not an error here — GitAttach can be called without one ever having been
// saved (e.g. directly, as some tests do) — entryModes then simply returns
// nil and every lookup misses, leaving DirtyFile.Mode zero (gitx.Attach's
// documented "keep the staged file's own mode" fallback).
func (l *Local) entryModes(jobID string) (map[int]fs.FileMode, error) {
	m, err := transfer.Load(filepath.Join(l.jobDir(jobID), "manifest.json"))
	if err != nil {
		// R-P3-20d: a manifest that was never saved (GitAttach called
		// directly, without any ManifestDiff/BuildManifest ever having
		// run — some tests do this) is not an error: entryModes returns
		// nil and every lookup misses, leaving DirtyFile.Mode zero
		// (gitx.Attach's documented "keep the staged file's own mode"
		// fallback). Any OTHER load failure (corrupt/truncated JSON, an
		// unsupported version, a permissions error) must NOT be treated
		// the same way and silently fall back to mode 0 — that would let
		// a corrupt manifest install a dirty git file with whatever mode
		// its staged copy happens to carry, unnoticed.
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	modes := make(map[int]fs.FileMode, len(m.Entries))
	for _, e := range m.Entries {
		modes[e.ID] = fs.FileMode(e.Mode)
	}
	return modes, nil
}

// GitAttach resolves the pack and dirty files from this host's staging
// directory (jobs are keyed by session id, staging by job id) and calls
// gitx.Attach. The pack arrives via the pack stream (Task 16) as
// staging/<job>/objects.pack; dirty files are staged manifest entries and
// land with the mode the manifest recorded for that entry id, or the
// staged copy's own mode when the manifest has no entry for it.
func (l *Local) GitAttach(ctx context.Context, p *gitx.Plan, jobID string) error {
	if err := checkJobID(jobID); err != nil {
		return err
	}
	staging := job.StagingDir(l.paths.DataDir, jobID)
	packPath := ""
	if p.NeedPack {
		packPath = filepath.Join(staging, "objects.pack")
		if _, err := os.Stat(packPath); err != nil {
			return &Error{Code: "not-found", Message: fmt.Sprintf("git-attach: pack %s: %v", packPath, err)}
		}
	}
	modes, err := l.entryModes(jobID)
	if err != nil {
		return &Error{Code: "internal", Message: fmt.Sprintf("git-attach: mode lookup: %v", err)}
	}
	dirty := map[string]gitx.DirtyFile{}
	for dst, id := range p.DirtyEntries {
		dirty[dst] = gitx.DirtyFile{Src: filepath.Join(staging, strconv.Itoa(id)), Mode: modes[id]}
	}
	if p.IndexEntryID >= 0 { // gitx.NoEntry (-1) means "no index entry"; 0 is a real id
		dirty[filepath.Join(p.DstMain, p.IndexRel)] = gitx.DirtyFile{Src: filepath.Join(staging, strconv.Itoa(p.IndexEntryID)), Mode: modes[p.IndexEntryID]}
	}
	for dst, df := range dirty {
		if _, err := os.Stat(df.Src); err != nil {
			return &Error{Code: "not-found", Message: fmt.Sprintf("git-attach: staged file for %s: %v", dst, err)}
		}
	}
	if err := gitx.Attach(ctx, p, packPath, dirty); err != nil {
		var re *gitx.RefuseError
		if errors.As(err, &re) {
			return &Error{Code: "conflict", Message: re.Reason}
		}
		return &Error{Code: "internal", Message: fmt.Sprintf("git-attach: %v", err)}
	}
	return nil
}
