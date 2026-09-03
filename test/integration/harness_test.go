//go:build integration

// test/integration/harness_test.go
package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// projectName is unique per test-binary run (controller requirement C: a
// crashed or concurrently-running suite's containers/networks/volumes can
// never collide with this one's).
var projectName = fmt.Sprintf("ct25-%d-%d", os.Getpid(), time.Now().UnixNano())

// composeFiles is the -f/--profile prefix every compose invocation shares.
// Layer 1 (this file) uses just docker-compose.yml; realclaude_test.go's
// init() (build tags integration && realclaude) overrides it to also layer
// docker-compose.realclaude.yml and enable the "realclaude" profile so the
// fakeapi service comes up and the fakeclaude bind-mounts are dropped.
var composeFiles = []string{"-f", "docker-compose.yml"}

func composeArgs(args ...string) []string {
	return append(append([]string{"compose", "-p", projectName}, composeFiles...), args...)
}

func compose(t testing.TB, args ...string) string {
	t.Helper()
	cmd := exec.Command("docker", composeArgs(args...)...)
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// execAs runs argv inside svc as user (no shell) and returns combined
// output and the exit code.
//
// -e USER=<user> is an adaptation (see task-25-report.md "Adaptations"):
// `docker compose exec -u <user>` does not export $USER the way a real
// login shell would, but internal/cli's dialTarget (transport.go) falls
// back to $USER (then "root") as the local account for --via hops and any
// target spelled without an explicit "user@" prefix. Without this, every
// `--via jump` dial in these tests would authenticate to jump as root,
// which has no key wired in the harness (see task-24-report.md).
func execAs(t testing.TB, svc, user string, argv ...string) (string, int) {
	t.Helper()
	args := composeArgs("exec", "-T", "-u", user, "-e", "USER="+user, svc)
	args = append(args, argv...)
	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("docker exec %s: %v\n%s", strings.Join(argv, " "), err, out)
	}
	return string(out), code
}

// sh runs a shell snippet inside svc as user and fails the test on error.
func sh(t testing.TB, svc, user, script string) string {
	t.Helper()
	out, code := execAs(t, svc, user, "sh", "-ec", script)
	if code != 0 {
		t.Fatalf("[%s/%s] %s\nexit %d:\n%s", svc, user, script, code, out)
	}
	return out
}

// shCode is sh without the failure: returns output and exit code.
func shCode(t testing.TB, svc, user, script string) (string, int) {
	t.Helper()
	return execAs(t, svc, user, "sh", "-ec", script)
}

func TestMain(m *testing.M) {
	if _, err := os.Stat(filepath.Join("..", "..", "dist", "claude-teleport")); err != nil {
		fmt.Fprintln(os.Stderr, "run test/integration/build.sh first:", err)
		os.Exit(2)
	}
	up := exec.Command("docker", composeArgs("up", "-d", "--wait")...)
	if out, err := up.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "compose up: %v\n%s", err, out)
		exec.Command("docker", composeArgs("down", "-v", "--remove-orphans")...).Run()
		os.Exit(2)
	}
	code := func() (code int) {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintln(os.Stderr, "panic running suite:", r)
				code = 2
			}
		}()
		return m.Run()
	}()
	// Controller requirement C: always torn down, on every path (pass,
	// fail, or panic in the suite itself).
	exec.Command("docker", composeArgs("down", "-v", "--remove-orphans")...).Run()
	os.Exit(code)
}

const teleportOpts = "-o StrictHostKeyChecking=accept-new --via jump --start-timeout 60s"

// trustJump seeds jump's host key into svc/user's known_hosts using the
// TOOL's OWN accept-new logic (never the ssh binary), satisfying
// controller requirement A even though a --via hop can't be accept-new'd
// directly. See task-25-report.md "Adaptations" for why this is needed:
// internal/sshx.Dial deliberately never applies -o overrides to --via
// hops (only to the final target — see its doc comment), and
// internal/sshx.Resolve never reads StrictHostKeyChecking from
// ~/.ssh/config for ANY host either (Resolve only takes User/Port/
// IdentityFile/ProxyJump from config; every other -o-style key, including
// StrictHostKeyChecking, comes only from the overrides map, which Dial
// passes as nil for jump hops). So the only way to get jump's key
// accepted by the tool itself is to dial it once as a plain, non-via
// target — `list --host` does exactly that dial (transport.go's
// dialTarget) and accepts -o normally.
func trustJump(t testing.TB, svc, user string) {
	t.Helper()
	sh(t, svc, user, "claude-teleport list --host jump -o StrictHostKeyChecking=accept-new")
}

// ensureTmuxServer starts (or leaves alone) a tmux server on svc for user
// with one placeholder session, so orchestrate.Preflight's destination
// tmux inventory finds a live server to attach a new window to — spec §9
// treats "no live server" as "only --state idle is possible" regardless
// of what the caller asked for. Real destinations normally already have a
// tmux server running from the user's own workflow; this harness starts
// one explicitly since dest/jump/source begin every test with none.
func ensureTmuxServer(t testing.TB, svc, user string) {
	t.Helper()
	sh(t, svc, user, "tmux new-session -d -s _keepalive -n idle || tmux has-session -t _keepalive")
}

// reset wipes Claude and claude-teleport state and every project dir on
// all hosts (for both users) and kills tmux servers. It does NOT touch
// ~/.ssh (known_hosts, seeded once by trustJump, must survive).
func reset(t testing.TB) {
	t.Helper()
	for _, svc := range []string{"source", "jump", "dest"} {
		for _, u := range []string{"alice", "bob"} {
			shCode(t, svc, u, "tmux kill-server || true; rm -rf ~/.claude ~/.claude.json ~/.local/share/claude-teleport ~/proj* ~/repo*; mkdir -p ~/.claude")
		}
	}
}

// dumpDiagnostics logs docker compose's own logs (last 300 lines) and every
// job directory's contents (log.txt, capture.txt, job.json) on source and
// dest, for both users, BEFORE TestMain's suite-wide `down -v` tears
// everything down — so a CI failure is debuggable without a re-run.
// Callers register it via t.Cleanup, guarded on t.Failed(), as the first
// statement in each test (so it still fires if an early helper like
// reset/trustJump itself calls t.Fatalf).
func dumpDiagnostics(t testing.TB) {
	t.Helper()
	logs, err := exec.Command("docker", composeArgs("logs", "--no-color")...).CombinedOutput()
	if err != nil {
		t.Logf("=== docker compose logs: %v ===", err)
	}
	lines := strings.Split(strings.TrimRight(string(logs), "\n"), "\n")
	if len(lines) > 300 {
		lines = lines[len(lines)-300:]
	}
	t.Logf("=== docker compose logs (last %d lines) ===\n%s", len(lines), strings.Join(lines, "\n"))
	for _, svc := range []string{"source", "dest"} {
		for _, u := range []string{"alice", "bob"} {
			// Never 2>/dev/null: a missing/empty jobs dir is tolerated by
			// `|| true` around the fallible commands, and any stderr they
			// do produce is merged in (2>&1, not discarded) so it shows up
			// in the dump rather than being silently dropped.
			out, _ := shCode(t, svc, u, `d=~/.local/share/claude-teleport/jobs; `+
				`[ -d "$d" ] || { echo "(no $d)"; exit 0; }; `+
				`for j in "$d"/*/; do echo "--- $j ---"; `+
				`for f in log.txt capture.txt job.json; do echo "-- $f --"; cat "$j$f" 2>&1 || true; done; done`)
			if strings.TrimSpace(out) != "" {
				t.Logf("=== job dirs on %s/%s ===\n%s", svc, u, out)
			}
		}
	}
}

func newSID(t testing.TB) string {
	t.Helper()
	b := make([]byte, 16)
	f, err := os.Open("/dev/urandom")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// seed creates a session transcript in cwd on svc via fakeclaude -p: a
// real, non-empty transcript with its permission-mode startup record
// (fakeclaude writes this itself on every fresh session).
func seed(t testing.TB, svc, user, cwd, sid string) {
	t.Helper()
	sh(t, svc, user, "mkdir -p "+cwd+" && cd "+cwd+" && claude -p --session-id "+sid+" 'remember the word pineapple'")
}

// makeRepo creates a git repo with one commit at dir on svc.
func makeRepo(t testing.TB, svc, user, dir string) {
	t.Helper()
	sh(t, svc, user, "mkdir -p "+dir+" && cd "+dir+" && git init -q -b main && git config user.email a@laptop.example && git config user.name alice && echo hi > README.md && git add . && git commit -q -m init")
}

// startInTmux starts `claude --resume sid` in a new tmux window and waits
// for the registry to report idle. group is the tmux session name; the
// window is always "claude".
func startInTmux(t testing.TB, svc, user, cwd, sid, group string, extraEnv string) {
	t.Helper()
	sh(t, svc, user, "tmux -f /dev/null new-session -d -s "+group+" -n claude -c "+cwd)
	sh(t, svc, user, "tmux send-keys -t "+group+":claude \""+extraEnv+" claude --resume "+sid+"\" Enter")
	waitRegistry(t, svc, user, sid, "idle")
}

// registry returns the registry JSON for sid on svc, or "".
func registry(t testing.TB, svc, user, sid string) string {
	t.Helper()
	// grep -s (--no-messages), not `2>/dev/null`: it tells grep itself not
	// to emit the "no such file" message when ~/.claude/sessions/*.json
	// doesn't glob-expand (no sessions yet), rather than discarding a
	// stream grep might otherwise write real output to.
	out, _ := shCode(t, svc, user, "grep -ls '\"sessionId\":\""+sid+"\"' ~/.claude/sessions/*.json | head -1 | xargs -r cat")
	return strings.TrimSpace(out)
}

func waitRegistry(t testing.TB, svc, user, sid, status string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if r := registry(t, svc, user, sid); r != "" && strings.Contains(r, `"status":"`+status+`"`) {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("[%s/%s] session %s never reached %s", svc, user, sid, status)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// pidFromRegistry extracts the "pid" field from a registry() result.
func pidFromRegistry(reg string) string {
	i := strings.Index(reg, `"pid":`)
	if i < 0 {
		return ""
	}
	rest := reg[i+len(`"pid":`):]
	j := strings.IndexAny(rest, ",}")
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:j])
}

// procState returns the ps STAT letter(s) for pid on svc, or "" if the
// process is gone.
func procState(t testing.TB, svc, user, pid string) string {
	t.Helper()
	out, code := shCode(t, svc, user, "cut -d' ' -f3 /proc/"+pid+"/stat")
	if code != 0 {
		return ""
	}
	return strings.TrimSpace(out)
}

func transcriptPath(home, cwd, sid string) string {
	munged := strings.NewReplacer("/", "-", ".", "-").Replace(cwd)
	return home + "/.claude/projects/" + munged + "/" + sid + ".jsonl"
}

func teleport(t testing.TB, svc, user, args string) (string, int) {
	t.Helper()
	return shCode(t, svc, user, "claude-teleport "+args+" "+teleportOpts)
}
