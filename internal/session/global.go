package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ProjectEntry is projects["<cwd>"] of ~/.claude.json: opaque, copied whole.
type ProjectEntry = map[string]any

// ReadProjectEntry returns projects[cwd]. Only the "projects" key is
// inspected; nothing else in the file is read into typed structures.
func ReadProjectEntry(globalJSON, cwd string) (ProjectEntry, bool, error) {
	doc, ok, err := readJSONDoc(globalJSON)
	if err != nil || !ok {
		return nil, false, err
	}
	projects, _ := doc["projects"].(map[string]any)
	e, ok := projects[cwd].(map[string]any)
	if !ok {
		return nil, false, nil
	}
	return e, true, nil
}

// AddProjectEntry adds projects[cwd] = e if absent. The existing file is
// first copied to <globalJSON>.claude-teleport.bak, then rewritten via a
// temp file + rename. This is the only global file the tool ever writes,
// and only to add a key; every other key is preserved byte-for-byte in
// value (numbers via json.Number, HTML unescaped).
func AddProjectEntry(globalJSON, cwd string, e ProjectEntry) (added bool, err error) {
	doc, exists, err := readJSONDoc(globalJSON)
	if err != nil {
		return false, err
	}
	if !exists {
		doc = map[string]any{}
	}
	projects, _ := doc["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	if _, present := projects[cwd]; present {
		return false, nil
	}
	if exists {
		if err := copyFile(globalJSON, globalJSON+".claude-teleport.bak"); err != nil {
			return false, fmt.Errorf("backup %s: %w", globalJSON, err)
		}
	}
	projects[cwd] = e
	doc["projects"] = projects
	out, err := encodeIndented(doc)
	if err != nil {
		return false, fmt.Errorf("encode %s: %w", globalJSON, err)
	}
	if err := WriteFileAtomic(globalJSON, out, 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
