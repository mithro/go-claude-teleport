// internal/orchestrate/fixture_test.go
package orchestrate

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

// host is one side of an in-process teleport: its own home, config dir,
// data dir and a Local endpoint. Tmux is either nil (no tmux) or a fake.
type host struct {
	name  string
	paths session.Paths
	opts  remote.LocalOptions
	ep    *remote.Local
	tmux  *fakeTmux
}

func newHost(t *testing.T, name, user string, tm *fakeTmux) *host {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home", user)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	p := session.Paths{Home: home, ConfigDir: filepath.Join(home, ".claude"), GlobalJSON: filepath.Join(home, ".claude.json"), DataDir: filepath.Join(home, ".local", "share", "claude-teleport")}
	os.MkdirAll(p.ConfigDir, 0o700)
	opts := remote.LocalOptions{ProcRoot: "/proc", TmuxSocketDir: filepath.Join(home, "tmux-sockets"), Logf: t.Logf}
	if tm != nil {
		tm.env = func(paneID, sess, win string) []string {
			return []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + p.ConfigDir, "TMUX_PANE=" + paneID, "TMUX=" + tm.socket + ",1,0", "FAKECLAUDE_TMUX=" + sess + ":" + win + "." + paneID, "PATH=" + os.Getenv("PATH"), "XDG_DATA_HOME=" + filepath.Join(home, ".local", "share")}
		}
		opts.Tmux = func(context.Context, string) (tmuxx.Transport, error) { return tm, nil }
		os.MkdirAll(opts.TmuxSocketDir, 0o700)
		tm.socket = filepath.Join(opts.TmuxSocketDir, "default")
		os.WriteFile(tm.socket, nil, 0o600) // FindServer lists it; Dial is the fake above
		t.Cleanup(tm.killAll)
	}
	h := &host{name: name, paths: p, opts: opts, tmux: tm}
	h.ep = remote.NewLocal(p, selfExe(t), opts)
	return h
}

// refreshProbe rebuilds the endpoint with a pane probe over a fresh
// process-table snapshot; call it after starting Claude in a pane and
// before ResolveSession.
func (h *host) refreshProbe(t *testing.T) {
	t.Helper()
	procs, err := procx.Scan("/proc")
	if err != nil {
		t.Fatal(err)
	}
	o := h.opts
	o.Probe = tmuxx.Prober(context.Background(), h.tmux, procs, h.tmux.socket)
	h.ep = remote.NewLocal(h.paths, selfExe(t), o)
}

var builtExe string

// selfExe builds cmd/claude-teleport once per test binary and puts it and
// test/fakeclaude (as `claude`) on PATH.
func selfExe(t *testing.T) string {
	t.Helper()
	if builtExe != "" {
		return builtExe
	}
	dir, err := os.MkdirTemp("", "claude-teleport-test-bin")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range [][2]string{{"claude-teleport", "github.com/mithro/go-claude-teleport/cmd/claude-teleport"}, {"claude", "github.com/mithro/go-claude-teleport/test/fakeclaude"}} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(dir, b[0]), b[1])
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", b[0], err, out)
		}
	}
	os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	builtExe = filepath.Join(dir, "claude-teleport")
	return builtExe
}

// seedSession creates a transcript for sid in cwd on h with one user turn
// via `claude -p --session-id`, so --resume works later.
func seedSession(t *testing.T, h *host, cwd string) {
	t.Helper()
	os.MkdirAll(cwd, 0o755)
	cmd := exec.Command("claude", "-p", "--session-id", sid, "remember the word pineapple")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "HOME="+h.paths.Home, "CLAUDE_CONFIG_DIR="+h.paths.ConfigDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
}

// startClaudeInPane opens a window in h's fake tmux and resumes sid there;
// returns once the registry reports the session idle.
func startClaudeInPane(t *testing.T, h *host, group, cwd string) *session.TmuxRef {
	t.Helper()
	ctx := context.Background()
	ref, err := tmuxx.OpenWindow(ctx, h.tmux, &tmuxx.Plan{SocketPath: h.tmux.socket, Group: group, WindowName: "claude", AutoRename: false, Cwd: cwd, CreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := tmuxx.TypeCommand(ctx, h.tmux, ref.PaneID, []string{"claude", "--resume", sid}); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, h, "idle")
	h.refreshProbe(t)
	return ref
}

func waitRegistry(t *testing.T, h *host, status string) *session.Registry {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		reg, ok, err := h.ep.ClaudeStatus(context.Background(), session.ID(sid))
		if err != nil {
			t.Fatal(err)
		}
		if ok && (status == "" || reg.Status == status) {
			return reg
		}
		if time.Now().After(deadline) {
			t.Fatalf("session never reached status %q on %s", status, h.name)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.MarshalIndent(v, "", "  ")
	os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
