package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/orchestrate"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/test/fakeclaude/harness"
)

const teleportE2ESID = "5e6f7a8b-9c0d-4e1f-8a2b-3c4d5e6f7a8b"

var builtTeleportExe string

// buildTeleportExe builds cmd/claude-teleport once per test binary; the
// real orchestrate.RunJob path is only reachable through a real process
// (internal-runner is a self re-exec, spawned via procx.SpawnDetached).
func buildTeleportExe(t *testing.T) string {
	t.Helper()
	if builtTeleportExe != "" {
		return builtTeleportExe
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "claude-teleport")
	cmd := exec.Command("go", "build", "-o", out, "github.com/mithro/go-claude-teleport/cmd/claude-teleport")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build claude-teleport: %v\n%s", err, out)
	}
	builtTeleportExe = out
	return builtTeleportExe
}

// TestTeleportEndToEndLocalToLocal drives the real production path — Task
// 21's a.endpoints, orchestrate.Preflight, a.spawnAndFollow (a REAL
// `internal-runner` subprocess, procx.SpawnDetached) and a.follow — for an
// idle, no-tmux, same-machine move (Options.LocalDest is the sanctioned
// test hook: "a second Local endpoint instead of ssh", options.go). Flag
// parsing itself (teleportFlags -> orchestrate.Options) is unit-tested
// separately (root_test.go's TestTeleportOptionsFromFlags); this test
// exercises the pieces flag parsing hands off to, at the a.endpoints/
// a.spawnAndFollow boundary, exactly as runTeleport itself calls them.
func TestTeleportEndToEndLocalToLocal(t *testing.T) {
	exe := buildTeleportExe(t)
	claudeDir := harness.Build(t)
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", claudeDir+string(os.PathListSeparator)+oldPath)

	root := t.TempDir()
	srcHome := filepath.Join(root, "src", "home", "alice")
	dstHome := filepath.Join(root, "dst", "home", "bob")
	os.MkdirAll(srcHome, 0o700)
	os.MkdirAll(dstHome, 0o700)
	srcPaths := session.Paths{Home: srcHome, ConfigDir: filepath.Join(srcHome, ".claude"), GlobalJSON: filepath.Join(srcHome, ".claude.json"), DataDir: filepath.Join(srcHome, ".local", "share", "claude-teleport"), ProcRoot: "/proc"}
	dstPaths := session.Paths{Home: dstHome, ConfigDir: filepath.Join(dstHome, ".claude"), GlobalJSON: filepath.Join(dstHome, ".claude.json"), DataDir: filepath.Join(dstHome, ".local", "share", "claude-teleport"), ProcRoot: "/proc"}
	os.MkdirAll(srcPaths.ConfigDir, 0o700)
	os.MkdirAll(dstPaths.ConfigDir, 0o700)

	cwd := filepath.Join(srcHome, "x")
	os.MkdirAll(cwd, 0o755)
	seed := exec.Command("claude", "-p", "--session-id", teleportE2ESID, "remember the word pineapple")
	seed.Dir = cwd
	seed.Env = append(os.Environ(), "HOME="+srcHome, "CLAUDE_CONFIG_DIR="+srcPaths.ConfigDir, "PATH="+claudeDir+string(os.PathListSeparator)+oldPath)
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}

	// TMUX_TMPDIR points somewhere with no tmux server: without this,
	// a.localEndpoint's spec §9 discovery (no $TMUX set) falls back to the
	// real default /tmp/tmux-<uid> and can find and touch THIS machine's
	// own tmux server, which the implementer rules forbid outright — this
	// test wants (and asserts, via State: "idle") the no-tmux path.
	env := []string{"HOME=" + srcHome, "PATH=" + claudeDir + string(os.PathListSeparator) + oldPath, "TMUX_TMPDIR=" + filepath.Join(root, "no-tmux-here")}
	a := &app{env: parseEnv(env), selfExe: exe, logf: t.Logf}
	if err := a.ensurePaths(); err != nil {
		t.Fatal(err)
	}

	o := orchestrate.Options{
		Direction: "to", Selector: session.Selector{ID: session.ID(teleportE2ESID)}, State: "idle",
		ExitTimeout: 10 * time.Second, StartTimeout: 20 * time.Second, LocalDest: &dstPaths,
	}
	ctx := context.Background()
	src, dst, closeFn, err := a.endpoints(ctx, o)
	if err != nil {
		t.Fatalf("endpoints: %v", err)
	}
	sess, err := src.ResolveSession(ctx, o.Selector)
	if err != nil {
		closeFn()
		t.Fatalf("ResolveSession: %v", err)
	}
	jobID := string(sess.ID)
	plan, err := orchestrate.Preflight(ctx, o, src, dst, jobID)
	if err != nil {
		closeFn()
		t.Fatalf("Preflight: %v", err)
	}
	j, err := job.New(a.paths.DataDir, jobID)
	if err != nil {
		closeFn()
		t.Fatal(err)
	}
	j.SessionID, j.Direction = jobID, o.Direction
	j.SourceHost, j.DestHost = plan.SourceInfo.Hostname, plan.DestInfo.Hostname
	j.CreatedAt, j.UpdatedAt = time.Now(), time.Now()
	if j.Plan, err = plan.ToJSON(); err != nil {
		closeFn()
		t.Fatal(err)
	}
	if err := j.Save(); err != nil {
		closeFn()
		t.Fatal(err)
	}
	closeFn()

	var out bytes.Buffer
	a.stdout, a.stderr = &out, &out
	code := a.spawnAndFollow(ctx, j, false)
	if code != ExitOK {
		t.Fatalf("spawnAndFollow = %d, log:\n%s", code, out.String())
	}

	// The job journal now records success, and `continue` on a finished
	// job is a no-op that reports so (spec: `continue` resumes an
	// INTERRUPTED job).
	j2, ok, err := job.Open(a.paths.DataDir, jobID)
	if err != nil || !ok || j2.Outcome != "success" || !j2.Finished {
		t.Fatalf("journal after spawnAndFollow = %+v ok=%v err=%v", j2, ok, err)
	}
	if orchestrate.ExitCode(j2) != ExitOK {
		t.Errorf("ExitCode(finished journal) = %d", orchestrate.ExitCode(j2))
	}

	// `continue` on an already-finished job (newContinueCmd's own guard,
	// through the real Main()/cobra path this time) reports success
	// without re-spawning a runner.
	code2, out2, errOut2 := run(t, env, "continue", jobID)
	if code2 != ExitOK || !strings.Contains(out2, "already finished successfully") {
		t.Fatalf("continue on a finished job = %d\nstdout: %s\nstderr: %s", code2, out2, errOut2)
	}
}
