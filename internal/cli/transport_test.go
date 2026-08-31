package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/job"
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
	stdin := strings.NewReader(`{"id":1,"op":"hello","args":{"version":"dev","protocol":2}}` + "\n" + `{"id":2,"op":"paths","args":{}}` + "\n")
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
	code := Main([]string{"remote", "stream", "log", tsid, "s1"}, strings.NewReader(""), &stdout, &stderr, env)
	if code != ExitOK || stdout.String() != "hello log\n" {
		t.Errorf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	code = Main([]string{"remote", "stream", "bogus", tsid, "s1"}, strings.NewReader(""), &stdout, &stderr, env)
	if code != ExitFailed || !strings.Contains(stderr.String(), "unknown stream kind") {
		t.Errorf("bad kind: exit %d stderr %q", code, stderr.String())
	}
}

func TestInternalRunnerWithoutStepsIsExplicit(t *testing.T) {
	env, home := testEnv(t)
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
	j, _ := job.New(dataDir, tsid)
	j.Save()
	var stdout, stderr bytes.Buffer
	code := Main([]string{"internal-runner", j.Dir}, strings.NewReader(""), &stdout, &stderr, env)
	if code != ExitFailed || !strings.Contains(stderr.String(), "no steps registered") {
		t.Errorf("exit %d stderr %q", code, stderr.String())
	}
	code = Main([]string{"internal-runner", filepath.Join(dataDir, "jobs", "nope")}, strings.NewReader(""), &stdout, &stderr, env)
	if code != ExitFailed || !strings.Contains(stderr.String(), "no journal") {
		t.Errorf("missing journal: exit %d stderr %q", code, stderr.String())
	}
	if _, err := os.Stat(j.Dir); err != nil {
		t.Errorf("runner must not remove the job dir: %v", err)
	}
	_ = io.EOF
}

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
