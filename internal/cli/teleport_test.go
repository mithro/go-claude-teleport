package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/orchestrate"
	"github.com/mithro/go-claude-teleport/internal/session"
)

func TestTeleportUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{}, // no direction
		{"--to", "a.example", "--from", "b.example"}, // both
		{"--to", "a.example", "--state", "sideways"}, // bad state
		{"--to", "a.example", "--map", "notapair"},   // bad map
	} {
		var out, errb bytes.Buffer
		code := Main(args, strings.NewReader(""), &out, &errb, []string{"HOME=/home/alice", "PATH=/usr/bin"})
		if code != ExitUsage {
			t.Errorf("Main(%v) = %d (%s), want %d", args, code, errb.String(), ExitUsage)
		}
	}
}

func TestParseMaps(t *testing.T) {
	m, err := parseMaps([]string{"/home/alice/a=/srv/a", "/x=/y"})
	if err != nil || len(m) != 2 || m[0].From != "/home/alice/a" || m[1].To != "/y" {
		t.Fatalf("parseMaps = %v %v", m, err)
	}
	if _, err := parseMaps([]string{"relative=/y"}); err == nil {
		t.Error("relative source must be rejected")
	}
}

func TestInternalRunnerUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Main([]string{"internal-runner"}, strings.NewReader(""), &out, &errb, []string{"HOME=/home/alice"}); code != ExitUsage {
		t.Errorf("internal-runner without a job dir = %d", code)
	}
}

// TestSpawnAndFollowClearsAStaleFailedOutcome pins the `continue` race the
// Docker integration suite's network-drop scenario hit: the journal on disk
// still says finished/failed from the run being continued, and follow's
// first done() check can read it before the freshly spawned runner has
// cleared it — reporting the OLD failure without waiting for anything.
func TestSpawnAndFollowClearsAStaleFailedOutcome(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := &app{env: parseEnv([]string{"HOME=" + home, "PATH=/usr/bin:/bin"}), logf: t.Logf}
	if err := a.ensurePaths(); err != nil {
		t.Fatal(err)
	}
	const sid = "5c4b3a29-1d0e-4f8a-9b7c-6d5e4f3a2b19"
	j, err := job.New(a.paths.DataDir, sid)
	if err != nil {
		t.Fatal(err)
	}
	j.Steps = []job.StepState{{Name: "transfer", Status: job.Failed, Error: "tar stream: EOF"}}
	j.Finished, j.Outcome = true, "failed"
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}
	// The journal a successful re-run would leave, and a stand-in runner
	// that takes its time producing it. selfExe is invoked as
	// `<selfExe> internal-runner <job dir>` by procx.SpawnDetached.
	done := *j
	done.Steps = []job.StepState{{Name: "transfer", Status: job.Done}}
	done.Outcome, done.Finished = "success", true
	raw, err := json.MarshalIndent(done, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	successPath := filepath.Join(dir, "success.json")
	if err := os.WriteFile(successPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := filepath.Join(dir, "fake-runner.sh")
	script := "#!/bin/sh\nsleep 1\ncp " + successPath + " \"$2\"/job.json\n"
	if err := os.WriteFile(runner, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	a.selfExe = runner

	var out bytes.Buffer
	a.stdout, a.stderr = &out, &out
	start := time.Now()
	if code := a.spawnAndFollow(context.Background(), j, false); code != ExitOK {
		t.Fatalf("spawnAndFollow = %d after %s (the stale failed outcome was reported instead of the new run's)\n%s",
			code, time.Since(start), out.String())
	}
}

// fixtureSID is the idle session in ../session/testdata/config (its
// registry pid is not alive on this machine, so it resolves as idle).
const fixtureSID = "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"

// fixtureApp is an app whose local endpoint reads the read-only session
// fixture and whose data dir (jobs, journals) is a fresh temp dir.
func fixtureApp(t *testing.T) (*app, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	env := []string{"HOME=/home/alice", "CLAUDE_CONFIG_DIR=../session/testdata/config",
		"XDG_DATA_HOME=" + filepath.Join(dir, "share"), "PATH=/usr/bin:/bin",
		// somewhere with no tmux server: never touch this machine's own
		"TMUX_TMPDIR=" + filepath.Join(dir, "no-tmux-here")}
	var out bytes.Buffer
	a := &app{env: parseEnv(env), logf: t.Logf, stdout: &out, stderr: &out}
	if err := a.ensurePaths(); err != nil {
		t.Fatal(err)
	}
	return a, &out
}

// fakeRunner writes a shell script that stands in for `<selfExe>
// internal-runner <job dir>`: it records that it ran (marker) and then
// finishes the job the way a successful runner would.
func fakeRunner(t *testing.T, a *app, sid string) (script, marker string) {
	t.Helper()
	dir := t.TempDir()
	marker = filepath.Join(dir, "runner-ran")
	done := job.Journal{ID: sid, SessionID: sid, Finished: true, Outcome: "success",
		Steps: []job.StepState{{Name: "transfer", Status: job.Done}}}
	raw, err := json.MarshalIndent(done, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	successPath := filepath.Join(dir, "success.json")
	if err := os.WriteFile(successPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	script = filepath.Join(dir, "fake-runner.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\ncp "+successPath+" \"$2\"/job.json\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return script, marker
}

// unfinishedJob stores a failed, continuable job for sid.
func unfinishedJob(t *testing.T, a *app, sid string) *job.Journal {
	t.Helper()
	j, err := job.New(a.paths.DataDir, sid)
	if err != nil {
		t.Fatal(err)
	}
	j.SessionID, j.Direction, j.DestHost = sid, "to", "big-storage.example"
	j.Steps = []job.StepState{{Name: "transfer", Status: job.Failed, Error: "tar stream: EOF"}}
	j.Finished, j.Outcome = true, "failed"
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}
	return j
}

// TestDryRunNeverContinuesAnExistingJob pins A1: with an unfinished job on
// disk, `--dry-run` used to reach the "continue it" branch BEFORE the
// dry-run check — spawning a runner that SIGSTOPs the live Claude and
// teleports for real. Nothing may be spawned and the journal must be
// untouched.
func TestDryRunNeverContinuesAnExistingJob(t *testing.T) {
	a, out := fixtureApp(t)
	script, marker := fakeRunner(t, a, fixtureSID)
	a.selfExe = script
	unfinishedJob(t, a, fixtureSID)

	o := orchestrate.Options{Direction: "to", Target: "big-storage.example",
		Selector: session.Selector{ID: session.ID(fixtureSID)}, State: "auto", LocalDest: &a.paths}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if code := a.teleport(ctx, o, true); code != ExitOK {
		t.Fatalf("dry run over an existing job = %d\n%s", code, out.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("--dry-run spawned a runner (marker %s exists)\n%s", marker, out.String())
	}
	j, ok, err := job.Open(a.paths.DataDir, fixtureSID)
	if err != nil || !ok {
		t.Fatalf("job.Open = %v %v", ok, err)
	}
	if j.RunnerPID != 0 || !j.Finished || j.Outcome != "failed" {
		t.Errorf("--dry-run modified the journal: %+v", j)
	}
	if !strings.Contains(out.String(), "nothing was moved") {
		t.Errorf("dry run must say nothing was moved:\n%s", out.String())
	}
}

// TestFollowEndsWhenTheRunnerDies pins the foreground half of finding A2:
// a runner that dies before it ever marks the journal (peer down, a bad
// plan, an outright crash) left `follow` waiting on jj.Finished forever,
// and Ctrl-C then printed "the runner keeps going" about a dead process.
func TestFollowEndsWhenTheRunnerDies(t *testing.T) {
	a, out := fixtureApp(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "dying-runner.sh")
	// A runner that exits at once without touching the journal.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a.selfExe = script
	j := unfinishedJob(t, a, fixtureSID)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	code := a.spawnAndFollow(ctx, j, false)
	elapsed := time.Since(start)
	if code == ExitOK || code == ExitInterrupted {
		t.Fatalf("follow over a dead runner = %d after %s, want a plain failure\n%s", code, elapsed, out.String())
	}
	if elapsed > 10*time.Second {
		t.Errorf("follow took %s to notice the dead runner", elapsed)
	}
	if !strings.Contains(out.String(), "runner") {
		t.Errorf("the failure must say the runner died:\n%s", out.String())
	}
}

// TestBangModeIgnoresAReplacedRunsThawExitStatus pins carry 2: in !-mode
// `follow` returns as soon as the thaw+exit step starts, but a job being
// continued still carries the REPLACED run's status for that step — so
// `! claude-teleport continue` over a job that failed at or after step 9
// reported success the instant it started, while its freshly spawned
// runner was still dialling.
func TestBangModeIgnoresAReplacedRunsThawExitStatus(t *testing.T) {
	a, out := fixtureApp(t)
	j := unfinishedJob(t, a, fixtureSID)
	j.Steps = []job.StepState{
		{Name: "start", Status: job.Done, Attempts: 1},
		{Name: "thaw+exit", Status: job.Failed, Attempts: 1, Error: "source claude did not exit within 30s"},
	}
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}
	// The runner takes a moment to get anywhere, then makes its own,
	// second attempt at thaw+exit.
	dir := t.TempDir()
	fresh := *j
	fresh.Steps = []job.StepState{
		{Name: "start", Status: job.Done, Attempts: 1},
		{Name: "thaw+exit", Status: job.Running, Attempts: 2},
	}
	fresh.Finished, fresh.Outcome = false, ""
	raw, err := json.MarshalIndent(fresh, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	freshPath := filepath.Join(dir, "second-attempt.json")
	if err := os.WriteFile(freshPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "slow-runner.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 1\ncp "+freshPath+" \"$2\"/job.json\nsleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a.selfExe = script

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	code := a.spawnAndFollow(ctx, j, true)
	elapsed := time.Since(start)
	if code != ExitOK {
		t.Fatalf("!-mode follow = %d after %s\n%s", code, elapsed, out.String())
	}
	if elapsed < time.Second {
		t.Errorf("!-mode follow returned after %s — it trusted the replaced run's thaw+exit status\n%s", elapsed, out.String())
	}
}
