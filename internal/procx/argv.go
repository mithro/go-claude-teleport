package procx

import "github.com/mithro/go-claude-teleport/internal/session"

// IsPlaceholderArgv recognises `claude-resume <uuid>` (go-tmux-saver, the
// rcfiles script) and `claude-teleport placeholder … --resume <uuid>`.
func IsPlaceholderArgv(argv []string) (sid string, ok bool) {
	sid, placeholder, ok := session.ArgvSessionID(argv)
	if !ok || !placeholder {
		return "", false
	}
	return sid, true
}

// IsClaudeArgv recognises a real claude process (`claude`, `…/claude`,
// `node …/cli.js`), returning its --resume id if any.
func IsClaudeArgv(argv []string) (resumeID string, ok bool) {
	sid, placeholder, ok := session.ArgvSessionID(argv)
	if !ok || placeholder {
		return "", false
	}
	return sid, true
}
