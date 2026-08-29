# claude-teleport Plan 02 — Transport Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the transport layer of claude-teleport: an in-binary ssh client with jump chains (`internal/sshx`), the JSON-over-ssh helper protocol with a local and a remote `Endpoint` (`internal/remote`), the manifest-driven verified tar+gzip transfer (`internal/transfer`), the resumable job journal and step runner (`internal/job`), the canned Anthropic API server (`internal/fakeapi`), and the cli commands `remote serve|stream`, `status`, `abandon`, `internal-runner`, plus remote `compare-config`/`inspect`.

**Architecture:** Every remote operation is a method of the `remote.Endpoint` interface; `remote.Local` implements it on the filesystem, `remote.Client` implements it by sending `{"id","op","args"}` lines to `claude-teleport remote serve` over one `sshx.Client`, and `remote.Serve` dispatches requests to a `Local`. Bulk data never crosses the control channel: `OpenStream` opens a fresh ssh session running `claude-teleport remote stream <kind> <job> <id>`. `transfer` hashes, diffs, streams and installs files against a manifest so every step is idempotent, and `job` records progress so an interrupted teleport continues. Git/tmux/claude operations are typed against opaque aliases that Plan 03 replaces.

**Tech Stack:** Go 1.26, `golang.org/x/crypto/ssh` (+`agent`, `knownhosts`), `github.com/kevinburke/ssh_config`, `github.com/spf13/cobra`, `github.com/google/go-cmp` (tests), stdlib `archive/tar`, `compress/gzip`, `crypto/sha256`, `net/http`.

**Spec:** `docs/superpowers/specs/2026-08-27-claude-teleport-design.md` (sections in scope: §4.2, §4.3, §6 job/journal/runner parts, §7, §12 fakeapi). **Interfaces:** `docs/superpowers/plans/2026-08-27-claude-teleport-00-interfaces.md` — every exported name below matches it verbatim; additions are listed at the end under "Interface additions".

## Global Constraints

- Module `github.com/mithro/go-claude-teleport`; binary `claude-teleport`; Go `1.26`; `CGO_ENABLED=0`; Apache-2.0.
- Dependencies allowed: `golang.org/x/crypto`, `github.com/kevinburke/ssh_config`, `github.com/go-git/go-git/v5`, `github.com/spf13/cobra`, `github.com/google/go-cmp` (tests). Nothing else.
- No `ssh`, `rsync`, `tar`, `gzip`, `git` subprocesses in the tool. `tmux -C` and `claude --version` (preflight only) are the only subprocesses. (`claude` is additionally run by the fakeapi spike/test — test code only.)
- Never read `.credentials.json`, `sessions/*.key`, or token fields. The spike in Task 17 uses a throw-away `CLAUDE_CONFIG_DIR`.
- Every exported function that touches the filesystem takes explicit directories; only `internal/cli` reads the environment.
- Errors wrap with `%w` and carry the path/pid/op involved. No silent fallbacks.
- Tests: stdlib `testing`, `go-cmp`; fixtures in `testdata/`; hosts `laptop.example`, `jump.example`, `big-storage.example`; homes `/home/alice`; fresh uuids.
- Forbidden transfer paths (spec §7.1): `.credentials.json`, `sessions/`, `*.key`, `~/.claude.json`, `settings.json`, `plugins/`.
- Host keys verified against `known_hosts`; unknown hosts are an error with the fingerprint unless `StrictHostKeyChecking=accept-new`; accept-new appends an **unhashed** line.
- Protocol version: `version.Protocol == 1`; mismatch aborts with both versions (exit 4 in cli).
- Workflow per task: TDD (failing test first), `go test -race ./...` green, one conventional commit. The engineer works in `.worktrees/plan-02-transport` (branch `plan-02-transport`) and opens one PR for this plan.
- Plan 01 (in parallel) delivers `internal/session`, `internal/claudecfg`, `internal/procx`, `internal/placeholder`, `internal/version`, the `internal/cli` skeleton and `test/fakeclaude`. This plan consumes them by the names in the interfaces doc and never redefines them. If Plan 01 is not merged yet when a task needs one of those packages, rebase onto its branch first; do not stub them.

---

## File structure

```
go.mod / go.sum                              + golang.org/x/crypto, github.com/kevinburke/ssh_config
internal/sshx/target.go                      Target, ParseTarget, Quote
internal/sshx/resolve.go                     Resolved, Resolve (ssh_config + -o overrides, ProxyJump, ProxyCommand refusal)
internal/sshx/auth.go                        agent + identity-file auth methods, passphrase error
internal/sshx/hostkey.go                     known_hosts callback: yes | accept-new | no
internal/sshx/dial.go                        Options, Client, Dial (jump chain), Close, String, Redial
internal/sshx/process.go                     Process, Start, StartPty, Run
internal/sshx/sshtest/server.go              in-process ssh server for tests (exec handler, direct-tcpip, resolver map)
internal/sshx/sshtest/keys.go                GenKey, WriteKeyFile, KnownHostsLine
internal/sshx/testdata/ssh_config            laptop.example / jump.example / big-storage.example
internal/job/journal.go                      Journal, Dir, StagingDir, Open, New, Save, Step, FirstIncomplete, RunnerAlive
internal/job/run.go                          Step, Run
internal/job/history.go                      HistoryRecord, AppendHistory
internal/job/log.go                          TailLog, FollowLog
internal/transfer/manifest.go                Entry, Manifest, Build, Load, Save, ByID
internal/transfer/diff.go                    Status, Diff, Need, Blocking
internal/transfer/stream.go                  Send, Receive
internal/transfer/install.go                 InstallReport, InstallExtras, Install, Uninstall
internal/remote/protocol.go                  Request, Response, Error, StreamKind, HostInfo
internal/remote/plan03_types.go              opaque aliases for gitx/tmuxx types (Plan 03 replaces)
internal/remote/endpoint.go                  Endpoint interface
internal/remote/ops.go                       per-op args/result structs (shared by client and server)
internal/remote/server.go                    Serve, ServeStream, dispatch table
internal/remote/local.go                     Local, NewLocal, LocalOptions
internal/remote/client.go                    Client, NewClient, call multiplexing, OpenStream
internal/fakeapi/server.go                   Server, Options, New, Handler, Requests
internal/fakeapi/messages.go                 /v1/messages streaming + non-streaming bodies
internal/fakeapi/ENDPOINTS.md                observed endpoint list from the spike
internal/fakeapi/realclaude_test.go          build tag realclaude
test/fakeapi-server/main.go                  -addr / -reply wrapper for the docker harness
internal/cli/transport.go                    AddTransportCommands: remote serve|stream, internal-runner
internal/cli/status.go                       status <sid> [--json]
internal/cli/abandon.go                      abandon <sid> [--delete-destination-files]
internal/cli/remotecfg.go                    compare-config <host>, inspect --host <host> via remote.Client
```

The `cli` files plug into Plan 01's root command through one function, `AddTransportCommands(root *cobra.Command)`, called from the function in Plan 01's `internal/cli/root.go` that builds the root command (Task 20 shows the one-line modification).

---

### Task 1: Module dependencies, `sshx.Target`, `ParseTarget`, `Quote`

**Files:**
- Modify: `go.mod` (add requires)
- Create: `internal/sshx/target.go`
- Test: `internal/sshx/target_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `type Target struct{User, Host string; Port int; Via []Target}`; `func ParseTarget(s string) (Target, error)`; `func Quote(argv []string) string`.

- [ ] **Step 1: Add the dependencies**

Run:
```bash
go get golang.org/x/crypto@latest github.com/kevinburke/ssh_config@latest
go mod tidy
```
Expected: `go.mod` lists both under `require`.

- [ ] **Step 2: Write the failing test**

`internal/sshx/target_test.go`:
```go
package sshx

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in   string
		want Target
		err  bool
	}{
		{"big-storage.example", Target{Host: "big-storage.example"}, false},
		{"alice@big-storage.example", Target{User: "alice", Host: "big-storage.example"}, false},
		{"alice@big-storage.example:2222", Target{User: "alice", Host: "big-storage.example", Port: 2222}, false},
		{"[fd00::1]:2222", Target{Host: "fd00::1", Port: 2222}, false},
		{"", Target{}, true},
		{"@host", Target{}, true},
		{"host:notaport", Target{}, true},
		{"host:0", Target{}, true},
	}
	for _, c := range cases {
		got, err := ParseTarget(c.in)
		if (err != nil) != c.err {
			t.Errorf("ParseTarget(%q) err=%v want err=%v", c.in, err, c.err)
			continue
		}
		if !c.err && !cmp.Equal(got, c.want) {
			t.Errorf("ParseTarget(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestQuote(t *testing.T) {
	got := Quote([]string{"claude-teleport", "remote", "stream", "tar", "job 1", "it's"})
	want := `claude-teleport remote stream tar 'job 1' 'it'\''s'`
	if got != want {
		t.Errorf("Quote = %s, want %s", got, want)
	}
	if Quote(nil) != "" {
		t.Errorf("Quote(nil) should be empty")
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/sshx/ -run 'TestParseTarget|TestQuote' -v`
Expected: FAIL — `undefined: Target`, `undefined: ParseTarget`, `undefined: Quote`.

- [ ] **Step 4: Implement**

`internal/sshx/target.go`:
```go
// Package sshx is the in-binary ssh client: ssh_config resolution, agent and
// key-file auth, known_hosts verification and jump chains (spec §4.2).
package sshx

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Target is a parsed "[user@]host[:port]" plus an optional jump chain.
type Target struct {
	User string
	Host string   // as typed (alias) — resolved HostName lives in Resolved
	Port int      // 0 = not specified
	Via  []Target // jump chain, outermost first
}

var safeArg = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// ParseTarget parses "[user@]host[:port]"; IPv6 literals must be bracketed.
func ParseTarget(s string) (Target, error) {
	if s == "" {
		return Target{}, fmt.Errorf("ssh target: empty")
	}
	var t Target
	if i := strings.LastIndex(s, "@"); i >= 0 {
		t.User, s = s[:i], s[i+1:]
		if t.User == "" {
			return Target{}, fmt.Errorf("ssh target %q: empty user", s)
		}
	}
	host, port := s, ""
	if strings.HasPrefix(s, "[") || strings.Count(s, ":") == 1 {
		h, p, err := net.SplitHostPort(s)
		if err == nil {
			host, port = h, p
		} else if strings.HasPrefix(s, "[") {
			return Target{}, fmt.Errorf("ssh target %q: %w", s, err)
		}
	}
	if host == "" {
		return Target{}, fmt.Errorf("ssh target %q: empty host", s)
	}
	t.Host = host
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return Target{}, fmt.Errorf("ssh target %q: bad port %q", s, port)
		}
		t.Port = n
	}
	return t, nil
}

// Quote renders argv for the remote sh: safe words verbatim, others single-quoted.
func Quote(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && safeArg.MatchString(a) {
			parts[i] = a
			continue
		}
		parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(parts, " ")
}
```

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/sshx/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/sshx/target.go internal/sshx/target_test.go
git commit -m "feat(sshx): add x/crypto and ssh_config deps, ParseTarget and Quote"
```

---

### Task 2: `sshx.Resolve` — ssh_config and `-o` overrides

**Files:**
- Create: `internal/sshx/resolve.go`
- Create: `internal/sshx/testdata/ssh_config`
- Test: `internal/sshx/resolve_test.go`

**Interfaces:**
- Consumes: `Target`, `ParseTarget` (Task 1); `github.com/kevinburke/ssh_config` (`ssh_config.Decode`, `(*Config).Get`, `(*Config).GetAll`).
- Produces: `type Resolved struct{Target; HostName string; IdentityFiles []string; Options map[string]string}`; `func Resolve(t Target, cfg *ssh_config.Config, overrides map[string]string, localUser string) (Resolved, error)`; `var ErrProxyCommand`.

Semantics (spec §4.2): `Host` aliases are looked up in the config; `HostName`, `User`, `Port`, `IdentityFile` (all of them, `~` kept unexpanded — `Dial` expands with `Options.Home`), `ProxyJump` (comma list, parsed with `ParseTarget`, prepended to `t.Via`, outermost first). Overrides (`-o KEY=VALUE`, keys case-insensitive) win over both the target string and the config for `User`, `Port`, `IdentityFile`, `ProxyJump`; `StrictHostKeyChecking` and any other key are returned in `Options` for `Dial`. A `ProxyCommand` in the config for the host is an error naming the host and suggesting `--via`.

- [ ] **Step 1: Write the testdata config**

`internal/sshx/testdata/ssh_config`:
```
Host laptop
    HostName laptop.example
    User alice
    Port 22

Host jump
    HostName jump.example
    User alice
    IdentityFile ~/.ssh/id_jump

Host big-storage
    HostName big-storage.example
    User alice
    Port 2222
    ProxyJump jump
    IdentityFile ~/.ssh/id_storage
    IdentityFile ~/.ssh/id_ed25519

Host legacy
    HostName legacy.example
    ProxyCommand ssh -W %h:%p jump

Host *
    IdentityFile ~/.ssh/id_ed25519
```

- [ ] **Step 2: Write the failing test**

`internal/sshx/resolve_test.go`:
```go
package sshx

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kevinburke/ssh_config"
)

func loadTestConfig(t *testing.T) *ssh_config.Config {
	t.Helper()
	f, err := os.Open("testdata/ssh_config")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := ssh_config.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestResolveAliasAndProxyJump(t *testing.T) {
	cfg := loadTestConfig(t)
	r, err := Resolve(Target{Host: "big-storage"}, cfg, nil, "bob")
	if err != nil {
		t.Fatal(err)
	}
	want := Resolved{
		Target: Target{User: "alice", Host: "big-storage", Port: 2222,
			Via: []Target{{Host: "jump"}}},
		HostName:      "big-storage.example",
		IdentityFiles: []string{"~/.ssh/id_storage", "~/.ssh/id_ed25519"},
		Options:       map[string]string{},
	}
	if diff := cmp.Diff(want, r); diff != "" {
		t.Errorf("Resolve mismatch (-want +got):\n%s", diff)
	}
}

func TestResolveDefaultsAndExplicitVia(t *testing.T) {
	cfg := loadTestConfig(t)
	r, err := Resolve(Target{Host: "unknown.example", Via: []Target{{Host: "jump"}}}, cfg, nil, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if r.User != "bob" || r.Port != 22 || r.HostName != "unknown.example" {
		t.Errorf("defaults not applied: %+v", r)
	}
	if diff := cmp.Diff([]Target{{Host: "jump"}}, r.Via); diff != "" {
		t.Errorf("via (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"~/.ssh/id_ed25519"}, r.IdentityFiles); diff != "" {
		t.Errorf("identity files (-want +got):\n%s", diff)
	}
}

func TestResolveConfigProxyJumpPrependedToVia(t *testing.T) {
	cfg := loadTestConfig(t)
	r, err := Resolve(Target{Host: "big-storage", Via: []Target{{Host: "laptop"}}}, cfg, nil, "bob")
	if err != nil {
		t.Fatal(err)
	}
	// config ProxyJump (jump) is the hop closest to the target; --via laptop is outermost.
	want := []Target{{Host: "laptop"}, {Host: "jump"}}
	if diff := cmp.Diff(want, r.Via); diff != "" {
		t.Errorf("via (-want +got):\n%s", diff)
	}
}

func TestResolveOverrides(t *testing.T) {
	cfg := loadTestConfig(t)
	ov := map[string]string{
		"user": "carol", "Port": "2200", "IdentityFile": "/home/alice/.ssh/id_override",
		"StrictHostKeyChecking": "accept-new", "ProxyJump": "a.example,b.example",
	}
	r, err := Resolve(Target{User: "alice", Host: "laptop", Port: 22}, cfg, ov, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if r.User != "carol" || r.Port != 2200 {
		t.Errorf("overrides not applied: %+v", r.Target)
	}
	if diff := cmp.Diff([]string{"/home/alice/.ssh/id_override"}, r.IdentityFiles); diff != "" {
		t.Errorf("identity (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]Target{{Host: "a.example"}, {Host: "b.example"}}, r.Via); diff != "" {
		t.Errorf("via (-want +got):\n%s", diff)
	}
	if r.Options["StrictHostKeyChecking"] != "accept-new" {
		t.Errorf("Options = %v", r.Options)
	}
}

func TestResolveProxyCommandRefused(t *testing.T) {
	cfg := loadTestConfig(t)
	_, err := Resolve(Target{Host: "legacy"}, cfg, nil, "bob")
	if !errors.Is(err, ErrProxyCommand) {
		t.Fatalf("err = %v, want ErrProxyCommand", err)
	}
	if !strings.Contains(err.Error(), "legacy") || !strings.Contains(err.Error(), "--via") {
		t.Errorf("error should name the host and suggest --via: %v", err)
	}
}

func TestResolveNilConfig(t *testing.T) {
	r, err := Resolve(Target{Host: "x.example"}, nil, nil, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if r.HostName != "x.example" || r.User != "bob" || r.Port != 22 {
		t.Errorf("nil config defaults: %+v", r)
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/sshx/ -run TestResolve -v`
Expected: FAIL — `undefined: Resolve`, `undefined: Resolved`, `undefined: ErrProxyCommand`.

- [ ] **Step 4: Implement**

`internal/sshx/resolve.go`:
```go
package sshx

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/kevinburke/ssh_config"
)

// Resolved is a Target with ssh_config and -o overrides applied.
type Resolved struct {
	Target
	HostName      string            // from ssh_config HostName or Host
	IdentityFiles []string          // "~" NOT expanded here; Dial expands with Options.Home
	Options       map[string]string // remaining -o overrides (canonical key case)
}

// ErrProxyCommand is returned when the config uses ProxyCommand for the host.
var ErrProxyCommand = errors.New("ProxyCommand is not supported")

// canonicalOption maps a case-insensitive -o key to its canonical spelling.
func canonicalOption(k string) string {
	switch strings.ToLower(k) {
	case "user":
		return "User"
	case "port":
		return "Port"
	case "identityfile":
		return "IdentityFile"
	case "proxyjump":
		return "ProxyJump"
	case "stricthostkeychecking":
		return "StrictHostKeyChecking"
	case "userknownhostsfile":
		return "UserKnownHostsFile"
	case "connecttimeout":
		return "ConnectTimeout"
	}
	return k
}

func cfgGet(cfg *ssh_config.Config, alias, key string) string {
	if cfg == nil {
		return ""
	}
	v, err := cfg.Get(alias, key)
	if err != nil {
		return ""
	}
	return v
}

func cfgGetAll(cfg *ssh_config.Config, alias, key string) []string {
	if cfg == nil {
		return nil
	}
	vs, err := cfg.GetAll(alias, key)
	if err != nil {
		return nil
	}
	return vs
}

func parseJumpList(s string) ([]Target, error) {
	var out []Target
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" || part == "none" {
			continue
		}
		t, err := ParseTarget(part)
		if err != nil {
			return nil, fmt.Errorf("ProxyJump %q: %w", s, err)
		}
		out = append(out, t)
	}
	return out, nil
}

// Resolve applies ~/.ssh/config (Host/HostName/User/Port/IdentityFile/ProxyJump)
// and -o overrides. The chain is outermost-first: explicit --via hops (t.Via)
// come first, then the config's ProxyJump hops, which sit closest to the target.
func Resolve(t Target, cfg *ssh_config.Config, overrides map[string]string, localUser string) (Resolved, error) {
	ov := map[string]string{}
	for k, v := range overrides {
		ov[canonicalOption(k)] = v
	}
	r := Resolved{Target: t, Options: map[string]string{}}

	if pc := cfgGet(cfg, t.Host, "ProxyCommand"); pc != "" && pc != "none" {
		return Resolved{}, fmt.Errorf("host %q: %w (config has ProxyCommand %q); use --via <jump> instead", t.Host, ErrProxyCommand, pc)
	}

	r.HostName = cfgGet(cfg, t.Host, "HostName")
	if r.HostName == "" {
		r.HostName = t.Host
	}

	if u, ok := ov["User"]; ok {
		r.User = u
	} else if r.User == "" {
		r.User = cfgGet(cfg, t.Host, "User")
	}
	if r.User == "" {
		r.User = localUser
	}

	if p, ok := ov["Port"]; ok {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return Resolved{}, fmt.Errorf("-o Port=%q: not a port", p)
		}
		r.Port = n
	} else if r.Port == 0 {
		if p := cfgGet(cfg, t.Host, "Port"); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil {
				return Resolved{}, fmt.Errorf("ssh_config Port %q for %q: %w", p, t.Host, err)
			}
			r.Port = n
		}
	}
	if r.Port == 0 {
		r.Port = 22
	}

	if f, ok := ov["IdentityFile"]; ok {
		r.IdentityFiles = []string{f}
	} else {
		for _, f := range cfgGetAll(cfg, t.Host, "IdentityFile") {
			if f != "" {
				r.IdentityFiles = append(r.IdentityFiles, f)
			}
		}
	}

	jumpSpec, has := ov["ProxyJump"]
	if !has {
		jumpSpec = cfgGet(cfg, t.Host, "ProxyJump")
	}
	jumps, err := parseJumpList(jumpSpec)
	if err != nil {
		return Resolved{}, fmt.Errorf("host %q: %w", t.Host, err)
	}
	r.Via = append(append([]Target{}, t.Via...), jumps...)

	for k, v := range ov {
		switch k {
		case "User", "Port", "IdentityFile", "ProxyJump":
		default:
			r.Options[k] = v
		}
	}
	return r, nil
}
```

Note on ordering: the interfaces doc says "ProxyJump from config is prepended to Via"; read with the spec's "`--via` … outermost first; composes with `ProxyJump`", the config hop is the one adjacent to the target, so it goes *after* the explicit `--via` hops in the outermost-first list. The test `TestResolveConfigProxyJumpPrependedToVia` pins `[laptop, jump]`.

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/sshx/ -v`
Expected: PASS (6 Resolve tests + Task 1 tests).

- [ ] **Step 6: Commit**

```bash
git add internal/sshx/resolve.go internal/sshx/resolve_test.go internal/sshx/testdata/ssh_config
git commit -m "feat(sshx): resolve targets through ssh_config with -o overrides and ProxyJump"
```

---

### Task 3: `sshtest` — in-process ssh server for tests

**Files:**
- Create: `internal/sshx/sshtest/keys.go`
- Create: `internal/sshx/sshtest/server.go`
- Test: `internal/sshx/sshtest/server_test.go`

**Interfaces:**
- Consumes: `golang.org/x/crypto/ssh`.
- Produces (exported, reused by `internal/remote` tests and Plan 03):
  - `func GenKey(t testing.TB) (ssh.Signer, ssh.PublicKey)`
  - `func WriteKeyFile(t testing.TB, dir, name string, passphrase string) (path string, signer ssh.Signer)`
  - `func KnownHostsLine(addr string, key ssh.PublicKey) string`
  - `type ExecFunc func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int`
  - `type Options struct{ Authorized []ssh.PublicKey; Exec ExecFunc; Resolver map[string]string; Logf func(string, ...any) }`
  - `type Server struct{ Addr string; HostKey ssh.PublicKey; … }`
  - `func New(t testing.TB, o Options) *Server`; `func (s *Server) Close()`; `func (s *Server) Forwarded() []string` (host:port strings requested via direct-tcpip); `func (s *Server) Execs() []string`.

Behaviour: accepts public-key auth for any key in `Authorized`; `session` channels handle `pty-req` (accepted), `env` (accepted), `exec` (runs `Exec` with the channel as stdio, sends `exit-status`), `shell` (treated as `exec ""`); `direct-tcpip` channels resolve the requested host through `Resolver` (host name → "127.0.0.1:port"), refuse with `Prohibited` if the name is unknown, else `net.Dial` and pipe both ways. All local dials happen inside the server (the "jump" host), which is what the jump-chain test relies on.

- [ ] **Step 1: Write the failing test**

`internal/sshx/sshtest/server_test.go`:
```go
package sshtest

import (
	"bytes"
	"io"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestExecAndForward(t *testing.T) {
	clientSigner, clientPub := GenKey(t)
	dest := New(t, Options{
		Authorized: []ssh.PublicKey{clientPub},
		Exec: func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
			io.WriteString(stdout, "dest ran: "+cmd+"\n")
			return 0
		},
	})
	jump := New(t, Options{
		Authorized: []ssh.PublicKey{clientPub},
		Resolver:   map[string]string{"dest.private": dest.Addr},
		Exec: func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
			io.WriteString(stderr, "no exec on jump\n")
			return 7
		},
	})

	cfg := &ssh.ClientConfig{
		User:            "alice",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: ssh.FixedHostKey(jump.HostKey),
	}
	jc, err := ssh.Dial("tcp", jump.Addr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer jc.Close()

	// exec on the jump: exit status 7 and stderr text
	sess, err := jc.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	err = sess.Run("true")
	var ee *ssh.ExitError
	if err == nil || !errorsAs(err, &ee) || ee.ExitStatus() != 7 {
		t.Fatalf("Run on jump: err=%v, want ExitError 7", err)
	}
	if !strings.Contains(stderr.String(), "no exec on jump") {
		t.Errorf("stderr = %q", stderr.String())
	}

	// direct-tcpip to a name only the jump can resolve
	conn, err := jc.Dial("tcp", "dest.private:22")
	if err != nil {
		t.Fatal(err)
	}
	dcfg := &ssh.ClientConfig{
		User:            "alice",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: ssh.FixedHostKey(dest.HostKey),
	}
	cc, chans, reqs, err := ssh.NewClientConn(conn, "dest.private:22", dcfg)
	if err != nil {
		t.Fatal(err)
	}
	dc := ssh.NewClient(cc, chans, reqs)
	defer dc.Close()
	out, err := func() ([]byte, error) {
		s, err := dc.NewSession()
		if err != nil {
			return nil, err
		}
		defer s.Close()
		return s.Output("echo hi")
	}()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "dest ran: echo hi\n" {
		t.Errorf("output = %q", out)
	}
	if got := jump.Forwarded(); len(got) != 1 || got[0] != "dest.private:22" {
		t.Errorf("Forwarded = %v", got)
	}

	// unknown name is refused
	if _, err := jc.Dial("tcp", "nowhere.private:22"); err == nil {
		t.Errorf("expected refusal for unknown host")
	}
	_ = net.Dial
}

func errorsAs(err error, target **ssh.ExitError) bool {
	e, ok := err.(*ssh.ExitError)
	if ok {
		*target = e
	}
	return ok
}

func TestKeyFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path, signer := WriteKeyFile(t, dir, "id_ed25519", "")
	raw, err := readFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(parsed.PublicKey().Marshal(), signer.PublicKey().Marshal()) {
		t.Errorf("public keys differ")
	}
	_, s2 := WriteKeyFile(t, dir, "id_locked", "secret")
	raw2, _ := readFile(dir + "/id_locked")
	if _, err := ssh.ParsePrivateKey(raw2); err == nil {
		t.Errorf("passphrase-protected key parsed without passphrase")
	}
	line := KnownHostsLine("[127.0.0.1]:2222", s2.PublicKey())
	if !strings.HasPrefix(line, "[127.0.0.1]:2222 ssh-ed25519 ") {
		t.Errorf("KnownHostsLine = %q", line)
	}
}
```
And add to the same file the tiny helper:
```go
func readFile(p string) ([]byte, error) { return os.ReadFile(p) }
```
(with `"os"` imported).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/sshx/sshtest/ -v`
Expected: FAIL — `undefined: GenKey`, `New`, `Options`, ….

- [ ] **Step 3: Implement keys.go**

`internal/sshx/sshtest/keys.go`:
```go
// Package sshtest is an in-process ssh server for tests: exec requests are
// handed to a Go function and direct-tcpip channels are resolved through an
// injected name map, so a "jump" server can forward to a "dest" server whose
// name only the jump knows.
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

// GenKey makes a fresh ed25519 key pair.
func GenKey(t testing.TB) (ssh.Signer, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer, signer.PublicKey()
}

// WriteKeyFile writes a new OpenSSH-format private key (optionally
// passphrase-protected) to dir/name with mode 0600 and returns its signer.
func WriteKeyFile(t testing.TB, dir, name, passphrase string) (string, ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "sshtest")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "sshtest", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return path, signer
}

// KnownHostsLine renders one unhashed known_hosts line for addr
// ("host" or "[host]:port").
func KnownHostsLine(addr string, key ssh.PublicKey) string {
	return addr + " " + key.Type() + " " + base64Marshal(key) + "\n"
}
```
plus, in the same file:
```go
import "encoding/base64"

func base64Marshal(key ssh.PublicKey) string {
	return base64.StdEncoding.EncodeToString(key.Marshal())
}
```
(merge the import into the block above).

- [ ] **Step 4: Implement server.go**

`internal/sshx/sshtest/server.go`:
```go
package sshtest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// ExecFunc handles one exec request and returns the exit status.
type ExecFunc func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int

// Options configures a test server.
type Options struct {
	Authorized []ssh.PublicKey   // keys accepted for any user
	Exec       ExecFunc          // nil = every exec exits 127 with "exec not configured"
	Resolver   map[string]string // name -> "127.0.0.1:port" for direct-tcpip; nil = refuse all
	Logf       func(string, ...any)
}

// Server is a running in-process ssh server bound to 127.0.0.1.
type Server struct {
	Addr    string
	HostKey ssh.PublicKey

	ln     net.Listener
	config *ssh.ServerConfig
	opts   Options
	mu     sync.Mutex
	fwd    []string
	execs  []string
	wg     sync.WaitGroup
}

// New starts a server; it is closed by t.Cleanup.
func New(t testing.TB, o Options) *Server {
	t.Helper()
	hostSigner, hostPub := GenKey(t)
	if o.Logf == nil {
		o.Logf = t.Logf
	}
	s := &Server{HostKey: hostPub, opts: o}
	s.config = &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			for _, k := range o.Authorized {
				if bytes.Equal(k.Marshal(), key.Marshal()) {
					return &ssh.Permissions{}, nil
				}
			}
			return nil, fmt.Errorf("sshtest: key not authorized for %q", conn.User())
		},
	}
	s.config.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.ln = ln
	s.Addr = ln.Addr().String()
	s.wg.Add(1)
	go s.acceptLoop()
	t.Cleanup(s.Close)
	return s
}

// Close stops accepting; in-flight handlers are abandoned.
func (s *Server) Close() { s.ln.Close() }

// Forwarded lists "host:port" strings requested via direct-tcpip.
func (s *Server) Forwarded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.fwd...)
}

// Execs lists the exec command lines received.
func (s *Server) Execs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.execs...)
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(c)
	}
}

func (s *Server) handleConn(c net.Conn) {
	sc, chans, reqs, err := ssh.NewServerConn(c, s.config)
	if err != nil {
		s.opts.Logf("sshtest: handshake: %v", err)
		c.Close()
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			ch, creqs, err := nc.Accept()
			if err != nil {
				continue
			}
			go s.handleSession(ch, creqs)
		case "direct-tcpip":
			go s.handleDirectTCPIP(nc)
		default:
			nc.Reject(ssh.UnknownChannelType, "sshtest: unsupported channel "+nc.ChannelType())
		}
	}
}

type execPayload struct{ Command string }

func (s *Server) handleSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "pty-req", "env", "window-change":
			req.Reply(true, nil)
		case "exec", "shell":
			var cmd string
			if req.Type == "exec" {
				var p execPayload
				if err := ssh.Unmarshal(req.Payload, &p); err != nil {
					req.Reply(false, nil)
					continue
				}
				cmd = p.Command
			}
			req.Reply(true, nil)
			s.mu.Lock()
			s.execs = append(s.execs, cmd)
			s.mu.Unlock()
			status := 127
			if s.opts.Exec != nil {
				status = s.opts.Exec(cmd, ch, ch, ch.Stderr())
			} else {
				io.WriteString(ch.Stderr(), "sshtest: exec not configured\n")
			}
			var buf [4]byte
			binary.BigEndian.PutUint32(buf[:], uint32(status))
			ch.SendRequest("exit-status", false, buf[:])
			return
		default:
			req.Reply(false, nil)
		}
	}
}

type directTCPIPPayload struct {
	Host       string
	Port       uint32
	OrigHost   string
	OrigPort   uint32
}

func (s *Server) handleDirectTCPIP(nc ssh.NewChannel) {
	var p directTCPIPPayload
	if err := ssh.Unmarshal(nc.ExtraData(), &p); err != nil {
		nc.Reject(ssh.ConnectionFailed, "sshtest: bad direct-tcpip payload")
		return
	}
	requested := net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
	s.mu.Lock()
	s.fwd = append(s.fwd, requested)
	s.mu.Unlock()
	target, ok := s.opts.Resolver[p.Host]
	if !ok {
		nc.Reject(ssh.Prohibited, "sshtest: cannot resolve "+p.Host)
		return
	}
	conn, err := net.Dial("tcp", target)
	if err != nil {
		nc.Reject(ssh.ConnectionFailed, "sshtest: dial "+target+": "+err.Error())
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(ch, conn); ch.CloseWrite() }()
	go func() { defer wg.Done(); io.Copy(conn, ch); conn.(*net.TCPConn).CloseWrite() }()
	wg.Wait()
	ch.Close()
	conn.Close()
}
```

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/sshx/sshtest/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/sshx/sshtest/
git commit -m "test(sshx): in-process ssh server with exec handler and resolver-backed direct-tcpip"
```

---

### Task 4: Authentication methods and known_hosts policy

**Files:**
- Create: `internal/sshx/auth.go`
- Create: `internal/sshx/hostkey.go`
- Test: `internal/sshx/auth_test.go`, `internal/sshx/hostkey_test.go`

**Interfaces:**
- Consumes: `sshtest.WriteKeyFile`, `sshtest.GenKey`, `sshtest.KnownHostsLine` (Task 3).
- Produces (unexported except the error):
  - `var ErrPassphrase = errors.New("private key is passphrase-protected")`
  - `func authMethods(agentSocket string, identityFiles []string, home string, logf func(string, ...any)) ([]ssh.AuthMethod, func(), error)` — agent first (if socket set and reachable), then every readable key file in `identityFiles` plus the defaults `~/.ssh/id_ed25519`, `~/.ssh/id_rsa`, `~/.ssh/id_ecdsa` (deduplicated, missing files skipped, passphrase-protected files → `ErrPassphrase` naming the file). Returns a cleanup func that closes the agent connection.
  - `func expandHome(p, home string) string`
  - `func hostKeyCallback(knownHostsFile, strict string, logf func(string, ...any)) (ssh.HostKeyCallback, error)` — `"yes"` (default when `""`): known → ok, unknown → error with SHA256 fingerprint, mismatch → error; `"accept-new"`: unknown → append `KnownHostsLine`-style unhashed line (creating the file 0600 and its dir 0700), mismatch → error; `"no"`: always ok (logged). Any other value → error.

- [ ] **Step 1: Write the failing tests**

`internal/sshx/auth_test.go`:
```go
package sshx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/sshx/sshtest"
)

func TestAuthMethodsFromIdentityFiles(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	os.MkdirAll(sshDir, 0o700)
	sshtest.WriteKeyFile(t, sshDir, "id_ed25519", "")
	extra, _ := sshtest.WriteKeyFile(t, home, "id_storage", "")

	methods, cleanup, err := authMethods("", []string{"~/id_storage", "~/.ssh/missing"}, home, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	// one method per key file: id_storage (explicit) + id_ed25519 (default)
	if len(methods) != 2 {
		t.Errorf("got %d methods, want 2 (explicit %s + default id_ed25519)", len(methods), extra)
	}
}

func TestAuthMethodsPassphraseIsClearError(t *testing.T) {
	home := t.TempDir()
	path, _ := sshtest.WriteKeyFile(t, home, "id_locked", "hunter2")
	_, _, err := authMethods("", []string{path}, home, t.Logf)
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("err = %v, want ErrPassphrase", err)
	}
	if !contains(err.Error(), "id_locked") {
		t.Errorf("error should name the file: %v", err)
	}
}

func TestAuthMethodsNoKeysIsError(t *testing.T) {
	_, _, err := authMethods("", nil, t.TempDir(), t.Logf)
	if err == nil {
		t.Fatal("expected an error when no key source is available")
	}
}

func TestExpandHome(t *testing.T) {
	if got := expandHome("~/.ssh/id_x", "/home/alice"); got != "/home/alice/.ssh/id_x" {
		t.Errorf("expandHome = %q", got)
	}
	if got := expandHome("/abs", "/home/alice"); got != "/abs" {
		t.Errorf("expandHome abs = %q", got)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

`internal/sshx/hostkey_test.go`:
```go
package sshx

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/sshx/sshtest"
)

func TestHostKeyStrictYes(t *testing.T) {
	_, known := sshtest.GenKey(t)
	_, other := sshtest.GenKey(t)
	file := filepath.Join(t.TempDir(), "known_hosts")
	os.WriteFile(file, []byte(sshtest.KnownHostsLine("big-storage.example", known)), 0o600)
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}

	cb, err := hostKeyCallback(file, "", t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if err := cb("big-storage.example:22", addr, known); err != nil {
		t.Errorf("known key rejected: %v", err)
	}
	err = cb("unknown.example:22", addr, known)
	if err == nil || !strings.Contains(err.Error(), "SHA256:") {
		t.Errorf("unknown host: err=%v, want fingerprint error", err)
	}
	err = cb("big-storage.example:22", addr, other)
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("changed key: err=%v, want mismatch error", err)
	}
}

func TestHostKeyAcceptNewAppendsUnhashed(t *testing.T) {
	_, key := sshtest.GenKey(t)
	file := filepath.Join(t.TempDir(), "sub", "known_hosts") // dir does not exist yet
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2222}
	cb, err := hostKeyCallback(file, "accept-new", t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if err := cb("big-storage.example:2222", addr, key); err != nil {
		t.Fatalf("accept-new first contact: %v", err)
	}
	data, _ := os.ReadFile(file)
	if !strings.HasPrefix(string(data), "[big-storage.example]:2222 ssh-ed25519 ") {
		t.Errorf("known_hosts line = %q (must be unhashed, bracketed non-22 port)", data)
	}
	// second contact with the same key is fine; a different key is a mismatch
	if err := cb("big-storage.example:2222", addr, key); err != nil {
		t.Errorf("second contact: %v", err)
	}
	_, other := sshtest.GenKey(t)
	if err := cb("big-storage.example:2222", addr, other); err == nil {
		t.Errorf("accept-new must still refuse a changed key")
	}
}

func TestHostKeyStrictNo(t *testing.T) {
	_, key := sshtest.GenKey(t)
	cb, err := hostKeyCallback(filepath.Join(t.TempDir(), "kh"), "no", t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if err := cb("x.example:22", &net.TCPAddr{}, key); err != nil {
		t.Errorf("strict=no rejected: %v", err)
	}
	if _, err := hostKeyCallback("kh", "maybe", t.Logf); err == nil {
		t.Errorf("bad StrictHostKeyChecking value must be an error")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/sshx/ -run 'TestAuth|TestExpandHome|TestHostKey' -v`
Expected: FAIL — undefined `authMethods`, `expandHome`, `hostKeyCallback`, `ErrPassphrase`.

- [ ] **Step 3: Implement auth.go**

`internal/sshx/auth.go`:
```go
package sshx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// ErrPassphrase is returned for an encrypted private key: the tool never
// prompts, so the user must load the key into ssh-agent instead.
var ErrPassphrase = errors.New("private key is passphrase-protected (load it into ssh-agent)")

var defaultIdentityFiles = []string{"~/.ssh/id_ed25519", "~/.ssh/id_rsa", "~/.ssh/id_ecdsa"}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// authMethods builds the auth list: ssh-agent (if agentSocket is set and
// connects), then one PublicKeys method per readable identity file.
func authMethods(agentSocket string, identityFiles []string, home string, logf func(string, ...any)) ([]ssh.AuthMethod, func(), error) {
	var methods []ssh.AuthMethod
	cleanup := func() {}
	if agentSocket != "" {
		conn, err := net.Dial("unix", agentSocket)
		if err != nil {
			logf("ssh-agent %s: %v (continuing with key files)", agentSocket, err)
		} else {
			ag := agent.NewClient(conn)
			methods = append(methods, ssh.PublicKeysCallback(ag.Signers))
			cleanup = func() { conn.Close() }
		}
	}
	seen := map[string]bool{}
	for _, f := range append(append([]string{}, identityFiles...), defaultIdentityFiles...) {
		path := expandHome(f, home)
		if seen[path] {
			continue
		}
		seen[path] = true
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("identity file %s: %w", path, err)
		}
		signer, err := ssh.ParsePrivateKey(raw)
		if err != nil {
			var pm *ssh.PassphraseMissingError
			if errors.As(err, &pm) {
				cleanup()
				return nil, nil, fmt.Errorf("identity file %s: %w", path, ErrPassphrase)
			}
			cleanup()
			return nil, nil, fmt.Errorf("identity file %s: %w", path, err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if len(methods) == 0 {
		return nil, nil, fmt.Errorf("no ssh authentication available: no ssh-agent (SSH_AUTH_SOCK) and no key file among %s", strings.Join(defaultIdentityFiles, ", "))
	}
	return methods, cleanup, nil
}
```

- [ ] **Step 4: Implement hostkey.go**

`internal/sshx/hostkey.go`:
```go
package sshx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// hostKeyCallback implements StrictHostKeyChecking yes|accept-new|no over
// one known_hosts file. accept-new appends an UNHASHED line (spec §4.2).
func hostKeyCallback(knownHostsFile, strict string, logf func(string, ...any)) (ssh.HostKeyCallback, error) {
	switch strict {
	case "", "yes", "accept-new", "no":
	default:
		return nil, fmt.Errorf("StrictHostKeyChecking=%q: want yes, accept-new or no", strict)
	}
	if strict == "no" {
		return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			logf("host key checking disabled for %s (%s)", hostname, ssh.FingerprintSHA256(key))
			return nil
		}, nil
	}
	var mu sync.Mutex
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		mu.Lock()
		defer mu.Unlock()
		if err := os.MkdirAll(filepath.Dir(knownHostsFile), 0o700); err != nil {
			return fmt.Errorf("known_hosts dir: %w", err)
		}
		f, err := os.OpenFile(knownHostsFile, os.O_RDONLY|os.O_CREATE, 0o600)
		if err != nil {
			return fmt.Errorf("known_hosts %s: %w", knownHostsFile, err)
		}
		f.Close()
		check, err := knownhosts.New(knownHostsFile)
		if err != nil {
			return fmt.Errorf("known_hosts %s: %w", knownHostsFile, err)
		}
		err = check(hostname, remote, key)
		if err == nil {
			return nil
		}
		var ke *knownhosts.KeyError
		if !errors.As(err, &ke) {
			return fmt.Errorf("known_hosts %s: %w", knownHostsFile, err)
		}
		if len(ke.Want) > 0 {
			return fmt.Errorf("host key mismatch for %s: offered %s %s, known_hosts %s has a different key (line %d) — refusing", hostname, key.Type(), ssh.FingerprintSHA256(key), knownHostsFile, ke.Want[0].Line)
		}
		if strict != "accept-new" {
			return fmt.Errorf("unknown host %s: key %s %s is not in %s (re-run with -o StrictHostKeyChecking=accept-new to add it)", hostname, key.Type(), ssh.FingerprintSHA256(key), knownHostsFile)
		}
		line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key) + "\n"
		af, err := os.OpenFile(knownHostsFile, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("known_hosts %s: %w", knownHostsFile, err)
		}
		defer af.Close()
		if _, err := af.WriteString(line); err != nil {
			return fmt.Errorf("known_hosts %s: %w", knownHostsFile, err)
		}
		logf("added %s (%s) to %s", hostname, ssh.FingerprintSHA256(key), knownHostsFile)
		return nil
	}, nil
}
```
`knownhosts.Normalize("big-storage.example:2222")` yields `[big-storage.example]:2222` and `"host:22"` yields `host`, which is exactly what the tests assert; `knownhosts.Line` never hashes.

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/sshx/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/sshx/auth.go internal/sshx/hostkey.go internal/sshx/auth_test.go internal/sshx/hostkey_test.go
git commit -m "feat(sshx): agent and key-file auth, known_hosts policy with unhashed accept-new"
```

---

### Task 5: `sshx.Dial` (single hop), `Client`, `Process`, `Start`/`StartPty`/`Run`

**Files:**
- Create: `internal/sshx/dial.go`
- Create: `internal/sshx/process.go`
- Test: `internal/sshx/dial_test.go`

**Interfaces:**
- Consumes: Tasks 1–4; `sshtest` (Task 3).
- Produces (verbatim from the interfaces doc, plus `Options.Home` and `Options.NetDial` additions):
```go
type Options struct {
    KnownHostsFile string
    AgentSocket    string
    StrictHostKey  string          // "yes" (default) | "accept-new" | "no"
    ConnectTimeout time.Duration
    Logf           func(string, ...any)
    Home           string          // ADDITION: for "~" in identity files
    NetDial        func(ctx context.Context, network, addr string) (net.Conn, error) // ADDITION: first hop dialer (tests inject a recorder); nil = net.Dialer
}
type Client struct{ /* wraps *ssh.Client and the jump clients */ }
func Dial(ctx context.Context, r Resolved, cfg *ssh_config.Config, overrides map[string]string, o Options) (*Client, error)
func (c *Client) Close() error
func (c *Client) String() string
type Process struct{ Stdin io.WriteCloser; Stdout io.Reader; Stderr io.Reader; Wait func() error; Close func() error }
func (c *Client) Start(ctx context.Context, cmd string) (*Process, error)
func (c *Client) StartPty(ctx context.Context, cmd string, rows, cols int) (*Process, error)
func (c *Client) Run(ctx context.Context, cmd string, stdin io.Reader) ([]byte, []byte, error)
```
`Dial` in this task handles `len(r.Via)==0`; Task 6 adds the chain (the function is written chain-ready now: a loop over hops with `dialThrough`).

- [ ] **Step 1: Write the failing test**

`internal/sshx/dial_test.go`:
```go
package sshx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mithro/go-claude-teleport/internal/sshx/sshtest"
)

// testHome creates a fake $HOME with one client key and returns it plus the key's public half.
func testHome(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, signer := sshtest.WriteKeyFile(t, sshDir, "id_ed25519", "")
	return home, signer.PublicKey()
}

func hostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, c := range p {
		n = n*10 + int(c-'0')
	}
	return h, n
}

func echoExec(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
	in, _ := io.ReadAll(stdin)
	io.WriteString(stdout, "cmd="+cmd+" stdin="+string(in))
	if strings.HasPrefix(cmd, "fail") {
		io.WriteString(stderr, "boom")
		return 3
	}
	return 0
}

func TestDialRunAndExitError(t *testing.T) {
	home, pub := testHome(t)
	srv := sshtest.New(t, sshtest.Options{Authorized: []ssh.PublicKey{pub}, Exec: echoExec})
	host, port := hostPort(t, srv.Addr)
	kh := filepath.Join(home, ".ssh", "known_hosts")
	os.WriteFile(kh, []byte(sshtest.KnownHostsLine(knownHostsName(host, port), srv.HostKey)), 0o600)

	r := Resolved{Target: Target{User: "alice", Host: host, Port: port}, HostName: host}
	c, err := Dial(context.Background(), r, nil, nil, Options{KnownHostsFile: kh, Home: home, Logf: t.Logf, ConnectTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.String() != "alice@"+host {
		t.Errorf("String = %q", c.String())
	}

	out, errOut, err := c.Run(context.Background(), "echo hi", strings.NewReader("input"))
	if err != nil {
		t.Fatalf("Run: %v (stderr %q)", err, errOut)
	}
	if string(out) != "cmd=echo hi stdin=input" {
		t.Errorf("stdout = %q", out)
	}

	_, errOut, err = c.Run(context.Background(), "fail now", nil)
	var ee *ssh.ExitError
	if !errors.As(err, &ee) || ee.ExitStatus() != 3 {
		t.Fatalf("Run fail: err=%v, want ExitError 3", err)
	}
	if string(errOut) != "boom" {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestStartStreamsAndClose(t *testing.T) {
	home, pub := testHome(t)
	srv := sshtest.New(t, sshtest.Options{Authorized: []ssh.PublicKey{pub}, Exec: echoExec})
	host, port := hostPort(t, srv.Addr)
	kh := filepath.Join(home, ".ssh", "known_hosts")
	os.WriteFile(kh, []byte(sshtest.KnownHostsLine(knownHostsName(host, port), srv.HostKey)), 0o600)
	r := Resolved{Target: Target{User: "alice", Host: host, Port: port}, HostName: host}
	c, err := Dial(context.Background(), r, nil, nil, Options{KnownHostsFile: kh, Home: home, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	p, err := c.Start(context.Background(), "cat")
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(p.Stdin, "abc")
	p.Stdin.Close()
	var buf bytes.Buffer
	io.Copy(&buf, p.Stdout)
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "cmd=cat stdin=abc" {
		t.Errorf("stdout = %q", buf.String())
	}
	p.Close()

	pty, err := c.StartPty(context.Background(), "tty", 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	pty.Stdin.Close()
	data, _ := io.ReadAll(pty.Stdout)
	if !strings.HasPrefix(string(data), "cmd=tty") {
		t.Errorf("pty stdout = %q", data)
	}
	pty.Close()
}

func TestDialUnknownHostRefused(t *testing.T) {
	home, pub := testHome(t)
	srv := sshtest.New(t, sshtest.Options{Authorized: []ssh.PublicKey{pub}})
	host, port := hostPort(t, srv.Addr)
	r := Resolved{Target: Target{User: "alice", Host: host, Port: port}, HostName: host}
	_, err := Dial(context.Background(), r, nil, nil, Options{KnownHostsFile: filepath.Join(home, ".ssh", "known_hosts"), Home: home, Logf: t.Logf})
	if err == nil || !strings.Contains(err.Error(), "unknown host") {
		t.Fatalf("err = %v, want unknown host", err)
	}
}
```
And in `dial_test.go` the helper used above:
```go
func knownHostsName(host string, port int) string {
	if port == 22 {
		return host
	}
	return "[" + host + "]:" + itoa(port)
}
func itoa(n int) string { return strconv.Itoa(n) }
```
(import `strconv`.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/sshx/ -run 'TestDial|TestStart' -v`
Expected: FAIL — undefined `Dial`, `Options`, `Client`.

- [ ] **Step 3: Implement dial.go**

`internal/sshx/dial.go`:
```go
package sshx

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/kevinburke/ssh_config"
	"golang.org/x/crypto/ssh"
)

// Options controls authentication, host verification and timeouts.
type Options struct {
	KnownHostsFile string
	AgentSocket    string        // $SSH_AUTH_SOCK
	StrictHostKey  string        // "yes" (default) | "accept-new" | "no"
	ConnectTimeout time.Duration // 0 = 15s
	Logf           func(string, ...any)
	Home           string // for "~" in identity files
	NetDial        func(ctx context.Context, network, addr string) (net.Conn, error) // first hop only; nil = net.Dialer
}

func (o Options) logf() func(string, ...any) {
	if o.Logf != nil {
		return o.Logf
	}
	return func(string, ...any) {}
}

// Client wraps the final *ssh.Client and every jump client under it.
type Client struct {
	ssh    *ssh.Client
	jumps  []*ssh.Client // outermost first
	desc   string
	closes []func()
}

// SSH exposes the underlying client (used by remote tests and Plan 03 pty runs).
func (c *Client) SSH() *ssh.Client { return c.ssh }

// Close closes the target connection, then each jump, innermost first.
func (c *Client) Close() error {
	err := c.ssh.Close()
	for i := len(c.jumps) - 1; i >= 0; i-- {
		c.jumps[i].Close()
	}
	for _, f := range c.closes {
		f()
	}
	return err
}

// String renders user@host (via a, b).
func (c *Client) String() string { return c.desc }

func hopAddr(r Resolved) string { return net.JoinHostPort(r.HostName, strconv.Itoa(r.Port)) }

func clientConfig(r Resolved, o Options) (*ssh.ClientConfig, func(), error) {
	strict := o.StrictHostKey
	if v, ok := r.Options["StrictHostKeyChecking"]; ok {
		strict = v
	}
	cb, err := hostKeyCallback(o.KnownHostsFile, strict, o.logf())
	if err != nil {
		return nil, nil, err
	}
	methods, cleanup, err := authMethods(o.AgentSocket, r.IdentityFiles, o.Home, o.logf())
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", r.Host, err)
	}
	timeout := o.ConnectTimeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	return &ssh.ClientConfig{User: r.User, Auth: methods, HostKeyCallback: cb, Timeout: timeout}, cleanup, nil
}

// Dial connects through the jump chain; each hop's hostname is resolved by
// the previous hop (client.Dial("tcp", host:port)), never locally.
func Dial(ctx context.Context, r Resolved, cfg *ssh_config.Config, overrides map[string]string, o Options) (*Client, error) {
	logf := o.logf()
	hops := make([]Resolved, 0, len(r.Via)+1)
	for _, v := range r.Via {
		rv, err := Resolve(v, cfg, nil, r.User)
		if err != nil {
			return nil, fmt.Errorf("jump %s: %w", v.Host, err)
		}
		hops = append(hops, rv)
	}
	hops = append(hops, r)

	c := &Client{}
	var prev *ssh.Client
	for i, hop := range hops {
		conf, cleanup, err := clientConfig(hop, o)
		if err != nil {
			c.closeAll()
			return nil, err
		}
		c.closes = append(c.closes, cleanup)
		addr := hopAddr(hop)
		var raw net.Conn
		if prev == nil {
			dial := o.NetDial
			if dial == nil {
				d := &net.Dialer{Timeout: conf.Timeout}
				dial = d.DialContext
			}
			raw, err = dial(ctx, "tcp", addr)
		} else {
			raw, err = prev.Dial("tcp", addr)
		}
		if err != nil {
			c.closeAll()
			return nil, fmt.Errorf("dial %s (%s): %w", hop.Host, addr, err)
		}
		if dl, ok := ctx.Deadline(); ok {
			raw.SetDeadline(dl)
		}
		cc, chans, reqs, err := ssh.NewClientConn(raw, addr, conf)
		if err != nil {
			raw.Close()
			c.closeAll()
			return nil, fmt.Errorf("ssh %s@%s (%s): %w", hop.User, hop.Host, addr, err)
		}
		raw.SetDeadline(time.Time{})
		cl := ssh.NewClient(cc, chans, reqs)
		logf("connected to %s@%s (%s)", hop.User, hop.Host, addr)
		if i < len(hops)-1 {
			c.jumps = append(c.jumps, cl)
		} else {
			c.ssh = cl
		}
		prev = cl
	}
	c.desc = r.User + "@" + r.Host
	if len(r.Via) > 0 {
		names := make([]string, len(r.Via))
		for i, v := range r.Via {
			names[i] = v.Host
		}
		c.desc += " (via " + strings.Join(names, ", ") + ")"
	}
	return c, nil
}

func (c *Client) closeAll() {
	if c.ssh != nil {
		c.ssh.Close()
	}
	for i := len(c.jumps) - 1; i >= 0; i-- {
		c.jumps[i].Close()
	}
	for _, f := range c.closes {
		f()
	}
}
```

- [ ] **Step 4: Implement process.go**

`internal/sshx/process.go`:
```go
package sshx

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"
)

// Process is a started remote command.
type Process struct {
	Stdin  io.WriteCloser
	Stdout io.Reader
	Stderr io.Reader
	Wait   func() error // *ssh.ExitError on non-zero exit
	Close  func() error
}

func (c *Client) start(ctx context.Context, cmd string, pty bool, rows, cols int) (*Process, error) {
	sess, err := c.ssh.NewSession()
	if err != nil {
		return nil, fmt.Errorf("%s: new session for %q: %w", c.desc, cmd, err)
	}
	if pty {
		if err := sess.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{ssh.ECHO: 0}); err != nil {
			sess.Close()
			return nil, fmt.Errorf("%s: request pty: %w", c.desc, err)
		}
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	if err := sess.Start(cmd); err != nil {
		sess.Close()
		return nil, fmt.Errorf("%s: start %q: %w", c.desc, cmd, err)
	}
	stop := context.AfterFunc(ctx, func() { sess.Close() })
	return &Process{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Wait: func() error {
			err := sess.Wait()
			stop()
			return err
		},
		Close: func() error { stop(); return sess.Close() },
	}, nil
}

// Start runs cmd on the remote sh without a pty.
func (c *Client) Start(ctx context.Context, cmd string) (*Process, error) {
	return c.start(ctx, cmd, false, 0, 0)
}

// StartPty runs cmd with a pty of rows x cols; stdout carries the pty output.
func (c *Client) StartPty(ctx context.Context, cmd string, rows, cols int) (*Process, error) {
	return c.start(ctx, cmd, true, rows, cols)
}

// Run is Start + drain; returns stdout, stderr, error (ExitError wrapped).
func (c *Client) Run(ctx context.Context, cmd string, stdin io.Reader) ([]byte, []byte, error) {
	p, err := c.Start(ctx, cmd)
	if err != nil {
		return nil, nil, err
	}
	defer p.Close()
	var out, errOut bytes.Buffer
	done := make(chan struct{}, 2)
	go func() { io.Copy(&out, p.Stdout); done <- struct{}{} }()
	go func() { io.Copy(&errOut, p.Stderr); done <- struct{}{} }()
	if stdin != nil {
		io.Copy(p.Stdin, stdin)
	}
	p.Stdin.Close()
	<-done
	<-done
	if err := p.Wait(); err != nil {
		return out.Bytes(), errOut.Bytes(), fmt.Errorf("%s: %q: %w", c.desc, cmd, err)
	}
	return out.Bytes(), errOut.Bytes(), nil
}
```

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/sshx/... -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/sshx/dial.go internal/sshx/process.go internal/sshx/dial_test.go
git commit -m "feat(sshx): Dial with known_hosts and key auth, Start/StartPty/Run"
```

---

### Task 6: Jump chains, `Redial`, and the no-local-DNS test

**Files:**
- Modify: `internal/sshx/dial.go` (add `Redial`)
- Test: `internal/sshx/jump_test.go`

**Interfaces:**
- Consumes: Task 5.
- Produces: `func Redial(ctx context.Context, attempts int, backoff time.Duration, logf func(string, ...any), dial func(ctx context.Context) (*Client, error)) (*Client, error)` — tries `dial` up to `attempts` times, sleeping `backoff, 2·backoff, 4·backoff …` (capped at 30s) between failures; honours ctx cancellation; the returned error lists every attempt's error.

- [ ] **Step 1: Write the failing test**

`internal/sshx/jump_test.go`:
```go
package sshx

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mithro/go-claude-teleport/internal/sshx/sshtest"
)

func TestDialThroughJumpResolvesOnJump(t *testing.T) {
	home, pub := testHome(t)
	dest := sshtest.New(t, sshtest.Options{Authorized: []ssh.PublicKey{pub}, Exec: echoExec})
	jump := sshtest.New(t, sshtest.Options{
		Authorized: []ssh.PublicKey{pub},
		Resolver:   map[string]string{"dest.private": dest.Addr},
	})
	jumpHost, jumpPort := hostPort(t, jump.Addr)

	kh := filepath.Join(home, ".ssh", "known_hosts")
	os.WriteFile(kh, []byte(
		sshtest.KnownHostsLine(knownHostsName(jumpHost, jumpPort), jump.HostKey)+
			sshtest.KnownHostsLine("[dest.private]:2222", dest.HostKey)), 0o600)

	var mu sync.Mutex
	var localDials []string
	recorder := func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		localDials = append(localDials, addr)
		mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}

	r := Resolved{
		Target:   Target{User: "alice", Host: "big-storage", Port: 2222, Via: []Target{{User: "alice", Host: jumpHost, Port: jumpPort}}},
		HostName: "dest.private",
	}
	c, err := Dial(context.Background(), r, nil, nil, Options{KnownHostsFile: kh, Home: home, Logf: t.Logf, NetDial: recorder})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if c.String() != "alice@big-storage (via "+jumpHost+")" {
		t.Errorf("String = %q", c.String())
	}
	out, _, err := c.Run(context.Background(), "hostname", nil)
	if err != nil || string(out) != "cmd=hostname stdin=" {
		t.Fatalf("Run via jump: out=%q err=%v", out, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(localDials) != 1 || localDials[0] != jump.Addr {
		t.Errorf("local dials = %v, want only the jump %s", localDials, jump.Addr)
	}
	for _, d := range localDials {
		if strings.Contains(d, "dest.private") {
			t.Errorf("dest.private was dialled locally: %v", localDials)
		}
	}
	if fw := jump.Forwarded(); len(fw) != 1 || fw[0] != "dest.private:2222" {
		t.Errorf("jump forwarded = %v, want [dest.private:2222]", fw)
	}
}

func TestRedialBoundedRetries(t *testing.T) {
	calls := 0
	_, err := Redial(context.Background(), 3, time.Millisecond, t.Logf, func(ctx context.Context) (*Client, error) {
		calls++
		return nil, errors.New("refused")
	})
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
	if err == nil || strings.Count(err.Error(), "refused") != 3 {
		t.Errorf("err = %v, want all three attempts listed", err)
	}

	calls = 0
	c, err := Redial(context.Background(), 3, time.Millisecond, t.Logf, func(ctx context.Context) (*Client, error) {
		calls++
		if calls < 2 {
			return nil, errors.New("refused")
		}
		return &Client{desc: "ok"}, nil
	})
	if err != nil || c == nil || calls != 2 {
		t.Errorf("second attempt should succeed: c=%v err=%v calls=%d", c, err, calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Redial(ctx, 3, time.Second, t.Logf, func(ctx context.Context) (*Client, error) { return nil, errors.New("x") })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled ctx: err = %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/sshx/ -run 'TestDialThroughJump|TestRedial' -v`
Expected: `TestRedial` FAIL with `undefined: Redial`; `TestDialThroughJump` passes already if Task 5's loop is correct (that is fine — it pins behaviour).

- [ ] **Step 3: Add Redial to dial.go**

Append to `internal/sshx/dial.go`:
```go
// Redial calls dial up to attempts times with exponential backoff
// (backoff, 2*backoff, ... capped at 30s). Every attempt's error is kept.
func Redial(ctx context.Context, attempts int, backoff time.Duration, logf func(string, ...any), dial func(ctx context.Context) (*Client, error)) (*Client, error) {
	if attempts < 1 {
		attempts = 1
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	var errs []string
	wait := backoff
	for i := 1; i <= attempts; i++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("redial aborted after %d attempt(s): %w (%s)", i-1, err, strings.Join(errs, "; "))
		}
		c, err := dial(ctx)
		if err == nil {
			return c, nil
		}
		errs = append(errs, fmt.Sprintf("attempt %d: %v", i, err))
		if i == attempts {
			break
		}
		logf("ssh dial failed (attempt %d/%d): %v; retrying in %s", i, attempts, err, wait)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("redial aborted: %w (%s)", ctx.Err(), strings.Join(errs, "; "))
		case <-time.After(wait):
		}
		wait *= 2
		if wait > 30*time.Second {
			wait = 30 * time.Second
		}
	}
	return nil, fmt.Errorf("ssh dial failed after %d attempts: %s", attempts, strings.Join(errs, "; "))
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/sshx/... -v`
Expected: PASS, including the assertion that `dest.private` never appears in local dials.

- [ ] **Step 5: Commit**

```bash
git add internal/sshx/dial.go internal/sshx/jump_test.go
git commit -m "feat(sshx): jump chains resolved by the previous hop, Redial with backoff"
```

---

### Task 7: `job.Journal` — job directory, atomic save, steps

**Files:**
- Create: `internal/job/journal.go`
- Test: `internal/job/journal_test.go`

**Interfaces:**
- Consumes: nothing outside stdlib.
- Produces (verbatim from the interfaces doc): `StepStatus` + constants, `StepState`, `Journal`, `Dir`, `StagingDir`, `Open`, `New`, `(*Journal).Save/LogPath/ManifestPath/CapturePath/Step/FirstIncomplete/RunnerAlive`.

- [ ] **Step 1: Write the failing test**

`internal/job/journal_test.go`:
```go
package job

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const sid = "6f1c2a4e-9b3d-4c7a-8e5f-1a2b3c4d5e6f"

func TestNewOpenSaveRoundTrip(t *testing.T) {
	data := t.TempDir()
	if _, ok, err := Open(data, sid); err != nil || ok {
		t.Fatalf("Open before New: ok=%v err=%v", ok, err)
	}
	j, err := New(data, sid)
	if err != nil {
		t.Fatal(err)
	}
	if j.Dir != filepath.Join(data, "jobs", sid) || j.ID != sid || j.SessionID != sid {
		t.Errorf("New: %+v", j)
	}
	st, err := os.Stat(j.Dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("job dir mode = %v err=%v, want 0700", st.Mode(), err)
	}
	if j.LogPath() != filepath.Join(j.Dir, "log.txt") || j.ManifestPath() != filepath.Join(j.Dir, "manifest.json") || j.CapturePath() != filepath.Join(j.Dir, "capture.txt") {
		t.Errorf("paths: %s %s %s", j.LogPath(), j.ManifestPath(), j.CapturePath())
	}
	if StagingDir(data, sid) != filepath.Join(data, "staging", sid) {
		t.Errorf("StagingDir = %s", StagingDir(data, sid))
	}

	j.Direction = "to"
	j.SourceHost = "laptop.example"
	j.DestHost = "big-storage.example"
	j.Step("preflight").Status = Done
	j.Step("freeze").Status = Running
	if err := j.Save(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(j.Dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}

	got, ok, err := Open(data, sid)
	if err != nil || !ok {
		t.Fatalf("Open: ok=%v err=%v", ok, err)
	}
	if diff := cmp.Diff(j, got); diff != "" {
		t.Errorf("round trip (-want +got):\n%s", diff)
	}
}

func TestStepFindOrAppendAndFirstIncomplete(t *testing.T) {
	j := &Journal{}
	if name, ok := j.FirstIncomplete(); ok || name != "" {
		t.Errorf("empty journal FirstIncomplete = %q,%v", name, ok)
	}
	a := j.Step("a")
	a.Status = Done
	j.Step("b").Status = Failed
	j.Step("c")
	if j.Step("a") != a || len(j.Steps) != 3 {
		t.Errorf("Step must find, not duplicate: %+v", j.Steps)
	}
	name, ok := j.FirstIncomplete()
	if !ok || name != "b" {
		t.Errorf("FirstIncomplete = %q,%v want b", name, ok)
	}
	j.Step("b").Status = Done
	j.Step("c").Status = Done
	if _, ok := j.FirstIncomplete(); ok {
		t.Errorf("all done must report none")
	}
}

func TestRunnerAlive(t *testing.T) {
	j := &Journal{RunnerPID: 4242}
	if !j.RunnerAlive(func(pid int) bool { return pid == 4242 }) {
		t.Errorf("alive runner not detected")
	}
	if j.RunnerAlive(func(pid int) bool { return false }) {
		t.Errorf("dead runner reported alive")
	}
	if (&Journal{}).RunnerAlive(func(int) bool { return true }) {
		t.Errorf("pid 0 must never be alive")
	}
}

func TestOpenMalformedIsError(t *testing.T) {
	data := t.TempDir()
	dir := Dir(data, sid)
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "job.json"), []byte("{not json"), 0o600)
	_, _, err := Open(data, sid)
	if err == nil {
		t.Fatal("malformed job.json must be an error")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/job/ -v`
Expected: FAIL — undefined `Open`, `New`, `Journal`, ….

- [ ] **Step 3: Implement**

`internal/job/journal.go`:
```go
// Package job is the teleport job journal (jobs/<sid>/job.json), its log
// and the resumable step runner (spec §6).
package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type StepStatus string

const (
	Pending StepStatus = "pending"
	Running StepStatus = "running"
	Done    StepStatus = "done"
	Failed  StepStatus = "failed"
)

type StepState struct {
	Name       string     `json:"name"`
	Status     StepStatus `json:"status"`
	StartedAt  time.Time  `json:"started_at,omitempty"`
	FinishedAt time.Time  `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
	Attempts   int        `json:"attempts"`
}

type Journal struct {
	ID         string          `json:"id"` // == session id
	SessionID  string          `json:"session_id"`
	Direction  string          `json:"direction"` // "to" | "from"
	SourceHost string          `json:"source_host"`
	DestHost   string          `json:"dest_host"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Plan       json.RawMessage `json:"plan"` // orchestrate.Plan (opaque here)
	Steps      []StepState     `json:"steps"`
	Finished   bool            `json:"finished"`
	Outcome    string          `json:"outcome"` // "" | "success" | "failed" | "abandoned"
	RunnerPID  int             `json:"runner_pid"`
	Dir        string          `json:"-"`
}

// Dir is <dataDir>/jobs/<id>.
func Dir(dataDir, id string) string { return filepath.Join(dataDir, "jobs", id) }

// StagingDir is <dataDir>/staging/<id>.
func StagingDir(dataDir, id string) string { return filepath.Join(dataDir, "staging", id) }

func journalPath(dir string) string { return filepath.Join(dir, "job.json") }

// Open loads jobs/<id>/job.json if it exists.
func Open(dataDir, id string) (*Journal, bool, error) {
	dir := Dir(dataDir, id)
	raw, err := os.ReadFile(journalPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open journal %s: %w", journalPath(dir), err)
	}
	var j Journal
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, false, fmt.Errorf("parse journal %s: %w", journalPath(dir), err)
	}
	j.Dir = dir
	return &j, true, nil
}

// New creates the job directory (0700) and an empty journal (not yet saved).
func New(dataDir, id string) (*Journal, error) {
	dir := Dir(dataDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create job dir %s: %w", dir, err)
	}
	now := time.Now().UTC()
	return &Journal{ID: id, SessionID: id, CreatedAt: now, UpdatedAt: now, Dir: dir}, nil
}

// Save writes job.json atomically (temp file + rename) and bumps UpdatedAt.
func (j *Journal) Save() error {
	if j.Dir == "" {
		return errors.New("journal has no Dir")
	}
	j.UpdatedAt = time.Now().UTC()
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return fmt.Errorf("encode journal: %w", err)
	}
	tmp, err := os.CreateTemp(j.Dir, "job.json.*.tmp")
	if err != nil {
		return fmt.Errorf("save journal %s: %w", j.Dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("save journal %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("save journal %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, journalPath(j.Dir)); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("save journal %s: %w", journalPath(j.Dir), err)
	}
	return nil
}

func (j *Journal) LogPath() string      { return filepath.Join(j.Dir, "log.txt") }
func (j *Journal) ManifestPath() string { return filepath.Join(j.Dir, "manifest.json") }
func (j *Journal) CapturePath() string  { return filepath.Join(j.Dir, "capture.txt") }

// Step finds the named step or appends a Pending one.
func (j *Journal) Step(name string) *StepState {
	for i := range j.Steps {
		if j.Steps[i].Name == name {
			return &j.Steps[i]
		}
	}
	j.Steps = append(j.Steps, StepState{Name: name, Status: Pending})
	return &j.Steps[len(j.Steps)-1]
}

// FirstIncomplete names the first step that is not Done.
func (j *Journal) FirstIncomplete() (string, bool) {
	for _, s := range j.Steps {
		if s.Status != Done {
			return s.Name, true
		}
	}
	return "", false
}

// RunnerAlive reports whether the recorded runner pid is alive per alive().
func (j *Journal) RunnerAlive(alive func(pid int) bool) bool {
	return j.RunnerPID > 0 && alive(j.RunnerPID)
}
```
`cmp.Diff` on `Journal` compares `time.Time` via `Equal`-less struct comparison; JSON round trip keeps UTC and nanoseconds, so the round-trip test passes as long as `New` uses `.UTC()` (it does).

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/job/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/job/journal.go internal/job/journal_test.go
git commit -m "feat(job): journal with atomic save, steps and runner liveness"
```

---

### Task 8: `job.Run` — Verify-then-Run step runner, continue after crash

**Files:**
- Create: `internal/job/run.go`
- Create: `internal/job/history.go`
- Test: `internal/job/run_test.go`

**Interfaces:**
- Consumes: Task 7.
- Produces: `type Step struct{Name string; Verify func(ctx) (bool, error); Run func(ctx) error}`; `func Run(ctx, j *Journal, steps []Step, logf) error`; `type HistoryRecord`; `func AppendHistory(dir string, r HistoryRecord) error`.

Runner semantics (spec §6): for each step in order — a `Done` step whose `Verify` returns `(true, nil)` is skipped; if `Verify` says not done (or the step was never done) the step becomes `Running`, `Attempts++`, journal saved, `Run` executes, then `Done`/`Failed` + `FinishedAt` saved. On error the journal's `Outcome` becomes `"failed"` and `Run` returns the error wrapped with the step name; subsequent steps are untouched. When all steps are `Done`, `Finished=true`, `Outcome="success"`. Steps in the journal that are not in `steps` are left as they are. `Verify==nil` means "trust the journal" (Done → skip).

- [ ] **Step 1: Write the failing test**

`internal/job/run_test.go`:
```go
package job

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunContinuesAfterCrash(t *testing.T) {
	data := t.TempDir()
	j, err := New(data, sid)
	if err != nil {
		t.Fatal(err)
	}
	var ran []string
	crash := errors.New("simulated crash")
	steps := func(crashAt string) []Step {
		mk := func(name string) Step {
			return Step{
				Name:   name,
				Verify: func(ctx context.Context) (bool, error) { ran = append(ran, "verify:"+name); return false, nil },
				Run: func(ctx context.Context) error {
					ran = append(ran, "run:"+name)
					if name == crashAt {
						return crash
					}
					return nil
				},
			}
		}
		return []Step{mk("one"), mk("two"), mk("three")}
	}

	// first run: steps 1–2 succeed, step 3 "crashes"
	err = Run(context.Background(), j, steps("three"), t.Logf)
	if !errors.Is(err, crash) || !strings.Contains(err.Error(), "three") {
		t.Fatalf("first run err = %v", err)
	}
	if j.Outcome != "failed" || j.Step("three").Status != Failed || j.Step("three").Error != "simulated crash" {
		t.Errorf("journal after crash: %+v", j.Steps)
	}

	// reopen from disk as `continue` would
	j2, ok, err := Open(data, sid)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if name, _ := j2.FirstIncomplete(); name != "three" {
		t.Errorf("FirstIncomplete = %q", name)
	}
	ran = nil
	verifiedDone := map[string]bool{"one": true, "two": true}
	st := steps("")
	for i := range st {
		name := st[i].Name
		st[i].Verify = func(ctx context.Context) (bool, error) {
			ran = append(ran, "verify:"+name)
			return verifiedDone[name], nil
		}
	}
	if err := Run(context.Background(), j2, st, t.Logf); err != nil {
		t.Fatal(err)
	}
	want := "verify:one verify:two verify:three run:three"
	if got := strings.Join(ran, " "); got != want {
		t.Errorf("continue ran %q, want %q (1–2 consulted via Verify, only 3 run)", got, want)
	}
	if !j2.Finished || j2.Outcome != "success" || j2.Step("three").Attempts != 2 {
		t.Errorf("journal after continue: finished=%v outcome=%q attempts=%d", j2.Finished, j2.Outcome, j2.Step("three").Attempts)
	}
	if j2.Step("three").FinishedAt.Before(j2.Step("three").StartedAt) {
		t.Errorf("timestamps: %+v", *j2.Step("three"))
	}
}

func TestRunVerifyErrorAndFailureStops(t *testing.T) {
	data := t.TempDir()
	j, _ := New(data, sid)
	verr := errors.New("precondition gone")
	third := false
	err := Run(context.Background(), j, []Step{
		{Name: "a", Run: func(ctx context.Context) error { return nil }},
		{Name: "b", Verify: func(ctx context.Context) (bool, error) { return false, verr }, Run: func(ctx context.Context) error { return nil }},
		{Name: "c", Run: func(ctx context.Context) error { third = true; return nil }},
	}, t.Logf)
	if !errors.Is(err, verr) {
		t.Fatalf("err = %v", err)
	}
	if third {
		t.Errorf("step c ran after b failed")
	}
	if j.Step("a").Status != Done || j.Step("b").Status != Failed || j.Step("c").Status != Pending {
		t.Errorf("statuses: %+v", j.Steps)
	}
	// state persisted before Run: a Running step is on disk while a step runs
	j3, _ := New(data, "b0b1c2d3-1111-4222-8333-444455556666")
	saw := ""
	Run(context.Background(), j3, []Step{{Name: "x", Run: func(ctx context.Context) error {
		disk, _, _ := Open(data, "b0b1c2d3-1111-4222-8333-444455556666")
		saw = string(disk.Step("x").Status)
		return nil
	}}}, t.Logf)
	if saw != "running" {
		t.Errorf("status on disk during Run = %q, want running", saw)
	}
}

func TestRunNilVerifyTrustsJournal(t *testing.T) {
	data := t.TempDir()
	j, _ := New(data, sid)
	j.Step("a").Status = Done
	ran := false
	if err := Run(context.Background(), j, []Step{{Name: "a", Run: func(ctx context.Context) error { ran = true; return nil }}}, t.Logf); err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Errorf("Done step with nil Verify must be skipped")
	}
}

func TestAppendHistory(t *testing.T) {
	dir := t.TempDir()
	rec := HistoryRecord{At: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC), SessionID: sid, Direction: "to", From: "laptop.example", To: "big-storage.example", Outcome: "success"}
	if err := AppendHistory(dir, rec); err != nil {
		t.Fatal(err)
	}
	rec.Outcome = "failed"
	if err := AppendHistory(dir, rec); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "history.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"outcome":"success"`) || !strings.Contains(lines[1], `"to":"big-storage.example"`) {
		t.Errorf("history = %q", raw)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/job/ -run 'TestRun|TestAppendHistory' -v`
Expected: FAIL — undefined `Run`, `Step`, `HistoryRecord`, `AppendHistory`.

- [ ] **Step 3: Implement run.go**

`internal/job/run.go`:
```go
package job

import (
	"context"
	"fmt"
	"time"
)

// Step is one unit of the state machine: Verify re-checks reality (done=true
// skips Run); Run does the work.
type Step struct {
	Name   string
	Verify func(ctx context.Context) (done bool, err error)
	Run    func(ctx context.Context) error
}

// Run executes steps in order, persisting state before/after each; returns
// the first error (journal marked Failed for that step).
func Run(ctx context.Context, j *Journal, steps []Step, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	for _, s := range steps {
		st := j.Step(s.Name)
		if s.Verify != nil {
			done, err := s.Verify(ctx)
			if err != nil {
				return j.fail(st, s.Name, fmt.Errorf("verify: %w", err))
			}
			if done {
				if st.Status != Done {
					logf("step %s: already satisfied", s.Name)
					st.Status = Done
					st.FinishedAt = time.Now().UTC()
					if err := j.Save(); err != nil {
						return err
					}
				} else {
					logf("step %s: verified done", s.Name)
				}
				continue
			}
		} else if st.Status == Done {
			logf("step %s: done (journal)", s.Name)
			continue
		}
		if err := ctx.Err(); err != nil {
			return j.fail(st, s.Name, err)
		}
		st.Status = Running
		st.Attempts++
		st.StartedAt = time.Now().UTC()
		st.Error = ""
		if err := j.Save(); err != nil {
			return err
		}
		logf("step %s: starting (attempt %d)", s.Name, st.Attempts)
		if err := s.Run(ctx); err != nil {
			return j.fail(st, s.Name, err)
		}
		st.Status = Done
		st.FinishedAt = time.Now().UTC()
		if err := j.Save(); err != nil {
			return err
		}
		logf("step %s: done", s.Name)
	}
	j.Finished = true
	j.Outcome = "success"
	return j.Save()
}

// fail records the step's own error text in the journal (the test asserts
// Error == "simulated crash") and returns it wrapped with the step name.
func (j *Journal) fail(st *StepState, name string, inner error) error {
	st.Status = Failed
	st.Error = inner.Error()
	st.FinishedAt = time.Now().UTC()
	j.Outcome = "failed"
	err := fmt.Errorf("step %s: %w", name, inner)
	if serr := j.Save(); serr != nil {
		return fmt.Errorf("%w (and saving the journal failed: %v)", err, serr)
	}
	return err
}
```

- [ ] **Step 4: Implement history.go**

`internal/job/history.go`:
```go
package job

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// HistoryRecord is one line of jobs/<id>/history.jsonl (spec §6 step 10).
type HistoryRecord struct {
	At        time.Time `json:"at"`
	SessionID string    `json:"session_id"`
	Direction string    `json:"direction"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Outcome   string    `json:"outcome"`
	Note      string    `json:"note,omitempty"`
}

// AppendHistory appends r to dir/history.jsonl, creating dir (0700) if needed.
func AppendHistory(dir string, r HistoryRecord) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("history dir %s: %w", dir, err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode history record: %w", err)
	}
	path := filepath.Join(dir, "history.jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("append history %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("append history %s: %w", path, err)
	}
	return nil
}
```
(The interfaces doc writes the `From, To` tags on one line — `json:"from","to"` is not a legal struct tag; the two separate fields above are the intended shape.)

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/job/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/job/run.go internal/job/history.go internal/job/run_test.go
git commit -m "feat(job): verify-then-run step runner with persisted attempts, history records"
```

---

### Task 9: `job.TailLog` and `job.FollowLog`

**Files:**
- Create: `internal/job/log.go`
- Test: `internal/job/log_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func TailLog(path string, n int) ([]string, error)` (last n lines; missing file → `nil, nil`); `func FollowLog(ctx, path string, w io.Writer, done func() bool) error` — poll-based tail (250ms): copies bytes appended to `path` to `w`, starting from the current end when the file does not exist yet, and returns when `done()` is true **and** everything on disk has been written, or when ctx ends (returning `ctx.Err()`).

- [ ] **Step 1: Write the failing test**

`internal/job/log_test.go`:
```go
package job

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestTailLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log.txt")
	if lines, err := TailLog(p, 3); err != nil || lines != nil {
		t.Errorf("missing file: %v %v", lines, err)
	}
	os.WriteFile(p, []byte("a\nb\nc\nd\n"), 0o600)
	lines, err := TailLog(p, 3)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"b", "c", "d"}, lines); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	lines, _ = TailLog(p, 10)
	if len(lines) != 4 {
		t.Errorf("n > lines: %v", lines)
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestFollowLogUntilDone(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log.txt")
	var out syncBuf
	var finished sync.Mutex
	isDone := false
	done := func() bool { finished.Lock(); defer finished.Unlock(); return isDone }

	errc := make(chan error, 1)
	go func() { errc <- FollowLog(context.Background(), p, &out, done) }()

	time.Sleep(300 * time.Millisecond) // file does not exist yet
	os.WriteFile(p, []byte("step one\n"), 0o600)
	time.Sleep(400 * time.Millisecond)
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("step two\n")
	f.Close()
	finished.Lock()
	isDone = true
	finished.Unlock()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("FollowLog did not return after done()")
	}
	if out.String() != "step one\nstep two\n" {
		t.Errorf("followed = %q", out.String())
	}
}

func TestFollowLogContextCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := FollowLog(ctx, filepath.Join(t.TempDir(), "nope"), &bytes.Buffer{}, func() bool { return false })
	if err == nil {
		t.Fatal("expected ctx error")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/job/ -run 'TestTailLog|TestFollowLog' -v`
Expected: FAIL — undefined `TailLog`, `FollowLog`.

- [ ] **Step 3: Implement**

`internal/job/log.go`:
```go
package job

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// TailLog returns the last n lines of path ("" lines trimmed at the end).
// A missing file yields nil, nil.
func TailLog(path string, n int) ([]string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("tail %s: %w", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	if n < len(lines) {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

const followPoll = 250 * time.Millisecond

// FollowLog copies bytes appended to path to w until done() reports true
// and the file has been drained, or ctx ends.
func FollowLog(ctx context.Context, path string, w io.Writer, done func() bool) error {
	var offset int64
	drain := func() error {
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("follow %s: %w", path, err)
		}
		defer f.Close()
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return fmt.Errorf("follow %s: %w", path, err)
		}
		n, err := io.Copy(w, f)
		offset += n
		if err != nil {
			return fmt.Errorf("follow %s: %w", path, err)
		}
		return nil
	}
	ticker := time.NewTicker(followPoll)
	defer ticker.Stop()
	for {
		if err := drain(); err != nil {
			return err
		}
		if done() {
			return drain()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/job/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/job/log.go internal/job/log_test.go
git commit -m "feat(job): TailLog and poll-based FollowLog"
```

---

### Task 10: `transfer.Manifest` — Build (hash of rewritten content), Save, Load, forbidden paths

**Files:**
- Create: `internal/transfer/manifest.go`
- Test: `internal/transfer/manifest_test.go`

**Interfaces:**
- Consumes (Plan 01): `session.FileEntry` (`Root, Rel, Category, Size, Mode, ModTime, Symlink, Rewrite`, `Path()`), `session.PathMap` (`ApplyPath`, `Empty`), `session.RewriteJSONL`, `session.RewriteJSON`, `session.Forbidden`, `session.CatSession`, `session.ID`, `session.Skipped`.
- Produces (verbatim): `Entry`, `Manifest` (+ addition `TmpDir string \`json:"-"\``), `Build`, `Load`, `(*Manifest).Save`, `(*Manifest).ByID`, plus `var ErrForbidden`.

Build rules: entries are numbered in input order starting at 0; `Dst = filepath.Join(pm.ApplyPath(e.Root), filepath.FromSlash(e.Rel))`; regular files are hashed streaming — `Rewrite` entries through `session.RewriteJSONL` (`.jsonl`) or `session.RewriteJSON` (anything else) into the hasher, and `Size` is the rewritten byte count; dirs and symlinks get `SHA256=""`; a `CatSession` entry whose `Rel` is `session.Forbidden` aborts `Build` with `ErrForbidden` listing every offender (checked before any hashing). `FFAllowed` is set for `CatSession` entries whose `Rel` starts with `projects/<munged>/<sid>` — i.e. `<sid>.jsonl` or the `<sid>/` sidecar directory — recognised by `strings.Contains(e.Rel, "/"+string(id))`.

- [ ] **Step 1: Write the failing test**

`internal/transfer/manifest_test.go`:
```go
package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/session"
)

const sid = "7a3f9c1e-2b4d-4e6f-8a1b-3c5d7e9f0a2b"

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// sourceTree builds a minimal source config dir under /home/alice-like root.
func sourceTree(t *testing.T) (cfg string, files []session.FileEntry) {
	t.Helper()
	cfg = filepath.Join(t.TempDir(), "home", "alice", ".claude")
	proj := "projects/-home-alice-work"
	writeFile(t, filepath.Join(cfg, proj, sid+".jsonl"),
		`{"type":"user","cwd":"/home/alice/work","sessionId":"`+sid+`"}`+"\n"+
			`{"type":"assistant","cwd":"/home/alice/work","n":1.50}`+"\n")
	writeFile(t, filepath.Join(cfg, proj, sid, "subagents", "agent-1.jsonl"), `{"cwd":"/home/alice/work"}`+"\n")
	writeFile(t, filepath.Join(cfg, "todos", sid+".json"), `{"path":"/home/alice/work/x"}`)
	os.Symlink("../"+sid+".jsonl", filepath.Join(cfg, proj, "link.jsonl"))
	mt := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	mk := func(rel string, mode os.FileMode, size int64, rewrite bool, link string) session.FileEntry {
		return session.FileEntry{Root: cfg, Rel: rel, Category: session.CatSession, Size: size, Mode: mode, ModTime: mt, Rewrite: rewrite, Symlink: link}
	}
	files = []session.FileEntry{
		mk(proj, os.ModeDir|0o700, 0, false, ""),
		mk(proj+"/"+sid+".jsonl", 0o600, 0, true, ""),
		mk(proj+"/"+sid+"/subagents/agent-1.jsonl", 0o600, 0, true, ""),
		mk("todos/"+sid+".json", 0o600, 0, true, ""),
		mk(proj+"/link.jsonl", os.ModeSymlink|0o777, 0, false, "../"+sid+".jsonl"),
	}
	return cfg, files
}

func TestBuildHashesRewrittenContent(t *testing.T) {
	cfg, files := sourceTree(t)
	pm := session.NewPathMap(session.Mapping{From: "/home/alice", To: "/home/bob"})
	m, err := Build(context.Background(), sid, session.ID(sid), "laptop.example", "big-storage.example", files, pm)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 1 || m.JobID != sid || m.SessionID != sid || m.SourceHost != "laptop.example" || m.DestHost != "big-storage.example" {
		t.Errorf("header: %+v", m)
	}
	if len(m.Entries) != 5 {
		t.Fatalf("entries = %d", len(m.Entries))
	}
	tr := m.Entries[1]
	wantContent := `{"type":"user","cwd":"/home/bob/work","sessionId":"` + sid + `"}` + "\n" +
		`{"type":"assistant","cwd":"/home/bob/work","n":1.50}` + "\n"
	if tr.SHA256 != sha(wantContent) || tr.Size != int64(len(wantContent)) {
		t.Errorf("transcript hash/size are not of the rewritten content: %+v", tr)
	}
	if !tr.FFAllowed || !m.Entries[2].FFAllowed || m.Entries[3].FFAllowed {
		t.Errorf("FFAllowed: transcript=%v sidecar=%v todo=%v", tr.FFAllowed, m.Entries[2].FFAllowed, m.Entries[3].FFAllowed)
	}
	wantDst := strings.Replace(cfg, "/home/alice/", "/home/bob/", 1)
	if tr.Dst != filepath.Join(wantDst, "projects/-home-alice-work", sid+".jsonl") || tr.Src != files[1].Path() {
		t.Errorf("paths: src=%s dst=%s", tr.Src, tr.Dst)
	}
	if m.Entries[0].SHA256 != "" || m.Entries[4].SHA256 != "" || m.Entries[4].Symlink != "../"+sid+".jsonl" {
		t.Errorf("dir/symlink entries must have no hash: %+v %+v", m.Entries[0], m.Entries[4])
	}
	for i, e := range m.Entries {
		if e.ID != i {
			t.Errorf("entry %d has id %d", i, e.ID)
		}
	}
	if _, ok := m.ByID(4); !ok {
		t.Errorf("ByID(4) missing")
	}
	if _, ok := m.ByID(99); ok {
		t.Errorf("ByID(99) should be absent")
	}
}

func TestBuildRawHashWithoutRewrite(t *testing.T) {
	cfg, files := sourceTree(t)
	files[1].Rewrite = false
	m, err := Build(context.Background(), sid, session.ID(sid), "a", "b", files[1:2], session.NewPathMap())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(cfg, "projects/-home-alice-work", sid+".jsonl"))
	if m.Entries[0].SHA256 != sha(string(raw)) || m.Entries[0].Size != int64(len(raw)) {
		t.Errorf("raw hash mismatch")
	}
	if m.Entries[0].Dst != m.Entries[0].Src {
		t.Errorf("empty path map must keep the path")
	}
}

func TestBuildRefusesForbiddenPaths(t *testing.T) {
	cfg, _ := sourceTree(t)
	forbidden := []string{".credentials.json", "sessions/12345.json", "sessions/12345.abcd.key", "settings.json", "plugins/installed_plugins.json", ".claude.json"}
	var files []session.FileEntry
	for _, rel := range forbidden {
		writeFile(t, filepath.Join(cfg, rel), "{}")
		files = append(files, session.FileEntry{Root: cfg, Rel: rel, Category: session.CatSession, Mode: 0o600})
	}
	_, err := Build(context.Background(), sid, session.ID(sid), "a", "b", files, session.NewPathMap())
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	for _, rel := range forbidden {
		if !strings.Contains(err.Error(), rel) {
			t.Errorf("error must list %s: %v", rel, err)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	_, files := sourceTree(t)
	m, err := Build(context.Background(), sid, session.ID(sid), "a", "b", files, session.NewPathMap(session.Mapping{From: "/home/alice", To: "/home/bob"}))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "manifest.json")
	if err := m.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != len(m.Entries) || got.Entries[1].SHA256 != m.Entries[1].SHA256 || got.PathMap.ApplyPath("/home/alice/x") != "/home/bob/x" {
		t.Errorf("round trip lost data: %+v", got)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Errorf("Load missing must error")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/transfer/ -v`
Expected: FAIL — undefined `Build`, `Load`, `ErrForbidden`, ….

- [ ] **Step 3: Implement**

`internal/transfer/manifest.go`:
```go
// Package transfer builds, diffs, streams and installs the file manifest of
// a teleport (spec §7).
package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mithro/go-claude-teleport/internal/session"
)

type Entry struct {
	ID        int              `json:"id"`
	Category  session.Category `json:"category"`
	Src       string           `json:"src"` // absolute on source
	Dst       string           `json:"dst"` // absolute on destination
	Size      int64            `json:"size"`
	Mode      uint32           `json:"mode"`
	ModTime   time.Time        `json:"mtime"`
	SHA256    string           `json:"sha256"` // "" for dirs/symlinks
	Symlink   string           `json:"symlink,omitempty"`
	Rewrite   bool             `json:"rewrite"`
	FFAllowed bool             `json:"ff_allowed"` // transcript/sidecar of THIS session
}

func (e Entry) IsDir() bool     { return os.FileMode(e.Mode)&os.ModeDir != 0 }
func (e Entry) IsSymlink() bool { return os.FileMode(e.Mode)&os.ModeSymlink != 0 }
func (e Entry) IsRegular() bool { return !e.IsDir() && !e.IsSymlink() }

type Manifest struct {
	Version    int               `json:"version"` // 1
	JobID      string            `json:"job_id"`
	SessionID  string            `json:"session_id"`
	SourceHost string            `json:"source_host"`
	DestHost   string            `json:"dest_host"`
	PathMap    session.PathMap   `json:"path_map"`
	Entries    []Entry           `json:"entries"`
	Skipped    []session.Skipped `json:"skipped"`
	TmpDir     string            `json:"-"` // where Send writes rewritten temp files ("" = os.TempDir())
}

// ErrForbidden is returned by Build when an entry is a never-transferred path.
var ErrForbidden = errors.New("manifest contains a forbidden path")

// Build hashes every file (streaming) and computes Dst via the path map.
// Rewrite entries are hashed AFTER rewriting so the hash matches what is sent.
func Build(ctx context.Context, jobID string, id session.ID, srcHost, dstHost string, files []session.FileEntry, pm session.PathMap) (*Manifest, error) {
	var bad []string
	for _, f := range files {
		if f.Category == session.CatSession && session.Forbidden(f.Rel) {
			bad = append(bad, f.Rel)
		}
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrForbidden, strings.Join(bad, ", "))
	}
	m := &Manifest{Version: 1, JobID: jobID, SessionID: string(id), SourceHost: srcHost, DestHost: dstHost, PathMap: pm}
	marker := "/" + string(id)
	for i, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		e := Entry{
			ID:       i,
			Category: f.Category,
			Src:      f.Path(),
			Dst:      filepath.Join(pm.ApplyPath(f.Root), filepath.FromSlash(f.Rel)),
			Size:     f.Size,
			Mode:     uint32(f.Mode),
			ModTime:  f.ModTime,
			Symlink:  f.Symlink,
			Rewrite:  f.Rewrite,
		}
		e.FFAllowed = f.Category == session.CatSession && strings.Contains("/"+f.Rel, marker)
		if e.IsRegular() {
			sum, n, err := hashEntry(e.Src, f.Rewrite, pm)
			if err != nil {
				return nil, fmt.Errorf("manifest: hash %s: %w", e.Src, err)
			}
			e.SHA256, e.Size = sum, n
			// Entries built without a stat (Plan 03's capture.txt entry carries
			// only Root/Rel/Category/Mode) take Mode/ModTime from the file here.
			if f.Mode == 0 || f.ModTime.IsZero() {
				st, err := os.Stat(e.Src)
				if err != nil {
					return nil, fmt.Errorf("manifest: stat %s: %w", e.Src, err)
				}
				e.Mode, e.ModTime = uint32(st.Mode()), st.ModTime()
			}
		}
		m.Entries = append(m.Entries, e)
	}
	return m, nil
}

// hashEntry streams the file (rewritten if asked) through sha256.
func hashEntry(path string, rewrite bool, pm session.PathMap) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	cw := &countWriter{w: h}
	if err := copyMaybeRewritten(f, cw, path, rewrite, pm); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), cw.n, nil
}

// copyMaybeRewritten is the ONE place that decides how bytes leave the
// source: raw copy, or session.RewriteJSONL / RewriteJSON when rewrite is set.
func copyMaybeRewritten(r io.Reader, w io.Writer, path string, rewrite bool, pm session.PathMap) error {
	if !rewrite || pm.Empty() {
		_, err := io.Copy(w, r)
		return err
	}
	var err error
	if strings.HasSuffix(path, ".jsonl") {
		_, err = session.RewriteJSONL(r, w, pm)
	} else {
		_, err = session.RewriteJSON(r, w, pm)
	}
	return err
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

var _ hash.Hash = sha256.New()

// Load reads a manifest file.
func Load(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load manifest %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	if m.Version != 1 {
		return nil, fmt.Errorf("manifest %s: unsupported version %d", path, m.Version)
	}
	return &m, nil
}

// Save writes the manifest atomically (temp + rename, 0600).
func (m *Manifest) Save(path string) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("save manifest %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("save manifest %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("save manifest %s: %w", path, err)
	}
	return nil
}

// ByID finds an entry by id (ids are dense and ordered, so index first).
func (m *Manifest) ByID(id int) (Entry, bool) {
	if id >= 0 && id < len(m.Entries) && m.Entries[id].ID == id {
		return m.Entries[id], true
	}
	for _, e := range m.Entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}
```
`session.PathMap` must survive JSON (`[]Mapping` with exported `From`/`To` fields — it does per the interfaces doc). If Plan 01's `NewPathMap` sorts on construction and `Load` bypasses it, longest-prefix order is still preserved because `Save` writes the sorted slice.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/transfer/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/transfer/manifest.go internal/transfer/manifest_test.go
git commit -m "feat(transfer): manifest build with rewritten-content hashes, save/load, forbidden-path refusal"
```

---

### Task 11: `transfer.Diff`, `Need`, `Blocking` — destination-side classification

**Files:**
- Create: `internal/transfer/diff.go`
- Test: `internal/transfer/diff_test.go`

**Interfaces:**
- Consumes: Task 10; `session.IsPrefix(existing, incoming string) (bool, error)`.
- Produces (verbatim): `Status` + constants, `Diff`, `Need`, `Blocking`; plus `func StagedPath(stagingDir string, id int) string` and `func HashFile(path string) (string, int64, error)`.

Status meaning (one per entry, evaluated on the destination):

| Status | Meaning | Sent? | Installable? |
|---|---|---|---|
| `absent` | Dst absent, nothing verified in staging | yes | no (not yet received) |
| `staged-same` | Dst absent, `staging/<id>` present with the manifest size | no | yes (rename in) |
| `present-same` | Dst exists and matches (hash / link target / is-dir) | no | yes (drop staged copy) |
| `ff-candidate` | Dst differs, `FFAllowed`, and Dst is a byte-prefix of the incoming file — checked against `staging/<id>` when it exists, else provisionally by `size(Dst) < entry.Size` (re-checked at install with the staged file) | yes (unless staged) | yes (rename over) |
| `staged-mismatch` | a `staging/<id>.part` remnant or a `staging/<id>` with the wrong size (both deleted by `Diff`) and Dst absent | yes | no |
| `present-different` | Dst exists and differs (and not an ff-candidate) | with `--force` only for `FFAllowed` | no |

`Need` returns ids with status `absent`, `ff-candidate` (when not already staged — Diff reports `staged-same` in that case only for absent Dst, so Need also checks the staged file directly), `staged-mismatch`, and `present-different` where `FFAllowed` (the orchestrator only calls Need after Blocking passed, so with `--force` those are legitimately needed). `Blocking(m, st, force)` returns entries with `present-different` unless `force && FFAllowed`.

- [ ] **Step 1: Write the failing test**

`internal/transfer/diff_test.go`:
```go
package transfer

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// destManifest fabricates a manifest whose Dst paths live under dest.
func destManifest(dest string) *Manifest {
	proj := filepath.Join(dest, ".claude", "projects", "-home-bob-work")
	tr := "line1\nline2\n"
	m := &Manifest{Version: 1, JobID: sid, SessionID: sid}
	m.Entries = []Entry{
		{ID: 0, Category: session.CatSession, Dst: proj, Mode: uint32(os.ModeDir | 0o700)},
		{ID: 1, Category: session.CatSession, Dst: filepath.Join(proj, sid+".jsonl"), Size: int64(len(tr)), Mode: 0o600, SHA256: sha(tr), FFAllowed: true},
		{ID: 2, Category: session.CatSession, Dst: filepath.Join(proj, "other.json"), Size: 2, Mode: 0o600, SHA256: sha("{}")},
		{ID: 3, Category: session.CatSession, Dst: filepath.Join(proj, "link"), Mode: uint32(os.ModeSymlink | 0o777), Symlink: "target"},
	}
	return m
}

func TestDiffStatuses(t *testing.T) {
	dest := t.TempDir()
	staging := filepath.Join(t.TempDir(), "staging")
	os.MkdirAll(staging, 0o700)
	m := destManifest(dest)

	st, err := Diff(context.Background(), m, staging)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]Status{0: Absent, 1: Absent, 2: Absent, 3: Absent}
	if diff := cmp.Diff(want, st); diff != "" {
		t.Errorf("empty dest (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]int{0, 1, 2, 3}, Need(m, st)); diff != "" {
		t.Errorf("Need (-want +got):\n%s", diff)
	}

	// present-same dir, present-same file, symlink same; a staged copy of 1; a .part remnant for 2
	os.MkdirAll(m.Entries[0].Dst, 0o700)
	writeFile(t, m.Entries[2].Dst, "{}")
	os.Symlink("target", m.Entries[3].Dst)
	writeFile(t, StagedPath(staging, 1), "line1\nline2\n")
	writeFile(t, StagedPath(staging, 2)+".part", "{")
	st, err = Diff(context.Background(), m, staging)
	if err != nil {
		t.Fatal(err)
	}
	want = map[int]Status{0: PresentSame, 1: StagedSame, 2: PresentSame, 3: PresentSame}
	if diff := cmp.Diff(want, st); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	if _, err := os.Stat(StagedPath(staging, 2) + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part remnant must be deleted by Diff")
	}
	if got := Need(m, st); len(got) != 0 {
		t.Errorf("Need = %v, want none", got)
	}

	// wrong-size staged copy -> staged-mismatch and removed
	os.Remove(m.Entries[2].Dst)
	writeFile(t, StagedPath(staging, 2), "{}}}")
	st, _ = Diff(context.Background(), m, staging)
	if st[2] != StagedMismatch {
		t.Errorf("wrong-size staged = %s", st[2])
	}
	if _, err := os.Stat(StagedPath(staging, 2)); !os.IsNotExist(err) {
		t.Errorf("mismatched staged file must be deleted")
	}
	if diff := cmp.Diff([]int{2}, Need(m, st)); diff != "" {
		t.Errorf("Need (-want +got):\n%s", diff)
	}
}

func TestDiffFastForwardAndCollision(t *testing.T) {
	dest := t.TempDir()
	staging := filepath.Join(t.TempDir(), "staging")
	m := destManifest(dest)

	// transcript on dest is a strict prefix of the incoming one -> ff-candidate (provisional, by size)
	writeFile(t, m.Entries[1].Dst, "line1\n")
	writeFile(t, m.Entries[2].Dst, "[]") // same size, different content -> present-different
	st, err := Diff(context.Background(), m, staging)
	if err != nil {
		t.Fatal(err)
	}
	if st[1] != FFCandidate || st[2] != PresentDifferent {
		t.Errorf("statuses: %v", st)
	}
	blk := Blocking(m, st, false)
	if len(blk) != 1 || blk[0].ID != 2 {
		t.Errorf("Blocking = %v, want entry 2 only (ff-candidate is allowed)", blk)
	}
	if diff := cmp.Diff([]int{1}, Need(m, st)); diff != "" {
		t.Errorf("Need (-want +got):\n%s", diff)
	}

	// once staged, the prefix check is exact: a non-prefix becomes present-different
	writeFile(t, StagedPath(staging, 1), "line1\nline2\n")
	writeFile(t, m.Entries[1].Dst, "lineX\n")
	st, _ = Diff(context.Background(), m, staging)
	if st[1] != PresentDifferent {
		t.Errorf("non-prefix same-session transcript = %s, want present-different", st[1])
	}
	if blk := Blocking(m, st, false); len(blk) != 2 {
		t.Errorf("Blocking without force = %v", blk)
	}
	// --force lifts the block for FFAllowed entries only
	blk = Blocking(m, st, true)
	if len(blk) != 1 || blk[0].ID != 2 {
		t.Errorf("Blocking with force = %v, want only the unrelated file", blk)
	}
	// entry 1 (FFAllowed, present-different) is needed; entry 2 (unrelated) never is
	if diff := cmp.Diff([]int{1}, Need(m, st)); diff != "" {
		t.Errorf("Need with forced present-different (-want +got):\n%s", diff)
	}

	// dir vs file collision
	writeFile(t, filepath.Join(dest, "x"), "")
	m.Entries[0].Dst = filepath.Join(dest, "x")
	st, _ = Diff(context.Background(), m, staging)
	if st[0] != PresentDifferent {
		t.Errorf("file where dir expected = %s", st[0])
	}
	_ = strconv.Itoa
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/transfer/ -run TestDiff -v`
Expected: FAIL — undefined `Diff`, `Need`, `Blocking`, `StagedPath`, `Status`.

- [ ] **Step 3: Implement**

`internal/transfer/diff.go`:
```go
package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/mithro/go-claude-teleport/internal/session"
)

type Status string

const (
	Absent           Status = "absent"
	PresentSame      Status = "present-same"
	StagedSame       Status = "staged-same"
	PresentDifferent Status = "present-different"
	FFCandidate      Status = "ff-candidate"
	StagedMismatch   Status = "staged-mismatch"
)

// StagedPath is stagingDir/<id>; the in-flight file is StagedPath+".part";
// dirs are recorded as StagedPath+".dir" and symlinks as StagedPath+".symlink".
func StagedPath(stagingDir string, id int) string {
	return filepath.Join(stagingDir, strconv.Itoa(id))
}

// HashFile streams path through sha256.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// stagedState reports whether a verified staged copy of e exists; it removes
// .part remnants and wrong-size staged files (returning mismatch=true).
func stagedState(stagingDir string, e Entry) (staged bool, mismatch bool, err error) {
	base := StagedPath(stagingDir, e.ID)
	if _, serr := os.Lstat(base + ".part"); serr == nil {
		if err := os.Remove(base + ".part"); err != nil {
			return false, false, fmt.Errorf("remove partial %s: %w", base+".part", err)
		}
		mismatch = true
	}
	switch {
	case e.IsDir():
		_, serr := os.Lstat(base + ".dir")
		return serr == nil, mismatch, nil
	case e.IsSymlink():
		raw, serr := os.ReadFile(base + ".symlink")
		return serr == nil && string(raw) == e.Symlink, mismatch, nil
	}
	st, serr := os.Lstat(base)
	if errors.Is(serr, os.ErrNotExist) {
		return false, mismatch, nil
	}
	if serr != nil {
		return false, false, fmt.Errorf("stat staged %s: %w", base, serr)
	}
	if st.Size() != e.Size || !st.Mode().IsRegular() {
		if err := os.Remove(base); err != nil {
			return false, false, fmt.Errorf("remove mismatched staged %s: %w", base, err)
		}
		return false, true, nil
	}
	return true, mismatch, nil
}

// Diff runs on the destination and classifies every entry.
func Diff(ctx context.Context, m *Manifest, stagingDir string) (map[int]Status, error) {
	out := make(map[int]Status, len(m.Entries))
	for _, e := range m.Entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		staged, mismatch, err := stagedState(stagingDir, e)
		if err != nil {
			return nil, err
		}
		st, err := os.Lstat(e.Dst)
		if errors.Is(err, os.ErrNotExist) {
			switch {
			case staged:
				out[e.ID] = StagedSame
			case mismatch:
				out[e.ID] = StagedMismatch
			default:
				out[e.ID] = Absent
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", e.Dst, err)
		}
		switch {
		case e.IsDir():
			if st.IsDir() {
				out[e.ID] = PresentSame
			} else {
				out[e.ID] = PresentDifferent
			}
		case e.IsSymlink():
			target, rerr := os.Readlink(e.Dst)
			if rerr == nil && target == e.Symlink {
				out[e.ID] = PresentSame
			} else {
				out[e.ID] = PresentDifferent
			}
		default:
			if !st.Mode().IsRegular() {
				out[e.ID] = PresentDifferent
				continue
			}
			sum, _, herr := HashFile(e.Dst)
			if herr != nil {
				return nil, fmt.Errorf("hash %s: %w", e.Dst, herr)
			}
			if sum == e.SHA256 {
				out[e.ID] = PresentSame
				continue
			}
			if !e.FFAllowed {
				out[e.ID] = PresentDifferent
				continue
			}
			if staged {
				ok, perr := session.IsPrefix(e.Dst, StagedPath(stagingDir, e.ID))
				if perr != nil {
					return nil, fmt.Errorf("prefix check %s: %w", e.Dst, perr)
				}
				if ok {
					out[e.ID] = FFCandidate
				} else {
					out[e.ID] = PresentDifferent
				}
				continue
			}
			if st.Size() < e.Size {
				out[e.ID] = FFCandidate // provisional; exact check once staged
			} else {
				out[e.ID] = PresentDifferent
			}
		}
	}
	return out, nil
}

// Need lists entry ids that must be sent given statuses (manifest order):
// absent, staged-mismatch, ff-candidate, and present-different for FFAllowed
// entries (only reachable after Blocking passed with --force). Need has no
// staging-dir parameter, so an ff-candidate that is ALREADY staged is listed
// again; Receive (Task 12) skips an entry whose verified staged copy exists,
// so the re-listing costs bandwidth for that one file only after a crash
// between staging and install.
func Need(m *Manifest, st map[int]Status) []int {
	var ids []int
	for _, e := range m.Entries {
		switch st[e.ID] {
		case Absent, StagedMismatch, FFCandidate:
			ids = append(ids, e.ID)
		case PresentDifferent:
			if e.FFAllowed {
				ids = append(ids, e.ID)
			}
		}
	}
	return ids
}

// Blocking lists entries whose status forbids install: present-different,
// unless force is set and the entry belongs to this session (FFAllowed).
func Blocking(m *Manifest, st map[int]Status, force bool) []Entry {
	var out []Entry
	for _, e := range m.Entries {
		if st[e.ID] == PresentDifferent && !(force && e.FFAllowed) {
			out = append(out, e)
		}
	}
	return out
}
```
(Drop the unused `sort` import from the import block above.)

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/transfer/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/transfer/diff.go internal/transfer/diff_test.go
git commit -m "feat(transfer): destination diff with staged/ff-candidate classification, Need and Blocking"
```

---

### Task 12: `transfer.Send` / `transfer.Receive` — verified tar+gzip streaming

**Files:**
- Create: `internal/transfer/stream.go`
- Test: `internal/transfer/stream_test.go`

**Interfaces:**
- Consumes: Tasks 10–11 (`copyMaybeRewritten`, `StagedPath`, `HashFile`).
- Produces (verbatim): `Send(ctx, m, need, w, progress)`, `Receive(ctx, m, r, stagingDir, progress)`.

Send: `tar.Writer` inside `gzip.Writer`; entries in manifest order restricted to `need`; tar names are `strconv.Itoa(e.ID)`; dirs → `TypeDir`, symlinks → `TypeSymlink` with `Linkname`, files → `TypeReg`. **Rewrite entries are rewritten to a temp file first** (`os.CreateTemp(m.TmpDir, "rewrite-*.tmp")`; `m.TmpDir==""` → `os.TempDir()`) because a tar header needs the exact size before the body — the manifest size is what Build computed, but the file may have been modified since (a running Claude appends), and a size/hash disagreement must fail loudly here rather than corrupt the stream: after writing the temp file, its size must equal `e.Size` or Send returns an error naming the entry ("source changed since manifest was built"). Non-rewrite files are streamed directly with `io.CopyN(tw, f, e.Size)` and the same size check. `progress(e, bytesSoFar)` is called after each entry.

Receive: `gzip.Reader` → `tar.Reader`; for each header look up the entry by id; dirs → create `StagedPath+".dir"` (empty file); symlinks → write `StagedPath+".symlink"` with the target; files → if `StagedPath` already exists with the right size, drain and skip (crash between staging and install); else write to `StagedPath+".part"` through a sha256 counter, verify size and hash, `rename` to `StagedPath`; on mismatch delete the `.part` and return an error naming the entry. A truncated stream (`io.ErrUnexpectedEOF`) also deletes the current `.part` and returns the error — nothing else in staging is touched.

- [ ] **Step 1: Write the failing test**

`internal/transfer/stream_test.go`:
```go
package transfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// twoHosts builds a source manifest (from sourceTree) whose Dst paths live in
// a second temp dir, and returns manifest + staging dir on the "dest" host.
func twoHosts(t *testing.T) (*Manifest, string) {
	t.Helper()
	_, files := sourceTree(t)
	destHome := filepath.Join(t.TempDir(), "home", "bob")
	srcHome := filepath.Dir(filepath.Dir(files[0].Root)) // .../home/alice
	pm := session.NewPathMap(session.Mapping{From: srcHome, To: destHome})
	m, err := Build(context.Background(), sid, session.ID(sid), "laptop.example", "big-storage.example", files, pm)
	if err != nil {
		t.Fatal(err)
	}
	m.TmpDir = t.TempDir()
	return m, filepath.Join(t.TempDir(), "staging")
}

func TestSendReceiveRoundTrip(t *testing.T) {
	m, staging := twoHosts(t)
	st, _ := Diff(context.Background(), m, staging)
	need := Need(m, st)

	var buf bytes.Buffer
	var sent []int
	if err := Send(context.Background(), m, need, &buf, func(e Entry, n int64) { sent = append(sent, e.ID) }); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(need, sent); diff != "" {
		t.Errorf("progress ids (-want +got):\n%s", diff)
	}
	var recv []int
	if err := Receive(context.Background(), m, &buf, staging, func(e Entry, n int64) { recv = append(recv, e.ID) }); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(need, recv); diff != "" {
		t.Errorf("received ids (-want +got):\n%s", diff)
	}

	// staged transcript is the REWRITTEN content and matches the manifest hash
	sum, size, err := HashFile(StagedPath(staging, 1))
	if err != nil || sum != m.Entries[1].SHA256 || size != m.Entries[1].Size {
		t.Errorf("staged transcript: sum=%s size=%d err=%v", sum, size, err)
	}
	raw, _ := os.ReadFile(StagedPath(staging, 1))
	if strings.Contains(string(raw), "/home/alice/") {
		t.Errorf("staged transcript still mentions the source home: %s", raw)
	}
	if _, err := os.Stat(StagedPath(staging, 0) + ".dir"); err != nil {
		t.Errorf("dir metadata missing: %v", err)
	}
	link, _ := os.ReadFile(StagedPath(staging, 4) + ".symlink")
	if string(link) != "../"+sid+".jsonl" {
		t.Errorf("symlink metadata = %q", link)
	}
	st, _ = Diff(context.Background(), m, staging)
	for id, s := range st {
		if s != StagedSame {
			t.Errorf("entry %d after receive = %s, want staged-same", id, s)
		}
	}
	entries, _ := os.ReadDir(m.TmpDir)
	if len(entries) != 0 {
		t.Errorf("rewrite temp files left behind: %v", entries)
	}
}

func TestReceiveInterruptedLosesOnlyInFlightEntry(t *testing.T) {
	m, staging := twoHosts(t)
	need := Need(m, map[int]Status{0: Absent, 1: Absent, 2: Absent, 3: Absent, 4: Absent})
	var full bytes.Buffer
	if err := Send(context.Background(), m, need, &full, nil); err != nil {
		t.Fatal(err)
	}
	// cut the gzip stream at 60%: some entries complete, one is in flight
	cut := full.Bytes()[:full.Len()*6/10]
	err := Receive(context.Background(), m, bytes.NewReader(cut), staging, nil)
	if err == nil {
		t.Fatal("truncated stream must be an error")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) && !strings.Contains(err.Error(), "unexpected EOF") {
		t.Errorf("err = %v", err)
	}
	parts, _ := filepath.Glob(filepath.Join(staging, "*.part"))
	if len(parts) != 0 {
		t.Errorf(".part remnants: %v", parts)
	}
	st, err := Diff(context.Background(), m, staging)
	if err != nil {
		t.Fatal(err)
	}
	stagedCount := 0
	for _, s := range st {
		if s == StagedSame {
			stagedCount++
		}
	}
	if stagedCount == 0 || stagedCount == len(m.Entries) {
		t.Fatalf("expected a partial staging, got %d/%d staged: %v", stagedCount, len(m.Entries), st)
	}

	// second round: only Need() is sent, and it completes
	rest := Need(m, st)
	var again bytes.Buffer
	if err := Send(context.Background(), m, rest, &again, nil); err != nil {
		t.Fatal(err)
	}
	if err := Receive(context.Background(), m, &again, staging, nil); err != nil {
		t.Fatal(err)
	}
	st, _ = Diff(context.Background(), m, staging)
	for id, s := range st {
		if s != StagedSame {
			t.Errorf("entry %d = %s after resume", id, s)
		}
	}
}

func TestReceiveHashMismatchDeletesPart(t *testing.T) {
	m, staging := twoHosts(t)
	var buf bytes.Buffer
	if err := Send(context.Background(), m, []int{3}, &buf, nil); err != nil {
		t.Fatal(err)
	}
	m.Entries[3].SHA256 = sha("something else")
	err := Receive(context.Background(), m, &buf, staging, nil)
	if err == nil || !strings.Contains(err.Error(), "entry 3") {
		t.Fatalf("err = %v, want hash mismatch naming entry 3", err)
	}
	if _, err := os.Stat(StagedPath(staging, 3) + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part must be deleted on mismatch")
	}
	if _, err := os.Stat(StagedPath(staging, 3)); !os.IsNotExist(err) {
		t.Errorf("mismatched entry must not be staged")
	}
}

func TestSendRefusesChangedSource(t *testing.T) {
	m, _ := twoHosts(t)
	f, _ := os.OpenFile(m.Entries[3].Src, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("\n{}")
	f.Close()
	err := Send(context.Background(), m, []int{3}, io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("err = %v, want 'changed since manifest was built'", err)
	}
}
```
- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/transfer/ -run 'TestSend|TestReceive' -v`
Expected: FAIL — undefined `Send`, `Receive`.

- [ ] **Step 3: Implement**

`internal/transfer/stream.go`:
```go
package transfer

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// Send writes a gzip'd tar of the needed entries (manifest order) to w.
// Rewrite entries are rewritten to a temp file first: tar needs the exact
// size in the header before the body, and the rewrite changes the size.
func Send(ctx context.Context, m *Manifest, need []int, w io.Writer, progress func(Entry, int64)) error {
	wanted := make(map[int]bool, len(need))
	for _, id := range need {
		wanted[id] = true
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	var total int64
	for _, e := range m.Entries {
		if !wanted[e.ID] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr := &tar.Header{Name: strconv.Itoa(e.ID), Mode: int64(os.FileMode(e.Mode).Perm()), ModTime: e.ModTime}
		switch {
		case e.IsDir():
			hdr.Typeflag = tar.TypeDir
			if err := tw.WriteHeader(hdr); err != nil {
				return fmt.Errorf("send entry %d (%s): %w", e.ID, e.Src, err)
			}
		case e.IsSymlink():
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = e.Symlink
			if err := tw.WriteHeader(hdr); err != nil {
				return fmt.Errorf("send entry %d (%s): %w", e.ID, e.Src, err)
			}
		default:
			hdr.Typeflag = tar.TypeReg
			hdr.Size = e.Size
			if err := sendFile(tw, hdr, e, m); err != nil {
				return err
			}
		}
		total += e.Size
		if progress != nil {
			progress(e, total)
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("send: close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("send: close gzip: %w", err)
	}
	return nil
}

func sendFile(tw *tar.Writer, hdr *tar.Header, e Entry, m *Manifest) error {
	src, err := os.Open(e.Src)
	if err != nil {
		return fmt.Errorf("send entry %d: %w", e.ID, err)
	}
	defer src.Close()
	var body io.Reader = src
	if e.Rewrite && !m.PathMap.Empty() {
		tmpDir := m.TmpDir
		if tmpDir == "" {
			tmpDir = os.TempDir()
		}
		tmp, err := os.CreateTemp(tmpDir, "rewrite-*.tmp")
		if err != nil {
			return fmt.Errorf("send entry %d: temp file: %w", e.ID, err)
		}
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		if err := copyMaybeRewritten(src, tmp, e.Src, true, m.PathMap); err != nil {
			return fmt.Errorf("send entry %d (%s): rewrite: %w", e.ID, e.Src, err)
		}
		st, err := tmp.Stat()
		if err != nil {
			return err
		}
		if st.Size() != e.Size {
			return fmt.Errorf("send entry %d (%s): rewritten size %d != manifest %d: source changed since manifest was built", e.ID, e.Src, st.Size(), e.Size)
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return err
		}
		body = tmp
	} else {
		st, err := src.Stat()
		if err != nil {
			return err
		}
		if st.Size() != e.Size {
			return fmt.Errorf("send entry %d (%s): size %d != manifest %d: source changed since manifest was built", e.ID, e.Src, st.Size(), e.Size)
		}
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("send entry %d (%s): %w", e.ID, e.Src, err)
	}
	if _, err := io.CopyN(tw, body, e.Size); err != nil {
		return fmt.Errorf("send entry %d (%s): body: %w", e.ID, e.Src, err)
	}
	return nil
}

// Receive reads the stream into stagingDir/<id>.part, verifies, renames to
// stagingDir/<id>. A truncated stream loses only the entry in flight.
func Receive(ctx context.Context, m *Manifest, r io.Reader, stagingDir string, progress func(Entry, int64)) error {
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return fmt.Errorf("staging dir %s: %w", stagingDir, err)
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("receive: gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("receive: tar header: %w", err)
		}
		id, err := strconv.Atoi(hdr.Name)
		if err != nil {
			return fmt.Errorf("receive: bad entry name %q", hdr.Name)
		}
		e, ok := m.ByID(id)
		if !ok {
			return fmt.Errorf("receive: entry %d not in manifest", id)
		}
		base := StagedPath(stagingDir, e.ID)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.WriteFile(base+".dir", nil, 0o600); err != nil {
				return fmt.Errorf("receive entry %d: %w", e.ID, err)
			}
		case tar.TypeSymlink:
			if err := os.WriteFile(base+".symlink", []byte(hdr.Linkname), 0o600); err != nil {
				return fmt.Errorf("receive entry %d: %w", e.ID, err)
			}
		case tar.TypeReg:
			if st, serr := os.Stat(base); serr == nil && st.Size() == e.Size {
				if _, err := io.Copy(io.Discard, tr); err != nil {
					return fmt.Errorf("receive entry %d: drain: %w", e.ID, err)
				}
				break
			}
			if err := receiveFile(tr, base, e, hdr.Size); err != nil {
				return err
			}
		default:
			return fmt.Errorf("receive entry %d: unsupported tar type %q", e.ID, hdr.Typeflag)
		}
		total += e.Size
		if progress != nil {
			progress(e, total)
		}
	}
}

func receiveFile(tr *tar.Reader, base string, e Entry, size int64) error {
	part := base + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("receive entry %d: %w", e.ID, err)
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), tr)
	closeErr := f.Close()
	fail := func(err error) error {
		os.Remove(part)
		return err
	}
	if copyErr != nil {
		return fail(fmt.Errorf("receive entry %d (%s): %w", e.ID, e.Dst, copyErr))
	}
	if closeErr != nil {
		return fail(fmt.Errorf("receive entry %d: %w", e.ID, closeErr))
	}
	if n != e.Size || n != size {
		return fail(fmt.Errorf("receive entry %d (%s): size %d, manifest %d", e.ID, e.Dst, n, e.Size))
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != e.SHA256 {
		return fail(fmt.Errorf("receive entry %d (%s): sha256 mismatch %s != %s", e.ID, e.Dst, sum, e.SHA256))
	}
	if err := os.Rename(part, base); err != nil {
		return fail(fmt.Errorf("receive entry %d: %w", e.ID, err))
	}
	return nil
}

var _ = filepath.Join
```
(`io.Copy` from a `tar.Reader` on a truncated gzip stream returns `io.ErrUnexpectedEOF`, which is what the interruption test asserts.)

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/transfer/ -v`
Expected: PASS. If `TestReceiveInterruptedLosesOnlyInFlightEntry` finds 0 or all entries staged at the 60% cut, the fixture is too small for gzip framing — enlarge `sourceTree`'s transcript to ~200 lines (loop-generated) so the cut lands mid-file; do not weaken the assertion.

- [ ] **Step 5: Commit**

```bash
git add internal/transfer/stream.go internal/transfer/stream_test.go
git commit -m "feat(transfer): verified tar+gzip Send/Receive with per-entry staging and resume"
```

---

### Task 13: `transfer.Install` and `transfer.Uninstall`

**Files:**
- Create: `internal/transfer/install.go`
- Test: `internal/transfer/install_test.go`

**Interfaces:**
- Consumes: Tasks 10–12; Plan 01 `session.MergeIndexEntry(projectDir, e)`, `session.AppendHistory(historyFile, lines)`, `session.AddProjectEntry(globalJSON, cwd, e)`, `session.Paths` (`ProjectDir(cwd)`, `HistoryFile()`, `GlobalJSON`), `session.IsPrefix`.
- Produces (verbatim): `InstallReport`, `InstallExtras`, `Install`; addition `func Uninstall(m *Manifest, p session.Paths) (removed []string, err error)`.

Install rules (spec §7.5), per entry in manifest order:

| Status | Action |
|---|---|
| `staged-same` (Dst absent, staged verified) | dirs: `MkdirAll(Dst, mode)`; symlinks: `os.Symlink`; files: create parents (`0700` for `CatSession`, `0755` otherwise), move staged → Dst (rename; copy+rename on `EXDEV`), `Chmod(mode)`, `Chtimes(mtime)` |
| `present-same` | remove the staged copy/metadata if any |
| `ff-candidate` | verify `session.IsPrefix(Dst, staged)` again; move staged over Dst (the only overwrite); `FastForwarded++` |
| `absent`, `staged-mismatch`, `present-different` | error naming `Dst` and the status; nothing further is touched |

Then the merges: `IndexEntry` (if non-nil) → `session.MergeIndexEntry(p.ProjectDir(extra.ProjectCwd), *e)` with `FileMtime` set from the installed transcript's mtime (ms since epoch) when `FullPath` is one of the installed Dst paths; `History` → `session.AppendHistory(p.HistoryFile(), lines)`; `ProjectEntry` (non-nil) → `session.AddProjectEntry(p.GlobalJSON, extra.ProjectCwd, e)`; `Memory` entries: if Dst absent → move staged copy in and list in `MemoryCopied`; if present → compare hash, list in `MemoryDiffers` when different, drop the staged copy either way.

Uninstall (for `abandon --delete-destination-files`): for every regular-file entry whose Dst exists **and whose current sha256 equals the manifest hash**, remove it; then remove manifest directories in reverse order only when empty; symlinks whose target matches are removed. Files that differ are left and reported in the error-free return (they are simply not in `removed`).

- [ ] **Step 1: Write the failing test**

`internal/transfer/install_test.go`:
```go
package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// staged builds two hosts, streams everything into staging and returns the
// dest Paths (config dir = <destHome>/.claude).
func staged(t *testing.T) (*Manifest, string, session.Paths) {
	t.Helper()
	m, staging := twoHosts(t)
	st, _ := Diff(context.Background(), m, staging)
	var buf bytes.Buffer
	if err := Send(context.Background(), m, Need(m, st), &buf, nil); err != nil {
		t.Fatal(err)
	}
	if err := Receive(context.Background(), m, &buf, staging, nil); err != nil {
		t.Fatal(err)
	}
	cfg := m.Entries[0].Dst
	for filepath.Base(cfg) != ".claude" {
		cfg = filepath.Dir(cfg)
	}
	home := filepath.Dir(cfg)
	p := session.Paths{Home: home, ConfigDir: cfg, GlobalJSON: filepath.Join(home, ".claude.json"), DataDir: filepath.Join(home, ".local", "share", "claude-teleport")}
	return m, staging, p
}

func TestInstallFreshDestination(t *testing.T) {
	m, staging, p := staged(t)
	st, _ := Diff(context.Background(), m, staging)
	extra := InstallExtras{
		IndexEntry: &session.IndexEntry{SessionID: sid, FullPath: m.Entries[1].Dst, ProjectPath: "/home/bob/work", FirstPrompt: "hello"},
		History:    []json.RawMessage{json.RawMessage(`{"display":"hi","timestamp":1,"project":"/home/bob/work","sessionId":"` + sid + `"}`)},
		ProjectCwd: "/home/bob/work",
		ProjectEntry: session.ProjectEntry{"hasTrustDialogAccepted": true},
	}
	rep, err := Install(context.Background(), m, st, staging, p, extra)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Installed != 5 || rep.FastForwarded != 0 || rep.SkippedSame != 0 {
		t.Errorf("report = %+v", rep)
	}
	if rep.IndexMerged != 1 || rep.HistoryAdded != 1 || !rep.ProjectEntryAdded {
		t.Errorf("merges = %+v", rep)
	}
	for _, e := range m.Entries {
		fi, err := os.Lstat(e.Dst)
		if err != nil {
			t.Errorf("%s not installed: %v", e.Dst, err)
			continue
		}
		if e.IsRegular() && fi.Mode().Perm() != os.FileMode(e.Mode).Perm() {
			t.Errorf("%s mode %v want %v", e.Dst, fi.Mode().Perm(), os.FileMode(e.Mode).Perm())
		}
		if e.IsRegular() && !fi.ModTime().Equal(e.ModTime) {
			t.Errorf("%s mtime %v want %v", e.Dst, fi.ModTime(), e.ModTime)
		}
	}
	parent, _ := os.Stat(filepath.Dir(m.Entries[3].Dst)) // todos/ created by Install
	if parent.Mode().Perm() != 0o700 {
		t.Errorf("session parent dir mode = %v, want 0700", parent.Mode().Perm())
	}
	if _, err := os.Stat(StagedPath(staging, 1)); !os.IsNotExist(err) {
		t.Errorf("staged copy must be moved, not copied")
	}
	idx, _ := os.ReadFile(filepath.Join(p.ProjectDir("/home/bob/work"), "sessions-index.json"))
	if !strings.Contains(string(idx), sid) {
		t.Errorf("sessions-index.json not merged: %s", idx)
	}
	hist, _ := os.ReadFile(p.HistoryFile())
	if !strings.Contains(string(hist), `"display":"hi"`) {
		t.Errorf("history not appended: %s", hist)
	}
	gj, _ := os.ReadFile(p.GlobalJSON)
	if !strings.Contains(string(gj), `"/home/bob/work"`) {
		t.Errorf("project entry not added: %s", gj)
	}

	// idempotent: a second Install after a fresh Diff is all present-same
	st, _ = Diff(context.Background(), m, staging)
	rep, err = Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err != nil || rep.SkippedSame != 5 || rep.Installed != 0 {
		t.Errorf("second install: rep=%+v err=%v", rep, err)
	}
}

func TestInstallFastForwardOverwritesOnlyPrefix(t *testing.T) {
	m, staging, p := staged(t)
	full, _ := os.ReadFile(StagedPath(staging, 1))
	os.MkdirAll(filepath.Dir(m.Entries[1].Dst), 0o700)
	os.WriteFile(m.Entries[1].Dst, full[:len(full)/2], 0o600)
	st, _ := Diff(context.Background(), m, staging)
	if st[1] != FFCandidate {
		t.Fatalf("status = %s", st[1])
	}
	rep, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.FastForwarded != 1 {
		t.Errorf("report = %+v", rep)
	}
	got, _ := os.ReadFile(m.Entries[1].Dst)
	if !bytes.Equal(got, full) {
		t.Errorf("fast-forward did not replace the prefix")
	}
}

func TestInstallRefusesCollision(t *testing.T) {
	m, staging, p := staged(t)
	os.MkdirAll(filepath.Dir(m.Entries[3].Dst), 0o700)
	os.WriteFile(m.Entries[3].Dst, []byte("unrelated"), 0o600)
	st, _ := Diff(context.Background(), m, staging)
	_, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err == nil || !strings.Contains(err.Error(), m.Entries[3].Dst) || !strings.Contains(err.Error(), "present-different") {
		t.Fatalf("err = %v, want refusal naming the path", err)
	}
	got, _ := os.ReadFile(m.Entries[3].Dst)
	if string(got) != "unrelated" {
		t.Errorf("collision file was modified")
	}
	// entries before the collision were installed (idempotent), the rest untouched
	if _, err := os.Stat(m.Entries[1].Dst); err != nil {
		t.Errorf("entry 1 should have been installed before the stop: %v", err)
	}
	if _, err := os.Lstat(m.Entries[4].Dst); !os.IsNotExist(err) {
		t.Errorf("entry 4 must not be installed after the stop")
	}
}

func TestInstallRefusesNotStaged(t *testing.T) {
	m, staging, p := staged(t)
	os.Remove(StagedPath(staging, 2))
	st, _ := Diff(context.Background(), m, staging)
	_, err := Install(context.Background(), m, st, staging, p, InstallExtras{})
	if err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("err = %v, want absent refusal", err)
	}
}

func TestInstallMemoryCopyIfAbsent(t *testing.T) {
	m, staging, p := staged(t)
	memSrc := filepath.Join(t.TempDir(), "memory.md")
	os.WriteFile(memSrc, []byte("# notes\n"), 0o600)
	memDst := filepath.Join(p.ConfigDir, "projects", "-home-bob-work", "memory", "MEMORY.md")
	mem := Entry{ID: 5, Category: session.CatSession, Src: memSrc, Dst: memDst, Size: 8, Mode: 0o600, SHA256: sha("# notes\n")}
	m.Entries = append(m.Entries, mem)
	writeFile(t, StagedPath(staging, 5), "# notes\n")
	st, _ := Diff(context.Background(), m, staging)
	rep, err := Install(context.Background(), m, st, staging, p, InstallExtras{Memory: []Entry{mem}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.MemoryCopied) != 1 || rep.MemoryCopied[0] != memDst {
		t.Errorf("MemoryCopied = %v", rep.MemoryCopied)
	}
	// second run with different dest content: reported, not overwritten
	os.WriteFile(memDst, []byte("# edited locally\n"), 0o600)
	writeFile(t, StagedPath(staging, 5), "# notes\n")
	st, _ = Diff(context.Background(), m, staging)
	rep, err = Install(context.Background(), m, st, staging, p, InstallExtras{Memory: []Entry{mem}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.MemoryDiffers) != 1 || rep.MemoryDiffers[0] != memDst {
		t.Errorf("MemoryDiffers = %v", rep.MemoryDiffers)
	}
	got, _ := os.ReadFile(memDst)
	if string(got) != "# edited locally\n" {
		t.Errorf("memory file overwritten")
	}
}

func TestUninstallRemovesOnlyMatching(t *testing.T) {
	m, staging, p := staged(t)
	st, _ := Diff(context.Background(), m, staging)
	if _, err := Install(context.Background(), m, st, staging, p, InstallExtras{}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(m.Entries[3].Dst, []byte("changed after install"), 0o600)
	removed, err := Uninstall(m, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range removed {
		if r == m.Entries[3].Dst {
			t.Errorf("modified file must not be removed")
		}
	}
	if _, err := os.Stat(m.Entries[3].Dst); err != nil {
		t.Errorf("modified file gone")
	}
	if _, err := os.Stat(m.Entries[1].Dst); !os.IsNotExist(err) {
		t.Errorf("matching transcript should be removed")
	}
	if _, err := os.Lstat(m.Entries[4].Dst); !os.IsNotExist(err) {
		t.Errorf("symlink should be removed")
	}
	if _, err := os.Stat(m.Entries[0].Dst); !os.IsNotExist(err) {
		t.Errorf("emptied project dir should be removed")
	}
}
```
Note for `TestInstallMemoryCopyIfAbsent`: memory entries are part of `m.Entries` (so `Diff`/`Receive` handle them) **and** listed in `extra.Memory`; `Install` treats ids listed in `extra.Memory` with the copy-if-absent rule instead of the table above. `Build` callers (Plan 03 orchestrator) append `Inventory.Memory` entries after the regular files and pass them in `InstallExtras.Memory`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/transfer/ -run 'TestInstall|TestUninstall' -v`
Expected: FAIL — undefined `Install`, `InstallExtras`, `Uninstall`.

- [ ] **Step 3: Implement**

`internal/transfer/install.go`:
```go
package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/mithro/go-claude-teleport/internal/session"
)

type InstallReport struct {
	Installed, SkippedSame, FastForwarded int
	IndexMerged, HistoryAdded            int
	ProjectEntryAdded                    bool
	MemoryCopied, MemoryDiffers          []string
}

type InstallExtras struct {
	IndexEntry   *session.IndexEntry
	History      []json.RawMessage
	ProjectCwd   string
	ProjectEntry session.ProjectEntry
	Memory       []Entry // memory files: copy only if absent
}

func parentMode(e Entry) os.FileMode {
	if e.Category == session.CatSession || e.Category == session.CatCapture {
		return 0o700
	}
	return 0o755
}

// moveFile renames src to dst, falling back to copy+rename across filesystems.
func moveFile(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	var lerr *os.LinkError
	if !errors.As(err, &lerr) || !errors.Is(lerr.Err, syscall.EXDEV) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".claude-teleport.tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Remove(src)
}

func dropStaged(stagingDir string, e Entry) {
	base := StagedPath(stagingDir, e.ID)
	os.Remove(base)
	os.Remove(base + ".dir")
	os.Remove(base + ".symlink")
}

func placeFile(stagingDir string, e Entry) error {
	if err := os.MkdirAll(filepath.Dir(e.Dst), parentMode(e)); err != nil {
		return fmt.Errorf("create parent of %s: %w", e.Dst, err)
	}
	if err := moveFile(StagedPath(stagingDir, e.ID), e.Dst); err != nil {
		return fmt.Errorf("install %s: %w", e.Dst, err)
	}
	if err := os.Chmod(e.Dst, os.FileMode(e.Mode).Perm()); err != nil {
		return fmt.Errorf("chmod %s: %w", e.Dst, err)
	}
	if !e.ModTime.IsZero() {
		if err := os.Chtimes(e.Dst, e.ModTime, e.ModTime); err != nil {
			return fmt.Errorf("chtimes %s: %w", e.Dst, err)
		}
	}
	return nil
}

func placeEntry(stagingDir string, e Entry) error {
	switch {
	case e.IsDir():
		if err := os.MkdirAll(e.Dst, os.FileMode(e.Mode).Perm()|0o700); err != nil {
			return fmt.Errorf("create dir %s: %w", e.Dst, err)
		}
		os.Remove(StagedPath(stagingDir, e.ID) + ".dir")
		return nil
	case e.IsSymlink():
		if err := os.MkdirAll(filepath.Dir(e.Dst), parentMode(e)); err != nil {
			return fmt.Errorf("create parent of %s: %w", e.Dst, err)
		}
		if err := os.Symlink(e.Symlink, e.Dst); err != nil {
			return fmt.Errorf("symlink %s: %w", e.Dst, err)
		}
		os.Remove(StagedPath(stagingDir, e.ID) + ".symlink")
		return nil
	}
	return placeFile(stagingDir, e)
}

// Install moves staged entries into place per spec §7.5 and performs the merges.
func Install(ctx context.Context, m *Manifest, st map[int]Status, stagingDir string, p session.Paths, extra InstallExtras) (*InstallReport, error) {
	rep := &InstallReport{}
	memory := map[int]bool{}
	for _, e := range extra.Memory {
		memory[e.ID] = true
	}
	installed := map[string]bool{}
	for _, e := range m.Entries {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if memory[e.ID] {
			continue
		}
		switch st[e.ID] {
		case StagedSame:
			if err := placeEntry(stagingDir, e); err != nil {
				return rep, err
			}
			rep.Installed++
			installed[e.Dst] = true
		case PresentSame:
			dropStaged(stagingDir, e)
			rep.SkippedSame++
		case FFCandidate:
			staged := StagedPath(stagingDir, e.ID)
			if _, err := os.Stat(staged); err != nil {
				return rep, fmt.Errorf("install %s: ff-candidate but nothing staged: %w", e.Dst, err)
			}
			ok, err := session.IsPrefix(e.Dst, staged)
			if err != nil {
				return rep, fmt.Errorf("install %s: prefix check: %w", e.Dst, err)
			}
			if !ok {
				return rep, fmt.Errorf("install %s: existing file is not a prefix of the incoming one (present-different)", e.Dst)
			}
			if err := placeFile(stagingDir, e); err != nil {
				return rep, err
			}
			rep.FastForwarded++
			installed[e.Dst] = true
		default:
			return rep, fmt.Errorf("install %s: status %s — refusing (nothing after this entry was touched)", e.Dst, st[e.ID])
		}
	}

	for _, e := range extra.Memory {
		switch st[e.ID] {
		case StagedSame:
			if err := placeEntry(stagingDir, e); err != nil {
				return rep, err
			}
			rep.MemoryCopied = append(rep.MemoryCopied, e.Dst)
		case PresentSame:
			dropStaged(stagingDir, e)
		case Absent, StagedMismatch:
			return rep, fmt.Errorf("install memory %s: status %s (not staged)", e.Dst, st[e.ID])
		default:
			rep.MemoryDiffers = append(rep.MemoryDiffers, e.Dst)
			dropStaged(stagingDir, e)
		}
	}

	if extra.IndexEntry != nil {
		ie := *extra.IndexEntry
		if installed[ie.FullPath] {
			if fi, err := os.Stat(ie.FullPath); err == nil {
				ie.FileMtime = fi.ModTime().UnixMilli()
			}
		}
		if err := session.MergeIndexEntry(p.ProjectDir(extra.ProjectCwd), ie); err != nil {
			return rep, fmt.Errorf("merge sessions-index: %w", err)
		}
		rep.IndexMerged = 1
	}
	if len(extra.History) > 0 {
		n, err := session.AppendHistory(p.HistoryFile(), extra.History)
		if err != nil {
			return rep, fmt.Errorf("append history: %w", err)
		}
		rep.HistoryAdded = n
	}
	if extra.ProjectEntry != nil {
		added, err := session.AddProjectEntry(p.GlobalJSON, extra.ProjectCwd, extra.ProjectEntry)
		if err != nil {
			return rep, fmt.Errorf("add project entry: %w", err)
		}
		rep.ProjectEntryAdded = added
	}
	return rep, nil
}

// Uninstall removes manifest-listed installed files whose current content
// still matches the manifest (for `abandon --delete-destination-files`).
// Directories are removed in reverse order only when empty.
func Uninstall(m *Manifest, p session.Paths) ([]string, error) {
	var removed []string
	for _, e := range m.Entries {
		switch {
		case e.IsDir():
			continue
		case e.IsSymlink():
			if target, err := os.Readlink(e.Dst); err == nil && target == e.Symlink {
				if err := os.Remove(e.Dst); err != nil {
					return removed, fmt.Errorf("remove %s: %w", e.Dst, err)
				}
				removed = append(removed, e.Dst)
			}
		default:
			sum, _, err := HashFile(e.Dst)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return removed, fmt.Errorf("hash %s: %w", e.Dst, err)
			}
			if sum != e.SHA256 {
				continue
			}
			if err := os.Remove(e.Dst); err != nil {
				return removed, fmt.Errorf("remove %s: %w", e.Dst, err)
			}
			removed = append(removed, e.Dst)
		}
	}
	for i := len(m.Entries) - 1; i >= 0; i-- {
		e := m.Entries[i]
		if !e.IsDir() {
			continue
		}
		if err := os.Remove(e.Dst); err == nil {
			removed = append(removed, e.Dst)
		}
	}
	_ = p
	return removed, nil
}
```
`p` is accepted by `Uninstall` so callers can later restrict removal to paths under `p.ConfigDir`/data dir; in this plan it only documents the intent — the `_ = p` line is replaced by that check in Plan 03 if needed.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/transfer/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/transfer/install.go internal/transfer/install_test.go
git commit -m "feat(transfer): install staged entries per spec 7.5 with merges, memory copy-if-absent, uninstall"
```

---

### Task 14: `remote` protocol types, `Endpoint`, op structs, and `Serve`

**Files:**
- Create: `internal/remote/protocol.go`
- Create: `internal/remote/plan03_types.go`
- Create: `internal/remote/endpoint.go`
- Create: `internal/remote/ops.go`
- Create: `internal/remote/server.go`
- Test: `internal/remote/server_test.go`

**Interfaces:**
- Consumes: `session`, `claudecfg`, `job`, `transfer`, `version.Protocol`, `version.Version`.
- Produces (verbatim from the interfaces doc): `HostInfo`, `StreamKind` + constants, `Endpoint`, `Request`, `Response`, `Error`, `(*Error).Error`, `Serve`, `ServeStream`. Plus the opaque Plan 03 aliases and one args/result struct pair per op in `ops.go`.

- [ ] **Step 1: Write the protocol types**

`internal/remote/protocol.go`:
```go
// Package remote is the JSON-over-ssh helper protocol (spec §4.3): an
// Endpoint interface, a Local implementation, a Server that dispatches
// requests to it, and a Client that speaks to a remote Server.
package remote

import (
	"encoding/json"
	"fmt"
)

type HostInfo struct {
	Version         string `json:"version"` // claude-teleport version
	Protocol        int    `json:"protocol"`
	Hostname        string `json:"hostname"`
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	UID             int    `json:"uid"`
	Home            string `json:"home"`
	ConfigDir       string `json:"config_dir"`
	DataDir         string `json:"data_dir"`
	TmuxSocketDir   string `json:"tmux_socket_dir"`
	HasTmux         bool   `json:"has_tmux"`
	HasClaude       bool   `json:"has_claude"`
	ClaudeVersion   string `json:"claude_version"`
	HasClaudeResume bool   `json:"has_claude_resume"` // go-tmux-saver's claude-resume on PATH
}

// StreamKind names the bulk channels.
type StreamKind string

const (
	StreamTar     StreamKind = "tar"     // driver -> dest: transfer stream
	StreamCapture StreamKind = "capture" // source -> driver: pane capture
	StreamPack    StreamKind = "pack"    // source -> driver: git packfile
	StreamLog     StreamKind = "log"     // remote job log tail
)

type Request struct {
	ID   int             `json:"id"`
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args"`
}

type Response struct {
	ID     int             `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error codes: "usage" | "not-found" | "conflict" | "drift" | "unavailable" | "internal".
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// Unavailable is the explicit stub error for ops implemented in Plan 03.
func Unavailable(op string) *Error {
	return &Error{Code: "unavailable", Message: op + ": implemented in Plan 03"}
}
```

`internal/remote/plan03_types.go`:
```go
package remote

// Plan 03 REPLACES this file with
//   type GitInfo = gitx.Info; type GitDestState = gitx.DestState; type GitPlan = gitx.Plan
//   type TmuxFacts = tmuxx.Facts; type TmuxPlan = tmuxx.Plan; type TmuxPaneState = tmuxx.PaneState
//   type TmuxDialer = tmuxx.Dialer
// Until then the Endpoint round-trips these values as opaque JSON so the
// protocol, Client and Server are complete and testable without git/tmux.

import "encoding/json"

type GitInfo = json.RawMessage
type GitDestState = json.RawMessage
type GitPlan = json.RawMessage
type TmuxFacts = json.RawMessage
type TmuxPlan = json.RawMessage
type TmuxPaneState = json.RawMessage

// TmuxDialer is nil in this plan (tmux unavailable).
type TmuxDialer = any
```

`internal/remote/endpoint.go`:
```go
package remote

import (
	"context"
	"io"
	"time"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// Endpoint is every operation the orchestrator performs on a host.
// Local implements it directly; Client implements it over the protocol;
// Server dispatches protocol requests to a Local.
type Endpoint interface {
	Hello(ctx context.Context) (HostInfo, error)
	Paths() session.Paths

	// inventories
	ResolveSession(ctx context.Context, sel session.Selector) (*session.Session, error)
	InventorySession(ctx context.Context, id session.ID) (*session.Inventory, *session.Usage, error)
	InventoryHost(ctx context.Context, cwd, claudeVersion string) (*claudecfg.Inventory, error)
	InventoryGit(ctx context.Context, cwd string) (*GitInfo, error)
	GitDestState(ctx context.Context, mainDir, worktreeDir, branch string) (*GitDestState, error)
	InventoryTmux(ctx context.Context, ref *session.TmuxRef, preferredSocket string) (*TmuxFacts, error)

	// transfer
	ManifestDiff(ctx context.Context, m *transfer.Manifest, jobID string) (map[int]transfer.Status, error)
	OpenStream(ctx context.Context, kind StreamKind, jobID, streamID string) (io.ReadWriteCloser, error)
	Install(ctx context.Context, m *transfer.Manifest, jobID string) (*transfer.InstallReport, error)
	GitAttach(ctx context.Context, plan *GitPlan, jobID string) error

	// processes and panes
	Freeze(ctx context.Context, pid int, startTime string) error
	Thaw(ctx context.Context, pid int) error
	Capture(ctx context.Context, ref *session.TmuxRef, jobID string) error
	OpenWindow(ctx context.Context, p *TmuxPlan) (*session.TmuxRef, error)
	StartClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, jobID string, argv []string) error
	ConfirmClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, timeout time.Duration) (*session.Registry, error)
	ExitClaude(ctx context.Context, ref *session.TmuxRef, pid int, startTime string, timeout time.Duration) error
	TypeCommand(ctx context.Context, ref *session.TmuxRef, argv []string) error
	PaneState(ctx context.Context, ref *session.TmuxRef) (*TmuxPaneState, error)
	RunPtyResume(ctx context.Context, id session.ID, cwd string, timeout time.Duration) error

	// journal
	JournalGet(ctx context.Context, jobID string) (*job.Journal, bool, error)
	JournalPut(ctx context.Context, j *job.Journal) error
	Record(ctx context.Context, jobID string, rec job.HistoryRecord) error
}
```
Note: `Install` over the wire takes only the manifest and job id; the `InstallExtras` (index entry, history lines, project entry, memory ids) are carried **inside** the manifest file for that job — see `InstallArgs` below — so `Local.Install` reads them from `jobs/<id>/extras.json` written by `ManifestDiff`'s caller. To keep the interface verbatim, this plan adds `PutInstallExtras(ctx, jobID string, extra transfer.InstallExtras) error` as an extra method on both `Local` and `Client` (not on `Endpoint`; Plan 03's orchestrator type-asserts `interface{ PutInstallExtras(...) }` — recorded under "Interface additions"). The statuses `Install` needs are recomputed by a fresh `Diff` on the destination.

- [ ] **Step 2: Write ops.go (args/result structs shared by client and server)**

`internal/remote/ops.go`:
```go
package remote

import (
	"encoding/json"
	"time"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// Op names (spec §4.3). hello is always first.
const (
	OpHello            = "hello"
	OpPaths            = "paths"
	OpResolveSession   = "resolve-session"
	OpInventorySession = "inventory-session"
	OpInventoryHost    = "inventory-host"
	OpInventoryGit     = "inventory-git"
	OpGitDestState     = "git-dest-state"
	OpInventoryTmux    = "inventory-tmux"
	OpManifestDiff     = "manifest-diff"
	OpInstallExtras    = "install-extras"
	OpInstall          = "install"
	OpGitAttach        = "git-attach"
	OpFreeze           = "freeze"
	OpThaw             = "thaw"
	OpCapture          = "tmux-capture"
	OpOpenWindow       = "tmux-open"
	OpStartClaude      = "claude-start"
	OpConfirmClaude    = "claude-confirm"
	OpExitClaude       = "claude-exit"
	OpTypeCommand      = "tmux-keys"
	OpPaneState        = "shape-state"
	OpRunPtyResume     = "claude-pty-resume"
	OpJournalGet       = "job-journal-get"
	OpJournalPut       = "job-journal-put"
	OpRecord           = "record"
)

type HelloArgs struct {
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}
type PathsResult struct {
	Paths session.Paths `json:"paths"`
}
type ResolveSessionArgs struct {
	Selector session.Selector `json:"selector"`
}
type ResolveSessionResult struct {
	Session *session.Session `json:"session"`
}
type InventorySessionArgs struct {
	ID session.ID `json:"id"`
}
type InventorySessionResult struct {
	Inventory *session.Inventory `json:"inventory"`
	Usage     *session.Usage     `json:"usage"`
}
type InventoryHostArgs struct {
	Cwd           string `json:"cwd"`
	ClaudeVersion string `json:"claude_version"`
}
type InventoryHostResult struct {
	Inventory *claudecfg.Inventory `json:"inventory"`
}
type InventoryGitArgs struct {
	Cwd string `json:"cwd"`
}
type InventoryGitResult struct {
	Info *GitInfo `json:"info"`
}
type GitDestStateArgs struct {
	MainDir, WorktreeDir, Branch string
}
type GitDestStateResult struct {
	State *GitDestState `json:"state"`
}
type InventoryTmuxArgs struct {
	Ref             *session.TmuxRef `json:"ref"`
	PreferredSocket string           `json:"preferred_socket"`
}
type InventoryTmuxResult struct {
	Facts *TmuxFacts `json:"facts"`
}
type ManifestDiffArgs struct {
	Manifest *transfer.Manifest `json:"manifest"`
	JobID    string             `json:"job_id"`
}
type ManifestDiffResult struct {
	Statuses map[int]transfer.Status `json:"statuses"`
}
type InstallExtrasArgs struct {
	JobID string                 `json:"job_id"`
	Extra transfer.InstallExtras `json:"extra"`
}
type InstallArgs struct {
	Manifest *transfer.Manifest `json:"manifest"`
	JobID    string             `json:"job_id"`
}
type InstallResult struct {
	Report *transfer.InstallReport `json:"report"`
}
type GitAttachArgs struct {
	Plan  *GitPlan `json:"plan"`
	JobID string   `json:"job_id"`
}
type FreezeArgs struct {
	PID       int    `json:"pid"`
	StartTime string `json:"start_time"`
}
type ThawArgs struct {
	PID int `json:"pid"`
}
type CaptureArgs struct {
	Ref   *session.TmuxRef `json:"ref"`
	JobID string           `json:"job_id"`
}
type OpenWindowArgs struct {
	Plan *TmuxPlan `json:"plan"`
}
type OpenWindowResult struct {
	Ref *session.TmuxRef `json:"ref"`
}
type StartClaudeArgs struct {
	Ref   *session.TmuxRef `json:"ref"`
	ID    session.ID       `json:"id"`
	JobID string           `json:"job_id"`
	Argv  []string         `json:"argv"`
}
type ConfirmClaudeArgs struct {
	Ref     *session.TmuxRef `json:"ref"`
	ID      session.ID       `json:"id"`
	Timeout time.Duration    `json:"timeout"`
}
type ConfirmClaudeResult struct {
	Registry *session.Registry `json:"registry"`
}
type ExitClaudeArgs struct {
	Ref       *session.TmuxRef `json:"ref"`
	PID       int              `json:"pid"`
	StartTime string           `json:"start_time"`
	Timeout   time.Duration    `json:"timeout"`
}
type TypeCommandArgs struct {
	Ref  *session.TmuxRef `json:"ref"`
	Argv []string         `json:"argv"`
}
type PaneStateArgs struct {
	Ref *session.TmuxRef `json:"ref"`
}
type PaneStateResult struct {
	State *TmuxPaneState `json:"state"`
}
type RunPtyResumeArgs struct {
	ID      session.ID    `json:"id"`
	Cwd     string        `json:"cwd"`
	Timeout time.Duration `json:"timeout"`
}
type JournalGetArgs struct {
	JobID string `json:"job_id"`
}
type JournalGetResult struct {
	Journal *job.Journal `json:"journal"`
	Found   bool         `json:"found"`
}
type JournalPutArgs struct {
	Journal *job.Journal `json:"journal"`
}
type RecordArgs struct {
	JobID  string            `json:"job_id"`
	Record job.HistoryRecord `json:"record"`
}

// Empty is the result of ops that return nothing.
type Empty struct{}

var _ = json.RawMessage(nil)
```

- [ ] **Step 3: Write the failing Serve test**

`internal/remote/server_test.go`:
```go
package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/version"
)

// stubEndpoint embeds a nil Endpoint so only the methods we override exist.
type stubEndpoint struct {
	Endpoint
	hello func() (HostInfo, error)
}

func (s stubEndpoint) Hello(ctx context.Context) (HostInfo, error) { return s.hello() }
func (s stubEndpoint) Paths() session.Paths                        { return session.Paths{Home: "/home/alice"} }
func (s stubEndpoint) Thaw(ctx context.Context, pid int) error {
	if pid == 0 {
		panic("pid zero")
	}
	return &Error{Code: "not-found", Message: "no such pid"}
}

func roundTrip(t *testing.T, ep Endpoint, reqs ...string) []Response {
	t.Helper()
	in := strings.NewReader(strings.Join(reqs, "\n") + "\n")
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), in, pw, ep); pw.Close() }()
	var out []Response
	sc := bufio.NewScanner(pr)
	for sc.Scan() {
		var r Response
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("bad response line %q: %v", sc.Text(), err)
		}
		out = append(out, r)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	return out
}

func TestServeHelloAndProtocolMismatch(t *testing.T) {
	ep := stubEndpoint{hello: func() (HostInfo, error) {
		return HostInfo{Version: version.Version, Protocol: version.Protocol, Hostname: "big-storage.example"}, nil
	}}
	rs := roundTrip(t, ep,
		`{"id":1,"op":"hello","args":{"version":"v0.3","protocol":1}}`,
		`{"id":2,"op":"hello","args":{"version":"v0.3","protocol":99}}`,
		`{"id":3,"op":"paths","args":{}}`,
	)
	if len(rs) != 3 {
		t.Fatalf("got %d responses", len(rs))
	}
	if !rs[0].OK || rs[0].ID != 1 || !strings.Contains(string(rs[0].Result), `"hostname":"big-storage.example"`) {
		t.Errorf("hello: %+v %s", rs[0], rs[0].Result)
	}
	if rs[1].OK || rs[1].Error == nil || rs[1].Error.Code != "usage" || !strings.Contains(rs[1].Error.Message, "99") || !strings.Contains(rs[1].Error.Message, "1") {
		t.Errorf("protocol mismatch must report both versions: %+v", rs[1].Error)
	}
	var pr PathsResult
	json.Unmarshal(rs[2].Result, &pr)
	if pr.Paths.Home != "/home/alice" {
		t.Errorf("paths: %+v", pr)
	}
}

func TestServeErrorsAndPanics(t *testing.T) {
	ep := stubEndpoint{hello: func() (HostInfo, error) { return HostInfo{}, errors.New("nope") }}
	rs := roundTrip(t, ep,
		`{"id":1,"op":"thaw","args":{"pid":42}}`,
		`{"id":2,"op":"thaw","args":{"pid":0}}`,
		`{"id":3,"op":"no-such-op","args":{}}`,
		`not json at all`,
		`{"id":5,"op":"thaw","args":{"pid":"x"}}`,
	)
	want := []struct {
		id   int
		code string
	}{{1, "not-found"}, {2, "internal"}, {3, "usage"}, {0, "usage"}, {5, "usage"}}
	if len(rs) != len(want) {
		t.Fatalf("got %d responses: %+v", len(rs), rs)
	}
	for i, w := range want {
		if rs[i].OK || rs[i].ID != w.id || rs[i].Error == nil || rs[i].Error.Code != w.code {
			t.Errorf("response %d = %+v (err %+v), want id=%d code=%s", i, rs[i], rs[i].Error, w.id, w.code)
		}
	}
	if !strings.Contains(rs[1].Error.Message, "pid zero") {
		t.Errorf("panic text must be reported: %+v", rs[1].Error)
	}
}

func TestServeOverNetPipeStopsOnClose(t *testing.T) {
	a, b := net.Pipe()
	ep := stubEndpoint{hello: func() (HostInfo, error) { return HostInfo{Protocol: version.Protocol}, nil }}
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), a, a, ep) }()
	io.WriteString(b, `{"id":1,"op":"hello","args":{"protocol":1}}`+"\n")
	line, _ := bufio.NewReader(b).ReadString('\n')
	if !strings.Contains(line, `"ok":true`) {
		t.Errorf("line = %q", line)
	}
	b.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve after peer close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after the peer closed")
	}
}
```

- [ ] **Step 4: Run the test to verify it fails**

Run: `go test ./internal/remote/ -run TestServe -v`
Expected: FAIL — undefined `Serve`.

- [ ] **Step 5: Implement server.go**

`internal/remote/server.go`:
```go
package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"sync"

	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/version"
)

// maxLine bounds one request/response line (manifests can be large).
const maxLine = 256 << 20

type handler func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error)

func decode[T any](args json.RawMessage) (T, error) {
	var v T
	if len(args) == 0 {
		return v, nil
	}
	if err := json.Unmarshal(args, &v); err != nil {
		return v, &Error{Code: "usage", Message: "bad args: " + err.Error()}
	}
	return v, nil
}

// dispatch is the op-name -> handler table. Every op's args/result types
// live in ops.go so Client and Server cannot drift apart.
var dispatch = map[string]handler{
	OpHello: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[HelloArgs](args)
		if err != nil {
			return nil, err
		}
		info, err := ep.Hello(ctx)
		if err != nil {
			return nil, err
		}
		if a.Protocol != info.Protocol {
			return nil, &Error{Code: "usage", Message: fmt.Sprintf(
				"protocol mismatch: driver speaks protocol %d (claude-teleport %s), this host speaks protocol %d (claude-teleport %s); install the same version on both hosts",
				a.Protocol, a.Version, info.Protocol, info.Version)}
		}
		return info, nil
	},
	OpPaths: func(ctx context.Context, ep Endpoint, _ json.RawMessage) (any, error) {
		return PathsResult{Paths: ep.Paths()}, nil
	},
	OpResolveSession: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[ResolveSessionArgs](args)
		if err != nil {
			return nil, err
		}
		s, err := ep.ResolveSession(ctx, a.Selector)
		return ResolveSessionResult{Session: s}, err
	},
	OpInventorySession: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[InventorySessionArgs](args)
		if err != nil {
			return nil, err
		}
		inv, usage, err := ep.InventorySession(ctx, a.ID)
		return InventorySessionResult{Inventory: inv, Usage: usage}, err
	},
	OpInventoryHost: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[InventoryHostArgs](args)
		if err != nil {
			return nil, err
		}
		inv, err := ep.InventoryHost(ctx, a.Cwd, a.ClaudeVersion)
		return InventoryHostResult{Inventory: inv}, err
	},
	OpInventoryGit: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[InventoryGitArgs](args)
		if err != nil {
			return nil, err
		}
		info, err := ep.InventoryGit(ctx, a.Cwd)
		return InventoryGitResult{Info: info}, err
	},
	OpGitDestState: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[GitDestStateArgs](args)
		if err != nil {
			return nil, err
		}
		st, err := ep.GitDestState(ctx, a.MainDir, a.WorktreeDir, a.Branch)
		return GitDestStateResult{State: st}, err
	},
	OpInventoryTmux: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[InventoryTmuxArgs](args)
		if err != nil {
			return nil, err
		}
		f, err := ep.InventoryTmux(ctx, a.Ref, a.PreferredSocket)
		return InventoryTmuxResult{Facts: f}, err
	},
	OpManifestDiff: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[ManifestDiffArgs](args)
		if err != nil {
			return nil, err
		}
		st, err := ep.ManifestDiff(ctx, a.Manifest, a.JobID)
		return ManifestDiffResult{Statuses: st}, err
	},
	OpInstallExtras: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[InstallExtrasArgs](args)
		if err != nil {
			return nil, err
		}
		p, ok := ep.(interface {
			PutInstallExtras(ctx context.Context, jobID string, extra transfer.InstallExtras) error
		})
		if !ok {
			return nil, Unavailable(OpInstallExtras)
		}
		return Empty{}, p.PutInstallExtras(ctx, a.JobID, a.Extra)
	},
	OpInstall: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[InstallArgs](args)
		if err != nil {
			return nil, err
		}
		rep, err := ep.Install(ctx, a.Manifest, a.JobID)
		return InstallResult{Report: rep}, err
	},
	OpGitAttach: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[GitAttachArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.GitAttach(ctx, a.Plan, a.JobID)
	},
	OpFreeze: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[FreezeArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.Freeze(ctx, a.PID, a.StartTime)
	},
	OpThaw: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[ThawArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.Thaw(ctx, a.PID)
	},
	OpCapture: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[CaptureArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.Capture(ctx, a.Ref, a.JobID)
	},
	OpOpenWindow: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[OpenWindowArgs](args)
		if err != nil {
			return nil, err
		}
		ref, err := ep.OpenWindow(ctx, a.Plan)
		return OpenWindowResult{Ref: ref}, err
	},
	OpStartClaude: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[StartClaudeArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.StartClaude(ctx, a.Ref, a.ID, a.JobID, a.Argv)
	},
	OpConfirmClaude: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[ConfirmClaudeArgs](args)
		if err != nil {
			return nil, err
		}
		reg, err := ep.ConfirmClaude(ctx, a.Ref, a.ID, a.Timeout)
		return ConfirmClaudeResult{Registry: reg}, err
	},
	OpExitClaude: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[ExitClaudeArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.ExitClaude(ctx, a.Ref, a.PID, a.StartTime, a.Timeout)
	},
	OpTypeCommand: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[TypeCommandArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.TypeCommand(ctx, a.Ref, a.Argv)
	},
	OpPaneState: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[PaneStateArgs](args)
		if err != nil {
			return nil, err
		}
		st, err := ep.PaneState(ctx, a.Ref)
		return PaneStateResult{State: st}, err
	},
	OpRunPtyResume: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[RunPtyResumeArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.RunPtyResume(ctx, a.ID, a.Cwd, a.Timeout)
	},
	OpJournalGet: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[JournalGetArgs](args)
		if err != nil {
			return nil, err
		}
		j, found, err := ep.JournalGet(ctx, a.JobID)
		return JournalGetResult{Journal: j, Found: found}, err
	},
	OpJournalPut: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[JournalPutArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.JournalPut(ctx, a.Journal)
	},
	OpRecord: func(ctx context.Context, ep Endpoint, args json.RawMessage) (any, error) {
		a, err := decode[RecordArgs](args)
		if err != nil {
			return nil, err
		}
		return Empty{}, ep.Record(ctx, a.JobID, a.Record)
	},
}

// toError maps any error to a protocol Error.
func toError(err error) *Error {
	var pe *Error
	if errors.As(err, &pe) {
		return pe
	}
	if errors.Is(err, session.ErrNotFound) {
		return &Error{Code: "not-found", Message: err.Error()}
	}
	return &Error{Code: "internal", Message: err.Error()}
}

func handle(ctx context.Context, ep Endpoint, req Request) (resp Response) {
	resp = Response{ID: req.ID}
	defer func() {
		if r := recover(); r != nil {
			resp.OK = false
			resp.Result = nil
			resp.Error = &Error{Code: "internal", Message: fmt.Sprintf("panic in %s: %v\n%s", req.Op, r, debug.Stack())}
		}
	}()
	h, ok := dispatch[req.Op]
	if !ok {
		resp.Error = &Error{Code: "usage", Message: "unknown op " + req.Op}
		return resp
	}
	result, err := h(ctx, ep, req.Args)
	if err != nil {
		resp.Error = toError(err)
		return resp
	}
	raw, err := json.Marshal(result)
	if err != nil {
		resp.Error = &Error{Code: "internal", Message: "encode result: " + err.Error()}
		return resp
	}
	resp.OK = true
	resp.Result = raw
	return resp
}

// Serve runs the helper: reads Requests from r (one per line), handles them
// one at a time, writes Responses to w. Returns nil at EOF.
func Serve(ctx context.Context, r io.Reader, w io.Writer, ep Endpoint) error {
	var wmu sync.Mutex
	write := func(resp Response) error {
		raw, err := json.Marshal(resp)
		if err != nil {
			return err
		}
		wmu.Lock()
		defer wmu.Unlock()
		_, err = w.Write(append(raw, '\n'))
		return err
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), maxLine)
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			if werr := write(Response{Error: &Error{Code: "usage", Message: "bad request line: " + err.Error()}}); werr != nil {
				return werr
			}
			continue
		}
		if err := write(handle(ctx, ep, req)); err != nil {
			return fmt.Errorf("write response %d: %w", req.ID, err)
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		return fmt.Errorf("read requests: %w", err)
	}
	return nil
}

// ServeStream handles `remote stream <kind> <job> <id>`: connects stdin/stdout
// to the local stream endpoint. Bytes from stdin go INTO the stream, bytes
// from the stream go to stdout; when stdin hits EOF the stream is closed and
// the close error (e.g. a receive verification failure) is returned.
func ServeStream(ctx context.Context, kind StreamKind, jobID, streamID string, stdin io.Reader, stdout io.Writer, ep Endpoint) error {
	s, err := ep.OpenStream(ctx, kind, jobID, streamID)
	if err != nil {
		return err
	}
	outDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(stdout, s)
		outDone <- err
	}()
	_, inErr := io.Copy(s, stdin)
	closeErr := s.Close()
	outErr := <-outDone
	if inErr != nil {
		return fmt.Errorf("stream %s/%s: stdin: %w", kind, streamID, inErr)
	}
	if closeErr != nil {
		return fmt.Errorf("stream %s/%s: %w", kind, streamID, closeErr)
	}
	if outErr != nil {
		return fmt.Errorf("stream %s/%s: stdout: %w", kind, streamID, outErr)
	}
	return nil
}
```
(Add `"github.com/mithro/go-claude-teleport/internal/transfer"` to the import block for `OpInstallExtras`.)

- [ ] **Step 6: Run the tests**

Run: `go test -race ./internal/remote/ -v`
Expected: PASS. (`net.Pipe` closing surfaces as `io.EOF` to the scanner → `Serve` returns nil.)

- [ ] **Step 7: Commit**

```bash
git add internal/remote/protocol.go internal/remote/plan03_types.go internal/remote/endpoint.go internal/remote/ops.go internal/remote/server.go internal/remote/server_test.go
git commit -m "feat(remote): protocol types, Endpoint interface, shared op structs, Serve with panic recovery"
```

---

### Task 15: `remote.Local` — the in-process Endpoint

**Files:**
- Create: `internal/remote/local.go`
- Test: `internal/remote/local_test.go`

**Interfaces:**
- Consumes: Plan 01 `session.Resolve`, `session.Load`, `session.InventoryFiles`, `session.ScanUsage`, `claudecfg.Collect`, `procx.Freeze`, `procx.Scan`; `job.*`; `transfer.*`.
- Produces (verbatim): `NewLocal(p session.Paths, selfExe string, opts LocalOptions) *Local`, `LocalOptions{ProcRoot string; Probe session.PaneProbe; Tmux TmuxDialer; Logf}`; addition `(*Local).PutInstallExtras(ctx, jobID, extra) error`, `(*Local).Hostname string` field set by `NewLocal` from `os.Hostname()`.

Implemented here: `Hello`, `Paths`, `ResolveSession`, `InventorySession`, `InventoryHost`, `ManifestDiff` (saves the manifest to `jobs/<id>/manifest.json` — the Receive side needs it), `OpenStream` (`tar`: pipe into `transfer.Receive` against the saved manifest, `Close` waits for the receive result; `capture`: reader of `jobs/<id>/capture.txt`; `log`: reader of `jobs/<id>/log.txt` with `TailLog`-style full read; `pack`: `Unavailable`), `Install` (fresh `Diff` + `extras.json`), `Freeze`/`Thaw` (via `procx.Freeze`; freezers kept in a map by pid), `JournalGet`/`JournalPut`/`Record`. Explicit `Unavailable` stubs: `InventoryGit`, `GitDestState`, `InventoryTmux`, `GitAttach`, `Capture`, `OpenWindow`, `StartClaude`, `ConfirmClaude`, `ExitClaude`, `TypeCommand`, `PaneState`, `RunPtyResume`.

- [ ] **Step 1: Write the failing test**

`internal/remote/local_test.go`:
```go
package remote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
	"github.com/mithro/go-claude-teleport/internal/version"
)

const sid = "9c8b7a6f-5e4d-4c3b-a2b1-0f9e8d7c6b5a"

func testPaths(t *testing.T) session.Paths {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home", "bob")
	cfg := filepath.Join(home, ".claude")
	os.MkdirAll(cfg, 0o700)
	return session.Paths{Home: home, ConfigDir: cfg, GlobalJSON: filepath.Join(home, ".claude.json"), DataDir: filepath.Join(home, ".local", "share", "claude-teleport")}
}

// sourceManifest builds a one-file manifest whose Dst lands in p.ConfigDir.
func sourceManifest(t *testing.T, p session.Paths) *transfer.Manifest {
	t.Helper()
	srcCfg := filepath.Join(t.TempDir(), "home", "alice", ".claude")
	rel := "projects/-home-alice-work/" + sid + ".jsonl"
	os.MkdirAll(filepath.Dir(filepath.Join(srcCfg, rel)), 0o700)
	os.WriteFile(filepath.Join(srcCfg, rel), []byte(`{"cwd":"/home/alice/work","sessionId":"`+sid+`"}`+"\n"), 0o600)
	files := []session.FileEntry{{Root: srcCfg, Rel: rel, Category: session.CatSession, Mode: 0o600, ModTime: time.Now(), Rewrite: true}}
	pm := session.NewPathMap(session.Mapping{From: filepath.Dir(srcCfg), To: p.Home})
	m, err := transfer.Build(context.Background(), sid, session.ID(sid), "laptop.example", "big-storage.example", files, pm)
	if err != nil {
		t.Fatal(err)
	}
	m.TmpDir = t.TempDir()
	return m
}

func TestLocalHelloAndPaths(t *testing.T) {
	p := testPaths(t)
	l := NewLocal(p, "/usr/local/bin/claude-teleport", LocalOptions{ProcRoot: "/proc", Logf: t.Logf})
	info, err := l.Hello(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Protocol != version.Protocol || info.Version != version.Version || info.Home != p.Home || info.ConfigDir != p.ConfigDir || info.DataDir != p.DataDir || info.Hostname == "" || info.OS == "" || info.Arch == "" {
		t.Errorf("Hello = %+v", info)
	}
	if l.Paths() != p {
		t.Errorf("Paths = %+v", l.Paths())
	}
}

func TestLocalManifestDiffStreamInstall(t *testing.T) {
	p := testPaths(t)
	l := NewLocal(p, "self", LocalOptions{Logf: t.Logf})
	m := sourceManifest(t, p)
	ctx := context.Background()

	st, err := l.ManifestDiff(ctx, m, sid)
	if err != nil {
		t.Fatal(err)
	}
	if st[0] != transfer.Absent {
		t.Fatalf("status = %v", st)
	}
	if _, err := os.Stat(filepath.Join(job.Dir(p.DataDir, sid), "manifest.json")); err != nil {
		t.Errorf("ManifestDiff must persist the manifest on the destination: %v", err)
	}

	var buf bytes.Buffer
	if err := transfer.Send(ctx, m, transfer.Need(m, st), &buf, nil); err != nil {
		t.Fatal(err)
	}
	s, err := l.OpenStream(ctx, StreamTar, sid, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(s, &buf); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close must return the receive result: %v", err)
	}
	st, _ = l.ManifestDiff(ctx, m, sid)
	if st[0] != transfer.StagedSame {
		t.Fatalf("after stream: %v", st)
	}

	if err := l.PutInstallExtras(ctx, sid, transfer.InstallExtras{ProjectCwd: "/home/bob/work", ProjectEntry: session.ProjectEntry{"hasTrustDialogAccepted": true}}); err != nil {
		t.Fatal(err)
	}
	rep, err := l.Install(ctx, m, sid)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Installed != 1 || !rep.ProjectEntryAdded {
		t.Errorf("report = %+v", rep)
	}
	got, _ := os.ReadFile(m.Entries[0].Dst)
	if !bytes.Contains(got, []byte(`"cwd":"`+p.Home+`/work"`)) {
		t.Errorf("installed transcript not rewritten: %s", got)
	}

	// a corrupt tar stream: Close reports it and nothing is installed twice
	s2, _ := l.OpenStream(ctx, StreamTar, sid, "s2")
	io.WriteString(s2, "definitely not gzip")
	if err := s2.Close(); err == nil {
		t.Errorf("corrupt stream must fail on Close")
	}
}

func TestLocalCaptureAndLogStreams(t *testing.T) {
	p := testPaths(t)
	l := NewLocal(p, "self", LocalOptions{Logf: t.Logf})
	j, _ := job.New(p.DataDir, sid)
	os.WriteFile(j.CapturePath(), []byte("pane contents\n"), 0o600)
	os.WriteFile(j.LogPath(), []byte("log line\n"), 0o600)
	for kind, want := range map[StreamKind]string{StreamCapture: "pane contents\n", StreamLog: "log line\n"} {
		s, err := l.OpenStream(context.Background(), kind, sid, "x")
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(s)
		s.Close()
		if string(data) != want {
			t.Errorf("%s stream = %q", kind, data)
		}
	}
	if _, err := l.OpenStream(context.Background(), StreamPack, sid, "x"); !isUnavailable(err) {
		t.Errorf("pack stream err = %v, want unavailable", err)
	}
	if _, err := l.OpenStream(context.Background(), StreamCapture, "no-such-job", "x"); err == nil {
		t.Errorf("missing capture must be an error")
	}
}

func TestLocalJournalOps(t *testing.T) {
	p := testPaths(t)
	l := NewLocal(p, "self", LocalOptions{Logf: t.Logf})
	ctx := context.Background()
	if _, found, err := l.JournalGet(ctx, sid); err != nil || found {
		t.Fatalf("JournalGet before put: found=%v err=%v", found, err)
	}
	j := &job.Journal{ID: sid, SessionID: sid, Direction: "to", SourceHost: "laptop.example", DestHost: "big-storage.example"}
	j.Step("preflight").Status = job.Done
	if err := l.JournalPut(ctx, j); err != nil {
		t.Fatal(err)
	}
	got, found, err := l.JournalGet(ctx, sid)
	if err != nil || !found || got.Step("preflight").Status != job.Done || got.Dir != job.Dir(p.DataDir, sid) {
		t.Errorf("JournalGet: %+v found=%v err=%v", got, found, err)
	}
	if err := l.Record(ctx, sid, job.HistoryRecord{At: time.Now(), SessionID: sid, Outcome: "success"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(job.Dir(p.DataDir, sid), "history.jsonl")); err != nil {
		t.Errorf("history not written: %v", err)
	}
}

func TestLocalPlan03OpsAreExplicitStubs(t *testing.T) {
	l := NewLocal(testPaths(t), "self", LocalOptions{Logf: t.Logf})
	ctx := context.Background()
	checks := map[string]error{}
	_, checks["InventoryGit"] = l.InventoryGit(ctx, "/home/bob/work")
	_, checks["GitDestState"] = l.GitDestState(ctx, "/m", "/w", "main")
	_, checks["InventoryTmux"] = l.InventoryTmux(ctx, nil, "")
	checks["GitAttach"] = l.GitAttach(ctx, nil, sid)
	checks["Capture"] = l.Capture(ctx, nil, sid)
	_, checks["OpenWindow"] = l.OpenWindow(ctx, nil)
	checks["StartClaude"] = l.StartClaude(ctx, nil, session.ID(sid), sid, nil)
	_, checks["ConfirmClaude"] = l.ConfirmClaude(ctx, nil, session.ID(sid), time.Second)
	checks["ExitClaude"] = l.ExitClaude(ctx, nil, 1, "0", time.Second)
	checks["TypeCommand"] = l.TypeCommand(ctx, nil, nil)
	_, checks["PaneState"] = l.PaneState(ctx, nil)
	checks["RunPtyResume"] = l.RunPtyResume(ctx, session.ID(sid), "/home/bob/work", time.Second)
	for name, err := range checks {
		if !isUnavailable(err) {
			t.Errorf("%s: err = %v, want Error{Code: unavailable}", name, err)
		}
	}
}

func isUnavailable(err error) bool {
	var pe *Error
	return errors.As(err, &pe) && pe.Code == "unavailable"
}

func TestLocalResolveNotFoundMapsToProtocolError(t *testing.T) {
	l := NewLocal(testPaths(t), "self", LocalOptions{Logf: t.Logf})
	_, err := l.ResolveSession(context.Background(), session.Selector{ID: session.ID(sid)})
	if !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound (Serve maps it to not-found)", err)
	}
	if code := toError(err).Code; code != "not-found" {
		t.Errorf("toError code = %s", code)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/remote/ -run TestLocal -v`
Expected: FAIL — undefined `NewLocal`, `LocalOptions`.

- [ ] **Step 3: Implement**

`internal/remote/local.go`:
```go
package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
	"github.com/mithro/go-claude-teleport/internal/version"
)

type LocalOptions struct {
	ProcRoot string // "/proc"
	Probe    session.PaneProbe
	Tmux     TmuxDialer // Plan 03; nil = tmux unavailable
	Logf     func(string, ...any)
}

// Local is the in-process implementation used on whichever side is local
// and by Server.
type Local struct {
	paths    session.Paths
	selfExe  string
	opts     LocalOptions
	Hostname string

	mu       sync.Mutex
	freezers map[int]*procx.Freezer
}

func NewLocal(p session.Paths, selfExe string, opts LocalOptions) *Local {
	if opts.ProcRoot == "" {
		opts.ProcRoot = "/proc"
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	host, _ := os.Hostname()
	return &Local{paths: p, selfExe: selfExe, opts: opts, Hostname: host, freezers: map[int]*procx.Freezer{}}
}

var _ Endpoint = (*Local)(nil)

func (l *Local) Hello(ctx context.Context) (HostInfo, error) {
	info := HostInfo{
		Version: version.Version, Protocol: version.Protocol, Hostname: l.Hostname,
		OS: runtime.GOOS, Arch: runtime.GOARCH, UID: os.Getuid(),
		Home: l.paths.Home, ConfigDir: l.paths.ConfigDir, DataDir: l.paths.DataDir,
	}
	if dir := os.Getenv("TMUX_TMPDIR"); dir != "" {
		info.TmuxSocketDir = filepath.Join(dir, fmt.Sprintf("tmux-%d", info.UID))
	} else {
		info.TmuxSocketDir = filepath.Join(os.TempDir(), fmt.Sprintf("tmux-%d", info.UID))
	}
	_, err := exec.LookPath("tmux")
	info.HasTmux = err == nil
	_, err = exec.LookPath("claude-resume")
	info.HasClaudeResume = err == nil
	if claudePath, err := exec.LookPath("claude"); err == nil {
		info.HasClaude = true
		cmd := exec.CommandContext(ctx, claudePath, "--version")
		if out, err := cmd.Output(); err == nil {
			info.ClaudeVersion = firstLine(string(out))
		}
	}
	return info, nil
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	return s
}

func (l *Local) Paths() session.Paths { return l.paths }

func (l *Local) ResolveSession(ctx context.Context, sel session.Selector) (*session.Session, error) {
	return session.Resolve(l.paths, sel, l.opts.Probe)
}

func (l *Local) InventorySession(ctx context.Context, id session.ID) (*session.Inventory, *session.Usage, error) {
	s, err := session.Load(l.paths, id, l.opts.Probe)
	if err != nil {
		return nil, nil, err
	}
	inv, err := session.InventoryFiles(s)
	if err != nil {
		return nil, nil, err
	}
	usage, err := session.ScanUsage(s)
	if err != nil {
		return nil, nil, err
	}
	return inv, usage, nil
}

func (l *Local) InventoryHost(ctx context.Context, cwd, claudeVersion string) (*claudecfg.Inventory, error) {
	return claudecfg.Collect(l.paths, cwd, l.Hostname, claudeVersion)
}

func (l *Local) InventoryGit(ctx context.Context, cwd string) (*GitInfo, error) {
	return nil, Unavailable(OpInventoryGit)
}
func (l *Local) GitDestState(ctx context.Context, mainDir, worktreeDir, branch string) (*GitDestState, error) {
	return nil, Unavailable(OpGitDestState)
}
func (l *Local) InventoryTmux(ctx context.Context, ref *session.TmuxRef, preferredSocket string) (*TmuxFacts, error) {
	return nil, Unavailable(OpInventoryTmux)
}

func (l *Local) jobDir(jobID string) string     { return job.Dir(l.paths.DataDir, jobID) }
func (l *Local) stagingDir(jobID string) string { return job.StagingDir(l.paths.DataDir, jobID) }
func (l *Local) extrasPath(jobID string) string { return filepath.Join(l.jobDir(jobID), "extras.json") }

// ManifestDiff persists the manifest under jobs/<id>/ (the tar stream
// receiver needs it) and classifies every entry.
func (l *Local) ManifestDiff(ctx context.Context, m *transfer.Manifest, jobID string) (map[int]transfer.Status, error) {
	if m == nil {
		return nil, &Error{Code: "usage", Message: "manifest-diff: nil manifest"}
	}
	if err := os.MkdirAll(l.jobDir(jobID), 0o700); err != nil {
		return nil, err
	}
	if err := m.Save(filepath.Join(l.jobDir(jobID), "manifest.json")); err != nil {
		return nil, err
	}
	return transfer.Diff(ctx, m, l.stagingDir(jobID))
}

// PutInstallExtras stores the merge inputs for Install under jobs/<id>/extras.json.
func (l *Local) PutInstallExtras(ctx context.Context, jobID string, extra transfer.InstallExtras) error {
	raw, err := json.Marshal(extra)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(l.jobDir(jobID), 0o700); err != nil {
		return err
	}
	return os.WriteFile(l.extrasPath(jobID), raw, 0o600)
}

// tarStream is the write side of a receive: bytes written go to Receive;
// Close waits for it and returns its verdict.
type tarStream struct {
	pw   *io.PipeWriter
	done chan error
	once sync.Once
	err  error
}

func (t *tarStream) Read(p []byte) (int, error)  { return 0, io.EOF }
func (t *tarStream) Write(p []byte) (int, error) { return t.pw.Write(p) }
func (t *tarStream) Close() error {
	t.once.Do(func() {
		t.pw.Close()
		t.err = <-t.done
	})
	return t.err
}

type readStream struct{ io.ReadCloser }

func (r readStream) Write(p []byte) (int, error) { return 0, errors.New("read-only stream") }

func (l *Local) OpenStream(ctx context.Context, kind StreamKind, jobID, streamID string) (io.ReadWriteCloser, error) {
	switch kind {
	case StreamTar:
		m, err := transfer.Load(filepath.Join(l.jobDir(jobID), "manifest.json"))
		if err != nil {
			return nil, fmt.Errorf("stream tar %s: manifest-diff must run first: %w", jobID, err)
		}
		pr, pw := io.Pipe()
		t := &tarStream{pw: pw, done: make(chan error, 1)}
		go func() {
			err := transfer.Receive(ctx, m, pr, l.stagingDir(jobID), func(e transfer.Entry, n int64) {
				l.opts.Logf("received entry %d %s (%d bytes total)", e.ID, e.Dst, n)
			})
			pr.CloseWithError(err)
			t.done <- err
		}()
		return t, nil
	case StreamCapture:
		f, err := os.Open(filepath.Join(l.jobDir(jobID), "capture.txt"))
		if err != nil {
			return nil, fmt.Errorf("stream capture %s: %w", jobID, err)
		}
		return readStream{f}, nil
	case StreamLog:
		f, err := os.Open(filepath.Join(l.jobDir(jobID), "log.txt"))
		if err != nil {
			return nil, fmt.Errorf("stream log %s: %w", jobID, err)
		}
		return readStream{f}, nil
	case StreamPack:
		return nil, Unavailable("stream pack")
	}
	return nil, &Error{Code: "usage", Message: "unknown stream kind " + string(kind)}
}

func (l *Local) Install(ctx context.Context, m *transfer.Manifest, jobID string) (*transfer.InstallReport, error) {
	st, err := transfer.Diff(ctx, m, l.stagingDir(jobID))
	if err != nil {
		return nil, err
	}
	var extra transfer.InstallExtras
	raw, err := os.ReadFile(l.extrasPath(jobID))
	if err == nil {
		if err := json.Unmarshal(raw, &extra); err != nil {
			return nil, fmt.Errorf("install %s: extras.json: %w", jobID, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return transfer.Install(ctx, m, st, l.stagingDir(jobID), l.paths, extra)
}

func (l *Local) GitAttach(ctx context.Context, plan *GitPlan, jobID string) error {
	return Unavailable(OpGitAttach)
}

func (l *Local) Freeze(ctx context.Context, pid int, startTime string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.freezers[pid]; ok {
		return nil
	}
	f, err := procx.Freeze(l.selfExe, pid, startTime)
	if err != nil {
		return err
	}
	l.freezers[pid] = f
	return nil
}

func (l *Local) Thaw(ctx context.Context, pid int) error {
	l.mu.Lock()
	f, ok := l.freezers[pid]
	delete(l.freezers, pid)
	l.mu.Unlock()
	if !ok {
		return nil // not frozen by us: no-op (spec §6 step 9)
	}
	return f.Thaw()
}

func (l *Local) Capture(ctx context.Context, ref *session.TmuxRef, jobID string) error {
	return Unavailable(OpCapture)
}
func (l *Local) OpenWindow(ctx context.Context, p *TmuxPlan) (*session.TmuxRef, error) {
	return nil, Unavailable(OpOpenWindow)
}
func (l *Local) StartClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, jobID string, argv []string) error {
	return Unavailable(OpStartClaude)
}
func (l *Local) ConfirmClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, timeout time.Duration) (*session.Registry, error) {
	return nil, Unavailable(OpConfirmClaude)
}
func (l *Local) ExitClaude(ctx context.Context, ref *session.TmuxRef, pid int, startTime string, timeout time.Duration) error {
	return Unavailable(OpExitClaude)
}
func (l *Local) TypeCommand(ctx context.Context, ref *session.TmuxRef, argv []string) error {
	return Unavailable(OpTypeCommand)
}
func (l *Local) PaneState(ctx context.Context, ref *session.TmuxRef) (*TmuxPaneState, error) {
	return nil, Unavailable(OpPaneState)
}
func (l *Local) RunPtyResume(ctx context.Context, id session.ID, cwd string, timeout time.Duration) error {
	return Unavailable(OpRunPtyResume)
}

func (l *Local) JournalGet(ctx context.Context, jobID string) (*job.Journal, bool, error) {
	return job.Open(l.paths.DataDir, jobID)
}

func (l *Local) JournalPut(ctx context.Context, j *job.Journal) error {
	if j == nil {
		return &Error{Code: "usage", Message: "journal-put: nil journal"}
	}
	if err := os.MkdirAll(l.jobDir(j.ID), 0o700); err != nil {
		return err
	}
	j.Dir = l.jobDir(j.ID)
	return j.Save()
}

func (l *Local) Record(ctx context.Context, jobID string, rec job.HistoryRecord) error {
	return job.AppendHistory(l.jobDir(jobID), rec)
}
```
`Thaw` on a pid that this `Local` did not freeze is a no-op by design (spec: "thaw no-op if not stopped"); the freezer helper itself guarantees SIGCONT on runner death.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/remote/ -v`
Expected: PASS. `TestLocalResolveNotFoundMapsToProtocolError` depends on Plan 01's `session.Resolve` returning `ErrNotFound` for an unknown full uuid with an empty projects tree.

- [ ] **Step 5: Commit**

```bash
git add internal/remote/local.go internal/remote/local_test.go
git commit -m "feat(remote): Local endpoint with manifest diff, tar/capture/log streams, install, freeze, journal ops"
```

---

### Task 16: `remote.Client` — multiplexed calls over ssh, `OpenStream`, tests over `net.Pipe` and `sshtest`

**Files:**
- Create: `internal/remote/client.go`
- Test: `internal/remote/client_test.go`

**Interfaces:**
- Consumes: `sshx.Client` (`Start`, `Quote`, `SSH()`), `sshtest`, Tasks 14–15.
- Produces (verbatim): `NewClient(ctx, ssh *sshx.Client, remoteExe string, logf) (*Client, error)`, `(*Client).Close() error`; `Client` implements `Endpoint`; additions `NewClientConn(ctx, conn io.ReadWriteCloser, openStream func(ctx, kind, jobID, streamID string) (io.ReadWriteCloser, error), logf) (*Client, error)` (transport-agnostic constructor used by `NewClient` and by the `net.Pipe` test), `(*Client).PutInstallExtras`, `(*Client).Info() HostInfo` (the Hello result).

Design: one writer goroutine-safe `encode` under a mutex; one reader goroutine parses response lines and delivers them to `map[int]chan Response` by id; `call(ctx, op, args, result)` blocks on its channel or ctx. Ids start at 1 and increase. The remote `stderr` is copied line-by-line to `logf("remote: %s")`. `Close` closes stdin (the server exits at EOF), waits for the process, closes pending calls with an error. `OpenStream` runs `Quote([]string{remoteExe, "remote", "stream", kind, jobID, streamID})` in a **new ssh session** and returns an `io.ReadWriteCloser` whose `Write` goes to the session stdin, `Read` from its stdout, and `Close` closes stdin, then waits for exit: a non-zero exit (the remote `ServeStream` failed, e.g. a verification error) becomes the `Close` error with the remote stderr text.

- [ ] **Step 1: Write the failing test**

`internal/remote/client_test.go`:
```go
package remote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/sshx"
	"github.com/mithro/go-claude-teleport/internal/sshx/sshtest"
	"github.com/mithro/go-claude-teleport/internal/transfer"
	"github.com/mithro/go-claude-teleport/internal/version"
)

// pipeClient wires a Client to a Local through net.Pipe; streams are opened
// directly on the Local.
func pipeClient(t *testing.T, l *Local) *Client {
	t.Helper()
	a, b := net.Pipe()
	go func() { Serve(context.Background(), b, b, l); b.Close() }()
	c, err := NewClientConn(context.Background(), a, l.OpenStream, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestClientHelloAndCallsOverPipe(t *testing.T) {
	p := testPaths(t)
	l := NewLocal(p, "self", LocalOptions{Logf: t.Logf})
	c := pipeClient(t, l)
	if c.Info().Protocol != version.Protocol || c.Info().ConfigDir != p.ConfigDir {
		t.Errorf("Info = %+v", c.Info())
	}
	if c.Paths() != p {
		t.Errorf("Paths = %+v", c.Paths())
	}
	ctx := context.Background()
	if _, err := c.InventoryGit(ctx, "/x"); !isUnavailable(err) {
		t.Errorf("stub error must cross the wire: %v", err)
	}
	if _, err := c.ResolveSession(ctx, session.Selector{ID: session.ID(sid)}); err == nil {
		t.Errorf("expected not-found")
	} else if pe := new(Error); !errors.As(err, &pe) || pe.Code != "not-found" {
		t.Errorf("err = %v", err)
	}

	// journal round trip
	j := &job.Journal{ID: sid, SessionID: sid, Direction: "to"}
	j.Step("preflight").Status = job.Done
	if err := c.JournalPut(ctx, j); err != nil {
		t.Fatal(err)
	}
	got, found, err := c.JournalGet(ctx, sid)
	if err != nil || !found || got.Step("preflight").Status != job.Done {
		t.Errorf("journal: %+v %v %v", got, found, err)
	}

	// concurrent calls are multiplexed by id
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Hello(ctx); err != nil {
				t.Errorf("concurrent hello: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestClientProtocolMismatchIsUsageError(t *testing.T) {
	a, b := net.Pipe()
	ep := stubEndpoint{hello: func() (HostInfo, error) { return HostInfo{Protocol: version.Protocol + 1, Version: "v9.9"}, nil }}
	go func() { Serve(context.Background(), b, b, ep); b.Close() }()
	_, err := NewClientConn(context.Background(), a, nil, t.Logf)
	if err == nil || !strings.Contains(err.Error(), "protocol mismatch") || !strings.Contains(err.Error(), "v9.9") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientTransferOverSSHTest(t *testing.T) {
	// "dest" host: a Local behind an in-process sshd whose exec handler runs
	// `<exe> remote serve` and `<exe> remote stream ...` in-process.
	destPaths := testPaths(t)
	dest := NewLocal(destPaths, "claude-teleport", LocalOptions{Logf: t.Logf})
	exec := func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
		args := strings.Fields(cmd)
		switch {
		case len(args) == 3 && args[1] == "remote" && args[2] == "serve":
			if err := Serve(context.Background(), stdin, stdout, dest); err != nil {
				io.WriteString(stderr, err.Error())
				return 1
			}
			return 0
		case len(args) == 6 && args[1] == "remote" && args[2] == "stream":
			if err := ServeStream(context.Background(), StreamKind(args[3]), args[4], args[5], stdin, stdout, dest); err != nil {
				io.WriteString(stderr, err.Error())
				return 1
			}
			return 0
		}
		io.WriteString(stderr, "unexpected command "+cmd)
		return 127
	}
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	_, signer := sshtest.WriteKeyFile(t, filepath.Join(home, ".ssh"), "id_ed25519", "")
	srv := sshtest.New(t, sshtest.Options{Authorized: []ssh.PublicKey{signer.PublicKey()}, Exec: exec})
	host, portStr, _ := net.SplitHostPort(srv.Addr)
	port := 0
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}
	kh := filepath.Join(home, ".ssh", "known_hosts")
	os.WriteFile(kh, []byte(sshtest.KnownHostsLine("["+host+"]:"+portStr, srv.HostKey)), 0o600)
	sc, err := sshx.Dial(context.Background(), sshx.Resolved{Target: sshx.Target{User: "bob", Host: host, Port: port}, HostName: host}, nil, nil,
		sshx.Options{KnownHostsFile: kh, Home: home, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	c, err := NewClient(context.Background(), sc, "claude-teleport", t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Info().Hostname == "" {
		t.Errorf("hello over ssh: %+v", c.Info())
	}

	ctx := context.Background()
	m := sourceManifest(t, destPaths)
	st, err := c.ManifestDiff(ctx, m, sid)
	if err != nil {
		t.Fatal(err)
	}
	if st[0] != transfer.Absent {
		t.Fatalf("diff = %v", st)
	}
	s, err := c.OpenStream(ctx, StreamTar, sid, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := transfer.Send(ctx, m, transfer.Need(m, st), s, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("stream close: %v", err)
	}
	st, _ = c.ManifestDiff(ctx, m, sid)
	if st[0] != transfer.StagedSame {
		t.Fatalf("after stream: %v", st)
	}
	rep, err := c.Install(ctx, m, sid)
	if err != nil || rep.Installed != 1 {
		t.Fatalf("install: %+v %v", rep, err)
	}
	if _, err := os.Stat(m.Entries[0].Dst); err != nil {
		t.Errorf("not installed on dest: %v", err)
	}

	// a failing stream surfaces on Close with the remote stderr
	bad, _ := c.OpenStream(ctx, StreamTar, sid, "s2")
	io.WriteString(bad, "garbage")
	if err := bad.Close(); err == nil || !strings.Contains(err.Error(), "gzip") {
		t.Errorf("bad stream Close = %v, want remote gzip error", err)
	}

	// log stream reads the remote job log
	os.WriteFile(filepath.Join(job.Dir(destPaths.DataDir, sid), "log.txt"), []byte("remote log\n"), 0o600)
	lg, err := c.OpenStream(ctx, StreamLog, sid, "l")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	io.Copy(&buf, lg)
	lg.Close()
	if buf.String() != "remote log\n" {
		t.Errorf("log stream = %q", buf.String())
	}

	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if _, err := c.Hello(ctx); err == nil {
		t.Errorf("calls after Close must fail")
	}
	_ = time.Second
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/remote/ -run TestClient -v`
Expected: FAIL — undefined `NewClientConn`, `NewClient`.

- [ ] **Step 3: Implement**

`internal/remote/client.go`:
```go
package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/sshx"
	"github.com/mithro/go-claude-teleport/internal/transfer"
	"github.com/mithro/go-claude-teleport/internal/version"
)

// Client implements Endpoint over a request/response line connection.
type Client struct {
	conn       io.ReadWriteCloser
	openStream func(ctx context.Context, kind StreamKind, jobID, streamID string) (io.ReadWriteCloser, error)
	logf       func(string, ...any)
	wait       func() error // remote process wait (nil for raw conns)

	wmu     sync.Mutex
	mu      sync.Mutex
	nextID  int
	pending map[int]chan Response
	closed  bool
	readErr error

	info  HostInfo
	paths session.Paths
}

var _ Endpoint = (*Client)(nil)

// NewClientConn builds a Client on any line connection, performs hello and
// paths. openStream may be nil when streams are not needed (tests).
func NewClientConn(ctx context.Context, conn io.ReadWriteCloser, openStream func(ctx context.Context, kind StreamKind, jobID, streamID string) (io.ReadWriteCloser, error), logf func(string, ...any)) (*Client, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	c := &Client{conn: conn, openStream: openStream, logf: logf, pending: map[int]chan Response{}}
	go c.readLoop()
	if err := c.call(ctx, OpHello, HelloArgs{Version: version.Version, Protocol: version.Protocol}, &c.info); err != nil {
		c.Close()
		return nil, fmt.Errorf("hello: %w", err)
	}
	var pr PathsResult
	if err := c.call(ctx, OpPaths, struct{}{}, &pr); err != nil {
		c.Close()
		return nil, fmt.Errorf("paths: %w", err)
	}
	c.paths = pr.Paths
	return c, nil
}

// sshConn adapts an sshx.Process to io.ReadWriteCloser.
type sshConn struct {
	p *sshx.Process
}

func (s sshConn) Read(b []byte) (int, error)  { return s.p.Stdout.Read(b) }
func (s sshConn) Write(b []byte) (int, error) { return s.p.Stdin.Write(b) }
func (s sshConn) Close() error                { s.p.Stdin.Close(); return s.p.Close() }

// NewClient runs `<exe> remote serve` over ssh and performs hello.
func NewClient(ctx context.Context, ssh *sshx.Client, remoteExe string, logf func(string, ...any)) (*Client, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	p, err := ssh.Start(ctx, sshx.Quote([]string{remoteExe, "remote", "serve"}))
	if err != nil {
		return nil, fmt.Errorf("start remote helper on %s: %w", ssh, err)
	}
	go func() {
		sc := bufio.NewScanner(p.Stderr)
		for sc.Scan() {
			logf("remote %s: %s", ssh, sc.Text())
		}
	}()
	openStream := func(ctx context.Context, kind StreamKind, jobID, streamID string) (io.ReadWriteCloser, error) {
		sp, err := ssh.Start(ctx, sshx.Quote([]string{remoteExe, "remote", "stream", string(kind), jobID, streamID}))
		if err != nil {
			return nil, fmt.Errorf("open stream %s/%s on %s: %w", kind, streamID, ssh, err)
		}
		return &sshStream{p: sp, stderr: &strings.Builder{}, kind: kind, id: streamID}, nil
	}
	c, err := NewClientConn(ctx, sshConn{p}, openStream, logf)
	if err != nil {
		p.Close()
		return nil, err
	}
	c.wait = p.Wait
	return c, nil
}

// sshStream is a bulk channel on its own ssh session.
type sshStream struct {
	p      *sshx.Process
	stderr *strings.Builder
	kind   StreamKind
	id     string
	once   sync.Once
	err    error
}

func (s *sshStream) Read(b []byte) (int, error)  { return s.p.Stdout.Read(b) }
func (s *sshStream) Write(b []byte) (int, error) { return s.p.Stdin.Write(b) }

// Close closes stdin (EOF to the remote ServeStream) and waits: the remote's
// exit status and stderr become the error. Idempotent.
func (s *sshStream) Close() error {
	s.once.Do(func() {
		s.p.Stdin.Close()
		io.Copy(s.stderr, s.p.Stderr)
		err := s.p.Wait()
		s.p.Close()
		if err != nil {
			s.err = fmt.Errorf("stream %s/%s: %w: %s", s.kind, s.id, err, strings.TrimSpace(s.stderr.String()))
		}
	})
	return s.err
}

func (c *Client) readLoop() {
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 1<<20), maxLine)
	for sc.Scan() {
		var resp Response
		if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
			c.logf("remote: bad response line: %v", err)
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		delete(c.pending, resp.ID)
		c.mu.Unlock()
		if ok {
			ch <- resp
		} else {
			c.logf("remote: response for unknown id %d", resp.ID)
		}
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	c.mu.Lock()
	c.readErr = err
	for id, ch := range c.pending {
		ch <- Response{ID: id, Error: &Error{Code: "internal", Message: "connection closed: " + err.Error()}}
		delete(c.pending, id)
	}
	c.mu.Unlock()
}

func (c *Client) call(ctx context.Context, op string, args any, result any) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("%s: encode args: %w", op, err)
	}
	c.mu.Lock()
	if c.closed || c.readErr != nil {
		c.mu.Unlock()
		return &Error{Code: "internal", Message: op + ": client closed"}
	}
	c.nextID++
	id := c.nextID
	ch := make(chan Response, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	line, _ := json.Marshal(Request{ID: id, Op: op, Args: raw})
	c.wmu.Lock()
	_, werr := c.conn.Write(append(line, '\n'))
	c.wmu.Unlock()
	if werr != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("%s: send: %w", op, werr)
	}
	select {
	case resp := <-ch:
		if !resp.OK {
			if resp.Error == nil {
				return &Error{Code: "internal", Message: op + ": no error in failed response"}
			}
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("%s: decode result: %w", op, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("%s: %w", op, ctx.Err())
	}
}

// Close ends the helper (EOF on its stdin) and fails pending calls.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	err := c.conn.Close()
	if c.wait != nil {
		if werr := c.wait(); werr != nil && !errors.Is(werr, io.EOF) {
			c.logf("remote helper exit: %v", werr)
		}
	}
	return err
}

// Info is the Hello result.
func (c *Client) Info() HostInfo { return c.info }

func (c *Client) Hello(ctx context.Context) (HostInfo, error) {
	var info HostInfo
	err := c.call(ctx, OpHello, HelloArgs{Version: version.Version, Protocol: version.Protocol}, &info)
	return info, err
}

func (c *Client) Paths() session.Paths { return c.paths }

func (c *Client) ResolveSession(ctx context.Context, sel session.Selector) (*session.Session, error) {
	var r ResolveSessionResult
	err := c.call(ctx, OpResolveSession, ResolveSessionArgs{Selector: sel}, &r)
	return r.Session, err
}

func (c *Client) InventorySession(ctx context.Context, id session.ID) (*session.Inventory, *session.Usage, error) {
	var r InventorySessionResult
	err := c.call(ctx, OpInventorySession, InventorySessionArgs{ID: id}, &r)
	return r.Inventory, r.Usage, err
}

func (c *Client) InventoryHost(ctx context.Context, cwd, claudeVersion string) (*claudecfg.Inventory, error) {
	var r InventoryHostResult
	err := c.call(ctx, OpInventoryHost, InventoryHostArgs{Cwd: cwd, ClaudeVersion: claudeVersion}, &r)
	return r.Inventory, err
}

func (c *Client) InventoryGit(ctx context.Context, cwd string) (*GitInfo, error) {
	var r InventoryGitResult
	err := c.call(ctx, OpInventoryGit, InventoryGitArgs{Cwd: cwd}, &r)
	return r.Info, err
}

func (c *Client) GitDestState(ctx context.Context, mainDir, worktreeDir, branch string) (*GitDestState, error) {
	var r GitDestStateResult
	err := c.call(ctx, OpGitDestState, GitDestStateArgs{MainDir: mainDir, WorktreeDir: worktreeDir, Branch: branch}, &r)
	return r.State, err
}

func (c *Client) InventoryTmux(ctx context.Context, ref *session.TmuxRef, preferredSocket string) (*TmuxFacts, error) {
	var r InventoryTmuxResult
	err := c.call(ctx, OpInventoryTmux, InventoryTmuxArgs{Ref: ref, PreferredSocket: preferredSocket}, &r)
	return r.Facts, err
}

func (c *Client) ManifestDiff(ctx context.Context, m *transfer.Manifest, jobID string) (map[int]transfer.Status, error) {
	var r ManifestDiffResult
	err := c.call(ctx, OpManifestDiff, ManifestDiffArgs{Manifest: m, JobID: jobID}, &r)
	return r.Statuses, err
}

func (c *Client) PutInstallExtras(ctx context.Context, jobID string, extra transfer.InstallExtras) error {
	return c.call(ctx, OpInstallExtras, InstallExtrasArgs{JobID: jobID, Extra: extra}, nil)
}

func (c *Client) OpenStream(ctx context.Context, kind StreamKind, jobID, streamID string) (io.ReadWriteCloser, error) {
	if c.openStream == nil {
		return nil, &Error{Code: "unavailable", Message: "streams not configured on this client"}
	}
	return c.openStream(ctx, kind, jobID, streamID)
}

func (c *Client) Install(ctx context.Context, m *transfer.Manifest, jobID string) (*transfer.InstallReport, error) {
	var r InstallResult
	err := c.call(ctx, OpInstall, InstallArgs{Manifest: m, JobID: jobID}, &r)
	return r.Report, err
}

func (c *Client) GitAttach(ctx context.Context, plan *GitPlan, jobID string) error {
	return c.call(ctx, OpGitAttach, GitAttachArgs{Plan: plan, JobID: jobID}, nil)
}

func (c *Client) Freeze(ctx context.Context, pid int, startTime string) error {
	return c.call(ctx, OpFreeze, FreezeArgs{PID: pid, StartTime: startTime}, nil)
}

func (c *Client) Thaw(ctx context.Context, pid int) error {
	return c.call(ctx, OpThaw, ThawArgs{PID: pid}, nil)
}

func (c *Client) Capture(ctx context.Context, ref *session.TmuxRef, jobID string) error {
	return c.call(ctx, OpCapture, CaptureArgs{Ref: ref, JobID: jobID}, nil)
}

func (c *Client) OpenWindow(ctx context.Context, p *TmuxPlan) (*session.TmuxRef, error) {
	var r OpenWindowResult
	err := c.call(ctx, OpOpenWindow, OpenWindowArgs{Plan: p}, &r)
	return r.Ref, err
}

func (c *Client) StartClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, jobID string, argv []string) error {
	return c.call(ctx, OpStartClaude, StartClaudeArgs{Ref: ref, ID: id, JobID: jobID, Argv: argv}, nil)
}

func (c *Client) ConfirmClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, timeout time.Duration) (*session.Registry, error) {
	var r ConfirmClaudeResult
	err := c.call(ctx, OpConfirmClaude, ConfirmClaudeArgs{Ref: ref, ID: id, Timeout: timeout}, &r)
	return r.Registry, err
}

func (c *Client) ExitClaude(ctx context.Context, ref *session.TmuxRef, pid int, startTime string, timeout time.Duration) error {
	return c.call(ctx, OpExitClaude, ExitClaudeArgs{Ref: ref, PID: pid, StartTime: startTime, Timeout: timeout}, nil)
}

func (c *Client) TypeCommand(ctx context.Context, ref *session.TmuxRef, argv []string) error {
	return c.call(ctx, OpTypeCommand, TypeCommandArgs{Ref: ref, Argv: argv}, nil)
}

func (c *Client) PaneState(ctx context.Context, ref *session.TmuxRef) (*TmuxPaneState, error) {
	var r PaneStateResult
	err := c.call(ctx, OpPaneState, PaneStateArgs{Ref: ref}, &r)
	return r.State, err
}

func (c *Client) RunPtyResume(ctx context.Context, id session.ID, cwd string, timeout time.Duration) error {
	return c.call(ctx, OpRunPtyResume, RunPtyResumeArgs{ID: id, Cwd: cwd, Timeout: timeout}, nil)
}

func (c *Client) JournalGet(ctx context.Context, jobID string) (*job.Journal, bool, error) {
	var r JournalGetResult
	err := c.call(ctx, OpJournalGet, JournalGetArgs{JobID: jobID}, &r)
	if r.Journal != nil {
		r.Journal.Dir = job.Dir(c.paths.DataDir, jobID)
	}
	return r.Journal, r.Found, err
}

func (c *Client) JournalPut(ctx context.Context, j *job.Journal) error {
	return c.call(ctx, OpJournalPut, JournalPutArgs{Journal: j}, nil)
}

func (c *Client) Record(ctx context.Context, jobID string, rec job.HistoryRecord) error {
	return c.call(ctx, OpRecord, RecordArgs{JobID: jobID, Record: rec}, nil)
}
```
Ordering note for `sshStream.Close`: draining stderr before `Wait` is safe because the remote `ServeStream` writes at most one error line; the tar stream's stdout is never read by the driver (the remote writes nothing to it for `tar`), so no goroutine is needed there. For `capture`/`log`/`pack` the driver reads stdout to EOF before calling `Close`.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/remote/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/remote/client.go internal/remote/client_test.go
git commit -m "feat(remote): Client with multiplexed calls and per-stream ssh sessions; tests over net.Pipe and sshtest"
```

---

### Task 17: `fakeapi` — canned Anthropic Messages API server (spike facts pinned)

**Files:**
- Create: `internal/fakeapi/ENDPOINTS.md`
- Create: `internal/fakeapi/server.go`
- Create: `internal/fakeapi/messages.go`
- Test: `internal/fakeapi/server_test.go`

**Interfaces:**
- Consumes: stdlib `net/http` only.
- Produces (verbatim): `Server`, `Options{Reply, Model, LogDir string}`, `New(o Options) *Server`, `(*Server).Handler() http.Handler`, `(*Server).Requests() []Request`, `Request{Path string; Body []byte; At time.Time}`.

**Spike — already performed, results are facts to encode.** The endpoint-discovery spike (spec §12) was run against the real Claude Code **2.1.251** with exactly the environment the realclaude test below uses. Write its findings into `internal/fakeapi/ENDPOINTS.md` verbatim as follows, and design the handlers from them:

- [ ] **Step 1: Write ENDPOINTS.md**

`internal/fakeapi/ENDPOINTS.md`:
```markdown
# Endpoints Claude Code requests at startup (observed)

Observed on 2026-08-29 with Claude Code 2.1.251, running

    claude -p --session-id <uuid> "say hello"

with this environment (and CLAUDECODE, CLAUDE_PID, CLAUDE_CODE_SESSION_ID and
every CLAUDE_CODE_MESSAGING_* variable UNSET):

    ANTHROPIC_BASE_URL=http://127.0.0.1:<port>
    ANTHROPIC_API_KEY=dummy-key
    CLAUDE_CONFIG_DIR=<fresh temp dir>
    DISABLE_AUTOUPDATER=1
    DISABLE_TELEMETRY=1
    DISABLE_ERROR_REPORTING=1
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1

Result: EXACTLY ONE HTTP request was made.

| Method | Path | Body (shape) |
|---|---|---|
| POST | `/v1/messages?beta=true` | `{model:"claude-opus-5", stream:true, system:[...], tools:[21 tools], messages:[2]}` |

Not requested: `/api/hello`, `/v1/models`, `/v1/messages/count_tokens`.

A streaming SSE reply of
`message_start → content_block_start(text) → content_block_delta(text_delta
"Hello from the canned server.") → content_block_stop → message_delta(stop_reason
end_turn, usage) → message_stop`
made claude print the text and exit 0 in about 3 s, writing
`<CLAUDE_CONFIG_DIR>/projects/<munged cwd>/<sid>.jsonl` (10.6 KB).

Then `claude -p --resume <sid> "what did I say?"` also exited 0; its
`/v1/messages` request carried 5 messages whose body contained the original
"say hello" — the prior-context assertion the integration test makes.

Consequences for this package:

- `/v1/messages` must match on path PREFIX (the `?beta=true` query is ignored).
- `count_tokens`, `/v1/models` and `/api/hello` are kept (cheap; other code
  paths may hit them) but are not required for `-p` runs.
- Every request body is recorded so tests can assert prior-context inclusion.
- Non-streaming requests (`stream` absent/false) get a plain JSON message.
```

- [ ] **Step 2: Write the failing test**

`internal/fakeapi/server_test.go`:
```go
package fakeapi

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServer(t *testing.T, o Options) (*Server, *httptest.Server) {
	t.Helper()
	s := New(o)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func TestMessagesStreaming(t *testing.T) {
	s, ts := newTestServer(t, Options{Reply: "Hello from the canned server.", Model: "claude-opus-5"})
	body := `{"model":"claude-opus-5","stream":true,"messages":[{"role":"user","content":"say hello"}]}`
	resp, err := http.Post(ts.URL+"/v1/messages?beta=true", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d content-type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var events []string
	var text strings.Builder
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}
		if strings.HasPrefix(line, "data: ") {
			var d struct {
				Type  string `json:"type"`
				Delta struct {
					Type       string `json:"type"`
					Text       string `json:"text"`
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Message struct {
					Model string `json:"model"`
					Role  string `json:"role"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d); err != nil {
				t.Fatalf("bad data line %q: %v", line, err)
			}
			if d.Type == "content_block_delta" {
				text.WriteString(d.Delta.Text)
			}
			if d.Type == "message_start" && (d.Message.Model != "claude-opus-5" || d.Message.Role != "assistant") {
				t.Errorf("message_start = %+v", d.Message)
			}
			if d.Type == "message_delta" && d.Delta.StopReason != "end_turn" {
				t.Errorf("message_delta stop_reason = %q", d.Delta.StopReason)
			}
		}
	}
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Errorf("events = %v, want %v", events, want)
	}
	if text.String() != "Hello from the canned server." {
		t.Errorf("text = %q", text.String())
	}
	reqs := s.Requests()
	if len(reqs) != 1 || reqs[0].Path != "/v1/messages" || !strings.Contains(string(reqs[0].Body), "say hello") || reqs[0].At.IsZero() {
		t.Errorf("recorded = %+v", reqs)
	}
}

func TestMessagesNonStreaming(t *testing.T) {
	_, ts := newTestServer(t, Options{Reply: "ok", Model: "claude-opus-5"})
	resp, err := http.Post(ts.URL+"/v1/messages", "application/json", strings.NewReader(`{"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m.Type != "message" || m.Role != "assistant" || m.Model != "claude-opus-5" || len(m.Content) != 1 || m.Content[0].Text != "ok" || m.StopReason != "end_turn" || !strings.HasPrefix(m.ID, "msg_") || m.Usage.OutputTokens == 0 {
		t.Errorf("message = %+v", m)
	}
}

func TestOtherEndpoints(t *testing.T) {
	_, ts := newTestServer(t, Options{Reply: "ok", Model: "claude-opus-5"})
	get := func(path string) (int, string) {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("/v1/models"); code != 200 || !strings.Contains(body, `"id":"claude-opus-5"`) {
		t.Errorf("/v1/models: %d %s", code, body)
	}
	if code, body := get("/api/hello"); code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Errorf("/api/hello: %d %s", code, body)
	}
	resp, _ := http.Post(ts.URL+"/v1/messages/count_tokens", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"hi there"}]}`))
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"input_tokens":`) {
		t.Errorf("count_tokens: %d %s", resp.StatusCode, b)
	}
	if code, body := get("/v1/nope"); code != 404 || !strings.Contains(body, `"type":"not_found_error"`) || !strings.Contains(body, "/v1/nope") {
		t.Errorf("404: %d %s", code, body)
	}
}

func TestLogDirWritesOneFilePerRequest(t *testing.T) {
	dir := t.TempDir()
	_, ts := newTestServer(t, Options{Reply: "ok", Model: "m", LogDir: dir})
	http.Post(ts.URL+"/v1/messages", "application/json", strings.NewReader(`{"a":1}`))
	http.Get(ts.URL + "/api/hello")
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 2 {
		t.Fatalf("log files = %v", files)
	}
	raw, _ := os.ReadFile(files[0])
	if !strings.Contains(string(raw), `"path"`) {
		t.Errorf("log file = %s", raw)
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/fakeapi/ -v`
Expected: FAIL — undefined `New`, `Options`.

- [ ] **Step 4: Implement server.go**

`internal/fakeapi/server.go`:
```go
// Package fakeapi is a canned Anthropic Messages API server so Claude Code
// can run without credentials in tests (spec §12; endpoints per ENDPOINTS.md).
package fakeapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Options struct {
	Reply  string // canned assistant text
	Model  string // reported model id
	LogDir string // one file per request body, "" = memory only
}

type Request struct {
	Path string
	Body []byte
	At   time.Time
}

type Server struct {
	opts Options
	mu   sync.Mutex
	reqs []Request
	seq  int
}

func New(o Options) *Server {
	if o.Reply == "" {
		o.Reply = "Hello from the canned server."
	}
	if o.Model == "" {
		o.Model = "claude-opus-5"
	}
	return &Server{opts: o}
}

// Requests returns a copy of every recorded request.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

func (s *Server) record(r *http.Request) []byte {
	body, _ := io.ReadAll(r.Body)
	rec := Request{Path: r.URL.Path, Body: body, At: time.Now()}
	s.mu.Lock()
	s.reqs = append(s.reqs, rec)
	s.seq++
	n := s.seq
	s.mu.Unlock()
	if s.opts.LogDir != "" {
		entry, _ := json.Marshal(map[string]any{"path": rec.Path, "method": r.Method, "query": r.URL.RawQuery, "at": rec.At, "body": json.RawMessage(bodyOrNull(body))})
		_ = os.MkdirAll(s.opts.LogDir, 0o755)
		_ = os.WriteFile(filepath.Join(s.opts.LogDir, fmt.Sprintf("%04d.json", n)), entry, 0o644)
	}
	return body
}

func bodyOrNull(b []byte) []byte {
	if !json.Valid(b) {
		q, _ := json.Marshal(string(b))
		return q
	}
	return b
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body := s.record(r)
		p := r.URL.Path
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(p, "/v1/messages/count_tokens"):
			writeJSON(w, 200, map[string]int{"input_tokens": estimateTokens(body)})
		case r.Method == http.MethodPost && strings.HasPrefix(p, "/v1/messages"):
			s.handleMessages(w, body)
		case r.Method == http.MethodGet && strings.HasPrefix(p, "/v1/models"):
			writeJSON(w, 200, map[string]any{
				"data": []map[string]any{{"id": s.opts.Model, "type": "model", "display_name": s.opts.Model, "created_at": "2026-01-01T00:00:00Z"}},
				"has_more": false, "first_id": s.opts.Model, "last_id": s.opts.Model,
			})
		case strings.HasPrefix(p, "/api/hello"):
			writeJSON(w, 200, map[string]any{"ok": true, "server": "claude-teleport fakeapi"})
		default:
			writeJSON(w, 404, map[string]any{"type": "error", "error": map[string]string{"type": "not_found_error", "message": "no such endpoint: " + r.Method + " " + p}})
		}
	})
	return mux
}

func estimateTokens(body []byte) int {
	n := len(body) / 4
	if n < 1 {
		n = 1
	}
	return n
}
```

- [ ] **Step 5: Implement messages.go**

`internal/fakeapi/messages.go`:
```go
package fakeapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
)

var msgCounter atomic.Int64

func nextMessageID() string { return fmt.Sprintf("msg_fake%08d", msgCounter.Add(1)) }

func (s *Server) handleMessages(w http.ResponseWriter, body []byte) {
	var req struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)
	id := nextMessageID()
	inTokens := estimateTokens(body)
	outTokens := estimateTokens([]byte(s.opts.Reply))
	if !req.Stream {
		writeJSON(w, 200, map[string]any{
			"id": id, "type": "message", "role": "assistant", "model": s.opts.Model,
			"content":     []map[string]string{{"type": "text", "text": s.opts.Reply}},
			"stop_reason": "end_turn", "stop_sequence": nil,
			"usage":       map[string]int{"input_tokens": inTokens, "output_tokens": outTokens},
		})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	fl, _ := w.(http.Flusher)
	event := func(name string, v any) {
		raw, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, raw)
		if fl != nil {
			fl.Flush()
		}
	}
	event("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": s.opts.Model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": inTokens, "output_tokens": 1},
	}})
	event("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}})
	event("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": s.opts.Reply}})
	event("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	event("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]int{"output_tokens": outTokens}})
	event("message_stop", map[string]any{"type": "message_stop"})
}
```

- [ ] **Step 6: Run the tests**

Run: `go test -race ./internal/fakeapi/ -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/fakeapi/ENDPOINTS.md internal/fakeapi/server.go internal/fakeapi/messages.go internal/fakeapi/server_test.go
git commit -m "feat(fakeapi): canned Messages API (SSE + JSON), models, count_tokens, hello, recorded requests"
```

---

### Task 18: `test/fakeapi-server` binary and the `realclaude` assertion test

**Files:**
- Create: `test/fakeapi-server/main.go`
- Create: `internal/fakeapi/realclaude_test.go` (build tag `realclaude`)
- Create: `internal/fakeapi/realclaude.go` (helper used by the tagged test: `RunClaude`)

**Interfaces:**
- Consumes: Task 17.
- Produces: `func RunClaude(ctx context.Context, baseURL, configDir, cwd string, args ...string) (stdout, stderr []byte, err error)` — runs the real `claude` with the spike environment, never touching the real `~/.claude`; the binary `fakeapi-server -addr :8080 -reply TEXT -model ID -log DIR` for the Plan 03 docker harness.

The tagged test repeats the spike as assertions: exactly one request, `POST /v1/messages` (query ignored), `"stream":true` in the body, exit 0, a transcript `<cfg>/projects/<munged cwd>/<sid>.jsonl` exists and is non-empty; then `--resume` exits 0 and the second recorded body contains `say hello`. It is skipped when `claude` is not on `PATH`, and only compiled with `-tags realclaude`.

- [ ] **Step 1: Write the helper**

`internal/fakeapi/realclaude.go`:
```go
package fakeapi

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
)

// spikeEnv is the exact environment ENDPOINTS.md was observed with.
func spikeEnv(baseURL, configDir string) []string {
	drop := map[string]bool{
		"ANTHROPIC_BASE_URL": true, "ANTHROPIC_API_KEY": true, "CLAUDE_CONFIG_DIR": true,
		"CLAUDECODE": true, "CLAUDE_PID": true, "CLAUDE_CODE_SESSION_ID": true, "CLAUDE_CODE_EXECPATH": true,
	}
	var env []string
	for _, kv := range os.Environ() {
		k := kv[:strings.IndexByte(kv+"=", '=')]
		if drop[k] || strings.HasPrefix(k, "CLAUDE_CODE_MESSAGING_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"ANTHROPIC_BASE_URL="+baseURL,
		"ANTHROPIC_API_KEY=dummy-key",
		"CLAUDE_CONFIG_DIR="+configDir,
		"DISABLE_AUTOUPDATER=1",
		"DISABLE_TELEMETRY=1",
		"DISABLE_ERROR_REPORTING=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	)
}

// RunClaude runs the real `claude` binary against a fakeapi at baseURL with
// a throw-away config dir. It never reads ~/.claude or .credentials.json:
// CLAUDE_CONFIG_DIR points at configDir, which the caller creates fresh.
func RunClaude(ctx context.Context, baseURL, configDir, cwd string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = cwd
	cmd.Env = spikeEnv(baseURL, configDir)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}
```

- [ ] **Step 2: Write the tagged test**

`internal/fakeapi/realclaude_test.go`:
```go
//go:build realclaude

package fakeapi

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/session"
)

func freshUUID(t *testing.T) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// TestRealClaudeAgainstFakeAPI pins ENDPOINTS.md: exactly one POST
// /v1/messages for `-p`, a transcript on disk, and prior context on --resume.
func TestRealClaudeAgainstFakeAPI(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH")
	}
	repoRoot, _ := filepath.Abs(filepath.Join("..", ".."))
	tmpRoot := filepath.Join(repoRoot, "tmp")
	os.MkdirAll(tmpRoot, 0o755)
	configDir, err := os.MkdirTemp(tmpRoot, "fakeapi-cfg-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(configDir)
	cwd, _ := os.MkdirTemp(tmpRoot, "fakeapi-cwd-")
	defer os.RemoveAll(cwd)

	s := New(Options{Reply: "Hello from the canned server.", Model: "claude-opus-5"})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	sid := freshUUID(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, errOut, err := RunClaude(ctx, ts.URL, configDir, cwd, "-p", "--session-id", sid, "say hello")
	if err != nil {
		t.Fatalf("claude -p: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if !strings.Contains(string(out), "Hello from the canned server.") {
		t.Errorf("claude did not print the canned reply: %q", out)
	}
	reqs := s.Requests()
	if len(reqs) != 1 {
		paths := make([]string, len(reqs))
		for i, r := range reqs {
			paths[i] = r.Path
		}
		t.Fatalf("expected exactly one request, got %d: %v — update ENDPOINTS.md if Claude Code changed", len(reqs), paths)
	}
	if reqs[0].Path != "/v1/messages" || !strings.Contains(string(reqs[0].Body), `"stream":true`) || !strings.Contains(string(reqs[0].Body), "say hello") {
		t.Errorf("first request = %s %.200s", reqs[0].Path, reqs[0].Body)
	}
	transcript := filepath.Join(configDir, "projects", session.Munge(cwd), sid+".jsonl")
	if fi, err := os.Stat(transcript); err != nil || fi.Size() == 0 {
		t.Fatalf("transcript %s: %v", transcript, err)
	}

	out, errOut, err = RunClaude(ctx, ts.URL, configDir, cwd, "-p", "--resume", sid, "what did I say?")
	if err != nil {
		t.Fatalf("claude --resume: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	reqs = s.Requests()
	if len(reqs) != 2 {
		t.Fatalf("expected two requests after resume, got %d", len(reqs))
	}
	if !strings.Contains(string(reqs[1].Body), "say hello") || !strings.Contains(string(reqs[1].Body), "what did I say?") {
		t.Errorf("resume request must carry the prior conversation: %.400s", reqs[1].Body)
	}
	if _, err := os.Stat(filepath.Join(configDir, ".credentials.json")); err == nil {
		t.Errorf("a credentials file appeared in the throw-away config dir — the test must never touch real credentials")
	}
}
```
(`session.Munge` is Plan 01's; `cwd` here is the session's launch cwd.)

- [ ] **Step 3: Write the binary**

`test/fakeapi-server/main.go`:
```go
// Command fakeapi-server serves internal/fakeapi for the docker harness.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/mithro/go-claude-teleport/internal/fakeapi"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	reply := flag.String("reply", "Hello from the canned server.", "canned assistant text")
	model := flag.String("model", "claude-opus-5", "model id to report")
	logDir := flag.String("log", "", "directory for one JSON file per request (\"\" = memory only)")
	flag.Parse()
	s := fakeapi.New(fakeapi.Options{Reply: *reply, Model: *model, LogDir: *logDir})
	fmt.Fprintf(os.Stderr, "fakeapi-server listening on %s (model %s)\n", *addr, *model)
	log.Fatal(http.ListenAndServe(*addr, s.Handler()))
}
```

- [ ] **Step 4: Run**

Run: `go build ./test/fakeapi-server/ && go vet -tags realclaude ./internal/fakeapi/ && go test -race ./internal/fakeapi/`
Expected: builds, vets, PASS (the tagged test is not compiled without the tag).
Then, on a machine with `claude` installed: `go test -race -tags realclaude ./internal/fakeapi/ -run TestRealClaude -v`
Expected: PASS with the single `/v1/messages` request. If Claude Code has changed and more requests appear, the failure message lists their paths: add handlers for them in `server.go`, update `ENDPOINTS.md` with the new observation (date, version, list), and adjust the count assertion — never loosen the prior-context assertion.

- [ ] **Step 5: Commit**

```bash
git add internal/fakeapi/realclaude.go internal/fakeapi/realclaude_test.go test/fakeapi-server/main.go
git commit -m "test(fakeapi): realclaude-tagged spike assertion and fakeapi-server binary"
```

---

### Task 19: cli — `remote serve`, `remote stream`, hidden `internal-runner`, ssh dialling helper

**Files:**
- Create: `internal/cli/transport.go`
- Modify: `internal/cli/root.go` (Plan 01): one call `AddTransportCommands(root)` where the root command's subcommands are added
- Test: `internal/cli/transport_test.go`

**Interfaces:**
- Consumes: Plan 01 `cli.Main(args, stdin, stdout, stderr, env) int`, exit-code constants; `remote`, `job`, `sshx`.
- Produces: `func AddTransportCommands(root *cobra.Command)`; `var RunnerSteps func(j *job.Journal, logf func(string, ...any)) ([]job.Step, error)` (nil until Plan 03 registers the orchestrator); `func envPaths(env []string) (session.Paths, error)`; `func dialTarget(ctx, target string, via []string, opts []string, env []string, logf) (*sshx.Client, sshx.Resolved, error)`; `func envValue(env []string, key string) string`.

If Plan 01's `root.go` already has a function that derives `session.Paths` from an env slice, use it inside `envPaths` instead of duplicating the rules; the rules are: config dir `$CLAUDE_CONFIG_DIR` else `$HOME/.claude`; global json `$HOME/.claude.json` (or `<ConfigDir>/.claude.json` per Plan 01's spike result — call Plan 01's helper for this one); data dir `$XDG_DATA_HOME/claude-teleport` else `$HOME/.local/share/claude-teleport`; missing `$HOME` is an error.

- [ ] **Step 1: Write the failing test**

`internal/cli/transport_test.go`:
```go
package cli

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/job"
)

const tsid = "3d2c1b0a-9f8e-4d7c-b6a5-4f3e2d1c0b9a"

func testEnv(t *testing.T) ([]string, string) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home", "bob")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	return []string{"HOME=" + home, "PATH=/usr/bin:/bin", "XDG_DATA_HOME=" + filepath.Join(home, ".local", "share")}, home
}

func TestRemoteServeOverStdio(t *testing.T) {
	env, home := testEnv(t)
	stdin := strings.NewReader(`{"id":1,"op":"hello","args":{"version":"dev","protocol":1}}` + "\n" + `{"id":2,"op":"paths","args":{}}` + "\n")
	var stdout, stderr bytes.Buffer
	code := Main([]string{"remote", "serve"}, stdin, &stdout, &stderr, env)
	if code != ExitOK {
		t.Fatalf("exit %d, stderr %s", code, stderr.String())
	}
	sc := bufio.NewScanner(&stdout)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) != 2 || !strings.Contains(lines[0], `"ok":true`) || !strings.Contains(lines[1], filepath.Join(home, ".claude")) {
		t.Errorf("responses = %v", lines)
	}
}

func TestRemoteStreamLog(t *testing.T) {
	env, home := testEnv(t)
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
	j, _ := job.New(dataDir, tsid)
	os.WriteFile(j.LogPath(), []byte("hello log\n"), 0o600)
	var stdout, stderr bytes.Buffer
	code := Main([]string{"remote", "stream", "log", tsid, "s1"}, strings.NewReader(""), &stdout, &stderr, env)
	if code != ExitOK || stdout.String() != "hello log\n" {
		t.Errorf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	code = Main([]string{"remote", "stream", "bogus", tsid, "s1"}, strings.NewReader(""), &stdout, &stderr, env)
	if code != ExitFailed || !strings.Contains(stderr.String(), "unknown stream kind") {
		t.Errorf("bad kind: exit %d stderr %q", code, stderr.String())
	}
}

func TestInternalRunnerWithoutStepsIsExplicit(t *testing.T) {
	env, home := testEnv(t)
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
	j, _ := job.New(dataDir, tsid)
	j.Save()
	var stdout, stderr bytes.Buffer
	code := Main([]string{"internal-runner", j.Dir}, strings.NewReader(""), &stdout, &stderr, env)
	if code != ExitFailed || !strings.Contains(stderr.String(), "no steps registered") {
		t.Errorf("exit %d stderr %q", code, stderr.String())
	}
	code = Main([]string{"internal-runner", filepath.Join(dataDir, "jobs", "nope")}, strings.NewReader(""), &stdout, &stderr, env)
	if code != ExitFailed || !strings.Contains(stderr.String(), "no journal") {
		t.Errorf("missing journal: exit %d stderr %q", code, stderr.String())
	}
	if _, err := os.Stat(j.Dir); err != nil {
		t.Errorf("runner must not remove the job dir: %v", err)
	}
	_ = io.EOF
}

func TestEnvPaths(t *testing.T) {
	p, err := envPaths([]string{"HOME=/home/alice", "CLAUDE_CONFIG_DIR=/home/alice/cfg"})
	if err != nil || p.ConfigDir != "/home/alice/cfg" || p.DataDir != "/home/alice/.local/share/claude-teleport" || p.Home != "/home/alice" {
		t.Errorf("envPaths = %+v err=%v", p, err)
	}
	if _, err := envPaths([]string{"PATH=/bin"}); err == nil {
		t.Errorf("missing HOME must be an error")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cli/ -run 'TestRemote|TestInternalRunner|TestEnvPaths' -v`
Expected: FAIL — `remote`/`internal-runner` unknown commands (exit 2) and `undefined: envPaths`.

- [ ] **Step 3: Implement transport.go**

`internal/cli/transport.go`:
```go
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kevinburke/ssh_config"
	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/sshx"
)

// RunnerSteps is registered by Plan 03's orchestrator; nil means the
// detached runner cannot run any job yet.
var RunnerSteps func(j *job.Journal, logf func(string, ...any)) ([]job.Step, error)

// fail is shorthand for Plan 01's cli.Exit: it returns a *cli.ExitError
// carrying a spec §5 exit code out of a cobra RunE (Plan 01's Main maps it).
func fail(code int, format string, args ...any) error {
	return Exit(code, format, args...)
}

// envValue(env []string, key string) string is defined by Plan 01
// (internal/cli/compare.go, Task 21); do not redefine it here.

// envPaths derives session.Paths from an environment slice using Plan 01's
// session.NewPaths (the one place the CLAUDE_CONFIG_DIR / .claude.json rule
// lives); missing $HOME is an error.
func envPaths(env []string) (session.Paths, error) {
	home := envValue(env, "HOME")
	if home == "" {
		return session.Paths{}, errors.New("HOME is not set")
	}
	return session.NewPaths(home, envValue(env, "CLAUDE_CONFIG_DIR"), envValue(env, "XDG_DATA_HOME")), nil
}

// cmdEnv is how Plan 01's Main hands the env slice and stdio to commands:
// it stores them in the root command's context under this key (Main is
// modified in this task to do so; see the wiring note below).
type cmdEnvKey struct{}

type cmdEnv struct {
	env    []string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func envOf(cmd *cobra.Command) cmdEnv {
	if v, ok := cmd.Context().Value(cmdEnvKey{}).(cmdEnv); ok {
		return v
	}
	return cmdEnv{env: os.Environ(), stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}
}

func stderrLogf(w io.Writer) func(string, ...any) {
	return func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }
}

func selfExe() string {
	exe, err := os.Executable()
	if err != nil {
		return "claude-teleport"
	}
	return exe
}

// dialTarget resolves target (with --via hops and -o overrides) through
// ~/.ssh/config and dials it with 3 attempts. Honours -o UserKnownHostsFile
// and -o StrictHostKeyChecking (via Resolved.Options). Exit code 2 for bad
// input, 4 when the host cannot be reached or authenticated.
func dialTarget(ctx context.Context, target string, via []string, opts []string, env []string, logf func(string, ...any)) (*sshx.Client, sshx.Resolved, error) {
	t, err := sshx.ParseTarget(target)
	if err != nil {
		return nil, sshx.Resolved{}, fail(ExitUsage, "%v", err)
	}
	for _, v := range via {
		vt, err := sshx.ParseTarget(v)
		if err != nil {
			return nil, sshx.Resolved{}, fail(ExitUsage, "--via %q: %v", v, err)
		}
		t.Via = append(t.Via, vt)
	}
	overrides := map[string]string{}
	for _, o := range opts {
		k, v, ok := strings.Cut(o, "=")
		if !ok || k == "" {
			return nil, sshx.Resolved{}, fail(ExitUsage, "-o %q: want KEY=VALUE", o)
		}
		overrides[k] = v
	}
	home := envValue(env, "HOME")
	var cfg *ssh_config.Config
	if f, err := os.Open(filepath.Join(home, ".ssh", "config")); err == nil {
		cfg, err = ssh_config.Decode(f)
		f.Close()
		if err != nil {
			return nil, sshx.Resolved{}, fail(ExitUsage, "~/.ssh/config: %v", err)
		}
	}
	localUser := envValue(env, "USER")
	if localUser == "" {
		localUser = "root"
	}
	r, err := sshx.Resolve(t, cfg, overrides, localUser)
	if err != nil {
		return nil, sshx.Resolved{}, fail(ExitUsage, "%v", err)
	}
	o := sshx.Options{
		KnownHostsFile: filepath.Join(home, ".ssh", "known_hosts"),
		AgentSocket:    envValue(env, "SSH_AUTH_SOCK"),
		Home:           home,
		Logf:           logf,
	}
	if kh, ok := r.Options["UserKnownHostsFile"]; ok {
		o.KnownHostsFile = kh
	}
	c, err := sshx.Redial(ctx, 3, 500*time.Millisecond, logf, func(ctx context.Context) (*sshx.Client, error) {
		return sshx.Dial(ctx, r, cfg, overrides, o)
	})
	if err != nil {
		return nil, r, fail(ExitUnreachable, "%v", err)
	}
	return c, r, nil
}

// AddTransportCommands registers remote serve|stream and internal-runner.
func AddTransportCommands(root *cobra.Command) {
	remoteCmd := &cobra.Command{Use: "remote", Short: "internal: remote helper", Hidden: true}
	remoteCmd.AddCommand(&cobra.Command{
		Use:  "serve",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := envOf(cmd)
			p, err := envPaths(e.env)
			if err != nil {
				return fail(ExitUsage, "%v", err)
			}
			local := remote.NewLocal(p, selfExe(), remote.LocalOptions{ProcRoot: "/proc", Logf: stderrLogf(e.stderr)})
			if err := remote.Serve(cmd.Context(), e.stdin, e.stdout, local); err != nil {
				return fail(ExitFailed, "remote serve: %v", err)
			}
			return nil
		},
	})
	remoteCmd.AddCommand(&cobra.Command{
		Use:  "stream <kind> <job> <stream-id>",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := envOf(cmd)
			p, err := envPaths(e.env)
			if err != nil {
				return fail(ExitUsage, "%v", err)
			}
			local := remote.NewLocal(p, selfExe(), remote.LocalOptions{ProcRoot: "/proc", Logf: stderrLogf(e.stderr)})
			if err := remote.ServeStream(cmd.Context(), remote.StreamKind(args[0]), args[1], args[2], e.stdin, e.stdout, local); err != nil {
				return fail(ExitFailed, "%v", err)
			}
			return nil
		},
	})
	root.AddCommand(remoteCmd)

	root.AddCommand(&cobra.Command{
		Use:    "internal-runner <job-dir>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := envOf(cmd)
			jobDir := filepath.Clean(args[0])
			id := filepath.Base(jobDir)
			dataDir := filepath.Dir(filepath.Dir(jobDir))
			j, found, err := job.Open(dataDir, id)
			if err != nil {
				return fail(ExitFailed, "%v", err)
			}
			if !found {
				return fail(ExitFailed, "no journal at %s", jobDir)
			}
			logf := stderrLogf(e.stderr)
			if RunnerSteps == nil {
				return fail(ExitFailed, "internal-runner: no steps registered for job %s (orchestrator arrives in Plan 03)", id)
			}
			steps, err := RunnerSteps(j, logf)
			if err != nil {
				return fail(ExitFailed, "%v", err)
			}
			j.RunnerPID = os.Getpid()
			if err := j.Save(); err != nil {
				return fail(ExitFailed, "%v", err)
			}
			if err := job.Run(cmd.Context(), j, steps, logf); err != nil {
				return fail(ExitFailed, "%v", err)
			}
			return nil
		},
	})
}
```
Wiring into Plan 01's cli (Plan 01 is merged first; these are the exact names): in `internal/cli/cli.go` `Main`, after `root := a.rootCmd()`, add `root.SetContext(context.WithValue(context.Background(), cmdEnvKey{}, cmdEnv{env: env, stdin: stdin, stdout: stdout, stderr: stderr}))` (Plan 01's `Main` already maps a returned `*ExitError` to its code, which is what `fail` returns). In `internal/cli/root.go` `(*app).rootCmd()`, add the single line `AddTransportCommands(root)` after the `root.AddCommand(...)` call. `filepath` and `strings` stay imported in `transport.go` for `dialTarget`.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/cli/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/transport.go internal/cli/transport_test.go internal/cli/root.go
git commit -m "feat(cli): remote serve/stream helper commands, hidden internal-runner, ssh dial helper"
```

---

### Task 20: cli — `status <sid> [--json]`

**Files:**
- Create: `internal/cli/status.go`
- Modify: `internal/cli/transport.go` (register `statusCmd()` in `AddTransportCommands`)
- Test: `internal/cli/status_test.go`

**Interfaces:**
- Consumes: `job.Open`, `job.TailLog`, `transfer.Load`, `session.ParseID`, `envPaths`, `fail`.
- Produces: `func statusCmd() *cobra.Command`; `func renderStatus(w io.Writer, j *job.Journal, m *transfer.Manifest, logTail []string)`.

- [ ] **Step 1: Write the failing test**

`internal/cli/status_test.go`:
```go
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/job"
)

func TestStatusRendersJournal(t *testing.T) {
	env, home := testEnv(t)
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
	j, _ := job.New(dataDir, tsid)
	j.Direction, j.SourceHost, j.DestHost = "to", "laptop.example", "big-storage.example"
	j.Step("preflight").Status = job.Done
	s := j.Step("transfer")
	s.Status, s.Error, s.Attempts = job.Failed, "connection reset", 2
	j.Outcome = "failed"
	j.Save()
	os.WriteFile(j.LogPath(), []byte("l1\nl2\nl3\n"), 0o600)

	var out, errOut bytes.Buffer
	code := Main([]string{"status", tsid}, strings.NewReader(""), &out, &errOut, env)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	for _, want := range []string{"laptop.example", "big-storage.example", "preflight", "done", "transfer", "failed", "connection reset", "attempts 2", "l3", "continue " + tsid, "abandon " + tsid} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output lacks %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	code = Main([]string{"status", tsid, "--json"}, strings.NewReader(""), &out, &errOut, env)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var doc struct {
		Journal job.Journal `json:"journal"`
		LogTail []string    `json:"log_tail"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("json: %v\n%s", err, out.String())
	}
	if doc.Journal.Outcome != "failed" || len(doc.LogTail) != 3 {
		t.Errorf("json doc = %+v", doc)
	}
	_ = time.Now
}

func TestStatusMissingAndBadID(t *testing.T) {
	env, _ := testEnv(t)
	var out, errOut bytes.Buffer
	if code := Main([]string{"status", tsid}, strings.NewReader(""), &out, &errOut, env); code != ExitFailed || !strings.Contains(errOut.String(), "no job") {
		t.Errorf("missing: exit %d stderr %q", code, errOut.String())
	}
	if code := Main([]string{"status", "not-a-uuid"}, strings.NewReader(""), &out, &errOut, env); code != ExitUsage {
		t.Errorf("bad id: exit %d", code)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cli/ -run TestStatus -v`
Expected: FAIL — `status` is unknown (or Plan 01 has a stub that does not print these fields).

- [ ] **Step 3: Implement**

`internal/cli/status.go`:
```go
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

func statusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status <sid>",
		Short: "journal and manifest of a teleport job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := envOf(cmd)
			id, err := session.ParseID(args[0])
			if err != nil {
				return fail(ExitUsage, "%v", err)
			}
			p, err := envPaths(e.env)
			if err != nil {
				return fail(ExitUsage, "%v", err)
			}
			j, found, err := job.Open(p.DataDir, string(id))
			if err != nil {
				return fail(ExitFailed, "%v", err)
			}
			if !found {
				return fail(ExitFailed, "no job for session %s under %s", id, job.Dir(p.DataDir, string(id)))
			}
			m, err := transfer.Load(j.ManifestPath())
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fail(ExitFailed, "%v", err)
			}
			tail, err := job.TailLog(j.LogPath(), 20)
			if err != nil {
				return fail(ExitFailed, "%v", err)
			}
			if asJSON {
				doc := map[string]any{"journal": j, "manifest": m, "log_tail": tail}
				enc := json.NewEncoder(e.stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(doc)
			}
			renderStatus(e.stdout, j, m, tail)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

func renderStatus(w io.Writer, j *job.Journal, m *transfer.Manifest, logTail []string) {
	fmt.Fprintf(w, "job %s: %s %s -> %s\n", j.ID, j.Direction, j.SourceHost, j.DestHost)
	outcome := j.Outcome
	if outcome == "" {
		outcome = "in progress"
	}
	fmt.Fprintf(w, "created %s, updated %s, outcome: %s\n", j.CreatedAt.Format("2006-01-02T15:04:05Z07:00"), j.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"), outcome)
	if j.RunnerPID > 0 {
		fmt.Fprintf(w, "runner pid %d\n", j.RunnerPID)
	}
	fmt.Fprintln(w, "steps:")
	for _, s := range j.Steps {
		line := fmt.Sprintf("  %-10s %-8s", s.Name, s.Status)
		if s.Attempts > 0 {
			line += fmt.Sprintf(" attempts %d", s.Attempts)
		}
		if s.Error != "" {
			line += " error: " + s.Error
		}
		fmt.Fprintln(w, line)
	}
	if m != nil {
		var bytes int64
		for _, e := range m.Entries {
			bytes += e.Size
		}
		fmt.Fprintf(w, "manifest: %d entries, %d bytes, %d skipped\n", len(m.Entries), bytes, len(m.Skipped))
	}
	if len(logTail) > 0 {
		fmt.Fprintln(w, "log tail:")
		for _, l := range logTail {
			fmt.Fprintln(w, "  "+l)
		}
	}
	if !j.Finished {
		fmt.Fprintf(w, "\nnext: claude-teleport continue %s   |   claude-teleport abandon %s\n", j.ID, j.ID)
	}
}
```
Register it: in `AddTransportCommands`, add `root.AddCommand(statusCmd())`. If Plan 01 registered a placeholder `status` command, remove that placeholder in `root.go` so this one is the only `status`.

`transfer.Load` wraps `os.ReadFile`'s error with `%w`, so `errors.Is(err, os.ErrNotExist)` works for a job without a manifest yet.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/cli/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/status.go internal/cli/status_test.go internal/cli/transport.go internal/cli/root.go
git commit -m "feat(cli): status renders the job journal, manifest summary and log tail (--json)"
```

---

### Task 21: cli — `abandon <sid> [--delete-destination-files]` (local side)

**Files:**
- Create: `internal/cli/abandon.go`
- Modify: `internal/cli/transport.go` (register `abandonCmd()`)
- Test: `internal/cli/abandon_test.go`

**Interfaces:**
- Consumes: `job.Open`, `job.StagingDir`, `transfer.Load`, `transfer.Uninstall`, `envPaths`, `fail`.
- Produces: `func abandonCmd() *cobra.Command`.

Behaviour: refuse (exit 1) if the runner is alive (`j.RunnerAlive` with a `/proc`-based `alive` — `os.FindProcess` + `Signal(0)`); mark `Outcome="abandoned"`, `Finished=true`, save; remove the local staging dir `StagingDir(dataDir, sid)` (always safe: staging is ours); with `--delete-destination-files`: if this host is the destination (`j.Direction == "from"`), load the manifest and `transfer.Uninstall`, printing each removed path; if this host is the source (`"to"`), error "deleting files on <dest> over ssh arrives in Plan 03; run `claude-teleport abandon <sid> --delete-destination-files` on <dest> instead" (exit 1) — the journal is still marked abandoned first. Job dir, log and journal are never deleted (spec §6: manual, inspectable clean-up).

- [ ] **Step 1: Write the failing test**

`internal/cli/abandon_test.go`:
```go
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

func TestAbandonMarksAndRemovesStaging(t *testing.T) {
	env, home := testEnv(t)
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
	j, _ := job.New(dataDir, tsid)
	j.Direction = "to"
	j.Save()
	staging := job.StagingDir(dataDir, tsid)
	os.MkdirAll(staging, 0o700)
	os.WriteFile(filepath.Join(staging, "1"), []byte("x"), 0o600)

	var out, errOut bytes.Buffer
	if code := Main([]string{"abandon", tsid}, strings.NewReader(""), &out, &errOut, env); code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	got, _, _ := job.Open(dataDir, tsid)
	if got.Outcome != "abandoned" || !got.Finished {
		t.Errorf("journal = %+v", got)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Errorf("staging dir must be removed")
	}
	if _, err := os.Stat(j.LogPath()); err == nil {
		// log may not exist; only assert the job dir survives
	}
	if _, err := os.Stat(j.Dir); err != nil {
		t.Errorf("job dir must survive abandon: %v", err)
	}

	// source side cannot delete destination files in this plan
	if code := Main([]string{"abandon", tsid, "--delete-destination-files"}, strings.NewReader(""), &out, &errOut, env); code != ExitFailed || !strings.Contains(errOut.String(), "Plan 03") {
		t.Errorf("source-side delete: exit %d stderr %q", code, errOut.String())
	}
}

func TestAbandonDeleteDestinationFilesLocally(t *testing.T) {
	env, home := testEnv(t)
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
	cfg := filepath.Join(home, ".claude")
	j, _ := job.New(dataDir, tsid)
	j.Direction = "from" // this host is the destination
	j.Save()
	installed := filepath.Join(cfg, "projects", "-home-bob-work", tsid+".jsonl")
	os.MkdirAll(filepath.Dir(installed), 0o700)
	os.WriteFile(installed, []byte("{}\n"), 0o600)
	modified := filepath.Join(cfg, "todos", tsid+".json")
	os.MkdirAll(filepath.Dir(modified), 0o700)
	os.WriteFile(modified, []byte("changed"), 0o600)
	m := &transfer.Manifest{Version: 1, JobID: tsid, SessionID: tsid, Entries: []transfer.Entry{
		{ID: 0, Category: session.CatSession, Dst: filepath.Dir(installed), Mode: uint32(os.ModeDir | 0o700)},
		{ID: 1, Category: session.CatSession, Dst: installed, Size: 3, Mode: 0o600, SHA256: shaOf("{}\n")},
		{ID: 2, Category: session.CatSession, Dst: modified, Size: 2, Mode: 0o600, SHA256: shaOf("{}")},
	}}
	m.Save(j.ManifestPath())

	var out, errOut bytes.Buffer
	if code := Main([]string{"abandon", tsid, "--delete-destination-files"}, strings.NewReader(""), &out, &errOut, env); code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Errorf("matching installed file should be removed")
	}
	if _, err := os.Stat(modified); err != nil {
		t.Errorf("modified file must be kept: %v", err)
	}
	if !strings.Contains(out.String(), installed) {
		t.Errorf("removed paths must be printed: %s", out.String())
	}
}
```
And in the same test file:
```go
func shaOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
```
(imports `crypto/sha256`, `encoding/hex`).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cli/ -run TestAbandon -v`
Expected: FAIL — `abandon` unknown or does nothing.

- [ ] **Step 3: Implement**

`internal/cli/abandon.go`:
```go
package cli

import (
	"fmt"
	"os"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func abandonCmd() *cobra.Command {
	var deleteDest bool
	cmd := &cobra.Command{
		Use:   "abandon <sid>",
		Short: "give up on a teleport job; clean staging, optionally remove installed files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := envOf(cmd)
			id, err := session.ParseID(args[0])
			if err != nil {
				return fail(ExitUsage, "%v", err)
			}
			p, err := envPaths(e.env)
			if err != nil {
				return fail(ExitUsage, "%v", err)
			}
			j, found, err := job.Open(p.DataDir, string(id))
			if err != nil {
				return fail(ExitFailed, "%v", err)
			}
			if !found {
				return fail(ExitFailed, "no job for session %s under %s", id, job.Dir(p.DataDir, string(id)))
			}
			if j.RunnerAlive(pidAlive) {
				return fail(ExitFailed, "job %s has a live runner (pid %d); stop it first", id, j.RunnerPID)
			}
			j.Outcome = "abandoned"
			j.Finished = true
			if err := j.Save(); err != nil {
				return fail(ExitFailed, "%v", err)
			}
			staging := job.StagingDir(p.DataDir, string(id))
			if err := os.RemoveAll(staging); err != nil {
				return fail(ExitFailed, "remove staging %s: %w", staging, err)
			}
			fmt.Fprintf(e.stdout, "job %s marked abandoned; staging %s removed; journal kept at %s\n", id, staging, j.Dir)
			if !deleteDest {
				return nil
			}
			if j.Direction != "from" {
				return fail(ExitFailed, "deleting files on %s over ssh arrives in Plan 03; run `claude-teleport abandon %s --delete-destination-files` on %s instead", j.DestHost, id, j.DestHost)
			}
			m, err := transfer.Load(j.ManifestPath())
			if err != nil {
				return fail(ExitFailed, "%v", err)
			}
			removed, err := transfer.Uninstall(m, p)
			for _, r := range removed {
				fmt.Fprintf(e.stdout, "removed %s\n", r)
			}
			if err != nil {
				return fail(ExitFailed, "%v", err)
			}
			fmt.Fprintf(e.stdout, "%d of %d manifest entries removed (unchanged files only)\n", len(removed), len(m.Entries))
			return nil
		},
	}
	cmd.Flags().BoolVar(&deleteDest, "delete-destination-files", false, "remove files this job installed on the destination (only those still matching the manifest)")
	return cmd
}
```
Register `root.AddCommand(abandonCmd())` in `AddTransportCommands` (and drop any Plan 01 placeholder `abandon`).

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/cli/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/abandon.go internal/cli/abandon_test.go internal/cli/transport.go
git commit -m "feat(cli): abandon marks the job, clears staging, optionally uninstalls matching files locally"
```

---

### Task 22: cli — remote `compare-config <host>` and `inspect --host`

**Files:**
- Create: `internal/cli/remotecfg.go`
- Modify: `internal/cli/transport.go` (register `compareConfigCmd()`; call `addInspectHost(root)`)
- Test: `internal/cli/remotecfg_test.go`

**Interfaces:**
- Consumes: `dialTarget`, `remote.NewClient`, `remote.NewLocal`, `claudecfg.Compare/Render/JSON`, `session.ParseSelector`, `session.Env`.
- Produces: `func compareConfigCmd() *cobra.Command`; `func addInspectHost(root *cobra.Command)`; `func openRemote(ctx, cmd *cobra.Command, host string, via, opts []string) (*remote.Client, func(), error)`.

`compare-config <host> [--session <session>] [--via H]... [-o K=V]... [--json]`: local inventory via `remote.NewLocal(...).InventoryHost(ctx, cwd, ver)` and remote via `Client.InventoryHost`; `cwd` is the session's launch cwd when `--session` is given (resolved locally), else the process cwd from `$PWD`; usage is `session.ScanUsage` of that session or nil ("everything used"); `claudecfg.Compare(local, remote, usage)` rendered as the table (or `--json`). Exit 0 always unless unreachable (4) or usage (2) — it is a report, not a gate.

`inspect --host H`: the existing Plan 01 `inspect [<session>]` gains `--host` (+ `--via`, `-o`): the selector is resolved **on the remote** (`Client.ResolveSession`), then `Client.InventorySession` lists what would move (files, sizes, skipped) and `claudecfg.Compare(remoteHost, localHost, usage)` shows the drift *if that session were teleported here*. Implemented by wrapping the existing command's `RunE`.

- [ ] **Step 1: Write the failing test**

`internal/cli/remotecfg_test.go`:
```go
package cli

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/mithro/go-claude-teleport/internal/sshx/sshtest"
)

// remoteHost starts an sshtest server whose exec handler runs THIS cli's
// `remote serve` in-process with remoteEnv, and returns "host:port" + the
// -o flags the client needs (key file, accept-new).
func remoteHost(t *testing.T, remoteEnv []string) (string, []string, string) {
	t.Helper()
	localHome := filepath.Join(t.TempDir(), "home", "alice")
	os.MkdirAll(filepath.Join(localHome, ".ssh"), 0o700)
	keyPath, signer := sshtest.WriteKeyFile(t, filepath.Join(localHome, ".ssh"), "id_ed25519", "")
	srv := sshtest.New(t, sshtest.Options{
		Authorized: []ssh.PublicKey{signer.PublicKey()},
		Exec: func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
			f := strings.Fields(cmd)
			if len(f) < 3 || f[1] != "remote" {
				io.WriteString(stderr, "unexpected: "+cmd)
				return 127
			}
			return Main(f[1:], stdin, stdout, stderr, remoteEnv)
		},
	})
	host, port, _ := net.SplitHostPort(srv.Addr)
	target := "bob@" + host + ":" + port
	opts := []string{"-o", "IdentityFile=" + keyPath, "-o", "StrictHostKeyChecking=accept-new"}
	return target, opts, localHome
}

func writeSettings(t *testing.T, cfgDir, hooks string) {
	t.Helper()
	os.MkdirAll(cfgDir, 0o700)
	os.WriteFile(filepath.Join(cfgDir, "settings.json"), []byte(`{"hooks":`+hooks+`,"permissions":{"defaultMode":"default"}}`), 0o600)
}

func TestCompareConfigRemote(t *testing.T) {
	remoteEnv, remoteHome := testEnv(t)
	writeSettings(t, filepath.Join(remoteHome, ".claude"), `{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo remote"}]}]}`)
	target, opts, localHome := remoteHost(t, remoteEnv)
	writeSettings(t, filepath.Join(localHome, ".claude"), `{}`)
	localEnv := []string{"HOME=" + localHome, "USER=alice", "PWD=" + localHome, "PATH=/usr/bin:/bin"}

	var out, errOut bytes.Buffer
	args := append([]string{"compare-config", target}, opts...)
	code := Main(args, strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "hooks") || !strings.Contains(out.String(), "block") {
		t.Errorf("hook drift must be reported as block:\n%s", out.String())
	}
	kh, _ := os.ReadFile(filepath.Join(localHome, ".ssh", "known_hosts"))
	if !strings.Contains(string(kh), "ssh-ed25519") {
		t.Errorf("accept-new should have recorded the host key")
	}

	out.Reset()
	code = Main(append(args, "--json"), strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitOK || !strings.HasPrefix(strings.TrimSpace(out.String()), "{") {
		t.Errorf("--json: exit %d out %q", code, out.String())
	}
}

func TestCompareConfigUnreachable(t *testing.T) {
	_, localHome := testEnv(t)
	localEnv := []string{"HOME=" + localHome, "USER=alice", "PATH=/bin"}
	var out, errOut bytes.Buffer
	code := Main([]string{"compare-config", "nobody@127.0.0.1:1", "-o", "StrictHostKeyChecking=no"}, strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitUnreachable {
		t.Errorf("exit %d, want %d: %s", code, ExitUnreachable, errOut.String())
	}
}

func TestInspectHostResolvesRemotely(t *testing.T) {
	remoteEnv, remoteHome := testEnv(t)
	target, opts, localHome := remoteHost(t, remoteEnv)
	localEnv := []string{"HOME=" + localHome, "USER=alice", "PATH=/bin"}
	// no such session on the remote -> not-found from the remote resolver
	var out, errOut bytes.Buffer
	code := Main(append([]string{"inspect", tsid, "--host", target}, opts...), strings.NewReader(""), &out, &errOut, localEnv)
	if code == ExitOK || !strings.Contains(errOut.String(), "not") {
		t.Errorf("exit %d stderr %q", code, errOut.String())
	}
	// with a transcript on the remote, inspect lists it
	proj := filepath.Join(remoteHome, ".claude", "projects", "-home-bob-work")
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, tsid+".jsonl"), []byte(`{"type":"user","cwd":"/home/bob/work","sessionId":"`+tsid+`","version":"2.1.247","timestamp":"2026-08-27T10:00:00Z","message":{"role":"user","content":"hi"}}`+"\n"), 0o600)
	out.Reset()
	errOut.Reset()
	code = Main(append([]string{"inspect", tsid, "--host", target}, opts...), strings.NewReader(""), &out, &errOut, localEnv)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), tsid+".jsonl") || !strings.Contains(out.String(), "/home/bob/work") {
		t.Errorf("inspect --host output:\n%s", out.String())
	}
	_ = context.Background
}
```
`TestInspectHostResolvesRemotely` depends on Plan 01's `session.Resolve` finding a session by full uuid from a lone transcript file (it does per the interfaces doc: "scanning the registry, the projects tree").

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cli/ -run 'TestCompareConfig|TestInspectHost' -v`
Expected: FAIL — `compare-config` has no remote path / `--host` unknown flag.

- [ ] **Step 3: Implement**

`internal/cli/remotecfg.go`:
```go
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// openRemote dials host and starts the remote helper. The returned func
// closes both.
func openRemote(ctx context.Context, cmd *cobra.Command, host string, via, opts []string) (*remote.Client, func(), error) {
	e := envOf(cmd)
	logf := stderrLogf(e.stderr)
	sc, _, err := dialTarget(ctx, host, via, opts, e.env, logf)
	if err != nil {
		return nil, nil, err
	}
	rc, err := remote.NewClient(ctx, sc, "claude-teleport", logf)
	if err != nil {
		sc.Close()
		var pe *remote.Error
		if errors.As(err, &pe) && pe.Code == "usage" {
			return nil, nil, fail(ExitUnreachable, "%s: %v", sc, err)
		}
		return nil, nil, fail(ExitUnreachable, "%s: start remote helper: %v (is claude-teleport installed there?)", sc, err)
	}
	return rc, func() { rc.Close(); sc.Close() }, nil
}

func localEndpoint(cmd *cobra.Command) (*remote.Local, session.Paths, error) {
	e := envOf(cmd)
	p, err := envPaths(e.env)
	if err != nil {
		return nil, p, fail(ExitUsage, "%v", err)
	}
	return remote.NewLocal(p, selfExe(), remote.LocalOptions{ProcRoot: "/proc", Logf: stderrLogf(e.stderr)}), p, nil
}

func compareConfigCmd() *cobra.Command {
	var via, opts []string
	var sel string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "compare-config <host>",
		Short: "compare this host's Claude configuration with <host>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			e := envOf(cmd)
			local, _, err := localEndpoint(cmd)
			if err != nil {
				return err
			}
			cwd := envValue(e.env, "PWD")
			var usage *session.Usage
			if sel != "" {
				s, err := local.ResolveSession(ctx, session.Selector{ID: session.ID(sel), Prefix: sel})
				if err != nil {
					return fail(ExitUsage, "--session %s: %v", sel, err)
				}
				cwd = s.LaunchCwd
				if _, usage, err = local.InventorySession(ctx, s.ID); err != nil {
					return fail(ExitFailed, "%v", err)
				}
			}
			rc, closeRemote, err := openRemote(ctx, cmd, args[0], via, opts)
			if err != nil {
				return err
			}
			defer closeRemote()
			localInfo, _ := local.Hello(ctx)
			src, err := local.InventoryHost(ctx, cwd, localInfo.ClaudeVersion)
			if err != nil {
				return fail(ExitFailed, "local inventory: %v", err)
			}
			dst, err := rc.InventoryHost(ctx, cwd, rc.Info().ClaudeVersion)
			if err != nil {
				return fail(ExitFailed, "%s inventory: %v", args[0], err)
			}
			rep := claudecfg.Compare(src, dst, usage)
			if asJSON {
				raw, err := rep.JSON()
				if err != nil {
					return fail(ExitFailed, "%v", err)
				}
				fmt.Fprintln(e.stdout, string(raw))
				return nil
			}
			fmt.Fprintf(e.stdout, "configuration: %s (local) vs %s\n", localInfo.Hostname, rc.Info().Hostname)
			rep.Render(e.stdout)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&via, "via", nil, "jump host (repeatable, outermost first)")
	cmd.Flags().StringArrayVarP(&opts, "option", "o", nil, "ssh option KEY=VALUE")
	cmd.Flags().StringVar(&sel, "session", "", "session (uuid or prefix) whose usage decides what counts as used")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

// addInspectHost gives Plan 01's `inspect` a --host that resolves and
// inventories the session on the remote instead of here.
func addInspectHost(root *cobra.Command) {
	var inspect *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "inspect" {
			inspect = c
		}
	}
	if inspect == nil {
		return
	}
	var host string
	var via, opts []string
	inspect.Flags().StringVar(&host, "host", "", "inspect the session on this host (as `--from` would see it)")
	inspect.Flags().StringArrayVar(&via, "via", nil, "jump host (repeatable, outermost first)")
	inspect.Flags().StringArrayVarP(&opts, "option", "o", nil, "ssh option KEY=VALUE")
	orig := inspect.RunE
	inspect.RunE = func(cmd *cobra.Command, args []string) error {
		if host == "" {
			return orig(cmd, args)
		}
		ctx := cmd.Context()
		e := envOf(cmd)
		selector, err := session.ParseSelector(args, session.Env{})
		if err != nil {
			return fail(ExitUsage, "%v", err)
		}
		rc, closeRemote, err := openRemote(ctx, cmd, host, via, opts)
		if err != nil {
			return err
		}
		defer closeRemote()
		s, err := rc.ResolveSession(ctx, selector)
		if err != nil {
			return fail(ExitFailed, "%s: %v", host, err)
		}
		inv, usage, err := rc.InventorySession(ctx, s.ID)
		if err != nil {
			return fail(ExitFailed, "%s: %v", host, err)
		}
		fmt.Fprintf(e.stdout, "session %s on %s: %s (state %s)\n", s.ID, host, s.LaunchCwd, s.State)
		var total int64
		for _, f := range inv.Files {
			total += f.Size
			fmt.Fprintf(e.stdout, "  %-9s %10d  %s\n", f.Category, f.Size, f.Path())
		}
		fmt.Fprintf(e.stdout, "%d files, %d bytes; %d memory files; %d skipped\n", len(inv.Files), total, len(inv.Memory), len(inv.Skipped))
		for _, sk := range inv.Skipped {
			fmt.Fprintf(e.stdout, "  skipped %s: %s\n", sk.Path, sk.Reason)
		}
		local, _, err := localEndpoint(cmd)
		if err != nil {
			return err
		}
		localInfo, _ := local.Hello(ctx)
		remoteInv, err := rc.InventoryHost(ctx, s.LaunchCwd, s.Version)
		if err != nil {
			return fail(ExitFailed, "%s inventory: %v", host, err)
		}
		localInv, err := local.InventoryHost(ctx, s.LaunchCwd, localInfo.ClaudeVersion)
		if err != nil {
			return fail(ExitFailed, "local inventory: %v", err)
		}
		fmt.Fprintf(e.stdout, "\ndrift if teleported here (%s -> %s):\n", host, localInfo.Hostname)
		claudecfg.Compare(remoteInv, localInv, usage).Render(e.stdout)
		return nil
	}
	_ = json.Marshal
}
```
Register in `AddTransportCommands`: `root.AddCommand(compareConfigCmd())` (replacing any Plan 01 local-only `compare-config` stub; if Plan 01's `compare-config` already does the local half, keep its name and add the remote path the same way `addInspectHost` wraps `inspect`) and, **after** all other commands are added, `addInspectHost(root)`.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/cli/ -v && go test -race ./...`
Expected: PASS across the module.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/remotecfg.go internal/cli/remotecfg_test.go internal/cli/transport.go
git commit -m "feat(cli): compare-config against a remote host and inspect --host via the remote helper"
```

---

### Task 23: Plan wrap-up — vet, race, PR

**Files:**
- Modify: none new (verification only)

- [ ] **Step 1: Full verification**

Run:
```bash
go vet ./... && go vet -tags realclaude ./internal/fakeapi/ && go test -race ./... && CGO_ENABLED=0 go build ./cmd/claude-teleport/ ./test/fakeapi-server/
```
Expected: all green; the binary builds statically.

- [ ] **Step 2: Open the PR**

```bash
git push -u origin plan-02-transport
gh pr create --title "Plan 02: transport (sshx, remote, transfer, job, fakeapi, cli)" --body "Implements docs/superpowers/plans/2026-08-27-claude-teleport-02-transport.md. Spec sections 4.2, 4.3, 6 (journal/runner), 7, 12 (fakeapi)."
```

---

## Interface additions (relative to `00-interfaces.md`)

Recorded per the interfaces doc's rule; Plan 03 may rely on these names.

`internal/sshx`
- `Options.Home string` — home dir for `~` expansion in identity files (packages never call `os.UserHomeDir`).
- `Options.NetDial func(ctx, network, addr string) (net.Conn, error)` — first-hop dialer; tests inject a recorder to prove the final host is never dialled locally.
- `(*Client).SSH() *ssh.Client` — the underlying client (Plan 03's `RunPtyResume` uses `RequestPty`).
- `func Redial(ctx, attempts int, backoff time.Duration, logf, dial func(ctx) (*Client, error)) (*Client, error)`.
- `var ErrProxyCommand`, `var ErrPassphrase`.
- Package `internal/sshx/sshtest`: `GenKey`, `WriteKeyFile`, `KnownHostsLine`, `ExecFunc`, `Options`, `Server{Addr, HostKey}`, `New`, `(*Server).Close/Forwarded/Execs`.

`internal/remote`
- `plan03_types.go` aliases: `GitInfo`, `GitDestState`, `GitPlan`, `TmuxFacts`, `TmuxPlan`, `TmuxPaneState` (= `json.RawMessage`), `TmuxDialer` (= `any`). Plan 03 replaces the file with aliases to the real `gitx`/`tmuxx` types; the `Endpoint` method signatures are written against these alias names so they do not change.
- `func Unavailable(op string) *Error`.
- Op name constants `OpHello … OpRecord` and the args/result structs in `ops.go`.
- `(*Local).PutInstallExtras(ctx, jobID string, extra transfer.InstallExtras) error` and `(*Client).PutInstallExtras(...)` (op `install-extras`); `Local.Install` reads `jobs/<id>/extras.json`. Plan 03's orchestrator calls it before `Install` via `interface{ PutInstallExtras(...) }`.
- `(*Local).Hostname string`; `func NewClientConn(ctx, conn io.ReadWriteCloser, openStream func(...), logf) (*Client, error)`; `(*Client).Info() HostInfo`.
- `Local.ManifestDiff` persists the manifest to `jobs/<id>/manifest.json` on the destination (the tar receiver needs it).

`internal/transfer`
- `Manifest.TmpDir string \`json:"-"\`` — where `Send` writes rewritten temp files (orchestrator sets it to the job dir).
- `Build` takes `Size` from the bytes it hashes and, when the `FileEntry` has zero `Mode`/`ModTime`, `Mode`/`ModTime` from `os.Stat` of the source file (Plan 03's capture entry relies on this).
- `Entry.IsDir/IsSymlink/IsRegular() bool`; `func StagedPath(stagingDir string, id int) string`; `func HashFile(path string) (string, int64, error)`; `var ErrForbidden`.
- `func Uninstall(m *Manifest, p session.Paths) (removed []string, err error)`.
- Staging metadata files: `<id>.dir` (directories), `<id>.symlink` (link target), `<id>.part` (in flight).
- Status semantics table (Task 11) — `StagedSame` means "verified in staging, destination absent".

`internal/job`
- `HistoryRecord.From` / `.To` are two fields with tags `json:"from"` / `json:"to"` (the interfaces doc's one-line tag is not legal Go).

`internal/cli`
- `func AddTransportCommands(root *cobra.Command)`; `var RunnerSteps func(*job.Journal, func(string, ...any)) ([]job.Step, error)`; `func envPaths(env []string) (session.Paths, error)`; `func dialTarget(...)`; `func openRemote(...)`; `func addInspectHost(root)`; `fail(code, format, ...)` = Plan 01's `cli.Exit` (returns `*cli.ExitError`); `envValue` is Plan 01's; `Main` gains the `cmdEnvKey{}` context value.

`internal/fakeapi`
- `func RunClaude(ctx, baseURL, configDir, cwd string, args ...string) ([]byte, []byte, error)`; `ENDPOINTS.md` is the pinned spike record; build tag `realclaude` for the live test; binary `test/fakeapi-server`.

---

## Self-review

**Spec coverage (sections in scope)**

| Spec | Task(s) |
|---|---|
| §4.2 target string, ssh_config aliases/HostName/User/Port/IdentityFile/ProxyJump, `--via`, `-o` | 1, 2, 19 |
| §4.2 agent + `~/.ssh/id_*` auth, known_hosts, accept-new, fingerprint on unknown host | 4 |
| §4.2 jump chain resolved by the previous hop, never locally | 5, 6 (recorder test) |
| §4.2 ProxyCommand → error suggesting `--via` | 2 |
| §4.2 one connection per endpoint, channels multiplexed, re-dial with bounded retries | 6 (`Redial`), 16 (streams on new sessions of the same connection), 19 (`dialTarget` uses `Redial`) |
| §4.3 newline-delimited JSON request/response, ids, stderr = remote log | 14, 16 |
| §4.3 bulk data on separate channels (`remote stream <kind> <job> <id>`) | 15, 16, 19 |
| §4.3 `hello` first, protocol mismatch aborts with both versions | 14, 16, 22 (exit 4) |
| §4.3 op list: hello, inventory-host, inventory-session, inventory-git, inventory-tmux, manifest-diff, stream, install, git-attach, tmux-open, tmux-capture, tmux-keys, claude-start/confirm/exit, freeze, thaw, shape-state, job-journal-get/put, record | 14 (all names in `ops.go`), 15 (implemented vs explicit `unavailable` stubs) |
| §6 job dir `jobs/<sid>/` with job.json, log.txt, manifest.json, capture.txt; detached runner; continue at first incomplete step; every step re-verifies | 7, 8, 19 (`internal-runner`) |
| §6 `status`/`continue`/`abandon` commands (status, abandon here; `continue` is Plan 03 with the orchestrator) | 20, 21 |
| §6 step 10 history record on both hosts | 8 (`AppendHistory`), 15 (`Record`) |
| §7.1 manifest fields, symlinks recorded, forbidden list enforced + tested with every forbidden path | 10 |
| §7.2 rewriting applied to JSON session files (hash of rewritten content) | 10, 12 |
| §7.3 diff statuses, ff-candidate for same-session prefix, `--force` for same session only | 11 |
| §7.4 one gzip tar in manifest order, `.part` + verify + rename, lose at most one entry, staging under `$XDG_DATA_HOME/claude-teleport/staging/<sid>/` | 12, 15, 21 |
| §7.5 install rules and merges (index, history, `~/.claude.json` project entry, memory copy-if-absent) | 13 |
| §12 fakeapi endpoints, request logging, spike pinned by a real-binary test | 17, 18 |
| §12 in-process ssh server with a fake "DNS" only the jump knows | 3, 6 |
| §14 known_hosts verification; job dirs 0700 | 4, 7 |
| §5 `compare-config <host>`, `inspect --host`, exit codes 2/4 | 22 |

Gaps deliberately left to Plan 03 (documented as explicit `unavailable` stubs or absent commands): git/tmux/claude ops, `continue`, the teleport root command, remote-side `abandon --delete-destination-files`.

**Placeholder scan** — searched for "TBD", "TODO", "implement later", "fill in", "add appropriate", "handle edge cases", "similar to Task": none remain. Every code step carries complete Go; the only cross-plan adaptation points are named explicitly (Plan 01's root builder, its env/exit helpers, `session.Munge`).

**Type consistency** — checked across tasks: `sshx.Options{KnownHostsFile, AgentSocket, StrictHostKey, ConnectTimeout, Logf, Home, NetDial}` (Tasks 5, 6, 16, 19); `transfer.StagedPath` (11, 12, 13, 15); `Status` names (11–13, 15, 16); `remote.Error{Code, Message}` and `Unavailable` (14–16, 22); `job.Journal` field names (7, 8, 15, 20, 21); `Endpoint` method signatures identical in `endpoint.go`, `Local` and `Client` (14–16); `HistoryRecord{At, SessionID, Direction, From, To, Outcome, Note}` (8, 15); `envPaths`/`envOf`/`fail` (19–22).

**Verified assumptions about libraries** — `ssh.MarshalPrivateKey`/`MarshalPrivateKeyWithPassphrase` (x/crypto ≥ 0.12), `ssh.PassphraseMissingError`, `knownhosts.New/Line/Normalize/KeyError`, `ssh_config.Decode/(*Config).Get/GetAll`, `context.AfterFunc` (Go ≥ 1.21), `tar.Reader` returning `io.ErrUnexpectedEOF` on truncation. If a signature differs in the pinned version, adjust the call, not the test's expectation.

