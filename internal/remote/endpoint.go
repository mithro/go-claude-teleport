package remote

import (
	"context"
	"io"
	"time"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// Endpoint is every operation the orchestrator performs on a host.
// Local implements it directly; Client implements it over the protocol;
// Server dispatches protocol requests to a Local.
type Endpoint interface {
	Hello(ctx context.Context) (HostInfo, error)
	Paths() session.Paths

	// inventories
	ResolveSession(ctx context.Context, sel session.Selector) (*session.Session, error)
	InventorySession(ctx context.Context, id session.ID) (*session.Inventory, *session.Usage, error)
	InventoryHost(ctx context.Context, cwd, claudeVersion string) (*claudecfg.Inventory, error)
	InventoryGit(ctx context.Context, cwd string) (*GitInfo, error)
	GitDestState(ctx context.Context, mainDir, worktreeDir, branch string) (*GitDestState, error)
	InventoryTmux(ctx context.Context, ref *session.TmuxRef, preferredSocket string) (*TmuxFacts, error)

	// transfer
	ManifestDiff(ctx context.Context, m *transfer.Manifest, jobID string) (map[int]transfer.Status, error)
	OpenStream(ctx context.Context, kind StreamKind, jobID, streamID string) (io.ReadWriteCloser, error)
	Install(ctx context.Context, m *transfer.Manifest, jobID string) (*transfer.InstallReport, error)
	GitAttach(ctx context.Context, plan *GitPlan, jobID string) error

	// processes and panes
	Freeze(ctx context.Context, pid int, startTime string) error
	Thaw(ctx context.Context, pid int) error
	Capture(ctx context.Context, ref *session.TmuxRef, jobID string) error
	OpenWindow(ctx context.Context, p *TmuxPlan) (*session.TmuxRef, error)
	StartClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, jobID string, argv []string) error
	ConfirmClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, timeout time.Duration) (*session.Registry, error)
	ExitClaude(ctx context.Context, ref *session.TmuxRef, pid int, startTime string, timeout time.Duration) error
	TypeCommand(ctx context.Context, ref *session.TmuxRef, argv []string) error
	PaneState(ctx context.Context, ref *session.TmuxRef) (*TmuxPaneState, error)
	RunPtyResume(ctx context.Context, id session.ID, cwd string, timeout time.Duration) error

	// journal
	JournalGet(ctx context.Context, jobID string) (*job.Journal, bool, error)
	JournalPut(ctx context.Context, j *job.Journal) error
	Record(ctx context.Context, jobID string, rec job.HistoryRecord) error
}
