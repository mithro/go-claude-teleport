// Package version holds the build version (set by -ldflags) and the remote
// helper protocol version.
package version

// Version is "dev" for local builds; the release workflow sets it to the
// vX.Y tag with -ldflags "-X github.com/mithro/go-claude-teleport/internal/version.Version=vX.Y".
var Version = "dev"

// Protocol is the remote helper protocol version (spec §4.3).
const Protocol = 1
