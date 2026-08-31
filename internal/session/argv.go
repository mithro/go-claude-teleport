package session

import (
	"path"
	"regexp"
	"strings"
)

var (
	// `claude-resume <uuid>` at a word/path boundary — covers the rcfiles
	// script (`python3 …/claude-resume <uuid>`), go-tmux-saver's built-in
	// (`go-tmux-saver claude-resume <uuid>`) and a bare `claude-resume`.
	claudeResumeRe = regexp.MustCompile(`(?:^|[\s/])claude-resume\s+([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)
	// `claude-teleport placeholder … --resume <uuid>`.
	teleportPlaceholderRe = regexp.MustCompile(`(?:^|[\s/])claude-teleport\s+placeholder\s(?:.*\s)?--resume\s+([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)
)

// ArgvSessionID classifies a pane's foreground command line.
//
//	ok=false                        not a claude or placeholder process
//	ok=true, placeholder=true       a placeholder holding sid
//	ok=true, placeholder=false      a real claude; sid is its --resume id or ""
//
// A real claude is argv[0] named `claude` (any directory) or `node …/cli.js`
// (Claude Code's npm/native entry point).
func ArgvSessionID(argv []string) (sid string, placeholder bool, ok bool) {
	if len(argv) == 0 {
		return "", false, false
	}
	joined := strings.Join(argv, " ")
	if m := teleportPlaceholderRe.FindStringSubmatch(joined); m != nil {
		return m[1], true, true
	}
	if m := claudeResumeRe.FindStringSubmatch(joined); m != nil {
		return m[1], true, true
	}
	base := path.Base(argv[0])
	isClaude := base == "claude"
	if (base == "node" || base == "nodejs" || base == "bun") && len(argv) > 1 {
		script := argv[1]
		isClaude = strings.HasSuffix(script, "/cli.js") || strings.Contains(script, "@anthropic-ai/claude-code")
	}
	if !isClaude {
		return "", false, false
	}
	for i := 1; i+1 < len(argv); i++ {
		if (argv[i] == "--resume" || argv[i] == "-r") && IsUUID(strings.ToLower(argv[i+1])) {
			return strings.ToLower(argv[i+1]), false, true
		}
	}
	return "", false, true
}
