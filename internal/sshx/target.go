// Package sshx is the in-binary ssh client: ssh_config resolution, agent and
// key-file auth, known_hosts verification and jump chains (spec §4.2).
package sshx

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Target is a parsed "[user@]host[:port]" plus an optional jump chain.
type Target struct {
	User string
	Host string   // as typed (alias) — resolved HostName lives in Resolved
	Port int      // 0 = not specified
	Via  []Target // jump chain, outermost first
}

var safeArg = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// ParseTarget parses "[user@]host[:port]"; IPv6 literals must be bracketed.
func ParseTarget(s string) (Target, error) {
	if s == "" {
		return Target{}, fmt.Errorf("ssh target: empty")
	}
	var t Target
	if i := strings.LastIndex(s, "@"); i >= 0 {
		t.User, s = s[:i], s[i+1:]
		if t.User == "" {
			return Target{}, fmt.Errorf("ssh target %q: empty user", s)
		}
	}
	host, port := s, ""
	if strings.HasPrefix(s, "[") || strings.Count(s, ":") == 1 {
		h, p, err := net.SplitHostPort(s)
		if err == nil {
			host, port = h, p
		} else if strings.HasPrefix(s, "[") {
			return Target{}, fmt.Errorf("ssh target %q: %w", s, err)
		}
	}
	if host == "" {
		return Target{}, fmt.Errorf("ssh target %q: empty host", s)
	}
	t.Host = host
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return Target{}, fmt.Errorf("ssh target %q: bad port %q", s, port)
		}
		t.Port = n
	}
	return t, nil
}

// Quote renders argv for the remote sh: safe words verbatim, others single-quoted.
func Quote(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && safeArg.MatchString(a) {
			parts[i] = a
			continue
		}
		parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(parts, " ")
}
