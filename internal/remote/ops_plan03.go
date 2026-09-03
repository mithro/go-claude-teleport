package remote

import (
	"context"
	"encoding/json"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// Argument/result shapes for the Plan 03 ops that Plan 02 does not already
// dispatch. Every op here is registered into dispatch by registerPlan03Ops
// below, under the same Op* constant its Client method calls.
type (
	gitFilesArgs struct {
		Plan           *gitx.Plan `json:"plan"`
		Excludes       []string   `json:"excludes"`
		IncludeIgnored bool       `json:"include_ignored"`
	}
	gitSourceFactsArgs struct {
		MainDir  string `json:"main_dir"`
		IndexRel string `json:"index_rel"`
		Tip      string `json:"tip"`
		DestTip  string `json:"dest_tip"`
	}
	tmuxSessionsArgs struct {
		SocketPath string `json:"socket_path"`
	}
	killWindowArgs struct {
		Ref *session.TmuxRef `json:"ref"`
	}
	claudeStatusArgs struct {
		ID session.ID `json:"id"`
	}
	claudeStatusResult struct {
		Registry *session.Registry `json:"registry"`
		OK       bool              `json:"ok"`
	}
	buildManifestArgs struct {
		JobID   string              `json:"job_id"`
		ID      session.ID          `json:"id"`
		SrcHost string              `json:"src_host"`
		DstHost string              `json:"dst_host"`
		Files   []session.FileEntry `json:"files"`
		PathMap session.PathMap     `json:"path_map"`
	}
	sessionExtrasArgs struct {
		ID      session.ID      `json:"id"`
		PathMap session.PathMap `json:"path_map"`
	}
	extrasResult struct {
		Extras *transfer.InstallExtras `json:"extras"`
	}
	cleanupArgs struct {
		JobID string `json:"job_id"`
	}
	filesResult struct {
		Files []session.FileEntry `json:"files"`
	}
	sessionsResult struct {
		Sessions []SessionSummary `json:"sessions"`
	}
	deleteInstalledArgs struct {
		Manifest *transfer.Manifest `json:"manifest"`
		IDs      []int              `json:"ids"`
	}
	deleteInstalledResult struct {
		Deleted []string `json:"deleted"`
	}
	tmuxSessionsResult struct {
		Sessions []tmuxx.SessionInfo `json:"sessions"`
	}
)

// plan03Ops is merged into Server's single dispatch table (see server.go).
// Every op here runs on the Endpoint interface exactly like the Plan 02 ops
// do (I5): the nine methods are on Endpoint, both Local and Client
// implement them, so a chained/proxied server can serve them and `handle`
// has no type assertion to make. The table holds ONLY ops Plan 02's table
// does not know — Plan 02 already dispatches inventory-git, git-dest-state,
// git-attach, inventory-tmux, tmux-open, tmux-capture, tmux-keys,
// shape-state (PaneState), claude-start, claude-confirm, claude-exit and
// claude-pty-resume, and its Client already implements those twelve, so
// this file must not redeclare any of them (mergeOps panics if it does).
var plan03Ops = map[string]handler{
	OpGitFiles: func(ctx context.Context, ep Endpoint, a json.RawMessage) (any, error) {
		v, err := decode[gitFilesArgs](a)
		if err != nil {
			return nil, err
		}
		files, err := ep.GitFiles(ctx, v.Plan, v.Excludes, v.IncludeIgnored)
		return filesResult{Files: files}, err
	},
	OpGitSourceFacts: func(ctx context.Context, ep Endpoint, a json.RawMessage) (any, error) {
		v, err := decode[gitSourceFactsArgs](a)
		if err != nil {
			return nil, err
		}
		return ep.GitSourceFacts(ctx, v.MainDir, v.IndexRel, v.Tip, v.DestTip)
	},
	OpTmuxSessions: func(ctx context.Context, ep Endpoint, a json.RawMessage) (any, error) {
		v, err := decode[tmuxSessionsArgs](a)
		if err != nil {
			return nil, err
		}
		s, err := ep.TmuxSessions(ctx, v.SocketPath)
		return tmuxSessionsResult{Sessions: s}, err
	},
	OpKillWindow: func(ctx context.Context, ep Endpoint, a json.RawMessage) (any, error) {
		v, err := decode[killWindowArgs](a)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.KillWindow(ctx, v.Ref)
	},
	OpClaudeStatus: func(ctx context.Context, ep Endpoint, a json.RawMessage) (any, error) {
		v, err := decode[claudeStatusArgs](a)
		if err != nil {
			return nil, err
		}
		r, ok, err := ep.ClaudeStatus(ctx, v.ID)
		return claudeStatusResult{Registry: r, OK: ok}, err
	},
	OpBuildManifest: func(ctx context.Context, ep Endpoint, a json.RawMessage) (any, error) {
		v, err := decode[buildManifestArgs](a)
		if err != nil {
			return nil, err
		}
		return ep.BuildManifest(ctx, v.JobID, v.ID, v.SrcHost, v.DstHost, v.Files, v.PathMap)
	},
	OpSessionExtras: func(ctx context.Context, ep Endpoint, a json.RawMessage) (any, error) {
		v, err := decode[sessionExtrasArgs](a)
		if err != nil {
			return nil, err
		}
		ex, err := ep.SessionExtras(ctx, v.ID, v.PathMap)
		return extrasResult{Extras: ex}, err
	},
	OpCleanup: func(ctx context.Context, ep Endpoint, a json.RawMessage) (any, error) {
		v, err := decode[cleanupArgs](a)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.Cleanup(ctx, v.JobID)
	},
	OpListSessions: func(ctx context.Context, ep Endpoint, _ json.RawMessage) (any, error) {
		s, err := ep.ListSessions(ctx)
		return sessionsResult{Sessions: s}, err
	},
	OpDeleteInstalled: func(ctx context.Context, ep Endpoint, a json.RawMessage) (any, error) {
		v, err := decode[deleteInstalledArgs](a)
		if err != nil {
			return nil, err
		}
		deleted, err := ep.DeleteInstalled(ctx, v.Manifest, v.IDs)
		return deleteInstalledResult{Deleted: deleted}, err
	},
}

// ---- Client side -------------------------------------------------------

func (c *Client) GitFiles(ctx context.Context, p *gitx.Plan, excludes []string, includeIgnored bool) ([]session.FileEntry, error) {
	var out filesResult
	if err := c.call(ctx, OpGitFiles, gitFilesArgs{p, excludes, includeIgnored}, &out); err != nil {
		return nil, err
	}
	return out.Files, nil
}

func (c *Client) GitSourceFacts(ctx context.Context, mainDir, indexRel, tip, destTip string) (*gitx.SourceFacts, error) {
	var out gitx.SourceFacts
	if err := c.call(ctx, OpGitSourceFacts, gitSourceFactsArgs{mainDir, indexRel, tip, destTip}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) TmuxSessions(ctx context.Context, socketPath string) ([]tmuxx.SessionInfo, error) {
	var out tmuxSessionsResult
	if err := c.call(ctx, OpTmuxSessions, tmuxSessionsArgs{socketPath}, &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

func (c *Client) KillWindow(ctx context.Context, ref *session.TmuxRef) error {
	return c.call(ctx, OpKillWindow, killWindowArgs{ref}, nil)
}

func (c *Client) ClaudeStatus(ctx context.Context, id session.ID) (*session.Registry, bool, error) {
	var out claudeStatusResult
	if err := c.call(ctx, OpClaudeStatus, claudeStatusArgs{id}, &out); err != nil {
		return nil, false, err
	}
	return out.Registry, out.OK, nil
}

func (c *Client) BuildManifest(ctx context.Context, jobID string, id session.ID, srcHost, dstHost string, files []session.FileEntry, pm session.PathMap) (*transfer.Manifest, error) {
	var out transfer.Manifest
	if err := c.call(ctx, OpBuildManifest, buildManifestArgs{jobID, id, srcHost, dstHost, files, pm}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SessionExtras(ctx context.Context, id session.ID, pm session.PathMap) (*transfer.InstallExtras, error) {
	var out extrasResult
	if err := c.call(ctx, OpSessionExtras, sessionExtrasArgs{id, pm}, &out); err != nil {
		return nil, err
	}
	return out.Extras, nil
}

func (c *Client) Cleanup(ctx context.Context, jobID string) error {
	return c.call(ctx, OpCleanup, cleanupArgs{jobID}, nil)
}

func (c *Client) ListSessions(ctx context.Context) ([]SessionSummary, error) {
	var out sessionsResult
	if err := c.call(ctx, OpListSessions, struct{}{}, &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

func (c *Client) DeleteInstalled(ctx context.Context, m *transfer.Manifest, ids []int) ([]string, error) {
	var out deleteInstalledResult
	if err := c.call(ctx, OpDeleteInstalled, deleteInstalledArgs{Manifest: m, IDs: ids}, &out); err != nil {
		return nil, err
	}
	return out.Deleted, nil
}
