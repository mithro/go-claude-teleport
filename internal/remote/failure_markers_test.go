package remote

import "testing"

func TestHasFailureMarker(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"╭─ Welcome to Claude Code ─╮\n> ", ""},
		{"Not logged in · Please run /login", "Not logged in"},
		{"Error: Invalid API key · Fix external API key", "Invalid API key"},
		{"No conversation found with session ID: 3f2a9c1e", "No conversation found"},
		{"Session 3f2a… not found for this project", "not found for"},
		{"Unable to resume session", "Unable to resume"},
		{"OAuth token has expired. Please run /login", "OAuth token has expired"},
	}
	for _, c := range cases {
		got, ok := HasFailureMarker(c.text)
		if (c.want != "") != ok || got != c.want {
			t.Errorf("HasFailureMarker(%q) = %q,%v want %q", c.text, got, ok, c.want)
		}
	}
}

// TestHasTrustPrompt pins Claude Code 2.1.259's first-run trust dialog
// (ruling R-P3-TRUST-1 item 2). It is NOT a failure marker: the
// destination Claude is alive and waiting for an answer, not broken — and
// its wording must never be answered with the "/login" advice.
func TestHasTrustPrompt(t *testing.T) {
	dialog := "╭─────────────────────────────────╮\n" +
		"│ Quick safety check              │\n" +
		"│ Is this a project you created   │\n" +
		"│ or one you trust?               │\n" +
		"│  ❯ No, exit                     │\n" +
		"│    Yes, I trust this folder     │\n" +
		"╰─────────────────────────────────╯\n"
	cases := []struct {
		text string
		want string
	}{
		{"╭─ Welcome to Claude Code ─╮\n> ", ""},
		{"Not logged in · Please run /login", ""},
		// The whole dialog: the rendered selection identifies it first.
		{dialog, trustNoSelected},
		// Both phrases with no selection on screen (a narrow pane that
		// wrapped the marker away, say) still identify a dialog.
		{"Quick safety check: Is this a project you created or one you trust?\n  No, exit\n  Yes, I trust this folder\n", "Quick safety check"},
		// The rendered selection alone is enough: after Down, real Claude
		// Code 2.1.259 draws exactly this (verified in the layer-2
		// container), and the question text may have scrolled.
		{"   No, exit\n ❯ Yes, I trust this folder\n", trustYesSelected},
		{" ❯ No, exit\n   Yes, I trust this folder\n", trustNoSelected},
		// A resumed conversation QUOTING the dialog is not a dialog: one
		// phrase on its own, with no rendered selection, must never make
		// the confirm step type Down+Enter into a healthy Claude.
		{"> remind me what \"Quick safety check\" meant\n", ""},
		{"assistant: it asks whether you trust the folder — \"Yes, I trust this folder\".\n", ""},
	}
	for _, c := range cases {
		got, ok := HasTrustPrompt(c.text)
		if (c.want != "") != ok || got != c.want {
			t.Errorf("HasTrustPrompt(%q) = %q,%v want %q", c.text, got, ok, c.want)
		}
	}
	// A trust prompt is not a failure marker, and vice versa.
	if m, hit := HasFailureMarker(dialog); hit {
		t.Errorf("the trust dialog matched failure marker %q", m)
	}
}
