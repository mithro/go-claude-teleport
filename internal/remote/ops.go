package remote

import (
	"time"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// Op names (spec §4.3). hello is always first.
const (
	OpHello            = "hello"
	OpPaths            = "paths"
	OpResolveSession   = "resolve-session"
	OpInventorySession = "inventory-session"
	OpInventoryHost    = "inventory-host"
	OpInventoryGit     = "inventory-git"
	OpGitDestState     = "git-dest-state"
	OpInventoryTmux    = "inventory-tmux"
	OpManifestDiff     = "manifest-diff"
	OpInstallExtras    = "install-extras"
	OpInstall          = "install"
	OpGitAttach        = "git-attach"
	OpFreeze           = "freeze"
	OpThaw             = "thaw"
	OpCapture          = "tmux-capture"
	OpOpenWindow       = "tmux-open"
	OpStartClaude      = "claude-start"
	OpConfirmClaude    = "claude-confirm"
	OpExitClaude       = "claude-exit"
	OpTypeCommand      = "tmux-keys"
	OpPaneState        = "shape-state"
	OpRunPtyResume     = "claude-pty-resume"
	OpJournalGet       = "job-journal-get"
	OpJournalPut       = "job-journal-put"
	OpRecord           = "record"
)

type HelloArgs struct {
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}
type PathsResult struct {
	Paths session.Paths `json:"paths"`
}
type ResolveSessionArgs struct {
	Selector session.Selector `json:"selector"`
}
type ResolveSessionResult struct {
	Session *session.Session `json:"session"`
}
type InventorySessionArgs struct {
	ID session.ID `json:"id"`
}
type InventorySessionResult struct {
	Inventory *session.Inventory `json:"inventory"`
	Usage     *session.Usage     `json:"usage"`
}
type InventoryHostArgs struct {
	Cwd           string `json:"cwd"`
	ClaudeVersion string `json:"claude_version"`
}
type InventoryHostResult struct {
	Inventory *claudecfg.Inventory `json:"inventory"`
}
type InventoryGitArgs struct {
	Cwd string `json:"cwd"`
}
type InventoryGitResult struct {
	Info *GitInfo `json:"info"`
}
type GitDestStateArgs struct {
	MainDir     string `json:"main_dir"`
	WorktreeDir string `json:"worktree_dir"`
	Branch      string `json:"branch"`
}
type GitDestStateResult struct {
	State *GitDestState `json:"state"`
}
type InventoryTmuxArgs struct {
	Ref             *session.TmuxRef `json:"ref"`
	PreferredSocket string           `json:"preferred_socket"`
}
type InventoryTmuxResult struct {
	Facts *TmuxFacts `json:"facts"`
}
type ManifestDiffArgs struct {
	Manifest *transfer.Manifest `json:"manifest"`
	JobID    string             `json:"job_id"`
}
type ManifestDiffResult struct {
	Statuses map[int]transfer.Status `json:"statuses"`
}
type InstallExtrasArgs struct {
	JobID string                 `json:"job_id"`
	Extra transfer.InstallExtras `json:"extra"`
}
type InstallArgs struct {
	Manifest *transfer.Manifest `json:"manifest"`
	JobID    string             `json:"job_id"`
}
type InstallResult struct {
	Report *transfer.InstallReport `json:"report"`
}
type GitAttachArgs struct {
	Plan  *GitPlan `json:"plan"`
	JobID string   `json:"job_id"`
}
type FreezeArgs struct {
	PID       int    `json:"pid"`
	StartTime string `json:"start_time"`
}
type ThawArgs struct {
	PID int `json:"pid"`
}
type CaptureArgs struct {
	Ref   *session.TmuxRef `json:"ref"`
	JobID string           `json:"job_id"`
}
type OpenWindowArgs struct {
	Plan *TmuxPlan `json:"plan"`
}
type OpenWindowResult struct {
	Ref *session.TmuxRef `json:"ref"`
}
type StartClaudeArgs struct {
	Ref   *session.TmuxRef `json:"ref"`
	ID    session.ID       `json:"id"`
	JobID string           `json:"job_id"`
	Argv  []string         `json:"argv"`
}
type ConfirmClaudeArgs struct {
	Ref     *session.TmuxRef `json:"ref"`
	ID      session.ID       `json:"id"`
	Timeout time.Duration    `json:"timeout"`
}
type ConfirmClaudeResult struct {
	Registry *session.Registry `json:"registry"`
}
type ExitClaudeArgs struct {
	Ref       *session.TmuxRef `json:"ref"`
	PID       int              `json:"pid"`
	StartTime string           `json:"start_time"`
	Timeout   time.Duration    `json:"timeout"`
}
type TypeCommandArgs struct {
	Ref  *session.TmuxRef `json:"ref"`
	Argv []string         `json:"argv"`
}
type PaneStateArgs struct {
	Ref *session.TmuxRef `json:"ref"`
}
type PaneStateResult struct {
	State *TmuxPaneState `json:"state"`
}
type RunPtyResumeArgs struct {
	ID      session.ID    `json:"id"`
	Cwd     string        `json:"cwd"`
	Timeout time.Duration `json:"timeout"`
}
type JournalGetArgs struct {
	JobID string `json:"job_id"`
}
type JournalGetResult struct {
	Journal *job.Journal `json:"journal"`
	Found   bool         `json:"found"`
}
type JournalPutArgs struct {
	Journal *job.Journal `json:"journal"`
}
type RecordArgs struct {
	JobID  string            `json:"job_id"`
	Record job.HistoryRecord `json:"record"`
}

// Empty is the result of ops that return nothing.
type Empty struct{}
