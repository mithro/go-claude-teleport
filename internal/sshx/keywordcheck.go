package sshx

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/kevinburke/ssh_config"
)

// UnknownKeyword is one keyword found in a config that ssh_config(5) does not
// define, with where it was found.
type UnknownKeyword struct {
	Keyword string
	File    string
	Line    int
}

// UnknownKeywordError reports keywords this build does not recognise. ssh
// itself refuses a config containing one ("Bad configuration option"), and so
// does this: a keyword we cannot place is either a typo, in which case the
// setting the user believes is in force is not, or a keyword newer than this
// build's table, in which case guessing is worse than saying so.
type UnknownKeywordError struct {
	Unknown []UnknownKeyword
}

func (e *UnknownKeywordError) Error() string {
	var b strings.Builder
	b.WriteString("unknown ssh_config keyword")
	if len(e.Unknown) != 1 {
		b.WriteString("s")
	}
	var names []string
	seen := map[string]bool{}
	for _, u := range e.Unknown {
		fmt.Fprintf(&b, "\n  %s line %d: %s", u.File, u.Line, u.Keyword)
		if k := strings.ToLower(u.Keyword); !seen[k] {
			seen[k] = true
			names = append(names, u.Keyword)
		}
	}
	fmt.Fprintf(&b, "\nset IgnoreUnknown in the config, or pass -o IgnoreUnknown=%q, to proceed anyway",
		strings.Join(names, ","))
	return b.String()
}

// matchKeywordPattern reports whether an OpenSSH pattern list covers name.
// Entries are comma-separated, "*" and "?" are wildcards, and a "!" entry that
// matches vetoes the whole list, as everywhere else in ssh_config.
func matchKeywordPattern(patterns []string, name string) bool {
	lower := strings.ToLower(name)
	matched := false
	for _, group := range patterns {
		for _, p := range strings.Split(group, ",") {
			p = strings.ToLower(strings.TrimSpace(p))
			if p == "" {
				continue
			}
			negated := strings.HasPrefix(p, "!")
			p = strings.TrimPrefix(p, "!")
			if ok, err := path.Match(p, lower); err != nil || !ok {
				continue
			}
			if negated {
				return false
			}
			matched = true
		}
	}
	return matched
}

// checkKeywords walks a config and every file it Includes, appending to out
// each keyword that ssh_config(5) does not define and that no IgnoreUnknown
// pattern in force at that point covers. It returns the pattern list as it
// stands at the end of the file, because an Include is textual: patterns set
// inside one go on applying after it.
//
// The whole file is walked, including blocks that will not apply to the host
// being dialled. ssh reads the file before it decides which blocks match, and
// a typo in a block for another host is still a typo.
func checkKeywords(src, file string, depth int, o ConfigOptions, ignore []string, out *[]UnknownKeyword) []string {
	for i, line := range strings.Split(src, "\n") {
		key, rest := splitKeyword(line)
		if key == "" {
			continue
		}
		switch strings.ToLower(key) {
		case "ignoreunknown":
			ignore = append(ignore, rest)
			continue
		case "include":
			if depth >= maxIncludeDepth {
				continue
			}
			for _, p := range includePaths(rest, o.Home) {
				b, err := os.ReadFile(p)
				if err != nil {
					// Unreadable or a directory: the parser reports that
					// itself, and more precisely than we would here.
					continue
				}
				ignore = checkKeywords(string(b), p, depth+1, o, ignore, out)
			}
			continue
		}
		if _, defined := sshConfigKeywords[strings.ToLower(key)]; defined {
			continue
		}
		if matchKeywordPattern(ignore, key) {
			continue
		}
		*out = append(*out, UnknownKeyword{Keyword: key, File: file, Line: i + 1})
	}
	return ignore
}

// IgnoredKeywords returns the keywords ssh_config defines, and that appear in
// cfg with a value for alias, which this package does not act on — what the
// config asks for and the teleport will not do. `doctor` reports it; nothing
// else needs to, since these are known gaps rather than surprises.
func IgnoredKeywords(cfg *ssh_config.Config, alias string) []string {
	if cfg == nil {
		return nil
	}
	var out []string
	for lower, canonical := range sshConfigKeywords {
		if honouredKeywords[lower] != "" {
			continue
		}
		if v, err := cfg.Get(alias, canonical); err == nil && v != "" {
			out = append(out, canonical)
		}
	}
	sort.Strings(out)
	return out
}
