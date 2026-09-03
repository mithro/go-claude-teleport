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
	os.RemoveAll("api-log")
	os.MkdirAll("api-log", 0o755)
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
while kill -0 "$pid"; do
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
done
wait "$pid" || true
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

	// Assert the fact ConfirmClaude's gate depends on: every "busy"
	// snapshot observed must carry entrypoint "sdk-cli". Parse each
	// snapshot block (a JSON object) rather than doing a blind substring
	// search, since a false negative here (missing a real "busy" snapshot)
	// would silently under-report the finding.
	var busySnapshots, printSnapshots int
	for _, block := range strings.Split(out, "=== snapshot") {
		if !strings.Contains(block, "{") {
			continue
		}
		start := strings.Index(block, "{")
		end := strings.LastIndex(block, "}")
		if start < 0 || end < start {
			continue
		}
		var reg struct {
			Status     string `json:"status"`
			Kind       string `json:"kind"`
			Entrypoint string `json:"entrypoint"`
		}
		if err := json.Unmarshal([]byte(block[start:end+1]), &reg); err != nil {
			t.Logf("entrypoint probe: could not parse snapshot block: %v", err)
			continue
		}
		if strings.EqualFold(reg.Kind, "print") {
			t.Errorf(`a real claude -p run wrote kind=%q: real Claude Code was believed to have no "print" kind at all (task-26-report.md) — ConfirmClaude's gate may need revisiting`, reg.Kind)
		}
		if strings.EqualFold(reg.Status, "busy") {
			busySnapshots++
			t.Logf("entrypoint probe: busy snapshot has kind=%q entrypoint=%q", reg.Kind, reg.Entrypoint)
			if strings.EqualFold(reg.Entrypoint, "sdk-cli") {
				printSnapshots++
			} else {
				t.Errorf(`ConfirmClaude gates a "busy" print-mode turn on entrypoint=="sdk-cli" (internal/remote/local_claude.go), but a real claude -p run wrote status=busy with entrypoint=%q (kind=%q)`, reg.Entrypoint, reg.Kind)
			}
		}
	}
	if busySnapshots == 0 {
		t.Logf("entrypoint probe: never observed a busy snapshot (the window between registration and completion may be too short to sample against this fakeapi's near-instant canned reply) — see the full transcript above for what WAS observed")
	} else {
		t.Logf("entrypoint probe: observed %d busy snapshot(s), %d with entrypoint=sdk-cli", busySnapshots, printSnapshots)
	}
}
