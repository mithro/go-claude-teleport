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
}

type InstallExtras struct {
	IndexEntry   *session.IndexEntry
	History      []json.RawMessage
	ProjectCwd   string
	ProjectEntry session.ProjectEntry
	Memory       []Entry // memory files: copy only if absent
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

// Install moves staged entries into place per spec §7.5 and performs the merges.
func Install(ctx context.Context, m *Manifest, st map[int]Status, stagingDir string, p session.Paths, extra InstallExtras) (*InstallReport, error) {
	rep := &InstallReport{}
	memory := map[int]bool{}
	for _, e := range extra.Memory {
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
			if err := placeEntry(stagingDir, e); err != nil {
				return rep, err
			}
			rep.Installed++
			installed[e.Dst] = true
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
			if err := placeFile(stagingDir, e); err != nil {
				return rep, err
			}
			rep.FastForwarded++
			installed[e.Dst] = true
		default:
			return rep, fmt.Errorf("install %s: status %s — refusing (nothing after this entry was touched)", e.Dst, st[e.ID])
		}
	}

	for _, e := range extra.Memory {
		switch st[e.ID] {
		case StagedSame:
			if err := placeEntry(stagingDir, e); err != nil {
				return rep, err
			}
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
// removes directories the install emptied. p is accepted so a future caller
// can restrict removal to paths under p.ConfigDir/p.DataDir; Plan 02 does not
// need that check since every Dst already comes from this manifest.
func Uninstall(m *Manifest, p session.Paths) ([]string, error) {
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
