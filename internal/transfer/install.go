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
	MemoryCopied, MemoryDiffers           []string
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

// Install moves staged entries into place per spec §7.5 and performs the merges.
func Install(ctx context.Context, m *Manifest, st map[int]Status, stagingDir string, p session.Paths, extra InstallExtras) (*InstallReport, error) {
	rep := &InstallReport{}
	// Defense-in-depth destination re-check, before anything is touched.
	// modeInstall also CLAIMS every root this job finds absent, recording
	// it in jobs/<id>/roots.json before a single file lands under it, so
	// this job's own later re-runs (verifyInstall's re-diff, a resumed
	// job) still recognise the now-populated root as their own while a
	// root somebody else populated stays refused.
	v, err := newValidator(m, p, modeInstall)
	if err != nil {
		return rep, err
	}
	for _, e := range m.Entries {
		if err := v.check(e); err != nil {
			return rep, err
		}
	}
	for _, e := range extra.Memory {
		if err := v.check(e); err != nil {
			return rep, err
		}
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
			return rep, fmt.Errorf("refusing deferred entry: %s has category %q; only %q entries are installed by this path", e.Dst, e.Category, session.CatCapture)
		}
	}
	memory := map[int]bool{}
	for _, e := range extra.Memory {
		// Enforce the InstallExtras.Memory invariant: every entry must be a
		// row of THIS manifest (same ID space), not a foreign or
		// hand-built Entry that merely resembles one.
		me, ok := m.ByID(e.ID)
		if !ok || me.Dst != e.Dst {
			return rep, fmt.Errorf("install memory %s: id %d is not a manifest entry with this Dst (extra.Memory must be rows of the manifest passed to Install)", e.Dst, e.ID)
		}
		memory[e.ID] = true
	}
	installed := map[string]bool{}
	for _, e := range m.Entries {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if memory[e.ID] {
			continue
		}
		switch st[e.ID] {
		case StagedSame:
			if err := v.checkPlacement(e); err != nil {
				return rep, err
			}
			if err := placeEntry(stagingDir, e); err != nil {
				return rep, err
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
				return rep, fmt.Errorf("install %s: ff-candidate but nothing staged: %w", e.Dst, err)
			}
			// Authoritative re-check immediately before the only permitted
			// overwrite (controller ruling 1): reuse Task 11's ffPrefixCheck
			// so this matches Diff's classification exactly — record-wise
			// for .jsonl transcripts, byte-prefix otherwise.
			ok, err := ffPrefixCheck(e.Dst, staged)
			if err != nil {
				return rep, fmt.Errorf("install %s: prefix check: %w", e.Dst, err)
			}
			if !ok {
				return rep, fmt.Errorf("install %s: existing file is not a prefix of the incoming one (present-different)", e.Dst)
			}
			if err := v.checkPlacement(e); err != nil {
				return rep, err
			}
			if err := placeFile(stagingDir, e); err != nil {
				return rep, err
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
				return rep, err
			}
			staged := StagedPath(stagingDir, e.ID)
			sum, _, err := HashFile(staged)
			if err != nil {
				return rep, fmt.Errorf("install %s: force overwrite: %w", e.Dst, err)
			}
			if sum != e.SHA256 {
				return rep, fmt.Errorf("install %s: force overwrite: staged copy does not match the manifest hash", e.Dst)
			}
			if err := os.Remove(e.Dst); err != nil && !errors.Is(err, os.ErrNotExist) {
				return rep, fmt.Errorf("install %s: force overwrite: %w", e.Dst, err)
			}
			if err := v.checkPlacement(e); err != nil {
				return rep, err
			}
			if err := placeFile(stagingDir, e); err != nil {
				return rep, err
			}
			rep.ForceOverwritten++
			installed[e.Dst] = true
		default:
			return rep, fmt.Errorf("install %s: status %s — refusing (nothing after this entry was touched)", e.Dst, st[e.ID])
		}
	}

	for _, e := range extra.Memory {
		switch st[e.ID] {
		case StagedSame:
			if err := v.checkPlacement(e); err != nil {
				return rep, err
			}
			if err := placeEntry(stagingDir, e); err != nil {
				return rep, err
			}
			// Memory files (CLAUDE.md etc.) are the user's own documents,
			// not manifest entries abandon ever deletes: MemoryCopied is
			// reported for visibility only and deliberately never folds
			// into InstalledIDs (accepted per review, folded minor 1).
			rep.MemoryCopied = append(rep.MemoryCopied, e.Dst)
		case PresentSame:
			dropStaged(stagingDir, e)
		case Absent, StagedMismatch:
			return rep, fmt.Errorf("install memory %s: status %s (not staged)", e.Dst, st[e.ID])
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
			return rep, fmt.Errorf("merge sessions-index: %w", err)
		}
		rep.IndexMerged = 1
	}
	if len(extra.History) > 0 {
		n, err := session.AppendHistory(p.HistoryFile(), extra.History)
		if err != nil {
			return rep, fmt.Errorf("append history: %w", err)
		}
		rep.HistoryAdded = n
	}
	if extra.ProjectEntry != nil {
		added, err := session.AddProjectEntry(p.GlobalJSON, extra.ProjectCwd, extra.ProjectEntry)
		if err != nil {
			return rep, fmt.Errorf("add project entry: %w", err)
		}
		rep.ProjectEntryAdded = added
	}
	return rep, nil
}

// Uninstall removes manifest-listed installed files whose current content
// still matches the manifest (for `abandon --delete-destination-files`), then
// removes directories the install emptied. Every entry is re-checked against
// p with the same defense-in-depth validator as Install (modeUninstall: the
// Root rules are meaningless here — Uninstall only ever removes content
// some earlier Install call already placed and hash-verifies each one
// before removing it, and it also legitimately processes existing-main's
// Deferred dirty entries, whose Root was never this job's), before anything
// is deleted, so a manifest cannot be used to smuggle a deletion outside
// the session's own paths.
func Uninstall(m *Manifest, p session.Paths) ([]string, error) {
	v, err := newValidator(m, p, modeUninstall)
	if err != nil {
		return nil, err
	}
	for _, e := range m.Entries {
		if err := v.check(e); err != nil {
			return nil, err
		}
	}
	var removed []string
	var errs []error
	for _, e := range m.Entries {
		switch {
		case e.IsDir():
			continue // directories are handled in the pass below
		case e.IsSymlink():
			target, err := os.Readlink(e.Dst)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("readlink %s: %w", e.Dst, err))
				continue
			}
			if target != e.Symlink {
				continue
			}
			if err := os.Remove(e.Dst); err != nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", e.Dst, err))
				continue
			}
			removed = append(removed, e.Dst)
		default:
			sum, _, err := HashFile(e.Dst)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("hash %s: %w", e.Dst, err))
				continue
			}
			if sum != e.SHA256 {
				continue // differs from the manifest: left in place, not reported
			}
			if err := os.Remove(e.Dst); err != nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", e.Dst, err))
				continue
			}
			removed = append(removed, e.Dst)
		}
	}

	for _, dir := range impliedDirsDeepestFirst(m) {
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
// containment check (validateDst) and hash verification unchanged.
//
// Ruling M1: a directory entry is a deletion/empty-cleanup candidate ONLY
// when its own id is in ids — i.e. the job itself created that directory
// (Install's StagedSame/FFCandidate case, which is exactly what populates
// InstalledIDs). A pre-existing directory (PresentSame at install time,
// never in InstalledIDs) must never be removed even if it happens to be
// empty by the time abandon runs — unlike Uninstall's caller in the
// "this host is already the destination" path, which legitimately means
// every manifest dir.
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
	return Uninstall(sub, p)
}

func dirEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

// impliedDirsDeepestFirst returns every directory the install created,
// deepest first: each manifest dir entry's Dst (the ones Install explicitly
// MkdirAll'd), plus every intermediate directory that Install's per-file
// MkdirAll implicitly created between a manifest dir entry and a file or
// symlink nested under it (e.g. a "<sid>/subagents/" sidecar tree that has
// no dir entry of its own, only files under it). A directory that is not
// itself, or nested under, a manifest dir entry — e.g. a shared "todos/"
// that this manifest never listed as a directory — is never a candidate,
// so Uninstall can never remove or even consider a directory the install
// did not itself establish.
func impliedDirsDeepestFirst(m *Manifest) []string {
	var roots []string
	for _, e := range m.Entries {
		if e.IsDir() {
			roots = append(roots, e.Dst)
		}
	}
	rootOf := func(d string) (string, bool) {
		for _, r := range roots {
			if d == r || strings.HasPrefix(d, r+string(filepath.Separator)) {
				return r, true
			}
		}
		return "", false
	}
	seen := map[string]bool{}
	var dirs []string
	for _, e := range m.Entries {
		start := e.Dst
		if !e.IsDir() {
			start = filepath.Dir(e.Dst)
		}
		root, ok := rootOf(start)
		if !ok {
			continue
		}
		for d := start; ; d = filepath.Dir(d) {
			if !seen[d] {
				seen[d] = true
				dirs = append(dirs, d)
			}
			if d == root {
				break
			}
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		return strings.Count(dirs[i], string(filepath.Separator)) > strings.Count(dirs[j], string(filepath.Separator))
	})
	return dirs
}
