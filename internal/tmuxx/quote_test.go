// Copied from github.com/mithro/go-tmux-saver internal/tmuxctl
// (Apache-2.0, same author). Keep in sync by hand; do not import.

package tmuxx

import "testing"

// TestQuote covers RULING R30's escaping rules directly: backslash,
// double-quote, and dollar sign are escaped (dollar because tmux's own
// double-quote parsing expands "$NAME" — verified against this tmux via
// control mode); a literal newline is escaped as "\n" text; and a raw ESC
// byte (0x1B) passes through completely untouched (Go's %q would have
// mangled it, per the same verification — tmux doesn't understand \xNN).
func TestQuote(t *testing.T) {
	in := "a\\b\"c$d\ne" + "\x1b" + "f"
	got := Quote(in)
	want := `"a\\b\"c\$d\ne` + "\x1b" + `f"`
	if got != want {
		t.Fatalf("Quote(%q) = %q, want %q", in, got, want)
	}
}

// TestQuoteCommandSeparators pins the C1 property the restore planner
// depends on: a value containing tmux's command separator (';') or an
// argument separator (' ') comes back as ONE quoted argument, with the
// separators inert inside the quotes.
func TestQuoteCommandSeparators(t *testing.T) {
	if got, want := Quote("evil;kill-window -t default:0"), `"evil;kill-window -t default:0"`; got != want {
		t.Fatalf("Quote = %q, want %q", got, want)
	}
}

// TestUnvisName covers the decode side of tmux's name storage (issue #8's
// hostile-names work): tmux vis(3)-encodes session/window names at
// creation (backslash doubled, control chars → \t/\n/\ooo...), so restore
// must decode a stored name before handing it back to -s/-n — tmux's own
// re-encoding then reproduces the stored spelling exactly.
//
// Not every tmux vis-encodes (PR #8 CI: ubuntu-latest's tmux 3.4 stores
// window names raw), so UnvisName must also be safe on a raw, never-encoded
// name: the `w\x` and `a\b` cases below are exactly what such a tmux would
// hand back for the live-test fixture names, and both must round-trip as
// the identity rather than being misread as a vis(3) escape.
func TestUnvisName(t *testing.T) {
	cases := []struct{ in, want string }{
		{`plain name`, `plain name`},
		{`a\\b`, `a\b`},              // stored doubled backslash
		{`a\\\\b`, `a\\b`},           // two stored backslashes
		{`tab\there`, "tab\there"},   // \t → TAB
		{`nl\nhere`, "nl\nhere"},     // \n → LF
		{`oct\011here`, "oct\there"}, // \011 → TAB (octal form)
		{`bel\007`, "bel\a"},         // full 3-digit octal
		{`mixed\\and\ttab`, "mixed\\and\ttab"},
		{`unknown\qkeep`, `unknown\qkeep`}, // unknown escape preserved
		{`q"uo\\te.s;s`, `q"uo\te.s;s`},    // the live-test fixture shape
		{`w\x`, `w\x`},                     // raw backslash, non-escape char: identity
		{`a\b`, `a\b`},                     // raw backslash + 'b': NOT read as vis(3) \b (backspace)
		{`a\r`, `a\r`},                     // raw backslash + 'r': NOT read as vis(3) \r (carriage return)
	}
	for _, c := range cases {
		if got := UnvisName(c.in); got != c.want {
			t.Errorf("UnvisName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCanonicalMember covers issue #12's selection rule: one member per
// session group survives into snapshots/LiveState — the member whose name
// equals the group name when present (the usual case: tmux names the group
// after the original session), else the lexically smallest member so the
// choice is deterministic.
func TestCanonicalMember(t *testing.T) {
	if got := CanonicalMember("default", []string{"default-36", "default", "default-25"}); got != "default" {
		t.Errorf("name==group should win, got %q", got)
	}
	if got := CanonicalMember("default", []string{"default-36", "default-25"}); got != "default-25" {
		t.Errorf("lexically smallest fallback, got %q", got)
	}
	if got := CanonicalMember("g", []string{"only"}); got != "only" {
		t.Errorf("single member, got %q", got)
	}
}
