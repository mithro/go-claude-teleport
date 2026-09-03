//go:build integration && realclaude

// test/integration/realclaude_test.go
package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func init() {
	composeFiles = []string{"-f", "docker-compose.yml", "-f", "docker-compose.realclaude.yml", "--profile", "realclaude"}
	// latestAPIRequest picks the highest-numbered file in api-log/ as
	// "latest" (fakeapi numbers requests "%04d.json" from 1 per container
	// lifetime). A fresh `api` container (every `go test` invocation
	// brings one up in TestMain) restarts that count at 1, so a stale
	// api-log/ left over from an EARLIER invocation (e.g. re-running this
	// test binary locally without `rm -rf api-log` first, or an ad-hoc
	// `docker compose ... up` against the same host directory while
	// developing this test) can leave higher-numbered files on disk than
	// the current run ever writes, and latestAPIRequest would silently
	// return that stale, unrelated request instead of failing loudly.
	// Clearing it once here (before TestMain brings up this run's own
	// `api` container) makes "latest" unambiguous for this process.
	//
	// A failure here must be fatal, not ignored: leaving a stale file
	// behind silently reintroduces exactly the wrong-request bug this
	// clearing exists to prevent. init() has no *testing.T to fail, so
	// panic — TestMain has not started any container yet, so there is
	// nothing to tear down.
	if err := os.RemoveAll("api-log"); err != nil {
		panic("clear api-log before the run: " + err.Error())
	}
	if err := os.MkdirAll("api-log", 0o755); err != nil {
		panic("recreate api-log: " + err.Error())
	}
}

// seedOnboarding pre-seeds svc/user's Claude Code global config with the
// fields a real, interactive Claude Code launch (never `-p`) writes after a
// human completes first-run onboarding, approves a custom ANTHROPIC_API_KEY,
// and accepts the "trust this folder" dialog for cwd.
//
// Task 26 adaptation (disclosed, two stacked real-claude-only findings, both
// reproduced with a throwaway pty-driving probe outside this package before
// writing this fix — see task-26-report.md for the full trace):
//
//  1. Real Claude Code's FIRST interactive launch on a machine performs an
//     unconditional, blocking reachability check against the REAL
//     api.anthropic.com — shown as a "Welcome to Claude Code" splash —
//     before it will even offer the theme picker, regardless of
//     ANTHROPIC_BASE_URL / ANTHROPIC_API_KEY / any DISABLE_* var. dest's
//     network (docker-compose.yml's `private`, `internal: true`) has no
//     route to the real internet by design (controller requirement A/D:
//     "nothing else phones home"), so this splash fails with "Failed to
//     connect to api.anthropic.com: ETIMEOUT" and every interactive
//     `claude --resume` on dest hangs at it. `claude -p` never shows this
//     screen (every -p call in this suite works without it) — only
//     interactive launches do. The gate is two ~/.claude.json fields:
//     "hasCompletedOnboarding" (skips the splash/theme-picker) and
//     "customApiKeyResponses.approved" (skips the separate "detected a
//     custom API key, use it?" prompt).
//
//  2. WHICH FILE those fields must be written to depends on whether
//     CLAUDE_CONFIG_DIR is present in claude's environment AT ALL, even set
//     to a value that already equals the default $HOME/.claude: unset (or
//     absent), real claude reads/writes plain $HOME/.claude.json; merely
//     having CLAUDE_CONFIG_DIR exported (any value) makes it read/write
//     $CLAUDE_CONFIG_DIR/.claude.json instead — confirmed in isolation
//     (identical run, only the presence of that one env var changed the
//     file claude touched). internal/remote/local_pty.go's RunPtyResume
//     (used by every --state idle "start" step, no-tmux or not, since idle
//     still confirms-then-exits under a pty) always exports
//     CLAUDE_CONFIG_DIR explicitly, so the tool's own "start" step needs
//     the seed at $CLAUDE_CONFIG_DIR/.claude.json; a plain `claude -p` or
//     `claude --resume` this test types directly (docker-compose.yml
//     deliberately never sets CLAUDE_CONFIG_DIR at the container level —
//     see its own comment) needs it at plain $HOME/.claude.json. Both are
//     seeded so either path works. This is a real discrepancy between
//     internal/session.Paths.GlobalJSON (always $HOME/.claude.json) and
//     where real Claude Code actually persists this state once
//     CLAUDE_CONFIG_DIR is explicitly set — reported, not fixed here (see
//     task-26-report.md); not applied to internal/ per the implementer
//     rules.
//
// The real precondition this harness is meant to model is the README's own:
// "Claude Code must already be installed and logged in on the destination;
// the tool never installs or logs in" — a real destination machine has
// already been through both of the above exactly once, by a human, before
// any teleport ever lands on it.
//
// Nothing seeded here is a credential: "dummy-key" is the same inert
// placeholder docker-compose.yml sets as ANTHROPIC_API_KEY (pointing at the
// in-harness fakeapi, never api.anthropic.com), and the field records that
// SOME key was approved — the decision, not a secret. There is no
// oauthAccount, no token and no real endpoint anywhere in this seed, and no
// host ~/.claude is ever read (controller requirement A).
func seedOnboarding(t testing.TB, svc, user, cwd string) {
	t.Helper()
	body := `{"hasCompletedOnboarding": true, "customApiKeyResponses": {"approved": ["dummy-key"], "rejected": []}, "projects": {"` + cwd + `": {"hasTrustDialogAccepted": true}}}`
	sh(t, svc, user, "mkdir -p ~/.claude && printf '%s' '"+body+"' > ~/.claude.json && printf '%s' '"+body+"' > ~/.claude/.claude.json")
}

// apiLogEntry is the shape internal/fakeapi.Server writes to LogDir (one
// file per request): {"path":..., "method":..., "query":..., "at":...,
// "body":...}. This differs from the brief's assumed filename scheme
// (<unix-nanos>-<path>.json, raw body as the file content) — the real
// implementation (internal/fakeapi/server.go's Server.record) numbers
// files sequentially ("%04d.json") and wraps the raw request body inside
// a JSON envelope under the "body" key. Adapted per the brief's own
// escape hatch ("if it uses another scheme, adapt latestAPIRequest's
// filter to that scheme — the content check is what matters").
type apiLogEntry struct {
	Path string          `json:"path"`
	Body json.RawMessage `json:"body"`
}

// latestAPIRequest returns the newest request body fakeapi logged whose
// path is /v1/messages (excluding /v1/messages/count_tokens, which Claude
// Code also calls and which never carries conversation content).
func latestAPIRequest(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir("api-log")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Fatal("fakeapi logged no request at all")
	}
	sort.Strings(names) // "%04d.json" sequence numbers sort into request order
	for i := len(names) - 1; i >= 0; i-- {
		b, err := os.ReadFile(filepath.Join("api-log", names[i]))
		if err != nil {
			t.Fatal(err)
		}
		var rec apiLogEntry
		if err := json.Unmarshal(b, &rec); err != nil {
			t.Fatalf("parse %s: %v", names[i], err)
		}
		if strings.HasPrefix(rec.Path, "/v1/messages") && !strings.HasPrefix(rec.Path, "/v1/messages/count_tokens") {
			return string(rec.Body)
		}
	}
	t.Fatal("fakeapi logged no /v1/messages request")
	return ""
}

func TestRealClaudeResumeCarriesConversation(t *testing.T) {
	t.Cleanup(func() {
		if t.Failed() {
			dumpDiagnostics(t)
		}
	})
	reset(t)
	trustJump(t, "source", "alice")
	seedOnboarding(t, "dest", "alice", "/home/alice/proj")
	sid := newSID(t)
	v := strings.TrimSpace(sh(t, "source", "alice", "claude --version"))
	t.Logf("claude on source: %s", v)
	sh(t, "source", "alice", "mkdir -p ~/proj && cd ~/proj && claude -p --session-id "+sid+" 'remember the word pineapple'")
	sh(t, "source", "alice", "test -f "+transcriptPath("/home/alice", "/home/alice/proj", sid))
	out, code := teleport(t, "source", "alice", sid+" --to dest --state idle --no-tmux")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	sh(t, "dest", "alice", "cd ~/proj && claude -p --resume "+sid+" 'what word?'")
	body := latestAPIRequest(t)
	if !strings.Contains(body, "pineapple") {
		t.Errorf("resumed request on dest does not carry the first turn:\n%s", body)
	}
	if !strings.Contains(body, "what word?") {
		t.Errorf("resumed request lacks the new prompt:\n%s", body)
	}
}

func TestRealClaudeTmuxResumeWritesRegistry(t *testing.T) {
	t.Cleanup(func() {
		if t.Failed() {
			dumpDiagnostics(t)
		}
	})
	reset(t)
	trustJump(t, "source", "alice")
	seedOnboarding(t, "dest", "alice", "/home/alice/proj")
	sid := newSID(t)
	sh(t, "source", "alice", "mkdir -p ~/proj && cd ~/proj && claude -p --session-id "+sid+" 'hello'")
	if out, code := teleport(t, "source", "alice", sid+" --to dest --state idle"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	sh(t, "dest", "alice", "tmux -f /dev/null new-session -d -s w -n c -c ~/proj")
	sh(t, "dest", "alice", "tmux send-keys -t w:c ' claude --resume "+sid+"' Enter")
	reg := waitRegistry(t, "dest", "alice", sid, "idle")
	if !strings.Contains(reg, `"tmux": "w:@0.%0"`) && !strings.Contains(reg, `"tmux":"w:@0.%0"`) {
		t.Errorf("registry tmux field: %s", reg)
	}
	sh(t, "dest", "alice", "tmux send-keys -t w:c '/exit'")
	time.Sleep(500 * time.Millisecond)
	sh(t, "dest", "alice", "tmux send-keys -t w:c Enter")
	deadline := time.Now().Add(30 * time.Second)
	for registry(t, "dest", "alice", sid) != "" && time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
	}
	if r := registry(t, "dest", "alice", sid); r != "" {
		t.Errorf("registry entry for %s still present after /exit: %s", sid, r)
	}
}

// TestRealClaudePrintRegistryKind is controller requirement C's hard probe
// (ledgered carry M5, internal/remote/local_claude.go's ConfirmClaude):
// nothing in the codebase had verified what a real `claude -p` writes to
// ~/.claude/sessions/<pid>.json while it is running. This test backgrounds
// a real `claude -p` run and polls the registry as fast as the shell
// allows, recording every distinct snapshot it observes plus the state
// immediately after the process exits.
//
// The first run of this probe (task-26-report.md) established that "kind"
// is "interactive" for a print run AND an interactive one — real Claude
// Code has no "print" kind at all — and that "entrypoint" is the field
// that differs ("sdk-cli" vs "cli"). ConfirmClaude was moved onto
// "entrypoint" for that reason (T26-1), so this probe now asserts the fact
// the tool actually depends on: any snapshot seen with status "busy" must
// carry entrypoint "sdk-cli", and none of them may claim kind "print".
func TestRealClaudePrintRegistryKind(t *testing.T) {
	t.Cleanup(func() {
		if t.Failed() {
			dumpDiagnostics(t)
		}
	})
	reset(t)
	sid := newSID(t)
	script := `sid='` + sid + `'
cd ~ && mkdir -p proj && cd proj
claude -p --session-id "$sid" 'kind probe: remember the word pineapple' &
pid=$!
last=""
snap=0
polls=0
# Bounded (600 x 0.05s = 30s of sampling): a real -p turn against this
# fakeapi's canned reply finishes in about two seconds, so the cap is only
# ever reached by a claude that hung — in which case it is killed here and
# the exit-status assertion below reports it, rather than the suite
# spinning a container CPU until go test's own -timeout fires.
while kill -0 "$pid"; do
  polls=$((polls+1))
  if [ "$polls" -gt 600 ]; then
    echo "=== poll cap reached after $polls samples; killing $pid ==="
    kill "$pid"
    break
  fi
  f=$(grep -ls "\"sessionId\":\"$sid\"" ~/.claude/sessions/*.json | head -1)
  if [ -n "$f" ]; then
    cur=$(cat "$f" 2>&1) || cur=""
    if [ -n "$cur" ] && [ "$cur" != "$last" ]; then
      snap=$((snap+1))
      echo "=== snapshot $snap (pid alive) ==="
      echo "$cur"
      last="$cur"
    fi
  fi
  sleep 0.05
done
# The exit status is recorded rather than discarded with "|| true": a
# claude -p that failed (or was killed at the cap above) must fail this
# test, not leave it quietly asserting over zero snapshots. Captured into
# rc instead of letting the shell's own -e abort here, so the
# after-exit registry state below still makes it into the log for a
# failure to be read from.
rc=0
wait "$pid" || rc=$?
echo "=== claude -p exit $rc ==="
echo "=== after exit ==="
f=$(grep -ls "\"sessionId\":\"$sid\"" ~/.claude/sessions/*.json | head -1)
if [ -n "$f" ]; then
  cat "$f" 2>&1 || echo "(registry file $f vanished before it could be read)"
else
  echo "(no registry entry for $sid after exit)"
fi
`
	out := sh(t, "source", "alice", script)
	t.Logf("entrypoint probe for sid %s:\n%s", sid, out)

	// The backgrounded run must have succeeded: a claude -p that failed (or
	// never started, or hit the poll cap) would otherwise leave this test
	// asserting over an empty snapshot list and passing for the wrong
	// reason. Checked before the snapshots so a broken claude reports
	// itself rather than surfacing as "captured no snapshot".
	if !strings.Contains(out, "=== claude -p exit 0 ===") {
		t.Fatalf("the backgrounded claude -p did not exit 0:\n%s", out)
	}

	// Parse each snapshot section on its own (the JSON between the section
	// header and the next "=== " marker) rather than scanning the whole
	// transcript for one { ... } span, which would run a final snapshot
	// together with the after-exit dump and silently fail to parse.
	var snapshots, busySnapshots int
	for _, sec := range strings.Split(out, "=== ") {
		if !strings.HasPrefix(sec, "snapshot ") {
			continue
		}
		nl := strings.Index(sec, "\n")
		if nl < 0 {
			continue
		}
		body := sec[nl+1:]
		start := strings.Index(body, "{")
		end := strings.LastIndex(body, "}")
		if start < 0 || end < start {
			continue
		}
		var reg struct {
			Status     string `json:"status"`
			Kind       string `json:"kind"`
			Entrypoint string `json:"entrypoint"`
		}
		if err := json.Unmarshal([]byte(body[start:end+1]), &reg); err != nil {
			t.Errorf("registry snapshot is not parseable JSON (%v):\n%s", err, body)
			continue
		}
		snapshots++
		if strings.EqualFold(reg.Status, "busy") {
			busySnapshots++
		}

		// PINNED REAL-CLAUDE FACTS (task-26-report.md; identical on 2.1.247
		// and 2.1.259). These two lines are the whole point of this probe:
		// the tool's own print-mode gate is built on them, so a Claude Code
		// release that changes either must fail HERE, loudly, instead of
		// silently defeating the gate in production.
		//
		//  1. kind is "interactive" for a `-p` run just as it is for a
		//     terminal one — real Claude Code has no "print" kind at all,
		//     which is why ConfirmClaude no longer keys on Kind (M5).
		//  2. entrypoint is "sdk-cli" for `-p` vs "cli" for an interactive
		//     terminal run — the field that actually distinguishes them,
		//     and the one internal/remote/local_claude.go's ConfirmClaude
		//     now gates its "busy" print-mode acceptance on, read through
		//     internal/session.Registry.Entrypoint.
		if !strings.EqualFold(reg.Kind, "interactive") {
			t.Errorf(`pinned fact broken: a real claude -p registry entry had kind=%q, want "interactive" (real Claude Code has no "print" kind; internal/remote/local_claude.go's ConfirmClaude gate and internal/session.Registry were built on this — re-verify both)`, reg.Kind)
		}
		if !strings.EqualFold(reg.Entrypoint, "sdk-cli") {
			t.Errorf(`pinned fact broken: a real claude -p registry entry had entrypoint=%q, want "sdk-cli" (internal/remote/local_claude.go's ConfirmClaude accepts a "busy" print-mode turn only on entrypoint=="sdk-cli", via internal/session.Registry.Entrypoint — that gate can no longer match)`, reg.Entrypoint)
		}
	}
	if snapshots == 0 {
		t.Fatalf("captured no registry snapshot at all for a live `claude -p` — the probe asserts nothing unless it sees one (real Claude Code writes ~/.claude/sessions/<pid>.json for the life of the process):\n%s", out)
	}
	t.Logf("entrypoint probe: %d registry snapshot(s), %d with status=busy; kind/entrypoint pinned on every one", snapshots, busySnapshots)
}
