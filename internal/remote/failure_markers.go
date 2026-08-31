package remote

import "strings"

// FailureMarkers are substrings of Claude Code's output that mean the
// destination did NOT resume (spec §6.2). Update when Claude changes its
// wording; TestHasFailureMarker pins the current set. Order matters:
// HasFailureMarker returns the first list entry that matches, so the
// specific markers come before "Please run /login" — a generic suffix
// several of the more specific messages also carry (e.g. an expired OAuth
// token) and must not shadow them.
var FailureMarkers = []string{
	"Not logged in",
	"Invalid API key",
	"No conversation found",
	"not found for",
	"Unable to resume",
	"OAuth token has expired",
	"Please run /login",
}

// HasFailureMarker returns the first marker found in text.
func HasFailureMarker(text string) (string, bool) {
	for _, m := range FailureMarkers {
		if strings.Contains(text, m) {
			return m, true
		}
	}
	return "", false
}
