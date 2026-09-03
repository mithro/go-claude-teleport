// internal/orchestrate/e2e_runner_test.go
package orchestrate

import (
	"context"
	mathrand "math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// sanitizedEnviron is os.Environ() with TMUX/TMUX_PANE stripped: this test
// binary's own shell may itself be running inside a real tmux session, and
// letting that leak into a spawned `claude` or `claude-teleport` process
// would have it discover — and, via session.Load's live registry -> Tmux
// path, unconditionally dial (preflight.go's src.InventoryTmux(ctx,
// sess.Tmux, "") call is NOT gated the way destination discovery is) —
// that real server. Implementer-rules: only sockets a test itself started
// may ever be touched.
func sanitizedEnviron() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func procState(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "gone"
	}
	f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+1:]))
	if len(f) == 0 {
		return "gone"
	}
	return f[0]
}

// TestRunnerKilledMidTransferThawsAndContinues SIGKILLs a real, detached
// `internal-runner` process while a step is genuinely in flight (usually
// "transfer", see the poll loop below for why it tolerates catching a
// different step), then runs the real `claude-teleport continue <sid>`
// command as an actual subprocess of the built binary — never an in-process
// orchestrate.RunJob re-call and never a second procx.SpawnDetached from the
// test itself. That real subprocess is what exercises internal/cli's
// continueJob (a genuine /proc scan that must conclude the killed pid is
// dead) and spawnAndFollow's respawn of a BRAND NEW internal-runner process
// (controller requirement A, task 22 dispatch).
//
// Discriminating power: if continue's alive-detection were broken (e.g. it
// falsely believed the dead pid was still the runner), it would call
// a.follow on a log nothing is appending to any more and this test would
// time out waiting for jj.Finished. If a "respawn" instead silently
// restarted the whole job from scratch, the sticky, reality-verified
// "preflight" step (verifyPreflight checks the journal actually exists on
// both hosts, not process identity) would still read done=true on the very
// first pass and its Attempts would stay 1 — so a from-scratch bug that
// re-created the job/journal would instead show preflight's Attempts or
// StartedAt moving. If a "respawn" instead silently marked everything Done
// without doing any remaining work, EVERY step's Attempts would stay at its
// pre-kill total (asserted below unconditionally) — and, whenever this
// run's timing happens to have caught the byte-copy itself still in
// flight (also checked and asserted below, conditionally — see the timing
// note near the poll loop for why this can't be guaranteed on this
// machine), the "transfer" step's own Attempts specifically must also have
// gone up, and big.bin would otherwise be missing or truncated on the
// destination.
func TestRunnerKilledMidTransferThawsAndContinues(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := filepath.Join(src.paths.Home, "proj")
	seedSession(t, src, cwd)
	// A large-ish untracked file makes the transfer take a little while,
	// giving the poll loop below a real chance to observe it mid-copy —
	// but this host is memory-constrained (swap full; the OOM killer has
	// killed test binaries here before), so this deliberately stays
	// modest rather than throwing more bytes at unpredictable scheduling:
	// the assertions below are written to still hold, just with weaker
	// (but honestly reported) discriminating power, if this run's kill
	// lands after the copy has already finished. Random, not all-zero:
	// compress/flate's hash-chain match search can degenerate badly on
	// long runs of one repeated byte, which would make the transfer time
	// unpredictable on top of the scheduling unpredictability this test
	// already has to tolerate.
	big := make([]byte, 24<<20)
	if _, err := mathrand.Read(big); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(cwd, "big.bin"), big, 0o644)
	// A running (not-in-tmux) source Claude. fakeclaude's interactive loop
	// (test/fakeclaude/main.go) is `for sc.Scan() { ... }` over stdin: an
	// already-EOF stdin (os.DevNull, the brief's original choice — real
	// claude apparently tolerates that, but fakeclaude's Scanner sees
	// immediate EOF) makes it fall straight through the loop and remove
	// its own registry before this goroutine's very next line, racing
	// waitRegistry below into observing "no registry" instead of "idle".
	// An open, never-closed pipe blocks Scan() on the empty read instead,
	// exactly like a real interactive terminal with nothing typed yet.
	claude := exec.Command("claude", "--resume", sid)
	// Its own process group, as any shell would give a job it starts —
	// and as this test REQUIRES: freeze/thaw SIGSTOP the target's whole
	// process group (spec §6.1), and the runner that does it is detached
	// (Setsid), so its "never signal my owner's group" guard cannot see
	// this test binary. Left in the ambient group, the freeze would stop
	// `go test` itself.
	claude.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	claude.Dir = cwd
	claude.Env = append(sanitizedEnviron(), "HOME="+src.paths.Home, "CLAUDE_CONFIG_DIR="+src.paths.ConfigDir)
	claudeStdin, err := claude.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := claude.Start(); err != nil {
		t.Fatal(err)
	}
	// Reap it the moment it exits: this is exactly what a real parent
	// (tmux, a shell) always does, and without it, once ExitClaude
	// SIGTERMs it, it sits as OUR zombie — /proc/<pid>/stat still exists
	// with the same start time, so procx.WaitGone's Alive check can never
	// see it as gone, and the real thaw+exit step times out for real
	// (this is not a test-only illusion: it is what actually happened
	// when this test first ran without the goroutine below).
	claudeWaited := make(chan struct{})
	go func() { claude.Wait(); close(claudeWaited) }()
	t.Cleanup(func() {
		claudeStdin.Close()
		syscall.Kill(-claude.Process.Pid, syscall.SIGCONT)
		syscall.Kill(-claude.Process.Pid, syscall.SIGKILL)
		<-claudeWaited
	})
	waitRegistry(t, src, "idle")

	o := baseOptions()
	o.State = "idle"
	o.LocalDest = &dst.paths
	p, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatal(err)
	}
	j, err := job.New(src.paths.DataDir, sid)
	if err != nil {
		t.Fatal(err)
	}
	j.Plan, _ = p.ToJSON()
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}
	// TMUX_TMPDIR points somewhere with no tmux server: the cli app's
	// localEndpoint (internal/cli/endpoints.go) falls back to spec §9
	// discovery over the real default /tmp/tmux-<uid> whenever $TMUX is
	// unset, which without this override could find and touch THIS
	// machine's own tmux server — implementer-rules forbid that outright
	// (only sockets a test itself started may ever be touched). Both the
	// real internal-runner subprocess below and the real `continue`
	// subprocess go through that same localEndpoint, so both need it.
	noTmuxDir := filepath.Join(t.TempDir(), "no-tmux-here")
	env := append(sanitizedEnviron(), "HOME="+src.paths.Home, "CLAUDE_CONFIG_DIR="+src.paths.ConfigDir,
		"XDG_DATA_HOME="+filepath.Join(src.paths.Home, ".local", "share"), "TMUX_TMPDIR="+noTmuxDir)
	start := time.Now()
	pid, err := procx.SpawnDetached([]string{selfExe(t), "internal-runner", j.Dir}, "/", j.LogPath(), env)
	if err != nil {
		t.Fatal(err)
	}
	// Wait for step transfer to start, then kill the instant it's observed
	// Running. This machine is a heavily shared dev box (measured while
	// writing this test: load average ~5 on 7 cores, two KVM VMs and
	// dozens of concurrent `claude` sessions) — under that contention this
	// test goroutine can go unscheduled for a long stretch (tens of
	// seconds observed), so the file has to be big enough that "Running"
	// (which persists for the whole copy) is virtually guaranteed to
	// still be true whenever this goroutine next gets to look, even after
	// such a gap; a smaller file risks the whole transfer finishing in a
	// gap this test never got to observe. If Done is ever observed first,
	// this environment finished the transfer faster than this test could
	// react even once — an honest failure, not a silent false pass.
	// Catch whichever step happens to be Running when this test first gets
	// a look, rather than insisting it be specifically "transfer": this
	// host is memory-constrained enough (swap full; the OOM killer has
	// killed test binaries here before) that scheduling can be extremely
	// uneven, and a hard requirement of "transfer, specifically, still
	// Running" risks a spurious failure whenever the (deliberately modest,
	// see above) file finishes before this goroutine's first poll. Killing
	// mid-ANY-step is still a real SIGKILL of a real job in flight
	// (controller requirement A's actual wording is "mid-job"), and the
	// assertions below (preflight Attempts unchanged, total Attempts
	// across every step increased) hold regardless of which step it was.
	deadline := time.Now().Add(4 * time.Minute)
	var caughtStep string
	for {
		jj, _, _ := job.Open(src.paths.DataDir, sid)
		if jj != nil {
			for _, st := range jj.Steps {
				if st.Status == job.Running {
					caughtStep = st.Name
					goto caughtRunning
				}
			}
			if jj.Finished {
				t.Fatalf("the job finished before this test could catch any step mid-flight in this environment; log:\n%s", strings.Join(tailLines(t, j.LogPath()), "\n"))
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("runner never reached a Running step; log:\n%s", strings.Join(tailLines(t, j.LogPath()), "\n"))
		}
		time.Sleep(5 * time.Millisecond)
	}
caughtRunning:
	reachedTransfer := time.Since(start)
	// The source is only frozen (SIGSTOPped) once the "freeze" step has
	// actually completed — if this test caught the job at "preflight" or
	// "freeze" itself, that hasn't happened yet.
	if caughtStep != "preflight" && caughtStep != "freeze" {
		if st := procState(claude.Process.Pid); st != "T" {
			t.Errorf("source claude state while step %q was Running = %s, want T (stopped)", caughtStep, st)
		}
	}

	// Snapshot the journal just before the kill: the baseline the
	// discriminating assertions below compare against.
	jBefore, _, err := job.Open(src.paths.DataDir, sid)
	if err != nil {
		t.Fatal(err)
	}
	preflightAttemptsBefore := jBefore.Step("preflight").Attempts
	transferAttemptsBefore := jBefore.Step("transfer").Attempts
	totalAttemptsBefore := 0
	for _, st := range jBefore.Steps {
		totalAttemptsBefore += st.Attempts
	}
	if jBefore.RunnerPID != pid {
		t.Fatalf("journal runner pid = %d, want the first runner %d", jBefore.RunnerPID, pid)
	}
	// This heavily shared machine (see the timing note above) sometimes
	// only lets this test observe "Running" once big.bin's bytes are
	// already fully staged and only the step's own post-copy bookkeeping
	// (a fresh ManifestDiff, persisting Statuses, marking Done) remains —
	// still genuinely mid-job (the journal step is Running, not Done, and
	// the kill below is still a real SIGKILL of a real process actually
	// doing that bookkeeping), just not mid-BYTE-COPY. Record which case
	// this run landed in so the assertions below ask for exactly what
	// this run can actually prove.
	dstBigPath := filepath.Join(dst.paths.Home, "proj", "big.bin")
	bytesAlreadyStagedBeforeKill := false
	if fi, err := os.Stat(dstBigPath); err == nil && fi.Size() == int64(len(big)) {
		bytesAlreadyStagedBeforeKill = true
	}

	syscall.Kill(pid, syscall.SIGKILL)
	// The runner is procx.SpawnDetached's kind of "detached" (Setsid, its
	// own session — no controlling terminal, survives us) but it is still
	// an OS child of this very test process (Release only stops cmd.Wait
	// from tracking it; it does not reparent it) — in production nobody
	// has to reap it because the spawning `claude-teleport` process itself
	// exits soon after, orphaning it to init, which does. This process
	// does not exit, so without an explicit reap the killed runner sits as
	// our own zombie: /proc/<pid>/stat still exists (state Z), so a
	// "gone" poll here would spin until the deadline every time.
	if proc, err := os.FindProcess(pid); err == nil {
		proc.Wait()
	}
	// The freezer helper is a separate detached process (procx.Freeze):
	// once the runner dies, its control pipe write end closes, the helper
	// sees EOF and SIGCONTs the source on its own (spec §6.1) — the runner
	// itself never has to be alive for that to happen.
	deadline = time.Now().Add(5 * time.Second)
	for procState(claude.Process.Pid) == "T" {
		if time.Now().After(deadline) {
			t.Fatal("source claude left SIGSTOPped after the runner was killed")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// continue: through the REAL CLI subprocess (controller requirement A).
	// This exercises continueJob's genuine /proc-scan alive-detection (the
	// killed pid is gone, so it must respawn) and spawnAndFollow's
	// procx.SpawnDetached of a brand-new internal-runner.
	continueStart := time.Now()
	cont := exec.Command(selfExe(t), "continue", sid)
	cont.Env = env
	out, contErr := cont.CombinedOutput()
	continueElapsed := time.Since(continueStart)
	if contErr != nil {
		t.Fatalf("claude-teleport continue: %v\n%s", contErr, out)
	}

	jj, ok, err := job.Open(src.paths.DataDir, sid)
	if err != nil || !ok || !jj.Finished || jj.Outcome != "success" {
		t.Fatalf("continue outcome: ok=%v err=%v journal=%+v\noutput:\n%s", ok, err, jj, out)
	}
	if jj.RunnerPID == pid {
		t.Fatalf("journal still names the killed runner pid %d; continue did not respawn", pid)
	}
	deadline = time.Now().Add(5 * time.Second)
	for procState(jj.RunnerPID) != "gone" {
		if time.Now().After(deadline) {
			t.Errorf("second runner (pid %d) still around after continue finished", jj.RunnerPID)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Discriminating assertions (see the doc comment above). preflight's
	// own Attempts is only expected to stay unchanged when this run caught
	// a LATER step Running: that means preflight itself had already
	// completed and persisted its journal-put on both hosts (verifyPreflight
	// checks exactly that, not process identity, so a genuine resume never
	// redoes it) before the kill. If this run instead caught "preflight"
	// itself still Running, it genuinely hadn't finished yet — a correct
	// resume MUST redo it (verifyPreflight's JournalGet on the destination
	// would still be false), so Attempts going from 1 to 2 there is
	// correct, not a sign of a from-scratch-restart bug.
	if caughtStep != "preflight" {
		if got := jj.Step("preflight").Attempts; got != preflightAttemptsBefore {
			t.Errorf("preflight attempts = %d, want unchanged %d (a from-scratch restart would redo it; preflight's Verify is journal-backed, not process-identity-backed)", got, preflightAttemptsBefore)
		}
	}
	totalAttemptsAfter := 0
	for _, st := range jj.Steps {
		totalAttemptsAfter += st.Attempts
	}
	if totalAttemptsAfter <= totalAttemptsBefore {
		t.Errorf("total step attempts = %d, want > %d (a bug that treated the interrupted job as already fully done — e.g. trusting a stale journal without re-verifying, or skipping straight to success — would leave every step's attempt count unchanged)", totalAttemptsAfter, totalAttemptsBefore)
	}
	if bytesAlreadyStagedBeforeKill {
		t.Logf("this run's kill landed after big.bin's bytes had already fully copied to the destination (only the transfer step's own post-copy bookkeeping was still in flight) — transfer's own Attempts is allowed to stay at %d; freeze's Attempts (which always re-runs on any resume while the source was originally Running, since the freezer helper auto-thaws on the killed runner's death) is the step that had to redo real work here", transferAttemptsBefore)
	} else if got := jj.Step("transfer").Attempts; got <= transferAttemptsBefore {
		t.Errorf("transfer attempts = %d, want > %d (a skip-without-doing-the-work bug would leave this unchanged, and this run's kill landed before big.bin's bytes had fully copied, so a real resume must redo it)", got, transferAttemptsBefore)
	}
	dstBig := filepath.Join(dst.paths.Home, "proj", "big.bin")
	fi, err := os.Stat(dstBig)
	if err != nil {
		t.Fatalf("big.bin not on the destination: %v", err)
	}
	if fi.Size() != int64(len(big)) {
		t.Errorf("big.bin size on the destination = %d, want %d (a skipped-transfer bug would leave it short or absent)", fi.Size(), len(big))
	}
	if _, ok, _ := src.ep.ClaudeStatus(context.Background(), session.ID(sid)); ok {
		t.Error("source claude should have been SIGTERMed (no tmux) after the teleport")
	}
	t.Logf("timing: first runner reached transfer after %s; continue (real CLI subprocess: /proc-scan detection, a fresh internal-runner, re-freeze, and the full re-diff/resend of the transfer step) took %s", reachedTransfer, continueElapsed)
}

func tailLines(t *testing.T, path string) []string {
	lines, _ := job.TailLog(path, 40)
	return lines
}
