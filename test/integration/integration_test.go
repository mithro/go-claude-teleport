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
	// C6: `!` mode ends in the same place every other running-state
	// scenario does, and must be held to the same evidence — the
	// transcript really landed under the destination's project dir, and
	// the destination pane is really running claude (not a shell that
	// merely sits in a window called "claude", and not the placeholder,
	// whose command name also contains "claude").
	sh(t, "dest", "alice", "test -f "+transcriptPath("/home/alice", "/home/alice/proj", sid))
	if cmd := strings.TrimSpace(sh(t, "dest", "alice", "tmux list-panes -t main:claude -F '#{pane_current_command}'")); cmd != "claude" {
		t.Errorf("dest pane should be running claude, got pane_current_command=%q", cmd)
	}
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
	// The source claude's pid, for the C5 check below: taken before the
	// teleport starts, since the freeze makes the registry file's own
	// contents nothing to rely on mid-run.
	pid := pidFromRegistry(registry(t, "source", "alice", sid))
	if pid == "" {
		t.Fatal("no source registry entry to take a pid from")
	}
	sh(t, "source", "alice", "nohup claude-teleport "+sid+" --to dest --state idle "+teleportOpts+" > /home/alice/fg.log 2>&1 &")

	waitStepRunning(t, "source", "alice", sid, "transfer", 60*time.Second)
	before, ok := readJournal(t, "source", "alice", sid)
	if !ok {
		t.Fatal("no journal for the running job")
	}
	// [c]laude-teleport, not claude-teleport: pkill -f matches against the
	// full command line, including its OWN invocation's argv on this same
	// docker-exec shell — an unbracketed pattern kills the pkill/sh
	// process itself (observed: exit 137) before it ever reaches the
	// runner.
	sh(t, "source", "alice", "pkill -9 -f '[c]laude-teleport internal-runner' || true")
	// C5, spec §12: a killed runner never leaves the source stopped BY US.
	// The freezer is a separate, detached helper holding a pipe to the
	// runner; the runner's death is an EOF on that pipe, and
	// procx.RunFreezerHelper's only route from that EOF to its own exit
	// goes through the SIGCONT (or through finding the target already
	// gone). So "no internal-freezer process remains, and the source
	// claude is still alive" is direct evidence that the freeze was
	// released — if this regresses, the helper is still sitting on the
	// pipe with the user's Claude stopped and nothing left alive that
	// could ever thaw it. Polled, since the EOF and the SIGCONT are
	// asynchronous to pkill returning.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, code := shCode(t, "source", "alice", "pgrep -f '[c]laude-teleport internal-freezer'"); code != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a freezer helper is still holding source claude (pid %s) 30s after its owner was killed", pid)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if procState(t, "source", "alice", pid) == "" {
		t.Fatalf("source claude (pid %s) died when the runner was killed; it must survive to be continued", pid)
	}
	// Measured residue, deliberately not asserted away (reproduced by hand
	// against this harness): the SIGCONT above does resume the process, but
	// an interactive shell took the pty back the moment its foreground job
	// stopped, so the resumed Claude re-stops on SIGTTIN at its next read
	// and /proc reports "T" again (state do_signal_stop, tpgid naming the
	// shell's group). Only tcsetpgrp — in practice the shell's own `fg`,
	// which remote.Local.Thaw types into the pane — gives the terminal
	// back, and no such caller exists once the runner is dead. The session
	// is not lost: `continue` below re-freezes and thaws it properly, and
	// a user could type fg. Logged so a change in this behaviour is
	// visible in the run output.
	t.Logf("C5: after the runner was killed the freezer is gone; source claude (pid %s) /proc state is %q", pid, procState(t, "source", "alice", pid))
	out, code := shCode(t, "source", "alice", "claude-teleport continue "+sid)
	if code != 0 {
		t.Fatalf("continue: %d\n%s", code, out)
	}
	// C7: `continue` must RESUME the killed job, not silently start a new
	// one that happens to end in the same place. The journal is the
	// evidence: same job (its created_at is stamped once, when the job
	// directory is first written) with the transfer step re-attempted.
	after, ok := readJournal(t, "source", "alice", sid)
	if !ok {
		t.Fatal("no journal after continue")
	}
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("continue started a NEW job: created_at %s -> %s", before.CreatedAt, after.CreatedAt)
	}
	beforeStep, _ := journalStep(before, "transfer")
	afterStep, ok := journalStep(after, "transfer")
	if !ok || afterStep.Attempts <= beforeStep.Attempts {
		t.Errorf("transfer step attempts %d -> %d, want the killed attempt plus the resumed one", beforeStep.Attempts, afterStep.Attempts)
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

	waitStepRunning(t, "source", "alice", sid, "transfer", 60*time.Second)
	before, ok := readJournal(t, "source", "alice", sid)
	if !ok {
		t.Fatal("no journal for the running job")
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
	deadline := time.Now().Add(3 * time.Minute)
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
	// C7: the same job was resumed, not restarted (see the identical
	// check in TestKilledRunnerThenContinueCompletes).
	after, ok := readJournal(t, "source", "alice", sid)
	if !ok {
		t.Fatal("no journal after continue")
	}
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("continue started a NEW job: created_at %s -> %s", before.CreatedAt, after.CreatedAt)
	}
	beforeStep, _ := journalStep(before, "transfer")
	afterStep, ok := journalStep(after, "transfer")
	if !ok || afterStep.Attempts <= beforeStep.Attempts {
		t.Errorf("transfer step attempts %d -> %d, want the dropped attempt plus the resumed one", beforeStep.Attempts, afterStep.Attempts)
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
	// C8: the one pane the teleport created, compared exactly — a
	// list-panes -a substring match would also be satisfied by any other
	// pane on the host (including the _keepalive session's shell if it
	// ever ran the tool), and "claude-teleport" is itself a substring of
	// nothing else only by luck.
	if cmd := strings.TrimSpace(sh(t, "dest", "alice", "tmux list-panes -t main:claude -F '#{pane_current_command}'")); cmd != "claude-teleport" {
		t.Errorf("dest pane should be running the placeholder, got pane_current_command=%q", cmd)
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

// scenario 8 (C3): the suite's git coverage, and its only cross-user hop.
// alice@source has a repository with a LINKED worktree
// (~/repo/.worktrees/x) on its own branch, a dirty tracked file with a
// non-default mode, and a Claude session whose cwd is that worktree. It
// teleports to bob@dest, whose $HOME differs — so every path is rewritten
// (spec §7.2) and the destination's project directory is a different
// Munge()'d name. The destination has no repository at all, which is what
// puts the git plan in fresh-main mode (spec §8: M absent, transfer
// everything, then repair the linked-worktree metadata for the new paths).
func TestWorktreeSessionTeleportsToOtherUserHome(t *testing.T) {
	t.Cleanup(func() {
		if t.Failed() {
			dumpDiagnostics(t)
		}
	})
	reset(t)
	trustJump(t, "source", "alice")
	ensureTmuxServer(t, "dest", "bob")
	sid := newSID(t)
	const (
		srcMain = "/home/alice/repo"
		srcWT   = "/home/alice/repo/.worktrees/x"
		dstMain = "/home/bob/repo"
		dstWT   = "/home/bob/repo/.worktrees/x"
	)
	makeRepo(t, "source", "alice", srcMain)
	sh(t, "source", "alice", "cd "+srcMain+" && git worktree add -q -b feature .worktrees/x")
	// A dirty tracked file, with a mode git itself records and the
	// transfer must reproduce: content alone would pass even if every
	// file landed 0644.
	sh(t, "source", "alice", "cd "+srcWT+" && printf 'dirty in the worktree\n' > README.md && chmod 755 README.md")
	// A project entry for the session's cwd in the SOURCE global config,
	// so the destination-side merge (T26-2) has something to carry: this
	// is the file real Claude Code reads for the trust dialog and
	// mcpServers, and it must land in the file the destination's Claude
	// actually reads.
	sh(t, "source", "alice", `printf '%s' '{"projects": {"`+srcWT+`": {"hasTrustDialogAccepted": true}}}' > ~/.claude.json`)
	seed(t, "source", "alice", srcWT, sid)
	startInTmux(t, "source", "alice", srcWT, sid, "main", "")

	out, code := teleport(t, "source", "alice", sid+" --to bob@dest --state running")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}

	// The repository landed under bob's home, with the linked worktree
	// attached to it — `git worktree list` reads the .git link file and
	// the repository's own worktrees/<name>/gitdir, so it only agrees if
	// BOTH were repaired for the destination's paths.
	if got := sh(t, "dest", "bob", "cd "+dstMain+" && git worktree list"); !strings.Contains(got, dstWT) {
		t.Errorf("dest worktree list does not show %s:\n%s", dstWT, got)
	}
	if got := strings.TrimSpace(sh(t, "dest", "bob", "cat "+dstWT+"/.git")); got != "gitdir: "+dstMain+"/.git/worktrees/x" {
		t.Errorf("worktree .git link = %q, want it repointed at bob's paths", got)
	}
	if got := strings.TrimSpace(sh(t, "dest", "bob", "cd "+dstWT+" && git rev-parse --abbrev-ref HEAD")); got != "feature" {
		t.Errorf("dest worktree is on branch %q, want feature", got)
	}
	if got := sh(t, "dest", "bob", "cat "+dstWT+"/README.md"); !strings.Contains(got, "dirty in the worktree") {
		t.Errorf("dirty file content on dest: %q", got)
	}
	if got := strings.TrimSpace(sh(t, "dest", "bob", "stat -c %a "+dstWT+"/README.md")); got != "755" {
		t.Errorf("dirty file mode on dest = %q, want 755", got)
	}
	// The transcript is under bob's OWN munged project dir, which is a
	// different directory name entirely (-home-bob-repo--worktrees-x).
	sh(t, "dest", "bob", "test -f "+transcriptPath("/home/bob", dstWT, sid))
	reg := waitRegistry(t, "dest", "bob", sid, "idle")
	if !strings.Contains(reg, `"cwd":"`+dstWT+`"`) {
		t.Errorf("dest registry should record bob's cwd: %s", reg)
	}
	if cmd := strings.TrimSpace(sh(t, "dest", "bob", "tmux list-panes -t main:claude -F '#{pane_current_command}'")); cmd != "claude" {
		t.Errorf("dest pane should be running claude, got pane_current_command=%q", cmd)
	}
	// T26-2: the carried-over project entry must be in the file the
	// destination's Claude Code actually reads. Nothing in this harness
	// sets CLAUDE_CONFIG_DIR (docker-compose.yml deliberately doesn't —
	// see its comment), so that file is $HOME/.claude.json, and
	// $HOME/.claude/.claude.json must not be invented alongside it.
	if got := sh(t, "dest", "bob", "cat /home/bob/.claude.json"); !strings.Contains(got, dstWT) {
		t.Errorf("bob's ~/.claude.json should carry the rewritten project entry:\n%s", got)
	}
	if _, code := shCode(t, "dest", "bob", "test -e /home/bob/.claude/.claude.json"); code == 0 {
		t.Error("a global config inside CLAUDE_CONFIG_DIR was written even though the variable is unset: real Claude Code reads ~/.claude.json here (T26-2)")
	}
	// The source ends as every running-state teleport does: claude gone,
	// the placeholder pane naming where it went.
	if registry(t, "source", "alice", sid) != "" {
		t.Error("source claude still registered after the teleport")
	}
	if pane := sh(t, "source", "alice", "tmux capture-pane -p -t main:claude"); !strings.Contains(pane, "teleported to") || !strings.Contains(pane, sid[:8]) {
		t.Errorf("source pane should show the placeholder banner for %s:\n%s", sid, pane)
	}
}
