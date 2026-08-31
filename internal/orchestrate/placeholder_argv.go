package orchestrate

import "github.com/mithro/go-claude-teleport/internal/session"

// PlaceholderArgv builds the built-in placeholder command line (spec §11).
// The argv keeps `--resume <uuid>` adjacent so go-tmux-saver's process
// resolver and procx.IsPlaceholderArgv keep recognising the pane.
func PlaceholderArgv(id session.ID, savedOutput string, now bool, teleportedTo, teleportedAt string) []string {
	argv := []string{"claude-teleport", "placeholder", "--resume", string(id)}
	if savedOutput != "" {
		argv = append(argv, "--saved-output", savedOutput)
	}
	if now {
		argv = append(argv, "--now")
	}
	if teleportedTo != "" {
		argv = append(argv, "--teleported-to", teleportedTo)
		if teleportedAt != "" {
			argv = append(argv, "--teleported-at", teleportedAt)
		}
	}
	return argv
}

// SuspendArgv is what the `suspended` end state types (spec §9): go-tmux-
// saver's claude-resume when the destination has it, else our placeholder
// without --now.
func SuspendArgv(id session.ID, savedOutput string, hasClaudeResume bool) []string {
	if hasClaudeResume {
		argv := []string{"claude-resume", string(id)}
		if savedOutput != "" {
			argv = append(argv, "--saved-output", savedOutput)
		}
		return argv
	}
	return PlaceholderArgv(id, savedOutput, false, "", "")
}
