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
		{dialog, "Quick safety check"},
		{"    Yes, I trust this folder\n", "Yes, I trust this folder"},
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
