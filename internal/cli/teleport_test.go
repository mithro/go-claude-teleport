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
