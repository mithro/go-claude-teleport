// internal/transfer/installed.go
package transfer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// installedRecord is one thing THIS job's Install actually placed on this
// destination: the path, what it placed there, and the category it did it
// under. Ruling R-P3-B1f N3 makes this the only licence to delete
// anything: a manifest is a source-supplied wish list, and hash equality
// alone made every empty file (and every empty directory the manifest
// merely named) under $HOME removable by `abandon
// --delete-destination-files`.
type installedRecord struct {
	Dst      string           `json:"dst"`
	SHA256   string           `json:"sha256,omitempty"` // regular files only
	Category session.Category `json:"category"`
	Kind     string           `json:"kind"`              // "file", "dir" or "symlink"
	Symlink  string           `json:"symlink,omitempty"` // symlinks only
}

const (
	kindFile    = "file"
	kindDir     = "dir"
	kindSymlink = "symlink"
)

// installedFile is jobs/<id>/installed.json on the DESTINATION.
type installedFile struct {
	Version int               `json:"version"`
	Entries []installedRecord `json:"entries"`
}

func installedPath(dataDir, jobID string) (string, error) {
	if err := job.ValidateID(jobID); err != nil {
		return "", fmt.Errorf("installed record: %w", err)
	}
	return filepath.Join(job.Dir(dataDir, jobID), "installed.json"), nil
}

// loadInstalled reads the record, keyed by cleaned Dst. A missing file
// means this job installed nothing here — so nothing here may be deleted.
func loadInstalled(dataDir, jobID string) (map[string]installedRecord, error) {
	path, err := installedPath(dataDir, jobID)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]installedRecord{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f installedFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := make(map[string]installedRecord, len(f.Entries))
	for _, r := range f.Entries {
		out[filepath.Clean(r.Dst)] = r
	}
	return out, nil
}

// saveInstalled merges recs into jobs/<id>/installed.json (temp + rename,
// 0600). Merging, not replacing: Install is idempotent and re-runs after a
// crash, and each run may place a different subset.
func saveInstalled(dataDir, jobID string, recs []installedRecord) error {
	if len(recs) == 0 {
		return nil
	}
	path, err := installedPath(dataDir, jobID)
	if err != nil {
		return err
	}
	all, err := loadInstalled(dataDir, jobID)
	if err != nil {
		return err
	}
	for _, r := range recs {
		r.Dst = filepath.Clean(r.Dst)
		all[r.Dst] = r
	}
	f := installedFile{Version: 1}
	for _, r := range all {
		f.Entries = append(f.Entries, r)
	}
	sort.Slice(f.Entries, func(i, j int) bool { return f.Entries[i].Dst < f.Entries[j].Dst })
	blob, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("record installed: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return fmt.Errorf("record installed: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("record installed: %w", err)
	}
	return nil
}

// recordOf describes what placing e writes.
func recordOf(e Entry) installedRecord {
	r := installedRecord{Dst: filepath.Clean(e.Dst), Category: e.Category, Kind: kindFile, SHA256: e.SHA256}
	switch {
	case e.IsDir():
		r.Kind, r.SHA256 = kindDir, ""
	case e.IsSymlink():
		r.Kind, r.SHA256, r.Symlink = kindSymlink, "", e.Symlink
	}
	return r
}

// absentAncestors lists the directories from dir upward that do not exist
// yet — exactly the ones the MkdirAll of the next placement is about to
// create, deepest first. They carry no manifest entry of their own (a
// sidecar tree like projects/<munged>/<sid>/subagents/ is implied by the
// files under it), but this job DID create them, so they are recorded and
// may later be removed when they end up empty.
func absentAncestors(dir string) []string {
	var out []string
	for d := filepath.Clean(dir); ; {
		if _, err := os.Lstat(d); err == nil {
			break
		}
		out = append(out, d)
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return out
}

// recordPlaced notes what a successful placement of e wrote, plus every
// directory that placement had to create.
func (v *validator) recordPlaced(e Entry, createdDirs []string) {
	for _, d := range createdDirs {
		v.placed = append(v.placed, installedRecord{Dst: d, Category: e.Category, Kind: kindDir})
	}
	v.placed = append(v.placed, recordOf(e))
}

// flushInstalled writes the accumulated record. Install calls it on EVERY
// exit path, success or failure: a partially-succeeded install's
// placements must still be deletable by abandon (ruling R-P3-23j's
// reasoning, applied to the deletion record).
func (v *validator) flushInstalled() error {
	if len(v.placed) == 0 {
		return nil
	}
	if err := saveInstalled(v.p.DataDir, v.jobID, v.placed); err != nil {
		return err
	}
	v.placed = nil
	return nil
}
