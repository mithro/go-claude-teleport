// Package orchestrate is the teleport state machine (spec §6).
package orchestrate

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

type Options struct {
	Direction      string            `json:"direction"` // "to" | "from"
	Selector       session.Selector  `json:"selector"`
	DestPath       string            `json:"dest_path"`
	Maps           []session.Mapping `json:"maps"`
	State          string            `json:"state"` // auto|running|suspended|idle
	AllowDrift     bool              `json:"allow_drift"`
	Force          bool              `json:"force"`
	TmuxSocket     string            `json:"tmux_socket"`
	NoTmux         bool              `json:"no_tmux"`
	Excludes       []string          `json:"excludes"`
	IncludeIgnored bool              `json:"include_ignored"`
	ExitTimeout    time.Duration     `json:"exit_timeout"`
	StartTimeout   time.Duration     `json:"start_timeout"`
	BangMode       bool              `json:"bang_mode"` // running inside the session ($CLAUDE_PID == source pid)

	// Additions (Plan 03): what the runner needs to re-dial the remote.
	Target     string            `json:"target"` // [user@]host[:port] of the remote endpoint
	Via        []string          `json:"via"`
	SSHOptions map[string]string `json:"ssh_options"`

	// LocalDest, when set, makes the destination a second in-process Local
	// endpoint with these paths instead of an ssh client (tests only; no
	// flag exposes it).
	LocalDest *session.Paths `json:"local_dest,omitempty"`
}

// Plan is the immutable outcome of preflight plus the few facts later
// steps record (DestRef, CreatedWindow, DestRegistry, CaptureEntryID).
// JSON tags are load-bearing: remote.planView reads "statuses", "git",
// "extras" straight from the journal.
type Plan struct {
	Options      Options          `json:"options"`
	Session      *session.Session `json:"session"`
	SourceInfo   remote.HostInfo  `json:"source_info"`
	DestInfo     remote.HostInfo  `json:"dest_info"`
	PathMap      session.PathMap  `json:"path_map"`
	Git          *gitx.Plan       `json:"git"`
	Tmux         *tmuxx.Plan      `json:"tmux"` // nil = no tmux on dest
	TargetState  string           `json:"target_state"`
	Drift        claudecfg.Report `json:"drift"`
	ManifestPath string           `json:"manifest_path"`
	Collisions   []transfer.Entry `json:"collisions"`

	// Additions (Plan 03):
	JobID       string                  `json:"job_id"`
	SourceFacts *tmuxx.Facts            `json:"source_facts"` // nil when the source has no pane
	Files       []session.FileEntry     `json:"files"`        // everything the manifest is built from (rebuilt with the capture at step 3)
	Statuses    map[int]transfer.Status `json:"statuses"`
	// InstalledIDs is the set of manifest ids the install step (spec §6
	// step 5) actually placed on the destination (transfer.InstallReport's
	// StagedSame/FFCandidate entries) — recorded once there and never
	// recomputed: Statuses is overwritten by later manifest-diffs (capture,
	// verifyTransfer, runTransfer all re-diff and persist a fresh map), so
	// by the time a job finishes every entry reads back PresentSame and
	// Statuses can no longer answer "what did THIS job install" (ruling
	// R-P3-23a). abandon --delete-destination-files reads this field
	// directly instead of deriving a candidate set from Statuses.
	InstalledIDs   []int                   `json:"installed_ids"`
	Extras         *transfer.InstallExtras `json:"extras"`
	CaptureEntryID int                     `json:"capture_entry_id"`
	DestCwd        string                  `json:"dest_cwd"`
	DestCapture    string                  `json:"dest_capture"` // capture.txt path on the destination
	DestRef        *session.TmuxRef        `json:"dest_ref"`
	CreatedSession bool                    `json:"created_session"`
	CreatedWindow  bool                    `json:"created_window"`
	DestRegistry   *session.Registry       `json:"dest_registry"`
	StartedAt      time.Time               `json:"started_at"`
	// RecordedSrc/RecordedDst say whether each host has already appended
	// this job's history row (spec §6 step 10). The row is appended, not
	// merged, and the record step re-runs whenever anything after those
	// calls fails, so without these a resumed job grew a duplicate row on
	// every pass (finding A8).
	RecordedSrc bool `json:"recorded_src"`
	RecordedDst bool `json:"recorded_dst"`
}

func (p *Plan) ToJSON() (json.RawMessage, error) { return json.Marshal(p) }

// PlanFromJournal decodes the plan a journal carries.
func PlanFromJournal(j *job.Journal) (*Plan, error) {
	var p Plan
	if err := json.Unmarshal(j.Plan, &p); err != nil {
		return nil, fmt.Errorf("decode plan of job %s: %w", j.ID, err)
	}
	return &p, nil
}

// RefusedError is a preflight refusal (exit 3): nothing was touched.
type RefusedError struct{ Reason string }

func (e *RefusedError) Error() string { return "refused: " + e.Reason }

func refusef(format string, a ...any) error { return &RefusedError{Reason: fmt.Sprintf(format, a...)} }

// UnreachableError covers dial failures and version mismatch (exit 4).
type UnreachableError struct {
	Host string
	Err  error
}

func (e *UnreachableError) Error() string { return fmt.Sprintf("%s: %v", e.Host, e.Err) }
func (e *UnreachableError) Unwrap() error { return e.Err }

// sourceState is a nil-safe accessor used by the steps.
func (p *Plan) sourceState() session.State {
	if p.Session == nil {
		return session.StateIdle
	}
	return p.Session.State
}
