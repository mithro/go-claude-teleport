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

// TrustPromptMarkers are substrings of Claude Code's first-run trust
// dialog ("Quick safety check: Is this a project you created or one you
// trust? … ❯ No, exit / Yes, I trust this folder"), seen in 2.1.259.
//
// It is deliberately NOT a failure marker: a Claude sitting at this dialog
// resumed fine, it is simply waiting for an answer and writes no registry
// entry until it gets one — which is how the first real teleport failed
// its start step (ruling R-P3-TRUST-1). The strings are matched against
// the pane's VISIBLE screen only (tmuxx.CaptureScreen): the same words in
// the scrollback describe a dialog that has already been answered.
var TrustPromptMarkers = []string{
	"Quick safety check",
	"Yes, I trust this folder",
}

// HasTrustPrompt returns the first trust-dialog marker found in text.
func HasTrustPrompt(text string) (string, bool) {
	for _, m := range TrustPromptMarkers {
		if strings.Contains(text, m) {
			return m, true
		}
	}
	return "", false
}

// TrustPromptWaiting opens the ConfirmClaude error raised when the
// destination is at that dialog and the source carried no accepted trust
// to answer it with. internal/cli matches on it so it never advises
// `/login` for a Claude that resumed perfectly well (R-P3-TRUST-1 item 2).
const TrustPromptWaiting = "destination Claude is waiting at the trust prompt"
