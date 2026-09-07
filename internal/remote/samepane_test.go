package remote

import (
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// TestSamePaneIgnoresGroupedSessionName covers issue #19: the destination
// Claude registers its pane under whichever grouped-session alias is current
// (e.g. "default-97"), while the teleport opened the window under the base
// session name ("default"). The window id (@N) and pane id (%N) are unique
// per tmux server, so the same @win.%pane is the same pane regardless of the
// session-name prefix; confirmation must accept it.
func TestSamePaneIgnoresGroupedSessionName(t *testing.T) {
	ref := &session.TmuxRef{Session: "default", WindowID: "@249", PaneID: "%250"}
	cases := []struct {
		name    string
		regTmux string
		want    bool
	}{
		{"exact match", "default:@249.%250", true},
		{"grouped session alias (issue #19)", "default-97:@249.%250", true},
		{"another grouped alias", "default-10:@249.%250", true},
		{"different pane, same window", "default:@249.%251", false},
		{"different window, same pane number", "default:@250.%250", false},
		{"unparseable registry tmux", "garbage", false},
		{"empty registry tmux", "", false},
	}
	for _, c := range cases {
		if got := samePane(&session.Registry{Tmux: c.regTmux}, ref); got != c.want {
			t.Errorf("%s: samePane(reg.Tmux=%q, ref=%s:%s.%s) = %v, want %v",
				c.name, c.regTmux, ref.Session, ref.WindowID, ref.PaneID, got, c.want)
		}
	}
	if samePane(&session.Registry{Tmux: "default:@1.%1"}, nil) {
		t.Error("samePane with a nil ref must be false")
	}
}
