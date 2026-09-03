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

// TrustPromptMarkers are the phrases of Claude Code's first-run trust
// dialog ("Quick safety check: Is this a project you created or one you
// trust? … ❯ No, exit / Yes, I trust this folder"), captured verbatim from
// 2.1.259 in the layer-2 container.
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

// trustNoSelected/trustYesSelected are the dialog's two rendered choices
// with the selection marker on them, exactly as Claude Code 2.1.259 draws
// them (U+276F then one space; "❯ No, exit" before an answer, "❯ Yes, I
// trust this folder" after one Down). Nothing but a real dialog draws
// these, so either one alone identifies one.
const (
	trustNoSelected  = "❯ No, exit"
	trustYesSelected = "❯ Yes, I trust this folder"
)

// HasTrustPrompt reports whether text is a trust dialog waiting for an
// answer, and what identified it.
//
// A single phrase is not enough (PR #11 review): a resumed conversation
// that QUOTES the dialog — a transcript replayed on screen, a user asking
// what it meant — would otherwise make the confirm step type Down+Enter
// into a perfectly healthy Claude, or fail a good teleport with the trust
// advice. So it takes either a rendered selection line, which only the
// dialog itself draws, or BOTH phrases of the real dialog together.
func HasTrustPrompt(text string) (string, bool) {
	for _, sel := range []string{trustYesSelected, trustNoSelected} {
		if strings.Contains(text, sel) {
			return sel, true
		}
	}
	for _, m := range TrustPromptMarkers {
		if !strings.Contains(text, m) {
			return "", false
		}
	}
	return TrustPromptMarkers[0], true
}

// TrustAnswerSelected reports whether the pane is showing "Yes, I trust
// this folder" as the SELECTED choice — what a Down keystroke must have
// achieved before Enter may be pressed (PR #11 review minor 4).
func TrustAnswerSelected(text string) bool { return strings.Contains(text, trustYesSelected) }

// TrustPromptWaiting opens the ConfirmClaude error raised when the
// destination is at that dialog and the source carried no accepted trust
// to answer it with. internal/cli matches on it so it never advises
// `/login` for a Claude that resumed perfectly well (R-P3-TRUST-1 item 2).
const TrustPromptWaiting = "destination Claude is waiting at the trust prompt"
