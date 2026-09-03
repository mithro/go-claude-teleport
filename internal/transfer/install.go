package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/mithro/go-claude-teleport/internal/session"
)

type InstallReport struct {
	Installed, SkippedSame, FastForwarded int
	IndexMerged, HistoryAdded             int
	ProjectEntryAdded                     bool
	// TrustGranted says this call marked InstallExtras.TrustCwd trusted in
	// the destination's global config (ruling R-P3-TRUST-1 item 1). False
	// when the source was not trusted, or when the destination already
	// said so — the grant is idempotent.
	TrustGranted                bool
	MemoryCopied, MemoryDiffers []string
	// InstalledIDs is the manifest ids this call placed at Dst from
	// scratch (the StagedSame case below) — never a PresentSame entry,
	// which was already there before this call touched anything.
	// abandon --delete-destination-files (ruling R-P3-23a) is the reader.
	InstalledIDs []int `json:"InstalledIDs"` // ruling R-P3-23m: exact Go name, no wire change
	// FastForwardedIDs is populated for every FFCandidate entry this call
	// extended — NOT necessarily an id THIS job created (ruling
	// R-P3-23h): an ff-candidate is, by definition, an entry that
	// ALREADY existed on the destination (a prefix of the incoming file
	// — from an earlier, unrelated teleport of this session, or from
	// Claude itself running there) before install ever touched anything.
	// Once install has extended it, its hash matches the manifest, so
	// Uninstall's hash check no longer distinguishes "this job's own
	// content" from "someone else's file that happens to now match".
	// The caller (orchestrate's runInstall) must fold an id from here
	// into Plan.InstalledIDs ONLY when the id was already there before
	// this call — i.e. this job's own earlier (partial) placement being
	// re-extended on a retry, never a destination file this job is
	// meeting for the first time.
	FastForwardedIDs []int
	// ForceOverwritten counts entries replaced wholesale under --force
	// (spec §7.3's non-prefix case, same session id only). Like a
	// fast-forward, and for the same reason, these ids are NOT recorded:
	// the destination file existed before this job touched it, so
	// abandon --delete-destination-files must never remove it.
	ForceOverwritten int
}

type InstallExtras struct {
	IndexEntry   *session.IndexEntry
	History      []json.RawMessage
	ProjectCwd   string
	ProjectEntry session.ProjectEntry
	// TrustCwd is the destination path whose ~/.claude.json project entry
	// grants Claude Code's first-run trust dialog for this session
	// (R-P3-TRUST-1 item 1). It is ProjectCwd for an ordinary session, and
	// the mapped MAIN repository path when the session's cwd is a linked
	// git worktree — real Claude Code 2.1.259 keys the entry there, not at
	// the worktree, so granting it at ProjectCwd alone would leave the
	// destination sitting at the dialog with no registry entry, which is
	// exactly how the first real teleport failed.
	TrustCwd string `json:"trust_cwd,omitempty"`
	// SourceTrusted is the source's own answer to that dialog: the
	// destination only grants trust it can point at on the source.
	SourceTrusted bool `json:"source_trusted,omitempty"`
	// Memory holds the memory-file entries: copy only if absent. INVARIANT:
	// every entry MUST be a row of the SAME manifest passed to Install (same
	// ID space) — i.e. m.ByID(e.ID) must find an entry with an identical
	// Dst. Install validates this and errors otherwise; it never accepts a
	// Memory entry whose ID belongs to a different manifest or a different
	// Dst under a reused ID.
	Memory []Entry
	// Force is the driver's --force, relayed to the destination (spec
	// §7.3): it permits replacing a destination file that belongs to THIS
	// session but is not a prefix of the incoming one. It is the user's
	// consent, so the destination refuses the overwrite without it even
	// though preflight's Blocking() already applied the same gate.
	Force bool `json:"force,omitempty"`
}

func parentMode(e Entry) os.FileMode {
	if e.Category == session.CatSession || e.Category == session.CatCapture {
		return 0o700
	}
	return 0o755
}

// moveFile renames src to dst, falling back to copy+rename across filesystems.
func moveFile(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	var lerr *os.LinkError
	if !errors.As(err, &lerr) || !errors.Is(lerr.Err, syscall.EXDEV) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".claude-teleport.tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Remove(src)
}

// place performs one placement under the destination's own final guards:
// the entry is re-validated (R-P3-B1f N7), the directories the placement is
// about to create are noted, the placement happens, and what it wrote is
// recorded for jobs/<id>/installed.json (N3). fileOnly is the
// fast-forward/force path, which only ever replaces a regular file.
func (v *validator) place(stagingDir string, e Entry, fileOnly bool) error {
	if err := v.recheckBeforePlacement(e); err != nil {
		return err
	}
	parent := filepath.Dir(filepath.Clean(e.Dst))
	if e.IsDir() && !fileOnly {
		parent = filepath.Clean(e.Dst) // MkdirAll creates the entry itself too
	}
	created := absentAncestors(parent)
	place := placeEntry
	if fileOnly {
		place = placeFile
	}
	if err := place(stagingDir, e); err != nil {
		return err
	}
	v.recordPlaced(e, created)
	return nil
}

func dropStaged(stagingDir string, e Entry) {
	base := StagedPath(stagingDir, e.ID)
	os.Remove(base)
	os.Remove(base + ".dir")
	os.Remove(base + ".symlink")
}

func placeFile(stagingDir string, e Entry) error {
	if err := os.MkdirAll(filepath.Dir(e.Dst), parentMode(e)); err != nil {
		return fmt.Errorf("create parent of %s: %w", e.Dst, err)
	}
	if err := moveFile(StagedPath(stagingDir, e.ID), e.Dst); err != nil {
		return fmt.Errorf("install %s: %w", e.Dst, err)
	}
	if err := os.Chmod(e.Dst, os.FileMode(e.Mode).Perm()); err != nil {
		return fmt.Errorf("chmod %s: %w", e.Dst, err)
	}
	if !e.ModTime.IsZero() {
		if err := os.Chtimes(e.Dst, e.ModTime, e.ModTime); err != nil {
			return fmt.Errorf("chtimes %s: %w", e.Dst, err)
		}
	}
	return nil
}

func placeEntry(stagingDir string, e Entry) error {
	switch {
	case e.IsDir():
		if err := os.MkdirAll(e.Dst, os.FileMode(e.Mode).Perm()|0o700); err != nil {
			return fmt.Errorf("create dir %s: %w", e.Dst, err)
		}
		os.Remove(StagedPath(stagingDir, e.ID) + ".dir")
		return nil
	case e.IsSymlink():
		if err := os.MkdirAll(filepath.Dir(e.Dst), parentMode(e)); err != nil {
			return fmt.Errorf("create parent of %s: %w", e.Dst, err)
		}
		if err := os.Symlink(e.Symlink, e.Dst); err != nil {
			return fmt.Errorf("symlink %s: %w", e.Dst, err)
		}
		os.Remove(StagedPath(stagingDir, e.ID) + ".symlink")
		return nil
	}
	return placeFile(stagingDir, e)
}

// underDir reports whether the already-Clean cleanPath is dir itself, or
// lexically nested under it. dir must be a non-empty absolute path; a
// relative or empty dir fails closed (never "under").
func underDir(cleanPath, dir string) bool {
	if dir == "" || !filepath.IsAbs(dir) {
		return false
	}
	dir = filepath.Clean(dir)
	return cleanPath == dir || strings.HasPrefix(cleanPath, dir+string(filepath.Separator))
}

// checkForceOverwrite is the destination's gate on spec §7.3's forced
// (non-fast-forward) replacement. FFAllowed travels from the source, so it
// is corroborated — never simply believed — by re-deriving from Dst whether
// the file belongs to the session this manifest names; a regular file is
// the only shape this path can replace.
func checkForceOverwrite(m *Manifest, e Entry, force bool) error {
	if !force {
		return fmt.Errorf("install %s: status %s — refusing (pass --force to replace a diverged copy of this session)", e.Dst, PresentDifferent)
	}
	if !e.IsRegular() {
		return fmt.Errorf("install %s: refusing to force-overwrite a directory or symlink", e.Dst)
	}
	if !e.FFAllowed || e.Category != session.CatSession || !ffAllowed(e.Dst, session.ID(m.SessionID)) {
		return fmt.Errorf("install %s: refusing to force-overwrite a file that does not belong to session %s", e.Dst, m.SessionID)
	}
	return nil
}

// Install moves staged entries into place per spec §7.5 and performs the
// merges. Whatever it actually places is recorded in
// jobs/<id>/installed.json before it returns — on the failure paths too —
// because that record, not the manifest, is what a later Uninstall/
// DeleteInstalled is allowed to remove (ruling R-P3-B1f N3).
func Install(ctx context.Context, m *Manifest, st map[int]Status, stagingDir string, p session.Paths, extra InstallExtras) (*InstallReport, error) {
	rep := &InstallReport{}
	v, err := newValidator(m, p, modeInstall)
	if err != nil {
		return rep, err
	}
	err = install(ctx, m, st, stagingDir, extra, rep, v)
	if ferr := v.flushInstalled(); ferr != nil && err == nil {
		err = ferr
	}
	return rep, err
}

func install(ctx context.Context, m *Manifest, st map[int]Status, stagingDir string, extra InstallExtras, rep *InstallReport, v *validator) error {
	p := v.p
	// Defense-in-depth destination re-check, before anything is touched.
	for _, e := range m.Entries {
		if err := v.check(e); err != nil {
			return err
		}
	}
	for _, e := range extra.Memory {
		if err := v.check(e); err != nil {
			return err
		}
	}
	// Only now — the whole manifest validated, still before any placement
	// — are the roots this job found absent claimed in jobs/<id>/roots.json
	// (ruling R-P3-B1f N5), so a refused manifest leaves no claim behind
	// while this job's own later re-runs still recognise what it created.
	if err := v.commitClaims(); err != nil {
		return err
	}
	// Second half of the same destination-side re-check (B1): the plain
	// file-install path handles exactly one Deferred category, the pane
	// capture. A Deferred CatRepo/CatWorktree entry belongs to git-attach
	// and is filtered out of the manifest Install is given; any OTHER
	// Deferred category is a source trying to place a file at a Dst the
	// destination was never allowed to compare against, so refuse before
	// touching anything.
	for _, e := range append(append([]Entry(nil), m.Entries...), extra.Memory...) {
		if e.Deferred && e.Category != session.CatCapture {
			return fmt.Errorf("refusing deferred entry: %s has category %q; only %q entries are installed by this path", e.Dst, e.Category, session.CatCapture)
		}
	}
	memory := map[int]bool{}
	for _, e := range extra.Memory {
		// Enforce the InstallExtras.Memory invariant: every entry must be a
		// row of THIS manifest (same ID space), not a foreign or
		// hand-built Entry that merely resembles one.
		me, ok := m.ByID(e.ID)
		if !ok || me.Dst != e.Dst {
			return fmt.Errorf("install memory %s: id %d is not a manifest entry with this Dst (extra.Memory must be rows of the manifest passed to Install)", e.Dst, e.ID)
		}
		memory[e.ID] = true
	}
	installed := map[string]bool{}
	for _, e := range m.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if memory[e.ID] {
			continue
		}
		switch st[e.ID] {
		case StagedSame:
			if err := v.place(stagingDir, e, false); err != nil {
				return err
			}
			rep.Installed++
			installed[e.Dst] = true
			rep.InstalledIDs = append(rep.InstalledIDs, e.ID)
		case PresentSame:
			dropStaged(stagingDir, e)
			rep.SkippedSame++
		case FFCandidate:
			staged := StagedPath(stagingDir, e.ID)
			if _, err := os.Stat(staged); err != nil {
				return fmt.Errorf("install %s: ff-candidate but nothing staged: %w", e.Dst, err)
			}
			// Authoritative re-check immediately before the only permitted
			// overwrite (controller ruling 1): reuse Task 11's ffPrefixCheck
			// so this matches Diff's classification exactly — record-wise
			// for .jsonl transcripts, byte-prefix otherwise.
			ok, err := ffPrefixCheck(e.Dst, staged)
			if err != nil {
				return fmt.Errorf("install %s: prefix check: %w", e.Dst, err)
			}
			if !ok {
				return fmt.Errorf("install %s: existing file is not a prefix of the incoming one (present-different)", e.Dst)
			}
			if err := v.place(stagingDir, e, true); err != nil {
				return err
			}
			rep.FastForwarded++
			installed[e.Dst] = true
			rep.FastForwardedIDs = append(rep.FastForwardedIDs, e.ID)
		case PresentDifferent:
			// Spec §7.3: --force extends the fast-forward exception to the
			// non-prefix case "for the same session id only; it never
			// allows overwriting unrelated files". Both halves are checked
			// here, on the destination: the caller's explicit consent, and
			// session ownership re-derived from Dst itself rather than
			// trusted from the source-computed FFAllowed flag.
			if err := checkForceOverwrite(m, e, extra.Force); err != nil {
				return err
			}
			staged := StagedPath(stagingDir, e.ID)
			sum, _, err := HashFile(staged)
			if err != nil {
				return fmt.Errorf("install %s: force overwrite: %w", e.Dst, err)
			}
			if sum != e.SHA256 {
				return fmt.Errorf("install %s: force overwrite: staged copy does not match the manifest hash", e.Dst)
			}
			if err := os.Remove(e.Dst); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("install %s: force overwrite: %w", e.Dst, err)
			}
			if err := v.place(stagingDir, e, true); err != nil {
				return err
			}
			rep.ForceOverwritten++
			installed[e.Dst] = true
		default:
			return fmt.Errorf("install %s: status %s — refusing (nothing after this entry was touched)", e.Dst, st[e.ID])
		}
	}

	for _, e := range extra.Memory {
		switch st[e.ID] {
		case StagedSame:
			if err := v.place(stagingDir, e, false); err != nil {
				return err
			}
			// Memory files (CLAUDE.md etc.) are the user's own documents,
			// not manifest entries abandon ever deletes: MemoryCopied is
			// reported for visibility only and deliberately never folds
			// into InstalledIDs (accepted per review, folded minor 1).
			rep.MemoryCopied = append(rep.MemoryCopied, e.Dst)
		case PresentSame:
			dropStaged(stagingDir, e)
		case Absent, StagedMismatch:
			return fmt.Errorf("install memory %s: status %s (not staged)", e.Dst, st[e.ID])
		default:
			rep.MemoryDiffers = append(rep.MemoryDiffers, e.Dst)
			dropStaged(stagingDir, e)
		}
	}

	if extra.IndexEntry != nil {
		ie := *extra.IndexEntry
		if installed[ie.FullPath] {
			if fi, err := os.Stat(ie.FullPath); err == nil {
				ie.FileMtime = fi.ModTime().UnixMilli()
			}
		}
		if err := session.MergeIndexEntry(p.ProjectDir(extra.ProjectCwd), ie); err != nil {
			return fmt.Errorf("merge sessions-index: %w", err)
		}
		rep.IndexMerged = 1
	}
	if len(extra.History) > 0 {
		n, err := session.AppendHistory(p.HistoryFile(), extra.History)
		if err != nil {
			return fmt.Errorf("append history: %w", err)
		}
		rep.HistoryAdded = n
	}
	if extra.ProjectEntry != nil {
		added, err := session.AddProjectEntry(p.GlobalJSON, extra.ProjectCwd, extra.ProjectEntry)
		if err != nil {
			return fmt.Errorf("add project entry: %w", err)
		}
		rep.ProjectEntryAdded = added
	}
	return grantTrust(p, extra, rep)
}

// grantTrust carries the source's answer to Claude Code's first-run trust
// dialog to the destination (ruling R-P3-TRUST-1 item 1), so the resumed
// Claude reaches its prompt instead of the "Quick safety check" dialog —
// which produces no registry entry, and so failed the whole start step.
//
// TrustCwd is source-supplied, so it is bounded here: only an absolute,
// already-clean path that is not the destination's own home, config dir,
// data dir or the filesystem root may be granted. Granting trust for one
// of those would hand a hostile source a blanket "trusted" for everything
// under it.
func grantTrust(p session.Paths, extra InstallExtras, rep *InstallReport) error {
	if !extra.SourceTrusted {
		return nil
	}
	cwd := extra.TrustCwd
	if cwd == "" || !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cwd {
		return Refuse(cwd, "trust cwd must be an absolute, clean path")
	}
	for _, forbidden := range []string{p.Home, p.ConfigDir, p.DataDir, string(filepath.Separator)} {
		if forbidden != "" && cwd == filepath.Clean(forbidden) {
			return Refuse(cwd, "refusing to grant the trust dialog for %s", forbidden)
		}
	}
	granted, err := session.GrantProjectTrust(p.GlobalJSON, cwd)
	if err != nil {
		return fmt.Errorf("grant trust for %s: %w", cwd, err)
	}
	rep.TrustGranted = granted
	return nil
}

// Uninstall removes what THIS job's own Install placed on this host (for
// `abandon --delete-destination-files`), then removes the directories it
// created once they are empty.
//
// Ruling R-P3-B1f N3: the manifest is a source-supplied wish list, so it
// can only ever NARROW what is removed — the licence comes from
// jobs/<id>/installed.json, the record Install wrote as it placed each
// thing. Content must still match what was placed (hash for a file, target
// for a symlink), so anything edited since is left alone; a path this job
// never placed is never touched, whatever the manifest claims about it.
// Every entry additionally passes the same validator Install ran
// (modeUninstall: the Root rules apply here too — the job's own roots are
// recorded in roots.json by then — while nothing is ever claimed).
func Uninstall(m *Manifest, p session.Paths) ([]string, error) {
	return uninstall(m, p, nil)
}

// uninstall is Uninstall with the caller's own exclusions: protected holds
// destination paths the caller deliberately left out of m (UninstallIDs'
// unnamed ids), which must not be swept up as "a directory this job
// created that is now empty" either.
func uninstall(m *Manifest, p session.Paths, protected map[string]bool) ([]string, error) {
	v, err := newValidator(m, p, modeUninstall)
	if err != nil {
		return nil, err
	}
	for _, e := range m.Entries {
		if err := v.check(e); err != nil {
			return nil, err
		}
	}
	installed, err := loadInstalled(p.DataDir, m.JobID)
	if err != nil {
		return nil, err
	}
	var removed []string
	var errs []error
	for _, e := range m.Entries {
		dst := filepath.Clean(e.Dst)
		rec, ours := installed[dst]
		if !ours {
			continue // this job never placed anything here
		}
		switch {
		case e.IsDir():
			continue // directories are handled in the pass below
		case e.IsSymlink():
			if rec.Kind != kindSymlink {
				continue
			}
			target, err := os.Readlink(dst)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("readlink %s: %w", e.Dst, err))
				continue
			}
			if target != rec.Symlink {
				continue
			}
			if err := os.Remove(dst); err != nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", e.Dst, err))
				continue
			}
			removed = append(removed, e.Dst)
		default:
			if rec.Kind != kindFile || rec.SHA256 == "" {
				continue
			}
			sum, _, err := HashFile(dst)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("hash %s: %w", e.Dst, err))
				continue
			}
			if sum != rec.SHA256 {
				continue // changed since this job installed it: left in place
			}
			if err := os.Remove(dst); err != nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", e.Dst, err))
				continue
			}
			removed = append(removed, e.Dst)
		}
	}

	for _, dir := range createdDirsDeepestFirst(m, installed, protected) {
		empty, err := dirEmpty(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("read dir %s: %w", dir, err))
			continue
		}
		if !empty {
			continue // never remove a non-empty dir
		}
		if err := os.Remove(dir); err != nil {
			errs = append(errs, fmt.Errorf("remove dir %s: %w", dir, err))
			continue
		}
		removed = append(removed, dir)
	}
	return removed, errors.Join(errs...)
}

// UninstallIDs is Uninstall restricted to the manifest entries named by
// ids (abandon --delete-destination-files, Plan 03: ids is
// orchestrate.Plan.InstalledIDs — only files this job itself installed —
// so a file that merely already existed on the destination with matching
// content for unrelated reasons is never removed). It reuses Uninstall's
// containment check and its installed-record verification unchanged.
//
// Ruling M1 (a pre-existing directory must never be removed even if it is
// empty by the time abandon runs) no longer depends on which ids the
// caller passes: Uninstall removes only directories jobs/<id>/installed.
// json records THIS job as having created (R-P3-B1f N3). ids still narrows
// the set, as the caller intends.
func UninstallIDs(m *Manifest, p session.Paths, ids []int) ([]string, error) {
	want := make(map[int]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	sub := &Manifest{Version: m.Version, JobID: m.JobID, SessionID: m.SessionID, SourceHost: m.SourceHost, DestHost: m.DestHost, PathMap: m.PathMap, Roots: m.Roots}
	for _, e := range m.Entries {
		if want[e.ID] {
			sub.Entries = append(sub.Entries, e)
		}
	}
	protected := map[string]bool{}
	for _, e := range m.Entries {
		if !want[e.ID] {
			protected[filepath.Clean(e.Dst)] = true
		}
	}
	return uninstall(sub, p, protected)
}

func dirEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

// createdDirsDeepestFirst returns the directories THIS job created (as
// recorded in jobs/<id>/installed.json — manifest directory entries it
// placed, plus the intermediate directories its per-file MkdirAll had to
// create) that this manifest is actually about, deepest first.
//
// Provenance is the licence (ruling R-P3-B1f N3) and the manifest is the
// filter: only a recorded directory that is itself, or an ancestor of, one
// of this (sub)manifest's own entry paths is a candidate. A directory that
// already existed when Install ran is not in the record at all and can
// never be removed — which is ruling M1's promise, now enforced by what
// happened rather than by which ids a caller passed.
func createdDirsDeepestFirst(m *Manifest, installed map[string]installedRecord, protected map[string]bool) []string {
	var dirs []string
	for dir, rec := range installed {
		if rec.Kind != kindDir || protected[dir] {
			continue
		}
		for _, e := range m.Entries {
			dst := filepath.Clean(e.Dst)
			if dst == dir || strings.HasPrefix(dst, dir+string(filepath.Separator)) {
				dirs = append(dirs, dir)
				break
			}
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		di, dj := strings.Count(dirs[i], string(filepath.Separator)), strings.Count(dirs[j], string(filepath.Separator))
		if di != dj {
			return di > dj
		}
		return dirs[i] < dirs[j]
	})
	return dirs
}
