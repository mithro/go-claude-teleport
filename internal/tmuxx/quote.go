// Copied from github.com/mithro/go-tmux-saver internal/tmuxctl
// (Apache-2.0, same author). Keep in sync by hand; do not import.

package tmuxx

import "strings"

// Quote wraps s as ONE tmux double-quoted command-line argument.
//
// RULING R30: tmux's own double-quote syntax is NOT Go's %q, and NOT a
// POSIX shell's either — verified against this tmux (next-3.8) directly via
// control mode: inside "…", tmux EXPANDS "$NAME" (an argument written as
// "'echo' '$HOME'" is typed into the pane as "'echo' '/home/tim'" — tmux
// resolved $HOME itself before send-keys ever ran), and Go-style "\xNN"
// escapes are mangled (an argument written as "esc\x1bhere" is typed as
// "escx1bhere" — tmux doesn't understand \x and just drops the backslash).
// "\"", "\\", "\$", ";", "#{…}", and a raw literal tab all pass through
// tmux's double-quote parsing untouched.
//
// So only backslash, the closing quote, and the dollar sign need escaping
// for tmux itself. A literal newline or carriage return in the argument
// would otherwise end the command line before reaching tmux's own escape
// handling, so those are also escaped, as "\n"/"\r" text (not the control
// bytes) — tmux does understand those in a double-quoted string. Every
// other byte, including a raw ESC, passes through unescaped.
//
// EVERY data-derived argument on a tmux command line must go through Quote,
// including composed targets like "-t <session>:<index>.<pane>": a session
// or window name is arbitrary user text, and tmux treats an unquoted ';' as
// a command separator (a session named `evil;kill-window -t default:0`
// really did run kill-window against a live server before this was
// enforced) and an unquoted space as an argument separator.
func Quote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\', '"', '$':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// UnvisName reverses the vis(3) encoding tmux applies to session and
// window names at creation time (session_check_name → utf8_stravis with
// VIS_OCTAL|VIS_CSTYLE|VIS_TAB|VIS_NL): stored names carry `\` doubled and
// control characters rendered as \t, \n, \ooo, … — probe-verified on tmux
// 3.5a and next-3.8 (`new-session -s 'a\b'` stores a\\b; has-session
// confirms the stored spelling). Handing a stored name back to
// `new-session -s` / `new-window -n` verbatim would re-encode it (\\ →
// \\\\), so restore decodes first; tmux's own re-encoding then reproduces
// the stored spelling exactly. Raw backslashes are always doubled by the
// encoder, so every backslash in a stored name begins an escape — an
// unrecognised escape is preserved literally as the safe fallback.
func UnvisName(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i == len(s)-1 {
			b.WriteByte(c)
			continue
		}
		i++
		switch n := s[i]; {
		case n == '\\':
			b.WriteByte('\\')
		case n == 'n':
			b.WriteByte('\n')
		case n == 't':
			b.WriteByte('\t')
		case n == 'r':
			b.WriteByte('\r')
		case n == 'a':
			b.WriteByte('\a')
		case n == 'b':
			b.WriteByte('\b')
		case n == 'f':
			b.WriteByte('\f')
		case n == 'v':
			b.WriteByte('\v')
		case n == 's':
			b.WriteByte(' ')
		case n >= '0' && n <= '7':
			v, j := 0, 0
			for ; j < 3 && i+j < len(s) && s[i+j] >= '0' && s[i+j] <= '7'; j++ {
				v = v*8 + int(s[i+j]-'0')
			}
			i += j - 1
			b.WriteByte(byte(v))
		default:
			b.WriteByte('\\')
			b.WriteByte(n)
		}
	}
	return b.String()
}

// CanonicalMember picks the one session of a tmux session group that
// snapshots and live-state queries keep (issue #12): every member of a
// group reports session_grouped=1 — there is no "original" flagged 0 — so
// skipping grouped sessions outright drops the whole group. The canonical
// member is the one whose name equals the group name (tmux names groups
// after the session they were created from), falling back to the lexically
// smallest member so the choice stays deterministic when that session is
// gone. members must be non-empty.
func CanonicalMember(group string, members []string) string {
	best := ""
	for _, m := range members {
		if m == group {
			return m
		}
		if best == "" || m < best {
			best = m
		}
	}
	return best
}
