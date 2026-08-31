package session

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Mapping rewrites the path prefix From to To.
type Mapping struct{ From, To string }

// PathMap is an ordered prefix rewrite (longest prefix first; spec §7.2).
type PathMap []Mapping

// ParseMappings parses "SRC=DST" strings (trailing slashes trimmed) and
// validates that both sides are absolute, and that From is not duplicated.
func ParseMappings(specs []string) ([]Mapping, error) {
	var out []Mapping
	seen := map[string]bool{}
	for _, s := range specs {
		from, to, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fmt.Errorf("--map %q: expected SRC=DST", s)
		}
		from, to = cleanAbs(from), cleanAbs(to)
		if from == "" || to == "" {
			return nil, fmt.Errorf("--map %q: both sides must be absolute paths", s)
		}
		if seen[from] {
			return nil, fmt.Errorf("--map: duplicate From %q", from)
		}
		seen[from] = true
		out = append(out, Mapping{From: from, To: to})
	}
	return out, nil
}

// cleanAbs returns the cleaned absolute path, or "" if p is not absolute.
func cleanAbs(p string) string {
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, "/") {
		return ""
	}
	return path.Clean(p)
}

// NewPathMap sorts by len(From) descending. It panics on a relative path or
// a duplicate From: callers validate user input with ParseMappings first,
// so a panic here is a programming error.
func NewPathMap(maps ...Mapping) PathMap {
	seen := map[string]bool{}
	out := make(PathMap, 0, len(maps))
	for _, m := range maps {
		from, to := cleanAbs(m.From), cleanAbs(m.To)
		if from == "" || to == "" {
			panic(fmt.Sprintf("session.NewPathMap: mapping %q -> %q is not absolute", m.From, m.To))
		}
		if seen[from] {
			panic(fmt.Sprintf("session.NewPathMap: duplicate From %q", from))
		}
		seen[from] = true
		out = append(out, Mapping{From: from, To: to})
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].From) > len(out[j].From) })
	return out
}

func (m PathMap) Empty() bool { return len(m) == 0 }

// ApplyPath rewrites p when it equals a From or starts with From + "/".
func (m PathMap) ApplyPath(p string) string {
	for _, mp := range m {
		if p == mp.From {
			return mp.To
		}
		if strings.HasPrefix(p, mp.From+"/") {
			return mp.To + p[len(mp.From):]
		}
	}
	return p
}

// isPathByte reports whether c can be part of a path inside free text; the
// complement delimits path boundaries in Apply.
func isPathByte(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '"', '\'', '=', ':', ',', ';', '(', ')', '[', ']', '{', '}', '<', '>', '|', '&':
		return false
	}
	return true
}

// Apply rewrites every occurrence of a From inside s that starts at a
// boundary (start of string or a non-path byte) and ends at a boundary
// ("/", end of string, or a non-path byte). Used for JSON string values,
// which may embed paths in commands and messages.
func (m PathMap) Apply(s string) string {
	if len(m) == 0 || !strings.Contains(s, "/") {
		return s
	}
	var b strings.Builder
	i := 0
	for i < len(s) {
		if i == 0 || !isPathByte(s[i-1]) {
			matched := false
			for _, mp := range m {
				if !strings.HasPrefix(s[i:], mp.From) {
					continue
				}
				end := i + len(mp.From)
				if end == len(s) || s[end] == '/' || !isPathByte(s[end]) || s[end] == '.' && (end+1 == len(s) || !isPathByte(s[end+1])) {
					b.WriteString(mp.To)
					i = end
					matched = true
					break
				}
			}
			if matched {
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
