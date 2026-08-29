// Package session knows the on-disk model of a Claude Code session (spec §3):
// where its files are, how to find it, what it used, and how to rewrite its
// paths for another host.
package session

import (
	"fmt"
	"regexp"
	"strings"
)

// ID is a canonical (lower-case) session uuid.
type ID string

var uuidRe = regexp.MustCompile(`\A[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\z`)

// IsUUID reports whether s is a full (lower-case) uuid.
func IsUUID(s string) bool { return uuidRe.MatchString(s) }

// ParseID accepts a full uuid in any case and returns it lower-cased.
func ParseID(s string) (ID, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !IsUUID(s) {
		return "", fmt.Errorf("not a session id (full uuid expected): %q", s)
	}
	return ID(s), nil
}

// Short is the first 8 characters, for banners and logs.
func (id ID) Short() string {
	if len(id) < 8 {
		return string(id)
	}
	return string(id[:8])
}
