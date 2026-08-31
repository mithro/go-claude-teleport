// Copied from github.com/mithro/go-tmux-saver internal/tmuxctl
// (Apache-2.0, same author). Keep in sync by hand; do not import.

// Package tmuxctl talks to a tmux server over a single control-mode connection.
package tmuxx

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Reply is the body of one %begin…%end / %error block.
type Reply struct {
	Lines []string
	Err   bool
}

// tmuxNotifications is the set of tmux control-mode notification types.
// Matched as the first %-prefixed token followed by space or end-of-line.
var tmuxNotifications = map[string]bool{
	"%client-detached":         true,
	"%client-session-changed":  true,
	"%config-error":            true,
	"%continue":                true,
	"%extended-output":         true,
	"%layout-change":           true,
	"%message":                 true,
	"%output":                  true,
	"%pane-mode-changed":       true,
	"%paste-buffer-changed":    true,
	"%paste-buffer-deleted":    true,
	"%pause":                   true,
	"%session-changed":         true,
	"%session-renamed":         true,
	"%session-window-changed":  true,
	"%sessions-changed":        true,
	"%subscription-changed":    true,
	"%unlinked-window-add":     true,
	"%unlinked-window-close":   true,
	"%unlinked-window-renamed": true,
	"%window-add":              true,
	"%window-close":            true,
	"%window-pane-changed":     true,
	"%window-renamed":          true,
}

// ParseReplies reads control-mode output from r and sends one Reply per
// command block on out. Notifications outside a block (%exit, %layout-change…)
// are ignored; notifications inside a block (identified by their %-token in the
// whitelist) are dropped from the body. Terminators are matched by block number
// so a pane line that merely looks like "%end …" cannot close the block early.
// Returns nil at %exit or EOF. If the input ends while a block is open,
// returns an error wrapping io.ErrUnexpectedEOF.
func ParseReplies(r io.Reader, out chan<- Reply) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var (
		inBlock bool
		num     string
		cur     Reply
	)
	for sc.Scan() {
		line := sc.Text()
		if !inBlock {
			if strings.HasPrefix(line, "%begin ") {
				f := strings.Fields(line)
				if len(f) >= 3 {
					inBlock, num, cur = true, f[2], Reply{}
				}
				continue
			}
			if line == "%exit" {
				return nil
			}
			continue // other notification
		}
		if strings.HasPrefix(line, "%end ") || strings.HasPrefix(line, "%error ") {
			f := strings.Fields(line)
			if len(f) >= 3 && f[2] == num {
				cur.Err = f[0] == "%error"
				out <- cur
				inBlock = false
				continue
			}
		}
		if strings.HasPrefix(line, "%") && isNotification(line) {
			continue
		}
		cur.Lines = append(cur.Lines, line)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if inBlock {
		return fmt.Errorf("control stream ended inside block %s: %w", num, io.ErrUnexpectedEOF)
	}
	return nil
}

// isNotification checks if a line is a tmux control-mode notification.
// Matches the first %-prefixed token against the whitelist.
func isNotification(line string) bool {
	// Extract the first token (everything up to the first space or end-of-line).
	var token string
	if idx := strings.IndexByte(line, ' '); idx != -1 {
		token = line[:idx]
	} else {
		token = line
	}
	return tmuxNotifications[token]
}
