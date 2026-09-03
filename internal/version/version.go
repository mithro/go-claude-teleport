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
//
// Bumped to 3 by Plan 03's orchestration work (R-P3-28c). Every one of
// these is a shape a protocol-2 peer either cannot produce or would
// silently mis-handle:
//   - claudecfg.Inventory: Hooks (the decoded hook config) became
//     HooksHash (a sha256), for both the top-level settings and each
//     plugin — a 2 peer sends a field a 3 peer no longer reads, and the
//     comparison would report drift on every pair.
//   - two new ops: delete-installed and remove-job (abandon's
//     destination-side cleanup); a 2 peer answers "unknown op".
//   - transfer.InstallReport gained InstalledIDs, which is what
//     abandon --delete-destination-files deletes by; from a 2 peer it
//     arrives empty and nothing is cleaned up.
//   - ThawArgs gained Ref, the pane whose foreground the thaw restores;
//     a 2 peer ignores it and leaves Claude stopped on SIGTTIN.
//   - orchestrate.Plan gained its Plan-03 fields (JobID, CaptureEntryID,
//     InstalledIDs, Git/Tmux sub-plans, Options), which travel in the
//     journal a peer reads back.
//   - transfer.Entry gained Deferred, which changes how the destination
//     classifies an entry; a 2 peer drops it and would refuse the
//     existing-main git entries as collisions.
const Protocol = 3
