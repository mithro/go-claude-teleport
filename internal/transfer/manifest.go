// Package transfer builds, diffs, streams and installs the file manifest of
// a teleport (spec §7).
package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mithro/go-claude-teleport/internal/session"
)

type Entry struct {
	ID        int              `json:"id"`
	Category  session.Category `json:"category"`
	Src       string           `json:"src"` // absolute on source
	Dst       string           `json:"dst"` // absolute on destination
	Size      int64            `json:"size"`
	Mode      uint32           `json:"mode"`
	ModTime   time.Time        `json:"mtime"`
	SHA256    string           `json:"sha256"` // "" for dirs/symlinks
	Symlink   string           `json:"symlink,omitempty"`
	Rewrite   bool             `json:"rewrite"`
	FFAllowed bool             `json:"ff_allowed"` // transcript/sidecar of THIS session
}

func (e Entry) IsDir() bool     { return os.FileMode(e.Mode)&os.ModeDir != 0 }
func (e Entry) IsSymlink() bool { return os.FileMode(e.Mode)&os.ModeSymlink != 0 }
func (e Entry) IsRegular() bool { return !e.IsDir() && !e.IsSymlink() }

type Manifest struct {
	Version    int               `json:"version"` // 1
	JobID      string            `json:"job_id"`
	SessionID  string            `json:"session_id"`
	SourceHost string            `json:"source_host"`
	DestHost   string            `json:"dest_host"`
	PathMap    session.PathMap   `json:"path_map"`
	Entries    []Entry           `json:"entries"`
	Skipped    []session.Skipped `json:"skipped"`
	TmpDir     string            `json:"-"` // where Send writes rewritten temp files ("" = os.TempDir())
}

// ErrForbidden is returned by Build when an entry is a never-transferred path.
var ErrForbidden = errors.New("manifest contains a forbidden path")

// Build hashes every file (streaming) and computes Dst via the path map.
// Rewrite entries are hashed AFTER rewriting so the hash matches what is sent.
func Build(ctx context.Context, jobID string, id session.ID, srcHost, dstHost string, files []session.FileEntry, pm session.PathMap) (*Manifest, error) {
	var bad []string
	for _, f := range files {
		if f.Category == session.CatSession && session.Forbidden(f.Rel) {
			bad = append(bad, f.Rel)
		}
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrForbidden, strings.Join(bad, ", "))
	}
	m := &Manifest{Version: 1, JobID: jobID, SessionID: string(id), SourceHost: srcHost, DestHost: dstHost, PathMap: pm}
	for i, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		e := Entry{
			ID:       i,
			Category: f.Category,
			Src:      f.Path(),
			Dst:      filepath.Join(pm.ApplyPath(f.Root), filepath.FromSlash(f.Rel)),
			Size:     f.Size,
			Mode:     uint32(f.Mode),
			ModTime:  f.ModTime,
			Symlink:  f.Symlink,
			Rewrite:  f.Rewrite,
		}
		e.FFAllowed = f.Category == session.CatSession && ffAllowed(f.Rel, id)
		if e.IsRegular() {
			sum, n, err := hashEntry(e.Src, f.Rewrite, pm)
			if err != nil {
				return nil, fmt.Errorf("manifest: hash %s: %w", e.Src, err)
			}
			e.SHA256, e.Size = sum, n
			// Entries built without a stat (Plan 03's capture entry carries
			// only Root/Rel/Category/Mode) take Mode/ModTime from the file here.
			if f.Mode == 0 || f.ModTime.IsZero() {
				st, err := os.Stat(e.Src)
				if err != nil {
					return nil, fmt.Errorf("manifest: stat %s: %w", e.Src, err)
				}
				e.Mode, e.ModTime = uint32(st.Mode()), st.ModTime()
			}
		}
		m.Entries = append(m.Entries, e)
	}
	return m, nil
}

// ffAllowed reports whether rel belongs to session id: rel contains a
// "/<id>." or "/<id>/" segment (covering the transcript "<id>.jsonl", the
// "<id>/" sidecar directory used by projects/<munged>/<id>/**,
// file-history/<id>/**, tasks/<id>/**, session-env/<id>/**, and
// "todos/<id>*.json"), or rel IS the sidecar directory itself ("<id>" with
// nothing after it). This is keyed off the session id, not a projects/-prefix
// heuristic, so it does not false-positive on another session's file that
// merely lives alongside it in the same project directory.
func ffAllowed(rel string, id session.ID) bool {
	sid := string(id)
	if sid == "" {
		return false
	}
	p := "/" + filepath.ToSlash(rel)
	marker := "/" + sid
	for idx := 0; ; {
		i := strings.Index(p[idx:], marker)
		if i < 0 {
			return false
		}
		pos := idx + i
		end := pos + len(marker)
		if end == len(p) {
			return true // rel IS the sidecar dir named by the sid
		}
		switch p[end] {
		case '.', '/':
			return true
		}
		idx = pos + 1
	}
}

// hashEntry streams the file (rewritten if asked) through sha256.
func hashEntry(path string, rewrite bool, pm session.PathMap) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	cw := &countWriter{w: h}
	if err := copyMaybeRewritten(f, cw, path, rewrite, pm); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), cw.n, nil
}

// copyMaybeRewritten is the ONE place that decides how bytes leave the
// source: raw copy, or session.RewriteJSONL / RewriteJSON when rewrite is set.
func copyMaybeRewritten(r io.Reader, w io.Writer, path string, rewrite bool, pm session.PathMap) error {
	if !rewrite || pm.Empty() {
		_, err := io.Copy(w, r)
		return err
	}
	var err error
	if strings.HasSuffix(path, ".jsonl") {
		_, err = session.RewriteJSONL(r, w, pm)
	} else {
		_, err = session.RewriteJSON(r, w, pm)
	}
	return err
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// Load reads a manifest file.
func Load(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load manifest %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	if m.Version != 1 {
		return nil, fmt.Errorf("manifest %s: unsupported version %d", path, m.Version)
	}
	return &m, nil
}

// Save writes the manifest atomically (temp + rename, 0600).
func (m *Manifest) Save(path string) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("save manifest %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("save manifest %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("save manifest %s: %w", path, err)
	}
	return nil
}

// ByID finds an entry by id (ids are dense and ordered, so index first).
func (m *Manifest) ByID(id int) (Entry, bool) {
	if id >= 0 && id < len(m.Entries) && m.Entries[id].ID == id {
		return m.Entries[id], true
	}
	for _, e := range m.Entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}
