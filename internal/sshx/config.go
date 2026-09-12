package sshx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kevinburke/ssh_config"
)

// maxIncludeDepth mirrors the ssh_config parser's own recursion cap, so the
// repair pass below walks exactly the same Include tree the parser will.
const maxIncludeDepth = 5

// matchCriteria is the set of Match criteria OpenSSH defines. The in-process
// parser (spec §4.2 reads ~/.ssh/config in the binary, it does not shell out
// to ssh) implements only "all" and "host", and treats every other criterion
// as a syntax error that fails the whole file — including files pulled in by
// Include. A guard we cannot evaluate is dropped rather than allowed to brick
// the config; see DecodeConfig.
var matchCriteria = map[string]bool{
	"all": true, "canonical": true, "exec": true, "final": true,
	"host": true, "localnetwork": true, "localuser": true,
	"originalhost": true, "tagged": true, "user": true,
}

// splitKeyword splits an ssh_config line into its keyword and the rest,
// accepting the "Key Value", "Key=Value" and "Key = Value" spellings. A blank
// or comment line has no keyword.
func splitKeyword(line string) (key, rest string) {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "#") {
		return "", ""
	}
	i := strings.IndexAny(s, " \t=")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimLeft(strings.TrimSpace(s[i:]), "= \t")
}

// matchEvaluable reports whether the parser can decide this Match guard for
// itself. Only a bare "all" and a plain "host <patterns>" qualify: a compound
// guard such as "host x user y" is rejected too, because the parser reads the
// trailing criteria as further host patterns and would apply the block to the
// wrong hosts rather than failing.
func matchEvaluable(rest string) bool {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return false
	}
	switch strings.ToLower(fields[0]) {
	case "all":
		return len(fields) == 1
	case "host":
		if len(fields) < 2 {
			return false
		}
		for _, f := range fields[1:] {
			if matchCriteria[strings.ToLower(f)] {
				return false
			}
		}
		return true
	}
	return false
}

// describeMatch names, for the warning, why a guard could not be evaluated.
func describeMatch(rest string) string {
	fields := strings.Fields(rest)
	switch {
	case len(fields) == 0:
		return "Match with no criterion"
	case strings.EqualFold(fields[0], "all"), strings.EqualFold(fields[0], "host"):
		return fmt.Sprintf("compound Match guard %q", strings.TrimSpace(rest))
	default:
		return fmt.Sprintf("Match criterion %q", fields[0])
	}
}

// includePaths resolves an Include directive's arguments the way the parser
// does: absolute as-is, "~/" against home, anything else against ~/.ssh, each
// glob-expanded, duplicates removed, order preserved.
func includePaths(args, home string) []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range strings.Fields(args) {
		var p string
		switch {
		case filepath.IsAbs(a):
			p = a
		case strings.HasPrefix(a, "~/"):
			p = filepath.Join(home, a[2:])
		default:
			p = filepath.Join(home, ".ssh", a)
		}
		matches, err := filepath.Glob(p)
		if err != nil {
			continue
		}
		for _, f := range matches {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	return out
}

// expandInclude repairs the files an Include names and, only if one of them
// actually needed repairing, returns their contents to inline in its place.
// Leaving the directive alone otherwise keeps line numbers honest and keeps
// every existing parse error (an Include of a directory, a recursive Include)
// reported exactly as before.
//
// Inlining makes a Host block left open at the end of an included file carry
// into the parent, which is what OpenSSH itself does and what the parser —
// which reads each included file as a separate config — does not. That only
// applies to a file we are already repairing.
func expandInclude(args, home string, depth int, warnf func(string, ...any)) (string, bool) {
	if depth >= maxIncludeDepth {
		return "", false
	}
	paths := includePaths(args, home)
	bodies := make([]string, 0, len(paths))
	repaired := false
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", false
		}
		body, changed := sanitizeConfig(string(b), p, home, depth+1, warnf)
		repaired = repaired || changed
		bodies = append(bodies, body)
	}
	if !repaired {
		return "", false
	}
	return strings.Join(bodies, "\n"), true
}

// sanitizeConfig blanks out every Match block whose guard the parser cannot
// evaluate, recursing through Include. Dropped lines become empty lines rather
// than disappearing, so the line numbers in anything the parser reports
// afterwards still match the user's file. It returns the rewritten text and
// whether it dropped anything.
func sanitizeConfig(src, path, home string, depth int, warnf func(string, ...any)) (string, bool) {
	lines := strings.Split(src, "\n")
	dropping, changed := false, false
	for i, line := range lines {
		key, rest := splitKeyword(line)
		switch strings.ToLower(key) {
		case "host":
			dropping = false
		case "match":
			dropping = !matchEvaluable(rest)
			if dropping {
				changed = true
				warnf("%s line %d: unsupported %s, ignoring the block (its settings are not applied)",
					path, i+1, describeMatch(rest))
			}
		case "include":
			if !dropping {
				if inlined, ok := expandInclude(rest, home, depth, warnf); ok {
					lines[i] = inlined
					changed = true
					continue
				}
			}
		}
		if dropping {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n"), changed
}

// DecodeConfig decodes ssh_config bytes read from path, resolving Include
// against home.
//
// The parser implements two of OpenSSH's ten Match criteria, and rejects the
// whole file — before any host is resolved — when it meets one of the other
// eight anywhere in it, Include'd files included. A Match block guarded by a
// criterion we cannot evaluate is therefore dropped, with a warning naming it,
// and the rest of the config is used: an unevaluated guard means the block
// simply does not apply, which is what OpenSSH does when the guard is false.
// Every other parse error stays an error, because a config that is genuinely
// malformed is the user's to fix.
func DecodeConfig(b []byte, path, home string, warnf func(string, ...any)) (*ssh_config.Config, error) {
	cfg, err := ssh_config.DecodeBytes(b)
	if err == nil {
		return cfg, nil
	}
	if warnf == nil {
		warnf = func(string, ...any) {}
	}
	repaired, changed := sanitizeConfig(string(b), path, home, 0, warnf)
	if !changed {
		return nil, err
	}
	return ssh_config.DecodeBytes([]byte(repaired))
}
