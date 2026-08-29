# claude-teleport Plan 01 — Foundation and Local Model Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the `claude-teleport` module skeleton, CLI, the whole local on-disk model of a Claude Code session (`internal/session`, `internal/claudecfg`, `internal/procx`, `internal/placeholder`), the `test/fakeclaude` binary, and the CI/release pipeline — everything Plans 02 (transport) and 03 (git, tmux, orchestrator) build on.

**Architecture:** One static Go binary. `internal/cli` (cobra) resolves defaults from the environment and hands explicit directories to pure library packages. `internal/session` knows the Claude Code file layout (transcripts, registry, index, history, `~/.claude.json`) and the path-rewrite engine; `internal/claudecfg` inventories and compares host configuration; `internal/procx` reads `/proc`, owns the SIGSTOP/SIGCONT freezer helper and detached spawning; `internal/placeholder` is the confirm-before-resume pane command. In this plan the root teleport command parses every flag from spec §5 and fails with "transport not implemented yet" (exit 2); Plans 02/03 replace that stub.

**Tech Stack:** Go 1.26 (`CGO_ENABLED=0`), `github.com/spf13/cobra`, `github.com/google/go-cmp` (tests only), stdlib everywhere else. GitHub Actions, nfpm, python3 (packaging helper).

**Spec:** `docs/superpowers/specs/2026-08-27-claude-teleport-design.md` — this plan implements §3, §5 (local commands), §10, §11, §12 (unit tests + fakeclaude), §13.

**Interfaces:** `docs/superpowers/plans/2026-08-27-claude-teleport-00-interfaces.md` — every exported name below matches it; additions are listed at the end under "Interface additions".

**Workflow:** Work in a git worktree: `git worktree add .worktrees/plan-01-foundation -b plan-01-foundation main` and `cd .worktrees/plan-01-foundation`. One small commit per task (conventional-commit messages, as written in each task's last step). When every task is done, push the branch and open one PR titled `Plan 01: foundation and local model` into `main`. After each task run `go vet ./... && go test -race ./internal/... ./test/...` — it must be green before the commit.

## Global Constraints

- Module `github.com/mithro/go-claude-teleport`; binary `claude-teleport`; `go 1.26`; `CGO_ENABLED=0`; Apache-2.0.
- Dependencies in this plan: `github.com/spf13/cobra`, `github.com/google/go-cmp` (tests). Nothing else.
- No `ssh`, `rsync`, `tar`, `gzip`, `git` subprocesses in the tool. `tmux -C` and `claude --version` (preflight/doctor only) are the only subprocesses the tool may run. (`test/fakeclaude` may run `tmux display-message`; it is a test program, not the tool.)
- Never read `.credentials.json`, `sessions/*.key`, or token fields. No task in this plan touches the real `~/.claude`.
- Every exported function that touches the filesystem takes explicit directories (config dir, home, data dir) — never `os.UserHomeDir()` inside a package; only `internal/cli` resolves defaults from the environment.
- Errors wrap with `%w` and carry the path/pid/op involved. No silent fallbacks: a missing prerequisite is an error, not a default.
- Tests: stdlib `testing` + `go-cmp`; fixtures in `testdata/`; sanitised paths (`/home/alice`), hosts (`*.example`), fresh uuids; no real hostnames, usernames or prompts.
- Dates in docs/logs: ISO 8601.
- Exit codes (spec §5): 0 ok; 1 failed; 2 usage; 3 refused; 4 unreachable; 5 not resumed; 6 interrupted.
- Pinned Claude Code version string everywhere (fixtures, fakeclaude default, CI): `2.1.247`. (The fixtures were captured from 2.1.247; 2.1.251 was checked and the format is unchanged — mention that only in code comments.)

## Fixture identities used throughout this plan

| Thing | Value |
|---|---|
| user / home | `alice`, `/home/alice` |
| config dir | `/home/alice/.claude` |
| session cwd (a linked worktree) | `/home/alice/github/example/widget` |
| munged project dir | `-home-alice-github-example-widget` |
| session id A | `3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13` |
| session id B (second session, same project) | `a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d` |
| sub-agent id | `agent-0f8e7d6c` |
| branch | `feature/teleport` |
| Claude version | `2.1.247` |
| hosts | `laptop.example` (source), `big-storage.example` (dest), `jump.example` |

## File structure

```
go.mod, go.sum
cmd/claude-teleport/main.go              os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Environ()))
internal/version/version.go              Version, Protocol
internal/cli/
  cli.go          Main, app, exit codes, ExitError, env parsing, Paths resolution
  root.go         root teleport command: every spec §5 flag; stub error in this plan
  help.go         long help text (spec §5)
  version.go      `version`
  placeholder.go  `placeholder` (chdir + exec)
  freezer.go      `internal-freezer` (RunFreezerHelper entry)
  inspect.go      `inspect`
  list.go         `list`
  compare.go      `compare-config` (local-dir mode in this plan)
  doctor.go       `doctor` (local checks)
internal/session/
  id.go           ID, ParseID, Short, IsUUID
  paths.go        Munge, Paths, NewPaths
  registry.go     Registry, ReadRegistry, ReadRegistryFile, TmuxParts, State
  proc.go         ProcRoot, ProcAlive, ProcStartTime (tiny /proc reader; procx has the full table)
  argv.go         ArgvSessionID (placeholder / claude argv recognition — shared with procx)
  meta.go         Meta, ReadMeta, Label
  selector.go     Selector, Env, ParseSelector
  resolve.go      PaneProbe, PaneInfo, Session, TmuxRef, Resolve, Load, FindTranscript, ErrNotFound
  inventory.go    Category, FileEntry, Inventory, Skipped, InventoryFiles, Forbidden
  usage.go        Usage, ScanUsage
  pathmap.go      Mapping, PathMap, NewPathMap, ParseMappings, Apply, ApplyPath
  rewrite.go      RewriteStats, RewriteJSONL, RewriteJSON
  prefix.go       IsPrefix
  index.go        IndexEntry, ReadIndexEntry, MergeIndexEntry
  history.go      ExtractHistory, AppendHistory
  global.go       ProjectEntry, ReadProjectEntry, AddProjectEntry
  testdata/       a sanitised config-dir tree (Tasks 4, 5, 8, 11)
internal/claudecfg/
  collect.go      PluginInfo, Permissions, Inventory, Collect, TreeHash, FileHash
  compare.go      Class, Difference, Report, Compare, Downgrade, Render, JSON
  testdata/       two config dirs: src/ and dst/
internal/procx/
  table.go        Proc, Table, Scan, Get, Children, Subtree, Alive, StartTime
  registry.go     RegistryForPID, RegistryForSession
  argv.go         IsPlaceholderArgv, IsClaudeArgv
  freezer.go      Freezer, Freeze, Thaw, RunFreezerHelper
  wait.go         WaitGone
  spawn.go        SpawnDetached
  testdata/proc/  stat + cmdline fixtures; testdata/sessions/
internal/placeholder/placeholder.go     Options, Decision, Decide, Render, ChdirTarget
test/fakeclaude/main.go                 the fake `claude`
test/fakeclaude/harness/harness.go      Build(t) → dir containing `claude` (used by other packages' tests)
test/fakeclaude/fakeclaude_test.go      behaviour tests
.github/workflows/test.yml, release.yml
nfpm.yaml, packaging/version.py, packaging/version_test.py
README.md
```

---

### Task 1: Module skeleton, `version`, exit codes, `Main`

**Files:**
- Create: `go.mod`, `cmd/claude-teleport/main.go`, `internal/version/version.go`, `internal/cli/cli.go`, `internal/cli/version.go`
- Test: `internal/cli/cli_test.go`

**Interfaces:**
- Produces: `cli.Main(args []string, stdin io.Reader, stdout, stderr io.Writer, env []string) int`; exit constants `ExitOK…ExitInterrupted`; `type ExitError struct{ Code int; Err error }`; `func Exit(code int, format string, a ...any) error`; the `app` struct (`stdin`, `stdout`, `stderr`, `env map[string]string`) that every later cli task hangs commands on via `a.rootCmd()`; `version.Version`, `version.Protocol`.

- [ ] **Step 1: Create the module**

```bash
go mod init github.com/mithro/go-claude-teleport
go get github.com/spf13/cobra@latest github.com/google/go-cmp@latest
```

Then edit `go.mod` so the `go` directive reads exactly `go 1.26` (no patch suffix). `go mod tidy` runs at the end of the task.

- [ ] **Step 2: Write the failing test**

`internal/cli/cli_test.go`:

```go
package cli

import (
	"bytes"
	"strings"
	"testing"
)

func run(t *testing.T, env []string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Main(args, strings.NewReader(""), &out, &errb, env)
	return code, out.String(), errb.String()
}

func TestVersionCommand(t *testing.T) {
	code, out, _ := run(t, nil, "version")
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasPrefix(out, "claude-teleport dev (protocol 1)") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestUnknownFlagIsUsageError(t *testing.T) {
	code, _, stderr := run(t, nil, "--definitely-not-a-flag")
	if code != ExitUsage {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stderr, "claude-teleport:") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestExitErrorCodePropagates(t *testing.T) {
	err := Exit(ExitRefused, "drift on %s", "hooks")
	var ee *ExitError
	if !asExit(err, &ee) || ee.Code != ExitRefused || ee.Error() != "drift on hooks" {
		t.Fatalf("got %#v", err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/cli/ -run 'TestVersionCommand|TestUnknownFlag|TestExitError' -v`
Expected: FAIL — `undefined: Main`, `undefined: ExitOK`.

- [ ] **Step 4: Write the implementation**

`internal/version/version.go`:

```go
// Package version holds the build version (set by -ldflags) and the remote
// helper protocol version.
package version

// Version is "dev" for local builds; the release workflow sets it to the
// vX.Y tag with -ldflags "-X github.com/mithro/go-claude-teleport/internal/version.Version=vX.Y".
var Version = "dev"

// Protocol is the remote helper protocol version (spec §4.3).
const Protocol = 1
```

`cmd/claude-teleport/main.go`:

```go
package main

import (
	"os"

	"github.com/mithro/go-claude-teleport/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Environ()))
}
```

`internal/cli/cli.go`:

```go
// Package cli is the cobra command tree. It is the only package that reads
// the environment; every other package receives explicit directories.
package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// Exit codes (spec §5).
const (
	ExitOK          = 0
	ExitFailed      = 1
	ExitUsage       = 2
	ExitRefused     = 3
	ExitUnreachable = 4
	ExitNotResumed  = 5
	ExitInterrupted = 6
)

// ExitError carries the process exit code for an error.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// Exit builds an ExitError with a formatted message.
func Exit(code int, format string, a ...any) error {
	return &ExitError{Code: code, Err: fmt.Errorf(format, a...)}
}

func asExit(err error, target **ExitError) bool { return errors.As(err, target) }

// app is the per-invocation state shared by every command.
type app struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	env    map[string]string
}

func parseEnv(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// Main runs the CLI and returns the process exit code. It never calls
// os.Exit, so tests drive it directly.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, env []string) int {
	a := &app{stdin: stdin, stdout: stdout, stderr: stderr, env: parseEnv(env)}
	root := a.rootCmd()
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.Execute()
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		fmt.Fprintln(stderr, "claude-teleport:", ee.Err)
		return ee.Code
	}
	fmt.Fprintln(stderr, "claude-teleport:", err)
	return ExitUsage
}

// rootCmd builds the command tree. Task 20 replaces the bare root with the
// full teleport command; every other task adds one subcommand here.
func (a *app) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "claude-teleport",
		Short:         "move an in-progress Claude Code session to another machine",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	root.AddCommand(a.versionCmd())
	return root
}
```

`internal/cli/version.go`:

```go
package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/version"
)

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print the claude-teleport version and protocol number",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintf(a.stdout, "claude-teleport %s (protocol %d) %s/%s\n",
				version.Version, version.Protocol, runtime.GOOS, runtime.GOARCH)
			return err
		},
	}
}
```

- [ ] **Step 5: Run tests and build**

Run: `go mod tidy && go vet ./... && go test -race ./... && CGO_ENABLED=0 go build -o /dev/null ./cmd/claude-teleport`
Expected: PASS; the build succeeds.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum cmd internal
git commit -m "feat(cli): module skeleton, version command, exit codes"
```

---

### Task 2: CI, release pipeline, packaging and README skeleton

**Files:**
- Create: `.github/workflows/test.yml`, `.github/workflows/release.yml`, `nfpm.yaml`, `packaging/version.py`, `packaging/version_test.py`
- Modify: `README.md`

**Interfaces:**
- Consumes: `internal/version.Version` (ldflags target).
- Produces: nothing for Go code; the pipeline Plans 02/03 keep.

- [ ] **Step 1: Write the packaging helper test (copied from go-tmux-saver)**

`packaging/version_test.py`:

```python
"""Unit tests for packaging/version.py (pure helpers + a git fixture repo)."""
import os
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(__file__))
import version  # noqa: E402


class PureHelpers(unittest.TestCase):
    def test_parse_tag(self):
        self.assertEqual(version.parse_tag("v0.1"), (0, 1))
        self.assertEqual(version.parse_tag("v12.345"), (12, 345))
        self.assertIsNone(version.parse_tag("v1.2.3"))
        self.assertIsNone(version.parse_tag("v0.1-3-gabc1234"))
        self.assertIsNone(version.parse_tag("0.1"))

    def test_next_patch(self):
        self.assertEqual(version.next_patch(None), "v0.1")
        self.assertEqual(version.next_patch("v0.1"), "v0.2")
        self.assertEqual(version.next_patch("v0.9"), "v0.10")
        self.assertEqual(version.next_patch("v3.41"), "v3.42")

    def test_deb_version(self):
        self.assertEqual(version.deb_version("v0.2"), "0.2")
        with self.assertRaises(ValueError):
            version.deb_version("v0.1-3-gabc1234")


class GitRepo(unittest.TestCase):
    """Exercise the git-reading helpers against a throwaway repository."""

    def setUp(self):
        self.dir = tempfile.mkdtemp(prefix="ct-version-", dir=os.getcwd())
        self.addCleanup(lambda: subprocess.run(["rm", "-r", self.dir], check=True))
        self.cwd = os.getcwd()
        os.chdir(self.dir)
        self.addCleanup(os.chdir, self.cwd)
        env = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@x",
               "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@x"}
        self.env = env
        subprocess.run(["git", "init", "-q", "-b", "main"], check=True, env=env)
        self.commit("one")

    def commit(self, msg):
        subprocess.run(["git", "commit", "-q", "--allow-empty", "-m", msg],
                       check=True, env=self.env)

    def tag(self, name):
        subprocess.run(["git", "tag", name], check=True, env=self.env)

    def test_no_tags_starts_at_v0_1(self):
        self.assertIsNone(version.exact_tag())
        self.assertIsNone(version.latest_reachable_tag())
        self.assertEqual(version.head_tag(), "v0.1")

    def test_exact_tag_wins(self):
        self.tag("v0.1")
        self.assertEqual(version.exact_tag(), "v0.1")
        self.assertEqual(version.head_tag(), "v0.1")

    def test_next_patch_after_untagged_commit(self):
        self.tag("v0.1")
        self.commit("two")
        self.assertIsNone(version.exact_tag())
        self.assertEqual(version.latest_reachable_tag(), "v0.1")
        self.assertEqual(version.head_tag(), "v0.2")

    def test_non_matching_tags_ignored(self):
        self.tag("v0.0")
        self.tag("latest")
        self.commit("two")
        self.tag("v0.1-rc")  # not vX.Y
        self.assertEqual(version.head_tag(), "v0.1")


if __name__ == "__main__":
    unittest.main()
```

- [ ] **Step 2: Run it to verify it fails**

Run: `python3 -m unittest discover -s packaging -p 'version_test.py' -v`
Expected: FAIL — `ModuleNotFoundError: No module named 'version'`.

- [ ] **Step 3: Write `packaging/version.py`**

```python
#!/usr/bin/env python3
"""Release version helpers for the GitHub Actions release pipeline.

Versions come from git tags of the form vXX.ZZZ (the repo's tag ruleset).

    version.py tag        -> the tag for HEAD: the exact vX.Y tag if HEAD is
                             tagged, otherwise the next patch tag (latest
                             reachable vX.Y with Y+1; v0.1 when no tag exists)
    version.py is-tagged  -> exit 0 if HEAD carries an exact vX.Y tag, else 1
    version.py deb TAG    -> Debian version for TAG (v0.2 -> 0.2)

stdlib only; runs on the CI runner's python3.
"""
import re
import subprocess
import sys

TAG_RE = re.compile(r"^v(\d+)\.(\d+)$")


def git(*args):
    return subprocess.run(["git", *args], capture_output=True, text=True,
                          check=True).stdout.strip()


def parse_tag(tag):
    """Return (major, patch) for a vX.Y tag, or None."""
    m = TAG_RE.match(tag)
    return (int(m.group(1)), int(m.group(2))) if m else None


def next_patch(latest):
    """Next patch tag after `latest` (a vX.Y tag or None)."""
    if latest is None:
        return "v0.1"
    major, patch = parse_tag(latest)
    return f"v{major}.{patch + 1}"


def deb_version(tag):
    """Debian version string for a vX.Y tag."""
    if parse_tag(tag) is None:
        raise ValueError(f"not a vX.Y tag: {tag!r}")
    return tag[1:]


def exact_tag():
    """The vX.Y tag pointing at HEAD, or None."""
    try:
        out = git("tag", "--points-at", "HEAD")
    except subprocess.CalledProcessError:
        return None
    tags = sorted((t for t in out.splitlines() if parse_tag(t)), key=parse_tag)
    return tags[-1] if tags else None


def latest_reachable_tag():
    """The highest vX.Y tag reachable from HEAD, or None."""
    out = git("tag", "--merged", "HEAD")
    tags = sorted((t for t in out.splitlines() if parse_tag(t)), key=parse_tag)
    return tags[-1] if tags else None


def head_tag():
    return exact_tag() or next_patch(latest_reachable_tag())


def main(argv):
    if len(argv) < 2:
        print(__doc__, file=sys.stderr)
        return 2
    cmd = argv[1]
    if cmd == "tag":
        print(head_tag())
    elif cmd == "is-tagged":
        return 0 if exact_tag() else 1
    elif cmd == "deb":
        if len(argv) != 3:
            print("usage: version.py deb TAG", file=sys.stderr)
            return 2
        print(deb_version(argv[2]))
    else:
        print(f"unknown command {cmd!r}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
```

- [ ] **Step 4: Run the packaging tests**

Run: `python3 -m unittest discover -s packaging -p 'version_test.py' -v`
Expected: 7 tests OK.

- [ ] **Step 5: Write the workflows and nfpm config**

`.github/workflows/test.yml`:

```yaml
name: test
on:
  push:
    branches: [main]
  pull_request:
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: sudo apt-get update && sudo apt-get install -y tmux
      - run: go vet ./...
      - run: go test -race ./...
      - name: packaging helper tests
        run: python3 -m unittest discover -s packaging -p 'version_test.py' -v
```

`.github/workflows/release.yml`:

```yaml
name: release

# Every push to main is a release:
#   1. test   — full suite (tmux installed for the integration tests)
#   2. version — HEAD's vX.Y tag if it has one, else the next patch tag
#                (latest reachable vX.Y + 1), created and pushed here
#   3. build  — static linux amd64 + arm64 binaries (-X version.Version=<tag>)
#                and nfpm .debs (version X.Y)
#   4. release — GitHub Release <tag> with binaries, debs, checksums
#   5. apt    — signed apt repository on GitHub Pages
# Tags are always vXX.ZZZ (the repo's tag ruleset); git describe on a released
# commit is therefore exactly the release tag.

on:
  push:
    branches: [main]
  workflow_dispatch:

permissions:
  contents: read

# Serialise releases so two quick pushes never compute the same next tag.
concurrency:
  group: release-main
  cancel-in-progress: false

env:
  NFPM_VERSION: 2.41.1
  GO_MODULE: github.com/mithro/go-claude-teleport

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: sudo apt-get update && sudo apt-get install -y tmux
      - run: go vet ./...
      - run: go test -race ./...
      - name: packaging helper tests
        run: python3 -m unittest discover -s packaging -p 'version_test.py' -v

  version:
    needs: test
    runs-on: ubuntu-latest
    permissions:
      contents: write   # push the release tag
    outputs:
      tag: ${{ steps.v.outputs.tag }}
      deb: ${{ steps.v.outputs.deb }}
      created: ${{ steps.v.outputs.created }}
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
          fetch-tags: true
      - name: Decide the release tag
        id: v
        run: |
          set -euo pipefail
          TAG="$(python3 packaging/version.py tag)"
          DEB="$(python3 packaging/version.py deb "$TAG")"
          if python3 packaging/version.py is-tagged; then
            echo "HEAD already tagged $TAG"
            echo "created=false" >> "$GITHUB_OUTPUT"
          else
            git config user.name "github-actions[bot]"
            git config user.email "github-actions[bot]@users.noreply.github.com"
            git tag -a "$TAG" -m "Release $TAG"
            git push origin "refs/tags/$TAG"
            echo "created $TAG"
            echo "created=true" >> "$GITHUB_OUTPUT"
          fi
          echo "tag=$TAG" >> "$GITHUB_OUTPUT"
          echo "deb=$DEB" >> "$GITHUB_OUTPUT"
          echo "release tag: $TAG (deb $DEB)"

  build:
    needs: version
    runs-on: ubuntu-latest
    strategy:
      fail-fast: false
      matrix:
        arch: [amd64, arm64]
    env:
      TAG: ${{ needs.version.outputs.tag }}
      DEBVER: ${{ needs.version.outputs.deb }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Build static binary
        run: |
          set -euo pipefail
          mkdir -p dist
          CGO_ENABLED=0 GOOS=linux GOARCH=${{ matrix.arch }} \
            go build -trimpath \
              -ldflags "-s -w -X ${GO_MODULE}/internal/version.Version=${TAG}" \
              -o dist/claude-teleport ./cmd/claude-teleport
          ls -l dist/
      - name: Install nfpm
        run: |
          set -euo pipefail
          curl -fsSL -o /tmp/nfpm.deb \
            "https://github.com/goreleaser/nfpm/releases/download/v${NFPM_VERSION}/nfpm_${NFPM_VERSION}_amd64.deb"
          sudo dpkg -i /tmp/nfpm.deb
          nfpm --version
      - name: Build deb
        run: |
          set -euo pipefail
          VERSION="$DEBVER" ARCH="${{ matrix.arch }}" nfpm package -p deb -f nfpm.yaml -t dist/
          mv dist/claude-teleport "dist/claude-teleport-linux-${{ matrix.arch }}"
          ls -l dist/
          dpkg-deb -I dist/*_${{ matrix.arch }}.deb | head -20
          dpkg-deb -c dist/*_${{ matrix.arch }}.deb
      - uses: actions/upload-artifact@v4
        with:
          name: dist-${{ matrix.arch }}
          path: dist/

  release:
    needs: [version, build]
    runs-on: ubuntu-latest
    permissions:
      contents: write   # create the GitHub Release
    env:
      TAG: ${{ needs.version.outputs.tag }}
      GH_TOKEN: ${{ github.token }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/download-artifact@v4
        with:
          pattern: dist-*
          merge-multiple: true
          path: dist/
      - name: Checksums
        run: |
          set -euo pipefail
          cd dist
          sha256sum claude-teleport-linux-* *.deb > SHA256SUMS
          cat SHA256SUMS
      - name: Create or update the GitHub Release
        run: |
          set -euo pipefail
          if gh release view "$TAG" >/dev/null 2>&1; then
            gh release upload "$TAG" dist/* --clobber
          else
            gh release create "$TAG" dist/* \
              --title "$TAG" \
              --generate-notes \
              --notes "Static linux binaries (amd64, arm64), Debian packages and SHA256SUMS. Apt repository: https://mith.ro/go-claude-teleport/ (see index for setup)."
          fi

  apt:
    needs: [version, build]
    runs-on: ubuntu-latest
    permissions:
      pages: write
      id-token: write
    environment:
      name: github-pages
      url: ${{ steps.deploy.outputs.page_url }}
    env:
      TAG: ${{ needs.version.outputs.tag }}
    steps:
      - uses: actions/download-artifact@v4
        with:
          pattern: dist-*
          merge-multiple: true
          path: dist/
      - name: Assemble apt repo
        run: |
          set -euo pipefail
          sudo apt-get update
          sudo apt-get install -y --no-install-recommends dpkg-dev apt-utils
          mkdir -p apt-repo
          cp dist/*.deb apt-repo/
          cd apt-repo
          dpkg-scanpackages --multiversion . > Packages
          gzip -k Packages
          cat > Release <<EOF
          Origin: mithro-go-claude-teleport
          Label: mithro-go-claude-teleport
          Suite: stable
          Codename: trixie
          Architectures: amd64 arm64
          Components: main
          Description: go-claude-teleport ${TAG} — move Claude Code sessions between machines
          EOF
          apt-ftparchive release . >> Release
      - name: Sign repo
        env:
          GPG_PRIVATE_KEY: ${{ secrets.APT_GPG_PRIVATE_KEY }}
        if: env.GPG_PRIVATE_KEY != ''
        run: |
          set -euo pipefail
          echo "$GPG_PRIVATE_KEY" | gpg --batch --import
          cd apt-repo
          gpg --batch --yes --armor --detach-sign --output Release.gpg Release
          gpg --batch --yes --armor --clearsign --output InRelease Release
          gpg --batch --yes --armor --export > go-claude-teleport.gpg
      - name: Index page
        run: |
          cat > apt-repo/index.html <<HTMLEOF
          <!DOCTYPE html>
          <html><head><meta charset="utf-8"><title>mithro go-claude-teleport apt repository</title></head>
          <body>
          <h1>mithro go-claude-teleport apt repository</h1>
          <p><a href="https://github.com/mithro/go-claude-teleport">go-claude-teleport</a>
          — move an in-progress Claude Code session (transcript, git worktree,
          tmux window) to another machine over ssh.
          Current release: <b>${TAG}</b> (amd64, arm64). Every push to
          <code>main</code> publishes a new release here and on
          <a href="https://github.com/mithro/go-claude-teleport/releases">GitHub Releases</a>.</p>
          <h2>Setup</h2>
          <pre>
          curl -fsSL https://mith.ro/go-claude-teleport/go-claude-teleport.gpg \\
            | sudo tee /etc/apt/keyrings/mithro-go-claude-teleport.gpg > /dev/null
          echo "deb [signed-by=/etc/apt/keyrings/mithro-go-claude-teleport.gpg] https://mith.ro/go-claude-teleport/ ./" \\
            | sudo tee /etc/apt/sources.list.d/mithro-go-claude-teleport.list
          sudo apt update
          sudo apt install claude-teleport
          </pre>
          <p>Files: <a href="Packages">Packages</a> · <a href="Release">Release</a> ·
          <a href="InRelease">InRelease</a> · <a href="go-claude-teleport.gpg">signing key</a></p>
          </body></html>
          HTMLEOF
          sed -i 's/^          //' apt-repo/index.html
          ls -l apt-repo/
      - uses: actions/configure-pages@v5
      - uses: actions/upload-pages-artifact@v3
        with:
          path: apt-repo/
      - id: deploy
        uses: actions/deploy-pages@v4
```

`nfpm.yaml`:

```yaml
# nfpm packaging config — https://nfpm.goreleaser.com/configuration/
# VERSION and ARCH are expanded from the environment by nfpm at package time
# (nfpm does not expand them inside contents[].src, so the binary is always
# packaged from the fixed path ./dist/claude-teleport — build it there first):
#   VERSION=0.2 ARCH=arm64 nfpm package -p deb -f nfpm.yaml -t dist/
name: claude-teleport
arch: ${ARCH}
platform: linux
version: ${VERSION}
# Keep the Debian version exactly as given (X.Y from the vX.Y tag); without
# this nfpm re-renders it as semver (0.2 -> 0.2.0).
version_schema: none
section: utils
priority: optional
maintainer: Tim 'mithro' Ansell <me@mith.ro>
description: |
  Move an in-progress Claude Code session to another machine.
  Transfers the transcript and sidecar state, the git repository/worktree
  and the tmux window over ssh (in-binary ssh, git, tar and gzip), confirms
  the session resumed on the destination, and leaves the source resumable.
vendor: mithro
homepage: https://github.com/mithro/go-claude-teleport
license: Apache-2.0
recommends:
  - tmux (>= 3.3)
contents:
  - src: ./dist/claude-teleport
    dst: /usr/bin/claude-teleport
    file_info:
      mode: 0755
  - src: ./README.md
    dst: /usr/share/doc/claude-teleport/README.md
  - src: ./LICENSE
    dst: /usr/share/doc/claude-teleport/copyright
```

- [ ] **Step 6: Write the README skeleton**

Replace `README.md` with:

````markdown
# go-claude-teleport

`claude-teleport` moves one in-progress Claude Code session — everything
Claude Code needs to resume it, the git repository and worktree it was
working in, and the tmux window it was running in — from one machine to
another over ssh, confirms it resumed there, and leaves the source in a
state that can be resumed locally or teleported back later.

> **Not `claude --teleport`.** Anthropic's own `claude --teleport` moves a
> claude.ai *cloud* session into your terminal. `claude-teleport` (this
> tool) moves a *local* Claude Code session between two of your machines.
> The two are unrelated.

Status: under construction. Design: `docs/superpowers/specs/2026-08-27-claude-teleport-design.md`.

## Install

```sh
curl -fsSL https://mith.ro/go-claude-teleport/go-claude-teleport.gpg \
  | sudo tee /etc/apt/keyrings/mithro-go-claude-teleport.gpg > /dev/null
echo "deb [signed-by=/etc/apt/keyrings/mithro-go-claude-teleport.gpg] https://mith.ro/go-claude-teleport/ ./" \
  | sudo tee /etc/apt/sources.list.d/mithro-go-claude-teleport.list
sudo apt update && sudo apt install claude-teleport
```

Static binaries for linux amd64/arm64 are on the GitHub Releases page. Or
`go install github.com/mithro/go-claude-teleport/cmd/claude-teleport@latest`.
The same binary must be installed on both machines.

## Usage

```
claude-teleport [<session>] --to   <host> [--via <jump>]... [options]
claude-teleport [<session>] --from <host> [--via <jump>]... [options]
claude-teleport <tmux-session> <window> --to|--from <host> ...
claude-teleport continue <sid>            resume an interrupted job (default when re-running)
claude-teleport status  [<sid>]           journal and manifest of a job
claude-teleport abandon <sid> [--delete-destination-files]
claude-teleport inspect [<session>]       everything a teleport would move + drift report
claude-teleport list [--host <host>]      sessions here (running/suspended/idle) and teleport history
claude-teleport compare-config <host> [--session <session>]
claude-teleport doctor [<host>]           local (and remote) prerequisites
claude-teleport placeholder --resume <sid> [--saved-output F] [--now] [--teleported-to H]
claude-teleport version
claude-teleport remote …                  (internal)
```

`--teleport-to`/`--teleport-from` are the canonical spellings; `--to`/`--from`
are aliases. Exactly one of them is required for a teleport. Run it from
anywhere — including from *inside* the session being moved:
`! claude-teleport --to big-storage.example`.

### Session selector

`<session>` is resolved in this order:

1. absent → `$CLAUDE_CODE_SESSION_ID` if set (you are inside the session),
   else the Claude running in the current tmux pane, else an error listing
   candidates;
2. a full uuid;
3. a unique uuid prefix (≥ 4 hex chars) or a unique registry `name`;
4. two positional words `<tmux-session> <window>` (window by index or name);
   the pane's Claude (running) or placeholder (suspended) identifies the session.

With `--from`, selection runs on the remote (the source) with the same rules.

### Options

| Flag | Meaning |
|---|---|
| `--via HOST` | jump host(s), repeatable, outermost first; composes with `ProxyJump` |
| `-o KEY=VALUE` | ssh option override (User, Port, IdentityFile, StrictHostKeyChecking, …) |
| `--dest-path DIR` | put the session's cwd at DIR instead of the same path (implies a `--map`) |
| `--map SRC=DST` | extra path prefix rewrite, repeatable |
| `--state auto\|running\|suspended\|idle` | destination end state; `auto` preserves the source state |
| `--allow-config-drift` | turn blocking drift into warnings |
| `--force` | allow non-fast-forward replacement of an existing copy of this session on the destination |
| `--tmux-socket NAME` | destination socket name (default: same as source) |
| `--no-tmux` | do not use tmux on the destination even if present (end state must be `idle`) |
| `--exclude GLOB` | omit matching files from the repository transfer, repeatable |
| `--dry-run` | preflight and plan only; nothing touched, nothing frozen |
| `--exit-timeout D` / `--start-timeout D` | bounded waits (defaults 30s / 90s) |
| `--config-dir DIR` | local `CLAUDE_CONFIG_DIR` override |
| `--log FILE` | additional log destination |
| `--json` | machine-readable output for `status`, `list`, `inspect`, `compare-config` |
| `-v/--verbose`, `-q/--quiet` | log level |

### Exit codes

0 success; 1 teleport failed (job left resumable); 2 usage; 3 preflight
refused (drift, collision, unsupported state) — nothing touched; 4 remote
unreachable / version mismatch; 5 confirmation failed (destination Claude
did not resume — e.g. not logged in); 6 interrupted (job resumable).

## What moves, what never moves

Under `$CLAUDE_CONFIG_DIR` (default `~/.claude`):

| Path | Contents | Teleported? |
|---|---|---|
| `projects/<munged-cwd>/<sid>.jsonl` | the transcript | yes |
| `projects/<munged-cwd>/<sid>/subagents/`, `tool-results/` | sub-agent transcripts, large tool outputs | yes |
| `projects/<munged-cwd>/sessions-index.json` | session index | the session's entry is merged |
| `projects/<munged-cwd>/memory/` | project auto-memory | copied only if absent on the destination; otherwise diffed and reported |
| `file-history/<sid>/` | backups of edited files | yes |
| `tasks/<sid>/` | task list | yes (`.lock` excluded) |
| `session-env/<sid>/`, `todos/<sid>*.json` | per-session state | yes |
| `history.jsonl` | global prompt history | the session's lines are appended (deduped) |
| `sessions/<pid>.json` | registry of live Claude processes | **never** — read only |
| `sessions/*.key`, `.credentials.json` | tokens | **never** |
| `settings.json`, `plugins/`, `CLAUDE.md`, `agents/`, `skills/`, `commands/` | global configuration | compared, never copied |
| `shell-snapshots/`, `debug/`, `telemetry/`, `statsig/`, `cache/`, … | caches and diagnostics | no |

`~/.claude.json` (or `$CLAUDE_CONFIG_DIR/.claude.json` when that variable is
set): only `projects["<cwd>"]` is read; it is added on the destination if
absent (so the trust dialog and allow-list survive) and left alone
otherwise. Nothing else in that file is ever read or written.

The git repository/worktree and the tmux window are handled per the design
spec §8 and §9 (Plan 03 documents them here).

## Development

```sh
go vet ./... && go test -race ./...
python3 -m unittest discover -s packaging -p 'version_test.py'
```

Licence: Apache-2.0.
````

- [ ] **Step 7: Validate the YAML parses (optional, quick)**

Run: `python3 -c "import yaml; [yaml.safe_load(open(f)) for f in ['.github/workflows/test.yml','.github/workflows/release.yml','nfpm.yaml']]; print('ok')"` (if PyYAML is missing, skip — GitHub validates on push).
Expected: `ok`.

- [ ] **Step 8: Commit**

```bash
git add .github nfpm.yaml packaging README.md
git commit -m "ci: test and release workflows, nfpm packaging, README skeleton"
```

---

### Task 3: `session` IDs, `Munge`, `Paths`

**Files:**
- Create: `internal/session/id.go`, `internal/session/paths.go`
- Test: `internal/session/id_test.go`, `internal/session/paths_test.go`

**Interfaces:**
- Produces: `session.ID`, `ParseID`, `(ID).Short`, `IsUUID(s string) bool`, `Munge`, `Paths{Home, ConfigDir, GlobalJSON, DataDir}`, `NewPaths(home, configDirEnv, xdgDataHome string) Paths`, `(Paths).ProjectsDir/SessionsDir/HistoryFile/ProjectDir`.

Fact to encode (verified by the coordinator against Claude Code 2.1.251 with `claude --version`, `claude mcp list`, `claude config list` under `CLAUDE_CONFIG_DIR=<tmpdir>`: it created `<tmpdir>/.claude.json`, `<tmpdir>/projects/`, `<tmpdir>/sessions/`, `<tmpdir>/backups/`): **when `CLAUDE_CONFIG_DIR` is set, `.claude.json` lives inside that directory**; otherwise it is `$HOME/.claude.json`.

- [ ] **Step 1: Write the failing tests**

`internal/session/id_test.go`:

```go
package session

import "testing"

func TestParseID(t *testing.T) {
	id, err := ParseID("3F9C2B7E-5A14-4D8E-9B21-7C0E5D6A8F13")
	if err != nil {
		t.Fatal(err)
	}
	if id != "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13" {
		t.Fatalf("not lower-cased: %q", id)
	}
	if id.Short() != "3f9c2b7e" {
		t.Fatalf("Short = %q", id.Short())
	}
	for _, bad := range []string{"", "3f9c2b7e", "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f1", "not-a-uuid-at-all-really-not-a-uuid"} {
		if _, err := ParseID(bad); err == nil {
			t.Errorf("ParseID(%q) accepted", bad)
		}
	}
	if !IsUUID("3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13") || IsUUID("3f9c2b7e") {
		t.Fatal("IsUUID")
	}
}
```

`internal/session/paths_test.go`:

```go
package session

import "testing"

func TestMunge(t *testing.T) {
	cases := map[string]string{
		"/home/alice/github/x/.worktrees/y": "-home-alice-github-x--worktrees-y",
		"/home/alice/github/example/widget": "-home-alice-github-example-widget",
		"/":                                 "-",
	}
	for in, want := range cases {
		if got := Munge(in); got != want {
			t.Errorf("Munge(%q) = %q want %q", in, got, want)
		}
	}
}

func TestNewPathsDefaults(t *testing.T) {
	p := NewPaths("/home/alice", "", "")
	if p.ConfigDir != "/home/alice/.claude" || p.GlobalJSON != "/home/alice/.claude.json" ||
		p.DataDir != "/home/alice/.local/share/claude-teleport" {
		t.Fatalf("%+v", p)
	}
	if p.ProjectsDir() != "/home/alice/.claude/projects" || p.SessionsDir() != "/home/alice/.claude/sessions" ||
		p.HistoryFile() != "/home/alice/.claude/history.jsonl" {
		t.Fatalf("%+v", p)
	}
	if got := p.ProjectDir("/home/alice/github/example/widget"); got != "/home/alice/.claude/projects/-home-alice-github-example-widget" {
		t.Fatalf("ProjectDir = %q", got)
	}
}

// Verified against Claude Code 2.1.251: with CLAUDE_CONFIG_DIR set, Claude
// Code creates and uses <CLAUDE_CONFIG_DIR>/.claude.json, not $HOME/.claude.json.
func TestNewPathsWithConfigDir(t *testing.T) {
	p := NewPaths("/home/alice", "/srv/cfg", "/srv/xdg")
	if p.ConfigDir != "/srv/cfg" || p.GlobalJSON != "/srv/cfg/.claude.json" || p.DataDir != "/srv/xdg/claude-teleport" {
		t.Fatalf("%+v", p)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/session/ -run 'TestParseID|TestMunge|TestNewPaths' -v`
Expected: FAIL — `undefined: ParseID`, `undefined: Munge`, `undefined: NewPaths`.

- [ ] **Step 3: Implement**

`internal/session/id.go`:

```go
// Package session knows the on-disk model of a Claude Code session (spec §3):
// where its files are, how to find it, what it used, and how to rewrite its
// paths for another host.
package session

import (
	"fmt"
	"regexp"
	"strings"
)

// ID is a canonical (lower-case) session uuid.
type ID string

var uuidRe = regexp.MustCompile(`\A[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\z`)

// IsUUID reports whether s is a full (lower-case) uuid.
func IsUUID(s string) bool { return uuidRe.MatchString(s) }

// ParseID accepts a full uuid in any case and returns it lower-cased.
func ParseID(s string) (ID, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !IsUUID(s) {
		return "", fmt.Errorf("not a session id (full uuid expected): %q", s)
	}
	return ID(s), nil
}

// Short is the first 8 characters, for banners and logs.
func (id ID) Short() string {
	if len(id) < 8 {
		return string(id)
	}
	return string(id[:8])
}
```

`internal/session/paths.go`:

```go
package session

import (
	"path/filepath"
	"strings"
)

// Munge mirrors Claude Code's project-dir naming: '/' and '.' become '-'.
func Munge(absPath string) string {
	return strings.NewReplacer("/", "-", ".", "-").Replace(absPath)
}

// Paths resolves the on-disk locations for one config dir / home pair.
type Paths struct {
	Home       string // $HOME on this host
	ConfigDir  string // ~/.claude or $CLAUDE_CONFIG_DIR
	GlobalJSON string // ~/.claude.json, or <ConfigDir>/.claude.json when CLAUDE_CONFIG_DIR is set
	DataDir    string // claude-teleport data dir (jobs/, staging/)
}

// NewPaths computes Paths from the three environment inputs. configDirEnv is
// $CLAUDE_CONFIG_DIR ("" = unset); xdgDataHome is $XDG_DATA_HOME ("" = unset).
//
// Verified against Claude Code 2.1.251 (format unchanged since 2.1.247):
// when CLAUDE_CONFIG_DIR is set, Claude Code creates and reads `.claude.json`
// INSIDE that directory ($CLAUDE_CONFIG_DIR/.claude.json) alongside
// projects/, sessions/ and backups/; $HOME/.claude.json is untouched.
// Without the variable the file is $HOME/.claude.json, next to $HOME/.claude/.
func NewPaths(home, configDirEnv, xdgDataHome string) Paths {
	p := Paths{Home: home}
	if configDirEnv != "" {
		p.ConfigDir = configDirEnv
		p.GlobalJSON = filepath.Join(configDirEnv, ".claude.json")
	} else {
		p.ConfigDir = filepath.Join(home, ".claude")
		p.GlobalJSON = filepath.Join(home, ".claude.json")
	}
	if xdgDataHome == "" {
		xdgDataHome = filepath.Join(home, ".local", "share")
	}
	p.DataDir = filepath.Join(xdgDataHome, "claude-teleport")
	return p
}

func (p Paths) ProjectsDir() string { return filepath.Join(p.ConfigDir, "projects") }

// SessionsDir is the registry directory (read-only for us; never transferred).
func (p Paths) SessionsDir() string { return filepath.Join(p.ConfigDir, "sessions") }

func (p Paths) HistoryFile() string { return filepath.Join(p.ConfigDir, "history.jsonl") }

// ProjectDir is ProjectsDir()/Munge(cwd).
func (p Paths) ProjectDir(cwd string) string { return filepath.Join(p.ProjectsDir(), Munge(cwd)) }
```

- [ ] **Step 4: Run tests**

Run: `go test -race ./internal/session/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/session
git commit -m "feat(session): ids, munging, Paths (.claude.json lives in CLAUDE_CONFIG_DIR when set)"
```

---

### Task 4: Registry reading (`sessions/<pid>.json`) and `State`

**Files:**
- Create: `internal/session/registry.go`, `internal/session/testdata/config/sessions/41234.json`, `internal/session/testdata/config/sessions/41300.json`, `internal/session/testdata/config/sessions/41234.0a1b2c3d.key`, `internal/session/testdata/sessions-bad/9.json`
- Test: `internal/session/registry_test.go`

**Interfaces:**
- Produces: `State` (+`String`), `Registry` (fields per interfaces doc, `ProcStart` normalised to string), `ReadRegistry(sessionsDir string) ([]Registry, error)`, `ReadRegistryFile(path string) (Registry, error)`, `(Registry).TmuxParts() (sess, windowID, paneID string, ok bool)`.

- [ ] **Step 1: Write the fixtures**

`internal/session/testdata/config/sessions/41234.json` (string `procStart`, as 2.1.247 writes it):

```json
{"pid":41234,"sessionId":"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13","cwd":"/home/alice/github/example/widget","startedAt":1756289730123,"procStart":"123456","version":"2.1.247","kind":"interactive","entrypoint":"cli","tmux":"main:@3.%7","messagingSocketPath":"/home/alice/.claude/sessions/41234.sock","name":"widget","nameSource":"auto","status":"idle","updatedAt":1756289790456,"statusUpdatedAt":1756289790456}
```

`internal/session/testdata/config/sessions/41300.json` (numeric `procStart`, older writer; not in tmux):

```json
{"pid":41300,"sessionId":"a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d","cwd":"/home/alice/github/example/widget","startedAt":1756290000000,"procStart":234567,"version":"2.1.247","kind":"interactive","entrypoint":"cli","tmux":"","name":"","nameSource":"","status":"busy","updatedAt":1756290001000,"statusUpdatedAt":1756290001000}
```

`internal/session/testdata/config/sessions/41234.0a1b2c3d.key` — content is the single line `not-a-real-token-fixture` (the reader must never open it; the fixture proves it is skipped).

`internal/session/testdata/sessions-bad/9.json`:

```json
{"pid":9,"sessionId":"a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d","procStart":true}
```

- [ ] **Step 2: Write the failing test**

`internal/session/registry_test.go`:

```go
package session

import (
	"strings"
	"testing"
)

func TestReadRegistry(t *testing.T) {
	regs, err := ReadRegistry("testdata/config/sessions")
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 2 {
		t.Fatalf("got %d entries (the .key file must be ignored): %+v", len(regs), regs)
	}
	a, b := regs[0], regs[1]
	if a.PID != 41234 || a.SessionID != "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13" || a.ProcStart != "123456" ||
		a.Status != "idle" || a.Name != "widget" || a.Tmux != "main:@3.%7" || a.Version != "2.1.247" ||
		!strings.HasSuffix(a.File, "/41234.json") {
		t.Fatalf("a = %+v", a)
	}
	if b.PID != 41300 || b.ProcStart != "234567" || b.Tmux != "" {
		t.Fatalf("numeric procStart not normalised: %+v", b)
	}
	sess, win, pane, ok := a.TmuxParts()
	if !ok || sess != "main" || win != "@3" || pane != "%7" {
		t.Fatalf("TmuxParts = %q %q %q %v", sess, win, pane, ok)
	}
	if _, _, _, ok := b.TmuxParts(); ok {
		t.Fatal("empty tmux field must not parse")
	}
}

func TestReadRegistryMissingDirIsEmpty(t *testing.T) {
	regs, err := ReadRegistry(t.TempDir() + "/nope")
	if err != nil || len(regs) != 0 {
		t.Fatalf("%v %v", regs, err)
	}
}

func TestReadRegistryWrongTypedProcStartIsError(t *testing.T) {
	_, err := ReadRegistry("testdata/sessions-bad")
	if err == nil || !strings.Contains(err.Error(), "sessions-bad/9.json") {
		t.Fatalf("err = %v", err)
	}
}

func TestStateString(t *testing.T) {
	if StateIdle.String() != "idle" || StateRunning.String() != "running" || StateSuspended.String() != "suspended" {
		t.Fatal("State.String")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/session/ -run 'TestReadRegistry|TestState' -v`
Expected: FAIL — `undefined: ReadRegistry`.

- [ ] **Step 4: Implement `internal/session/registry.go`**

```go
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// State is where a session is right now (spec §5, §9).
type State int

const (
	StateIdle      State = iota // transcript on disk, no process, no placeholder pane
	StateRunning                // live claude process (registry entry)
	StateSuspended              // a pane whose foreground command is a placeholder
)

func (s State) String() string {
	switch s {
	case StateRunning:
		return "running"
	case StateSuspended:
		return "suspended"
	default:
		return "idle"
	}
}

// Registry is ~/.claude/sessions/<pid>.json (only the fields we use).
type Registry struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	ProcStart string `json:"procStart"` // string OR number in the file; normalised to string
	Version   string `json:"version"`
	Kind      string `json:"kind"`
	Status    string `json:"status"` // "busy" | "idle"
	Tmux      string `json:"tmux"`   // "<session>:@<win>.%<pane>" or ""
	Name      string `json:"name"`
	UpdatedAt int64  `json:"updatedAt"`
	File      string `json:"-"` // path it was read from
}

// registryFile is the on-disk shape; procStart may be a string or a number.
type registryFile struct {
	PID       int             `json:"pid"`
	SessionID string          `json:"sessionId"`
	Cwd       string          `json:"cwd"`
	ProcStart json.RawMessage `json:"procStart"`
	Version   string          `json:"version"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	Tmux      string          `json:"tmux"`
	Name      string          `json:"name"`
	UpdatedAt int64           `json:"updatedAt"`
}

// ReadRegistryFile reads one sessions/<pid>.json. *.key files are never opened.
func ReadRegistryFile(path string) (Registry, error) {
	if !strings.HasSuffix(path, ".json") {
		return Registry{}, fmt.Errorf("registry file %s: not a .json file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Registry{}, fmt.Errorf("read registry %s: %w", path, err)
	}
	var f registryFile
	if err := json.Unmarshal(data, &f); err != nil {
		return Registry{}, fmt.Errorf("parse registry %s: %w", path, err)
	}
	start, err := normaliseProcStart(f.ProcStart)
	if err != nil {
		return Registry{}, fmt.Errorf("parse registry %s: %w", path, err)
	}
	return Registry{PID: f.PID, SessionID: f.SessionID, Cwd: f.Cwd, ProcStart: start, Version: f.Version,
		Kind: f.Kind, Status: f.Status, Tmux: f.Tmux, Name: f.Name, UpdatedAt: f.UpdatedAt, File: path}, nil
}

// normaliseProcStart accepts a JSON string or number (older writers) and
// fails closed on any other type, so a reused pid can never match by accident.
func normaliseProcStart(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String(), nil
	}
	return "", fmt.Errorf("procStart has unsupported JSON type: %s", string(raw))
}

// ReadRegistry reads every *.json in sessionsDir, sorted by pid. A missing
// directory is an empty registry; a malformed file is an error naming it.
func ReadRegistry(sessionsDir string) ([]Registry, error) {
	entries, err := os.ReadDir(sessionsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read registry dir %s: %w", sessionsDir, err)
	}
	var out []Registry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue // *.key (messaging tokens) and anything else are never opened
		}
		r, err := ReadRegistryFile(filepath.Join(sessionsDir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

// TmuxParts splits "<session>:@<win>.%<pane>".
func (r Registry) TmuxParts() (sess, windowID, paneID string, ok bool) {
	i := strings.LastIndex(r.Tmux, ":@")
	if i < 0 {
		return "", "", "", false
	}
	sess = r.Tmux[:i]
	rest := r.Tmux[i+1:] // "@3.%7"
	j := strings.Index(rest, ".%")
	if j < 0 || sess == "" {
		return "", "", "", false
	}
	return sess, rest[:j], rest[j+1:], true
}
```

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/session/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/session
git commit -m "feat(session): registry reader with string-or-number procStart"
```

---

### Task 5: Transcript fixtures and `ReadMeta`

**Files:**
- Create: `internal/session/meta.go`, `internal/session/testdata/config/projects/-home-alice-github-example-widget/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13.jsonl`, `…/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/subagents/agent-0f8e7d6c.jsonl`, `…/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/subagents/agent-0f8e7d6c.meta.json`, `…/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/tool-results/toolu_01Ab3.txt`, `…/a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d.jsonl`
- Test: `internal/session/meta_test.go`

**Interfaces:**
- Produces: `Meta{Summary, Title, FirstUser, LaunchCwd, WorkCwd, Branch, Version, LastTS}`, `ReadMeta(transcript string) (Meta, error)`, `(Meta).Label() string`.

- [ ] **Step 1: Write the transcript fixtures**

Every record type from spec §3 appears at least once; field names match Claude Code 2.1.247. One record per line — the file has exactly these 11 lines.

`internal/session/testdata/config/projects/-home-alice-github-example-widget/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13.jsonl`:

```json
{"type":"permission-mode","permissionMode":"acceptEdits","sessionId":"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13","timestamp":"2026-08-27T10:15:29.900Z"}
{"parentUuid":null,"isSidechain":false,"userType":"external","cwd":"/home/alice/github/example/widget","sessionId":"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13","version":"2.1.247","gitBranch":"feature/teleport","type":"user","message":{"role":"user","content":"Add a --verbose flag to the widget CLI"},"uuid":"5b7d1c2e-0f3a-4c6b-8d9e-1a2b3c4d5e6f","timestamp":"2026-08-27T10:15:30.123Z"}
{"parentUuid":"5b7d1c2e-0f3a-4c6b-8d9e-1a2b3c4d5e6f","isSidechain":false,"userType":"external","cwd":"/home/alice/github/example/widget","sessionId":"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13","version":"2.1.247","gitBranch":"feature/teleport","message":{"id":"msg_01FixtureAssistantOne","type":"message","role":"assistant","model":"claude-opus-4-1","content":[{"type":"text","text":"I'll look at the CLI entry point first."},{"type":"tool_use","id":"toolu_01Ab1","name":"Skill","input":{"skill":"superpowers:test-driven-development"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":120,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":48}},"requestId":"req_01FixtureOne","type":"assistant","uuid":"6c8e2d3f-1a4b-4d7c-9e0f-2b3c4d5e6f70","timestamp":"2026-08-27T10:15:33.456Z"}
{"parentUuid":"6c8e2d3f-1a4b-4d7c-9e0f-2b3c4d5e6f70","isSidechain":false,"userType":"external","cwd":"/home/alice/github/example/widget","sessionId":"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13","version":"2.1.247","gitBranch":"feature/teleport","type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_01Ab1","type":"tool_result","content":"Launching skill: superpowers:test-driven-development"}]},"uuid":"7d9f3e40-2b5c-4e8d-a0f1-3c4d5e6f7081","timestamp":"2026-08-27T10:15:33.900Z","attributionSkill":"superpowers:test-driven-development","attributionPlugin":"superpowers@claude-plugins-official"}
{"parentUuid":"7d9f3e40-2b5c-4e8d-a0f1-3c4d5e6f7081","isSidechain":false,"userType":"external","cwd":"/home/alice/github/example/widget","sessionId":"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13","version":"2.1.247","gitBranch":"feature/teleport","type":"attachment","attachment":{"type":"skill_listing","skills":[{"name":"superpowers:test-driven-development","path":"/home/alice/.claude/plugins/cache/claude-plugins-official/superpowers/6.3.0/skills/test-driven-development/SKILL.md"}]},"uuid":"8ea04f51-3c6d-4f9e-b1a2-4d5e6f708192","timestamp":"2026-08-27T10:15:34.000Z"}
{"parentUuid":"8ea04f51-3c6d-4f9e-b1a2-4d5e6f708192","isSidechain":false,"userType":"external","cwd":"/home/alice/github/example/widget","sessionId":"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13","version":"2.1.247","gitBranch":"feature/teleport","message":{"id":"msg_01FixtureAssistantTwo","type":"message","role":"assistant","model":"claude-opus-4-1","content":[{"type":"tool_use","id":"toolu_01Ab2","name":"mcp__playwright__browser_navigate","input":{"url":"http://localhost:8080/"}},{"type":"tool_use","id":"toolu_01Ab3","name":"Agent","input":{"subagent_type":"Explore","prompt":"Find where flags are parsed"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":300,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":90}},"requestId":"req_01FixtureTwo","type":"assistant","uuid":"9fb15062-4d7e-4a0f-c2b3-5e6f708192a3","timestamp":"2026-08-27T10:15:40.000Z"}
{"type":"file-history-snapshot","messageId":"9fb15062-4d7e-4a0f-c2b3-5e6f708192a3","snapshot":{"messageId":"9fb15062-4d7e-4a0f-c2b3-5e6f708192a3","trackedFileBackups":{"/home/alice/github/example/widget/main.go":{"backupFileName":"0a1b2c3d4e5f60718293a4b5c6d7e8f9@v1","version":1,"backupTime":"2026-08-27T10:15:41.000Z"}},"timestamp":"2026-08-27T10:15:41.000Z"},"isSnapshotUpdate":false}
{"type":"summary","summary":"Add verbose flag to widget CLI","leafUuid":"9fb15062-4d7e-4a0f-c2b3-5e6f708192a3"}
{"type":"ai-title","aiTitle":"Widget verbose flag","sessionId":"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"}
{"type":"custom-title","customTitle":"widget-verbose","sessionId":"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"}
{"parentUuid":"9fb15062-4d7e-4a0f-c2b3-5e6f708192a3","isSidechain":false,"userType":"external","cwd":"/home/alice/github/example/widget/cmd","sessionId":"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13","version":"2.1.247","gitBranch":"feature/teleport","message":{"id":"msg_01FixtureAssistantThree","type":"message","role":"assistant","model":"claude-opus-4-1","content":[{"type":"text","text":"Done: main.go now parses --verbose."}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":500,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":20}},"requestId":"req_01FixtureThree","type":"assistant","uuid":"a0c26173-5e8f-4b10-d3c4-6f708192a3b4","timestamp":"2026-08-27T10:16:02.750Z"}
```

`…/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/subagents/agent-0f8e7d6c.jsonl`:

```json
{"parentUuid":null,"isSidechain":true,"userType":"external","cwd":"/home/alice/github/example/widget","sessionId":"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13","version":"2.1.247","gitBranch":"feature/teleport","agentId":"agent-0f8e7d6c","type":"user","message":{"role":"user","content":"Find where flags are parsed"},"uuid":"b1d37284-6f90-4c21-e4d5-708192a3b4c5","timestamp":"2026-08-27T10:15:42.000Z"}
{"parentUuid":"b1d37284-6f90-4c21-e4d5-708192a3b4c5","isSidechain":true,"userType":"external","cwd":"/home/alice/github/example/widget","sessionId":"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13","version":"2.1.247","gitBranch":"feature/teleport","agentId":"agent-0f8e7d6c","message":{"id":"msg_01FixtureAgentOne","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"toolu_01Ag1","name":"mcp__filesystem__read_file","input":{"path":"/home/alice/github/example/widget/main.go"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":30}},"requestId":"req_01FixtureAgent","type":"assistant","uuid":"c2e48395-70a1-4d32-f5e6-8192a3b4c5d6","timestamp":"2026-08-27T10:15:43.000Z"}
```

`…/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/subagents/agent-0f8e7d6c.meta.json`:

```json
{"agentId":"agent-0f8e7d6c","subagentType":"Explore","description":"Find flag parsing","startedAt":"2026-08-27T10:15:42.000Z","cwd":"/home/alice/github/example/widget"}
```

`…/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/tool-results/toolu_01Ab3.txt` — the single line `main.go:12: flag.Bool("verbose", ...)`.

`…/a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d.jsonl` (session B, no title, used for prefix/ambiguity tests):

```json
{"parentUuid":null,"isSidechain":false,"userType":"external","cwd":"/home/alice/github/example/widget","sessionId":"a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d","version":"2.1.247","gitBranch":"main","type":"user","message":{"role":"user","content":"Explain the release process"},"uuid":"d3f594a6-81b2-4e43-a6f7-92a3b4c5d6e7","timestamp":"2026-08-27T11:00:00.000Z"}
{"parentUuid":"d3f594a6-81b2-4e43-a6f7-92a3b4c5d6e7","isSidechain":false,"userType":"external","cwd":"/home/alice/github/example/widget","sessionId":"a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d","version":"2.1.247","gitBranch":"main","message":{"id":"msg_01FixtureB","type":"message","role":"assistant","model":"claude-opus-4-1","content":[{"type":"text","text":"Every push to main is a release."}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":8}},"requestId":"req_01FixtureB","type":"assistant","uuid":"e406a5b7-92c3-4f54-b7a8-a3b4c5d6e7f8","timestamp":"2026-08-27T11:00:05.000Z"}
```

- [ ] **Step 2: Write the failing test**

`internal/session/meta_test.go`:

```go
package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const (
	fixtureProject = "testdata/config/projects/-home-alice-github-example-widget"
	sidA           = ID("3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13")
	sidB           = ID("a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d")
)

func TestReadMeta(t *testing.T) {
	m, err := ReadMeta(filepath.Join(fixtureProject, string(sidA)+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	want := Meta{
		Summary:   "Add verbose flag to widget CLI",
		Title:     "widget-verbose", // custom-title comes after ai-title and wins
		FirstUser: "Add a --verbose flag to the widget CLI",
		LaunchCwd: "/home/alice/github/example/widget",
		WorkCwd:   "/home/alice/github/example/widget/cmd",
		Branch:    "feature/teleport",
		Version:   "2.1.247",
		LastTS:    "2026-08-27T10:16:02.750Z",
	}
	if diff := cmp.Diff(want, m); diff != "" {
		t.Fatal(diff)
	}
	if m.Label() != "widget-verbose" {
		t.Fatalf("Label = %q", m.Label())
	}
}

func TestLabelFallbacks(t *testing.T) {
	if (Meta{}).Label() != "(no summary found)" {
		t.Fatal("empty label")
	}
	if got := (Meta{FirstUser: "  a   b\nc "}).Label(); got != "a b c" {
		t.Fatalf("collapse = %q", got)
	}
	long := Meta{Summary: strings.Repeat("x", 250)}
	if got := long.Label(); len([]rune(got)) != 201 || !strings.HasSuffix(got, "…") {
		t.Fatalf("clip = %q", got)
	}
}

func TestReadMetaSkipsGarbageLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.jsonl")
	os.WriteFile(p, []byte("not json\n{\"type\":\"user\",\"cwd\":\"/tmp/a\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"hi\"}]}}\n"), 0o600)
	m, err := ReadMeta(p)
	if err != nil || m.LaunchCwd != "/tmp/a" || m.FirstUser != "hi" {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := ReadMeta(filepath.Join(t.TempDir(), "missing.jsonl")); err == nil {
		t.Fatal("missing transcript must be an error")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/session/ -run 'TestReadMeta|TestLabel' -v`
Expected: FAIL — `undefined: ReadMeta`.

- [ ] **Step 4: Implement `internal/session/meta.go`** (port of go-tmux-saver `resume.ReadMeta`, plus `Version` and an error return)

```go
package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Meta is the human context pulled from the transcript.
type Meta struct {
	Summary, Title, FirstUser  string
	LaunchCwd, WorkCwd, Branch string
	Version                    string
	LastTS                     string
}

// scannerBuf is the line buffer for transcripts (single records can be MBs).
const scannerBuf = 64 * 1024 * 1024

// ReadMeta scans a transcript's JSONL for a label, cwd and branch. Launch
// cwd = first cwd seen (its munge names the project folder — the directory
// to resume from); work cwd = last cwd seen. Unparseable lines are skipped.
func ReadMeta(transcript string) (Meta, error) {
	var m Meta
	f, err := os.Open(transcript)
	if err != nil {
		return m, fmt.Errorf("read transcript: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), scannerBuf)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var o struct {
			Type        string `json:"type"`
			Cwd         string `json:"cwd"`
			GitBranch   string `json:"gitBranch"`
			Version     string `json:"version"`
			Timestamp   string `json:"timestamp"`
			Summary     string `json:"summary"`
			AiTitle     string `json:"aiTitle"`
			CustomTitle string `json:"customTitle"`
			Message     struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &o) != nil {
			continue
		}
		if o.Cwd != "" {
			if m.LaunchCwd == "" {
				m.LaunchCwd = o.Cwd
			}
			m.WorkCwd = o.Cwd
		}
		if o.GitBranch != "" {
			m.Branch = o.GitBranch
		}
		if o.Version != "" {
			m.Version = o.Version
		}
		if o.Timestamp != "" && (o.Type == "user" || o.Type == "assistant") {
			m.LastTS = o.Timestamp
		}
		switch {
		case o.Type == "summary" && o.Summary != "":
			m.Summary = o.Summary
		case o.Type == "ai-title" && o.AiTitle != "":
			m.Title = o.AiTitle
		case o.Type == "custom-title" && o.CustomTitle != "":
			m.Title = o.CustomTitle
		case o.Type == "user" && m.FirstUser == "":
			m.FirstUser = firstUserText(o.Message.Content)
		}
	}
	if err := sc.Err(); err != nil {
		return m, fmt.Errorf("read transcript %s: %w", transcript, err)
	}
	return m, nil
}

// firstUserText extracts the first plain-text chunk of a user message:
// either a bare string (ignored when it looks like markup) or the first
// {"type":"text"} part of a content list.
func firstUserText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if strings.HasPrefix(strings.TrimSpace(s), "<") {
			return ""
		}
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		for _, p := range parts {
			if p.Type == "text" {
				return p.Text
			}
		}
	}
	return ""
}

// Label is the best one-line description: title > rolling summary > first
// user prompt, whitespace-collapsed and clipped to 200 runes.
func (m Meta) Label() string {
	text := m.Title
	if text == "" {
		text = m.Summary
	}
	if text == "" {
		text = m.FirstUser
	}
	if text == "" {
		text = "(no summary found)"
	}
	text = strings.Join(strings.Fields(text), " ")
	r := []rune(text)
	if len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return text
}
```

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/session/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/session
git commit -m "feat(session): transcript fixtures and ReadMeta"
```

---

### Task 6: Selector parsing

**Files:**
- Create: `internal/session/selector.go`
- Test: `internal/session/selector_test.go`

**Interfaces:**
- Produces: `Selector{Current, ID, Prefix, TmuxSess, TmuxWindow, TmuxPane}`, `Env{SessionID, PID, TmuxPane, Tmux}`, `ParseSelector(args []string, env Env) (Selector, error)`.

- [ ] **Step 1: Write the failing test**

`internal/session/selector_test.go`:

```go
package session

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseSelector(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  Env
		want Selector
		err  string
	}{
		{"inside session", nil, Env{SessionID: "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13", TmuxPane: "%7"},
			Selector{Current: true, ID: sidA, TmuxPane: "%7"}, ""},
		{"current pane", nil, Env{TmuxPane: "%7"}, Selector{Current: true, TmuxPane: "%7"}, ""},
		{"nothing to go on", nil, Env{}, Selector{Current: true}, ""},
		{"bad env session id", nil, Env{SessionID: "garbage"}, Selector{}, "CLAUDE_CODE_SESSION_ID"},
		{"full uuid", []string{"3F9C2B7E-5A14-4D8E-9B21-7C0E5D6A8F13"}, Env{}, Selector{ID: sidA}, ""},
		{"prefix", []string{"3f9c"}, Env{}, Selector{Prefix: "3f9c"}, ""},
		{"name", []string{"widget"}, Env{}, Selector{Prefix: "widget"}, ""},
		{"too short hex", []string{"3f9"}, Env{}, Selector{}, "at least 4"},
		{"tmux window", []string{"main", "3"}, Env{}, Selector{TmuxSess: "main", TmuxWindow: "3"}, ""},
		{"too many", []string{"a", "b", "c"}, Env{}, Selector{}, "too many"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseSelector(c.args, c.env)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want containing %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(c.want, got); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/session/ -run TestParseSelector -v`
Expected: FAIL — `undefined: ParseSelector`.

- [ ] **Step 3: Implement `internal/session/selector.go`**

```go
package session

import (
	"fmt"
	"regexp"
	"strings"
)

// Selector is the parsed positional/environment session selection (spec §5).
type Selector struct {
	Current    bool   // no args: use $CLAUDE_CODE_SESSION_ID / $TMUX_PANE
	ID         ID     // full uuid
	Prefix     string // >=4 hex chars, or a registry name
	TmuxSess   string // two-word form
	TmuxWindow string
	TmuxPane   string // $TMUX_PANE when Current (resolution hint)
}

// Env is the environment inside (or near) a session.
type Env struct {
	SessionID string // $CLAUDE_CODE_SESSION_ID
	PID       string // $CLAUDE_PID
	TmuxPane  string // $TMUX_PANE
	Tmux      string // $TMUX
}

var hexRe = regexp.MustCompile(`\A[0-9a-f]+\z`)

// ParseSelector classifies the positional arguments (0, 1 or 2 words).
func ParseSelector(args []string, env Env) (Selector, error) {
	switch len(args) {
	case 0:
		sel := Selector{Current: true, TmuxPane: env.TmuxPane}
		if env.SessionID != "" {
			id, err := ParseID(env.SessionID)
			if err != nil {
				return Selector{}, fmt.Errorf("CLAUDE_CODE_SESSION_ID: %w", err)
			}
			sel.ID = id
		}
		return sel, nil
	case 1:
		arg := strings.TrimSpace(args[0])
		if IsUUID(strings.ToLower(arg)) {
			id, _ := ParseID(arg)
			return Selector{ID: id}, nil
		}
		if hexRe.MatchString(strings.ToLower(arg)) && len(arg) < 4 {
			return Selector{}, fmt.Errorf("session id prefix %q: at least 4 hex characters are required", arg)
		}
		if arg == "" {
			return Selector{}, fmt.Errorf("empty session selector")
		}
		return Selector{Prefix: arg}, nil
	case 2:
		return Selector{TmuxSess: args[0], TmuxWindow: args[1]}, nil
	default:
		return Selector{}, fmt.Errorf("too many arguments: expected [<session>] or <tmux-session> <window>, got %d", len(args))
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race ./internal/session/ -run TestParseSelector -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/session
git commit -m "feat(session): selector parsing"
```

---

### Task 7: `Resolve` and `Load` with a `PaneProbe`

**Files:**
- Create: `internal/session/proc.go`, `internal/session/argv.go`, `internal/session/resolve.go`, `internal/session/testdata/proc/41234/stat`, `internal/session/testdata/proc/41300/stat`
- Test: `internal/session/resolve_test.go`, `internal/session/argv_test.go`

**Interfaces:**
- Consumes: `ReadRegistry`, `ReadMeta`, `Paths`, `Selector`.
- Produces: `ProcRoot` (package var, default `/proc`), `ProcStartTime(procRoot string, pid int) (string, error)`, `ProcAlive(procRoot string, pid int, procStart string) bool`, `ArgvSessionID(argv []string) (sid string, placeholder bool, ok bool)`, `PaneProbe` interface (with the added `ListPanes`), `PaneInfo`, `TmuxRef`, `Session`, `ErrNotFound`, `FindTranscript(projectsDir string, id ID) (string, error)`, `Resolve`, `Load`.

- [ ] **Step 1: Write the proc fixtures**

`internal/session/testdata/proc/41234/stat` (one line; field 22 = `123456`, matching the registry fixture):

```
41234 (claude) S 41000 41234 41000 34816 41234 4194304 0 0 0 0 0 0 0 0 20 0 1 0 123456 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0
```

`internal/session/testdata/proc/41300/stat` (field 22 = `999999` — a *reused pid*; the registry says `234567`, so the entry is stale):

```
41300 (python3) S 1 41300 41300 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 999999 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0
```

- [ ] **Step 2: Write the failing tests**

`internal/session/argv_test.go`:

```go
package session

import "testing"

func TestArgvSessionID(t *testing.T) {
	const u = "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"
	cases := []struct {
		argv        []string
		sid         string
		placeholder bool
		ok          bool
	}{
		{[]string{"claude"}, "", false, true},
		{[]string{"/home/alice/.local/bin/claude", "--resume", u}, u, false, true},
		{[]string{"claude", "-r", u}, u, false, true},
		{[]string{"node", "/home/alice/.claude/local/node_modules/@anthropic-ai/claude-code/cli.js", "--resume", u}, u, false, true},
		{[]string{"claude-resume", u}, u, true, true},
		{[]string{"python3", "/home/alice/bin/claude-resume", u, "--saved-output", "/tmp/x"}, u, true, true},
		{[]string{"/usr/bin/go-tmux-saver", "claude-resume", u}, u, true, true},
		{[]string{"claude-teleport", "placeholder", "--resume", u, "--now"}, u, true, true},
		{[]string{"/usr/bin/claude-teleport", "placeholder", "--saved-output", "/tmp/c", "--resume", u}, u, true, true},
		{[]string{"bash"}, "", false, false},
		{[]string{"foo-claude-resume", u}, "", false, false},
		{[]string{"claude-teleport", "list"}, "", false, false},
	}
	for _, c := range cases {
		sid, ph, ok := ArgvSessionID(c.argv)
		if sid != c.sid || ph != c.placeholder || ok != c.ok {
			t.Errorf("ArgvSessionID(%q) = %q %v %v, want %q %v %v", c.argv, sid, ph, ok, c.sid, c.placeholder, c.ok)
		}
	}
}
```

`internal/session/resolve_test.go`:

```go
package session

import (
	"errors"
	"strings"
	"testing"
)

// fakeProbe is the Plan 01 stand-in for tmuxx.Prober (Plan 03).
type fakeProbe struct {
	panes   map[string]struct{ argv []string; pid int } // pane id -> foreground command
	windows map[string][]PaneInfo                       // "sess window" -> panes
	socket  string
}

func (f *fakeProbe) PaneCommand(paneID string) ([]string, int, bool) {
	p, ok := f.panes[paneID]
	return p.argv, p.pid, ok
}
func (f *fakeProbe) FindWindow(sess, win string) ([]string, error) {
	infos, ok := f.windows[sess+" "+win]
	if !ok {
		return nil, errors.New("window not found: " + sess + " " + win)
	}
	var ids []string
	for _, i := range infos {
		ids = append(ids, i.PaneID)
	}
	return ids, nil
}
func (f *fakeProbe) ListPanes() ([]PaneInfo, error) {
	var out []PaneInfo
	for _, infos := range f.windows {
		out = append(out, infos...)
	}
	return out, nil
}
func (f *fakeProbe) SocketPath() string { return f.socket }

func fixturePaths() Paths { return NewPaths("/home/alice", "testdata/config", "/tmp/xdg") }

func useFixtureProc(t *testing.T) {
	t.Helper()
	old := ProcRoot
	ProcRoot = "testdata/proc"
	t.Cleanup(func() { ProcRoot = old })
}

func TestProcAlive(t *testing.T) {
	if !ProcAlive("testdata/proc", 41234, "123456") {
		t.Fatal("41234 should be alive")
	}
	if ProcAlive("testdata/proc", 41300, "234567") {
		t.Fatal("41300 has a different start time: stale")
	}
	if ProcAlive("testdata/proc", 1, "1") {
		t.Fatal("pid 1 is not in the fixture")
	}
	if ProcAlive("testdata/proc", 41234, "") {
		t.Fatal("empty procStart must never match")
	}
}

func TestLoadRunning(t *testing.T) {
	useFixtureProc(t)
	probe := &fakeProbe{socket: "/tmp/tmux-1000/default"}
	s, err := Load(fixturePaths(), sidA, probe)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != StateRunning || s.Registry == nil || s.Registry.PID != 41234 || s.Name != "widget" {
		t.Fatalf("%+v", s)
	}
	if s.Tmux == nil || s.Tmux.Session != "main" || s.Tmux.WindowID != "@3" || s.Tmux.PaneID != "%7" || s.Tmux.SocketPath != probe.socket {
		t.Fatalf("tmux = %+v", s.Tmux)
	}
	if s.LaunchCwd != "/home/alice/github/example/widget" || s.WorkCwd != "/home/alice/github/example/widget/cmd" ||
		s.Branch != "feature/teleport" || s.Version != "2.1.247" || !strings.HasSuffix(s.Transcript, "/"+string(sidA)+".jsonl") {
		t.Fatalf("%+v", s)
	}
}

func TestLoadStaleRegistryIsIdle(t *testing.T) {
	useFixtureProc(t)
	s, err := Load(fixturePaths(), sidB, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != StateIdle || s.Registry != nil {
		t.Fatalf("stale registry (pid reused) must not count as running: %+v", s)
	}
}

func TestLoadSuspendedViaPlaceholderPane(t *testing.T) {
	useFixtureProc(t)
	probe := &fakeProbe{socket: "/tmp/tmux-1000/default",
		panes:   map[string]struct{ argv []string; pid int }{"%9": {[]string{"claude-teleport", "placeholder", "--resume", string(sidB)}, 500}},
		windows: map[string][]PaneInfo{"main 4": {{Session: "main", WindowID: "@4", PaneID: "%9"}}}}
	s, err := Load(fixturePaths(), sidB, probe)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != StateSuspended || s.Tmux == nil || s.Tmux.PaneID != "%9" || s.Tmux.WindowID != "@4" {
		t.Fatalf("%+v tmux=%+v", s, s.Tmux)
	}
}

func TestLoadNotFound(t *testing.T) {
	_, err := Load(fixturePaths(), ID("00000000-0000-4000-8000-000000000000"), nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolve(t *testing.T) {
	useFixtureProc(t)
	p := fixturePaths()
	probe := &fakeProbe{socket: "/tmp/tmux-1000/default",
		panes: map[string]struct{ argv []string; pid int }{
			"%7": {[]string{"claude"}, 41234},
			"%9": {[]string{"claude-resume", string(sidB)}, 500},
		},
		windows: map[string][]PaneInfo{
			"main 3": {{Session: "main", WindowID: "@3", PaneID: "%7"}},
			"main 4": {{Session: "main", WindowID: "@4", PaneID: "%9"}},
		}}

	if s, err := Resolve(p, Selector{ID: sidA}, probe); err != nil || s.ID != sidA {
		t.Fatalf("by id: %v %v", s, err)
	}
	if s, err := Resolve(p, Selector{Prefix: "3f9c"}, probe); err != nil || s.ID != sidA {
		t.Fatalf("by prefix: %v %v", s, err)
	}
	if s, err := Resolve(p, Selector{Prefix: "widget"}, probe); err != nil || s.ID != sidA {
		t.Fatalf("by name: %v %v", s, err)
	}
	if _, err := Resolve(p, Selector{Prefix: "zzzz"}, probe); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown prefix: %v", err)
	}
	if s, err := Resolve(p, Selector{Current: true, TmuxPane: "%7"}, probe); err != nil || s.ID != sidA || s.State != StateRunning {
		t.Fatalf("current pane: %v %v", s, err)
	}
	if s, err := Resolve(p, Selector{Current: true, TmuxPane: "%9"}, probe); err != nil || s.ID != sidB || s.State != StateSuspended {
		t.Fatalf("current placeholder pane: %v %v", s, err)
	}
	if s, err := Resolve(p, Selector{TmuxSess: "main", TmuxWindow: "4"}, probe); err != nil || s.ID != sidB {
		t.Fatalf("window: %v %v", s, err)
	}
	if _, err := Resolve(p, Selector{Current: true}, probe); err == nil || !strings.Contains(err.Error(), "3f9c2b7e") {
		t.Fatalf("no selector must list candidates: %v", err)
	}
}

func TestResolveAmbiguousPrefix(t *testing.T) {
	useFixtureProc(t)
	// both fixture transcripts live in the same project dir; a prefix that
	// matches neither uuid but matches two names is simulated with a
	// common hex prefix: none exists, so craft the ambiguity via ID prefixes
	// of the two fixtures' first char? They differ ("3f" vs "a1"), so use the
	// registry name path: not ambiguous either. Ambiguity is exercised by a
	// temp project dir with two sessions sharing a prefix.
	dir := t.TempDir()
	p := NewPaths("/home/alice", dir, dir)
	proj := p.ProjectDir("/home/alice/x")
	mustMkdir(t, proj)
	for _, id := range []string{"deadbeef-0000-4000-8000-000000000001", "deadbeef-0000-4000-8000-000000000002"} {
		mustWrite(t, proj+"/"+id+".jsonl", `{"type":"user","cwd":"/home/alice/x","sessionId":"`+id+`","message":{"content":"hi"}}`+"\n")
	}
	_, err := Resolve(p, Selector{Prefix: "deadbeef"}, nil)
	if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "000000000001") || !strings.Contains(err.Error(), "000000000002") {
		t.Fatalf("ambiguity must list both candidates: %v", err)
	}
}
```

Add to `internal/session/testutil_test.go` (shared by later tests):

```go
package session

import (
	"os"
	"path/filepath"
	"testing"
)

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/session/ -run 'TestArgv|TestProcAlive|TestLoad|TestResolve' -v`
Expected: FAIL — `undefined: ArgvSessionID`, `undefined: Load`, …

- [ ] **Step 4: Implement**

`internal/session/proc.go`:

```go
package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ProcRoot is where /proc is mounted. Tests point it at a fixture tree.
var ProcRoot = "/proc"

// ProcStartTime returns field 22 (starttime) of /proc/<pid>/stat as a
// string, the value Claude Code stores as procStart.
func ProcStartTime(procRoot string, pid int) (string, error) {
	path := filepath.Join(procRoot, strconv.Itoa(pid), "stat")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	s := string(data)
	rp := strings.LastIndexByte(s, ')') // comm may contain spaces and parens
	if rp < 0 {
		return "", fmt.Errorf("parse %s: no ')'", path)
	}
	rest := strings.Fields(s[rp+1:]) // rest[0]=state rest[1]=ppid … rest[19]=starttime
	if len(rest) < 20 {
		return "", fmt.Errorf("parse %s: %d fields after comm", path, len(rest))
	}
	return rest[19], nil
}

// ProcAlive reports whether pid exists AND its start time equals procStart.
// An empty procStart never matches (a reused pid must never be trusted).
func ProcAlive(procRoot string, pid int, procStart string) bool {
	if procStart == "" || pid <= 0 {
		return false
	}
	start, err := ProcStartTime(procRoot, pid)
	return err == nil && start == procStart
}
```

`internal/session/argv.go`:

```go
package session

import (
	"path"
	"regexp"
	"strings"
)

var (
	// `claude-resume <uuid>` at a word/path boundary — covers the rcfiles
	// script (`python3 …/claude-resume <uuid>`), go-tmux-saver's built-in
	// (`go-tmux-saver claude-resume <uuid>`) and a bare `claude-resume`.
	claudeResumeRe = regexp.MustCompile(`(?:^|[\s/])claude-resume\s+([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)
	// `claude-teleport placeholder … --resume <uuid>`.
	teleportPlaceholderRe = regexp.MustCompile(`(?:^|[\s/])claude-teleport\s+placeholder\s(?:.*\s)?--resume\s+([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)
)

// ArgvSessionID classifies a pane's foreground command line.
//
//	ok=false                        not a claude or placeholder process
//	ok=true, placeholder=true       a placeholder holding sid
//	ok=true, placeholder=false      a real claude; sid is its --resume id or ""
//
// A real claude is argv[0] named `claude` (any directory) or `node …/cli.js`
// (Claude Code's npm/native entry point).
func ArgvSessionID(argv []string) (sid string, placeholder bool, ok bool) {
	if len(argv) == 0 {
		return "", false, false
	}
	joined := strings.Join(argv, " ")
	if m := teleportPlaceholderRe.FindStringSubmatch(joined); m != nil {
		return m[1], true, true
	}
	if m := claudeResumeRe.FindStringSubmatch(joined); m != nil {
		return m[1], true, true
	}
	base := path.Base(argv[0])
	isClaude := base == "claude"
	if (base == "node" || base == "nodejs" || base == "bun") && len(argv) > 1 {
		script := argv[1]
		isClaude = strings.HasSuffix(script, "/cli.js") || strings.Contains(script, "@anthropic-ai/claude-code")
	}
	if !isClaude {
		return "", false, false
	}
	for i := 1; i+1 < len(argv); i++ {
		if (argv[i] == "--resume" || argv[i] == "-r") && IsUUID(strings.ToLower(argv[i+1])) {
			return strings.ToLower(argv[i+1]), false, true
		}
	}
	return "", false, true
}
```

`internal/session/resolve.go`:

```go
package session

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNotFound is returned (wrapped) when no session matches.
var ErrNotFound = errors.New("session not found")

// TmuxRef is where a session's pane lives (from the registry or a pane scan).
type TmuxRef struct {
	SocketPath string
	Session    string // session name
	WindowID   string // "@N"
	PaneID     string // "%N"
}

// PaneInfo identifies one pane for ListPanes.
type PaneInfo struct {
	Session  string
	WindowID string
	PaneID   string
}

// PaneProbe lets Resolve consult tmux without importing tmuxx (Plan 03 wires
// tmuxx.Prober in; Plan 01 tests use a fake).
type PaneProbe interface {
	// PaneCommand returns the foreground command line (argv) and pid of the
	// pane; ok=false if the pane cannot be found.
	PaneCommand(paneID string) (argv []string, pid int, ok bool)
	// FindWindow resolves "<session> <window index|name>" to its pane ids.
	FindWindow(session, window string) (paneIDs []string, err error)
	// ListPanes enumerates every pane on the server (for suspended-pane discovery).
	ListPanes() ([]PaneInfo, error)
	SocketPath() string
}

// Session is a located session.
type Session struct {
	ID         ID
	Paths      Paths
	ProjectDir string // <ProjectsDir>/<munged launch cwd>
	Transcript string // <ProjectDir>/<id>.jsonl
	LaunchCwd  string // first cwd in the transcript
	WorkCwd    string // last cwd in the transcript
	Branch     string // last gitBranch
	Name       string // registry name if running, else ""
	Version    string // claude version from the transcript (last "version")
	State      State
	Registry   *Registry // non-nil iff StateRunning
	Tmux       *TmuxRef  // non-nil when a pane is known (running or suspended)
}

// FindTranscript locates <projectsDir>/*/<id>.jsonl. Exactly one must exist.
func FindTranscript(projectsDir string, id ID) (string, error) {
	hits, err := filepath.Glob(filepath.Join(projectsDir, "*", string(id)+".jsonl"))
	if err != nil {
		return "", fmt.Errorf("glob transcripts under %s: %w", projectsDir, err)
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("%w: no transcript %s.jsonl under %s", ErrNotFound, id, projectsDir)
	case 1:
		return hits[0], nil
	default:
		return "", fmt.Errorf("session %s has %d transcripts under %s: %s", id, len(hits), projectsDir, strings.Join(hits, ", "))
	}
}

// Load reads an already-known session (by id) from disk; State is Idle
// unless the registry (with a live pid) or a placeholder pane says otherwise.
func Load(p Paths, id ID, probe PaneProbe) (*Session, error) {
	transcript, err := FindTranscript(p.ProjectsDir(), id)
	if err != nil {
		return nil, err
	}
	meta, err := ReadMeta(transcript)
	if err != nil {
		return nil, err
	}
	s := &Session{ID: id, Paths: p, ProjectDir: filepath.Dir(transcript), Transcript: transcript,
		LaunchCwd: meta.LaunchCwd, WorkCwd: meta.WorkCwd, Branch: meta.Branch, Version: meta.Version, State: StateIdle}
	regs, err := ReadRegistry(p.SessionsDir())
	if err != nil {
		return nil, err
	}
	for i := range regs {
		r := regs[i]
		if r.SessionID != string(id) || !ProcAlive(ProcRoot, r.PID, r.ProcStart) {
			continue
		}
		s.State, s.Registry, s.Name = StateRunning, &r, r.Name
		if sess, win, pane, ok := r.TmuxParts(); ok {
			s.Tmux = &TmuxRef{Session: sess, WindowID: win, PaneID: pane}
			if probe != nil {
				s.Tmux.SocketPath = probe.SocketPath()
			}
		}
		return s, nil
	}
	if probe != nil {
		panes, err := probe.ListPanes()
		if err != nil {
			return nil, fmt.Errorf("list tmux panes: %w", err)
		}
		for _, pi := range panes {
			argv, _, ok := probe.PaneCommand(pi.PaneID)
			if !ok {
				continue
			}
			if sid, ph, ok := ArgvSessionID(argv); ok && ph && sid == string(id) {
				s.State = StateSuspended
				s.Tmux = &TmuxRef{SocketPath: probe.SocketPath(), Session: pi.Session, WindowID: pi.WindowID, PaneID: pi.PaneID}
				break
			}
		}
	}
	return s, nil
}

// Resolve turns a selector into a Session (spec §5 rules 1–4). Ambiguity is
// an error listing the candidates; not found wraps ErrNotFound.
func Resolve(p Paths, sel Selector, probe PaneProbe) (*Session, error) {
	switch {
	case sel.ID != "":
		return Load(p, sel.ID, probe)
	case sel.TmuxSess != "":
		return resolveWindow(p, sel, probe)
	case sel.Prefix != "":
		return resolvePrefix(p, sel.Prefix, probe)
	case sel.Current:
		return resolveCurrent(p, sel, probe)
	}
	return nil, fmt.Errorf("empty selector")
}

// liveRegistry returns registry entries whose pid is alive with a matching procStart.
func liveRegistry(p Paths) ([]Registry, error) {
	regs, err := ReadRegistry(p.SessionsDir())
	if err != nil {
		return nil, err
	}
	var live []Registry
	for _, r := range regs {
		if ProcAlive(ProcRoot, r.PID, r.ProcStart) {
			live = append(live, r)
		}
	}
	return live, nil
}

func sessionFromPane(p Paths, paneID string, live []Registry, probe PaneProbe) (*Session, error) {
	for _, r := range live {
		if _, _, pane, ok := r.TmuxParts(); ok && pane == paneID {
			return Load(p, ID(r.SessionID), probe)
		}
	}
	if probe == nil {
		return nil, fmt.Errorf("%w: no running claude in pane %s (tmux not available to inspect it)", ErrNotFound, paneID)
	}
	argv, pid, ok := probe.PaneCommand(paneID)
	if !ok {
		return nil, fmt.Errorf("%w: pane %s not found", ErrNotFound, paneID)
	}
	if sid, _, ok := ArgvSessionID(argv); ok && sid != "" {
		return Load(p, ID(sid), probe)
	}
	for _, r := range live { // a claude whose registry lacks a tmux field
		if r.PID == pid {
			return Load(p, ID(r.SessionID), probe)
		}
	}
	return nil, fmt.Errorf("%w: pane %s runs %q, not a claude or placeholder", ErrNotFound, paneID, strings.Join(argv, " "))
}

func resolveCurrent(p Paths, sel Selector, probe PaneProbe) (*Session, error) {
	live, err := liveRegistry(p)
	if err != nil {
		return nil, err
	}
	if sel.TmuxPane != "" {
		return sessionFromPane(p, sel.TmuxPane, live, probe)
	}
	var cands []string
	for _, r := range live {
		cands = append(cands, fmt.Sprintf("  %s  %-12s %s", r.SessionID, r.Name, r.Cwd))
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("%w: no session given and none running (set CLAUDE_CODE_SESSION_ID, run inside tmux, or pass a session id)", ErrNotFound)
	}
	return nil, fmt.Errorf("no session given; running sessions:\n%s", strings.Join(cands, "\n"))
}

func resolvePrefix(p Paths, prefix string, probe PaneProbe) (*Session, error) {
	live, err := liveRegistry(p)
	if err != nil {
		return nil, err
	}
	found := map[string]bool{}
	lower := strings.ToLower(prefix)
	for _, r := range live {
		if r.Name == prefix || strings.HasPrefix(r.SessionID, lower) {
			found[r.SessionID] = true
		}
	}
	if hexRe.MatchString(lower) || strings.ContainsRune(lower, '-') {
		hits, err := filepath.Glob(filepath.Join(p.ProjectsDir(), "*", lower+"*.jsonl"))
		if err != nil {
			return nil, fmt.Errorf("glob transcripts: %w", err)
		}
		for _, h := range hits {
			base := strings.TrimSuffix(filepath.Base(h), ".jsonl")
			if IsUUID(base) {
				found[base] = true
			}
		}
	}
	ids := make([]string, 0, len(found))
	for id := range found {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	switch len(ids) {
	case 0:
		return nil, fmt.Errorf("%w: nothing matches %q", ErrNotFound, prefix)
	case 1:
		return Load(p, ID(ids[0]), probe)
	default:
		return nil, fmt.Errorf("%q is ambiguous; candidates:\n  %s", prefix, strings.Join(ids, "\n  "))
	}
}

func resolveWindow(p Paths, sel Selector, probe PaneProbe) (*Session, error) {
	if probe == nil {
		return nil, fmt.Errorf("resolve %s %s: tmux is not available", sel.TmuxSess, sel.TmuxWindow)
	}
	panes, err := probe.FindWindow(sel.TmuxSess, sel.TmuxWindow)
	if err != nil {
		return nil, fmt.Errorf("resolve %s %s: %w", sel.TmuxSess, sel.TmuxWindow, err)
	}
	live, err := liveRegistry(p)
	if err != nil {
		return nil, err
	}
	var last error
	for _, pane := range panes {
		s, err := sessionFromPane(p, pane, live, probe)
		if err == nil {
			return s, nil
		}
		last = err
	}
	return nil, fmt.Errorf("%w: window %s %s has no claude or placeholder pane (%v)", ErrNotFound, sel.TmuxSess, sel.TmuxWindow, last)
}
```

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/session/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/session
git commit -m "feat(session): Resolve/Load with PaneProbe, argv recognition, /proc liveness"
```

---

### Task 8: File inventory and the forbidden list

**Files:**
- Create: `internal/session/inventory.go`, plus fixtures: `internal/session/testdata/config/file-history/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/0a1b2c3d4e5f60718293a4b5c6d7e8f9@v1`, `internal/session/testdata/config/tasks/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/1.json`, `internal/session/testdata/config/tasks/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/.lock`, `internal/session/testdata/config/session-env/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/env.json`, `internal/session/testdata/config/todos/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13-agent-0f8e7d6c.json`, `internal/session/testdata/config/projects/-home-alice-github-example-widget/memory/MEMORY.md`
- Test: `internal/session/inventory_test.go`

**Interfaces:**
- Consumes: `Session`.
- Produces: `Category` + constants, `FileEntry` (+`Path()`), `Inventory{Files, Skipped, Memory}`, `Skipped`, `InventoryFiles(s *Session) (*Inventory, error)`, `Forbidden(rel string) bool`.

- [ ] **Step 1: Write the fixtures**

`…/file-history/3f9c…8f13/0a1b2c3d4e5f60718293a4b5c6d7e8f9@v1` — the two lines `package main` and `// original main.go before the edit`.

`…/tasks/3f9c…8f13/1.json`:

```json
{"id":"1","subject":"Add --verbose flag","description":"Parse the flag in /home/alice/github/example/widget/main.go","status":"in_progress","blocks":[],"blockedBy":[]}
```

`…/tasks/3f9c…8f13/.lock` — empty file.

`…/session-env/3f9c…8f13/env.json`:

```json
{"cwd":"/home/alice/github/example/widget","env":{"GOFLAGS":"-mod=mod"}}
```

`…/todos/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13-agent-0f8e7d6c.json`:

```json
[{"content":"Find flag parsing","status":"completed","activeForm":"Finding flag parsing"}]
```

`…/projects/-home-alice-github-example-widget/memory/MEMORY.md` — the single line `- Widget CLI uses the stdlib flag package.`

- [ ] **Step 2: Write the failing test**

`internal/session/inventory_test.go`:

```go
package session

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestForbidden(t *testing.T) {
	yes := []string{".credentials.json", ".claude.json", "settings.json", "settings.local.json", "sessions", "sessions/41234.json",
		"sessions/41234.0a1b2c3d.key", "plugins", "plugins/installed_plugins.json", "plugins/cache/x/y/1/.mcp.json", "foo/bar.key", "/sessions/1.json"}
	no := []string{"projects/-home-alice-x/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13.jsonl", "history.jsonl", "tasks/x/1.json", "file-history/x/y@v1", "keybindings.json"}
	for _, r := range yes {
		if !Forbidden(r) {
			t.Errorf("Forbidden(%q) = false", r)
		}
	}
	for _, r := range no {
		if Forbidden(r) {
			t.Errorf("Forbidden(%q) = true", r)
		}
	}
}

func TestInventoryFiles(t *testing.T) {
	s, err := Load(fixturePaths(), sidA, nil)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := InventoryFiles(s)
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, f := range inv.Files {
		if f.Category != CatSession || f.Root != "testdata/config" {
			t.Errorf("entry %+v: wrong root/category", f)
		}
		if Forbidden(f.Rel) {
			t.Errorf("forbidden path in inventory: %s", f.Rel)
		}
		if !f.Mode.IsDir() {
			rels = append(rels, f.Rel)
		}
	}
	sort.Strings(rels)
	const proj = "projects/-home-alice-github-example-widget/"
	want := []string{
		"file-history/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/0a1b2c3d4e5f60718293a4b5c6d7e8f9@v1",
		proj + "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13.jsonl",
		proj + "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/subagents/agent-0f8e7d6c.jsonl",
		proj + "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/subagents/agent-0f8e7d6c.meta.json",
		proj + "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/tool-results/toolu_01Ab3.txt",
		"session-env/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/env.json",
		"tasks/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13/1.json",
		"todos/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13-agent-0f8e7d6c.json",
	}
	if strings.Join(rels, "\n") != strings.Join(want, "\n") {
		t.Fatalf("files:\n%s\nwant:\n%s", strings.Join(rels, "\n"), strings.Join(want, "\n"))
	}
	for _, f := range inv.Files {
		wantRewrite := strings.HasSuffix(f.Rel, ".json") || strings.HasSuffix(f.Rel, ".jsonl")
		if !f.Mode.IsDir() && f.Rewrite != wantRewrite {
			t.Errorf("%s: Rewrite=%v", f.Rel, f.Rewrite)
		}
		if f.Rel == proj+"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13.jsonl" && f.Path() != filepath.Join("testdata/config", f.Rel) {
			t.Errorf("Path() = %s", f.Path())
		}
	}
	if len(inv.Skipped) != 1 || !strings.HasSuffix(inv.Skipped[0].Path, "/.lock") {
		t.Fatalf("skipped = %+v", inv.Skipped)
	}
	if len(inv.Memory) != 1 || inv.Memory[0].Rel != proj+"memory/MEMORY.md" {
		t.Fatalf("memory = %+v", inv.Memory)
	}
	// the other session's transcript and the index are not ours to move
	for _, f := range inv.Files {
		if strings.Contains(f.Rel, string(sidB)) || strings.HasSuffix(f.Rel, "sessions-index.json") {
			t.Errorf("unexpected %s", f.Rel)
		}
	}
}

// A config dir containing every forbidden path, plus symlinks from session
// dirs pointing at them: nothing forbidden may come out, symlinks are
// recorded as symlinks (never followed), fifos are skipped and reported.
func TestInventoryNeverReturnsForbidden(t *testing.T) {
	dir := t.TempDir()
	p := NewPaths("/home/alice", dir, dir)
	const sid = "deadbeef-0000-4000-8000-000000000001"
	proj := p.ProjectDir("/home/alice/x")
	mustWrite(t, proj+"/"+sid+".jsonl", `{"type":"user","cwd":"/home/alice/x","sessionId":"`+sid+`","message":{"content":"hi"}}`+"\n")
	for _, f := range []string{".credentials.json", ".claude.json", "settings.json", "sessions/1.json", "sessions/1.ab.key", "plugins/installed_plugins.json"} {
		mustWrite(t, filepath.Join(dir, f), "{}")
	}
	mustMkdir(t, filepath.Join(dir, "tasks", sid))
	if err := os.Symlink(filepath.Join(dir, ".credentials.json"), filepath.Join(dir, "tasks", sid, "creds")); err != nil {
		t.Fatal(err)
	}
	if err := syscallMkfifo(filepath.Join(dir, "tasks", sid, "pipe")); err != nil {
		t.Fatal(err)
	}
	s, err := Load(p, ID(sid), nil)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := InventoryFiles(s)
	if err != nil {
		t.Fatal(err)
	}
	var sawLink bool
	for _, f := range inv.Files {
		if Forbidden(f.Rel) || strings.Contains(f.Rel, "credentials") && f.Symlink == "" {
			t.Errorf("forbidden content leaked: %+v", f)
		}
		if f.Symlink != "" {
			sawLink = true
			if f.Rel != "tasks/"+sid+"/creds" || f.Size != 0 {
				t.Errorf("symlink entry %+v", f)
			}
		}
	}
	if !sawLink {
		t.Fatal("symlink must be recorded as a symlink entry")
	}
	if len(inv.Skipped) != 1 || !strings.Contains(inv.Skipped[0].Reason, "fifo") {
		t.Fatalf("skipped = %+v", inv.Skipped)
	}
}
```

And `internal/session/fifo_test.go`:

```go
package session

import "syscall"

func syscallMkfifo(path string) error { return syscall.Mkfifo(path, 0o600) }
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/session/ -run 'TestForbidden|TestInventory' -v`
Expected: FAIL — `undefined: Forbidden`, `undefined: InventoryFiles`.

- [ ] **Step 4: Implement `internal/session/inventory.go`**

```go
package session

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Category classifies transferred files (spec §7.1).
type Category string

const (
	CatSession  Category = "session"
	CatRepo     Category = "repo"
	CatWorktree Category = "worktree"
	CatCapture  Category = "capture"
	CatPack     Category = "pack"
)

// FileEntry is one file/dir/symlink to move. Rel is relative to Root so the
// destination path is Root' + Rel after rewriting Root.
type FileEntry struct {
	Root     string // absolute root this entry belongs to (e.g. ConfigDir or repo dir)
	Rel      string // slash-separated, relative to Root ("" for the root dir itself)
	Category Category
	Size     int64
	Mode     fs.FileMode
	ModTime  time.Time
	Symlink  string // link target if a symlink
	Rewrite  bool   // JSON content must go through the path map
}

// Path is filepath.Join(Root, Rel).
func (e FileEntry) Path() string { return filepath.Join(e.Root, filepath.FromSlash(e.Rel)) }

// Skipped is a path the inventory refused with the reason.
type Skipped struct{ Path, Reason string }

// Inventory lists every session file to move (spec §3 table, "yes" rows).
type Inventory struct {
	Files   []FileEntry
	Skipped []Skipped
	Memory  []FileEntry // projects/<munged>/memory/** (copied only if absent on dest)
}

// Forbidden reports whether rel (relative to ConfigDir) may never be moved
// (spec §7.1): credentials, the registry, messaging keys, the global json,
// settings, plugins.
func Forbidden(rel string) bool {
	rel = strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(rel)), "/")
	switch rel {
	case ".credentials.json", ".claude.json", "settings.json", "settings.local.json", "sessions", "plugins":
		return true
	}
	if strings.HasPrefix(rel, "sessions/") || strings.HasPrefix(rel, "plugins/") {
		return true
	}
	return strings.HasSuffix(rel, ".key")
}

// InventoryFiles walks the session's directories under ConfigDir.
func InventoryFiles(s *Session) (*Inventory, error) {
	cfg := s.Paths.ConfigDir
	inv := &Inventory{}
	id := string(s.ID)
	projRel, err := filepath.Rel(cfg, s.ProjectDir)
	if err != nil {
		return nil, fmt.Errorf("project dir %s is not under %s: %w", s.ProjectDir, cfg, err)
	}
	projRel = filepath.ToSlash(projRel)

	// Roots to walk (relative to ConfigDir). Missing ones are simply absent.
	roots := []string{
		projRel + "/" + id + ".jsonl",
		projRel + "/" + id,
		"file-history/" + id,
		"tasks/" + id,
		"session-env/" + id,
	}
	todos, _ := filepath.Glob(filepath.Join(cfg, "todos", id+"*.json"))
	for _, tpath := range todos {
		roots = append(roots, "todos/"+filepath.Base(tpath))
	}
	for _, r := range roots {
		if err := walkInto(cfg, r, &inv.Files, &inv.Skipped); err != nil {
			return nil, err
		}
	}
	if err := walkInto(cfg, projRel+"/memory", &inv.Memory, &inv.Skipped); err != nil {
		return nil, err
	}
	sort.Slice(inv.Files, func(i, j int) bool { return inv.Files[i].Rel < inv.Files[j].Rel })
	sort.Slice(inv.Memory, func(i, j int) bool { return inv.Memory[i].Rel < inv.Memory[j].Rel })
	return inv, nil
}

// walkInto adds every entry under root/rel (a file or a directory) to out.
// Symlinks are recorded, never followed. Sockets, fifos, devices and the
// tasks .lock file go to skipped. Anything forbidden goes to skipped too —
// belt and braces; the roots above never include such paths.
func walkInto(root, rel string, out *[]FileEntry, skipped *[]Skipped) error {
	start := filepath.Join(root, filepath.FromSlash(rel))
	if _, err := os.Lstat(start); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			*skipped = append(*skipped, Skipped{Path: p, Reason: "unreadable: " + err.Error()})
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		r, _ := filepath.Rel(root, p)
		r = filepath.ToSlash(r)
		if Forbidden(r) {
			*skipped = append(*skipped, Skipped{Path: p, Reason: "forbidden"})
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			*skipped = append(*skipped, Skipped{Path: p, Reason: "stat: " + err.Error()})
			return nil
		}
		e := FileEntry{Root: root, Rel: r, Category: CatSession, Mode: info.Mode(), ModTime: info.ModTime()}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				*skipped = append(*skipped, Skipped{Path: p, Reason: "readlink: " + err.Error()})
				return nil
			}
			e.Symlink = target
		case info.Mode()&fs.ModeNamedPipe != 0:
			*skipped = append(*skipped, Skipped{Path: p, Reason: "fifo"})
			return nil
		case info.Mode()&fs.ModeSocket != 0:
			*skipped = append(*skipped, Skipped{Path: p, Reason: "socket"})
			return nil
		case info.Mode()&fs.ModeDevice != 0 || info.Mode()&fs.ModeCharDevice != 0:
			*skipped = append(*skipped, Skipped{Path: p, Reason: "device"})
			return nil
		case d.IsDir():
			// keep the directory entry (mode) so empty dirs are recreated
		case d.Name() == ".lock":
			*skipped = append(*skipped, Skipped{Path: p, Reason: "lock file"})
			return nil
		default:
			e.Size = info.Size()
			e.Rewrite = strings.HasSuffix(r, ".json") || strings.HasSuffix(r, ".jsonl")
		}
		*out = append(*out, e)
		return nil
	})
}
```

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/session/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/session
git commit -m "feat(session): file inventory with the forbidden list"
```

---

### Task 9: `ScanUsage`

**Files:**
- Create: `internal/session/usage.go`
- Test: `internal/session/usage_test.go`

**Interfaces:**
- Consumes: `Session`.
- Produces: `Usage{MCPServers, Skills, Plugins, SubagentTypes, PermissionModes map[string]bool}`, `ScanUsage(s *Session) (*Usage, error)`.

- [ ] **Step 1: Write the failing test**

`internal/session/usage_test.go`:

```go
package session

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestScanUsage(t *testing.T) {
	s, err := Load(fixturePaths(), sidA, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := ScanUsage(s)
	if err != nil {
		t.Fatal(err)
	}
	want := &Usage{
		MCPServers:      map[string]bool{"playwright": true, "filesystem": true}, // filesystem comes from the sub-agent transcript
		Skills:          map[string]bool{"superpowers:test-driven-development": true},
		Plugins:         map[string]bool{"superpowers@claude-plugins-official": true},
		SubagentTypes:   map[string]bool{"Explore": true},
		PermissionModes: map[string]bool{"acceptEdits": true},
	}
	if diff := cmp.Diff(want, u); diff != "" {
		t.Fatal(diff)
	}
}

func TestScanUsageEmptySession(t *testing.T) {
	s, err := Load(fixturePaths(), sidB, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := ScanUsage(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.MCPServers)+len(u.Skills)+len(u.Plugins)+len(u.SubagentTypes)+len(u.PermissionModes) != 0 {
		t.Fatalf("%+v", u)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/session/ -run TestScanUsage -v`
Expected: FAIL — `undefined: ScanUsage`.

- [ ] **Step 3: Implement `internal/session/usage.go`**

```go
package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Usage is what the session actually used (spec §10).
type Usage struct {
	MCPServers      map[string]bool // from tool_use names mcp__<server>__<tool>
	Skills          map[string]bool // Skill tool "skill" arg + attributionSkill
	Plugins         map[string]bool // attributionPlugin
	SubagentTypes   map[string]bool // Agent tool subagent_type
	PermissionModes map[string]bool // permission-mode records
}

func newUsage() *Usage {
	return &Usage{MCPServers: map[string]bool{}, Skills: map[string]bool{}, Plugins: map[string]bool{},
		SubagentTypes: map[string]bool{}, PermissionModes: map[string]bool{}}
}

// ScanUsage walks every record of the main transcript and the sub-agent
// transcripts generically (any nesting), so it keeps working when Claude
// moves fields around.
func ScanUsage(s *Session) (*Usage, error) {
	u := newUsage()
	files := []string{s.Transcript}
	sub, err := filepath.Glob(filepath.Join(s.ProjectDir, string(s.ID), "subagents", "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("glob subagents: %w", err)
	}
	files = append(files, sub...)
	for _, f := range files {
		if err := scanUsageFile(f, u); err != nil {
			return nil, err
		}
	}
	return u, nil
}

func scanUsageFile(path string, u *Usage) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("scan usage: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), scannerBuf)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec any
		if json.Unmarshal(line, &rec) != nil {
			continue // unparseable lines carry no usage we can read
		}
		walkUsage(rec, u)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("scan usage %s: %w", path, err)
	}
	return nil
}

func walkUsage(v any, u *Usage) {
	switch x := v.(type) {
	case map[string]any:
		typ, _ := x["type"].(string)
		if typ == "tool_use" {
			name, _ := x["name"].(string)
			input, _ := x["input"].(map[string]any)
			switch {
			case strings.HasPrefix(name, "mcp__"):
				if parts := strings.SplitN(name, "__", 3); len(parts) >= 2 && parts[1] != "" {
					u.MCPServers[parts[1]] = true
				}
			case name == "Skill":
				if sk, _ := input["skill"].(string); sk != "" {
					u.Skills[sk] = true
				}
			case name == "Agent" || name == "Task":
				if st, _ := input["subagent_type"].(string); st != "" {
					u.SubagentTypes[st] = true
				}
			}
		}
		if typ == "permission-mode" {
			if m, _ := x["permissionMode"].(string); m != "" {
				u.PermissionModes[m] = true
			}
		}
		if s, _ := x["attributionSkill"].(string); s != "" {
			u.Skills[s] = true
		}
		if s, _ := x["attributionPlugin"].(string); s != "" {
			u.Plugins[s] = true
		}
		for _, c := range x {
			walkUsage(c, u)
		}
	case []any:
		for _, c := range x {
			walkUsage(c, u)
		}
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race ./internal/session/ -run TestScanUsage -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/session
git commit -m "feat(session): ScanUsage over main and sub-agent transcripts"
```

---

### Task 10: `PathMap` and the JSON rewrite engine

**Files:**
- Create: `internal/session/pathmap.go`, `internal/session/rewrite.go`
- Test: `internal/session/pathmap_test.go`, `internal/session/rewrite_test.go`

**Interfaces:**
- Produces: `Mapping{From, To}`, `PathMap`, `NewPathMap(maps ...Mapping) PathMap` (panics on invalid input — validate with `ParseMappings` first), `ParseMappings(specs []string) ([]Mapping, error)` (`SRC=DST` strings), `(PathMap).Apply/ApplyPath/Empty`, `RewriteStats{Records, Rewritten, Unparseable}`, `RewriteJSONL(r io.Reader, w io.Writer, m PathMap) (RewriteStats, error)`, `RewriteJSON(r io.Reader, w io.Writer, m PathMap) (RewriteStats, error)`.

- [ ] **Step 1: Write the failing tests**

`internal/session/pathmap_test.go`:

```go
package session

import (
	"strings"
	"testing"
)

func TestNewPathMapOrdersLongestFirst(t *testing.T) {
	m := NewPathMap(Mapping{"/home/alice", "/home/bob"}, Mapping{"/home/alice/github/example/widget", "/srv/widget"})
	if m[0].From != "/home/alice/github/example/widget" {
		t.Fatalf("order: %+v", m)
	}
	if m.Empty() || !NewPathMap().Empty() {
		t.Fatal("Empty")
	}
}

func TestNewPathMapPanicsOnBadInput(t *testing.T) {
	for _, bad := range [][]Mapping{
		{{"relative", "/x"}},
		{{"/x", "relative"}},
		{{"/x", "/y"}, {"/x", "/z"}},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("no panic for %+v", bad)
				}
			}()
			NewPathMap(bad...)
		}()
	}
}

func TestParseMappings(t *testing.T) {
	ms, err := ParseMappings([]string{"/home/alice=/home/bob", "/a/b/=/c/d/"})
	if err != nil || len(ms) != 2 || ms[0].To != "/home/bob" || ms[1].From != "/a/b" || ms[1].To != "/c/d" {
		t.Fatalf("%+v %v", ms, err)
	}
	for _, bad := range []string{"nope", "=/x", "/x=", "rel=/x", "/x=rel"} {
		if _, err := ParseMappings([]string{bad}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestApplyPath(t *testing.T) {
	m := NewPathMap(Mapping{"/home/alice", "/home/bob"}, Mapping{"/home/alice/github/example/widget", "/srv/widget"})
	cases := map[string]string{
		"/home/alice":                              "/home/bob",
		"/home/alice/x":                            "/home/bob/x",
		"/home/alice/github/example/widget/main.go": "/srv/widget/main.go",
		"/home/alicent/x":                          "/home/alicent/x", // not a boundary
		"/opt/home/alice":                          "/opt/home/alice", // not a prefix
		"relative/home/alice":                      "relative/home/alice",
	}
	for in, want := range cases {
		if got := m.ApplyPath(in); got != want {
			t.Errorf("ApplyPath(%q) = %q want %q", in, got, want)
		}
	}
}

func TestApplyInsideStrings(t *testing.T) {
	m := NewPathMap(Mapping{"/home/alice", "/home/bob"})
	cases := map[string]string{
		"/home/alice/x":                          "/home/bob/x",
		"gitdir: /home/alice/r/.git/worktrees/w": "gitdir: /home/bob/r/.git/worktrees/w",
		"cd /home/alice && ls /home/alice/x":     "cd /home/bob && ls /home/bob/x",
		"see /home/alice.":                       "see /home/bob.",
		"/home/alicent":                          "/home/alicent",
		"x/home/alice":                           "x/home/alice",
		"no paths here":                          "no paths here",
		"\"/home/alice\"":                        "\"/home/bob\"",
	}
	for in, want := range cases {
		if got := m.Apply(in); got != want {
			t.Errorf("Apply(%q) = %q want %q", in, got, want)
		}
	}
	if strings.Contains(NewPathMap().Apply("/home/alice"), "bob") {
		t.Fatal("empty map must be identity")
	}
}
```

`internal/session/rewrite_test.go`:

```go
package session

import (
	"bytes"
	"strings"
	"testing"
)

func TestRewriteJSONL(t *testing.T) {
	m := NewPathMap(Mapping{"/home/alice", "/home/bob"})
	in := strings.Join([]string{
		`{"type":"user","cwd":"/home/alice/p","unknownField":{"deep":["/home/alice/q",1756289730123,0.1,1e21,true,null]},"n":12345678901234567890}`,
		`this line is not json`,
		`{"snapshot":{"trackedFileBackups":{"/home/alice/p/main.go":{"version":1}}},"html":"<b>&</b>"}`,
		``,
		`{"nothing":"to rewrite"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	st, err := RewriteJSONL(strings.NewReader(in), &out, m)
	if err != nil {
		t.Fatal(err)
	}
	if st.Records != 4 || st.Rewritten != 2 || st.Unparseable != 1 {
		t.Fatalf("stats %+v", st)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d lines:\n%s", len(lines), out.String())
	}
	checks := []struct{ line int; want []string }{
		{0, []string{`"cwd":"/home/bob/p"`, `"/home/bob/q"`, `1756289730123`, `0.1`, `1e21`, `true`, `null`, `12345678901234567890`, `"unknownField"`}},
		{1, []string{`this line is not json`}},
		{2, []string{`"/home/bob/p/main.go":{"version":1}`, `"<b>&</b>"`}}, // keys rewritten; HTML not escaped
		{4, []string{`{"nothing":"to rewrite"}`}},
	}
	for _, c := range checks {
		for _, w := range c.want {
			if !strings.Contains(lines[c.line], w) {
				t.Errorf("line %d = %s\n  missing %s", c.line, lines[c.line], w)
			}
		}
	}
	if lines[3] != "" {
		t.Errorf("blank line must stay blank: %q", lines[3])
	}
	if lines[1] != "this line is not json" {
		t.Errorf("unparseable line must be verbatim: %q", lines[1])
	}
}

func TestRewriteJSONLLastLineWithoutNewline(t *testing.T) {
	var out bytes.Buffer
	st, err := RewriteJSONL(strings.NewReader(`{"a":"/home/alice"}`), &out, NewPathMap(Mapping{"/home/alice", "/home/bob"}))
	if err != nil || st.Records != 1 || out.String() != "{\"a\":\"/home/bob\"}\n" {
		t.Fatalf("%q %+v %v", out.String(), st, err)
	}
}

func TestRewriteJSON(t *testing.T) {
	in := `{"projects":{"/home/alice/p":{"allowedTools":["Bash(ls /home/alice/p)"]}},"numStartups":12,"keep":"<x>"}`
	var out bytes.Buffer
	st, err := RewriteJSON(strings.NewReader(in), &out, NewPathMap(Mapping{"/home/alice", "/home/bob"}))
	if err != nil || st.Records != 1 || st.Rewritten != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	for _, w := range []string{`"/home/bob/p": {`, `"Bash(ls /home/bob/p)"`, `"numStartups": 12`, `"<x>"`} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("missing %s in\n%s", w, out.String())
		}
	}
	if _, err := RewriteJSON(strings.NewReader("{broken"), &out, NewPathMap()); err == nil {
		t.Fatal("broken document must be an error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/session/ -run 'TestNewPathMap|TestParseMappings|TestApply|TestRewrite' -v`
Expected: FAIL — `undefined: NewPathMap`, `undefined: RewriteJSONL`.

- [ ] **Step 3: Implement `internal/session/pathmap.go`**

```go
package session

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Mapping rewrites the path prefix From to To.
type Mapping struct{ From, To string }

// PathMap is an ordered prefix rewrite (longest prefix first; spec §7.2).
type PathMap []Mapping

// ParseMappings parses "SRC=DST" strings (trailing slashes trimmed) and
// validates that both sides are absolute.
func ParseMappings(specs []string) ([]Mapping, error) {
	var out []Mapping
	for _, s := range specs {
		from, to, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fmt.Errorf("--map %q: expected SRC=DST", s)
		}
		from, to = cleanAbs(from), cleanAbs(to)
		if from == "" || to == "" {
			return nil, fmt.Errorf("--map %q: both sides must be absolute paths", s)
		}
		out = append(out, Mapping{From: from, To: to})
	}
	return out, nil
}

// cleanAbs returns the cleaned absolute path, or "" if p is not absolute.
func cleanAbs(p string) string {
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, "/") {
		return ""
	}
	return path.Clean(p)
}

// NewPathMap sorts by len(From) descending. It panics on a relative path or
// a duplicate From: callers validate user input with ParseMappings first,
// so a panic here is a programming error.
func NewPathMap(maps ...Mapping) PathMap {
	seen := map[string]bool{}
	out := make(PathMap, 0, len(maps))
	for _, m := range maps {
		from, to := cleanAbs(m.From), cleanAbs(m.To)
		if from == "" || to == "" {
			panic(fmt.Sprintf("session.NewPathMap: mapping %q -> %q is not absolute", m.From, m.To))
		}
		if seen[from] {
			panic(fmt.Sprintf("session.NewPathMap: duplicate From %q", from))
		}
		seen[from] = true
		out = append(out, Mapping{From: from, To: to})
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].From) > len(out[j].From) })
	return out
}

func (m PathMap) Empty() bool { return len(m) == 0 }

// ApplyPath rewrites p when it equals a From or starts with From + "/".
func (m PathMap) ApplyPath(p string) string {
	for _, mp := range m {
		if p == mp.From {
			return mp.To
		}
		if strings.HasPrefix(p, mp.From+"/") {
			return mp.To + p[len(mp.From):]
		}
	}
	return p
}

// isPathByte reports whether c can be part of a path inside free text; the
// complement delimits path boundaries in Apply.
func isPathByte(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '"', '\'', '=', ':', ',', ';', '(', ')', '[', ']', '{', '}', '<', '>', '|', '&':
		return false
	}
	return true
}

// Apply rewrites every occurrence of a From inside s that starts at a
// boundary (start of string or a non-path byte) and ends at a boundary
// ("/", end of string, or a non-path byte). Used for JSON string values,
// which may embed paths in commands and messages.
func (m PathMap) Apply(s string) string {
	if len(m) == 0 || !strings.Contains(s, "/") {
		return s
	}
	var b strings.Builder
	i := 0
	for i < len(s) {
		if i == 0 || !isPathByte(s[i-1]) {
			matched := false
			for _, mp := range m {
				if !strings.HasPrefix(s[i:], mp.From) {
					continue
				}
				end := i + len(mp.From)
				if end == len(s) || s[end] == '/' || !isPathByte(s[end]) || s[end] == '.' && (end+1 == len(s) || !isPathByte(s[end+1])) {
					b.WriteString(mp.To)
					i = end
					matched = true
					break
				}
			}
			if matched {
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
```

- [ ] **Step 4: Implement `internal/session/rewrite.go`**

```go
package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// RewriteStats reports what a rewrite touched.
type RewriteStats struct{ Records, Rewritten, Unparseable int }

// rewriteValue walks a decoded JSON value, rewriting every string (values
// AND object keys — file-history snapshots key on absolute paths) and
// reports whether anything changed. Numbers are json.Number (UseNumber) and
// pass through untouched.
func rewriteValue(v any, m PathMap) (any, bool) {
	switch x := v.(type) {
	case string:
		n := m.Apply(x)
		return n, n != x
	case map[string]any:
		out := make(map[string]any, len(x))
		changed := false
		for k, val := range x {
			nk := m.Apply(k)
			nv, c := rewriteValue(val, m)
			if nk != k || c {
				changed = true
			}
			out[nk] = nv
		}
		return out, changed
	case []any:
		changed := false
		for i, val := range x {
			nv, c := rewriteValue(val, m)
			x[i] = nv
			changed = changed || c
		}
		return x, changed
	default:
		return v, false
	}
}

func decodeOne(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after JSON value")
	}
	return v, nil
}

func encodeCompact(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v) // appends "\n"
}

// RewriteJSONL streams r to w line by line. Each parseable line is decoded
// (UseNumber), rewritten and re-encoded compactly with SetEscapeHTML(false);
// unparseable lines are copied verbatim and counted; blank lines stay blank.
// Every output line ends with "\n".
func RewriteJSONL(r io.Reader, w io.Writer, m PathMap) (RewriteStats, error) {
	var st RewriteStats
	br := bufio.NewReaderSize(r, 1<<20)
	bw := bufio.NewWriterSize(w, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) == 0 && err == io.EOF {
			break
		}
		if err != nil && err != io.EOF {
			return st, fmt.Errorf("rewrite jsonl: read: %w", err)
		}
		body := bytes.TrimRight(line, "\r\n")
		if len(bytes.TrimSpace(body)) == 0 {
			bw.WriteString("\n")
		} else if v, perr := decodeOne(body); perr != nil {
			st.Unparseable++
			bw.Write(body)
			bw.WriteString("\n")
		} else {
			st.Records++
			nv, changed := rewriteValue(v, m)
			if changed {
				st.Rewritten++
			}
			if err := encodeCompact(bw, nv); err != nil {
				return st, fmt.Errorf("rewrite jsonl: encode record %d: %w", st.Records, err)
			}
		}
		if err == io.EOF {
			break
		}
	}
	if err := bw.Flush(); err != nil {
		return st, fmt.Errorf("rewrite jsonl: write: %w", err)
	}
	return st, nil
}

// RewriteJSON rewrites a single JSON document, re-encoded with 2-space
// indentation (as Claude Code writes its .json files).
func RewriteJSON(r io.Reader, w io.Writer, m PathMap) (RewriteStats, error) {
	var st RewriteStats
	data, err := io.ReadAll(r)
	if err != nil {
		return st, fmt.Errorf("rewrite json: read: %w", err)
	}
	v, err := decodeOne(data)
	if err != nil {
		return st, fmt.Errorf("rewrite json: parse: %w", err)
	}
	st.Records = 1
	nv, changed := rewriteValue(v, m)
	if changed {
		st.Rewritten = 1
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(nv); err != nil {
		return st, fmt.Errorf("rewrite json: encode: %w", err)
	}
	return st, nil
}
```

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/session/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/session
git commit -m "feat(session): PathMap and streaming JSON/JSONL rewrite"
```

---

### Task 11: `IsPrefix`, index merge, history merge, `~/.claude.json` project entry

**Files:**
- Create: `internal/session/prefix.go`, `internal/session/index.go`, `internal/session/history.go`, `internal/session/global.go`, fixtures `internal/session/testdata/config/projects/-home-alice-github-example-widget/sessions-index.json`, `internal/session/testdata/config/history.jsonl`, `internal/session/testdata/config/.claude.json`
- Test: `internal/session/prefix_test.go`, `internal/session/merge_test.go`

**Interfaces:**
- Produces: `IsPrefix(existing, incoming string) (bool, error)`, `IndexEntry`, `ReadIndexEntry(projectDir string, id ID) (*IndexEntry, bool, error)`, `MergeIndexEntry(projectDir string, e IndexEntry) error`, `ExtractHistory(historyFile string, id ID) ([]json.RawMessage, error)`, `AppendHistory(historyFile string, lines []json.RawMessage) (added int, err error)`, `ProjectEntry`, `ReadProjectEntry(globalJSON, cwd string) (ProjectEntry, bool, error)`, `AddProjectEntry(globalJSON, cwd string, e ProjectEntry) (added bool, err error)`, plus `WriteFileAtomic(path string, data []byte, mode fs.FileMode) error` (temp + rename in the same directory).

- [ ] **Step 1: Write the fixtures**

`…/projects/-home-alice-github-example-widget/sessions-index.json`:

```json
{
  "version": 1,
  "entries": [
    {
      "sessionId": "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d",
      "fullPath": "/home/alice/.claude/projects/-home-alice-github-example-widget/a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d.jsonl",
      "fileMtime": 1756292405000,
      "firstPrompt": "Explain the release process",
      "summary": "",
      "messageCount": 2,
      "created": "2026-08-27T11:00:00.000Z",
      "modified": "2026-08-27T11:00:05.000Z",
      "gitBranch": "main",
      "projectPath": "/home/alice/github/example/widget",
      "isSidechain": false
    },
    {
      "sessionId": "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13",
      "fullPath": "/home/alice/.claude/projects/-home-alice-github-example-widget/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13.jsonl",
      "fileMtime": 1756289762750,
      "firstPrompt": "Add a --verbose flag to the widget CLI",
      "summary": "Add verbose flag to widget CLI",
      "messageCount": 5,
      "created": "2026-08-27T10:15:30.123Z",
      "modified": "2026-08-27T10:16:02.750Z",
      "gitBranch": "feature/teleport",
      "projectPath": "/home/alice/github/example/widget",
      "isSidechain": false
    }
  ],
  "originalPath": "/home/alice/github/example/widget"
}
```

`internal/session/testdata/config/history.jsonl`:

```json
{"display":"Add a --verbose flag to the widget CLI","pastedContents":{},"timestamp":1756289730123,"project":"/home/alice/github/example/widget","sessionId":"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"}
{"display":"Explain the release process","pastedContents":{},"timestamp":1756292400000,"project":"/home/alice/github/example/widget","sessionId":"a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"}
{"display":"now run the tests","pastedContents":{},"timestamp":1756289800000,"project":"/home/alice/github/example/widget","sessionId":"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"}
```

`internal/session/testdata/config/.claude.json` (the keys we never read — `oauthAccount`, telemetry — are present with harmless values to prove they survive untouched and are never inspected):

```json
{
  "numStartups": 12,
  "installMethod": "native",
  "autoUpdates": true,
  "hasCompletedOnboarding": true,
  "oauthAccount": {
    "accountUuid": "0b1c2d3e-4f50-4617-8293-a4b5c6d7e8f9",
    "emailAddress": "alice@example.com",
    "organizationName": "Example Org"
  },
  "mcpServers": {
    "playwright": {
      "type": "stdio",
      "command": "npx",
      "args": ["@playwright/mcp@latest"]
    }
  },
  "projects": {
    "/home/alice/github/example/widget": {
      "allowedTools": ["Bash(go test:*)", "Bash(go vet:*)"],
      "mcpServers": {
        "filesystem": {
          "type": "stdio",
          "command": "npx",
          "args": ["-y", "@modelcontextprotocol/server-filesystem", "/home/alice/github/example/widget"]
        }
      },
      "enabledMcpjsonServers": ["repo-tools"],
      "disabledMcpjsonServers": [],
      "mcpContextUris": [],
      "hasTrustDialogAccepted": true,
      "hasClaudeMdExternalIncludesApproved": false,
      "lastCost": 0.42,
      "lastSessionId": "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"
    }
  }
}
```

- [ ] **Step 2: Write the failing tests**

`internal/session/prefix_test.go`:

```go
package session

import (
	"path/filepath"
	"testing"
)

func TestIsPrefix(t *testing.T) {
	dir := t.TempDir()
	a, b, c, e := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "c"), filepath.Join(dir, "e")
	mustWrite(t, a, "line1\nline2\n")
	mustWrite(t, b, "line1\nline2\nline3\n")
	mustWrite(t, c, "line1\nlineX\nline3\n")
	mustWrite(t, e, "")
	for _, tc := range []struct {
		existing, incoming string
		want               bool
	}{
		{a, b, true}, {a, a, true}, {b, a, false}, {a, c, false}, {e, a, true}, {a, e, false},
	} {
		got, err := IsPrefix(tc.existing, tc.incoming)
		if err != nil || got != tc.want {
			t.Errorf("IsPrefix(%s,%s) = %v %v want %v", filepath.Base(tc.existing), filepath.Base(tc.incoming), got, err, tc.want)
		}
	}
	if _, err := IsPrefix(a, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing incoming must be an error")
	}
}
```

`internal/session/merge_test.go`:

```go
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// copyFixture copies testdata/config/<rel> into a temp dir and returns the copy's path.
func copyFixture(t *testing.T, rel string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), filepath.Base(rel))
	mustWrite(t, dst, mustRead(t, filepath.Join("testdata/config", rel)))
	return dst
}

func TestReadIndexEntry(t *testing.T) {
	e, ok, err := ReadIndexEntry(fixtureProject, sidA)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	want := &IndexEntry{SessionID: string(sidA),
		FullPath:    "/home/alice/.claude/projects/-home-alice-github-example-widget/3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13.jsonl",
		FileMtime:   1756289762750, FirstPrompt: "Add a --verbose flag to the widget CLI", Summary: "Add verbose flag to widget CLI",
		MessageCount: 5, Created: "2026-08-27T10:15:30.123Z", Modified: "2026-08-27T10:16:02.750Z", GitBranch: "feature/teleport",
		ProjectPath: "/home/alice/github/example/widget"}
	if diff := cmp.Diff(want, e); diff != "" {
		t.Fatal(diff)
	}
	if _, ok, err := ReadIndexEntry(fixtureProject, ID("00000000-0000-4000-8000-000000000000")); err != nil || ok {
		t.Fatalf("unknown id: %v %v", ok, err)
	}
	if _, ok, err := ReadIndexEntry(t.TempDir(), sidA); err != nil || ok {
		t.Fatalf("missing index: %v %v", ok, err)
	}
}

func TestMergeIndexEntry(t *testing.T) {
	proj := filepath.Dir(copyFixture(t, "projects/-home-alice-github-example-widget/sessions-index.json"))
	e, _, _ := ReadIndexEntry(proj, sidA)
	e.FullPath = "/home/bob/.claude/projects/-home-bob-w/" + string(sidA) + ".jsonl"
	e.FileMtime = 42
	if err := MergeIndexEntry(proj, *e); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadIndexEntry(proj, sidA)
	if err != nil || !ok || got.FullPath != e.FullPath || got.FileMtime != 42 {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	if other, ok, _ := ReadIndexEntry(proj, sidB); !ok || other.FirstPrompt != "Explain the release process" {
		t.Fatalf("other entry damaged: %+v", other)
	}
	raw := mustRead(t, filepath.Join(proj, "sessions-index.json"))
	if !strings.Contains(raw, `"originalPath": "/home/alice/github/example/widget"`) || !strings.Contains(raw, `"version": 1`) {
		t.Fatalf("top-level fields lost:\n%s", raw)
	}
	if strings.Count(raw, string(sidA)) != 2 { // sessionId + fullPath, once each
		t.Fatalf("entry duplicated:\n%s", raw)
	}
	// creates the file when absent
	fresh := t.TempDir()
	if err := MergeIndexEntry(fresh, *e); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := ReadIndexEntry(fresh, sidA); !ok {
		t.Fatal("entry not written to a fresh index")
	}
}

func TestExtractAndAppendHistory(t *testing.T) {
	lines, err := ExtractHistory("testdata/config/history.jsonl", sidA)
	if err != nil || len(lines) != 2 {
		t.Fatalf("%d %v", len(lines), err)
	}
	if none, err := ExtractHistory(filepath.Join(t.TempDir(), "none"), sidA); err != nil || len(none) != 0 {
		t.Fatalf("missing history: %v %v", none, err)
	}
	dest := filepath.Join(t.TempDir(), "history.jsonl")
	mustWrite(t, dest, `{"display":"unrelated","pastedContents":{},"timestamp":1,"project":"/home/bob/p","sessionId":"00000000-0000-4000-8000-000000000000"}`) // no trailing newline
	added, err := AppendHistory(dest, lines)
	if err != nil || added != 2 {
		t.Fatalf("added %d %v", added, err)
	}
	added, err = AppendHistory(dest, lines)
	if err != nil || added != 0 {
		t.Fatalf("second append must dedupe: %d %v", added, err)
	}
	got := strings.Split(strings.TrimRight(mustRead(t, dest), "\n"), "\n")
	if len(got) != 3 || !strings.HasPrefix(got[0], `{"display":"unrelated"`) || !strings.Contains(got[2], "now run the tests") {
		t.Fatalf("%q", got)
	}
	for _, l := range got {
		if !json.Valid([]byte(l)) {
			t.Fatalf("corrupt line %q", l)
		}
	}
	// absent file is created
	fresh := filepath.Join(t.TempDir(), "h.jsonl")
	if added, err := AppendHistory(fresh, lines); err != nil || added != 2 {
		t.Fatalf("%d %v", added, err)
	}
}

func TestReadProjectEntry(t *testing.T) {
	e, ok, err := ReadProjectEntry("testdata/config/.claude.json", "/home/alice/github/example/widget")
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if e["hasTrustDialogAccepted"] != true || len(e["allowedTools"].([]any)) != 2 {
		t.Fatalf("%+v", e)
	}
	if _, ok, err := ReadProjectEntry("testdata/config/.claude.json", "/nope"); err != nil || ok {
		t.Fatalf("unknown cwd: %v %v", ok, err)
	}
	if _, ok, err := ReadProjectEntry(filepath.Join(t.TempDir(), "none.json"), "/x"); err != nil || ok {
		t.Fatalf("missing file: %v %v", ok, err)
	}
	if _, _, err := ReadProjectEntry("testdata/config/history.jsonl", "/x"); err == nil {
		t.Fatal("malformed global json must be an error")
	}
}

func TestAddProjectEntry(t *testing.T) {
	g := copyFixture(t, ".claude.json")
	e, _, _ := ReadProjectEntry(g, "/home/alice/github/example/widget")
	added, err := AddProjectEntry(g, "/home/bob/w", e)
	if err != nil || !added {
		t.Fatalf("%v %v", added, err)
	}
	if _, err := os.Stat(g + ".claude-teleport.bak"); err != nil {
		t.Fatal("backup missing")
	}
	added, err = AddProjectEntry(g, "/home/bob/w", e)
	if err != nil || added {
		t.Fatalf("present entry must be a no-op: %v %v", added, err)
	}
	raw := mustRead(t, g)
	for _, w := range []string{`"/home/bob/w"`, `"/home/alice/github/example/widget"`, `"emailAddress": "alice@example.com"`, `"numStartups": 12`, `"lastCost": 0.42`} {
		if !strings.Contains(raw, w) {
			t.Errorf("missing %s in\n%s", w, raw)
		}
	}
	// absent global file: created with just the projects map
	fresh := filepath.Join(t.TempDir(), ".claude.json")
	if added, err := AddProjectEntry(fresh, "/home/bob/w", e); err != nil || !added {
		t.Fatalf("%v %v", added, err)
	}
	if _, err := os.Stat(fresh + ".claude-teleport.bak"); err == nil {
		t.Fatal("no backup should be made when the file did not exist")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/session/ -run 'TestIsPrefix|TestReadIndex|TestMergeIndex|TestExtractAndAppend|TestReadProject|TestAddProject' -v`
Expected: FAIL — undefined functions.

- [ ] **Step 4: Implement**

`internal/session/prefix.go`:

```go
package session

import (
	"bytes"
	"fmt"
	"io"
	"os"
)

// IsPrefix reports whether file `existing` is a byte-prefix of `incoming`
// (streaming; spec §7.3 fast-forward rule). Equal files are a prefix.
func IsPrefix(existing, incoming string) (bool, error) {
	ef, err := os.Open(existing)
	if err != nil {
		return false, fmt.Errorf("is-prefix: %w", err)
	}
	defer ef.Close()
	inf, err := os.Open(incoming)
	if err != nil {
		return false, fmt.Errorf("is-prefix: %w", err)
	}
	defer inf.Close()
	es, err := ef.Stat()
	if err != nil {
		return false, err
	}
	is, err := inf.Stat()
	if err != nil {
		return false, err
	}
	if es.Size() > is.Size() {
		return false, nil
	}
	eb, ib := make([]byte, 64*1024), make([]byte, 64*1024)
	for {
		n, eerr := io.ReadFull(ef, eb)
		if n == 0 && (eerr == io.EOF || eerr == io.ErrUnexpectedEOF) {
			return true, nil
		}
		if eerr != nil && eerr != io.EOF && eerr != io.ErrUnexpectedEOF {
			return false, fmt.Errorf("is-prefix: read %s: %w", existing, eerr)
		}
		if _, err := io.ReadFull(inf, ib[:n]); err != nil {
			return false, fmt.Errorf("is-prefix: read %s: %w", incoming, err)
		}
		if !bytes.Equal(eb[:n], ib[:n]) {
			return false, nil
		}
		if eerr == io.EOF || eerr == io.ErrUnexpectedEOF {
			return true, nil
		}
	}
}
```

`internal/session/index.go`:

```go
package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// IndexEntry is one entry of projects/<munged>/sessions-index.json.
type IndexEntry struct {
	SessionID    string `json:"sessionId"`
	FullPath     string `json:"fullPath"`
	FileMtime    int64  `json:"fileMtime"`
	FirstPrompt  string `json:"firstPrompt"`
	Summary      string `json:"summary"`
	MessageCount int    `json:"messageCount"`
	Created      string `json:"created"`
	Modified     string `json:"modified"`
	GitBranch    string `json:"gitBranch"`
	ProjectPath  string `json:"projectPath"`
	IsSidechain  bool   `json:"isSidechain"`
}

const indexFile = "sessions-index.json"

// WriteFileAtomic writes data to a temp file in path's directory and renames
// it into place, so readers never see a partial file.
func WriteFileAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename %s -> %s: %w", tmpName, path, err)
	}
	return nil
}

// readJSONDoc decodes a JSON object file generically (UseNumber). Absent
// files return (nil, false, nil); malformed files are errors naming the path.
func readJSONDoc(path string) (map[string]any, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	v, err := decodeOne(data)
	if err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("parse %s: top level is not an object", path)
	}
	return obj, true, nil
}

func encodeIndented(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ReadIndexEntry returns the entry for id, ok=false if the file or entry is absent.
func ReadIndexEntry(projectDir string, id ID) (*IndexEntry, bool, error) {
	doc, ok, err := readJSONDoc(filepath.Join(projectDir, indexFile))
	if err != nil || !ok {
		return nil, false, err
	}
	entries, _ := doc["entries"].([]any)
	for _, raw := range entries {
		obj, _ := raw.(map[string]any)
		if obj["sessionId"] != string(id) {
			continue
		}
		b, err := json.Marshal(obj)
		if err != nil {
			return nil, false, err
		}
		var e IndexEntry
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, false, fmt.Errorf("parse index entry %s: %w", id, err)
		}
		return &e, true, nil
	}
	return nil, false, nil
}

// MergeIndexEntry adds or replaces the entry with e.SessionID. Other entries
// and unknown top-level fields are preserved; the file is created (version
// 1, originalPath = e.ProjectPath) if absent. The write is atomic.
func MergeIndexEntry(projectDir string, e IndexEntry) error {
	path := filepath.Join(projectDir, indexFile)
	doc, ok, err := readJSONDoc(path)
	if err != nil {
		return err
	}
	if !ok {
		doc = map[string]any{"version": json.Number("1"), "entries": []any{}, "originalPath": e.ProjectPath}
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var entryObj map[string]any
	if err := json.Unmarshal(b, &entryObj); err != nil {
		return err
	}
	entries, _ := doc["entries"].([]any)
	replaced := false
	for i, raw := range entries {
		if obj, _ := raw.(map[string]any); obj["sessionId"] == e.SessionID {
			entries[i] = entryObj
			replaced = true
		}
	}
	if !replaced {
		entries = append(entries, entryObj)
	}
	doc["entries"] = entries
	out, err := encodeIndented(doc)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return WriteFileAtomic(path, out, 0o600)
}
```

`internal/session/history.go`:

```go
package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

type historyKey struct {
	Timestamp json.RawMessage `json:"timestamp"`
	SessionID string          `json:"sessionId"`
}

func (k historyKey) key() string { return string(bytes.TrimSpace(k.Timestamp)) + "|" + k.SessionID }

// ExtractHistory returns the raw lines of history.jsonl whose sessionId is
// id. A missing file yields nothing.
func ExtractHistory(historyFile string, id ID) ([]json.RawMessage, error) {
	f, err := os.Open(historyFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	defer f.Close()
	var out []json.RawMessage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), scannerBuf)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		var k historyKey
		if len(line) == 0 || json.Unmarshal(line, &k) != nil || k.SessionID != string(id) {
			continue
		}
		out = append(out, json.RawMessage(bytes.Clone(line)))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read history %s: %w", historyFile, err)
	}
	return out, nil
}

// AppendHistory appends lines not already present (matched on
// timestamp+sessionId). The file is created if absent; a missing trailing
// newline on the existing file is repaired first.
func AppendHistory(historyFile string, lines []json.RawMessage) (added int, err error) {
	existing := map[string]bool{}
	needsNewline := false
	if data, rerr := os.ReadFile(historyFile); rerr == nil {
		for _, line := range bytes.Split(data, []byte("\n")) {
			var k historyKey
			if len(bytes.TrimSpace(line)) > 0 && json.Unmarshal(line, &k) == nil {
				existing[k.key()] = true
			}
		}
		needsNewline = len(data) > 0 && data[len(data)-1] != '\n'
	} else if !errors.Is(rerr, fs.ErrNotExist) {
		return 0, fmt.Errorf("read history: %w", rerr)
	}
	f, err := os.OpenFile(historyFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open history: %w", err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	if needsNewline {
		w.WriteString("\n")
	}
	for _, line := range lines {
		var k historyKey
		if json.Unmarshal(line, &k) != nil {
			return added, fmt.Errorf("append history: line is not JSON: %.80s", line)
		}
		if existing[k.key()] {
			continue
		}
		existing[k.key()] = true
		w.Write(bytes.TrimSpace(line))
		w.WriteString("\n")
		added++
	}
	if err := w.Flush(); err != nil {
		return added, fmt.Errorf("write history %s: %w", historyFile, err)
	}
	return added, nil
}
```

`internal/session/global.go`:

```go
package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ProjectEntry is projects["<cwd>"] of ~/.claude.json: opaque, copied whole.
type ProjectEntry = map[string]any

// ReadProjectEntry returns projects[cwd]. Only the "projects" key is
// inspected; nothing else in the file is read into typed structures.
func ReadProjectEntry(globalJSON, cwd string) (ProjectEntry, bool, error) {
	doc, ok, err := readJSONDoc(globalJSON)
	if err != nil || !ok {
		return nil, false, err
	}
	projects, _ := doc["projects"].(map[string]any)
	e, ok := projects[cwd].(map[string]any)
	if !ok {
		return nil, false, nil
	}
	return e, true, nil
}

// AddProjectEntry adds projects[cwd] = e if absent. The existing file is
// first copied to <globalJSON>.claude-teleport.bak, then rewritten via a
// temp file + rename. This is the only global file the tool ever writes,
// and only to add a key; every other key is preserved byte-for-byte in
// value (numbers via json.Number, HTML unescaped).
func AddProjectEntry(globalJSON, cwd string, e ProjectEntry) (added bool, err error) {
	doc, exists, err := readJSONDoc(globalJSON)
	if err != nil {
		return false, err
	}
	if !exists {
		doc = map[string]any{}
	}
	projects, _ := doc["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	if _, present := projects[cwd]; present {
		return false, nil
	}
	if exists {
		if err := copyFile(globalJSON, globalJSON+".claude-teleport.bak"); err != nil {
			return false, fmt.Errorf("backup %s: %w", globalJSON, err)
		}
	}
	projects[cwd] = e
	doc["projects"] = projects
	out, err := encodeIndented(doc)
	if err != nil {
		return false, fmt.Errorf("encode %s: %w", globalJSON, err)
	}
	if err := WriteFileAtomic(globalJSON, out, 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
```

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/session/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/session
git commit -m "feat(session): fast-forward prefix check, index/history merge, project entry add"
```

---

### Task 12: `claudecfg.Collect`

**Files:**
- Create: `internal/claudecfg/collect.go`, fixtures under `internal/claudecfg/testdata/src/` and `internal/claudecfg/testdata/dst/`
- Test: `internal/claudecfg/collect_test.go`

**Interfaces:**
- Consumes: `session.Paths`, `session.NewPaths`.
- Produces: `PluginInfo`, `Permissions`, `Inventory` (interfaces doc fields plus `Skills map[string]bool`, `Agents map[string]bool`), `Collect(p session.Paths, cwd, host, claudeVersion string) (*Inventory, error)`, `TreeHash(path string) (string, error)`, `FileHash(path string) (string, error)`.

- [ ] **Step 1: Write the fixtures**

`internal/claudecfg/testdata/src/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/home/alice/bin/guard.sh"}]}
    ]
  },
  "permissions": {
    "defaultMode": "acceptEdits",
    "allow": ["Bash(go test:*)", "Bash(go vet:*)"],
    "deny": ["Read(./.env)"]
  },
  "env": {"GOFLAGS": "-mod=mod"},
  "enabledPlugins": {"superpowers@claude-plugins-official": true},
  "model": "opus",
  "effortLevel": "high",
  "includeCoAuthoredBy": true
}
```

`internal/claudecfg/testdata/src/.claude.json`:

```json
{
  "numStartups": 12,
  "oauthAccount": {"accountUuid": "0b1c2d3e-4f50-4617-8293-a4b5c6d7e8f9", "emailAddress": "alice@example.com"},
  "mcpServers": {
    "playwright": {"type": "stdio", "command": "npx", "args": ["@playwright/mcp@latest"]}
  },
  "projects": {
    "/home/alice/github/example/widget": {
      "allowedTools": ["Bash(go test:*)"],
      "mcpServers": {
        "filesystem": {"type": "stdio", "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "/home/alice/github/example/widget"]}
      },
      "enabledMcpjsonServers": ["repo-tools"],
      "disabledMcpjsonServers": [],
      "hasTrustDialogAccepted": true
    }
  }
}
```

`internal/claudecfg/testdata/src/plugins/installed_plugins.json`:

```json
{
  "version": 2,
  "plugins": {
    "superpowers@claude-plugins-official": [
      {
        "version": "6.3.0",
        "installedAt": "2026-08-01T09:00:00.000Z",
        "lastUpdated": "2026-08-20T09:00:00.000Z",
        "installPath": "/home/alice/.claude/plugins/cache/claude-plugins-official/superpowers/6.3.0",
        "isLocal": false
      }
    ],
    "netgear-switch@example-marketplace": [
      {
        "version": "0.4.1",
        "installedAt": "2026-08-10T09:00:00.000Z",
        "lastUpdated": "2026-08-10T09:00:00.000Z",
        "installPath": "/home/alice/.claude/plugins/cache/example-marketplace/netgear-switch/0.4.1",
        "isLocal": false
      }
    ]
  }
}
```

`internal/claudecfg/testdata/src/CLAUDE.md` — the line `Prefer table-driven tests.`
`internal/claudecfg/testdata/src/skills/deploy/SKILL.md` — the line `# deploy`
`internal/claudecfg/testdata/src/agents/reviewer.md` — the line `# reviewer`
`internal/claudecfg/testdata/src/commands/ship.md` — the line `Ship it.`
`internal/claudecfg/testdata/src/keybindings.json` — `{"bindings":[{"context":"Chat","bindings":{"ctrl+s":"chat:submit"}}]}`

`internal/claudecfg/testdata/dst/settings.json` (hooks differ, model differs, `deny` same, `allow` differs):

```json
{
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/home/alice/bin/other-guard.sh"}]}
    ]
  },
  "permissions": {
    "defaultMode": "acceptEdits",
    "allow": ["Bash(go test:*)"],
    "deny": ["Read(./.env)"]
  },
  "enabledPlugins": {"superpowers@claude-plugins-official": true},
  "model": "sonnet"
}
```

`internal/claudecfg/testdata/dst/.claude.json` (playwright configured differently; no project entry):

```json
{
  "numStartups": 3,
  "mcpServers": {
    "playwright": {"type": "stdio", "command": "npx", "args": ["@playwright/mcp@0.0.30"]}
  },
  "projects": {}
}
```

`internal/claudecfg/testdata/dst/plugins/installed_plugins.json` (superpowers at an older version; netgear-switch absent):

```json
{
  "version": 2,
  "plugins": {
    "superpowers@claude-plugins-official": [
      {"version": "6.2.0", "installedAt": "2026-07-01T09:00:00.000Z", "lastUpdated": "2026-07-01T09:00:00.000Z",
       "installPath": "/home/alice/.claude/plugins/cache/claude-plugins-official/superpowers/6.2.0", "isLocal": false}
    ]
  }
}
```

`internal/claudecfg/testdata/dst/CLAUDE.md` — the line `Prefer table-driven tests. Always run vet.`
`internal/claudecfg/testdata/dst/skills/deploy/SKILL.md` — the line `# deploy` (identical to src).
No `agents/`, `commands/` or `keybindings.json` on dst.

- [ ] **Step 2: Write the failing test**

`internal/claudecfg/collect_test.go`:

```go
package claudecfg

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mithro/go-claude-teleport/internal/session"
)

const cwd = "/home/alice/github/example/widget"

func srcPaths() session.Paths { return session.NewPaths("/home/alice", "testdata/src", "/tmp/x") }
func dstPaths() session.Paths { return session.NewPaths("/home/alice", "testdata/dst", "/tmp/x") }

func TestCollectSrc(t *testing.T) {
	inv, err := Collect(srcPaths(), cwd, "laptop.example", "2.1.247")
	if err != nil {
		t.Fatal(err)
	}
	want := &Inventory{
		Host: "laptop.example", ClaudeVersion: "2.1.247",
		Hooks:          `{"PreToolUse":[{"hooks":[{"command":"/home/alice/bin/guard.sh","type":"command"}],"matcher":"Bash"}]}`,
		Permissions:    Permissions{DefaultMode: "acceptEdits", Allow: []string{"Bash(go test:*)", "Bash(go vet:*)"}, Deny: []string{"Read(./.env)"}},
		Env:            map[string]string{"GOFLAGS": "-mod=mod"},
		EnabledPlugins: map[string]bool{"superpowers@claude-plugins-official": true},
		Model:          "opus", Effort: "high",
		MCPServers:     map[string]string{"playwright": `{"args":["@playwright/mcp@latest"],"command":"npx","type":"stdio"}`},
		ProjectPresent: true,
		ProjectMCP:     map[string]string{"filesystem": `{"args":["-y","@modelcontextprotocol/server-filesystem","/home/alice/github/example/widget"],"command":"npx","type":"stdio"}`},
		ProjectEnabledMCPJSON:  []string{"repo-tools"},
		ProjectDisabledMCPJSON: []string{},
		AllowedTools:   []string{"Bash(go test:*)"},
		Plugins: map[string]PluginInfo{
			"superpowers@claude-plugins-official": {Version: "6.3.0"},
			"netgear-switch@example-marketplace":  {Version: "0.4.1"},
		},
		Skills: map[string]bool{"deploy": true},
		Agents: map[string]bool{"reviewer": true},
	}
	got := *inv
	if len(got.TreeHashes) != 4 || got.TreeHashes["CLAUDE.md"] == "" || got.TreeHashes["agents"] == "" ||
		got.TreeHashes["skills"] == "" || got.TreeHashes["commands"] == "" || got.KeybindingsHash == "" {
		t.Fatalf("hashes: %+v %q", got.TreeHashes, got.KeybindingsHash)
	}
	got.TreeHashes, got.KeybindingsHash = nil, ""
	if diff := cmp.Diff(want, &got); diff != "" {
		t.Fatal(diff)
	}
}

func TestCollectDstMissingFilesAreNotErrors(t *testing.T) {
	inv, err := Collect(dstPaths(), cwd, "big-storage.example", "2.1.250")
	if err != nil {
		t.Fatal(err)
	}
	if inv.ProjectPresent || inv.Model != "sonnet" || inv.KeybindingsHash != "" || inv.TreeHashes["agents"] != "" ||
		inv.Plugins["superpowers@claude-plugins-official"].Version != "6.2.0" || len(inv.Env) != 0 {
		t.Fatalf("%+v", inv)
	}
	empty, err := Collect(session.NewPaths("/home/nobody", t.TempDir(), "/tmp/x"), cwd, "h", "")
	if err != nil || empty.Hooks != "" || empty.ProjectPresent {
		t.Fatalf("%+v %v", empty, err)
	}
}

func TestCollectMalformedIsError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{nope"), 0o600)
	if _, err := Collect(session.NewPaths("/home/alice", dir, "/tmp/x"), cwd, "h", ""); err == nil {
		t.Fatal("malformed settings.json must be an error")
	}
}

func TestCollectPluginHashesAndSkills(t *testing.T) {
	dir := t.TempDir()
	plug := filepath.Join(dir, "plugins", "cache", "m", "p", "1.0.0")
	os.MkdirAll(filepath.Join(plug, "hooks"), 0o700)
	os.MkdirAll(filepath.Join(plug, "skills", "tdd"), 0o700)
	os.MkdirAll(filepath.Join(plug, "agents"), 0o700)
	os.WriteFile(filepath.Join(plug, "hooks", "hooks.json"), []byte(`{"hooks":{}}`), 0o600)
	os.WriteFile(filepath.Join(plug, ".mcp.json"), []byte(`{"mcpServers":{}}`), 0o600)
	os.WriteFile(filepath.Join(plug, "skills", "tdd", "SKILL.md"), []byte("# tdd"), 0o600)
	os.WriteFile(filepath.Join(plug, "agents", "explorer.md"), []byte("# explorer"), 0o600)
	os.WriteFile(filepath.Join(dir, "plugins", "installed_plugins.json"),
		[]byte(`{"version":2,"plugins":{"p@m":[{"version":"1.0.0","installPath":"`+plug+`"}]}}`), 0o600)
	inv, err := Collect(session.NewPaths("/home/alice", dir, "/tmp/x"), cwd, "h", "")
	if err != nil {
		t.Fatal(err)
	}
	pi := inv.Plugins["p@m"]
	wantHooks, _ := FileHash(filepath.Join(plug, "hooks", "hooks.json"))
	wantMCP, _ := FileHash(filepath.Join(plug, ".mcp.json"))
	if pi.Version != "1.0.0" || pi.HooksHash != wantHooks || pi.MCPHash != wantMCP || wantHooks == "" {
		t.Fatalf("%+v", pi)
	}
	if !inv.Skills["p:tdd"] || !inv.Agents["p:explorer"] {
		t.Fatalf("plugin skills/agents: %+v %+v", inv.Skills, inv.Agents)
	}
}

func TestTreeHash(t *testing.T) {
	a, err := TreeHash("testdata/src/skills")
	if err != nil || a == "" {
		t.Fatal(err)
	}
	b, _ := TreeHash("testdata/dst/skills")
	if a != b {
		t.Fatal("identical trees must hash equal")
	}
	c, _ := TreeHash("testdata/src/CLAUDE.md")
	d, _ := TreeHash("testdata/dst/CLAUDE.md")
	if c == d || c == "" {
		t.Fatal("different files must hash differently")
	}
	if h, err := TreeHash(filepath.Join(t.TempDir(), "missing")); err != nil || h != "" {
		t.Fatalf("missing = %q %v", h, err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/claudecfg/ -v`
Expected: FAIL — `undefined: Collect`.

- [ ] **Step 4: Implement `internal/claudecfg/collect.go`**

```go
// Package claudecfg inventories a host's Claude Code configuration and
// classifies differences between two hosts (spec §10). Nothing here is ever
// copied between hosts — only compared.
package claudecfg

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// PluginInfo describes one installed plugin.
type PluginInfo struct {
	Version   string
	HooksHash string // sha256 of hooks/hooks.json, "" if none
	MCPHash   string // sha256 of .mcp.json, "" if none
}

// Permissions is settings.permissions (the parts that change behaviour).
type Permissions struct {
	DefaultMode string
	Allow, Deny []string
}

// Inventory is everything Compare looks at on one host.
type Inventory struct {
	Host           string
	ClaudeVersion  string
	Hooks          string // canonical JSON of settings.hooks ("" if absent)
	Permissions    Permissions
	Env            map[string]string
	EnabledPlugins map[string]bool
	Model, Effort  string
	MCPServers     map[string]string // name -> canonical JSON config (user level)
	ProjectPresent bool
	ProjectMCP     map[string]string // projects[cwd].mcpServers
	ProjectEnabledMCPJSON, ProjectDisabledMCPJSON []string
	AllowedTools    []string
	Plugins         map[string]PluginInfo // "name@marketplace" -> info
	TreeHashes      map[string]string     // "CLAUDE.md", "agents", "skills", "commands"
	KeybindingsHash string
	Skills          map[string]bool // user skills/<name>/SKILL.md and plugin "<plugin>:<skill>"
	Agents          map[string]bool // user agents/<name>.md and plugin "<plugin>:<agent>"
}

// canonical renders a decoded JSON value deterministically (sorted keys,
// no HTML escaping, no trailing newline).
func canonical(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprintf("<unencodable: %v>", err)
	}
	return strings.TrimRight(buf.String(), "\n")
}

// readObject decodes a JSON object file; absent -> (nil,false,nil).
func readObject(path string) (map[string]any, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("parse %s: top level is not an object", path)
	}
	return obj, true, nil
}

func stringSlice(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func stringMap(v any) map[string]string {
	obj, _ := v.(map[string]any)
	out := make(map[string]string, len(obj))
	for k, val := range obj {
		out[k] = fmt.Sprint(val)
	}
	return out
}

func canonicalMap(v any) map[string]string {
	obj, _ := v.(map[string]any)
	out := make(map[string]string, len(obj))
	for k, val := range obj {
		out[k] = canonical(val)
	}
	return out
}

func stringOf(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// FileHash is the hex sha256 of a file; "" if it does not exist.
func FileHash(path string) (string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// TreeHash hashes a file, or every regular file under a directory (relative
// path + content, sorted). "" if path does not exist.
func TreeHash(path string) (string, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("tree hash %s: %w", path, err)
	}
	if !info.IsDir() {
		return FileHash(path)
	}
	var files []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("tree hash %s: %w", path, err)
	}
	sort.Strings(files)
	h := sha256.New()
	for _, f := range files {
		rel, _ := filepath.Rel(path, f)
		fh, err := FileHash(f)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\n%s\n", filepath.ToSlash(rel), fh)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// namesUnder lists the names of skills (dir/<name>/SKILL.md) or agents
// (dir/<name>.md) under dir, prefixed with prefix.
func namesUnder(dir, prefix string, skills bool, into map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	for _, e := range entries {
		switch {
		case skills && e.IsDir():
			if _, err := os.Stat(filepath.Join(dir, e.Name(), "SKILL.md")); err == nil {
				into[prefix+e.Name()] = true
			}
		case !skills && !e.IsDir() && strings.HasSuffix(e.Name(), ".md"):
			into[prefix+strings.TrimSuffix(e.Name(), ".md")] = true
		}
	}
	return nil
}

// Collect reads the host's configuration. claudeVersion is supplied by the
// caller (registry or `claude --version`). Missing files are not errors;
// malformed files are. Only the keys named in spec §10 are inspected.
func Collect(p session.Paths, cwd, host, claudeVersion string) (*Inventory, error) {
	inv := &Inventory{Host: host, ClaudeVersion: claudeVersion,
		Env: map[string]string{}, EnabledPlugins: map[string]bool{}, MCPServers: map[string]string{},
		ProjectMCP: map[string]string{}, Plugins: map[string]PluginInfo{}, TreeHashes: map[string]string{},
		Skills: map[string]bool{}, Agents: map[string]bool{}}

	// settings.json
	if s, ok, err := readObject(filepath.Join(p.ConfigDir, "settings.json")); err != nil {
		return nil, err
	} else if ok {
		if hooks, present := s["hooks"]; present {
			inv.Hooks = canonical(hooks)
		}
		if perm, _ := s["permissions"].(map[string]any); perm != nil {
			inv.Permissions = Permissions{DefaultMode: stringOf(perm["defaultMode"]),
				Allow: stringSlice(perm["allow"]), Deny: stringSlice(perm["deny"])}
		}
		inv.Env = stringMap(s["env"])
		for k, v := range stringMap(s["enabledPlugins"]) {
			inv.EnabledPlugins[k] = v == "true"
		}
		inv.Model = stringOf(s["model"])
		inv.Effort = stringOf(s["effortLevel"])
	}

	// ~/.claude.json (or <configdir>/.claude.json)
	if g, ok, err := readObject(p.GlobalJSON); err != nil {
		return nil, err
	} else if ok {
		inv.MCPServers = canonicalMap(g["mcpServers"])
		projects, _ := g["projects"].(map[string]any)
		if proj, present := projects[cwd].(map[string]any); present {
			inv.ProjectPresent = true
			inv.ProjectMCP = canonicalMap(proj["mcpServers"])
			inv.ProjectEnabledMCPJSON = stringSlice(proj["enabledMcpjsonServers"])
			inv.ProjectDisabledMCPJSON = stringSlice(proj["disabledMcpjsonServers"])
			inv.AllowedTools = stringSlice(proj["allowedTools"])
		}
	}

	// plugins/installed_plugins.json
	if ip, ok, err := readObject(filepath.Join(p.ConfigDir, "plugins", "installed_plugins.json")); err != nil {
		return nil, err
	} else if ok {
		plugins, _ := ip["plugins"].(map[string]any)
		for name, raw := range plugins {
			var entry map[string]any
			switch x := raw.(type) {
			case []any: // v2: a list of installs, newest first
				if len(x) > 0 {
					entry, _ = x[0].(map[string]any)
				}
			case map[string]any: // v1: a single object
				entry = x
			}
			if entry == nil {
				continue
			}
			pi := PluginInfo{Version: stringOf(entry["version"])}
			if install := stringOf(entry["installPath"]); install != "" {
				var err error
				if pi.HooksHash, err = FileHash(filepath.Join(install, "hooks", "hooks.json")); err != nil {
					return nil, err
				}
				if pi.MCPHash, err = FileHash(filepath.Join(install, ".mcp.json")); err != nil {
					return nil, err
				}
				short, _, _ := strings.Cut(name, "@")
				if err := namesUnder(filepath.Join(install, "skills"), short+":", true, inv.Skills); err != nil {
					return nil, err
				}
				if err := namesUnder(filepath.Join(install, "agents"), short+":", false, inv.Agents); err != nil {
					return nil, err
				}
			}
			inv.Plugins[name] = pi
		}
	}

	// user-level trees, skills, agents, keybindings
	for _, name := range []string{"CLAUDE.md", "agents", "skills", "commands"} {
		h, err := TreeHash(filepath.Join(p.ConfigDir, name))
		if err != nil {
			return nil, err
		}
		inv.TreeHashes[name] = h
	}
	if err := namesUnder(filepath.Join(p.ConfigDir, "skills"), "", true, inv.Skills); err != nil {
		return nil, err
	}
	if err := namesUnder(filepath.Join(p.ConfigDir, "agents"), "", false, inv.Agents); err != nil {
		return nil, err
	}
	var err error
	if inv.KeybindingsHash, err = FileHash(filepath.Join(p.ConfigDir, "keybindings.json")); err != nil {
		return nil, err
	}
	return inv, nil
}
```

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/claudecfg/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/claudecfg
git commit -m "feat(claudecfg): host configuration inventory"
```

---

### Task 13: `claudecfg.Compare`, `Downgrade`, `Render`, `JSON`

**Files:**
- Create: `internal/claudecfg/compare.go`
- Test: `internal/claudecfg/compare_test.go`

**Interfaces:**
- Consumes: `Inventory`, `session.Usage`.
- Produces: `Class` (`Info`, `Warn`, `Block`; `String`, `MarshalJSON`), `Difference{Class, Key, Source, Dest, Reason}`, `Report{Diffs, Blocking}`, `Compare(src, dst *Inventory, usage *session.Usage) Report`, `(Report).Downgrade() Report`, `(Report).Render(w io.Writer)`, `(Report).JSON() ([]byte, error)`.

- [ ] **Step 1: Write the failing test**

`internal/claudecfg/compare_test.go`:

```go
package claudecfg

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

func base() *Inventory {
	return &Inventory{Host: "laptop.example", ClaudeVersion: "2.1.247", Hooks: `{"a":1}`,
		Permissions: Permissions{DefaultMode: "acceptEdits", Allow: []string{"x"}, Deny: []string{"d"}},
		Env: map[string]string{"A": "1"}, EnabledPlugins: map[string]bool{"p@m": true}, Model: "opus", Effort: "high",
		MCPServers: map[string]string{"playwright": "cfg1", "unused": "u"}, ProjectPresent: true,
		ProjectMCP: map[string]string{"filesystem": "fs1"}, AllowedTools: []string{"t"},
		Plugins:    map[string]PluginInfo{"p@m": {Version: "1", HooksHash: "h1"}, "q@m": {Version: "2"}},
		TreeHashes: map[string]string{"CLAUDE.md": "c", "agents": "a", "skills": "s", "commands": "k"}, KeybindingsHash: "kb",
		Skills: map[string]bool{"p:tdd": true, "deploy": true}, Agents: map[string]bool{"reviewer": true}}
}

func classes(r Report) map[string]Class {
	m := map[string]Class{}
	for _, d := range r.Diffs {
		m[d.Key] = d.Class
	}
	return m
}

func TestCompareIdentical(t *testing.T) {
	r := Compare(base(), base(), nil)
	if len(r.Diffs) != 0 || r.Blocking {
		t.Fatalf("%+v", r)
	}
}

// The spec §10 classification table, one row per assertion.
func TestCompareClassification(t *testing.T) {
	src := base()
	dst := base()
	dst.Host = "big-storage.example"
	dst.ClaudeVersion = "2.1.250"
	dst.Hooks = `{"a":2}`
	dst.MCPServers = map[string]string{"playwright": "cfg2", "extra": "e"} // playwright differs, unused absent, extra only on dst
	dst.ProjectMCP = map[string]string{}                                 // filesystem absent
	dst.Plugins = map[string]PluginInfo{"p@m": {Version: "1", HooksHash: "h2"}, "q@m": {Version: "3"}}
	dst.Permissions = Permissions{DefaultMode: "default", Allow: []string{"y"}, Deny: []string{"e"}}
	dst.Model, dst.Effort = "sonnet", "medium"
	dst.Env = map[string]string{"A": "2"}
	dst.AllowedTools = []string{"u"}
	dst.KeybindingsHash = ""
	dst.TreeHashes = map[string]string{"CLAUDE.md": "c2", "agents": "a", "skills": "s", "commands": "k"}
	dst.ProjectPresent = false
	dst.Skills = map[string]bool{"deploy": true}
	dst.Agents = map[string]bool{}
	dst.EnabledPlugins = map[string]bool{}

	usage := &session.Usage{MCPServers: map[string]bool{"playwright": true, "filesystem": true},
		Plugins: map[string]bool{"p@m": true}, Skills: map[string]bool{"p:tdd": true, "project-only": true},
		SubagentTypes: map[string]bool{"reviewer": true, "Explore": true}, PermissionModes: map[string]bool{}}
	r := Compare(src, dst, usage)
	if !r.Blocking {
		t.Fatal("must block")
	}
	want := map[string]Class{
		"hooks":                   Block,
		"plugin.p@m.hooks":        Block,
		"mcp.playwright":          Block, // used, differs
		"mcp.filesystem":          Block, // used, absent
		"mcp.unused":              Warn,  // unused, absent
		"mcp.extra":               Warn,  // only on destination
		"plugin.q@m":              Warn,  // unused, version differs
		"skill.p:tdd":             Block,
		"subagent.reviewer":       Block,
		"permissions.deny":        Block,
		"permissions.defaultMode": Block,
		"permissions.allow":       Warn,
		"claude.version":          Warn,
		"model":                   Warn,
		"effortLevel":             Warn,
		"env":                     Warn,
		"keybindings":             Warn,
		"tree.CLAUDE.md":          Warn,
		"enabledPlugins":          Warn,
		"project":                 Info,
	}
	got := classes(r)
	for k, c := range want {
		if got[k] != c {
			t.Errorf("%s: got %v want %v", k, got[k], c)
		}
	}
	// allowedTools is compared only when both hosts have the project entry
	// (otherwise the entry — allow-list included — is carried over).
	for _, absent := range []string{"skill.project-only", "subagent.Explore", "skill.deploy", "tree.agents", "plugin.p@m", "allowedTools"} {
		if _, ok := got[absent]; ok {
			t.Errorf("%s must not be reported", absent)
		}
	}
	both := base()
	both.AllowedTools = []string{"other"}
	if classes(Compare(src, both, usage))["allowedTools"] != Warn {
		t.Error("allowedTools must warn when both project entries exist and differ")
	}
	// Block rows render first; Downgrade turns them into Warn.
	if r.Diffs[0].Class != Block {
		t.Fatalf("first diff = %+v", r.Diffs[0])
	}
	d := r.Downgrade()
	if d.Blocking {
		t.Fatal("downgraded report must not block")
	}
	for _, x := range d.Diffs {
		if x.Class == Block {
			t.Fatalf("still blocking: %+v", x)
		}
	}
	var buf bytes.Buffer
	r.Render(&buf)
	out := buf.String()
	if !strings.HasPrefix(out, "CLASS") || !strings.Contains(out, "block  hooks") || !strings.Contains(out, "laptop.example") {
		t.Fatalf("render:\n%s", out)
	}
	js, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Blocking bool
		Diffs    []struct{ Class, Key string }
	}
	if err := json.Unmarshal(js, &parsed); err != nil || !parsed.Blocking || parsed.Diffs[0].Class != "block" {
		t.Fatalf("json: %s %v", js, err)
	}
}

// usage == nil means everything is used (compare-config without a session).
func TestCompareNilUsageTreatsAllAsUsed(t *testing.T) {
	src, dst := base(), base()
	dst.MCPServers = map[string]string{"playwright": "cfg1"} // "unused" absent
	dst.Plugins = map[string]PluginInfo{"p@m": {Version: "1", HooksHash: "h1"}}
	r := Compare(src, dst, nil)
	got := classes(r)
	if got["mcp.unused"] != Block || got["plugin.q@m"] != Block {
		t.Fatalf("%+v", got)
	}
}

func TestCompareFixtureDirs(t *testing.T) {
	src, err := Collect(srcPaths(), cwd, "laptop.example", "2.1.247")
	if err != nil {
		t.Fatal(err)
	}
	dst, err := Collect(dstPaths(), cwd, "big-storage.example", "2.1.247")
	if err != nil {
		t.Fatal(err)
	}
	got := classes(Compare(src, dst, nil))
	for k, c := range map[string]Class{"hooks": Block, "mcp.playwright": Block, "mcp.filesystem": Block,
		"plugin.superpowers@claude-plugins-official": Block, "plugin.netgear-switch@example-marketplace": Block,
		"subagent.reviewer": Block, "model": Warn, "tree.CLAUDE.md": Warn, "keybindings": Warn, "project": Info} {
		if got[k] != c {
			t.Errorf("%s: got %v want %v", k, got[k], c)
		}
	}
	if _, ok := got["skill.deploy"]; ok {
		t.Error("identical skill reported")
	}
}

func TestRenderEmpty(t *testing.T) {
	var buf bytes.Buffer
	Report{}.Render(&buf)
	if !strings.Contains(buf.String(), "no configuration differences") {
		t.Fatal(buf.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/claudecfg/ -run TestCompare -v`
Expected: FAIL — `undefined: Compare`.

- [ ] **Step 3: Implement `internal/claudecfg/compare.go`**

```go
package claudecfg

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// Class is how serious a difference is (spec §10).
type Class int

const (
	Info Class = iota
	Warn
	Block
)

func (c Class) String() string {
	switch c {
	case Block:
		return "block"
	case Warn:
		return "warn"
	default:
		return "info"
	}
}

func (c Class) MarshalJSON() ([]byte, error) { return json.Marshal(c.String()) }

// Difference is one row of the drift table.
type Difference struct {
	Class  Class  `json:"class"`
	Key    string `json:"key"` // e.g. "hooks", "mcp.playwright", "plugin.superpowers@claude-plugins-official"
	Source string `json:"source"`
	Dest   string `json:"dest"`
	Reason string `json:"reason"`
}

// Report is the classified drift between two hosts.
type Report struct {
	SourceHost string       `json:"source_host"`
	DestHost   string       `json:"dest_host"`
	Diffs      []Difference `json:"diffs"`
	Blocking   bool         `json:"blocking"` // any Block
}

// short clips a rendering for the table.
func short(s string) string {
	if s == "" {
		return "(absent)"
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 40 {
		return s[:37] + "..."
	}
	return s
}

func hashShort(h string) string {
	if h == "" {
		return "(absent)"
	}
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func sortedKeys[V any](maps ...map[string]V) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range maps {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	sort.Strings(c)
	return c
}

func sameSet(a, b []string) bool { return slices.Equal(sortedCopy(a), sortedCopy(b)) }

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func sameBoolMap(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// mcpConfig is the effective config for a server on one host: the project
// entry wins over the user-level one.
func mcpConfig(inv *Inventory, name string) (string, bool) {
	if c, ok := inv.ProjectMCP[name]; ok {
		return c, true
	}
	c, ok := inv.MCPServers[name]
	return c, ok
}

// Compare classifies differences per spec §10. usage==nil means "everything used".
func Compare(src, dst *Inventory, usage *session.Usage) Report {
	r := Report{SourceHost: src.Host, DestHost: dst.Host}
	add := func(c Class, key, s, d, reason string) {
		r.Diffs = append(r.Diffs, Difference{Class: c, Key: key, Source: s, Dest: d, Reason: reason})
	}
	usedOr := func(m map[string]bool, name string) bool {
		if usage == nil {
			return true
		}
		return m[name]
	}
	pluginUsed := func(name string) bool {
		if usage == nil {
			return true
		}
		shortName, _, _ := strings.Cut(name, "@")
		return usage.Plugins[name] || usage.Plugins[shortName]
	}
	classIf := func(used bool) Class {
		if used {
			return Block
		}
		return Warn
	}

	if src.ClaudeVersion != dst.ClaudeVersion {
		add(Warn, "claude.version", short(src.ClaudeVersion), short(dst.ClaudeVersion), "Claude Code version differs")
	}
	if src.Hooks != dst.Hooks {
		add(Block, "hooks", short(src.Hooks), short(dst.Hooks), "settings.json hooks differ")
	}
	if src.Permissions.DefaultMode != dst.Permissions.DefaultMode {
		add(Block, "permissions.defaultMode", short(src.Permissions.DefaultMode), short(dst.Permissions.DefaultMode), "permission mode differs")
	}
	if !sameSet(src.Permissions.Deny, dst.Permissions.Deny) {
		add(Block, "permissions.deny", short(strings.Join(src.Permissions.Deny, ",")), short(strings.Join(dst.Permissions.Deny, ",")), "deny list differs")
	}
	if !sameSet(src.Permissions.Allow, dst.Permissions.Allow) {
		add(Warn, "permissions.allow", short(strings.Join(src.Permissions.Allow, ",")), short(strings.Join(dst.Permissions.Allow, ",")), "allow list differs")
	}

	// MCP servers: every server the source knows, then destination-only ones.
	srcNames := sortedKeys(src.MCPServers, src.ProjectMCP)
	for _, name := range srcNames {
		sc, _ := mcpConfig(src, name)
		dc, ok := mcpConfig(dst, name)
		used := usedOr(usage2map(usage), name)
		switch {
		case !ok:
			add(classIf(used), "mcp."+name, short(sc), "(absent)", "MCP server absent on destination")
		case sc != dc:
			add(classIf(used), "mcp."+name, short(sc), short(dc), "MCP server configured differently")
		}
	}
	for _, name := range sortedKeys(dst.MCPServers, dst.ProjectMCP) {
		if _, ok := mcpConfig(src, name); !ok {
			dc, _ := mcpConfig(dst, name)
			add(Warn, "mcp."+name, "(absent)", short(dc), "MCP server only on destination")
		}
	}
	if src.ProjectPresent && dst.ProjectPresent {
		if !sameSet(src.ProjectEnabledMCPJSON, dst.ProjectEnabledMCPJSON) || !sameSet(src.ProjectDisabledMCPJSON, dst.ProjectDisabledMCPJSON) {
			add(Warn, "project.mcpjson", short(strings.Join(src.ProjectEnabledMCPJSON, ",")+" / -"+strings.Join(src.ProjectDisabledMCPJSON, ",")),
				short(strings.Join(dst.ProjectEnabledMCPJSON, ",")+" / -"+strings.Join(dst.ProjectDisabledMCPJSON, ",")), "enabled/disabled .mcp.json servers differ")
		}
	}

	// Plugins.
	for _, name := range sortedKeys(src.Plugins) {
		sp := src.Plugins[name]
		dp, ok := dst.Plugins[name]
		used := pluginUsed(name)
		switch {
		case !ok:
			add(classIf(used), "plugin."+name, sp.Version, "(absent)", "plugin absent on destination")
			continue
		case sp.Version != dp.Version:
			add(classIf(used), "plugin."+name, sp.Version, dp.Version, "plugin version differs")
		}
		if sp.HooksHash != dp.HooksHash {
			add(Block, "plugin."+name+".hooks", hashShort(sp.HooksHash), hashShort(dp.HooksHash), "plugin hooks/hooks.json differs")
		}
		if sp.MCPHash != dp.MCPHash {
			add(classIf(used), "plugin."+name+".mcp", hashShort(sp.MCPHash), hashShort(dp.MCPHash), "plugin .mcp.json differs")
		}
	}
	for _, name := range sortedKeys(dst.Plugins) {
		if _, ok := src.Plugins[name]; !ok {
			add(Warn, "plugin."+name, "(absent)", dst.Plugins[name].Version, "plugin only on destination")
		}
	}
	if !sameBoolMap(src.EnabledPlugins, dst.EnabledPlugins) {
		add(Warn, "enabledPlugins", short(fmt.Sprint(sortedKeys(src.EnabledPlugins))), short(fmt.Sprint(sortedKeys(dst.EnabledPlugins))), "enabledPlugins differ")
	}

	// Skills and sub-agent types: only those the source has; a used one
	// missing on the destination blocks, an unused one warns.
	for _, name := range sortedKeys(src.Skills) {
		if !dst.Skills[name] {
			add(classIf(usedOr(usageSkills(usage), name)), "skill."+name, "present", "(absent)", "skill absent on destination")
		}
	}
	for _, name := range sortedKeys(src.Agents) {
		if !dst.Agents[name] {
			add(classIf(usedOr(usageAgents(usage), name)), "subagent."+name, "present", "(absent)", "sub-agent absent on destination")
		}
	}

	// Warn-only rows.
	if src.Model != dst.Model {
		add(Warn, "model", short(src.Model), short(dst.Model), "model differs")
	}
	if src.Effort != dst.Effort {
		add(Warn, "effortLevel", short(src.Effort), short(dst.Effort), "effortLevel differs")
	}
	if !sameMap(src.Env, dst.Env) {
		add(Warn, "env", short(fmt.Sprint(src.Env)), short(fmt.Sprint(dst.Env)), "settings env differs")
	}
	if src.ProjectPresent && dst.ProjectPresent && !sameSet(src.AllowedTools, dst.AllowedTools) {
		add(Warn, "allowedTools", short(strings.Join(src.AllowedTools, ",")), short(strings.Join(dst.AllowedTools, ",")), "project allowedTools differ")
	}
	if src.KeybindingsHash != dst.KeybindingsHash {
		add(Warn, "keybindings", hashShort(src.KeybindingsHash), hashShort(dst.KeybindingsHash), "keybindings.json differs")
	}
	for _, name := range []string{"CLAUDE.md", "agents", "skills", "commands"} {
		if src.TreeHashes[name] != dst.TreeHashes[name] {
			add(Warn, "tree."+name, hashShort(src.TreeHashes[name]), hashShort(dst.TreeHashes[name]), "user-level "+name+" differs")
		}
	}
	if src.ProjectPresent && !dst.ProjectPresent {
		add(Info, "project", "present", "(absent)", "project entry will be added on the destination")
	}

	sort.SliceStable(r.Diffs, func(i, j int) bool { return r.Diffs[i].Class > r.Diffs[j].Class })
	for _, d := range r.Diffs {
		if d.Class == Block {
			r.Blocking = true
		}
	}
	return r
}

func usage2map(u *session.Usage) map[string]bool {
	if u == nil {
		return nil
	}
	return u.MCPServers
}
func usageSkills(u *session.Usage) map[string]bool {
	if u == nil {
		return nil
	}
	return u.Skills
}
func usageAgents(u *session.Usage) map[string]bool {
	if u == nil {
		return nil
	}
	return u.SubagentTypes
}

// Downgrade implements --allow-config-drift: Block -> Warn.
func (r Report) Downgrade() Report {
	out := Report{SourceHost: r.SourceHost, DestHost: r.DestHost, Diffs: slices.Clone(r.Diffs)}
	for i := range out.Diffs {
		if out.Diffs[i].Class == Block {
			out.Diffs[i].Class = Warn
		}
	}
	return out
}

// Render writes an aligned table.
func (r Report) Render(w io.Writer) {
	if r.SourceHost != "" || r.DestHost != "" {
		fmt.Fprintf(w, "configuration drift: %s -> %s\n", r.SourceHost, r.DestHost)
	}
	if len(r.Diffs) == 0 {
		fmt.Fprintln(w, "no configuration differences")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CLASS\tKEY\tSOURCE\tDESTINATION\tREASON")
	for _, d := range r.Diffs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", d.Class, d.Key, d.Source, d.Dest, d.Reason)
	}
	tw.Flush()
	if r.Blocking {
		fmt.Fprintln(w, "blocking differences found (use --allow-config-drift to proceed anyway)")
	}
}

// JSON renders the report for --json.
func (r Report) JSON() ([]byte, error) {
	if r.Diffs == nil {
		r.Diffs = []Difference{}
	}
	return json.MarshalIndent(r, "", "  ")
}
```

`Report.SourceHost`/`DestHost` are additions to the interfaces doc (recorded at the end); `TestCompareClassification` relies on `Render` printing them, and `TestRenderEmpty` passes because an empty `Report{}` has no hosts and prints only the "no configuration differences" line.

- [ ] **Step 4: Run tests**

Run: `go test -race ./internal/claudecfg/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/claudecfg
git commit -m "feat(claudecfg): drift classification, render and JSON"
```

---

### Task 14: `procx` process table, registry lookups, argv recognisers

**Files:**
- Create: `internal/procx/table.go`, `internal/procx/registry.go`, `internal/procx/argv.go`, fixtures under `internal/procx/testdata/proc/` and `internal/procx/testdata/sessions/`
- Test: `internal/procx/table_test.go`

**Interfaces:**
- Consumes: `session.ReadRegistry`, `session.ReadRegistryFile`, `session.ArgvSessionID`, `session.ProcStartTime`.
- Produces: `Proc`, `Table`, `Scan(procRoot string) (*Table, error)`, `(*Table).Get/Children/Subtree/Alive`, `StartTime(procRoot string, pid int) (string, error)`, `RegistryForPID`, `RegistryForSession`, `IsPlaceholderArgv`, `IsClaudeArgv`.

- [ ] **Step 1: Write the fixtures**

Each `stat` file is one line; the number after the 19th field following `)` is the start time. `cmdline` files are NUL-separated with a trailing NUL (write them with `printf 'a\0b\0'`).

| pid | `stat` | `cmdline` (`\0`-separated) |
|---|---|---|
| 1 | `1 (systemd) S 0 1 1 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0` | `/sbin/init` |
| 100 | `100 (bash) S 1 100 100 34816 100 4194304 0 0 0 0 0 0 0 0 20 0 1 0 5000 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0` | `bash` |
| 101 | `101 (claude) S 100 100 100 34816 101 4194560 0 0 0 0 0 0 0 0 20 0 1 0 6000 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0` | `claude` |
| 102 | `102 (tail) S 101 100 100 34816 102 4194560 0 0 0 0 0 0 0 0 20 0 1 0 6100 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0` | `tail` `-f` `x` |
| 200 | `200 (python3) S 1 200 200 34817 200 4194560 0 0 0 0 0 0 0 0 20 0 1 0 8000 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0` | `python3` `/home/alice/bin/claude-resume` `a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d` |
| 300 | `300 (claude-teleport) S 1 300 300 34818 300 4194560 0 0 0 0 0 0 0 0 20 0 1 0 9000 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0` | `claude-teleport` `placeholder` `--resume` `3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13` `--now` |
| 400 | `400 (a (b) c) S 1 400 400 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 9500 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0` | `a (b) c` |

`internal/procx/testdata/sessions/101.json`:

```json
{"pid":101,"sessionId":"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13","cwd":"/home/alice/github/example/widget","procStart":"6000","version":"2.1.247","status":"idle","tmux":"main:@3.%7","name":"widget"}
```

`internal/procx/testdata/sessions/200.json` (stale: pid 200 is now python3 with start 8000, but the registry says 7000):

```json
{"pid":200,"sessionId":"a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d","cwd":"/home/alice/github/example/widget","procStart":7000,"version":"2.1.247","status":"idle","tmux":"","name":""}
```

- [ ] **Step 2: Write the failing test**

`internal/procx/table_test.go`:

```go
package procx

import (
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

func TestScanAndSubtree(t *testing.T) {
	tb, err := Scan("testdata/proc")
	if err != nil {
		t.Fatal(err)
	}
	p, ok := tb.Get(101)
	if !ok || p.Comm != "claude" || p.PPID != 100 || p.StartTime != "6000" || len(p.Cmdline) != 1 {
		t.Fatalf("proc 101 = %+v ok=%v", p, ok)
	}
	if p400, _ := tb.Get(400); p400.Comm != "a (b) c" || p400.StartTime != "9500" {
		t.Fatalf("embedded parens: %+v", p400)
	}
	if got := tb.Children(100); len(got) != 1 || got[0] != 101 {
		t.Fatalf("children = %v", got)
	}
	if got := tb.Subtree(100); len(got) != 3 || got[0] != 100 || got[1] != 101 || got[2] != 102 {
		t.Fatalf("subtree = %v", got)
	}
	if !tb.Alive(101, "6000") || tb.Alive(101, "6001") || tb.Alive(101, "") || tb.Alive(4242, "1") {
		t.Fatal("Alive")
	}
	if st, err := StartTime("testdata/proc", 102); err != nil || st != "6100" {
		t.Fatalf("StartTime = %q %v", st, err)
	}
	if _, err := Scan(t.TempDir() + "/none"); err == nil {
		t.Fatal("missing proc root must be an error")
	}
}

func TestRegistryLookups(t *testing.T) {
	r, ok, err := RegistryForPID("testdata/sessions", 101, "6000")
	if err != nil || !ok || r.SessionID != "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13" {
		t.Fatalf("%+v %v %v", r, ok, err)
	}
	if _, ok, err := RegistryForPID("testdata/sessions", 101, "9999"); err != nil || ok {
		t.Fatalf("stale start time matched: %v %v", ok, err)
	}
	if _, ok, err := RegistryForPID("testdata/sessions", 777, "1"); err != nil || ok {
		t.Fatalf("missing file: %v %v", ok, err)
	}
	if _, ok, err := RegistryForPID("testdata/sessions", 200, "8000"); err != nil || ok {
		t.Fatalf("numeric stale procStart matched: %v %v", ok, err)
	}
	r, ok, err = RegistryForSession("testdata/sessions", session.ID("a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"))
	if err != nil || !ok || r.PID != 200 || r.ProcStart != "7000" {
		t.Fatalf("%+v %v %v", r, ok, err)
	}
	if _, ok, _ := RegistryForSession("testdata/sessions", session.ID("00000000-0000-4000-8000-000000000000")); ok {
		t.Fatal("unknown session found")
	}
}

func TestArgvRecognisers(t *testing.T) {
	tb, _ := Scan("testdata/proc")
	p200, _ := tb.Get(200)
	p300, _ := tb.Get(300)
	p101, _ := tb.Get(101)
	if sid, ok := IsPlaceholderArgv(p200.Cmdline); !ok || sid != "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d" {
		t.Fatalf("claude-resume: %q %v", sid, ok)
	}
	if sid, ok := IsPlaceholderArgv(p300.Cmdline); !ok || sid != "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13" {
		t.Fatalf("placeholder: %q %v", sid, ok)
	}
	if _, ok := IsPlaceholderArgv(p101.Cmdline); ok {
		t.Fatal("claude is not a placeholder")
	}
	if id, ok := IsClaudeArgv(p101.Cmdline); !ok || id != "" {
		t.Fatalf("claude: %q %v", id, ok)
	}
	if id, ok := IsClaudeArgv([]string{"/usr/bin/claude", "--resume", "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"}); !ok || id != "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13" {
		t.Fatalf("claude --resume: %q %v", id, ok)
	}
	if _, ok := IsClaudeArgv(p300.Cmdline); ok {
		t.Fatal("a placeholder is not claude")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/procx/ -v`
Expected: FAIL — `undefined: Scan`.

- [ ] **Step 4: Implement**

`internal/procx/table.go`:

```go
// Package procx reads the process table, looks processes up in Claude's
// registry, freezes/thaws a pid safely, waits for exits and spawns
// detached runners.
package procx

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// Proc is one row of the process table.
type Proc struct {
	PID, PPID int
	Comm      string
	Cmdline   []string
	StartTime string // field 22 of /proc/<pid>/stat, as a string
}

// Table is a snapshot of /proc.
type Table struct {
	byPID    map[int]Proc
	children map[int][]int
}

// StartTime is session.ProcStartTime (re-exported so callers need only procx).
func StartTime(procRoot string, pid int) (string, error) { return session.ProcStartTime(procRoot, pid) }

// Scan reads every numeric directory under procRoot ("/proc" in production).
func Scan(procRoot string) (*Table, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", procRoot, err)
	}
	t := &Table{byPID: map[int]Proc{}, children: map[int][]int{}}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "stat"))
		if err != nil {
			continue // process exited mid-scan
		}
		p, ok := parseStat(pid, stat)
		if !ok {
			continue
		}
		if cl, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "cmdline")); err == nil && len(cl) > 0 {
			for _, part := range bytes.Split(bytes.TrimRight(cl, "\x00"), []byte{0}) {
				p.Cmdline = append(p.Cmdline, string(part))
			}
		}
		t.byPID[pid] = p
		t.children[p.PPID] = append(t.children[p.PPID], pid)
	}
	for ppid := range t.children {
		sort.Ints(t.children[ppid])
	}
	return t, nil
}

// parseStat handles comm containing spaces/parens by splitting on the LAST ')'.
func parseStat(pid int, stat []byte) (Proc, bool) {
	s := string(stat)
	lp, rp := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if lp < 0 || rp < 0 || rp < lp {
		return Proc{}, false
	}
	rest := strings.Fields(s[rp+1:]) // rest[0]=state rest[1]=ppid … rest[19]=starttime
	if len(rest) < 20 {
		return Proc{}, false
	}
	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return Proc{}, false
	}
	return Proc{PID: pid, PPID: ppid, Comm: s[lp+1 : rp], StartTime: rest[19]}, true
}

func (t *Table) Get(pid int) (Proc, bool) { p, ok := t.byPID[pid]; return p, ok }

// Children returns the direct children of pid, ascending.
func (t *Table) Children(pid int) []int { return append([]int(nil), t.children[pid]...) }

// Subtree returns pid and all descendants, breadth-first.
func (t *Table) Subtree(pid int) []int {
	out := []int{pid}
	for i := 0; i < len(out); i++ {
		out = append(out, t.children[out[i]]...)
	}
	return out
}

// Alive reports whether pid exists with exactly startTime. An empty
// startTime never matches: a reused pid must never be trusted.
func (t *Table) Alive(pid int, startTime string) bool {
	p, ok := t.byPID[pid]
	return ok && startTime != "" && p.StartTime == startTime
}
```

`internal/procx/registry.go`:

```go
package procx

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strconv"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// RegistryForPID reads sessions/<pid>.json and validates procStart against
// startTime; a mismatch (reused pid, stale file) is ok=false, not an error.
func RegistryForPID(sessionsDir string, pid int, startTime string) (*session.Registry, bool, error) {
	path := filepath.Join(sessionsDir, strconv.Itoa(pid)+".json")
	r, err := session.ReadRegistryFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if r.ProcStart == "" || r.ProcStart != startTime {
		return nil, false, nil
	}
	return &r, true, nil
}

// RegistryForSession finds the registry entry naming id. Liveness is NOT
// checked here (the entry may be stale); callers verify with Table.Alive.
func RegistryForSession(sessionsDir string, id session.ID) (*session.Registry, bool, error) {
	regs, err := session.ReadRegistry(sessionsDir)
	if err != nil {
		return nil, false, err
	}
	for i := range regs {
		if regs[i].SessionID == string(id) {
			return &regs[i], true, nil
		}
	}
	return nil, false, nil
}
```

`internal/procx/argv.go`:

```go
package procx

import "github.com/mithro/go-claude-teleport/internal/session"

// IsPlaceholderArgv recognises `claude-resume <uuid>` (go-tmux-saver, the
// rcfiles script) and `claude-teleport placeholder … --resume <uuid>`.
func IsPlaceholderArgv(argv []string) (sid string, ok bool) {
	sid, placeholder, ok := session.ArgvSessionID(argv)
	if !ok || !placeholder {
		return "", false
	}
	return sid, true
}

// IsClaudeArgv recognises a real claude process (`claude`, `…/claude`,
// `node …/cli.js`), returning its --resume id if any.
func IsClaudeArgv(argv []string) (resumeID string, ok bool) {
	sid, placeholder, ok := session.ArgvSessionID(argv)
	if !ok || placeholder {
		return "", false
	}
	return sid, true
}
```

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/procx/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/procx
git commit -m "feat(procx): process table, registry lookups, argv recognisers"
```

---

### Task 15: The freezer (`Freeze`/`Thaw`/`RunFreezerHelper`) and the `internal-freezer` command

**Files:**
- Create: `internal/procx/freezer.go`, `internal/cli/freezer.go`
- Modify: `internal/cli/cli.go` (register the command)
- Test: `internal/procx/freezer_test.go`, `internal/procx/main_test.go`

**Interfaces:**
- Consumes: `StartTime`.
- Produces: `Freezer`, `Freeze(selfExe string, pid int, startTime string) (*Freezer, error)`, `(*Freezer).Thaw() error`, `RunFreezerHelper(pid int, startTime string, control *os.File) error`, `ProcState(procRoot string, pid int) (byte, error)` (state letter from `/proc/<pid>/stat`), cli subcommand `internal-freezer <pid> <start>`.

Design (spec §6.1): the owner re-execs itself as `<selfExe> internal-freezer <pid> <start>` with the read end of a pipe on fd 3. The helper verifies the start time, `SIGSTOP`s, prints `stopped` on stdout, then blocks reading fd 3. Any read result — `thaw\n` from the owner or EOF because the owner died — makes it re-verify the start time and `SIGCONT`. The helper ignores SIGINT/SIGHUP/SIGTERM and lives in its own process group so a Ctrl-C aimed at the owner cannot kill it first.

- [ ] **Step 1: Write the failing tests**

`internal/procx/main_test.go` (lets the test binary act as the helper and as a killable owner):

```go
package procx

import (
	"fmt"
	"os"
	"strconv"
	"testing"
)

// TestMain turns the test binary into the freezer helper or a "freeze owner"
// when invoked with those argv, so Freeze can re-exec os.Executable().
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "internal-freezer":
			pid, _ := strconv.Atoi(os.Args[2])
			if err := RunFreezerHelper(pid, os.Args[3], os.NewFile(3, "control")); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		case "freeze-owner":
			// Freeze the target, announce, then hang until killed. The
			// helper must thaw the target when we die.
			pid, _ := strconv.Atoi(os.Args[2])
			self, _ := os.Executable()
			if _, err := Freeze(self, pid, os.Args[3]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Println("frozen")
			select {}
		}
	}
	os.Exit(m.Run())
}
```

`internal/procx/freezer_test.go`:

```go
package procx

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// startSleep spawns `sleep 60` and returns its pid and start time.
func startSleep(t *testing.T) (int, string) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	st, err := StartTime("/proc", cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid, st
}

func waitState(t *testing.T, pid int, want byte) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s, err := ProcState("/proc", pid); err == nil && s == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	s, err := ProcState("/proc", pid)
	t.Fatalf("pid %d state = %c (%v), want %c", pid, s, err, want)
}

func TestFreezeThaw(t *testing.T) {
	pid, st := startSleep(t)
	self, _ := os.Executable()
	f, err := Freeze(self, pid, st)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, pid, 'T')
	if err := f.Thaw(); err != nil {
		t.Fatal(err)
	}
	waitState(t, pid, 'S')
}

func TestFreezeRefusesWrongStartTime(t *testing.T) {
	pid, _ := startSleep(t)
	self, _ := os.Executable()
	if _, err := Freeze(self, pid, "1"); err == nil {
		t.Fatal("wrong start time must refuse")
	}
	if _, err := Freeze(self, pid, ""); err == nil {
		t.Fatal("empty start time must refuse")
	}
	if s, _ := ProcState("/proc", pid); s == 'T' {
		t.Fatal("target must not have been stopped")
	}
}

// The guarantee: when the owner dies (SIGKILL, no cleanup possible) the
// helper sees pipe EOF and SIGCONTs the target.
func TestHelperThawsWhenOwnerDies(t *testing.T) {
	pid, st := startSleep(t)
	self, _ := os.Executable()
	owner := exec.Command(self, "freeze-owner", strconv.Itoa(pid), st)
	owner.Stderr = os.Stderr
	out, err := owner.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "frozen\n" {
		owner.Process.Kill()
		t.Fatalf("owner said %q (%v)", line, err)
	}
	waitState(t, pid, 'T')
	if err := owner.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	owner.Wait()
	waitState(t, pid, 'S')
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/procx/ -run 'TestFreeze|TestHelper' -v`
Expected: FAIL — `undefined: Freeze`, `undefined: RunFreezerHelper`, `undefined: ProcState`.

- [ ] **Step 3: Implement `internal/procx/freezer.go`**

```go
package procx

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ProcState returns the state letter (R, S, D, T, Z, …) of /proc/<pid>/stat.
func ProcState(procRoot string, pid int) (byte, error) {
	path := filepath.Join(procRoot, strconv.Itoa(pid), "stat")
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	rp := bytes.LastIndexByte(data, ')')
	if rp < 0 || rp+2 >= len(data) {
		return 0, fmt.Errorf("parse %s: malformed", path)
	}
	return data[rp+2], nil
}

// Freezer holds a stopped pid; Thaw releases it. If the owning process dies
// first, the helper releases it on pipe EOF (spec §6.1).
type Freezer struct {
	cmd     *exec.Cmd
	control *os.File // write end; helper holds the read end on fd 3
	stderr  *bytes.Buffer
	pid     int
}

func checkStart(pid int, startTime string) error {
	if startTime == "" {
		return fmt.Errorf("pid %d: empty start time (refusing to signal an unverified pid)", pid)
	}
	st, err := StartTime("/proc", pid)
	if err != nil {
		return fmt.Errorf("pid %d: %w", pid, err)
	}
	if st != startTime {
		return fmt.Errorf("pid %d: start time %s != expected %s (pid reused)", pid, st, startTime)
	}
	return nil
}

// Freeze re-execs selfExe as `internal-freezer <pid> <start>` and waits for
// its "stopped" acknowledgement.
func Freeze(selfExe string, pid int, startTime string) (*Freezer, error) {
	if err := checkStart(pid, startTime); err != nil {
		return nil, fmt.Errorf("freeze: %w", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("freeze: pipe: %w", err)
	}
	cmd := exec.Command(selfExe, "internal-freezer", strconv.Itoa(pid), startTime)
	cmd.ExtraFiles = []*os.File{r} // fd 3 in the child
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		r.Close()
		w.Close()
		return nil, fmt.Errorf("freeze: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		r.Close()
		w.Close()
		return nil, fmt.Errorf("freeze: start helper %s: %w", selfExe, err)
	}
	r.Close() // only the helper holds the read end now
	line, rerr := bufio.NewReader(stdout).ReadString('\n')
	if rerr != nil || strings.TrimSpace(line) != "stopped" {
		w.Close()
		cmd.Wait()
		return nil, fmt.Errorf("freeze pid %d: helper did not stop it: %q %s", pid, strings.TrimSpace(line), strings.TrimSpace(stderr.String()))
	}
	return &Freezer{cmd: cmd, control: w, stderr: stderr, pid: pid}, nil
}

// Thaw writes "thaw\n", closes the pipe and waits for the helper to exit.
func (f *Freezer) Thaw() error {
	if _, err := f.control.Write([]byte("thaw\n")); err != nil && !errors.Is(err, os.ErrClosed) && !errors.Is(err, syscall.EPIPE) {
		return fmt.Errorf("thaw pid %d: %w", f.pid, err)
	}
	f.control.Close()
	if err := f.cmd.Wait(); err != nil {
		return fmt.Errorf("thaw pid %d: helper: %w: %s", f.pid, err, strings.TrimSpace(f.stderr.String()))
	}
	return nil
}

// RunFreezerHelper is the helper's main: SIGSTOP, ack on stdout, block on
// control, SIGCONT on data or EOF. It ignores terminal signals so it cannot
// die before thawing. The start time is re-checked before every kill.
func RunFreezerHelper(pid int, startTime string, control *os.File) error {
	signal.Ignore(syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGPIPE, syscall.SIGQUIT)
	if err := checkStart(pid, startTime); err != nil {
		return fmt.Errorf("freezer: %w", err)
	}
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		return fmt.Errorf("freezer: SIGSTOP %d: %w", pid, err)
	}
	fmt.Fprintln(os.Stdout, "stopped")
	buf := make([]byte, 16)
	control.Read(buf) // data ("thaw") or EOF (owner died): either way, thaw
	if err := checkStart(pid, startTime); err != nil {
		return nil // the target is gone or replaced: nothing to thaw
	}
	if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
		return fmt.Errorf("freezer: SIGCONT %d: %w", pid, err)
	}
	return nil
}
```

- [ ] **Step 4: Add the cli command**

`internal/cli/freezer.go`:

```go
package cli

import (
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/procx"
)

// internalFreezerCmd is re-exec'd by procx.Freeze with the control pipe on fd 3.
func (a *app) internalFreezerCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "internal-freezer <pid> <start-time>",
		Short:  "internal: SIGSTOP a pid until fd 3 reports thaw or EOF",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := strconv.Atoi(args[0])
			if err != nil {
				return Exit(ExitUsage, "internal-freezer: bad pid %q", args[0])
			}
			if err := procx.RunFreezerHelper(pid, args[1], os.NewFile(3, "control")); err != nil {
				return Exit(ExitFailed, "%v", err)
			}
			return nil
		},
	}
}
```

In `internal/cli/cli.go`, `rootCmd`, add `root.AddCommand(a.internalFreezerCmd())` after the version command.

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/procx/ ./internal/cli/ -v`
Expected: PASS (the three freezer tests take ~1s).

- [ ] **Step 6: Commit**

```bash
git add internal/procx internal/cli
git commit -m "feat(procx): freezer helper with pipe-on-fd-3 thaw guarantee"
```

---

### Task 16: `WaitGone` and `SpawnDetached`

**Files:**
- Create: `internal/procx/wait.go`, `internal/procx/spawn.go`
- Test: `internal/procx/wait_test.go`, `internal/procx/spawn_test.go`

**Interfaces:**
- Consumes: `Table`, `Alive`.
- Produces: `WaitGone(t func() (*Table, error), pid int, startTime string, timeout, poll time.Duration, sleep func(time.Duration)) error`, `SpawnDetached(argv []string, dir, logPath string, env []string) (int, error)`.

- [ ] **Step 1: Write the failing tests**

`internal/procx/wait_test.go`:

```go
package procx

import (
	"errors"
	"testing"
	"time"
)

func tableWith(pids map[int]string) *Table {
	t := &Table{byPID: map[int]Proc{}, children: map[int][]int{}}
	for pid, st := range pids {
		t.byPID[pid] = Proc{PID: pid, StartTime: st}
	}
	return t
}

func TestWaitGone(t *testing.T) {
	calls := 0
	scan := func() (*Table, error) {
		calls++
		if calls < 3 {
			return tableWith(map[int]string{42: "7"}), nil
		}
		return tableWith(map[int]string{}), nil
	}
	var slept []time.Duration
	sleep := func(d time.Duration) { slept = append(slept, d) }
	if err := WaitGone(scan, 42, "7", time.Second, 100*time.Millisecond, sleep); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(slept) != 2 || slept[0] != 100*time.Millisecond {
		t.Fatalf("calls=%d slept=%v", calls, slept)
	}
}

func TestWaitGoneTimesOut(t *testing.T) {
	scan := func() (*Table, error) { return tableWith(map[int]string{42: "7"}), nil }
	var total time.Duration
	err := WaitGone(scan, 42, "7", 500*time.Millisecond, 100*time.Millisecond, func(d time.Duration) { total += d })
	if err == nil || !errors.Is(err, ErrTimeout) || total < 500*time.Millisecond {
		t.Fatalf("err=%v total=%v", err, total)
	}
}

func TestWaitGoneReusedPidIsGone(t *testing.T) {
	scan := func() (*Table, error) { return tableWith(map[int]string{42: "8"}), nil }
	if err := WaitGone(scan, 42, "7", time.Second, 100*time.Millisecond, func(time.Duration) {}); err != nil {
		t.Fatalf("a pid with a different start time is gone: %v", err)
	}
}

func TestWaitGoneScanError(t *testing.T) {
	scan := func() (*Table, error) { return nil, errors.New("boom") }
	if err := WaitGone(scan, 42, "7", time.Second, 100*time.Millisecond, func(time.Duration) {}); err == nil {
		t.Fatal("scan error must propagate")
	}
}
```

`internal/procx/spawn_test.go`:

```go
package procx

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sessionID reads field 6 (session id) of /proc/<pid>/stat.
func sessionID(t *testing.T, pid int) string {
	t.Helper()
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	rest := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	return rest[3] // state ppid pgrp session
}

func TestSpawnDetached(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log.txt")
	pid, err := SpawnDetached([]string{"sh", "-c", "echo hello; echo \"$MARK\"; cat /proc/self/stat"}, dir, log, []string{"PATH=" + os.Getenv("PATH"), "MARK=marker-42"})
	if err != nil {
		t.Fatal(err)
	}
	if pid <= 0 {
		t.Fatalf("pid = %d", pid)
	}
	var out string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(log)
		out = string(b)
		if strings.Contains(out, "marker-42") && strings.Contains(out, ")") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.HasPrefix(out, "hello\nmarker-42\n") {
		t.Fatalf("log = %q", out)
	}
	// the child's session id (field 6 of its stat) equals its own pid: setsid worked
	statLine := out[strings.Index(out, "marker-42\n")+len("marker-42\n"):]
	rest := strings.Fields(statLine[strings.LastIndexByte(statLine, ')')+1:])
	childPID := strings.Fields(statLine)[0]
	if rest[3] != childPID || rest[3] == sessionID(t, os.Getpid()) {
		t.Fatalf("child session=%s pid=%s ours=%s: not detached", rest[3], childPID, sessionID(t, os.Getpid()))
	}
	if fi, _ := os.Stat(log); fi.Mode().Perm() != 0o600 {
		t.Fatalf("log mode %v", fi.Mode())
	}
}

func TestSpawnDetachedMissingBinary(t *testing.T) {
	if _, err := SpawnDetached([]string{"/nonexistent/binary"}, t.TempDir(), filepath.Join(t.TempDir(), "l"), nil); err == nil {
		t.Fatal("expected error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/procx/ -run 'TestWaitGone|TestSpawn' -v`
Expected: FAIL — `undefined: WaitGone`, `undefined: SpawnDetached`.

- [ ] **Step 3: Implement**

`internal/procx/wait.go`:

```go
package procx

import (
	"errors"
	"fmt"
	"time"
)

// ErrTimeout is returned (wrapped) when a bounded wait expires.
var ErrTimeout = errors.New("timeout")

// WaitGone polls until pid (with startTime) is no longer alive. t scans the
// table (procx.Scan("/proc") in production); sleep is injectable for tests.
func WaitGone(t func() (*Table, error), pid int, startTime string, timeout, poll time.Duration, sleep func(time.Duration)) error {
	var waited time.Duration
	for {
		tb, err := t()
		if err != nil {
			return fmt.Errorf("wait for pid %d to exit: %w", pid, err)
		}
		if !tb.Alive(pid, startTime) {
			return nil
		}
		if waited >= timeout {
			return fmt.Errorf("pid %d still alive after %s: %w", pid, timeout, ErrTimeout)
		}
		sleep(poll)
		waited += poll
	}
}
```

`internal/procx/spawn.go`:

```go
package procx

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// SpawnDetached starts argv in its own session (setsid) with stdin from
// /dev/null and stdout+stderr appended to logPath (created 0600); returns
// the child pid. The child is released: it is never waited for, so it
// outlives the caller (spec §6: the runner is never a child of Claude).
func SpawnDetached(argv []string, dir, logPath string, env []string) (int, error) {
	if len(argv) == 0 {
		return 0, fmt.Errorf("spawn: empty argv")
	}
	logf, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return 0, fmt.Errorf("spawn: open log %s: %w", logPath, err)
	}
	defer logf.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return 0, fmt.Errorf("spawn: %w", err)
	}
	defer devnull.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = devnull
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("spawn %q: %w", argv[0], err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return pid, fmt.Errorf("spawn: release pid %d: %w", pid, err)
	}
	return pid, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race ./internal/procx/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/procx
git commit -m "feat(procx): WaitGone with injectable sleep, SpawnDetached (setsid)"
```

---

### Task 17: `internal/placeholder`

**Files:**
- Create: `internal/placeholder/placeholder.go`
- Test: `internal/placeholder/placeholder_test.go`

**Interfaces:**
- Consumes: `session.Meta`, `session.ReadMeta`, `session.FindTranscript`, `session.Munge`, `session.IsUUID`.
- Produces: `Options{SessionID, SavedOutput, Now, TeleportedTo, TeleportedAt, ProjectsDir, Home}`, `Decision{Argv, Chdir, Skip}`, `Decide(w io.Writer, o Options, stdoutTTY, stdinTTY bool, readLine func() (string, error)) Decision`, `Render(w io.Writer, o Options, meta *session.Meta, tty bool)`, `ChdirTarget(m session.Meta, transcript string) string`.

- [ ] **Step 1: Write the failing test**

`internal/placeholder/placeholder_test.go`:

```go
package placeholder

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

const sid = "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"

// fixture builds a projects dir whose transcript's launch cwd is a real
// temp directory, so ChdirTarget has something to stat.
func fixture(t *testing.T) (projectsDir, launchCwd string) {
	t.Helper()
	root := t.TempDir()
	launchCwd = filepath.Join(root, "work")
	os.MkdirAll(launchCwd, 0o700)
	projectsDir = filepath.Join(root, "projects")
	proj := filepath.Join(projectsDir, session.Munge(launchCwd))
	os.MkdirAll(proj, 0o700)
	rec := `{"type":"user","cwd":"` + launchCwd + `","gitBranch":"feature/teleport","sessionId":"` + sid + `","timestamp":"2026-08-27T10:15:30.123Z","message":{"content":"Add a --verbose flag"}}` + "\n" +
		`{"type":"custom-title","customTitle":"widget-verbose"}` + "\n"
	os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(rec), 0o600)
	return projectsDir, launchCwd
}

func TestDecideResumesOnEnter(t *testing.T) {
	projects, cwd := fixture(t)
	var out bytes.Buffer
	d := Decide(&out, Options{SessionID: sid, ProjectsDir: projects, Home: filepath.Dir(cwd)}, false, true,
		func() (string, error) { return "\n", nil })
	if d.Skip || d.Chdir != cwd || strings.Join(d.Argv, " ") != "claude --resume "+sid {
		t.Fatalf("%+v", d)
	}
	s := out.String()
	for _, w := range []string{"Resume Claude session", "3f9c2b7e", "~/work", "feature/teleport", "widget-verbose", "last active 2026-08-27T10:15:30.123Z", "Enter = resume", "resuming"} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in:\n%s", w, s)
		}
	}
	if strings.Contains(s, "\033[") {
		t.Error("no ANSI when stdout is not a tty")
	}
}

func TestDecideSkipsOnInterrupt(t *testing.T) {
	projects, _ := fixture(t)
	var out bytes.Buffer
	d := Decide(&out, Options{SessionID: sid, ProjectsDir: projects}, true, true,
		func() (string, error) { return "", errors.New("interrupted") })
	if !d.Skip || !strings.Contains(out.String(), "skipped") || !strings.Contains(out.String(), "\033[") {
		t.Fatalf("%+v %q", d, out.String())
	}
}

func TestDecideNowAndNonTTYDoNotWait(t *testing.T) {
	projects, _ := fixture(t)
	called := false
	rl := func() (string, error) { called = true; return "", nil }
	if d := Decide(&bytes.Buffer{}, Options{SessionID: sid, ProjectsDir: projects, Now: true}, true, true, rl); d.Skip || called {
		t.Fatalf("--now must not wait: %+v called=%v", d, called)
	}
	if d := Decide(&bytes.Buffer{}, Options{SessionID: sid, ProjectsDir: projects}, false, false, rl); d.Skip || called {
		t.Fatalf("non-tty stdin must not wait: %+v called=%v", d, called)
	}
}

func TestDecidePrintsSavedOutputAndTeleportLine(t *testing.T) {
	projects, _ := fixture(t)
	saved := filepath.Join(t.TempDir(), "capture.txt")
	os.WriteFile(saved, []byte("old pane content\n"), 0o600)
	var out bytes.Buffer
	Decide(&out, Options{SessionID: sid, ProjectsDir: projects, SavedOutput: saved, Now: true,
		TeleportedTo: "big-storage.example", TeleportedAt: "2026-08-27T12:00:00Z"}, false, true, nil)
	s := out.String()
	if !strings.HasPrefix(s, "old pane content\n") {
		t.Fatalf("saved output must come first:\n%s", s)
	}
	if !strings.Contains(s, "teleported to big-storage.example at 2026-08-27T12:00:00Z") || !strings.Contains(s, "forks") {
		t.Fatalf("teleport line missing:\n%s", s)
	}
}

func TestDecideUnknownSessionStillResumes(t *testing.T) {
	var out bytes.Buffer
	d := Decide(&out, Options{SessionID: "00000000-0000-4000-8000-000000000000", ProjectsDir: t.TempDir(), Now: true}, false, true, nil)
	if d.Skip || d.Chdir != "" || len(d.Argv) != 3 || !strings.Contains(out.String(), "transcript not found") {
		t.Fatalf("%+v %q", d, out.String())
	}
	d = Decide(&out, Options{SessionID: "junk", ProjectsDir: t.TempDir(), Now: true}, false, true, nil)
	if len(d.Argv) != 1 || d.Argv[0] != "claude" {
		t.Fatalf("junk id must open the picker: %+v", d)
	}
}

func TestChdirTarget(t *testing.T) {
	projects, cwd := fixture(t)
	transcript := filepath.Join(projects, session.Munge(cwd), sid+".jsonl")
	if got := ChdirTarget(session.Meta{LaunchCwd: cwd}, transcript); got != cwd {
		t.Fatalf("got %q", got)
	}
	if got := ChdirTarget(session.Meta{LaunchCwd: cwd + "/missing"}, transcript); got != "" {
		t.Fatalf("missing dir: %q", got)
	}
	if got := ChdirTarget(session.Meta{LaunchCwd: filepath.Dir(cwd)}, transcript); got != "" {
		t.Fatalf("munge mismatch: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/placeholder/ -v`
Expected: FAIL — `undefined: Decide`.

- [ ] **Step 3: Implement `internal/placeholder/placeholder.go`** (port of go-tmux-saver's `resume` package)

```go
// Package placeholder implements `claude-teleport placeholder`: the command
// typed into a pane instead of relaunching Claude blindly. It shows WHICH
// conversation the pane held and waits for Enter (spec §11). Ported from
// go-tmux-saver's internal/resume (Apache-2.0, same author).
package placeholder

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// Options configures one placeholder run.
type Options struct {
	SessionID    string
	SavedOutput  string // file to print above the banner ("" = none)
	Now          bool   // skip the confirm wait
	TeleportedTo string
	TeleportedAt string // ISO 8601
	ProjectsDir  string
	Home         string
}

// Decision is what the placeholder resolved to: exec Argv (from Chdir when
// non-empty), or Skip back to the pane's shell.
type Decision struct {
	Argv  []string // claude --resume <sid>   (or claude)
	Chdir string
	Skip  bool
}

func shortenHome(home, p string) string {
	if home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// ChdirTarget returns the directory to resume from, or "". `claude --resume`
// is project-scoped: it resolves the id against the project matching the
// CURRENT directory's munged name, so the pane must cd back to the launch
// cwd (when it still exists and really is the transcript's project).
func ChdirTarget(m session.Meta, transcript string) string {
	cwd := m.LaunchCwd
	if cwd == "" || transcript == "" {
		return ""
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		return ""
	}
	if session.Munge(cwd) != filepath.Base(filepath.Dir(transcript)) {
		return ""
	}
	return cwd
}

// Render writes the banner. tty enables ANSI styling.
func Render(w io.Writer, o Options, meta *session.Meta, tty bool) {
	b, d, c, y, r := "", "", "", "", ""
	if tty {
		b, d, c, y, r = "\033[1m", "\033[2m", "\033[36m", "\033[33m", "\033[0m"
	}
	sid := o.SessionID
	if sid == "" {
		fmt.Fprintf(w, "\n%sResume Claude%s %s(no session id — picker)%s\n", b, r, d, r)
		fmt.Fprintf(w, "%s  Enter = open the resume picker · Ctrl-C = shell%s\n\n", d, r)
		return
	}
	fmt.Fprintf(w, "\n%sResume Claude session%s  %s%s%s%s…%s\n", b, r, c, sid[:8], r, d, r)
	if meta != nil {
		loc := shortenHome(o.Home, meta.LaunchCwd)
		if meta.Branch != "" {
			if loc != "" {
				loc = fmt.Sprintf("%s  %s@ %s%s", loc, d, meta.Branch, r)
			} else {
				loc = meta.Branch
			}
		}
		if loc != "" {
			fmt.Fprintf(w, "  %s\n", loc)
		}
		if meta.WorkCwd != "" && meta.WorkCwd != meta.LaunchCwd {
			fmt.Fprintf(w, "  %s↳ worktree %s%s\n", d, shortenHome(o.Home, meta.WorkCwd), r)
		}
		fmt.Fprintf(w, "  %s“%s%s%s”%s\n", d, r, meta.Label(), d, r)
		if meta.LastTS != "" {
			fmt.Fprintf(w, "  %slast active %s%s\n", d, meta.LastTS, r)
		}
	} else {
		fmt.Fprintf(w, "  %s(transcript not found — will still try to resume)%s\n", d, r)
	}
	if o.TeleportedTo != "" {
		at := o.TeleportedAt
		if at == "" {
			at = "an unknown time"
		}
		fmt.Fprintf(w, "  %s⚠ teleported to %s at %s — resuming here forks the session%s\n", y, o.TeleportedTo, at, r)
	}
	if o.Now {
		fmt.Fprintf(w, "%s  resuming now%s\n\n", d, r)
	} else {
		fmt.Fprintf(w, "%s  Enter = resume · Ctrl-C = shell%s\n\n", d, r)
	}
}

// Decide runs the whole placeholder flow against injected I/O: print the
// saved output, render the banner, wait for Enter (unless Now, or stdin is
// not a tty — a send-keys restore has no human to wait on), announce the
// choice, and return what to exec. readLine returning an error (Ctrl-C /
// Ctrl-D) means skip. A visible line always records the choice.
func Decide(w io.Writer, o Options, stdoutTTY, stdinTTY bool, readLine func() (string, error)) Decision {
	if o.SavedOutput != "" {
		if data, err := os.ReadFile(o.SavedOutput); err == nil {
			w.Write(data)
		} else {
			fmt.Fprintf(w, "(saved output %s not readable: %v)\n", o.SavedOutput, err)
		}
	}
	sid := strings.ToLower(strings.TrimSpace(o.SessionID))
	var meta *session.Meta
	transcript := ""
	argv := []string{"claude"}
	if session.IsUUID(sid) {
		if tp, err := session.FindTranscript(o.ProjectsDir, session.ID(sid)); err == nil {
			transcript = tp
			if m, err := session.ReadMeta(tp); err == nil {
				meta = &m
			}
		}
		argv = []string{"claude", "--resume", sid}
	} else {
		sid = "" // junk that isn't a uuid → plain claude (resume picker)
	}
	o.SessionID = sid
	Render(w, o, meta, stdoutTTY)

	d, grn, r := "", "", ""
	if stdoutTTY {
		d, grn, r = "\033[2m", "\033[32m", "\033[0m"
	}
	if stdinTTY && !o.Now {
		if _, err := readLine(); err != nil {
			fmt.Fprintf(w, "%s↩ skipped — shell ready%s\n", d, r)
			return Decision{Skip: true}
		}
	}
	if sid != "" {
		fmt.Fprintf(w, "%s↳ resuming%s %s%s…%s\n", grn, r, d, sid[:8], r)
	} else {
		fmt.Fprintf(w, "%s↳ opening resume picker%s%s…%s\n", grn, r, d, r)
	}
	chdir := ""
	if meta != nil {
		chdir = ChdirTarget(*meta, transcript)
	}
	return Decision{Argv: argv, Chdir: chdir}
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race ./internal/placeholder/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/placeholder
git commit -m "feat(placeholder): confirm-before-resume banner (port of go-tmux-saver resume)"
```

---

### Task 18: `placeholder` command (chdir + exec) and `--config-dir` path resolution

**Files:**
- Create: `internal/cli/placeholder.go`, `internal/cli/tty.go`
- Modify: `internal/cli/cli.go` (add `configDir` to `app`, the persistent `--config-dir` flag, `resolvePaths`; register the command)
- Test: `internal/cli/placeholder_test.go`, `internal/cli/paths_test.go`

**Interfaces:**
- Consumes: `placeholder.Decide`, `placeholder.Options`, `session.NewPaths`.
- Produces: `(*app).resolvePaths() (session.Paths, error)` (HOME required; `--config-dir` overrides `$CLAUDE_CONFIG_DIR`), package vars `execveFn`, `lookPathFn`, `chdirFn`, `stdinTTYFn`, `stdoutTTYFn` (swappable in tests), `isTTY(*os.File) bool`, `readLineInterruptible(io.Reader) (string, error)`.

- [ ] **Step 1: Write the failing tests**

`internal/cli/paths_test.go`:

```go
package cli

import "testing"

func TestResolvePaths(t *testing.T) {
	a := &app{env: parseEnv([]string{"HOME=/home/alice"})}
	p, err := a.resolvePaths()
	if err != nil || p.ConfigDir != "/home/alice/.claude" || p.GlobalJSON != "/home/alice/.claude.json" {
		t.Fatalf("%+v %v", p, err)
	}
	a = &app{env: parseEnv([]string{"HOME=/home/alice", "CLAUDE_CONFIG_DIR=/srv/cfg", "XDG_DATA_HOME=/srv/data"})}
	p, _ = a.resolvePaths()
	if p.ConfigDir != "/srv/cfg" || p.GlobalJSON != "/srv/cfg/.claude.json" || p.DataDir != "/srv/data/claude-teleport" {
		t.Fatalf("%+v", p)
	}
	a.configDir = "/flag/cfg"
	if p, _ = a.resolvePaths(); p.ConfigDir != "/flag/cfg" || p.GlobalJSON != "/flag/cfg/.claude.json" {
		t.Fatalf("--config-dir must win: %+v", p)
	}
	if _, err := (&app{env: map[string]string{}}).resolvePaths(); err == nil {
		t.Fatal("missing HOME must be an error")
	}
}
```

`internal/cli/placeholder_test.go`:

```go
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

const phSID = "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"

func placeholderFixture(t *testing.T) (cfg, cwd string) {
	t.Helper()
	root := t.TempDir()
	cwd = filepath.Join(root, "work")
	os.MkdirAll(cwd, 0o700)
	cfg = filepath.Join(root, "cfg")
	proj := filepath.Join(cfg, "projects", session.Munge(cwd))
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, phSID+".jsonl"),
		[]byte(`{"type":"user","cwd":"`+cwd+`","sessionId":"`+phSID+`","message":{"content":"hi"}}`+"\n"), 0o600)
	return cfg, cwd
}

func stubExec(t *testing.T) (execs *[][]string, chdirs *[]string) {
	t.Helper()
	oldExec, oldLook, oldChdir, oldIn, oldOut := execveFn, lookPathFn, chdirFn, stdinTTYFn, stdoutTTYFn
	t.Cleanup(func() { execveFn, lookPathFn, chdirFn, stdinTTYFn, stdoutTTYFn = oldExec, oldLook, oldChdir, oldIn, oldOut })
	var e [][]string
	var c []string
	execveFn = func(path string, argv []string, env []string) error { e = append(e, append([]string{path}, argv...)); return nil }
	lookPathFn = func(file string) (string, error) { return "/usr/local/bin/" + file, nil }
	chdirFn = func(dir string) error { c = append(c, dir); return nil }
	stdinTTYFn = func() bool { return false }
	stdoutTTYFn = func() bool { return false }
	return &e, &c
}

func TestPlaceholderExecsClaude(t *testing.T) {
	cfg, cwd := placeholderFixture(t)
	execs, chdirs := stubExec(t)
	code, out, stderr := run(t, []string{"HOME=" + filepath.Dir(cwd), "CLAUDE_CONFIG_DIR=" + cfg},
		"placeholder", "--resume", phSID, "--teleported-to", "big-storage.example")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if len(*execs) != 1 || strings.Join((*execs)[0], " ") != "/usr/local/bin/claude claude --resume "+phSID {
		t.Fatalf("execs = %q", *execs)
	}
	if len(*chdirs) != 1 || (*chdirs)[0] != cwd {
		t.Fatalf("chdirs = %q", *chdirs)
	}
	if !strings.Contains(out, "teleported to big-storage.example") {
		t.Fatalf("banner:\n%s", out)
	}
}

func TestPlaceholderSavedOutputAndMissingClaude(t *testing.T) {
	cfg, cwd := placeholderFixture(t)
	execs, _ := stubExec(t)
	lookPathFn = func(string) (string, error) { return "", os.ErrNotExist }
	saved := filepath.Join(t.TempDir(), "cap.txt")
	os.WriteFile(saved, []byte("captured pane\n"), 0o600)
	code, out, stderr := run(t, []string{"HOME=" + filepath.Dir(cwd), "CLAUDE_CONFIG_DIR=" + cfg},
		"placeholder", "--resume", phSID, "--saved-output", saved, "--now")
	if code != ExitFailed || !strings.Contains(stderr, "`claude` not found") || len(*execs) != 0 {
		t.Fatalf("exit %d stderr %q execs %q", code, stderr, *execs)
	}
	if !strings.HasPrefix(out, "captured pane\n") {
		t.Fatalf("saved output first:\n%s", out)
	}
}

func TestPlaceholderRequiresResume(t *testing.T) {
	if code, _, _ := run(t, []string{"HOME=/home/alice"}, "placeholder"); code != ExitUsage {
		t.Fatalf("exit %d", code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestResolvePaths|TestPlaceholder' -v`
Expected: FAIL — `a.resolvePaths undefined`, `undefined: execveFn`.

- [ ] **Step 3: Implement**

Add to `internal/cli/cli.go`:

```go
// (add to imports) "github.com/mithro/go-claude-teleport/internal/session"

// (add to type app)
//	configDir string // --config-dir (persistent flag)

// resolvePaths computes the local session.Paths from HOME, CLAUDE_CONFIG_DIR
// (overridden by --config-dir) and XDG_DATA_HOME.
func (a *app) resolvePaths() (session.Paths, error) {
	home := a.env["HOME"]
	if home == "" {
		return session.Paths{}, Exit(ExitUsage, "HOME is not set")
	}
	cfg := a.env["CLAUDE_CONFIG_DIR"]
	if a.configDir != "" {
		cfg = a.configDir
	}
	return session.NewPaths(home, cfg, a.env["XDG_DATA_HOME"]), nil
}
```

and in `rootCmd`, before `root.AddCommand(...)`:

```go
	root.PersistentFlags().StringVar(&a.configDir, "config-dir", "", "local CLAUDE_CONFIG_DIR override")
	root.AddCommand(a.placeholderCmd())
```

`internal/cli/tty.go`:

```go
package cli

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"unsafe"
)

// Swappable in tests: CI has no `claude` on PATH, and a test must never
// really exec or chdir. On success execveFn never returns — the placeholder
// becomes the claude process.
var (
	execveFn    = syscall.Exec
	lookPathFn  = exec.LookPath
	chdirFn     = os.Chdir
	stdinTTYFn  = func() bool { return isTTY(os.Stdin) }
	stdoutTTYFn = func() bool { return isTTY(os.Stdout) }
)

// isTTY reports whether f is a terminal, decided by the TCGETS ioctl like
// isatty(3) — a ModeCharDevice check would misclassify /dev/null.
func isTTY(f *os.File) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}

// readLineInterruptible reads one line from r, treating SIGINT (Ctrl-C at
// the pane prompt) as an error, which Decide turns into "skip — leave a
// shell in this pane".
func readLineInterruptible(r io.Reader) (string, error) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	type lineResult struct {
		s   string
		err error
	}
	ch := make(chan lineResult, 1)
	go func() {
		s, err := bufio.NewReader(r).ReadString('\n')
		ch <- lineResult{s, err}
	}()
	select {
	case <-sig:
		return "", errors.New("interrupted")
	case res := <-ch:
		return res.s, res.err
	}
}
```

`internal/cli/placeholder.go`:

```go
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/placeholder"
)

func (a *app) placeholderCmd() *cobra.Command {
	var o placeholder.Options
	cmd := &cobra.Command{
		Use:   "placeholder --resume <sid> [--saved-output F] [--now] [--teleported-to HOST] [--teleported-at TS]",
		Short: "confirm, then resume a specific Claude session (typed into a pane by a teleport)",
		Long: `Shows which conversation the pane held (project, branch, title, last
active) and waits for Enter before exec'ing "claude --resume <sid>" from the
session's launch directory. Ctrl-C leaves a shell in the pane. When stdin is
not a terminal the resume happens immediately.

--saved-output prints a pane capture above the banner so the pane looks as
it did before; --now skips the wait; --teleported-to/--teleported-at say
where the session went and warn that resuming here forks it.

The argv contains "--resume <uuid>", so go-tmux-saver's process resolver
classifies the pane as a Claude pane and saves/restores it as such.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.SessionID == "" {
				return Exit(ExitUsage, "placeholder: --resume <sid> is required")
			}
			p, err := a.resolvePaths()
			if err != nil {
				return err
			}
			o.ProjectsDir, o.Home = p.ProjectsDir(), p.Home
			d := placeholder.Decide(a.stdout, o, stdoutTTYFn(), stdinTTYFn(),
				func() (string, error) { return readLineInterruptible(a.stdin) })
			if d.Skip {
				return nil
			}
			if d.Chdir != "" {
				if err := chdirFn(d.Chdir); err != nil {
					fmt.Fprintln(a.stderr, "placeholder: chdir:", err)
				}
			}
			path, err := lookPathFn(d.Argv[0])
			if err != nil {
				return Exit(ExitFailed, "placeholder: `claude` not found on PATH")
			}
			if err := execveFn(path, d.Argv, os.Environ()); err != nil {
				return Exit(ExitFailed, "placeholder: exec %s: %v", path, err)
			}
			return nil // unreachable in production: exec replaced the process
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.SessionID, "resume", "", "session id to resume (required)")
	f.StringVar(&o.SavedOutput, "saved-output", "", "print this file's content above the banner")
	f.BoolVar(&o.Now, "now", false, "do not wait for Enter")
	f.StringVar(&o.TeleportedTo, "teleported-to", "", "host the session was teleported to")
	f.StringVar(&o.TeleportedAt, "teleported-at", "", "when it was teleported (ISO 8601)")
	return cmd
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race ./internal/cli/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "feat(cli): placeholder command with injectable exec, --config-dir"
```

---

### Task 19: `test/fakeclaude` — the fake `claude` binary and its harness

**Files:**
- Create: `test/fakeclaude/main.go`, `test/fakeclaude/harness/harness.go`
- Test: `test/fakeclaude/fakeclaude_test.go`

**Interfaces:**
- Consumes: `session.Munge`, `session.IsUUID`, `procx.StartTime`, `session.WriteFileAtomic`.
- Produces: the `claude` program described below; `harness.Build(t testing.TB) string` — builds it once per test process and returns a directory containing `claude` (prepend it to `PATH`); `harness.Env(t, home, configDir string, extra ...string) []string` — a clean environment for running it.

Behaviour (spec §12), controlled by environment:

| Input | Behaviour |
|---|---|
| `--version` | prints `2.1.247 (Claude Code)` (version from `FAKECLAUDE_VERSION` if set), exit 0 |
| `FAKECLAUDE_FAIL=not-logged-in` | prints `Not logged in · Please run /login` to stdout, exit 1, touches nothing |
| `--resume <id>` / `-r <id>` | resumes: transcript must exist under `projects/<munged cwd>/`, else prints `No conversation found with session ID: <id>` and exits 1 |
| `--session-id <id>` | creates a session with that id |
| neither | creates a session with a fresh v4 uuid |
| interactive (no `-p`) | writes the registry (`status: busy`), optionally runs `FAKECLAUDE_RUN_CHILD`, sets `idle`, then reads stdin lines: `/exit` → removes the registry file and exits 0; any other line → `busy`, appends a `user` record then an `assistant` record (text from `FAKECLAUDE_REPLY`, default `ok: <line>`), `idle` |
| `-p <prompt>` | same as one interactive exchange, then removes the registry and exits 0 |
| SIGTERM / SIGINT | removes the registry file, exit 0 |
| `FAKECLAUDE_RUN_CHILD=<cmd>` | after start-up runs `sh -c <cmd>` with `CLAUDE_PID=<own pid>`, `CLAUDE_CODE_SESSION_ID=<id>`, `CLAUDECODE=1` added; `status` is `busy` while it runs |
| `TMUX` + `TMUX_PANE` set | registry `tmux` = output of `tmux display-message -p -t $TMUX_PANE '#{session_name}:#{window_id}.#{pane_id}'`, else `""` |
| `FAKECLAUDE_TMUX=<session>:@<win>.%<pane>` | overrides the registry `tmux` field verbatim (Plan 03's fake tmux server has no `tmux` binary to query) |
| `FAKECLAUDE_BRANCH` | `gitBranch` in records (default `main`) |

Files written under `$CLAUDE_CONFIG_DIR` (or `$HOME/.claude`): `projects/<munged cwd>/<id>.jsonl`, `sessions/<pid>.json`, `history.jsonl`.

- [ ] **Step 1: Write the harness**

`test/fakeclaude/harness/harness.go`:

```go
// Package harness builds test/fakeclaude and puts it on PATH as `claude`.
package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var (
	once   sync.Once
	binDir string
	bErr   error
)

// Build compiles the fake claude once per test process and returns a
// directory containing the `claude` binary.
func Build(t testing.TB) string {
	t.Helper()
	once.Do(func() {
		_, self, _, _ := runtime.Caller(0)
		pkgDir := filepath.Dir(filepath.Dir(self)) // test/fakeclaude
		dir, err := os.MkdirTemp("", "fakeclaude-")
		if err != nil {
			bErr = err
			return
		}
		cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "claude"), ".")
		cmd.Dir = pkgDir
		if out, err := cmd.CombinedOutput(); err != nil {
			bErr = err
			t.Logf("go build fakeclaude: %s", out)
			return
		}
		binDir = dir
	})
	if bErr != nil {
		t.Fatalf("build fakeclaude: %v", bErr)
	}
	return binDir
}

// Env returns a minimal environment with the fake claude first on PATH.
func Env(t testing.TB, home, configDir string, extra ...string) []string {
	t.Helper()
	env := []string{
		"PATH=" + Build(t) + ":" + os.Getenv("PATH"),
		"HOME=" + home,
		"CLAUDE_CONFIG_DIR=" + configDir,
		"TERM=dumb",
	}
	for _, e := range extra {
		if !strings.Contains(e, "=") {
			t.Fatalf("bad env entry %q", e)
		}
		env = append(env, e)
	}
	return env
}
```

- [ ] **Step 2: Write the failing tests**

`test/fakeclaude/fakeclaude_test.go`:

```go
package main_test

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/test/fakeclaude/harness"
)

const sid = "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"

type env struct{ home, cfg, cwd string }

func setup(t *testing.T) env {
	t.Helper()
	root := t.TempDir()
	e := env{home: filepath.Join(root, "home"), cfg: filepath.Join(root, "home", ".claude"), cwd: filepath.Join(root, "home", "proj")}
	os.MkdirAll(e.cwd, 0o700)
	return e
}

func (e env) cmd(t *testing.T, extra []string, args ...string) *exec.Cmd {
	t.Helper()
	c := exec.Command("claude", args...)
	c.Dir = e.cwd
	c.Env = harness.Env(t, e.home, e.cfg, extra...)
	c.Path = filepath.Join(harness.Build(t), "claude")
	return c
}

func (e env) transcript(id string) string {
	return filepath.Join(e.cfg, "projects", session.Munge(e.cwd), id+".jsonl")
}

func lines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func registry(e env, pid int) (session.Registry, bool) {
	r, err := session.ReadRegistryFile(filepath.Join(e.cfg, "sessions", strconv.Itoa(pid)+".json"))
	return r, err == nil
}

func TestVersion(t *testing.T) {
	e := setup(t)
	out, err := e.cmd(t, nil, "--version").Output()
	if err != nil || strings.TrimSpace(string(out)) != "2.1.247 (Claude Code)" {
		t.Fatalf("%q %v", out, err)
	}
	out, _ = e.cmd(t, []string{"FAKECLAUDE_VERSION=2.1.250"}, "--version").Output()
	if strings.TrimSpace(string(out)) != "2.1.250 (Claude Code)" {
		t.Fatalf("%q", out)
	}
}

func TestPrintMode(t *testing.T) {
	e := setup(t)
	out, err := e.cmd(t, []string{"FAKECLAUDE_REPLY=hello back"}, "-p", "hello", "--session-id", sid).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	recs := lines(t, e.transcript(sid))
	if len(recs) != 2 || recs[0]["type"] != "user" || recs[1]["type"] != "assistant" {
		t.Fatalf("%+v", recs)
	}
	u := recs[0]
	if u["cwd"] != e.cwd || u["sessionId"] != sid || u["version"] != "2.1.247" || u["gitBranch"] != "main" || u["timestamp"] == nil || u["uuid"] == nil {
		t.Fatalf("user record %+v", u)
	}
	if u["message"].(map[string]any)["content"] != "hello" {
		t.Fatalf("prompt not recorded: %+v", u)
	}
	content := recs[1]["message"].(map[string]any)["content"].([]any)[0].(map[string]any)
	if content["text"] != "hello back" || recs[1]["parentUuid"] != u["uuid"] {
		t.Fatalf("assistant record %+v", recs[1])
	}
	if entries, _ := os.ReadDir(filepath.Join(e.cfg, "sessions")); len(entries) != 0 {
		t.Fatalf("registry must be removed after -p: %v", entries)
	}
	hist := lines(t, filepath.Join(e.cfg, "history.jsonl"))
	if len(hist) != 1 || hist[0]["display"] != "hello" || hist[0]["sessionId"] != sid || hist[0]["project"] != e.cwd {
		t.Fatalf("history %+v", hist)
	}
	// resume appends to the same file
	if out, err := e.cmd(t, nil, "-p", "again", "--resume", sid).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got := lines(t, e.transcript(sid)); len(got) != 4 || got[2]["message"].(map[string]any)["content"] != "again" {
		t.Fatalf("%+v", got)
	}
	if out, err := e.cmd(t, nil, "-p", "x", "--resume", "00000000-0000-4000-8000-000000000000").CombinedOutput(); err == nil || !strings.Contains(string(out), "No conversation found") {
		t.Fatalf("unknown resume: %v %s", err, out)
	}
}

func TestNotLoggedIn(t *testing.T) {
	e := setup(t)
	out, err := e.cmd(t, []string{"FAKECLAUDE_FAIL=not-logged-in"}, "-p", "hi").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "Not logged in") {
		t.Fatalf("%v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(e.cfg, "sessions")); err == nil {
		t.Fatal("registry dir must not be created")
	}
}

func TestInteractive(t *testing.T) {
	e := setup(t)
	c := e.cmd(t, nil, "--session-id", sid)
	stdin, _ := c.StdinPipe()
	c.Stdout, c.Stderr = io.Discard, os.Stderr
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { c.Process.Kill(); c.Wait() }()
	pid := c.Process.Pid
	var r session.Registry
	waitFor(t, "registry idle", func() bool {
		var ok bool
		r, ok = registry(e, pid)
		return ok && r.Status == "idle"
	})
	if r.PID != pid || r.SessionID != sid || r.Cwd != e.cwd || r.ProcStart == "" || r.Version != "2.1.247" || r.Name != "proj" || r.Tmux != "" {
		t.Fatalf("%+v", r)
	}
	if st, _ := session.ProcStartTime("/proc", pid); st != r.ProcStart {
		t.Fatalf("procStart %s != real %s", r.ProcStart, st)
	}
	io.WriteString(stdin, "first turn\n")
	waitFor(t, "two records", func() bool { b, _ := os.ReadFile(e.transcript(sid)); return strings.Count(string(b), "\n") == 2 })
	io.WriteString(stdin, "/exit\n")
	if err := c.Wait(); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if _, ok := registry(e, pid); ok {
		t.Fatal("registry must be removed on /exit")
	}
}

func TestSigtermCleansUp(t *testing.T) {
	e := setup(t)
	c := e.cmd(t, nil)
	stdin, _ := c.StdinPipe()
	defer stdin.Close()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	pid := c.Process.Pid
	waitFor(t, "registry", func() bool { _, ok := registry(e, pid); return ok })
	c.Process.Signal(syscall.SIGTERM)
	if err := c.Wait(); err != nil {
		t.Fatalf("SIGTERM must exit 0: %v", err)
	}
	if _, ok := registry(e, pid); ok {
		t.Fatal("registry must be removed on SIGTERM")
	}
	hits, _ := filepath.Glob(filepath.Join(e.cfg, "projects", "*", "*.jsonl"))
	if len(hits) != 1 {
		t.Fatalf("a fresh uuid session must have been created: %v", hits)
	}
}

func TestRunChild(t *testing.T) {
	e := setup(t)
	out := filepath.Join(t.TempDir(), "child.txt")
	c := e.cmd(t, []string{"FAKECLAUDE_RUN_CHILD=sleep 1; echo \"$CLAUDE_PID $CLAUDE_CODE_SESSION_ID $CLAUDECODE\" > " + out}, "--session-id", sid)
	stdin, _ := c.StdinPipe()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { c.Process.Kill(); c.Wait() }()
	pid := c.Process.Pid
	waitFor(t, "busy while child runs", func() bool { r, ok := registry(e, pid); return ok && r.Status == "busy" })
	waitFor(t, "idle after child", func() bool { r, ok := registry(e, pid); return ok && r.Status == "idle" })
	got, _ := os.ReadFile(out)
	if strings.TrimSpace(string(got)) != strconv.Itoa(pid)+" "+sid+" 1" {
		t.Fatalf("child env: %q", got)
	}
	io.WriteString(stdin, "/exit\n")
	c.Wait()
}

// The tmux field is exercised only when a tmux server is reachable.
func TestTmuxField(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	e := setup(t)
	sock := filepath.Join(t.TempDir(), "sock")
	tm := func(args ...string) string {
		out, err := exec.Command("tmux", append([]string{"-S", sock}, args...)...).Output()
		if err != nil {
			t.Fatalf("tmux %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	tm("new-session", "-d", "-s", "ft", "-x", "80", "-y", "24")
	defer exec.Command("tmux", "-S", sock, "kill-server").Run()
	pane := tm("display-message", "-p", "-t", "ft:0", "#{pane_id}")
	c := e.cmd(t, []string{"TMUX=" + sock + ",1,0", "TMUX_PANE=" + pane}, "--session-id", sid)
	stdin, _ := c.StdinPipe()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { c.Process.Kill(); c.Wait() }()
	var r session.Registry
	waitFor(t, "registry", func() bool { var ok bool; r, ok = registry(e, c.Process.Pid); return ok })
	if sess, win, p, ok := r.TmuxParts(); !ok || sess != "ft" || !strings.HasPrefix(win, "@") || p != pane {
		t.Fatalf("tmux field %q", r.Tmux)
	}
	io.WriteString(stdin, "/exit\n")
	c.Wait()
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./test/fakeclaude/ -v`
Expected: FAIL — `build fakeclaude: … no Go files` (or a compile error).

- [ ] **Step 4: Write `test/fakeclaude/main.go`**

```go
// Command fakeclaude reproduces the observable on-disk behaviour of Claude
// Code 2.1.247 (spec §12): registry file, transcript records, history,
// --resume/--session-id/-p/--version, /exit, signals, and a "! bash" child.
// It is built by tests and put on PATH as `claude`. It never talks to any
// network.
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/session"
)

const defaultVersion = "2.1.247"

type fake struct {
	version, cfg, cwd, branch, sid, transcript, registry string
	pid                                                 int
	procStart, tmux                                     string
	startedAt                                           int64
	lastUUID                                            string
}

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func uuid4() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func now() string    { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }
func nowMS() int64   { return time.Now().UnixMilli() }
func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	f := &fake{version: getenv("FAKECLAUDE_VERSION", defaultVersion), branch: getenv("FAKECLAUDE_BRANCH", "main"), pid: os.Getpid()}
	var resume, sessionID, prompt string
	printMode := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--version", "-v":
			fmt.Fprintf(stdout, "%s (Claude Code)\n", f.version)
			return 0
		case "--resume", "-r":
			if i+1 < len(args) {
				resume = args[i+1]
				i++
			}
		case "--session-id":
			if i+1 < len(args) {
				sessionID = args[i+1]
				i++
			}
		case "-p", "--print":
			printMode = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				prompt = args[i+1]
				i++
			}
		default:
			if printMode && prompt == "" && !strings.HasPrefix(args[i], "-") {
				prompt = args[i]
			}
		}
	}
	if os.Getenv("FAKECLAUDE_FAIL") == "not-logged-in" {
		fmt.Fprintln(stdout, "Not logged in · Please run /login")
		return 1
	}
	home := os.Getenv("HOME")
	f.cfg = getenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "fakeclaude:", err)
		return 1
	}
	f.cwd = cwd
	proj := filepath.Join(f.cfg, "projects", session.Munge(cwd))
	switch {
	case resume != "":
		f.sid = strings.ToLower(resume)
		f.transcript = filepath.Join(proj, f.sid+".jsonl")
		if _, err := os.Stat(f.transcript); err != nil {
			fmt.Fprintf(stdout, "No conversation found with session ID: %s\n", resume)
			return 1
		}
		f.lastUUID = lastUUID(f.transcript)
	case sessionID != "":
		f.sid = strings.ToLower(sessionID)
		f.transcript = filepath.Join(proj, f.sid+".jsonl")
	default:
		f.sid = uuid4()
		f.transcript = filepath.Join(proj, f.sid+".jsonl")
	}
	if !session.IsUUID(f.sid) {
		fmt.Fprintf(stderr, "fakeclaude: invalid session id %q\n", f.sid)
		return 1
	}
	if err := os.MkdirAll(proj, 0o700); err != nil {
		fmt.Fprintln(stderr, "fakeclaude:", err)
		return 1
	}
	f.procStart, _ = procx.StartTime("/proc", f.pid)
	f.startedAt = nowMS()
	f.tmux = tmuxRef()
	f.registry = filepath.Join(f.cfg, "sessions", strconv.Itoa(f.pid)+".json")
	if err := f.writeRegistry("busy"); err != nil {
		fmt.Fprintln(stderr, "fakeclaude:", err)
		return 1
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		os.Remove(f.registry)
		os.Exit(0)
	}()

	if child := os.Getenv("FAKECLAUDE_RUN_CHILD"); child != "" {
		// Deliberately a shell: this mimics Claude Code's `! <command>` bash
		// mode. The value comes from the test's own environment, never from
		// a user; fakeclaude is a test program, not part of the tool.
		c := exec.Command("sh", "-c", child)
		c.Env = append(os.Environ(), "CLAUDE_PID="+strconv.Itoa(f.pid), "CLAUDE_CODE_SESSION_ID="+f.sid, "CLAUDECODE=1")
		c.Stdout, c.Stderr = stdout, stderr
		c.Run()
	}
	if printMode {
		f.exchange(prompt)
		os.Remove(f.registry)
		return 0
	}
	f.writeRegistry("idle")
	fmt.Fprintf(stdout, "fakeclaude %s session %s in %s\n", f.version, f.sid, f.cwd)
	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line == "/exit" {
			break
		}
		f.writeRegistry("busy")
		f.exchange(line)
		f.writeRegistry("idle")
	}
	os.Remove(f.registry)
	return 0
}

// tmuxRef asks tmux for "<session>:@<win>.%<pane>" when running inside tmux.
func tmuxRef() string {
	if v := os.Getenv("FAKECLAUDE_TMUX"); v != "" {
		return v // Plan 03's fake tmux server: no tmux binary to ask
	}
	pane := os.Getenv("TMUX_PANE")
	if os.Getenv("TMUX") == "" || pane == "" {
		return ""
	}
	sock, _, _ := strings.Cut(os.Getenv("TMUX"), ",")
	out, err := exec.Command("tmux", "-S", sock, "display-message", "-p", "-t", pane, "#{session_name}:#{window_id}.#{pane_id}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (f *fake) writeRegistry(status string) error {
	ts := nowMS()
	rec := map[string]any{
		"pid": f.pid, "sessionId": f.sid, "cwd": f.cwd, "startedAt": f.startedAt, "procStart": f.procStart,
		"version": f.version, "kind": "interactive", "entrypoint": "cli", "tmux": f.tmux,
		"messagingSocketPath": filepath.Join(f.cfg, "sessions", strconv.Itoa(f.pid)+".sock"),
		"name": filepath.Base(f.cwd), "nameSource": "auto", "status": status, "updatedAt": ts, "statusUpdatedAt": ts,
	}
	data, _ := json.Marshal(rec)
	return session.WriteFileAtomic(f.registry, data, 0o600)
}

func lastUUID(transcript string) string {
	data, err := os.ReadFile(transcript)
	if err != nil {
		return ""
	}
	last := ""
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r struct {
			UUID string `json:"uuid"`
		}
		if json.Unmarshal([]byte(l), &r) == nil && r.UUID != "" {
			last = r.UUID
		}
	}
	return last
}

func (f *fake) append(rec map[string]any) {
	fh, err := os.OpenFile(f.transcript, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer fh.Close()
	enc := json.NewEncoder(fh)
	enc.SetEscapeHTML(false)
	enc.Encode(rec)
}

func (f *fake) base(typ string) map[string]any {
	parent := any(nil)
	if f.lastUUID != "" {
		parent = f.lastUUID
	}
	id := uuid4()
	rec := map[string]any{
		"parentUuid": parent, "isSidechain": false, "userType": "external", "cwd": f.cwd, "sessionId": f.sid,
		"version": f.version, "gitBranch": f.branch, "type": typ, "uuid": id, "timestamp": now(),
	}
	f.lastUUID = id
	return rec
}

// exchange appends one user turn and one assistant turn, and a history line.
func (f *fake) exchange(prompt string) {
	u := f.base("user")
	u["message"] = map[string]any{"role": "user", "content": prompt}
	f.append(u)
	hist := map[string]any{"display": prompt, "pastedContents": map[string]any{}, "timestamp": nowMS(), "project": f.cwd, "sessionId": f.sid}
	if hf, err := os.OpenFile(filepath.Join(f.cfg, "history.jsonl"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600); err == nil {
		json.NewEncoder(hf).Encode(hist)
		hf.Close()
	}
	reply := getenv("FAKECLAUDE_REPLY", "ok: "+prompt)
	a := f.base("assistant")
	a["requestId"] = "req_fake_" + strconv.FormatInt(nowMS(), 36)
	a["message"] = map[string]any{
		"id": "msg_fake_" + strconv.FormatInt(nowMS(), 36), "type": "message", "role": "assistant", "model": "claude-opus-4-1",
		"content":     []any{map[string]any{"type": "text", "text": reply}},
		"stop_reason": "end_turn", "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 10, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0, "output_tokens": 5},
	}
	f.append(a)
}
```

- [ ] **Step 5: Run tests**

Run: `go test -race ./test/fakeclaude/... -v`
Expected: PASS (`TestTmuxField` skips when tmux is absent; CI installs tmux).

- [ ] **Step 6: Commit**

```bash
git add test/fakeclaude
git commit -m "test(fakeclaude): fake claude binary reproducing the on-disk behaviour"
```

---

### Task 20: Root teleport command (all spec §5 flags), help text, `inspect`, `list`

**Files:**
- Create: `internal/cli/root.go`, `internal/cli/help.go`, `internal/cli/inspect.go`, `internal/cli/list.go`
- Modify: `internal/cli/cli.go` (`rootCmd` now delegates to `root.go`)
- Test: `internal/cli/root_test.go`, `internal/cli/list_test.go`

**Interfaces:**
- Consumes: `session.ParseSelector`, `session.Resolve`, `session.InventoryFiles`, `session.ScanUsage`, `session.ParseMappings`, `session.ReadRegistry`, `session.ReadMeta`, `session.ProcAlive`.
- Produces: `teleportFlags` struct (every option from spec §5; Plans 02/03 read it), `(*app).selectorEnv() session.Env`, `(*app).probe() session.PaneProbe` (nil in this plan; Plan 03 returns `tmuxx.Prober`), `(*app).resolveSession(args []string) (*session.Session, error)`, `inspectCmd`, `listCmd`.

- [ ] **Step 1: Write the failing tests**

`internal/cli/root_test.go`:

```go
package cli

import (
	"strings"
	"testing"
)

func TestTeleportFlagsParseButTransportIsStubbed(t *testing.T) {
	code, _, stderr := run(t, []string{"HOME=/home/alice"}, "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13", "--to", "big-storage.example",
		"--via", "jump.example", "-o", "User=alice", "--dest-path", "/srv/w", "--map", "/home/alice=/home/bob",
		"--state", "idle", "--allow-config-drift", "--force", "--tmux-socket", "main", "--exclude", "*.log",
		"--dry-run", "--exit-timeout", "10s", "--start-timeout", "1m", "--log", "/tmp/x.log", "-v")
	if code != ExitUsage || !strings.Contains(stderr, "transport not implemented yet") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
	// canonical spellings and --from
	code, _, stderr = run(t, []string{"HOME=/home/alice"}, "--teleport-from", "laptop.example")
	if code != ExitUsage || !strings.Contains(stderr, "transport not implemented yet") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func TestTeleportFlagValidation(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--to", "a", "--from", "b"}, "exactly one of"},
		{[]string{"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13"}, "exactly one of"},
		{[]string{"--to", "a", "--state", "bogus"}, "--state"},
		{[]string{"--to", "a", "--map", "nope"}, "--map"},
		{[]string{"--to", "a", "--no-tmux", "--state", "running"}, "--no-tmux"},
		{[]string{"--to", "a", "-v", "-q"}, "--verbose and --quiet"},
		{[]string{"--to", "a", "x", "y", "z"}, "too many"},
	}
	for _, c := range cases {
		code, _, stderr := run(t, []string{"HOME=/home/alice"}, c.args...)
		if code != ExitUsage || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: exit %d stderr %q (want %q)", c.args, code, stderr, c.want)
		}
	}
}

func TestHelpDocumentsEverything(t *testing.T) {
	code, out, _ := run(t, nil, "--help")
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	for _, w := range []string{
		"--teleport-to", "--teleport-from", "--to", "--from", "--via", "-o KEY=VALUE", "--dest-path", "--map", "--state",
		"--allow-config-drift", "--force", "--tmux-socket", "--no-tmux", "--exclude", "--dry-run", "--exit-timeout",
		"--start-timeout", "--config-dir", "--log", "--json", "--verbose", "--quiet",
		"continue <sid>", "status", "abandon", "inspect", "list", "compare-config", "doctor", "placeholder", "version",
		"CLAUDE_CODE_SESSION_ID", "Exit codes", "claude --teleport",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("help lacks %q", w)
		}
	}
	if strings.Contains(out, "Plan 02") || strings.Contains(out, "Plan 03") {
		t.Error("help must not mention implementation plans")
	}
}

func TestInspectFixture(t *testing.T) {
	env := []string{"HOME=/home/alice", "CLAUDE_CONFIG_DIR=../session/testdata/config"}
	code, out, stderr := run(t, env, "inspect", "3f9c")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, w := range []string{"3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13", "idle", "/home/alice/github/example/widget", "feature/teleport",
		"2.1.247", "tool-results/toolu_01Ab3.txt", "mcp: filesystem, playwright", "skills: superpowers:test-driven-development",
		"memory/MEMORY.md", "skipped", ".lock"} {
		if !strings.Contains(out, w) {
			t.Errorf("inspect lacks %q:\n%s", w, out)
		}
	}
	code, out, _ = run(t, env, "inspect", "--json", "3f9c")
	if code != ExitOK || !strings.HasPrefix(out, "{") || !strings.Contains(out, `"launch_cwd": "/home/alice/github/example/widget"`) {
		t.Fatalf("json: %d %s", code, out)
	}
	if code, _, stderr := run(t, env, "inspect", "zzzz"); code != ExitRefused || !strings.Contains(stderr, "session not found") {
		t.Fatalf("not found: %d %q", code, stderr)
	}
}
```

`internal/cli/list_test.go`:

```go
package cli

import (
	"strings"
	"testing"
)

func TestListFixture(t *testing.T) {
	env := []string{"HOME=/home/alice", "CLAUDE_CONFIG_DIR=../session/testdata/config"}
	code, out, stderr := run(t, env, "list")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	// the fixture registry pids are not alive on this machine: both sessions are idle
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "ID") {
		t.Fatalf("%q", out)
	}
	for _, w := range []string{"3f9c2b7e  idle", "a1b2c3d4  idle", "feature/teleport", "2026-08-27T11:00:05.000Z"} {
		if !strings.Contains(out, w) {
			t.Errorf("list lacks %q:\n%s", w, out)
		}
	}
	code, out, _ = run(t, env, "list", "--json")
	if code != ExitOK || !strings.Contains(out, `"state": "idle"`) {
		t.Fatalf("json: %d %s", code, out)
	}
	if code, _, stderr := run(t, env, "list", "--host", "big-storage.example"); code != ExitUsage || !strings.Contains(stderr, "not implemented yet") {
		t.Fatalf("--host: %d %q", code, stderr)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestTeleport|TestHelp|TestInspect|TestList' -v`
Expected: FAIL — unknown flags / `unknown command "inspect"`.

- [ ] **Step 3: Implement**

`internal/cli/help.go`:

```go
package cli

const rootLong = `claude-teleport moves one in-progress Claude Code session — its transcript
and sidecar state, the git repository/worktree it works in, and the tmux
window it runs in — from one machine to another over ssh, confirms it
resumed there, and leaves the source resumable (or teleportable back).

Not to be confused with Anthropic's own "claude --teleport", which brings a
claude.ai cloud session into your terminal; this tool moves local sessions
between your machines.

Usage:
  claude-teleport [<session>] --to   <host> [--via <jump>]... [options]
  claude-teleport [<session>] --from <host> [--via <jump>]... [options]
  claude-teleport <tmux-session> <window> --to|--from <host> ...
  claude-teleport continue <sid>            resume an interrupted job (default when re-running)
  claude-teleport status  [<sid>]           journal and manifest of a job
  claude-teleport abandon <sid> [--delete-destination-files]
  claude-teleport inspect [<session>]       everything a teleport would move + drift report
  claude-teleport list [--host <host>]      sessions here (running/suspended/idle) and teleport history
  claude-teleport compare-config <host> [--session <session>]
  claude-teleport doctor [<host>]           local (and remote) prerequisites
  claude-teleport placeholder --resume <sid> [--saved-output F] [--now] [--teleported-to H]
  claude-teleport version

--teleport-to/--teleport-from are the canonical spellings; --to/--from are
aliases. Exactly one of them is required for a teleport. Run it from
anywhere, including from inside the session being moved:
  ! claude-teleport --to big-storage.example

Session selector (<session>), in order of resolution:
  1. absent      $CLAUDE_CODE_SESSION_ID if set (you are inside the session),
                 else the Claude running in the current tmux pane ($TMUX_PANE),
                 else an error listing candidates
  2. a full uuid
  3. a unique uuid prefix (>= 4 hex chars) or a unique registry name
  4. <tmux-session> <window> (window by index or name): the pane's running
     Claude or its placeholder identifies the session
With --from, selection runs on the remote (the source) with the same rules.

Options:
  --via HOST              jump host(s), repeatable, outermost first; composes with ProxyJump
  -o KEY=VALUE            ssh option override (User, Port, IdentityFile, StrictHostKeyChecking, ...)
  --dest-path DIR         put the session's cwd at DIR instead of the same path (implies a --map)
  --map SRC=DST           extra path prefix rewrite, repeatable
  --state auto|running|suspended|idle
                          destination end state; auto preserves the source state
  --allow-config-drift    turn blocking configuration drift into warnings
  --force                 allow non-fast-forward replacement of an existing copy of this session
  --tmux-socket NAME      destination tmux socket name (default: same as source)
  --no-tmux               do not use tmux on the destination (end state must be idle)
  --exclude GLOB          omit matching files from the repository transfer, repeatable
  --dry-run               preflight and plan only; nothing touched, nothing frozen
  --exit-timeout D        wait for the source Claude to exit (default 30s)
  --start-timeout D       wait for the destination Claude to resume (default 90s)
  --config-dir DIR        local CLAUDE_CONFIG_DIR override
  --log FILE              additional log destination
  --json                  machine-readable output for status, list, inspect, compare-config
  -v, --verbose / -q, --quiet
                          log level

Exit codes:
  0 success; 1 teleport failed (job left resumable); 2 usage; 3 preflight
  refused (drift, collision, unsupported state) — nothing touched; 4 remote
  unreachable / version mismatch; 5 confirmation failed (destination Claude
  did not resume, e.g. not logged in); 6 interrupted (job resumable).

Environment honoured: CLAUDE_CONFIG_DIR, HOME, XDG_DATA_HOME,
CLAUDE_CODE_SESSION_ID, CLAUDE_PID, TMUX, TMUX_PANE, SSH_AUTH_SOCK.`
```

`internal/cli/root.go`:

```go
package cli

import (
	"errors"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// teleportFlags holds every option of the teleport command (spec §5).
// Plans 02/03 consume it; this plan only parses and validates it.
type teleportFlags struct {
	To, From     string
	Via          []string
	SSHOptions   []string // -o KEY=VALUE
	DestPath     string
	Maps         []string
	State        string
	AllowDrift   bool
	Force        bool
	TmuxSocket   string
	NoTmux       bool
	Excludes     []string
	DryRun       bool
	ExitTimeout  time.Duration
	StartTimeout time.Duration
	LogFile      string
	JSON         bool
	Verbose      bool
	Quiet        bool
}

var validStates = map[string]bool{"auto": true, "running": true, "suspended": true, "idle": true}

// validate applies the cross-flag rules; it returns usage errors.
func (f *teleportFlags) validate(args []string) error {
	if (f.To == "") == (f.From == "") {
		return Exit(ExitUsage, "exactly one of --teleport-to/--to or --teleport-from/--from is required")
	}
	if !validStates[f.State] {
		return Exit(ExitUsage, "--state must be auto, running, suspended or idle (got %q)", f.State)
	}
	if f.NoTmux && f.State != "idle" && f.State != "auto" {
		return Exit(ExitUsage, "--no-tmux allows only --state idle (got %q)", f.State)
	}
	if f.Verbose && f.Quiet {
		return Exit(ExitUsage, "--verbose and --quiet are mutually exclusive")
	}
	if _, err := session.ParseMappings(f.Maps); err != nil {
		return Exit(ExitUsage, "%v", err)
	}
	if len(args) > 2 {
		return Exit(ExitUsage, "too many arguments: expected [<session>] or <tmux-session> <window>")
	}
	return nil
}

// flagAliases maps --to/--from to the canonical spellings.
func flagAliases(f *pflag.FlagSet, name string) pflag.NormalizedName {
	switch name {
	case "to":
		name = "teleport-to"
	case "from":
		name = "teleport-from"
	}
	return pflag.NormalizedName(name)
}

func (a *app) rootCmd() *cobra.Command {
	var tf teleportFlags
	root := &cobra.Command{
		Use:           "claude-teleport [<session>] --to|--from <host> [options]",
		Short:         "move an in-progress Claude Code session to another machine",
		Long:          rootLong,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if tf.To == "" && tf.From == "" && len(args) == 0 {
				return cmd.Help()
			}
			if err := tf.validate(args); err != nil {
				return err
			}
			if _, err := session.ParseSelector(args, a.selectorEnv()); err != nil {
				return Exit(ExitUsage, "%v", err)
			}
			// The orchestrator (git, tmux, ssh transport) arrives with Plans 02
			// and 03; until then a teleport is a clean usage error.
			return Exit(ExitUsage, "transport not implemented yet: this build parses the command line only")
		},
	}
	root.SetHelpTemplate("{{.Long}}\n")
	f := root.Flags()
	f.SetNormalizeFunc(flagAliases)
	f.StringVar(&tf.To, "teleport-to", "", "destination host (alias --to)")
	f.StringVar(&tf.From, "teleport-from", "", "source host (alias --from)")
	f.StringArrayVar(&tf.Via, "via", nil, "jump host, repeatable")
	f.StringArrayVarP(&tf.SSHOptions, "option", "o", nil, "ssh option KEY=VALUE")
	f.StringVar(&tf.DestPath, "dest-path", "", "destination cwd")
	f.StringArrayVar(&tf.Maps, "map", nil, "path prefix rewrite SRC=DST")
	f.StringVar(&tf.State, "state", "auto", "destination end state")
	f.BoolVar(&tf.AllowDrift, "allow-config-drift", false, "downgrade blocking drift to warnings")
	f.BoolVar(&tf.Force, "force", false, "allow non-fast-forward replacement of this session")
	f.StringVar(&tf.TmuxSocket, "tmux-socket", "", "destination tmux socket name")
	f.BoolVar(&tf.NoTmux, "no-tmux", false, "do not use tmux on the destination")
	f.StringArrayVar(&tf.Excludes, "exclude", nil, "exclude glob, repeatable")
	f.BoolVar(&tf.DryRun, "dry-run", false, "preflight only")
	f.DurationVar(&tf.ExitTimeout, "exit-timeout", 30*time.Second, "source exit wait")
	f.DurationVar(&tf.StartTimeout, "start-timeout", 90*time.Second, "destination start wait")
	f.StringVar(&tf.LogFile, "log", "", "additional log file")
	root.PersistentFlags().BoolVar(&tf.JSON, "json", false, "machine-readable output")
	root.PersistentFlags().BoolVarP(&tf.Verbose, "verbose", "v", false, "verbose logging")
	root.PersistentFlags().BoolVarP(&tf.Quiet, "quiet", "q", false, "quiet logging")
	root.PersistentFlags().StringVar(&a.configDir, "config-dir", "", "local CLAUDE_CONFIG_DIR override")
	a.flags = &tf

	root.AddCommand(a.versionCmd(), a.internalFreezerCmd(), a.placeholderCmd(), a.inspectCmd(), a.listCmd())
	return root
}

// selectorEnv is the session-related environment (spec §3).
func (a *app) selectorEnv() session.Env {
	return session.Env{SessionID: a.env["CLAUDE_CODE_SESSION_ID"], PID: a.env["CLAUDE_PID"],
		TmuxPane: a.env["TMUX_PANE"], Tmux: a.env["TMUX"]}
}

// probe returns the tmux pane probe. Plan 03 returns tmuxx.Prober when a
// tmux server is reachable; in this plan there is no tmux client, so
// suspended panes and the two-word selector are not resolvable yet.
func (a *app) probe() session.PaneProbe { return nil }

// resolveSession applies the spec §5 selector rules locally.
func (a *app) resolveSession(args []string) (*session.Session, error) {
	p, err := a.resolvePaths()
	if err != nil {
		return nil, err
	}
	sel, err := session.ParseSelector(args, a.selectorEnv())
	if err != nil {
		return nil, Exit(ExitUsage, "%v", err)
	}
	s, err := session.Resolve(p, sel, a.probe())
	if errors.Is(err, session.ErrNotFound) {
		return nil, Exit(ExitRefused, "%v", err)
	}
	if err != nil {
		return nil, Exit(ExitUsage, "%v", err)
	}
	return s, nil
}

func (a *app) json() bool { return a.flags != nil && a.flags.JSON }
```

In `internal/cli/cli.go`: delete the Task 1 `rootCmd` function (it now lives in `root.go`) and extend the struct:

```go
// app is the per-invocation state shared by every command.
type app struct {
	stdin     io.Reader
	stdout    io.Writer
	stderr    io.Writer
	env       map[string]string
	configDir string         // --config-dir (persistent flag)
	flags     *teleportFlags // root flags incl. the persistent --json/-v/-q
}
```

(`cli.go` no longer needs the `cobra` import once `rootCmd` moves out.) Cobra's `--help` on the root prints `rootLong` because of the help template; subcommands keep the default template.

`internal/cli/inspect.go`:

```go
package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/session"
)

type inspectOut struct {
	ID         string              `json:"id"`
	State      string              `json:"state"`
	Name       string              `json:"name,omitempty"`
	LaunchCwd  string              `json:"launch_cwd"`
	WorkCwd    string              `json:"work_cwd"`
	Branch     string              `json:"branch"`
	Version    string              `json:"claude_version"`
	Transcript string              `json:"transcript"`
	Registry   *session.Registry   `json:"registry,omitempty"`
	Tmux       *session.TmuxRef    `json:"tmux,omitempty"`
	Files      []session.FileEntry `json:"files"`
	Memory     []session.FileEntry `json:"memory"`
	Skipped    []session.Skipped   `json:"skipped"`
	TotalBytes int64               `json:"total_bytes"`
	Usage      *session.Usage      `json:"usage"`
}

func keys(m map[string]bool) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	if len(ks) == 0 {
		return "(none)"
	}
	return strings.Join(ks, ", ")
}

func (a *app) inspectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect [<session>]",
		Short: "show everything a teleport would move for a session",
		Long: `Resolves the session (same rules as a teleport), then lists its state,
directories, every session file that would be transferred, what the
transcript used (MCP servers, skills, plugins, sub-agents) and what would
be skipped. The configuration drift report needs a destination host: see
"claude-teleport compare-config <host> --session <session>".`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.resolveSession(args)
			if err != nil {
				return err
			}
			inv, err := session.InventoryFiles(s)
			if err != nil {
				return Exit(ExitFailed, "%v", err)
			}
			usage, err := session.ScanUsage(s)
			if err != nil {
				return Exit(ExitFailed, "%v", err)
			}
			out := inspectOut{ID: string(s.ID), State: s.State.String(), Name: s.Name, LaunchCwd: s.LaunchCwd, WorkCwd: s.WorkCwd,
				Branch: s.Branch, Version: s.Version, Transcript: s.Transcript, Registry: s.Registry, Tmux: s.Tmux,
				Files: inv.Files, Memory: inv.Memory, Skipped: inv.Skipped, Usage: usage}
			for _, f := range inv.Files {
				out.TotalBytes += f.Size
			}
			if a.json() {
				b, _ := json.MarshalIndent(out, "", "  ")
				fmt.Fprintln(a.stdout, string(b))
				return nil
			}
			w := a.stdout
			fmt.Fprintf(w, "session    %s (%s)\n", out.ID, out.State)
			if out.Name != "" {
				fmt.Fprintf(w, "name       %s\n", out.Name)
			}
			fmt.Fprintf(w, "launch cwd %s\n", out.LaunchCwd)
			if out.WorkCwd != out.LaunchCwd {
				fmt.Fprintf(w, "work cwd   %s\n", out.WorkCwd)
			}
			fmt.Fprintf(w, "branch     %s\nclaude     %s\ntranscript %s\n", out.Branch, out.Version, out.Transcript)
			if out.Registry != nil {
				fmt.Fprintf(w, "process    pid %d status %s tmux %q\n", out.Registry.PID, out.Registry.Status, out.Registry.Tmux)
			}
			fmt.Fprintf(w, "\nfiles to move (%d, %d bytes):\n", len(out.Files), out.TotalBytes)
			for _, f := range out.Files {
				if f.Mode.IsDir() {
					continue
				}
				fmt.Fprintf(w, "  %10d  %s\n", f.Size, f.Rel)
			}
			if len(out.Memory) > 0 {
				fmt.Fprintln(w, "\nproject memory (copied only if absent on the destination):")
				for _, f := range out.Memory {
					if !f.Mode.IsDir() {
						fmt.Fprintf(w, "  %10d  %s\n", f.Size, f.Rel)
					}
				}
			}
			if len(out.Skipped) > 0 {
				fmt.Fprintln(w, "\nskipped:")
				for _, sk := range out.Skipped {
					fmt.Fprintf(w, "  %s (%s)\n", sk.Path, sk.Reason)
				}
			}
			fmt.Fprintf(w, "\nused by the transcript:\n  mcp: %s\n  skills: %s\n  plugins: %s\n  sub-agents: %s\n  permission modes: %s\n",
				keys(usage.MCPServers), keys(usage.Skills), keys(usage.Plugins), keys(usage.SubagentTypes), keys(usage.PermissionModes))
			fmt.Fprintln(w, "\ndrift report: needs a destination — run: claude-teleport compare-config <host> --session", out.ID[:8])
			return nil
		},
	}
}
```

`internal/cli/list.go`:

```go
package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/session"
)

type listRow struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Name   string `json:"name,omitempty"`
	PID    int    `json:"pid,omitempty"`
	Cwd    string `json:"cwd"`
	Branch string `json:"branch"`
	Last   string `json:"last_active"`
	Tmux   string `json:"tmux,omitempty"`
}

// listSessions enumerates every transcript under projects/ and marks the
// ones with a live registry entry as running (a placeholder pane scan needs
// the tmux probe, which arrives with Plan 03).
func listSessions(p session.Paths, probe session.PaneProbe) ([]listRow, error) {
	regs, err := session.ReadRegistry(p.SessionsDir())
	if err != nil {
		return nil, err
	}
	running := map[string]session.Registry{}
	for _, r := range regs {
		if session.ProcAlive(session.ProcRoot, r.PID, r.ProcStart) {
			running[r.SessionID] = r
		}
	}
	transcripts, err := filepath.Glob(filepath.Join(p.ProjectsDir(), "*", "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("glob transcripts: %w", err)
	}
	var rows []listRow
	for _, t := range transcripts {
		id := strings.TrimSuffix(filepath.Base(t), ".jsonl")
		if !session.IsUUID(id) {
			continue
		}
		m, err := session.ReadMeta(t)
		if err != nil {
			return nil, err
		}
		row := listRow{ID: id, State: session.StateIdle.String(), Cwd: m.LaunchCwd, Branch: m.Branch, Last: m.LastTS}
		if r, ok := running[id]; ok {
			row.State, row.Name, row.PID, row.Tmux = session.StateRunning.String(), r.Name, r.PID, r.Tmux
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].State != rows[j].State {
			return rows[i].State == "running"
		}
		return rows[i].Last > rows[j].Last
	})
	return rows, nil
}

func (a *app) listCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{
		Use:   "list [--host <host>]",
		Short: "list sessions on this host (running / suspended / idle)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if host != "" {
				// Remote listing rides on the Plan 02 helper protocol.
				return Exit(ExitUsage, "--host: remote listing not implemented yet")
			}
			p, err := a.resolvePaths()
			if err != nil {
				return err
			}
			rows, err := listSessions(p, a.probe())
			if err != nil {
				return Exit(ExitFailed, "%v", err)
			}
			if a.json() {
				if rows == nil {
					rows = []listRow{}
				}
				b, _ := json.MarshalIndent(rows, "", "  ")
				fmt.Fprintln(a.stdout, string(b))
				return nil
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTATE\tNAME\tPID\tCWD\tBRANCH\tLAST ACTIVE")
			for _, r := range rows {
				pid := ""
				if r.PID != 0 {
					pid = fmt.Sprint(r.PID)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID[:8], r.State, r.Name, pid, r.Cwd, r.Branch, r.Last)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "list sessions on a remote host")
	return cmd
}
```

- [ ] **Step 4: Run tests**

Run: `go vet ./... && go test -race ./internal/cli/ -v`
Expected: PASS. (If `TestListFixture` finds an extra row: the fixture project dir must contain only the two session transcripts; `sessions-index.json` is not a `.jsonl` and is skipped.)

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "feat(cli): teleport flags and help, inspect and list"
```

---

### Task 21: `compare-config` (local directory mode) and `doctor`

**Files:**
- Create: `internal/cli/compare.go`, `internal/cli/doctor.go`
- Modify: `internal/cli/root.go` (register both)
- Test: `internal/cli/compare_test.go`, `internal/cli/doctor_test.go`

**Interfaces:**
- Consumes: `claudecfg.Collect`, `claudecfg.Compare`, `Report.Render/JSON/Downgrade`.
- Produces: package var `claudeVersionFn func(env []string) (string, error)` (runs `claude --version`; swappable), `compareConfigCmd`, `doctorCmd`.

`compare-config <host>`: when `<host>` is an absolute path to an existing directory, it is treated as the *destination config dir* on this machine (with `--dest-home`, default: the directory's parent) — this exercises the whole report locally and is how tests and the docker layer compare two config dirs. A hostname needs the Plan 02 transport and is refused with "not implemented yet".

- [ ] **Step 1: Write the failing tests**

`internal/cli/compare_test.go`:

```go
package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func stubClaudeVersion(t *testing.T, v string) {
	t.Helper()
	old := claudeVersionFn
	claudeVersionFn = func([]string) (string, error) { return v, nil }
	t.Cleanup(func() { claudeVersionFn = old })
}

func TestCompareConfigLocalDirs(t *testing.T) {
	stubClaudeVersion(t, "2.1.247")
	dst, _ := filepath.Abs("../claudecfg/testdata/dst")
	env := []string{"HOME=/home/alice", "CLAUDE_CONFIG_DIR=../claudecfg/testdata/src", "PWD=/home/alice/github/example/widget"}
	code, out, stderr := run(t, env, "compare-config", dst)
	if code != ExitRefused {
		t.Fatalf("blocking drift must exit 3: %d %s %s", code, out, stderr)
	}
	for _, w := range []string{"block  hooks", "block  mcp.playwright", "block  plugin.superpowers@claude-plugins-official", "warn   model", "info   project"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q:\n%s", w, out)
		}
	}
	code, out, _ = run(t, env, "compare-config", "--allow-config-drift", dst)
	if code != ExitOK || strings.Contains(out, "block ") {
		t.Fatalf("downgraded: %d\n%s", code, out)
	}
	code, out, _ = run(t, env, "compare-config", "--json", dst)
	if code != ExitRefused || !strings.Contains(out, `"blocking": true`) {
		t.Fatalf("json: %d %s", code, out)
	}
}

func TestCompareConfigWithSessionUsesUsage(t *testing.T) {
	stubClaudeVersion(t, "2.1.247")
	dst, _ := filepath.Abs("../claudecfg/testdata/dst")
	// the session fixture's cwd is the same project; its usage names playwright, filesystem, superpowers
	env := []string{"HOME=/home/alice", "CLAUDE_CONFIG_DIR=../session/testdata/config"}
	code, out, _ := run(t, env, "compare-config", "--session", "3f9c", dst)
	if code != ExitRefused || !strings.Contains(out, "block  mcp.playwright") {
		t.Fatalf("%d\n%s", code, out)
	}
	if strings.Contains(out, "block  mcp.unused") {
		t.Fatal("unused servers must not block when a session is given")
	}
}

func TestCompareConfigRemoteNotYet(t *testing.T) {
	if code, _, stderr := run(t, []string{"HOME=/home/alice"}, "compare-config", "big-storage.example"); code != ExitUsage || !strings.Contains(stderr, "not implemented yet") {
		t.Fatalf("%d %q", code, stderr)
	}
}
```

`internal/cli/doctor_test.go`:

```go
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/test/fakeclaude/harness"
)

func TestDoctorPassesWithFakeClaude(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, ".claude")
	os.MkdirAll(filepath.Join(cfg, "projects"), 0o700)
	env := harness.Env(t, root, cfg, "XDG_DATA_HOME="+filepath.Join(root, "data"))
	code, out, stderr := run(t, env, "doctor")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s%s", code, out, stderr)
	}
	for _, w := range []string{"ok    claude on PATH", "2.1.247", "ok    config dir", "ok    data dir writable"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q:\n%s", w, out)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "data", "claude-teleport")); err != nil {
		t.Fatal("doctor must create the data dir")
	}
}

func TestDoctorFailsWithoutClaude(t *testing.T) {
	root := t.TempDir()
	code, out, _ := run(t, []string{"HOME=" + root, "PATH=" + t.TempDir()}, "doctor")
	if code != ExitFailed || !strings.Contains(out, "FAIL  claude on PATH") || !strings.Contains(out, "FAIL  config dir") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if code, _, stderr := run(t, []string{"HOME=" + root}, "doctor", "big-storage.example"); code != ExitUsage || !strings.Contains(stderr, "not implemented yet") {
		t.Fatalf("remote: %d %q", code, stderr)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestCompareConfig|TestDoctor' -v`
Expected: FAIL — `unknown command "compare-config"`.

- [ ] **Step 3: Implement**

`internal/cli/compare.go`:

```go
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// lookInPath finds bin in a PATH string (exec.LookPath consults the process
// environment; we must honour the environment handed to Main).
func lookInPath(pathEnv, bin string) (string, error) {
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, bin)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: %w", bin, exec.ErrNotFound)
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// claudeVersionFn runs `claude --version` (the one subprocess besides tmux,
// spec §10) with the given environment; swappable in tests.
var claudeVersionFn = func(env []string) (string, error) {
	bin, err := lookInPath(envValue(env, "PATH"), "claude")
	if err != nil {
		return "", err
	}
	cmd := exec.Command(bin, "--version")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude --version: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (a *app) envSlice() []string {
	out := make([]string, 0, len(a.env))
	for k, v := range a.env {
		out = append(out, k+"="+v)
	}
	return out
}

// localClaudeVersion prefers the running session's registry, then the
// transcript, then `claude --version`.
func (a *app) localClaudeVersion(s *session.Session) (string, error) {
	if s != nil && s.Registry != nil && s.Registry.Version != "" {
		return s.Registry.Version, nil
	}
	if s != nil && s.Version != "" {
		return s.Version, nil
	}
	return claudeVersionFn(a.envSlice())
}

func (a *app) compareConfigCmd() *cobra.Command {
	var sel, destHome string
	var allowDrift bool
	cmd := &cobra.Command{
		Use:   "compare-config <host> [--session <session>]",
		Short: "compare Claude configuration here with a destination and classify the drift",
		Long: `Prints the configuration drift table (hooks, permissions, MCP servers,
plugins, skills, sub-agents, model, CLAUDE.md, ...) between this host and
the destination. With --session only what that session used can block;
without it everything counts as used. Exit 3 when anything blocks unless
--allow-config-drift is given.

<host> may also be an absolute path to a Claude config directory on this
machine (with --dest-home for its home directory); the comparison then runs
entirely locally.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			p, err := a.resolvePaths()
			if err != nil {
				return err
			}
			var s *session.Session
			cwd := a.env["PWD"]
			if sel != "" {
				if s, err = a.resolveSession(strings.Fields(sel)); err != nil {
					return err
				}
				cwd = s.LaunchCwd
			} else if cwd == "" {
				if cwd, err = os.Getwd(); err != nil {
					return Exit(ExitFailed, "getwd: %v", err)
				}
			}
			var usage *session.Usage
			if s != nil {
				if usage, err = session.ScanUsage(s); err != nil {
					return Exit(ExitFailed, "%v", err)
				}
			}
			ver, err := a.localClaudeVersion(s)
			if err != nil {
				return Exit(ExitFailed, "%v", err)
			}
			src, err := claudecfg.Collect(p, cwd, "local", ver)
			if err != nil {
				return Exit(ExitFailed, "%v", err)
			}
			info, statErr := os.Stat(target)
			if !filepath.IsAbs(target) || statErr != nil || !info.IsDir() {
				// A hostname: needs the Plan 02 transport (hello + inventory-host).
				return Exit(ExitUsage, "compare-config %s: remote comparison not implemented yet (an absolute config-dir path works locally)", target)
			}
			if destHome == "" {
				destHome = filepath.Dir(target)
			}
			dstPaths := session.NewPaths(destHome, target, "")
			dst, err := claudecfg.Collect(dstPaths, cwd, target, ver)
			if err != nil {
				return Exit(ExitFailed, "%v", err)
			}
			rep := claudecfg.Compare(src, dst, usage)
			if allowDrift {
				rep = rep.Downgrade()
			}
			if a.json() {
				b, err := rep.JSON()
				if err != nil {
					return Exit(ExitFailed, "%v", err)
				}
				fmt.Fprintln(a.stdout, string(b))
			} else {
				rep.Render(a.stdout)
			}
			if rep.Blocking {
				return Exit(ExitRefused, "configuration drift would block a teleport")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&sel, "session", "", "session selector; limits blocking to what the session used")
	cmd.Flags().StringVar(&destHome, "dest-home", "", "home directory for a local destination config dir")
	cmd.Flags().BoolVar(&allowDrift, "allow-config-drift", false, "downgrade blocking drift to warnings")
	return cmd
}
```

`internal/cli/doctor.go`:

```go
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

type check struct {
	name, detail string
	ok           bool
}

// localChecks runs the doctor checks that need no remote host.
func (a *app) localChecks() []check {
	var cs []check
	env := a.envSlice()
	pathLookup := func(bin string) (string, error) { return lookInPath(a.env["PATH"], bin) }
	if p, err := pathLookup("claude"); err != nil {
		cs = append(cs, check{"claude on PATH", "not found (install Claude Code and log in)", false})
	} else if v, err := claudeVersionFn(env); err != nil {
		cs = append(cs, check{"claude on PATH", p + " but --version failed: " + err.Error(), false})
	} else {
		cs = append(cs, check{"claude on PATH", p + " (" + v + ")", true})
	}
	if p, err := pathLookup("tmux"); err != nil {
		cs = append(cs, check{"tmux on PATH", "not found (optional: needed to move tmux windows)", true})
	} else {
		out, _ := exec.Command(p, "-V").Output()
		cs = append(cs, check{"tmux on PATH", p + " (" + strings.TrimSpace(string(out)) + ")", true})
	}
	paths, err := a.resolvePaths()
	if err != nil {
		return append(cs, check{"config dir", err.Error(), false})
	}
	if fi, err := os.Stat(paths.ProjectsDir()); err != nil || !fi.IsDir() {
		cs = append(cs, check{"config dir", paths.ConfigDir + " has no projects/ (has Claude ever run here?)", false})
	} else {
		cs = append(cs, check{"config dir", paths.ConfigDir, true})
	}
	if _, err := os.Stat(paths.GlobalJSON); err != nil {
		cs = append(cs, check{"global json", paths.GlobalJSON + " absent (Claude has not run, or a different CLAUDE_CONFIG_DIR)", true})
	} else {
		cs = append(cs, check{"global json", paths.GlobalJSON, true})
	}
	if err := os.MkdirAll(paths.DataDir, 0o700); err != nil {
		cs = append(cs, check{"data dir writable", err.Error(), false})
	} else if f, err := os.CreateTemp(paths.DataDir, ".doctor-*"); err != nil {
		cs = append(cs, check{"data dir writable", err.Error(), false})
	} else {
		f.Close()
		os.Remove(f.Name())
		cs = append(cs, check{"data dir writable", paths.DataDir, true})
	}
	return cs
}

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor [<host>]",
		Short: "check local (and remote) prerequisites",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				// Remote checks arrive with the Plan 02 helper (hello op).
				return Exit(ExitUsage, "doctor %s: remote checks not implemented yet", args[0])
			}
			failed := false
			for _, c := range a.localChecks() {
				status := "ok  "
				if !c.ok {
					status, failed = "FAIL", true
				}
				fmt.Fprintf(a.stdout, "%s  %-18s %s\n", status, c.name, c.detail)
			}
			if failed {
				return Exit(ExitFailed, "doctor found problems")
			}
			return nil
		},
	}
}
```

In `root.go`, extend the `AddCommand` call: `root.AddCommand(a.versionCmd(), a.internalFreezerCmd(), a.placeholderCmd(), a.inspectCmd(), a.listCmd(), a.compareConfigCmd(), a.doctorCmd())`.

- [ ] **Step 4: Run tests**

Run: `go test -race ./internal/cli/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "feat(cli): compare-config (local dirs) and doctor"
```

---

### Task 22: End-to-end: the CLI against sessions created by fakeclaude

**Files:**
- Test: `internal/cli/e2e_test.go`

**Interfaces:**
- Consumes: `harness.Build`, `harness.Env`, `Main`.
- Produces: the confidence that `list`/`inspect`/`compare-config` agree with what a (fake) Claude writes — the contract Plans 02/03 build the orchestrator on.

- [ ] **Step 1: Write the test**

`internal/cli/e2e_test.go`:

```go
package cli

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/test/fakeclaude/harness"
)

const e2eSID = "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"

func TestListInspectAgainstFakeClaude(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	cfg := filepath.Join(home, ".claude")
	cwd := filepath.Join(home, "github", "example", "widget")
	os.MkdirAll(cwd, 0o700)
	env := harness.Env(t, home, cfg, "FAKECLAUDE_BRANCH=feature/teleport")
	claude := filepath.Join(harness.Build(t), "claude")

	// 1. a finished -p session is idle
	c := exec.Command(claude, "-p", "make it verbose", "--session-id", e2eSID)
	c.Dir, c.Env = cwd, env
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	code, out, stderr := run(t, env, "list", "--json")
	if code != ExitOK {
		t.Fatalf("list: %d %s", code, stderr)
	}
	var rows []struct{ ID, State, Cwd, Branch string }
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 || rows[0].ID != e2eSID || rows[0].State != "idle" ||
		rows[0].Cwd != cwd || rows[0].Branch != "feature/teleport" {
		t.Fatalf("rows %+v %v\n%s", rows, err, out)
	}
	code, out, stderr = run(t, env, "inspect", e2eSID)
	if code != ExitOK || !strings.Contains(out, "(idle)") {
		t.Fatalf("inspect: %d %s %s", code, out, stderr)
	}
	if !strings.Contains(out, e2eSID+".jsonl") {
		t.Fatalf("inspect must list the transcript:\n%s", out)
	}

	// 2. the same session resumed interactively is running, with its registry name
	ic := exec.Command(claude, "--resume", e2eSID)
	ic.Dir, ic.Env = cwd, env
	stdin, _ := ic.StdinPipe()
	if err := ic.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { ic.Process.Kill(); ic.Wait() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, out, _ = run(t, env, "list")
		if strings.Contains(out, "running") || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code != ExitOK || !strings.Contains(out, "a1b2c3d4  running  widget") {
		t.Fatalf("running session not listed:\n%s", out)
	}
	// inside-the-session selection: CLAUDE_CODE_SESSION_ID picks it with no args
	code, out, stderr = run(t, append(env, "CLAUDE_CODE_SESSION_ID="+e2eSID), "inspect")
	if code != ExitOK || !strings.Contains(out, "(running)") || !strings.Contains(out, "process    pid") {
		t.Fatalf("inspect current: %d %s %s", code, out, stderr)
	}
	// compare-config against a copy of the same config dir: nothing blocks
	stubClaudeVersion(t, "2.1.247")
	other := filepath.Join(root, "other", ".claude")
	os.MkdirAll(other, 0o700)
	code, out, _ = run(t, env, "compare-config", "--session", e2eSID, other)
	if code != ExitOK || !strings.Contains(out, "no configuration differences") {
		t.Fatalf("compare-config: %d\n%s", code, out)
	}
	io.WriteString(stdin, "/exit\n")
	if err := ic.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, out, _ = run(t, env, "list"); strings.Contains(out, "running") {
		t.Fatalf("session must be idle after /exit:\n%s", out)
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test -race ./internal/cli/ -run TestListInspectAgainstFakeClaude -v`
Expected: PASS. If `compare-config` reports a `project` info row: `harness.Env` sets `CLAUDE_CONFIG_DIR`, so fakeclaude never writes `.claude.json`, both inventories lack the project entry and the report is empty — as asserted.

- [ ] **Step 3: Full verification**

Run: `go vet ./... && go test -race ./... && CGO_ENABLED=0 go build -o /dev/null ./cmd/claude-teleport && python3 -m unittest discover -s packaging -p 'version_test.py'`
Expected: everything green.

- [ ] **Step 4: Commit and open the PR**

```bash
git add internal/cli
git commit -m "test(cli): end-to-end list/inspect/compare-config against fakeclaude"
git push -u origin plan-01-foundation
gh pr create --title "Plan 01: foundation and local model" --body "Implements docs/superpowers/plans/2026-08-27-claude-teleport-01-foundation.md: module skeleton, cli (flags, inspect, list, compare-config, doctor, placeholder, internal-freezer), internal/session, internal/claudecfg, internal/procx, internal/placeholder, test/fakeclaude, CI and release pipeline."
```

---

## Self-review

**Spec coverage (sections in scope):**

- §3 on-disk model: every "yes" row is inventoried (Task 8); the "merged" rows have their merge functions (Task 11); "never" rows are `Forbidden` (Task 8) and the registry is read-only (Task 4); munging (Task 3); environment variables inside a session (Task 6 `Env`, Task 20 `selectorEnv`); resume semantics — chdir to the launch cwd (Task 17). `~/.claude.json` location with `CLAUDE_CONFIG_DIR` encoded as a verified fact (Task 3).
- §5 command line: all flags parsed and validated, aliases, help text, exit codes (Tasks 1, 20); selector rules 1–4 (Tasks 6, 7); `inspect`, `list`, `compare-config`, `doctor`, `placeholder`, `version` (Tasks 18, 20, 21). `continue`/`status`/`abandon`/`remote` are Plan 02/03 (out of scope here; the help documents them).
- §6.1 freezer with procStart re-check and thaw-on-owner-death (Task 15); detached runner spawning (Task 16); `WaitGone` for §6.3 (Task 16).
- §7.1 forbidden list + test with every forbidden path (Task 8); §7.2 rewrite engine with keys, numbers, unknown fields, unparseable lines (Task 10); §7.3 `IsPrefix` (Task 11); §7.5 merges (Task 11).
- §9 suspended-pane recognition via `ArgvSessionID`/`IsPlaceholderArgv` (Tasks 7, 14).
- §10 inventory (Task 12), usage (Task 9), classification table row by row (Task 13, `TestCompareClassification`), `compare-config` without a session = everything used (Task 21).
- §11 placeholder incl. `--saved-output`, `--now`, `--teleported-to/--at`, argv carries `--resume <uuid>` (Tasks 17, 18).
- §12 unit tests per package with sanitised fixtures; `test/fakeclaude` with registry/tmux/status/procStart, transcript records, `--resume`, `--session-id`, `-p`, `--version`, `/exit`, signals, not-logged-in, `!`-mode child (Task 19). `internal/fakeapi` and docker integration are Plan 02/03.
- §13 workflows, nfpm, packaging helper, README skeleton with usage and the moves/never-moves table (Task 2). Docker layers are Plan 03.
- §14: forbidden paths enforced in code and tests; `.claude.json` parsed generically with only named keys inspected (Tasks 11, 12); invented hosts/homes throughout.

**Placeholder scan:** no "TBD/TODO/implement later"; every code step has full code; every test has a body; fixtures are written out. One deliberate stub — the root command's "transport not implemented yet" — is the specified Plan 01 behaviour, not a placeholder.

**Type consistency:** `session.Paths`/`NewPaths` (Task 3) used identically in Tasks 7, 8, 12, 18, 20, 21; `PaneProbe` with `ListPanes` (Task 7) implemented by the fake in Task 7 and returned as nil by `app.probe()` (Task 20); `ArgvSessionID` (Task 7) wrapped by Task 14; `ProcStartTime` (Task 7) re-exported as `procx.StartTime` (Task 14) and used by fakeclaude (Task 19); `WriteFileAtomic` (Task 11) used by fakeclaude (Task 19); `Report.SourceHost/DestHost` set in `Compare`, copied in `Downgrade`, printed in `Render` (Task 13); `teleportFlags.JSON` read through `a.json()` in Tasks 20–21; `claudeVersionFn` defined in Task 21 and stubbed by `stubClaudeVersion` in Tasks 21–22.

## Interface additions

Recorded per the interfaces doc's rule; nothing listed there was renamed or re-typed.

| Package | Addition | Why |
|---|---|---|
| `session` | `func IsUUID(s string) bool` | shared uuid check (selector, argv, placeholder) |
| `session` | `func NewPaths(home, configDirEnv, xdgDataHome string) Paths` | the one place the `CLAUDE_CONFIG_DIR` / `.claude.json` rule lives |
| `session` | `func ReadRegistryFile(path string) (Registry, error)` | `procx.RegistryForPID` reads one file, not the whole dir |
| `session` | `var ProcRoot = "/proc"`; `func ProcStartTime(procRoot string, pid int) (string, error)`; `func ProcAlive(procRoot string, pid int, procStart string) bool` | `Resolve`/`Load` must verify registry liveness without importing `procx` (which imports `session`) |
| `session` | `func ArgvSessionID(argv []string) (sid string, placeholder bool, ok bool)` | pane-command classification needed by `Resolve`; `procx.IsPlaceholderArgv`/`IsClaudeArgv` wrap it |
| `session` | `Selector.TmuxPane string` | carries `$TMUX_PANE` from `ParseSelector` to `Resolve` for rule 1 |
| `session` | `PaneProbe.ListPanes() ([]PaneInfo, error)`; `type PaneInfo struct{ Session, WindowID, PaneID string }` | suspended-pane discovery in `Load` (Plan 03's `tmuxx.Prober` must implement it: `list-panes -a -F '#{session_name} #{window_id} #{pane_id}'`) |
| `session` | `func FindTranscript(projectsDir string, id ID) (string, error)` | shared by `Load` and the placeholder |
| `session` | `func ParseMappings(specs []string) ([]Mapping, error)`; `NewPathMap` panics on invalid input | CLI validation of `--map` with an error; `NewPathMap`'s signature has no error return |
| `session` | `func WriteFileAtomic(path string, data []byte, mode fs.FileMode) error` | temp+rename used by index merge, project entry, fakeclaude; Plan 02's `job.Journal.Save` can reuse it |
| `claudecfg` | `Inventory.Skills map[string]bool`, `Inventory.Agents map[string]bool` | spec §10 "a used skill or sub-agent type absent → block" needs the names, not just tree hashes |
| `claudecfg` | `Report.SourceHost`, `Report.DestHost` | rendered table header and JSON |
| `claudecfg` | `func (c Class) MarshalJSON() ([]byte, error)`; `func TreeHash(path string) (string, error)`; `func FileHash(path string) (string, error)` | JSON output; hashing helpers reused by tests |
| `procx` | `func StartTime(procRoot string, pid int) (string, error)`; `func ProcState(procRoot string, pid int) (byte, error)`; `var ErrTimeout` | freezer verification, freezer tests, `WaitGone` timeout classification |
| `cli` | `type ExitError`; `func Exit(code int, format string, a ...any) error`; vars `execveFn`, `lookPathFn`, `chdirFn`, `stdinTTYFn`, `stdoutTTYFn`, `claudeVersionFn` | exit-code plumbing and test injection |
| `test/fakeclaude/harness` | `func Build(t testing.TB) string`; `func Env(t testing.TB, home, configDir string, extra ...string) []string` | Plans 02/03 put the fake `claude` on PATH the same way |
| `test/fakeclaude` | env `FAKECLAUDE_TMUX=<session>:@<win>.%<pane>` overrides the registry `tmux` field (alongside `FAKECLAUDE_VERSION`, `FAKECLAUDE_FAIL=not-logged-in`, `FAKECLAUDE_RUN_CHILD`, `FAKECLAUDE_REPLY`, `FAKECLAUDE_BRANCH`) | Plan 03's fake tmux transport has no `tmux` binary for `display-message` |
