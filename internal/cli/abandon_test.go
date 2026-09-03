package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/orchestrate"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
	"github.com/mithro/go-claude-teleport/test/fakeclaude/harness"
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
// ids listed in Plan.InstalledIDs (kept.jsonl, never recorded as
// installed by this job, is never a candidate even though its current
// content also matches the manifest hash), and DeleteInstalled itself
// re-verifies the hash before removing anything (covered directly by
// TestLocalDeleteInstalledOnlyNamedIDs and transfer's
// TestUninstallIDsOnlyNamedEntries; this test exercises the CLI wiring
// end-to-end). See TestAbandonDeleteDestinationFilesAfterRealTeleport for
// the same mechanism driven by a real runner (R-P3-23a) rather than a
// hand-authored Plan.
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
	// (its id is in InstalledIDs); "kept.jsonl" merely already existed
	// (never recorded as installed) and must survive even though its
	// content also matches.
	dir := filepath.Join(remoteHome, ".claude", "projects", "-home-bob-work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, "gone.jsonl")
	kept := filepath.Join(dir, "kept.jsonl")
	if err := os.WriteFile(kept, []byte("kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &transfer.Manifest{Version: 1, JobID: tsid, SessionID: tsid, Entries: []transfer.Entry{
		{ID: 0, Category: session.CatSession, Dst: dir, Mode: uint32(os.ModeDir | 0o700)},
		{ID: 1, Category: session.CatSession, Dst: gone, Size: 5, Mode: 0o600, SHA256: shaOf("gone\n")},
		{ID: 2, Category: session.CatSession, Dst: kept, Size: 5, Mode: 0o600, SHA256: shaOf("kept\n")},
	}}

	// gone.jsonl is placed by a REAL install on the destination, which is
	// also what writes the destination's own jobs/<id>/installed.json —
	// the record ruling R-P3-B1f N3 makes the only licence to delete
	// anything later. kept.jsonl (present-same, never installed by this
	// job) is deliberately not in it, even though its content matches the
	// manifest hash too.
	if err := os.WriteFile(transfer.StagedPath(staging, 1), []byte("gone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dstPaths := session.Paths{Home: remoteHome, ConfigDir: filepath.Join(remoteHome, ".claude"),
		GlobalJSON: filepath.Join(remoteHome, ".claude.json"), DataDir: remoteDataDir}
	st, err := transfer.Diff(context.Background(), m, staging, dstPaths)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transfer.Install(context.Background(), m, st, staging, dstPaths, transfer.InstallExtras{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(gone); err != nil {
		t.Fatalf("test setup: install did not place %s: %v", gone, err)
	}

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
		InstalledIDs: []int{1},
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

const abandonE2ESID = "6f5e4d3c-2b1a-4c9d-8e7f-0a1b2c3d4e5f"

// TestAbandonDeleteDestinationFilesAfterRealTeleport is ruling R-P3-23a's
// regression test: it drives a REAL teleport through the production path
// (a.endpoints / orchestrate.Preflight / a.spawnAndFollow, Options.LocalDest
// as the sanctioned test hook — same as TestTeleportEndToEndLocalToLocal),
// so Plan.InstalledIDs is populated by the real install step, never
// hand-authored. Plan.Statuses is NOT usable here: capture, verifyTransfer
// and runTransfer all re-diff and persist a fresh Statuses map, so by the
// time the job finishes every entry reads back PresentSame/StagedSame and a
// Statuses-based id selection is empty — abandon must read InstalledIDs.
// It then tampers with one installed file's content (simulating "changed
// since install") and asserts it survives the delete while a genuinely
// installed, untouched file is removed.
func TestAbandonDeleteDestinationFilesAfterRealTeleport(t *testing.T) {
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
	seed := exec.Command("claude", "-p", "--session-id", abandonE2ESID, "remember the word pineapple")
	seed.Dir = cwd
	seed.Env = append(os.Environ(), "HOME="+srcHome, "CLAUDE_CONFIG_DIR="+srcPaths.ConfigDir, "PATH="+claudeDir+string(os.PathListSeparator)+oldPath)
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}

	env := []string{"HOME=" + srcHome, "PATH=" + claudeDir + string(os.PathListSeparator) + oldPath, "TMUX_TMPDIR=" + filepath.Join(root, "no-tmux-here")}
	a := &app{env: parseEnv(env), selfExe: exe, logf: t.Logf}
	if err := a.ensurePaths(); err != nil {
		t.Fatal(err)
	}

	o := orchestrate.Options{
		Direction: "to", Selector: session.Selector{ID: session.ID(abandonE2ESID)}, State: "idle",
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

	j2, ok, err := job.Open(a.paths.DataDir, jobID)
	if err != nil || !ok || j2.Outcome != "success" {
		t.Fatalf("journal after teleport = %+v ok=%v err=%v", j2, ok, err)
	}
	finishedPlan, err := orchestrate.PlanFromJournal(j2)
	if err != nil {
		t.Fatal(err)
	}
	if len(finishedPlan.InstalledIDs) == 0 {
		t.Fatalf("a real, successful teleport must populate InstalledIDs")
	}
	m, err := transfer.Load(finishedPlan.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	transcriptID := -1
	for _, id := range finishedPlan.InstalledIDs {
		e, _ := m.ByID(id)
		if e.Category == session.CatSession && strings.HasSuffix(e.Dst, ".jsonl") {
			transcriptID = id
			break
		}
	}
	if transcriptID < 0 {
		t.Fatal("no installed transcript entry found among InstalledIDs")
	}
	transcriptEntry, _ := m.ByID(transcriptID)

	// Pick a second installed regular-file entry to tamper with (simulate
	// "changed since install") — it must survive the delete.
	tamperedID := -1
	for _, id := range finishedPlan.InstalledIDs {
		if id == transcriptID {
			continue
		}
		if e, _ := m.ByID(id); e.IsRegular() {
			tamperedID = id
			break
		}
	}
	var tamperedEntry transfer.Entry
	if tamperedID >= 0 {
		tamperedEntry, _ = m.ByID(tamperedID)
		if err := os.WriteFile(tamperedEntry.Dst, []byte("tampered after install\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	code2, out2, errOut2 := run(t, env, "abandon", jobID, "--delete-destination-files")
	if code2 != ExitOK {
		t.Fatalf("abandon: exit %d\nstdout: %s\nstderr: %s", code2, out2, errOut2)
	}
	if _, err := os.Stat(transcriptEntry.Dst); !os.IsNotExist(err) {
		t.Errorf("genuinely installed, unmodified transcript must be deleted: %v", err)
	}
	if !strings.Contains(out2, transcriptEntry.Dst) {
		t.Errorf("abandon must name the deleted file:\n%s", out2)
	}
	if tamperedID >= 0 {
		if _, err := os.Stat(tamperedEntry.Dst); err != nil {
			t.Errorf("tampered (hash-mismatched) installed file must survive: %v", err)
		}
	}
	j3, _, err := job.Open(a.paths.DataDir, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if j3.Outcome != "abandoned" || !j3.Finished {
		t.Errorf("journal after abandon = %+v", j3)
	}
}

// TestAbandonDeleteRefusesWhileDestinationSessionLive is ruling R-P3-23e:
// --delete-destination-files must check dst.ClaudeStatus first and refuse
// (exit 3, no override) while the destination session is still running —
// deleting files a live Claude process might still have open is unsafe.
func TestAbandonDeleteRefusesWhileDestinationSessionLive(t *testing.T) {
	env, home := testEnv(t)
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")

	dstHome := filepath.Join(t.TempDir(), "home", "bob")
	dstPaths := session.Paths{Home: dstHome, ConfigDir: filepath.Join(dstHome, ".claude"), GlobalJSON: filepath.Join(dstHome, ".claude.json"), DataDir: filepath.Join(dstHome, ".local", "share", "claude-teleport"), ProcRoot: "/proc"}
	if err := os.MkdirAll(dstPaths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// A real, long-lived process stands in for the destination's Claude —
	// dst.ClaudeStatus needs a REAL pid + matching /proc start time (a.
	// localEndpoint always uses ProcRoot "/proc"), not a fake table.
	pid := startFakeRunner(t)
	start, err := session.ProcStartTime("/proc", pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstPaths.SessionsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	reg, _ := json.Marshal(map[string]any{
		"pid": pid, "sessionId": tsid, "cwd": "/home/bob/work", "procStart": start,
		"version": "2.1.247", "status": "busy", "tmux": "", "updatedAt": time.Now().UnixMilli(),
	})
	if err := os.WriteFile(filepath.Join(dstPaths.SessionsDir(), strconv.Itoa(pid)+".json"), reg, 0o600); err != nil {
		t.Fatal(err)
	}

	j, err := job.New(dataDir, tsid)
	if err != nil {
		t.Fatal(err)
	}
	j.SessionID, j.Direction = tsid, "to"
	j.SourceHost, j.DestHost = "src.example", "dest.example"
	m := &transfer.Manifest{Version: 1, JobID: tsid, SessionID: tsid}
	if err := m.Save(j.ManifestPath()); err != nil {
		t.Fatal(err)
	}
	plan := &orchestrate.Plan{
		JobID: tsid, Session: &session.Session{ID: session.ID(tsid)},
		DestInfo: remote.HostInfo{Hostname: "dest.example"}, ManifestPath: j.ManifestPath(),
		Options: orchestrate.Options{
			Direction: "to", Selector: session.Selector{ID: session.ID(tsid)}, State: "auto",
			LocalDest: &dstPaths, ExitTimeout: 10 * time.Second, StartTimeout: 10 * time.Second,
		},
	}
	if j.Plan, err = plan.ToJSON(); err != nil {
		t.Fatal(err)
	}
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}

	code, out, stderr := run(t, env, "abandon", tsid, "--delete-destination-files")
	if code != ExitRefused {
		t.Fatalf("exit %d, want ExitRefused; stdout %s stderr %s", code, out, stderr)
	}
	for _, want := range []string{"still running", "/exit"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", stderr, want)
		}
	}
	got, _, err := job.Open(dataDir, tsid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome == "abandoned" || got.Finished {
		t.Errorf("journal must not be touched by a refused delete: %+v", got)
	}
}

// TestAbandonCompletesLocalSideWhenDestinationUnreachable is ruling
// R-P3-23f: the destination being unreachable must not block the local
// side (journal marked abandoned) — exit 4, message says destination
// clean-up is pending. Re-running abandon afterward is allowed and
// retries only the destination side (still unreachable here), without
// re-doing (or re-announcing) the already-completed local side.
func TestAbandonCompletesLocalSideWhenDestinationUnreachable(t *testing.T) {
	env, home := testEnv(t)
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")

	j, err := job.New(dataDir, tsid)
	if err != nil {
		t.Fatal(err)
	}
	j.SessionID, j.Direction = tsid, "to"
	j.SourceHost, j.DestHost = "src.example", "dest.example"
	m := &transfer.Manifest{Version: 1, JobID: tsid, SessionID: tsid}
	if err := m.Save(j.ManifestPath()); err != nil {
		t.Fatal(err)
	}
	plan := &orchestrate.Plan{
		JobID: tsid, Session: &session.Session{ID: session.ID(tsid)},
		DestInfo: remote.HostInfo{Hostname: "dest.example"}, ManifestPath: j.ManifestPath(),
		Options: orchestrate.Options{
			// 127.0.0.1:1 (loopback, nothing listening) with no ssh
			// agent/keys in env fails fast and deterministically, with no
			// real network or DNS dependency (sshx checks for available
			// auth before ever attempting the TCP connect).
			Direction: "to", Target: "bob@127.0.0.1:1",
			Selector: session.Selector{ID: session.ID(tsid)}, State: "auto",
			ExitTimeout: 5 * time.Second, StartTimeout: 5 * time.Second,
		},
	}
	if j.Plan, err = plan.ToJSON(); err != nil {
		t.Fatal(err)
	}
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}

	code, out, stderr := run(t, env, "abandon", tsid)
	if code != ExitUnreachable {
		t.Fatalf("exit %d, want ExitUnreachable; stdout %s stderr %s", code, out, stderr)
	}
	if !strings.Contains(out, "abandoned locally") {
		t.Errorf("stdout = %q, want it to say the local side completed", out)
	}
	if !strings.Contains(stderr, "clean-up is pending") {
		t.Errorf("stderr = %q, want it to say destination clean-up is pending", stderr)
	}
	got, _, err := job.Open(dataDir, tsid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "abandoned" || !got.Finished {
		t.Errorf("local side must complete even though the destination is unreachable: %+v", got)
	}

	// Retry: allowed, retries only the (still unreachable) destination
	// side — no second "abandoned locally".
	code2, out2, stderr2 := run(t, env, "abandon", tsid)
	if code2 != ExitUnreachable {
		t.Fatalf("retry exit %d, want ExitUnreachable; stdout %s stderr %s", code2, out2, stderr2)
	}
	if strings.Contains(out2, "abandoned locally") {
		t.Errorf("retry must not re-mark the already-abandoned local side: %s", out2)
	}
}

// TestAbandonNonUnreachableEndpointsFailureKeepsItsOwnExitCode is ruling
// R-P3-23k: not every a.endpoints failure is "the destination is
// unreachable" — a stored target that no longer even parses (a usage
// error) must keep its OWN classification (exit 2, matching every other
// command's a.fail), never get flattened to ExitUnreachable, and must
// NOT trigger R-P3-23f's "mark abandoned locally, clean-up pending"
// special case (which is reserved for a genuine dial/UnreachableError).
func TestAbandonNonUnreachableEndpointsFailureKeepsItsOwnExitCode(t *testing.T) {
	env, home := testEnv(t)
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")

	j, err := job.New(dataDir, tsid)
	if err != nil {
		t.Fatal(err)
	}
	j.SessionID, j.Direction = tsid, "to"
	j.SourceHost, j.DestHost = "src.example", "dest.example"
	m := &transfer.Manifest{Version: 1, JobID: tsid, SessionID: tsid}
	if err := m.Save(j.ManifestPath()); err != nil {
		t.Fatal(err)
	}
	plan := &orchestrate.Plan{
		JobID: tsid, Session: &session.Session{ID: session.ID(tsid)},
		DestInfo: remote.HostInfo{Hostname: "dest.example"}, ManifestPath: j.ManifestPath(),
		Options: orchestrate.Options{
			// An empty Target fails sshx.ParseTarget ("ssh target: empty")
			// before any dial is even attempted — a usage error, not an
			// unreachable one.
			Direction: "to", Target: "",
			Selector: session.Selector{ID: session.ID(tsid)}, State: "auto",
			ExitTimeout: 5 * time.Second, StartTimeout: 5 * time.Second,
		},
	}
	if j.Plan, err = plan.ToJSON(); err != nil {
		t.Fatal(err)
	}
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}

	code, out, stderr := run(t, env, "abandon", tsid)
	if code != ExitUsage {
		t.Fatalf("exit %d, want ExitUsage; stdout %s stderr %s", code, out, stderr)
	}
	if strings.Contains(out, "abandoned locally") || strings.Contains(stderr, "clean-up is pending") {
		t.Errorf("a non-unreachable failure must not trigger the R-P3-23f local-abandon/clean-up-pending path: stdout %q stderr %q", out, stderr)
	}
	got, _, err := job.Open(dataDir, tsid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome == "abandoned" || got.Finished {
		t.Errorf("journal must not be marked abandoned on a non-unreachable failure: %+v", got)
	}
}

// TestAbandonRetryDoesNotDuplicateHistoryRecord is ruling R-P3-23l:
// src.Record/dst.Record append a history.jsonl row each — running abandon
// a second time (retrying, say, the destination side) on an
// already-abandoned job must not append a second "abandoned" row.
func TestAbandonRetryDoesNotDuplicateHistoryRecord(t *testing.T) {
	env, home := testEnv(t)
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")

	dstHome := filepath.Join(t.TempDir(), "home", "bob")
	dstPaths := session.Paths{Home: dstHome, ConfigDir: filepath.Join(dstHome, ".claude"), GlobalJSON: filepath.Join(dstHome, ".claude.json"), DataDir: filepath.Join(dstHome, ".local", "share", "claude-teleport"), ProcRoot: "/proc"}
	if err := os.MkdirAll(dstPaths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}

	j, err := job.New(dataDir, tsid)
	if err != nil {
		t.Fatal(err)
	}
	j.SessionID, j.Direction = tsid, "to"
	j.SourceHost, j.DestHost = "src.example", "dest.example"
	m := &transfer.Manifest{Version: 1, JobID: tsid, SessionID: tsid}
	if err := m.Save(j.ManifestPath()); err != nil {
		t.Fatal(err)
	}
	plan := &orchestrate.Plan{
		JobID: tsid, Session: &session.Session{ID: session.ID(tsid)},
		DestInfo: remote.HostInfo{Hostname: "dest.example"}, ManifestPath: j.ManifestPath(),
		Options: orchestrate.Options{
			Direction: "to", Selector: session.Selector{ID: session.ID(tsid)}, State: "auto",
			LocalDest: &dstPaths, ExitTimeout: 10 * time.Second, StartTimeout: 10 * time.Second,
		},
	}
	if j.Plan, err = plan.ToJSON(); err != nil {
		t.Fatal(err)
	}
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}

	if code, out, stderr := run(t, env, "abandon", tsid); code != ExitOK {
		t.Fatalf("first abandon: exit %d\nstdout: %s\nstderr: %s", code, out, stderr)
	}
	if code, out, stderr := run(t, env, "abandon", tsid); code != ExitOK {
		t.Fatalf("second abandon (retry): exit %d\nstdout: %s\nstderr: %s", code, out, stderr)
	}

	raw, err := os.ReadFile(filepath.Join(job.Dir(dataDir, tsid), "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(l) != "" {
			lines++
		}
	}
	if lines != 1 {
		t.Errorf("history.jsonl has %d line(s) after abandon ran twice, want exactly 1:\n%s", lines, raw)
	}
}

// TestAbandonRefusesFromTheJobsOtherEnd covers the deferred carry: a job
// is driven from one side (the source for --to), and the plan's stored
// target names the far end AS SEEN FROM THERE — so an abandon run on the
// destination would re-dial that name from the wrong machine. Refuse with
// a message naming the host to run it on, rather than dialling something
// arbitrary.
func TestAbandonRefusesFromTheJobsOtherEnd(t *testing.T) {
	here, err := os.Hostname()
	if err != nil || here == "" {
		t.Skip("no hostname on this machine")
	}
	env, home := testEnv(t)
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
	j, _ := job.New(dataDir, tsid)
	j.Direction = "to"
	p := &orchestrate.Plan{
		Options:    orchestrate.Options{Direction: "to", Target: "dest.private"},
		Session:    &session.Session{ID: session.ID(tsid)},
		SourceInfo: remote.HostInfo{Hostname: "laptop.example"},
		// This machine IS the job's destination: abandon belongs on the
		// source, which drove the job.
		DestInfo: remote.HostInfo{Hostname: here},
	}
	j.Plan, _ = p.ToJSON()
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := run(t, env, "abandon", tsid)
	if code != ExitUsage {
		t.Fatalf("abandon from the destination = %d, want %d: %s", code, ExitUsage, stderr)
	}
	for _, want := range []string{"laptop.example", "destination"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal must mention %q:\n%s", want, stderr)
		}
	}
	got, _, err := job.Open(dataDir, tsid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome == "abandoned" {
		t.Error("a refused abandon must not mark the journal")
	}
}
