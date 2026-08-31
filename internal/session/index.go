package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// IndexEntry is one entry of projects/<munged>/sessions-index.json.
type IndexEntry struct {
	SessionID    string `json:"sessionId"`
	FullPath     string `json:"fullPath"`
	FileMtime    int64  `json:"fileMtime"`
	FirstPrompt  string `json:"firstPrompt"`
	Summary      string `json:"summary"`
	MessageCount int    `json:"messageCount"`
	Created      string `json:"created"`
	Modified     string `json:"modified"`
	GitBranch    string `json:"gitBranch"`
	ProjectPath  string `json:"projectPath"`
	IsSidechain  bool   `json:"isSidechain"`
}

const indexFile = "sessions-index.json"

// WriteFileAtomic writes data to a temp file in path's directory and renames
// it into place, so readers never see a partial file.
func WriteFileAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename %s -> %s: %w", tmpName, path, err)
	}
	return nil
}

// readJSONDoc decodes a JSON object file generically (UseNumber). Absent
// files return (nil, false, nil); malformed files are errors naming the path.
func readJSONDoc(path string) (map[string]any, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	v, err := decodeOne(data)
	if err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("parse %s: top level is not an object", path)
	}
	return obj, true, nil
}

func encodeIndented(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ReadIndexEntry returns the entry for id, ok=false if the file or entry is absent.
func ReadIndexEntry(projectDir string, id ID) (*IndexEntry, bool, error) {
	doc, ok, err := readJSONDoc(filepath.Join(projectDir, indexFile))
	if err != nil || !ok {
		return nil, false, err
	}
	entries, _ := doc["entries"].([]any)
	for _, raw := range entries {
		obj, _ := raw.(map[string]any)
		if obj["sessionId"] != string(id) {
			continue
		}
		b, err := json.Marshal(obj)
		if err != nil {
			return nil, false, err
		}
		var e IndexEntry
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, false, fmt.Errorf("parse index entry %s: %w", id, err)
		}
		return &e, true, nil
	}
	return nil, false, nil
}

// MergeIndexEntry adds or replaces the entry with e.SessionID. Other entries
// and unknown top-level fields are preserved; the file is created (version
// 1, originalPath = e.ProjectPath) if absent. The write is atomic.
func MergeIndexEntry(projectDir string, e IndexEntry) error {
	path := filepath.Join(projectDir, indexFile)
	doc, ok, err := readJSONDoc(path)
	if err != nil {
		return err
	}
	if !ok {
		doc = map[string]any{"version": json.Number("1"), "entries": []any{}, "originalPath": e.ProjectPath}
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var entryObj map[string]any
	if err := json.Unmarshal(b, &entryObj); err != nil {
		return err
	}
	entries, _ := doc["entries"].([]any)
	replaced := false
	for i, raw := range entries {
		if obj, _ := raw.(map[string]any); obj["sessionId"] == e.SessionID {
			entries[i] = entryObj
			replaced = true
		}
	}
	if !replaced {
		entries = append(entries, entryObj)
	}
	doc["entries"] = entries
	out, err := encodeIndented(doc)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return WriteFileAtomic(path, out, 0o600)
}
