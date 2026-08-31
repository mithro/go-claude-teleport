package session

import (
	"fmt"
	"regexp"
	"strings"
)

// Selector is the parsed positional/environment session selection (spec §5).
type Selector struct {
	Current    bool   // no args: use $CLAUDE_CODE_SESSION_ID / $TMUX_PANE
	ID         ID     // full uuid
	Prefix     string // >=4 hex chars, or a registry name
	TmuxSess   string // two-word form
	TmuxWindow string
	TmuxPane   string // $TMUX_PANE when Current (resolution hint)
}

// Env is the environment inside (or near) a session.
type Env struct {
	SessionID string // $CLAUDE_CODE_SESSION_ID
	PID       string // $CLAUDE_PID
	TmuxPane  string // $TMUX_PANE
	Tmux      string // $TMUX
}

var hexRe = regexp.MustCompile(`\A[0-9a-f]+\z`)

// ParseSelector classifies the positional arguments (0, 1 or 2 words).
func ParseSelector(args []string, env Env) (Selector, error) {
	switch len(args) {
	case 0:
		sel := Selector{Current: true, TmuxPane: env.TmuxPane}
		if env.SessionID != "" {
			id, err := ParseID(env.SessionID)
			if err != nil {
				return Selector{}, fmt.Errorf("CLAUDE_CODE_SESSION_ID: %w", err)
			}
			sel.ID = id
		}
		return sel, nil
	case 1:
		arg := strings.TrimSpace(args[0])
		if IsUUID(strings.ToLower(arg)) {
			id, _ := ParseID(arg)
			return Selector{ID: id}, nil
		}
		if hexRe.MatchString(strings.ToLower(arg)) && len(arg) < 4 {
			return Selector{}, fmt.Errorf("session id prefix %q: at least 4 hex characters are required", arg)
		}
		if arg == "" {
			return Selector{}, fmt.Errorf("empty session selector")
		}
		return Selector{Prefix: arg}, nil
	case 2:
		return Selector{TmuxSess: args[0], TmuxWindow: args[1]}, nil
	default:
		return Selector{}, fmt.Errorf("too many arguments: expected [<session>] or <tmux-session> <window>, got %d", len(args))
	}
}
