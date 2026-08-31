// Package version holds the build version (set by -ldflags) and the remote
// helper protocol version.
package version

// Version is "dev" for local builds; the release workflow sets it to the
// vX.Y tag with -ldflags "-X github.com/mithro/go-claude-teleport/internal/version.Version=vX.Y".
var Version = "dev"

// Protocol is the remote helper protocol version (spec §4.3).
//
// Bumped to 2 by Plan 03 Task 13: internal/remote's git/tmux payloads
// stopped being opaque json.RawMessage blobs and gained a concrete shape
// (gitx.Info, gitx.DestState, gitx.Plan, tmuxx.Facts, tmuxx.Plan,
// tmuxx.PaneState) that a mismatched peer can no longer just pass through.
const Protocol = 2
