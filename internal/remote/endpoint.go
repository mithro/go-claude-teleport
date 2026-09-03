package remote

import (
	"context"
	"io"
	"time"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
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
	InventoryGit(ctx context.Context, cwd string) (*gitx.Info, error)
	GitDestState(ctx context.Context, mainDir, worktreeDir, branch string) (*gitx.DestState, error)
	GitSourceFacts(ctx context.Context, mainDir, indexRel, tip, destTip string) (*gitx.SourceFacts, error)
	InventoryTmux(ctx context.Context, ref *session.TmuxRef, preferredSocket string) (*tmuxx.Facts, error)
	TmuxSessions(ctx context.Context, socketPath string) ([]tmuxx.SessionInfo, error)
	ListSessions(ctx context.Context) ([]SessionSummary, error)

	// transfer
	GitFiles(ctx context.Context, plan *gitx.Plan, excludes []string, includeIgnored bool) ([]session.FileEntry, error)
	BuildManifest(ctx context.Context, jobID string, id session.ID, srcHost, dstHost string, files []session.FileEntry, pm session.PathMap) (*transfer.Manifest, error)
	SessionExtras(ctx context.Context, id session.ID, pm session.PathMap) (*transfer.InstallExtras, error)
	ManifestDiff(ctx context.Context, m *transfer.Manifest, jobID string) (map[int]transfer.Status, error)
	PutInstallExtras(ctx context.Context, jobID string, extra transfer.InstallExtras) error
	OpenStream(ctx context.Context, kind StreamKind, jobID, streamID string) (io.ReadWriteCloser, error)
	Install(ctx context.Context, m *transfer.Manifest, jobID string) (*transfer.InstallReport, error)
	GitAttach(ctx context.Context, plan *gitx.Plan, jobID string) error
	Cleanup(ctx context.Context, jobID string) error
	// DeleteInstalled removes the manifest entries named by ids from this
	// host when their current content still matches the manifest hash
	// (spec §7.4/abandon --delete-destination-files); see
	// transfer.UninstallIDs for the containment and hash-check semantics.
	DeleteInstalled(ctx context.Context, m *transfer.Manifest, ids []int) ([]string, error)
	// RemoveJob removes jobs/<jobID>/ (manifest.json, extras.json, ...)
	// entirely — unlike Cleanup, which only removes staging. Ruling
	// R-P3-23i: this exists so inspect --host's throwaway job dir does
	// not linger on the destination forever; the wire dispatch handler
	// (not Local) refuses any jobID not prefixed "inspect-", so a real
	// job's directory can never be removed this way.
	RemoveJob(ctx context.Context, jobID string) error

	// processes and panes
	Freeze(ctx context.Context, pid int, startTime string) error
	Thaw(ctx context.Context, pid int, ref *session.TmuxRef) error
	Capture(ctx context.Context, ref *session.TmuxRef, jobID string) error
	OpenWindow(ctx context.Context, p *tmuxx.Plan) (*session.TmuxRef, error)
	KillWindow(ctx context.Context, ref *session.TmuxRef) error
	StartClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, jobID string, argv []string) error
	ClaudeStatus(ctx context.Context, id session.ID) (*session.Registry, bool, error)
	ConfirmClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, timeout time.Duration) (*session.Registry, error)
	ExitClaude(ctx context.Context, ref *session.TmuxRef, pid int, startTime string, timeout time.Duration) error
	TypeCommand(ctx context.Context, ref *session.TmuxRef, argv []string) error
	PaneState(ctx context.Context, ref *session.TmuxRef) (*tmuxx.PaneState, error)
	RunPtyResume(ctx context.Context, id session.ID, cwd string, timeout time.Duration) error

	// journal
	JournalGet(ctx context.Context, jobID string) (*job.Journal, bool, error)
	JournalPut(ctx context.Context, j *job.Journal) error
	Record(ctx context.Context, jobID string, rec job.HistoryRecord) error
}
