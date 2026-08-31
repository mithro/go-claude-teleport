// Package remote is the JSON-over-ssh helper protocol (spec §4.3): an
// Endpoint interface, a Local implementation, a Server that dispatches
// requests to it, and a Client that speaks to a remote Server.
package remote

import (
	"encoding/json"
	"fmt"
)

type HostInfo struct {
	Version          string `json:"version"` // claude-teleport version
	Protocol         int    `json:"protocol"`
	Hostname         string `json:"hostname"`
	OS               string `json:"os"`
	Arch             string `json:"arch"`
	UID              int    `json:"uid"`
	Home             string `json:"home"`
	ConfigDir        string `json:"config_dir"`
	DataDir          string `json:"data_dir"`
	TmuxSocketDir    string `json:"tmux_socket_dir"`
	HasTmux          bool   `json:"has_tmux"`
	HasClaude        bool   `json:"has_claude"`
	ClaudeVersion    string `json:"claude_version"`
	ClaudeVersionErr string `json:"claude_version_err,omitempty"` // `claude --version` failed; message, not swallowed
	HasClaudeResume  bool   `json:"has_claude_resume"`            // go-tmux-saver's claude-resume on PATH
}

// StreamKind names the bulk channels.
type StreamKind string

const (
	StreamTar     StreamKind = "tar"     // driver -> dest: transfer stream
	StreamCapture StreamKind = "capture" // source -> driver: pane capture
	StreamPack    StreamKind = "pack"    // source -> driver: git packfile
	StreamLog     StreamKind = "log"     // remote job log tail
)

type Request struct {
	ID   int             `json:"id"`
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args"`
}

type Response struct {
	ID     int             `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error codes: "usage" | "not-found" | "conflict" | "drift" | "unavailable" | "internal".
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// Unavailable is the explicit stub error for ops implemented in Plan 03.
func Unavailable(op string) *Error {
	return &Error{Code: "unavailable", Message: op + ": implemented in Plan 03"}
}
