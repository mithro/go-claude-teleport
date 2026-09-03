//go:build tmuxlive

// internal/orchestrate/e2e_live_test.go
package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

// Both hosts share one real tmux server (the pane shells inherit the
// server's environment, so the server is started with the DESTINATION's
// HOME/CLAUDE_CONFIG_DIR and the source pane exports its own). The server
// runs on a throwaway, unique -L socket (tmuxx.StartTestServer) killed in
// t.Cleanup — never the user's own tmux server.
func TestLiveTeleportRunning(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	t.Setenv("HOME", dst.paths.Home)
	t.Setenv("CLAUDE_CONFIG_DIR", dst.paths.ConfigDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dst.paths.Home, ".local", "share"))
	sock, dir := tmuxx.StartTestServer(t)
	for _, h := range []*host{src, dst} {
		h.opts.Tmux, h.opts.TmuxSocketDir = tmuxx.DialControl, dir
		h.ep = remote.NewLocal(h.paths, selfExe(t), h.opts)
	}
	cwd := filepath.Join(src.paths.Home, "proj")
	seedSession(t, src, cwd)
	ctx := context.Background()
	tr, err := tmuxx.DialControl(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	// default-command "exec sh": every pane this test (or the production
	// code under test, for the destination window) opens from here on gets
	// a plain, non-rc-sourcing shell that inherits exactly the server's own
	// environment — this desktop's interactive bash sources ~/.bashrc,
	// which resets PATH and drops the test-built claude/claude-teleport
	// binaries from it (observed directly: a pane run with the default
	// $SHELL could not find `claude` at all). Job control is deliberately
	// left ON (it used to be forced off with "+m"): an interactive shell
	// reclaims the pty from a SIGSTOPped foreground job, and putting the
	// job back in the foreground on thaw is the tool's own job now
	// (remote.Local.restoreForeground) — not something the test may hide.
	if _, err := tr.Run(ctx, `set-option -g default-command "exec sh"`); err != nil {
		t.Fatal(err)
	}
	ref, err := tmuxx.OpenWindow(ctx, tr, &tmuxx.Plan{SocketPath: sock, Group: "work", WindowName: "claude", AutoRename: false, Cwd: cwd, CreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	export := "export HOME=" + src.paths.Home + " CLAUDE_CONFIG_DIR=" + src.paths.ConfigDir + " XDG_DATA_HOME=" + filepath.Join(src.paths.Home, ".local", "share")
	tr.Run(ctx, `send-keys -t `+tmuxx.Quote(ref.PaneID)+` `+tmuxx.Quote(" "+export)+` Enter`)
	tmuxx.TypeCommand(ctx, tr, ref.PaneID, []string{"claude", "--resume", sid})
	waitRegistry(t, src, "idle")
	procs := mustProcs(t)
	o := src.opts
	o.Probe = tmuxx.Prober(ctx, tr, procs, sock)
	src.ep = remote.NewLocal(src.paths, selfExe(t), o)

	p, j := teleport(t, baseOptions(), src, dst)
	if j.Outcome != "success" {
		t.Fatalf("outcome %q", j.Outcome)
	}
	reg := waitRegistry(t, dst, "idle")
	facts, err := tmuxx.Describe(ctx, tr, p.DestRef.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	if facts.SessionName != "work" && facts.Group != "work" || facts.WindowName != "claude" || facts.AutoRename {
		t.Errorf("dest window facts = %+v", facts)
	}
	if reg.Tmux != p.DestRef.Session+":"+p.DestRef.WindowID+"."+p.DestRef.PaneID {
		t.Errorf("dest registry tmux = %q, ref %+v", reg.Tmux, p.DestRef)
	}
	out, _ := tmuxx.Capture(ctx, tr, ref.PaneID)
	// The literal typed text is individually single-quoted per argv token
	// (tmuxx.TypeCommand's use of tmuxx.ShellQuote, so any argument
	// containing shell metacharacters is still typed safely) — build the
	// expected substring the same way, rather than assuming an unquoted
	// "placeholder --resume <sid>" run of text.
	want := tmuxx.ShellQuote([]string{"placeholder", "--resume", sid})
	if !strings.Contains(string(out), want) {
		t.Errorf("source pane should show the typed placeholder (want %q):\n%s", want, out)
	}
	if _, err := os.Stat(filepath.Join(job.Dir(dst.paths.DataDir, sid), "capture.txt")); err != nil {
		t.Error("capture.txt not transferred to the destination job dir")
	}
}

func mustProcs(t *testing.T) *procx.Table {
	t.Helper()
	procs, err := procx.Scan("/proc")
	if err != nil {
		t.Fatal(err)
	}
	return procs
}
