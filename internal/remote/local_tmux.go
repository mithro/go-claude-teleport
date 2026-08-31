package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

// dial opens a control-mode transport; callers must Close it.
func (l *Local) dial(ctx context.Context, socketPath string) (tmuxx.Transport, error) {
	if l.opts.Tmux == nil {
		return nil, &Error{Code: "unavailable", Message: "tmux is not available on this host"}
	}
	t, err := l.opts.Tmux(ctx, socketPath)
	if errors.Is(err, tmuxx.ErrNoServer) {
		return nil, &Error{Code: "unavailable", Message: err.Error()}
	}
	if err != nil {
		return nil, &Error{Code: "internal", Message: fmt.Sprintf("tmux dial %s: %v", socketPath, err)}
	}
	return t, nil
}

// InventoryTmux describes the pane in ref, or — with ref == nil — only
// discovers the server (spec §9) and returns Facts with SocketPath set.
func (l *Local) InventoryTmux(ctx context.Context, ref *session.TmuxRef, preferredSocket string) (*tmuxx.Facts, error) {
	if l.opts.Tmux == nil {
		return nil, &Error{Code: "unavailable", Message: "tmux is not available on this host"}
	}
	if ref == nil {
		sock, err := tmuxx.FindServer(l.opts.TmuxSocketDir, preferredSocket, "")
		if err != nil {
			return nil, &Error{Code: "unavailable", Message: err.Error()}
		}
		return &tmuxx.Facts{SocketPath: sock}, nil
	}
	t, err := l.dial(ctx, ref.SocketPath)
	if err != nil {
		return nil, err
	}
	defer t.Close()
	facts, err := tmuxx.Describe(ctx, t, ref.PaneID)
	if err != nil {
		return nil, &Error{Code: "not-found", Message: err.Error()}
	}
	facts.SocketPath = ref.SocketPath
	return facts, nil
}

func (l *Local) TmuxSessions(ctx context.Context, socketPath string) ([]tmuxx.SessionInfo, error) {
	t, err := l.dial(ctx, socketPath)
	if err != nil {
		return nil, err
	}
	defer t.Close()
	return tmuxx.ListSessions(ctx, t)
}

func (l *Local) OpenWindow(ctx context.Context, p *tmuxx.Plan) (*session.TmuxRef, error) {
	t, err := l.dial(ctx, p.SocketPath)
	if err != nil {
		return nil, err
	}
	defer t.Close()
	ref, err := tmuxx.OpenWindow(ctx, t, p)
	if err != nil {
		return nil, &Error{Code: "internal", Message: err.Error()}
	}
	return ref, nil
}

// Capture writes jobs/<jobID>/capture.txt on this host.
func (l *Local) Capture(ctx context.Context, ref *session.TmuxRef, jobID string) error {
	t, err := l.dial(ctx, ref.SocketPath)
	if err != nil {
		return err
	}
	defer t.Close()
	data, err := tmuxx.Capture(ctx, t, ref.PaneID)
	if err != nil {
		return &Error{Code: "internal", Message: err.Error()}
	}
	dir := job.Dir(l.paths.DataDir, jobID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "capture.txt.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "capture.txt"))
}

func (l *Local) TypeCommand(ctx context.Context, ref *session.TmuxRef, argv []string) error {
	if ref == nil {
		return &Error{Code: "usage", Message: "tmux-keys: nil pane ref"}
	}
	t, err := l.dial(ctx, ref.SocketPath)
	if err != nil {
		return err
	}
	defer t.Close()
	return tmuxx.TypeCommand(ctx, t, ref.PaneID, argv)
}

func (l *Local) PaneState(ctx context.Context, ref *session.TmuxRef) (*tmuxx.PaneState, error) {
	t, err := l.dial(ctx, ref.SocketPath)
	if err != nil {
		return nil, err
	}
	defer t.Close()
	procs, err := l.procs()
	if err != nil {
		return nil, err
	}
	st, err := tmuxx.State(ctx, t, ref.PaneID, procs)
	if err != nil {
		return nil, &Error{Code: "not-found", Message: err.Error()}
	}
	return st, nil
}

func (l *Local) KillWindow(ctx context.Context, ref *session.TmuxRef) error {
	t, err := l.dial(ctx, ref.SocketPath)
	if err != nil {
		return err
	}
	defer t.Close()
	return tmuxx.KillWindow(ctx, t, ref.WindowID)
}
