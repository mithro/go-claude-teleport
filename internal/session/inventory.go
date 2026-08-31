package session

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Category classifies transferred files (spec §7.1).
type Category string

const (
	CatSession  Category = "session"
	CatRepo     Category = "repo"
	CatWorktree Category = "worktree"
	CatCapture  Category = "capture"
	CatPack     Category = "pack"
)

// FileEntry is one file/dir/symlink to move. Rel is relative to Root so the
// destination path is Root' + Rel after rewriting Root.
type FileEntry struct {
	Root     string // absolute root this entry belongs to (e.g. ConfigDir or repo dir)
	Rel      string // slash-separated, relative to Root ("" for the root dir itself)
	Category Category
	Size     int64
	Mode     fs.FileMode
	ModTime  time.Time
	Symlink  string // link target if a symlink
	Rewrite  bool   // JSON content must go through the path map
}

// Path is filepath.Join(Root, Rel).
func (e FileEntry) Path() string { return filepath.Join(e.Root, filepath.FromSlash(e.Rel)) }

// Skipped is a path the inventory refused with the reason.
type Skipped struct{ Path, Reason string }

// Inventory lists every session file to move (spec §3 table, "yes" rows).
type Inventory struct {
	Files   []FileEntry
	Skipped []Skipped
	Memory  []FileEntry // projects/<munged>/memory/** (copied only if absent on dest)
}

// Forbidden reports whether rel (relative to ConfigDir) may never be moved
// (spec §7.1): credentials, the registry, messaging keys, the global json,
// settings, plugins.
func Forbidden(rel string) bool {
	rel = strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(rel)), "/")
	switch rel {
	case ".credentials.json", ".claude.json", "settings.json", "settings.local.json", "sessions", "plugins":
		return true
	}
	if strings.HasPrefix(rel, "sessions/") || strings.HasPrefix(rel, "plugins/") {
		return true
	}
	return strings.HasSuffix(rel, ".key")
}

// InventoryFiles walks the session's directories under ConfigDir.
func InventoryFiles(s *Session) (*Inventory, error) {
	cfg := s.Paths.ConfigDir
	inv := &Inventory{}
	id := string(s.ID)
	projRel, err := filepath.Rel(cfg, s.ProjectDir)
	if err != nil {
		return nil, fmt.Errorf("project dir %s is not under %s: %w", s.ProjectDir, cfg, err)
	}
	projRel = filepath.ToSlash(projRel)

	// Roots to walk (relative to ConfigDir). Missing ones are simply absent.
	roots := []string{
		projRel + "/" + id + ".jsonl",
		projRel + "/" + id,
		"file-history/" + id,
		"tasks/" + id,
		"session-env/" + id,
	}
	todos, _ := filepath.Glob(filepath.Join(cfg, "todos", id+"*.json"))
	for _, tpath := range todos {
		roots = append(roots, "todos/"+filepath.Base(tpath))
	}
	for _, r := range roots {
		if err := walkInto(cfg, r, &inv.Files, &inv.Skipped); err != nil {
			return nil, err
		}
	}
	if err := walkInto(cfg, projRel+"/memory", &inv.Memory, &inv.Skipped); err != nil {
		return nil, err
	}
	// Filter out directories from memory (only copy files)
	var memoryFiles []FileEntry
	for _, f := range inv.Memory {
		if !f.Mode.IsDir() {
			memoryFiles = append(memoryFiles, f)
		}
	}
	inv.Memory = memoryFiles
	sort.Slice(inv.Files, func(i, j int) bool { return inv.Files[i].Rel < inv.Files[j].Rel })
	sort.Slice(inv.Memory, func(i, j int) bool { return inv.Memory[i].Rel < inv.Memory[j].Rel })
	return inv, nil
}

// walkInto adds every entry under root/rel (a file or a directory) to out.
// Symlinks are recorded, never followed. Sockets, fifos, devices and the
// tasks .lock file go to skipped. Anything forbidden goes to skipped too —
// belt and braces; the roots above never include such paths.
func walkInto(root, rel string, out *[]FileEntry, skipped *[]Skipped) error {
	start := filepath.Join(root, filepath.FromSlash(rel))
	if _, err := os.Lstat(start); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			*skipped = append(*skipped, Skipped{Path: p, Reason: "unreadable: " + err.Error()})
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		r, _ := filepath.Rel(root, p)
		r = filepath.ToSlash(r)
		if Forbidden(r) {
			*skipped = append(*skipped, Skipped{Path: p, Reason: "forbidden"})
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			*skipped = append(*skipped, Skipped{Path: p, Reason: "stat: " + err.Error()})
			return nil
		}
		e := FileEntry{Root: root, Rel: r, Category: CatSession, Mode: info.Mode(), ModTime: info.ModTime()}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				*skipped = append(*skipped, Skipped{Path: p, Reason: "readlink: " + err.Error()})
				return nil
			}
			e.Symlink = target
		case info.Mode()&fs.ModeNamedPipe != 0:
			*skipped = append(*skipped, Skipped{Path: p, Reason: "fifo"})
			return nil
		case info.Mode()&fs.ModeSocket != 0:
			*skipped = append(*skipped, Skipped{Path: p, Reason: "socket"})
			return nil
		case info.Mode()&fs.ModeDevice != 0 || info.Mode()&fs.ModeCharDevice != 0:
			*skipped = append(*skipped, Skipped{Path: p, Reason: "device"})
			return nil
		case d.IsDir():
			// keep the directory entry (mode) so empty dirs are recreated
		case d.Name() == ".lock":
			*skipped = append(*skipped, Skipped{Path: p, Reason: "lock file"})
			return nil
		default:
			e.Size = info.Size()
			e.Rewrite = strings.HasSuffix(r, ".json") || strings.HasSuffix(r, ".jsonl")
		}
		*out = append(*out, e)
		return nil
	})
}
