package remote

// Plan 03 REPLACES this file with
//   type GitInfo = gitx.Info; type GitDestState = gitx.DestState; type GitPlan = gitx.Plan
//   type TmuxFacts = tmuxx.Facts; type TmuxPlan = tmuxx.Plan; type TmuxPaneState = tmuxx.PaneState
//   type TmuxDialer = tmuxx.Dialer
// Until then the Endpoint round-trips these values as opaque JSON so the
// protocol, Client and Server are complete and testable without git/tmux.
//
// Replacing these json.RawMessage aliases with real structs changes the
// wire format (git/tmux payloads stop being opaque blobs and gain a
// concrete shape a mismatched peer can no longer just pass through). Plan
// 03 MUST bump version.Protocol (internal/version/version.go) to 2 when it
// makes this replacement, so an old and a new binary refuse to talk to each
// other instead of silently misinterpreting these fields.

import "encoding/json"

type GitInfo = json.RawMessage
type GitDestState = json.RawMessage
type GitPlan = json.RawMessage
type TmuxFacts = json.RawMessage
type TmuxPlan = json.RawMessage
type TmuxPaneState = json.RawMessage

// TmuxDialer is nil in this plan (tmux unavailable).
type TmuxDialer = any
