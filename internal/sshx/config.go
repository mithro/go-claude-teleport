package sshx

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kevinburke/ssh_config"
)

// ConfigOptions tunes how DecodeConfig treats the constructs the ssh_config
// parser cannot represent.
type ConfigOptions struct {
	Path          string               // file the bytes came from, named in warnings
	Home          string               // resolves a relative Include
	ExecAllow     []string             // commands a Match exec may run beyond the built-ins; "none" refuses every one
	ExecTimeout   time.Duration        // how long one Match exec guard may take (default defaultExecTimeout)
	IgnoreUnknown []string             // pattern list of unknown keywords to tolerate, as ssh_config's own IgnoreUnknown
	Warnf         func(string, ...any) // where repairs are reported; nil discards them
}

// maxIncludeDepth mirrors the ssh_config parser's own recursion cap, so the
// repair pass below walks exactly the same Include tree the parser will.
const maxIncludeDepth = 5

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

// leadingSpace returns the indentation of a line, so a rewritten guard keeps
// the shape of the one it replaces.
func leadingSpace(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
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
func expandInclude(args string, depth int, o ConfigOptions) (string, bool) {
	if depth >= maxIncludeDepth {
		return "", false
	}
	paths := includePaths(args, o.Home)
	bodies := make([]string, 0, len(paths))
	repaired := false
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", false
		}
		body, changed := sanitizeConfig(string(b), p, depth+1, o)
		repaired = repaired || changed
		bodies = append(bodies, body)
	}
	if !repaired {
		return "", false
	}
	return strings.Join(bodies, "\n"), true
}

// sanitizeConfig rewrites every Match block the parser cannot represent into
// one it can: a guard decided here becomes the part the parser still has to
// evaluate ("Match all" when there is nothing left), and a guard that is
// false or undecidable has its whole block blanked out. Dropped lines become
// empty lines rather than disappearing, so the line numbers in anything the
// parser reports afterwards still match the user's file. It recurses through
// Include and returns the rewritten text and whether it changed anything.
func sanitizeConfig(src, path string, depth int, o ConfigOptions) (string, bool) {
	lines := strings.Split(src, "\n")
	dropping, changed := false, false
	for i, line := range lines {
		key, rest := splitKeyword(line)
		switch strings.ToLower(key) {
		case "host":
			dropping = false
		case "match":
			verdict, rewritten, why := evalMatch(rest, o)
			dropping = verdict == matchDrop
			if why != "" {
				o.Warnf("%s line %d: %s; ignoring the block (its settings are not applied)",
					path, i+1, why)
			}
			if verdict == matchRewrite {
				lines[i] = leadingSpace(line) + "Match " + rewritten
				changed = true
				continue
			}
		case "include":
			if !dropping {
				if inlined, ok := expandInclude(rest, depth, o); ok {
					lines[i] = inlined
					changed = true
					continue
				}
			}
		}
		if dropping {
			lines[i] = ""
			changed = true
		}
	}
	return strings.Join(lines, "\n"), changed
}

// DecodeConfig decodes the ssh_config in b, which was read from o.Path.
//
// The parser implements two of OpenSSH's ten Match criteria, "all" and
// "host", and rejects the whole file — before any host is resolved — when it
// meets one of the other eight anywhere in it, Include'd files included. This
// adds a third: a Match exec guard is run, if the command it names is on the
// allow list, and the block is kept or skipped on its exit status the way
// OpenSSH would. A guard that cannot be decided, because its criterion is not
// implemented or its command is not allowed to run, is dropped with a warning
// naming it: an unevaluated guard means the block does not apply, which is
// what OpenSSH does when a guard is false. Every other parse error stays an
// error, because a config that is genuinely malformed is the user's to fix.
//
// Before any of that, every keyword in the file — and in the files it
// Includes — is checked against the set ssh_config(5) defines. One that is
// not defined is an error, as it is under ssh itself, unless an IgnoreUnknown
// pattern covers it.
func DecodeConfig(b []byte, o ConfigOptions) (*ssh_config.Config, error) {
	if o.Warnf == nil {
		o.Warnf = func(string, ...any) {}
	}
	// Refuse a keyword we cannot place before reading anything into the
	// config: a typo means the setting the user believes is in force is not.
	var unknown []UnknownKeyword
	checkKeywords(string(b), o.Path, 0, o, o.IgnoreUnknown, &unknown)
	if len(unknown) > 0 {
		return nil, &UnknownKeywordError{Unknown: unknown}
	}

	cfg, err := ssh_config.DecodeBytes(b)
	if err == nil {
		return cfg, nil
	}
	repaired, changed := sanitizeConfig(string(b), o.Path, 0, o)
	if !changed {
		return nil, err
	}
	return ssh_config.DecodeBytes([]byte(repaired))
}
