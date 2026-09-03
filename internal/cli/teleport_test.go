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
	"github.com/mithro/go-claude-teleport/internal/remote"
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

// TestBangModeGate covers spec §6.3's !-mode gate (finding A5): only a
// $CLAUDE_PID that names the very Claude the session's live registry entry
// names may turn it on.
func TestBangModeGate(t *testing.T) {
	reg := &session.Registry{SessionID: fixtureSID, PID: 4242}
	for _, tc := range []struct {
		name string
		env  map[string]string
		reg  *session.Registry
		want bool
	}{
		{"inside the session", map[string]string{"CLAUDE_PID": "4242"}, reg, true},
		{"no CLAUDE_PID", map[string]string{}, reg, false},
		{"empty CLAUDE_PID", map[string]string{"CLAUDE_PID": ""}, reg, false},
		{"unparsable CLAUDE_PID", map[string]string{"CLAUDE_PID": "not-a-pid"}, reg, false},
		{"another Claude's pid", map[string]string{"CLAUDE_PID": "4243"}, reg, false},
		{"session has no live registry entry", map[string]string{"CLAUDE_PID": "4242"}, nil, false},
	} {
		if got := bangMode(tc.env, tc.reg); got != tc.want {
			t.Errorf("%s: bangMode = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestContinueDerivesBangMode pins A3: `! claude-teleport continue` ran
// with bang=false, so the foreground followed the job to Finished — the
// source Claude therefore never went idle, waitSourceIdle burned the whole
// exit timeout and step thaw+exit failed with BOTH Claudes alive. continue
// must derive !-mode from $CLAUDE_PID and the stored plan exactly as
// runTeleport does.
func TestContinueDerivesBangMode(t *testing.T) {
	a, out := fixtureApp(t)
	a.env["CLAUDE_PID"] = "4242"
	j := unfinishedJob(t, a, fixtureSID)
	p := &orchestrate.Plan{
		Options:    orchestrate.Options{Direction: "to", Target: "big-storage.example", BangMode: true},
		Session:    &session.Session{ID: session.ID(fixtureSID), Registry: &session.Registry{SessionID: fixtureSID, PID: 4242}},
		DestInfo:   remote.HostInfo{Hostname: "big-storage.example"},
		SourceInfo: remote.HostInfo{Hostname: "laptop.example"}, TargetState: "running",
	}
	raw, err := p.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	j.Plan = raw
	j.Steps = []job.StepState{{Name: "start", Status: job.Done, Attempts: 1}}
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}

	// A runner that reaches thaw+exit and then keeps going (the source
	// Claude's own /exit is what ends it) without ever finishing the job.
	dir := t.TempDir()
	at9 := *j
	at9.Steps = []job.StepState{{Name: "start", Status: job.Done, Attempts: 1}, {Name: "thaw+exit", Status: job.Running, Attempts: 1}}
	at9.Finished, at9.Outcome = false, ""
	rawJ, err := json.MarshalIndent(at9, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	at9Path := filepath.Join(dir, "at-thaw-exit.json")
	if err := os.WriteFile(at9Path, rawJ, 0o600); err != nil {
		t.Fatal(err)
	}
	// The write is flock'd on the SAME <job dir>/.journal.lock path every
	// real Journal.Save (and spawnAndFollow's SaveMerged) takes, exactly
	// like a real runner's save would be. Without this the write races
	// spawnAndFollow's own post-spawn save recording this runner's pid:
	// under CPU contention that save can read the journal before this
	// write lands and then write it back after, silently reverting it —
	// reproduced locally, and the likely cause of this test's flake on
	// GitHub's 2-CPU release runner (see job.SaveMerged's doc comment).
	// j.Dir (not $2, which is only equal to it by convention and would
	// anyway be unset inside flock -c's own nested shell) is baked in
	// directly: it is already known here.
	script := filepath.Join(dir, "runner.sh")
	runnerScript := "#!/bin/sh\nflock " + j.Dir + "/.journal.lock -c 'cp " + at9Path + " " + j.Dir + "/job.json'\nsleep 30\n"
	if err := os.WriteFile(script, []byte(runnerScript), 0o700); err != nil {
		t.Fatal(err)
	}
	a.selfExe = script

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := newContinueCmd(a)
	cmd.SetContext(ctx)
	start := time.Now()
	err = cmd.RunE(cmd, []string{fixtureSID})
	if err != nil {
		t.Fatalf("continue = %v after %s (it followed to Finished instead of returning at thaw+exit)\n%s", err, time.Since(start), out.String())
	}
	if !strings.Contains(out.String(), "this Claude will now exit") {
		t.Errorf("!-mode continue must hand the terminal back:\n%s", out.String())
	}
}

// TestBangFollowTailsTheLogOnFailure covers the last !-mode path finding
// A5 lists: in !-mode nothing streamed the log, so a failure has to carry
// its own tail out to stderr.
func TestBangFollowTailsTheLogOnFailure(t *testing.T) {
	a, out := fixtureApp(t)
	j := unfinishedJob(t, a, fixtureSID)
	j.Steps = []job.StepState{{Name: "transfer", Status: job.Failed, Error: "tar stream: EOF"}}
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(j.LogPath(), []byte("step transfer: starting (attempt 1)\ntar stream: EOF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code := a.follow(context.Background(), j, true, stepState(j, "thaw+exit"))
	if code != ExitFailed {
		t.Fatalf("follow = %d, want %d\n%s", code, ExitFailed, out.String())
	}
	if !strings.Contains(out.String(), "tar stream: EOF") {
		t.Errorf("!-mode failure must tail log.txt:\n%s", out.String())
	}
}
