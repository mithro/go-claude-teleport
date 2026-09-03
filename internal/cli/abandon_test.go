package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/orchestrate"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// startFakeRunner spawns `sleep 60` with argv[0] rewritten to
// "internal-runner" (the binary executed is still the real, universally
// available `sleep`; only the argv the child sees — and /proc/pid/cmdline
// — changes) and returns its pid, killing it on cleanup. runnerAlive
// (teleport.go) matches on cmdline containing "internal-runner" to guard
// against pid reuse, so a plain `sleep` process is not enough to exercise
// job.RunnerAlive's true branch.
func startFakeRunner(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	cmd.Args[0] = "internal-runner"
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd.Process.Pid
}

func shaOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestAbandonRefusesWhileRunnerAlive(t *testing.T) {
	env, home := testEnv(t)
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
	j, _ := job.New(dataDir, tsid)
	j.Direction = "to"
	j.RunnerPID = startFakeRunner(t)
	j.Save()

	var out, errOut bytes.Buffer
	code := Main([]string{"abandon", tsid}, strings.NewReader(""), &out, &errOut, env)
	if code != ExitFailed {
		t.Fatalf("exit %d, want ExitFailed; stderr %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "live runner") {
		t.Errorf("stderr = %q, want it to name the live runner", errOut.String())
	}
	got, _, err := job.Open(dataDir, tsid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome == "abandoned" || got.Finished {
		t.Errorf("journal must not be marked abandoned while the runner is alive: %+v", got)
	}
}

func TestAbandonNoJob(t *testing.T) {
	env, _ := testEnv(t)
	code, _, stderr := run(t, env, "abandon", tsid)
	if code != ExitUsage || !strings.Contains(stderr, "no job") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

// TestAbandonNoRecordedPlan covers a journal that was created but never
// reached a completed preflight (Plan still "null") — this cannot happen
// on the real runTeleport path (job.New/Save only run after a successful
// Preflight), but abandon must still fail loudly rather than panic.
func TestAbandonNoRecordedPlan(t *testing.T) {
	env, home := testEnv(t)
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
	j, _ := job.New(dataDir, tsid)
	j.Save()

	code, _, stderr := run(t, env, "abandon", tsid)
	if code != ExitFailed || !strings.Contains(stderr, "no recorded plan") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

// TestAbandonDeletesInstalledFilesOnRemoteDestination is Task 23's main
// case: abandon's destination-side cleanup and --delete-destination-files
// must work when the destination is a REAL (if loopback) remote host, not
// just this machine — it dials it exactly as a teleport would
// (a.endpoints) and drives the new DeleteInstalled remote op over the
// wire. It also proves the two layers of protection: the CLI only offers
// ids whose preflight status was Absent (kept.jsonl, status
// PresentSame, is never a candidate even though its current content also
// matches the manifest hash), and DeleteInstalled itself re-verifies the
// hash before removing anything (covered directly by
// TestLocalDeleteInstalledOnlyNamedIDs and transfer's
// TestUninstallIDsOnlyNamedEntries; this test exercises the CLI wiring
// end-to-end).
func TestAbandonDeletesInstalledFilesOnRemoteDestination(t *testing.T) {
	remoteEnv, remoteHome := testEnv(t)
	target, opts, localHome := remoteHost(t, remoteEnv)
	sshOpts := map[string]string{}
	for i := 0; i+1 < len(opts); i += 2 {
		if opts[i] != "-o" {
			continue
		}
		k, v, _ := strings.Cut(opts[i+1], "=")
		sshOpts[k] = v
	}
	localEnv := []string{"HOME=" + localHome, "USER=alice", "PATH=/usr/bin:/bin"}

	// Staging on the destination must exist before abandon and be gone
	// after (spec §7.4: staging is removed by abandon, or after step 10).
	remoteDataDir := filepath.Join(remoteHome, ".local", "share", "claude-teleport")
	staging := job.StagingDir(remoteDataDir, tsid)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "0"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Two files already sit on the destination with content that happens
	// to match the manifest hash: "gone.jsonl" is what THIS job installed
	// (preflight status Absent — it was not there before); "kept.jsonl"
	// merely already existed (status PresentSame) and must survive even
	// though its content also matches.
	dir := filepath.Join(remoteHome, ".claude", "projects", "-home-bob-work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, "gone.jsonl")
	kept := filepath.Join(dir, "kept.jsonl")
	if err := os.WriteFile(gone, []byte("gone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kept, []byte("kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &transfer.Manifest{Version: 1, JobID: tsid, SessionID: tsid, Entries: []transfer.Entry{
		{ID: 0, Category: session.CatSession, Dst: dir, Mode: uint32(os.ModeDir | 0o700)},
		{ID: 1, Category: session.CatSession, Dst: gone, Size: 5, Mode: 0o600, SHA256: shaOf("gone\n")},
		{ID: 2, Category: session.CatSession, Dst: kept, Size: 5, Mode: 0o600, SHA256: shaOf("kept\n")},
	}}

	localDataDir := filepath.Join(localHome, ".local", "share", "claude-teleport")
	j, err := job.New(localDataDir, tsid)
	if err != nil {
		t.Fatal(err)
	}
	j.SessionID, j.Direction = tsid, "to"
	j.SourceHost, j.DestHost = "src.example", "dest.example"
	if err := m.Save(j.ManifestPath()); err != nil {
		t.Fatal(err)
	}
	plan := &orchestrate.Plan{
		JobID:        tsid,
		Session:      &session.Session{ID: session.ID(tsid)},
		DestInfo:     remote.HostInfo{Hostname: "dest.example"},
		ManifestPath: j.ManifestPath(),
		Statuses:     map[int]transfer.Status{1: transfer.Absent, 2: transfer.PresentSame},
		Options: orchestrate.Options{
			Direction: "to", Target: target, SSHOptions: sshOpts,
			Selector: session.Selector{ID: session.ID(tsid)}, State: "auto",
			ExitTimeout: 10 * time.Second, StartTimeout: 10 * time.Second,
		},
	}
	if j.Plan, err = plan.ToJSON(); err != nil {
		t.Fatal(err)
	}
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := Main([]string{"abandon", tsid, "--delete-destination-files"}, strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Errorf("destination staging must be removed")
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("gone.jsonl (status Absent) must be deleted")
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("kept.jsonl (status PresentSame, pre-existing) must survive: %v", err)
	}
	if !strings.Contains(out.String(), gone) {
		t.Errorf("stdout must name the deleted file:\n%s", out.String())
	}

	got, _, err := job.Open(localDataDir, tsid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "abandoned" || !got.Finished {
		t.Errorf("journal = %+v", got)
	}

	// `continue` on an abandoned job is a usage error (spec: abandon is
	// terminal — start a new teleport instead).
	if code, _, stderr := run(t, localEnv, "continue", tsid); code != ExitUsage {
		t.Errorf("continue after abandon = %d %q, want ExitUsage", code, stderr)
	}
}
