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
