//go:build integration

// test/integration/integration_test.go
//
// Layer 1 integration tests: the REAL claude-teleport binary, dialing with
// its in-process x/crypto/ssh client as alice through the jump host
// (--via jump), against fakeclaude on source and dest, driven through the
// Task 24 docker-compose harness. See task-25-report.md for the seven
// controller-required scenarios, per-test evidence, and two internal/
// bugs this suite found (never fixed here, per the implementer rules).
package integration

import (
	"strings"
	"testing"
	"time"
)

// scenario 1: a running session teleports to a running session on dest;
// the source only exits (and its pane only shows the placeholder) once
// dest is confirmed resumed.
func TestRunningSessionTeleportsToRunningOnDest(t *testing.T) {
	// I3: dump docker compose logs + job dirs on failure, before TestMain's
	// suite-wide `down -v` tears everything down.
	t.Cleanup(func() {
		if t.Failed() {
			dumpDiagnostics(t)
		}
	})
	reset(t)
	trustJump(t, "source", "alice")
	ensureTmuxServer(t, "dest", "alice")
	sid := newSID(t)
	seed(t, "source", "alice", "/home/alice/proj", sid)
	startInTmux(t, "source", "alice", "/home/alice/proj", sid, "main", "")

	out, code := teleport(t, "source", "alice", sid+" --to dest")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	sh(t, "dest", "alice", "test -f "+transcriptPath("/home/alice", "/home/alice/proj", sid))
	reg := waitRegistry(t, "dest", "alice", sid, "idle")
	if !strings.Contains(reg, `"tmux":"main:`) {
		t.Errorf("dest registry tmux field: %s", reg)
	}
	// #{pane_current_command} specifically, not a substring match against
	// the whole list-panes line: the WINDOW is always named "claude"
	// (startInTmux/OpenWindow), so a plain strings.Contains(panes, "claude")
	// would pass even if the pane's actual foreground process were bash or
	// sh — it proves nothing about whether claude is really running there.
	// Targeting the one pane by ref and asserting the command column's
	// exact value (not "claude-teleport", which also contains "claude")
	// is the only check that discriminates a live claude from a stuck
	// shell or a leftover placeholder.
	if cmd := strings.TrimSpace(sh(t, "dest", "alice", "tmux list-panes -t main:claude -F '#{pane_current_command}'")); cmd != "claude" {
		t.Errorf("dest pane should be running claude, got pane_current_command=%q", cmd)
	}
	if registry(t, "source", "alice", sid) != "" {
		t.Error("source claude still registered: it must exit only after dest is confirmed")
	}
	// The placeholder is TYPED AND ENTERED (tmuxx.TypeCommand sends Enter),
	// so the pane is left RUNNING it, waiting at its "Enter = resume"
	// prompt — by then the screen shows its banner, not the command line
	// that started it. Assert both halves of that: the pane's foreground
	// process is the placeholder, and the placeholder says where the
	// session went (spec §6.3).
	if pane := sh(t, "source", "alice", "tmux capture-pane -p -t main:claude"); !strings.Contains(pane, "teleported to dest") || !strings.Contains(pane, sid[:8]) {
		t.Errorf("source pane should show the placeholder banner for %s:\n%s", sid, pane)
	}
	if cmd := sh(t, "source", "alice", "tmux list-panes -t main:claude -F '#{pane_current_command}'"); !strings.Contains(cmd, "claude-teleport") {
		t.Errorf("source pane should be running the placeholder, got %q", cmd)
	}
	if hist := sh(t, "source", "alice", "cat ~/.local/share/claude-teleport/jobs/"+sid+"/history.jsonl"); !strings.Contains(hist, `"outcome":"success"`) {
		t.Errorf("history: %s", hist)
	}
}

// scenario 2: teleport launched from inside the fakeclaude session itself
// (FAKECLAUDE_RUN_CHILD, mirroring Claude Code's `!` bash mode) SIGSTOPs
// the parent for the duration of the transfer and SIGCONTs it afterwards.
func TestBangModeStopsParentDuringTransfer(t *testing.T) {
	// I3: dump docker compose logs + job dirs on failure, before TestMain's
	// suite-wide `down -v` tears everything down.
	t.Cleanup(func() {
		if t.Failed() {
			dumpDiagnostics(t)
		}
	})
	reset(t)
	trustJump(t, "source", "alice")
	ensureTmuxServer(t, "dest", "alice")
	sid := newSID(t)
	seed(t, "source", "alice", "/home/alice/proj", sid)
	// 1 GiB, for the same reason as scenario 3: 256 MB crosses this
	// harness's docker bridge in about a second, which an external poll
	// loop whose own docker-exec round trip is ~200-300ms cannot reliably
	// catch.
	sh(t, "source", "alice", "head -c 1073741824 /dev/urandom > /home/alice/proj/big.bin")
	// NOT startInTmux: fakeclaude runs FAKECLAUDE_RUN_CHILD (its `!`-mode
	// stand-in) before it ever reports "idle" — exactly as Claude Code is
	// mid-turn while a `! claude-teleport` runs — so waiting for idle here
	// would wait for the whole teleport to finish and leave no
	// mid-transfer to observe. "busy" is written before the child starts.
	sh(t, "source", "alice", "tmux -f /dev/null new-session -d -s main -n claude -c /home/alice/proj")
	sh(t, "source", "alice", "tmux send-keys -t main:claude \"FAKECLAUDE_RUN_CHILD='claude-teleport --to dest "+teleportOpts+"' claude --resume "+sid+"\" Enter")
	pid := pidFromRegistry(waitRegistry(t, "source", "alice", sid, "busy"))

	sawStopped := false
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := shCode(t, "source", "alice", "cut -d' ' -f3 /proc/"+pid+"/stat")
		if strings.TrimSpace(st) == "T" {
			sawStopped = true
			break
		}
		if strings.Contains(st, "No such file") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !sawStopped {
		t.Error("parent fakeclaude was never observed SIGSTOPped during the transfer")
	}
	// !-mode: once teleported, the parent fakeclaude is meant to exit
	// entirely (spec: "this Claude will now exit") — bounded regardless of
	// its /proc state, so a stuck-but-not-"T" process cannot hang the
	// suite the way an unbounded loop would.
	deadline = time.Now().Add(60 * time.Second)
	gone := false
	for time.Now().Before(deadline) {
		if _, code := shCode(t, "source", "alice", "test -d /proc/"+pid); code != 0 {
			gone = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !gone {
		st := procState(t, "source", "alice", pid)
		t.Fatalf("parent fakeclaude (pid %s) never exited after the transfer (ps STAT %q)", pid, st)
	}
	waitRegistry(t, "dest", "alice", sid, "idle")
}

// scenario 3: kill the detached internal-runner mid-transfer, then
// `continue`; the job completes without the source being left stuck.
func TestKilledRunnerThenContinueCompletes(t *testing.T) {
	// I3: dump docker compose logs + job dirs on failure, before TestMain's
	// suite-wide `down -v` tears everything down.
	t.Cleanup(func() {
		if t.Failed() {
			dumpDiagnostics(t)
		}
	})
	reset(t)
	trustJump(t, "source", "alice")
	ensureTmuxServer(t, "dest", "alice")
	sid := newSID(t)
	seed(t, "source", "alice", "/home/alice/proj", sid)
	// 1 GiB: on this harness's docker-bridge networking a 256 MB transfer
	// (the brief's figure) completes in well under a second — too fast to
	// reliably catch "running" from an external poll loop whose own
	// docker-exec round trip is itself ~100-300ms. 1 GiB gives a multi-
	// second window instead of adding sleeps.
	sh(t, "source", "alice", "head -c 1073741824 /dev/urandom > /home/alice/proj/big.bin")
	startInTmux(t, "source", "alice", "/home/alice/proj", sid, "main", "")
	sh(t, "source", "alice", "nohup claude-teleport "+sid+" --to dest --state idle "+teleportOpts+" > /home/alice/fg.log 2>&1 &")

	deadline := time.Now().Add(60 * time.Second)
	for {
		st, _ := shCode(t, "source", "alice", `grep -A2 '"name": "transfer"' ~/.local/share/claude-teleport/jobs/`+sid+`/job.json | grep -q '"status": "running"' && echo yes`)
		if strings.Contains(st, "yes") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("transfer never started")
		}
		time.Sleep(100 * time.Millisecond)
	}
	// [c]laude-teleport, not claude-teleport: pkill -f matches against the
	// full command line, including its OWN invocation's argv on this same
	// docker-exec shell — an unbracketed pattern kills the pkill/sh
	// process itself (observed: exit 137) before it ever reaches the
	// runner.
	sh(t, "source", "alice", "pkill -9 -f '[c]laude-teleport internal-runner' || true")
	out, code := shCode(t, "source", "alice", "claude-teleport continue "+sid)
	if code != 0 {
		t.Fatalf("continue: %d\n%s", code, out)
	}
	sh(t, "dest", "alice", "test -f /home/alice/proj/big.bin")
	sh(t, "dest", "alice", "test -f "+transcriptPath("/home/alice", "/home/alice/proj", sid))
	// Destination-side end state, not just file presence: --state idle
	// means the destination's "shape" step /exit's the claude that "start"
	// just confirmed and (having created the window itself) kills it —
	// verified empirically against this exact harness (only the
	// ensureTmuxServer "_keepalive" placeholder session survives) and
	// matching TestE2EIdleNoTmuxDestination's own dest check
	// (internal/orchestrate/e2e_test.go). So the correct destination-side
	// assertion for an idle target is that the registry is gone, not that
	// it settles to a live "idle" status (that only happens for
	// --state running/suspended, see scenario 1).
	if reg := registry(t, "dest", "alice", sid); reg != "" {
		t.Errorf("dest claude should have exited for the idle target state, registry: %s", reg)
	}
}

// scenario 4: a network drop mid-transfer (pausing jump's sshd, which
// severs every ssh channel through it without touching DNS) fails the job
// loudly with a continuable journal; reconnecting and `continue` finishes
// the job.
func TestNetworkDropThenContinueCompletes(t *testing.T) {
	// I3: dump docker compose logs + job dirs on failure, before TestMain's
	// suite-wide `down -v` tears everything down.
	t.Cleanup(func() {
		if t.Failed() {
			dumpDiagnostics(t)
		}
	})
	reset(t)
	trustJump(t, "source", "alice")
	ensureTmuxServer(t, "dest", "alice")
	sid := newSID(t)
	seed(t, "source", "alice", "/home/alice/proj", sid)
	sh(t, "source", "alice", "head -c 1073741824 /dev/urandom > /home/alice/proj/big.bin")
	startInTmux(t, "source", "alice", "/home/alice/proj", sid, "main", "")
	sh(t, "source", "alice", "nohup claude-teleport "+sid+" --to dest --state idle "+teleportOpts+" > /home/alice/fg.log 2>&1 &")

	deadline := time.Now().Add(60 * time.Second)
	for {
		st, _ := shCode(t, "source", "alice", `grep -q '"name": "transfer"' ~/.local/share/claude-teleport/jobs/`+sid+`/job.json && grep -A2 '"name": "transfer"' ~/.local/share/claude-teleport/jobs/`+sid+`/job.json | grep -q '"status": "running"' && echo yes`)
		if strings.Contains(st, "yes") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("transfer never started")
		}
		time.Sleep(100 * time.Millisecond)
	}
	// The link must stay down long enough to BE a drop: the tool
	// deliberately survives a brief hiccup (TCP retransmits), and only
	// gives up when its ssh keepalives go unanswered for
	// ServerAliveInterval x ServerAliveCountMax (15s x 3 by default, so
	// ~45-60s — internal/sshx). A 3-second pause proves nothing either
	// way; jump therefore stays paused until the runner has actually
	// died, and is only unpaused for the `continue` below. `source` is
	// never paused, so the runner can still be observed throughout.
	compose(t, "pause", "jump")
	unpaused := false
	unpause := func() {
		if !unpaused {
			unpaused = true
			compose(t, "unpause", "jump")
		}
	}
	t.Cleanup(unpause)
	deadline = time.Now().Add(3 * time.Minute)
	for {
		if _, code := shCode(t, "source", "alice", "pgrep -f '[c]laude-teleport internal-runner'"); code != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runner did not stop after the network drop")
		}
		time.Sleep(500 * time.Millisecond)
	}
	unpause()
	journal := sh(t, "source", "alice", "cat ~/.local/share/claude-teleport/jobs/"+sid+"/job.json")
	if !strings.Contains(journal, `"outcome": "failed"`) {
		t.Errorf("job should have failed loudly with a continuable journal:\n%s", journal)
	}
	out, code := shCode(t, "source", "alice", "claude-teleport continue "+sid)
	if code != 0 {
		t.Fatalf("continue: %d\n%s", code, out)
	}
	sh(t, "dest", "alice", "test -f "+transcriptPath("/home/alice", "/home/alice/proj", sid))
	// Destination-side end state (see the identical comment in
	// TestKilledRunnerThenContinueCompletes): --state idle correctly ends
	// with no live claude on dest, verified empirically.
	if reg := registry(t, "dest", "alice", sid); reg != "" {
		t.Errorf("dest claude should have exited for the idle target state, registry: %s", reg)
	}
}

// scenario 5: dest is not logged in (FAKECLAUDE_FAIL=not-logged-in);
// teleport fails with the documented exit code, and the source session is
// left intact/resumable (registry entry present, pane still running
// claude, not the placeholder).
func TestNotLoggedInDestFailsSourceResumable(t *testing.T) {
	// I3: dump docker compose logs + job dirs on failure, before TestMain's
	// suite-wide `down -v` tears everything down.
	t.Cleanup(func() {
		if t.Failed() {
			dumpDiagnostics(t)
		}
	})
	reset(t)
	trustJump(t, "source", "alice")
	sid := newSID(t)
	seed(t, "source", "alice", "/home/alice/proj", sid)
	startInTmux(t, "source", "alice", "/home/alice/proj", sid, "main", "")
	// claude-teleport reaches dest over a fresh, non-interactive ssh exec
	// (never a login shell), so ~/.bashrc is never sourced there; only
	// sshd_config's AcceptEnv list or ~/.ssh/environment (created empty by
	// entrypoint.sh, with PermitUserEnvironment on) reach that process's
	// environment.
	sh(t, "dest", "alice", "echo FAKECLAUDE_FAIL=not-logged-in >> ~/.ssh/environment")
	t.Cleanup(func() { shCode(t, "dest", "alice", "sed -i '/FAKECLAUDE_FAIL/d' ~/.ssh/environment") })

	out, code := teleport(t, "source", "alice", sid+" --to dest --state idle")
	if code != 5 { // ExitNotResumed, internal/cli/cli.go
		t.Fatalf("expected exit 5 (not resumed), got %d\n%s", code, out)
	}
	reg := registry(t, "source", "alice", sid)
	if reg == "" {
		t.Error("source session registry entry should still be present (resumable)")
	}
	if pane := sh(t, "source", "alice", "tmux capture-pane -p -t main:claude"); strings.Contains(pane, "placeholder") {
		t.Errorf("source pane should still be running claude, not the placeholder:\n%s", pane)
	}
	// "Resumable" needs more than "not showing the placeholder": the
	// source claude process itself must not be left stopped (a bare
	// SIGCONT recovering a SIGSTOPped foreground job of an interactive
	// tmux/bash pane does not reliably un-background it — see
	// task-25-report.md's freeze/thaw finding). ps STAT "T" is
	// stopped/traced.
	if pid := pidFromRegistry(reg); pid != "" {
		if st := procState(t, "source", "alice", pid); st == "T" {
			t.Errorf("source claude process (pid %s) is left SIGSTOPped, not resumable", pid)
		}
	}
}

// scenario 6: a suspended source teleports to a suspended (idle process,
// placeholder pane) state on dest, confirmed resumable there.
func TestSuspendedSourceEndsSuspendedOnDest(t *testing.T) {
	// I3: dump docker compose logs + job dirs on failure, before TestMain's
	// suite-wide `down -v` tears everything down.
	t.Cleanup(func() {
		if t.Failed() {
			dumpDiagnostics(t)
		}
	})
	reset(t)
	trustJump(t, "source", "alice")
	ensureTmuxServer(t, "dest", "alice")
	sid := newSID(t)
	seed(t, "source", "alice", "/home/alice/proj", sid)
	startInTmux(t, "source", "alice", "/home/alice/proj", sid, "main", "")

	out, code := teleport(t, "source", "alice", sid+" --to dest --state suspended")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if registry(t, "dest", "alice", sid) != "" {
		t.Error("suspended: dest claude must not be a live process")
	}
	panes := sh(t, "dest", "alice", "tmux list-panes -a -F '#{pane_current_command}'")
	if !strings.Contains(panes, "claude-teleport") {
		t.Errorf("dest pane should run the placeholder (claude-teleport placeholder), got %q", panes)
	}
	sh(t, "dest", "alice", "test -f "+transcriptPath("/home/alice", "/home/alice/proj", sid))
	// The source side also ends as a placeholder pane: thaw+exit (step 9)
	// always exits the source claude and types the (non-"--now", i.e.
	// suspended/needs-Enter) placeholder once the source was confirmed
	// running, regardless of the destination's target state — the same
	// mechanism scenario 1 checks for its --state running case.
	if pane := sh(t, "source", "alice", "tmux capture-pane -p -t main:claude"); !strings.Contains(pane, "teleported to dest") || !strings.Contains(pane, sid[:8]) {
		t.Errorf("source pane should show the placeholder banner for %s:\n%s", sid, pane)
	}
	if cmd := strings.TrimSpace(sh(t, "source", "alice", "tmux list-panes -t main:claude -F '#{pane_current_command}'")); cmd != "claude-teleport" {
		t.Errorf("source pane should be running the placeholder, got pane_current_command=%q", cmd)
	}
}

// scenario 7: teleporting a session back to its original host (--from)
// fast-forwards the transcript record-wise.
func TestTeleportBackFastForwardsTranscript(t *testing.T) {
	// I3: dump docker compose logs + job dirs on failure, before TestMain's
	// suite-wide `down -v` tears everything down.
	t.Cleanup(func() {
		if t.Failed() {
			dumpDiagnostics(t)
		}
	})
	reset(t)
	trustJump(t, "source", "alice")
	sid := newSID(t)
	seed(t, "source", "alice", "/home/alice/proj", sid)
	startInTmux(t, "source", "alice", "/home/alice/proj", sid, "main", "")
	if out, code := teleport(t, "source", "alice", sid+" --to dest --state idle"); code != 0 {
		t.Fatalf("first teleport: %d\n%s", code, out)
	}
	sh(t, "dest", "alice", "cd /home/alice/proj && claude -p --resume "+sid+" 'second thought'")

	out, code := teleport(t, "source", "alice", sid+" --from dest --state idle")
	if code != 0 {
		t.Fatalf("teleport back: %d\n%s", code, out)
	}
	if tr := sh(t, "source", "alice", "cat "+transcriptPath("/home/alice", "/home/alice/proj", sid)); !strings.Contains(tr, "second thought") {
		t.Errorf("source transcript not fast-forwarded:\n%s", tr)
	}
	// Destination-side end state for this hop: the actual destination is
	// SOURCE (--from dest teleports back onto here). --state idle again
	// means no live claude on the receiving end (see the identical
	// comment in TestKilledRunnerThenContinueCompletes); "start" opens a
	// NEW window to resume+reshape (tmux allows duplicate window names,
	// and CreatedWindow is set whenever start had to call OpenWindow, even
	// though a "main:claude" window already existed from the first
	// teleport's placeholder), and "shape" kills that new window since it
	// created it — leaving the ORIGINAL placeholder window from the first
	// teleport untouched. Verified empirically against this exact harness.
	if reg := registry(t, "source", "alice", sid); reg != "" {
		t.Errorf("source claude should have exited after the back-teleport (idle target state), registry: %s", reg)
	}
	if cmd := strings.TrimSpace(sh(t, "source", "alice", "tmux list-panes -t main:claude -F '#{pane_current_command}'")); cmd != "claude-teleport" {
		t.Errorf("source pane should still be the (first-teleport) placeholder, got pane_current_command=%q", cmd)
	}
}
