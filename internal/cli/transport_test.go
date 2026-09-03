package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/orchestrate"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
	"github.com/mithro/go-claude-teleport/internal/version"
)

const tsid = "3d2c1b0a-9f8e-4d7c-b6a5-4f3e2d1c0b9a"

func testEnv(t *testing.T) ([]string, string) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home", "bob")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	return []string{"HOME=" + home, "PATH=/usr/bin:/bin", "XDG_DATA_HOME=" + filepath.Join(home, ".local", "share")}, home
}

func TestRemoteServeOverStdio(t *testing.T) {
	env, home := testEnv(t)
	stdin := strings.NewReader(fmt.Sprintf(`{"id":1,"op":"hello","args":{"version":"dev","protocol":%d}}`, version.Protocol) + "\n" + `{"id":2,"op":"paths","args":{}}` + "\n")
	var stdout, stderr bytes.Buffer
	code := Main([]string{"remote", "serve"}, stdin, &stdout, &stderr, env)
	if code != ExitOK {
		t.Fatalf("exit %d, stderr %s", code, stderr.String())
	}
	sc := bufio.NewScanner(&stdout)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) != 2 || !strings.Contains(lines[0], `"ok":true`) || !strings.Contains(lines[1], filepath.Join(home, ".claude")) {
		t.Errorf("responses = %v", lines)
	}
}

func TestRemoteStreamLog(t *testing.T) {
	env, home := testEnv(t)
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
	j, _ := job.New(dataDir, tsid)
	os.WriteFile(j.LogPath(), []byte("hello log\n"), 0o600)
	var stdout, stderr bytes.Buffer
	// streamID carries the direction (Task 16): log is a send-direction-only
	// kind (source/any streams jobs/<job>/log.txt out).
	code := Main([]string{"remote", "stream", "log", tsid, "send:1"}, strings.NewReader(""), &stdout, &stderr, env)
	if code != ExitOK || stdout.String() != "hello log\n" {
		t.Errorf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	code = Main([]string{"remote", "stream", "bogus", tsid, "send:1"}, strings.NewReader(""), &stdout, &stderr, env)
	if code != ExitFailed || !strings.Contains(stderr.String(), "unsupported stream") {
		t.Errorf("bad kind: exit %d stderr %q", code, stderr.String())
	}
}

// internal-runner's real behaviour (Task 21) is covered by
// internal/cli/teleport_test.go (TestInternalRunnerUsage) and
// internal/orchestrate/runner_test.go — this file predates the real
// orchestrator and no longer has a provisional stub to pin.

// TestDialTargetSurfacesSSHConfigOpenError covers the non-ENOENT branch of
// dialTarget's ~/.ssh/config open: a missing file must still proceed with a
// nil config (the ENOENT case, exercised implicitly elsewhere), but any
// other open error (permission denied here) must fail with the path named
// rather than being silently swallowed as "no config".
func TestDialTargetSurfacesSSHConfigOpenError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission checks are bypassed for root")
	}
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	os.MkdirAll(sshDir, 0o700)
	cfgPath := filepath.Join(sshDir, "config")
	os.WriteFile(cfgPath, []byte("Host *\n"), 0o600)
	if err := os.Chmod(cfgPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(cfgPath, 0o600) })

	env := []string{"HOME=" + home, "USER=alice"}
	_, _, err := dialTarget(context.Background(), "bob@dest.example", nil, nil, env, t.Logf)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitUsage {
		t.Fatalf("err = %v, want *ExitError{Code: ExitUsage}", err)
	}
	if !strings.Contains(err.Error(), cfgPath) {
		t.Errorf("err = %v, want it to name %s", err, cfgPath)
	}
}

func TestEnvPaths(t *testing.T) {
	p, err := envPaths([]string{"HOME=/home/alice", "CLAUDE_CONFIG_DIR=/home/alice/cfg"})
	if err != nil || p.ConfigDir != "/home/alice/cfg" || p.DataDir != "/home/alice/.local/share/claude-teleport" || p.Home != "/home/alice" {
		t.Errorf("envPaths = %+v err=%v", p, err)
	}
	if _, err := envPaths([]string{"PATH=/bin"}); err == nil {
		t.Errorf("missing HOME must be an error")
	}
}

// TestRemoteServeWiresTmuxDialer pins Bug 1 (task-25-report.md): the
// server-side Local built by `remote serve` must carry a real tmux dialer.
// Without one, remote.Local refuses every tmux op with "tmux is not
// available on this host" before it even looks for a server, so preflight
// refuses any destination end state but idle on every real ssh teleport.
// With the dialer wired the same op reaches spec §9 server discovery and
// fails (in this empty TMUX_TMPDIR) with discovery's own message instead.
func TestRemoteServeWiresTmuxDialer(t *testing.T) {
	env, _ := testEnv(t)
	sockDir := t.TempDir()
	env = append(env, "TMUX_TMPDIR="+sockDir)
	stdin := strings.NewReader(`{"id":1,"op":"inventory-tmux","args":{}}` + "\n")
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"remote", "serve"}, stdin, &stdout, &stderr, env); code != ExitOK {
		t.Fatalf("exit %d, stderr %s", code, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "tmux is not available on this host") {
		t.Fatalf("remote serve built its Local without a tmux dialer: %s", out)
	}
	if !strings.Contains(out, sockDir) {
		t.Errorf("expected server discovery to have looked in %s: %s", sockDir, out)
	}
}

// stubTmux answers the control commands the pane probe sends for one pane
// on one fake server; anything else is an error, so the test can never
// pass by accident.
type stubTmux struct {
	sessionName, windowID, paneID string
	panePID                       int
}

func (s *stubTmux) Run(_ context.Context, cmd string) ([]string, error) {
	switch {
	case strings.HasPrefix(cmd, "list-panes -a"):
		return []string{s.sessionName + "\t" + s.windowID + "\t" + s.paneID}, nil
	case strings.HasPrefix(cmd, "list-panes -t"):
		return []string{strconv.Itoa(s.panePID)}, nil
	case strings.HasPrefix(cmd, "capture-pane"):
		return []string{"[claude-teleport] this session was moved"}, nil
	}
	return nil, fmt.Errorf("stubTmux: unexpected command %q", cmd)
}

func (s *stubTmux) Close() error { return nil }

// placeholderProcess starts a real, long-lived process whose /proc
// cmdline reads as the teleport placeholder's, and returns its pid.
// session.ArgvSessionID matches the JOINED command line, so the whole
// spelling goes in argv[0] and `sleep` sees a single operand — that keeps
// the process childless, which is what makes tmuxx.State report it (and
// not a descendant) as the pane's foreground command.
func placeholderProcess(t *testing.T, sid string) int {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep on PATH: %v", err)
	}
	cmd := exec.Command(sleep)
	cmd.Args = []string{"claude-teleport placeholder --resume " + sid, "300"}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	// /proc/<pid>/cmdline is empty until the exec completes.
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", cmd.Process.Pid))
		if err == nil && len(raw) > 0 {
			return cmd.Process.Pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("placeholder stand-in never showed a command line (%v)", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRemoteServeResolvesSuspendedSessions pins finding A4: serverLocalOptions
// wired Tmux but no pane Probe, and remote.NewLocal derives none — so over
// ssh a session held by a placeholder pane read back as idle, selector rule
// 4 could not resolve on the remote and `--from host` downgraded the end
// state. The whole path is real here: a `remote serve` over the in-process
// sshd, dialled by the production dialTarget/remote.NewClient.
func TestRemoteServeResolvesSuspendedSessions(t *testing.T) {
	remoteEnv, remoteHome := testEnv(t)
	sockDir := t.TempDir()
	sock := filepath.Join(sockDir, "default")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stub := &stubTmux{sessionName: "main", windowID: "@4", paneID: "%9", panePID: placeholderProcess(t, tsid)}
	restore := tmuxx.Dial
	tmuxx.Dial = func(ctx context.Context, path string) (tmuxx.Transport, error) {
		if path == sock {
			return stub, nil
		}
		return nil, fmt.Errorf("no tmux server at %s", path)
	}
	t.Cleanup(func() { tmuxx.Dial = restore })
	remoteEnv = append(remoteEnv, "TMUX_TMPDIR="+sockDir)

	proj := filepath.Join(remoteHome, ".claude", "projects", "-home-bob-work")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","cwd":"/home/bob/work","sessionId":"` + tsid + `","gitBranch":"main","version":"2.1.247","timestamp":"2026-08-27T11:00:05.000Z","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(filepath.Join(proj, tsid+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	target, opts, localHome := remoteHost(t, remoteEnv)
	a := &app{
		env:    parseEnv([]string{"HOME=" + localHome, "USER=alice", "PATH=/usr/bin:/bin", "TMUX_TMPDIR=" + filepath.Join(t.TempDir(), "no-tmux-here")}),
		logf:   t.Logf,
		stdout: io.Discard, stderr: io.Discard,
	}
	ep, closeFn, err := a.dialRemote(context.Background(), orchestrate.Options{Target: target, SSHOptions: sshOptionMap(t, opts)})
	if err != nil {
		t.Fatalf("dial %s: %v", target, err)
	}
	defer closeFn()
	sess, err := ep.ResolveSession(context.Background(), session.Selector{ID: session.ID(tsid)})
	if err != nil {
		t.Fatalf("ResolveSession over ssh: %v", err)
	}
	if sess.State != session.StateSuspended {
		t.Errorf("session held by a placeholder pane resolves as %s over ssh, want suspended", sess.State)
	}
	if sess.Tmux == nil || sess.Tmux.PaneID != "%9" {
		t.Errorf("suspended session's pane ref = %+v", sess.Tmux)
	}
}

// sshOptionMap turns remoteHost's ["-o", "K=V", ...] into the map
// orchestrate.Options carries.
func sshOptionMap(t *testing.T, opts []string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for i := 0; i+1 < len(opts); i += 2 {
		if opts[i] != "-o" {
			t.Fatalf("unexpected option %q", opts[i])
		}
		k, v, ok := strings.Cut(opts[i+1], "=")
		if !ok {
			t.Fatalf("unexpected option %q", opts[i+1])
		}
		m[k] = v
	}
	return m
}
