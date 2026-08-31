package remote

// Plan 03 REPLACES this file with
//   type GitInfo = gitx.Info; type GitDestState = gitx.DestState; type GitPlan = gitx.Plan
//   type TmuxFacts = tmuxx.Facts; type TmuxPlan = tmuxx.Plan; type TmuxPaneState = tmuxx.PaneState
//   type TmuxDialer = tmuxx.Dialer
// Until then the Endpoint round-trips these values as opaque JSON so the
// protocol, Client and Server are complete and testable without git/tmux.

import "encoding/json"

type GitInfo = json.RawMessage
type GitDestState = json.RawMessage
type GitPlan = json.RawMessage
type TmuxFacts = json.RawMessage
type TmuxPlan = json.RawMessage
type TmuxPaneState = json.RawMessage

// TmuxDialer is nil in this plan (tmux unavailable).
type TmuxDialer = any
