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
// dispatch. Every op here is registered in plan03Ops below; Client methods
// call the same names.
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
	tmuxSessionsResult struct {
		Sessions []tmuxx.SessionInfo `json:"sessions"`
	}
)

// localHandler decodes args, runs the op on the Local, returns a result.
type localHandler func(ctx context.Context, l *Local, args json.RawMessage) (any, error)

// plan03Ops is merged into Server's dispatch table (see handle in server.go).
// It holds ONLY ops Plan 02's `dispatch` table does not know: Plan 02 already
// dispatches inventory-git, git-dest-state, git-attach, inventory-tmux,
// tmux-open, tmux-capture, tmux-keys, shape-state (PaneState), claude-start,
// claude-confirm, claude-exit and claude-pty-resume through Endpoint, and its
// Client already implements those twelve (InventoryGit, GitDestState,
// GitAttach, InventoryTmux, OpenWindow, Capture, StartClaude, ConfirmClaude,
// ExitClaude, TypeCommand, PaneState, RunPtyResume) — this file must not
// redeclare any of them.
var plan03Ops = map[string]localHandler{
	"git-files": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[gitFilesArgs](a)
		if err != nil {
			return nil, err
		}
		files, err := l.GitFiles(ctx, v.Plan, v.Excludes, v.IncludeIgnored)
		return filesResult{Files: files}, err
	},
	"git-source-facts": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[gitSourceFactsArgs](a)
		if err != nil {
			return nil, err
		}
		return l.GitSourceFacts(ctx, v.MainDir, v.IndexRel, v.Tip, v.DestTip)
	},
	"tmux-sessions": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[tmuxSessionsArgs](a)
		if err != nil {
			return nil, err
		}
		s, err := l.TmuxSessions(ctx, v.SocketPath)
		return tmuxSessionsResult{Sessions: s}, err
	},
	"tmux-kill": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[killWindowArgs](a)
		if err != nil {
			return nil, err
		}
		return Empty{}, l.KillWindow(ctx, v.Ref)
	},
	"claude-status": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[claudeStatusArgs](a)
		if err != nil {
			return nil, err
		}
		r, ok, err := l.ClaudeStatus(ctx, v.ID)
		return claudeStatusResult{Registry: r, OK: ok}, err
	},
	"build-manifest": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[buildManifestArgs](a)
		if err != nil {
			return nil, err
		}
		return l.BuildManifest(ctx, v.JobID, v.ID, v.SrcHost, v.DstHost, v.Files, v.PathMap)
	},
	"session-extras": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[sessionExtrasArgs](a)
		if err != nil {
			return nil, err
		}
		ex, err := l.SessionExtras(ctx, v.ID, v.PathMap)
		return extrasResult{Extras: ex}, err
	},
	"cleanup": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[cleanupArgs](a)
		if err != nil {
			return nil, err
		}
		return Empty{}, l.Cleanup(ctx, v.JobID)
	},
	"list-sessions": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		s, err := l.ListSessions(ctx)
		return sessionsResult{Sessions: s}, err
	},
}

// ---- Client side -------------------------------------------------------

func (c *Client) GitFiles(ctx context.Context, p *gitx.Plan, excludes []string, includeIgnored bool) ([]session.FileEntry, error) {
	var out filesResult
	if err := c.call(ctx, "git-files", gitFilesArgs{p, excludes, includeIgnored}, &out); err != nil {
		return nil, err
	}
	return out.Files, nil
}

func (c *Client) GitSourceFacts(ctx context.Context, mainDir, indexRel, tip, destTip string) (*gitx.SourceFacts, error) {
	var out gitx.SourceFacts
	if err := c.call(ctx, "git-source-facts", gitSourceFactsArgs{mainDir, indexRel, tip, destTip}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) TmuxSessions(ctx context.Context, socketPath string) ([]tmuxx.SessionInfo, error) {
	var out tmuxSessionsResult
	if err := c.call(ctx, "tmux-sessions", tmuxSessionsArgs{socketPath}, &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

func (c *Client) KillWindow(ctx context.Context, ref *session.TmuxRef) error {
	return c.call(ctx, "tmux-kill", killWindowArgs{ref}, nil)
}

func (c *Client) ClaudeStatus(ctx context.Context, id session.ID) (*session.Registry, bool, error) {
	var out claudeStatusResult
	if err := c.call(ctx, "claude-status", claudeStatusArgs{id}, &out); err != nil {
		return nil, false, err
	}
	return out.Registry, out.OK, nil
}

func (c *Client) BuildManifest(ctx context.Context, jobID string, id session.ID, srcHost, dstHost string, files []session.FileEntry, pm session.PathMap) (*transfer.Manifest, error) {
	var out transfer.Manifest
	if err := c.call(ctx, "build-manifest", buildManifestArgs{jobID, id, srcHost, dstHost, files, pm}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SessionExtras(ctx context.Context, id session.ID, pm session.PathMap) (*transfer.InstallExtras, error) {
	var out extrasResult
	if err := c.call(ctx, "session-extras", sessionExtrasArgs{id, pm}, &out); err != nil {
		return nil, err
	}
	return out.Extras, nil
}

func (c *Client) Cleanup(ctx context.Context, jobID string) error {
	return c.call(ctx, "cleanup", cleanupArgs{jobID}, nil)
}

func (c *Client) ListSessions(ctx context.Context) ([]SessionSummary, error) {
	var out sessionsResult
	if err := c.call(ctx, "list-sessions", struct{}{}, &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}
