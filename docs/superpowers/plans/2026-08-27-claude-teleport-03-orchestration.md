# claude-teleport Plan 03 — git, tmux, orchestration, integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish `claude-teleport`: the git inventory/pack/attach engine (`internal/gitx`), the tmux control-mode layer (`internal/tmuxx`), the remaining `remote.Local` operations, the ten-step teleport state machine (`internal/orchestrate`), the user-facing commands, the docker integration harness (layers 1 and 2), the CI jobs, and the README.

**Architecture:** `gitx` and `tmuxx` are pure libraries (go-git; a tmux control-mode transport copied from go-tmux-saver). `remote.Local` gains the git/tmux/claude ops and the `Server` dispatch entries, so the same operations run in-process or over ssh. `orchestrate.Preflight` produces an immutable `Plan` persisted in the job journal on **both** hosts; `orchestrate.Steps` turns it into `job.Step`s whose `Verify` re-checks reality before `Run`. The CLI spawns a detached `internal-runner` and follows its log; `continue` re-attaches or respawns it. Integration tests drive three docker containers (`source`, `jump`, `dest`) with a private network so `--via jump` is mandatory.

**Tech Stack:** Go 1.26, `github.com/go-git/go-git/v5` (repo inspect, `revlist`, `packfile`, `gitignore`), `github.com/creack/pty` (added; justified in Task 15), `github.com/spf13/cobra`, `github.com/google/go-cmp`, tmux ≥ 3.3 (control mode), docker compose, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-08-27-claude-teleport-design.md` (sections in scope here: §5 remaining commands, §6 full state machine, §8 git, §9 tmux, §12 docker layers 1–2, §13 workflows + README). Shared interfaces: `docs/superpowers/plans/2026-08-27-claude-teleport-00-interfaces.md` — every exported name below matches it verbatim; additions are listed at the end under "Interface additions".

**Pull requests:** this plan is delivered as three PRs into `main`, each from its own `.worktrees/<branch>`:

| PR | Branch | Tasks |
|---|---|---|
| A | `gitx` | 1–7 |
| B | `tmuxx-remote` | 8–16 |
| C | `orchestrate-integration` | 17–28 |

Each task ends with one conventional commit. Run `go vet ./... && go test -race ./...` before every commit.

## Global Constraints

- Module `github.com/mithro/go-claude-teleport`; binary `claude-teleport`; Go `1.26`; `CGO_ENABLED=0`; Apache-2.0.
- Allowed dependencies: `golang.org/x/crypto`, `github.com/kevinburke/ssh_config`, `github.com/go-git/go-git/v5`, `github.com/spf13/cobra`, `github.com/google/go-cmp` (tests). This plan adds `github.com/creack/pty` (pure Go, no cgo; the stdlib has no `openpty`; used only by `RunPtyResume`, spec §9 "No tmux on the destination").
- No `ssh`, `rsync`, `tar`, `gzip`, `git` subprocesses in the tool. `tmux -C` (control mode) and `claude --version` (preflight only) are the only subprocesses. **Tests** may exec `git` and `docker` to cross-check.
- Never read `.credentials.json`, `sessions/*.key`, or token fields.
- Every exported function that touches the filesystem takes explicit directories; only `internal/cli` reads the environment.
- Errors wrap with `%w` and carry the path/pid/op involved. No silent fallbacks.
- Tests: stdlib `testing`, `go-cmp`; fixtures in `testdata/`; sanitised paths (`/home/alice`), hosts (`*.example`), fresh uuids.
- tmux ≥ 3.3 on both ends when tmux is used. Never start a tmux server in the tool (tests start their own with `-L claude-teleport-test-<pid> -f /dev/null`).
- The destination is never damaged: no existing file is overwritten except the fast-forward of the same session's transcript (spec §7.3) and the fast-forward of a branch ref (spec §8).
- Exit codes (spec §5): 0 ok, 1 failed (resumable), 2 usage, 3 refused, 4 unreachable/version, 5 not resumed, 6 interrupted.
- Build tags used by this plan: `tmuxlive` (tests needing a real tmux server), `integration` (docker). Plain `go test -race ./...` must pass without tmux or docker installed.

---

## File structure

```
internal/gitx/
  inspect.go            Inspect, ErrNotRepo, findRoot, worktree metadata reads
  deststate.go          DestStateOf, IsAncestor
  plan.go               Mode, Plan, RefuseError, PlanTransfer
  files.go              Files (walk + ignore + excludes)
  pack.go               WritePack
  attach.go             Attach
  testutil_test.go      repo builders shared by tests (go-git + git CLI cross-checks)
internal/tmuxx/
  tmuxctl.go            copied Transport/Client/Dial (renamed DialControl)/CmdError/Fake
  parse.go, quote.go    copied verbatim (+ attribution header)
  testutil.go           StartTestServer (build tag tmuxlive) 
  describe.go           Facts, Describe, ListSessions, SessionInfo
  servers.go            FindServer, ListServers
  window.go             Plan, OpenWindow, KillWindow
  pane.go               Capture, SendKeys, TypeCommand, State, PaneState
  prober.go             Prober
  testdata/             probe.transcript, transcript-3.5a.txt (copied)
internal/remote/
  plan03_types.go       DELETED (replaced by real imports)
  failure_markers.go    FailureMarkers, HasFailureMarker
  local_git.go          InventoryGit, GitDestState, GitFiles, GitAttach
  local_tmux.go         InventoryTmux, TmuxSessions, OpenWindow, Capture, TypeCommand, PaneState, KillWindow
  local_claude.go       StartClaude, ConfirmClaude, ExitClaude, ClaudeStatus, RunPtyResume
  local_transfer.go     BuildManifest, SessionExtras, Cleanup, ListSessions
  streams.go            ServeStream cases for tar/pack, PipeStream
  ops_plan03.go         Server dispatch + Client methods for the ops above
internal/orchestrate/
  options.go            Options, Plan (+json), Extras
  preflight.go          Preflight
  render.go             Render
  steps.go              Steps
  placeholder_argv.go   PlaceholderArgv, SuspendArgv
  runner.go             EndpointFactory, RunJob, Outcome helpers
  orchestrate_test.go   in-process end-to-end (Fake tmux transport)
  orchestrate_live_test.go  (tmuxlive) real tmux variant
internal/cli/
  teleport.go           root teleport command, continue, internal-runner
  abandon.go            abandon (full)
  inspect.go            inspect (full)
  list.go               list --host
  doctor.go             doctor <host>
test/integration/
  Dockerfile, docker-compose.yml, sshd_config, entrypoint.sh
  integration_test.go   (build tag integration) layer 1
  realclaude_test.go    (build tag integration && realclaude) layer 2
.github/workflows/test.yml   integration + real-claude jobs
README.md
```

---

### Task 1: gitx.Inspect — repository and worktree inventory

**Files:**
- Create: `internal/gitx/inspect.go`
- Create: `internal/gitx/testutil_test.go`
- Test: `internal/gitx/inspect_test.go`

**Interfaces:**
- Consumes: nothing from other packages except go-git.
- Produces: `gitx.Info`, `gitx.Dirty`, `gitx.ErrNotRepo`, `gitx.Inspect(cwd string) (*Info, error)`; `Info.DirtySubmodules []string` (addition).

- [ ] **Step 1: Write the test helpers (real repos via go-git, cross-checked with the git CLI)**

```go
// internal/gitx/testutil_test.go
package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// gitCLI runs the real git binary inside dir. Tests only: the tool itself
// never execs git. Skips the test when git is not installed.
func gitCLI(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=alice", "GIT_AUTHOR_EMAIL=alice@laptop.example",
		"GIT_COMMITTER_NAME=alice", "GIT_COMMITTER_EMAIL=alice@laptop.example",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

var sig = &object.Signature{Name: "alice", Email: "alice@laptop.example", When: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)}

// initRepo creates a repository at dir with one commit ("README.md") and
// returns it plus the root commit hash.
func initRepo(t *testing.T, dir string) (*git.Repository, string) {
	t.Helper()
	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{InitOptions: git.InitOptions{DefaultBranch: "refs/heads/main"}})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "README.md"), "hello\n")
	h := commitAll(t, repo, "initial")
	return repo, h
}

func commitAll(t *testing.T, repo *git.Repository, msg string) string {
	t.Helper()
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatal(err)
	}
	h, err := wt.Commit(msg, &git.CommitOptions{Author: sig, Committer: sig, AllowEmptyCommits: true})
	if err != nil {
		t.Fatal(err)
	}
	return h.String()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// addWorktree uses the git CLI to create a linked worktree of main at
// <main>/.worktrees/<name> on a new branch <name>; returns its path.
func addWorktree(t *testing.T, mainDir, name string) string {
	t.Helper()
	w := filepath.Join(mainDir, ".worktrees", name)
	gitCLI(t, mainDir, "worktree", "add", "-b", name, w)
	return w
}

// porcelain returns `git status --porcelain` lines sorted by git.
func porcelain(t *testing.T, dir string) []string {
	t.Helper()
	out := strings.TrimRight(gitCLI(t, dir, "status", "--porcelain"), "\n")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}
```

- [ ] **Step 2: Write the failing tests**

```go
// internal/gitx/inspect_test.go
package gitx

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestInspectNotRepo(t *testing.T) {
	_, err := Inspect(t.TempDir())
	if !errors.Is(err, ErrNotRepo) {
		t.Fatalf("err = %v, want ErrNotRepo", err)
	}
}

func TestInspectMainCheckout(t *testing.T) {
	dir := t.TempDir()
	_, root := initRepo(t, dir)
	info, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Root != dir || info.MainDir != dir || info.CommonDir != filepath.Join(dir, ".git") {
		t.Errorf("paths = %+v", info)
	}
	if info.IsLinked || info.WorktreeName != "" || info.Detached {
		t.Errorf("flags = %+v", info)
	}
	if info.Branch != "main" || info.Head != root || info.RootCommit != root {
		t.Errorf("branch/head/root = %q %q %q, want main %q", info.Branch, info.Head, info.RootCommit, root)
	}
	if len(info.Dirty.Staged)+len(info.Dirty.Modified)+len(info.Dirty.Untracked)+len(info.Dirty.Deleted) != 0 {
		t.Errorf("clean repo reported dirty: %+v", info.Dirty)
	}
}

func TestInspectSubdirFindsRoot(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	writeFile(t, filepath.Join(dir, "a", "b", "c.txt"), "x")
	info, err := Inspect(filepath.Join(dir, "a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Root != dir {
		t.Errorf("Root = %q, want %q", info.Root, dir)
	}
}

func TestInspectLinkedWorktree(t *testing.T) {
	dir := t.TempDir()
	_, root := initRepo(t, dir)
	w := addWorktree(t, dir, "feature")
	other := addWorktree(t, dir, "other")
	info, err := Inspect(w)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsLinked || info.WorktreeName != "feature" {
		t.Errorf("linked/name = %v %q", info.IsLinked, info.WorktreeName)
	}
	if info.Root != w || info.MainDir != dir || info.CommonDir != filepath.Join(dir, ".git") {
		t.Errorf("paths = %+v", info)
	}
	if info.Branch != "feature" || info.RootCommit != root {
		t.Errorf("branch/root = %q %q", info.Branch, info.RootCommit)
	}
	if diff := cmp.Diff([]string{other}, info.OtherWorktrees); diff != "" {
		t.Errorf("OtherWorktrees (-want +got):\n%s", diff)
	}
}

func TestInspectDirtyMatchesGitStatus(t *testing.T) {
	dir := t.TempDir()
	repo, _ := initRepo(t, dir)
	writeFile(t, filepath.Join(dir, "tracked.txt"), "1\n")
	writeFile(t, filepath.Join(dir, "gone.txt"), "2\n")
	commitAll(t, repo, "second")
	writeFile(t, filepath.Join(dir, "tracked.txt"), "changed\n")   // modified
	writeFile(t, filepath.Join(dir, "new.txt"), "new\n")            // untracked
	writeFile(t, filepath.Join(dir, "staged.txt"), "staged\n")      // staged (added)
	gitCLI(t, dir, "add", "staged.txt")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil { // deleted
		t.Fatal(err)
	}
	info, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := Dirty{Staged: []string{"staged.txt"}, Modified: []string{"tracked.txt"}, Untracked: []string{"new.txt"}, Deleted: []string{"gone.txt"}}
	if diff := cmp.Diff(want, info.Dirty); diff != "" {
		t.Errorf("Dirty (-want +got):\n%s", diff)
	}
	// Cross-check: every path git reports is in exactly one of our lists.
	got := map[string]bool{}
	for _, l := range porcelain(t, dir) {
		got[l[3:]] = true
	}
	var ours []string
	ours = append(ours, info.Dirty.Staged...)
	ours = append(ours, info.Dirty.Modified...)
	ours = append(ours, info.Dirty.Untracked...)
	ours = append(ours, info.Dirty.Deleted...)
	sort.Strings(ours)
	var theirs []string
	for p := range got {
		theirs = append(theirs, p)
	}
	sort.Strings(theirs)
	if diff := cmp.Diff(theirs, ours); diff != "" {
		t.Errorf("git status --porcelain vs Inspect (-git +ours):\n%s", diff)
	}
}

func TestInspectDetachedHead(t *testing.T) {
	dir := t.TempDir()
	_, root := initRepo(t, dir)
	gitCLI(t, dir, "checkout", "--detach", root)
	info, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Detached || info.Branch != "" || info.Head != root {
		t.Errorf("detached = %v branch=%q head=%q", info.Detached, info.Branch, info.Head)
	}
}

func TestInspectSubmodules(t *testing.T) {
	sub := t.TempDir()
	initRepo(t, sub)
	dir := t.TempDir()
	repo, _ := initRepo(t, dir)
	gitCLI(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", sub, "vendor/sub")
	commitAll(t, repo, "add submodule")
	info, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"vendor/sub"}, info.Submodules); diff != "" {
		t.Errorf("Submodules (-want +got):\n%s", diff)
	}
	if len(info.DirtySubmodules) != 0 {
		t.Errorf("clean submodule reported dirty: %v", info.DirtySubmodules)
	}
	writeFile(t, filepath.Join(dir, "vendor", "sub", "junk.txt"), "x")
	info, err = Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"vendor/sub"}, info.DirtySubmodules); diff != "" {
		t.Errorf("DirtySubmodules (-want +got):\n%s", diff)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/gitx/ -run TestInspect -v`
Expected: FAIL — `undefined: Inspect`, `undefined: ErrNotRepo`.

- [ ] **Step 4: Implement Inspect**

```go
// internal/gitx/inspect.go
// Package gitx inspects git repositories and worktrees with go-git and
// performs the destination-side attach (spec §8). It never execs git.
package gitx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

type Dirty struct{ Staged, Modified, Untracked, Deleted []string }

type Info struct {
	Root            string   // worktree root (W)
	CommonDir       string   // M/.git
	MainDir         string   // M
	IsLinked        bool
	WorktreeName    string   // basename under .git/worktrees
	Branch          string   // "" if detached
	Head            string   // hex
	Detached        bool
	RootCommit      string
	Dirty           Dirty
	Submodules      []string
	DirtySubmodules []string // subset of Submodules whose checkout is not clean
	OtherWorktrees  []string // absolute paths of other linked worktrees
}

var ErrNotRepo = errors.New("not a git repository")

// findRoot walks up from cwd to the nearest directory containing ".git"
// (a directory for a main checkout, a file for a linked worktree).
func findRoot(cwd string) (root, dotGit string, err error) {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return "", "", err
	}
	for {
		p := filepath.Join(dir, ".git")
		if _, err := os.Lstat(p); err == nil {
			return dir, p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", fmt.Errorf("%s: %w", cwd, ErrNotRepo)
		}
		dir = parent
	}
}

// readGitdirFile parses a ".git" FILE ("gitdir: <path>") and returns the
// absolute path it points at.
func readGitdirFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir:") {
		return "", fmt.Errorf("%s: not a gitdir file: %q", path, line)
	}
	gd := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(filepath.Dir(path), gd)
	}
	return filepath.Clean(gd), nil
}

// Inspect describes the repository containing cwd (spec §8 inventory).
func Inspect(cwd string) (*Info, error) {
	root, dotGit, err := findRoot(cwd)
	if err != nil {
		return nil, err
	}
	info := &Info{Root: root}
	st, err := os.Lstat(dotGit)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		info.CommonDir = dotGit
		info.MainDir = root
	} else {
		gd, err := readGitdirFile(dotGit)
		if err != nil {
			return nil, err
		}
		cd, err := os.ReadFile(filepath.Join(gd, "commondir"))
		if err != nil {
			return nil, fmt.Errorf("read commondir of %s: %w", gd, err)
		}
		common := strings.TrimSpace(string(cd))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gd, common)
		}
		info.CommonDir = filepath.Clean(common)
		info.MainDir = filepath.Dir(info.CommonDir)
		info.IsLinked = true
		info.WorktreeName = filepath.Base(gd)
	}

	repo, err := git.PlainOpenWithOptions(root, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", root, err)
	}
	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("HEAD of %s: %w", root, err)
	}
	info.Head = head.Hash().String()
	if head.Name().IsBranch() {
		info.Branch = head.Name().Short()
	} else {
		info.Detached = true
	}
	rootCommit, err := firstParentRoot(repo, head.Hash())
	if err != nil {
		return nil, err
	}
	info.RootCommit = rootCommit

	wt, err := repo.Worktree()
	if err != nil {
		return nil, err
	}
	status, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("status of %s: %w", root, err)
	}
	info.Dirty = dirtyFromStatus(status)

	subs, err := wt.Submodules()
	if err != nil {
		return nil, fmt.Errorf("submodules of %s: %w", root, err)
	}
	for _, s := range subs {
		info.Submodules = append(info.Submodules, s.Config().Path)
		sst, err := s.Status()
		if err != nil {
			return nil, fmt.Errorf("submodule %s status: %w", s.Config().Path, err)
		}
		if !sst.IsClean() {
			info.DirtySubmodules = append(info.DirtySubmodules, s.Config().Path)
		}
	}
	sort.Strings(info.Submodules)
	sort.Strings(info.DirtySubmodules)

	others, err := linkedWorktrees(info.CommonDir)
	if err != nil {
		return nil, err
	}
	for name, path := range others {
		if name != info.WorktreeName {
			info.OtherWorktrees = append(info.OtherWorktrees, path)
		}
	}
	sort.Strings(info.OtherWorktrees)
	return info, nil
}

// firstParentRoot walks first parents from h to the parentless commit.
func firstParentRoot(repo *git.Repository, h plumbing.Hash) (string, error) {
	c, err := repo.CommitObject(h)
	if err != nil {
		return "", fmt.Errorf("commit %s: %w", h, err)
	}
	for len(c.ParentHashes) > 0 {
		c, err = repo.CommitObject(c.ParentHashes[0])
		if err != nil {
			return "", fmt.Errorf("commit %s: %w", c.Hash, err)
		}
	}
	return c.Hash.String(), nil
}

func dirtyFromStatus(st git.Status) Dirty {
	var d Dirty
	for path, fs := range st {
		switch {
		case fs.Worktree == git.Untracked:
			d.Untracked = append(d.Untracked, path)
			continue
		case fs.Staging == git.Deleted || fs.Worktree == git.Deleted:
			d.Deleted = append(d.Deleted, path)
			continue
		}
		if fs.Staging != git.Unmodified && fs.Staging != git.Untracked {
			d.Staged = append(d.Staged, path)
		}
		if fs.Worktree == git.Modified {
			d.Modified = append(d.Modified, path)
		}
	}
	sort.Strings(d.Staged)
	sort.Strings(d.Modified)
	sort.Strings(d.Untracked)
	sort.Strings(d.Deleted)
	return d
}

// linkedWorktrees maps worktree name -> worktree directory by reading
// <common>/worktrees/<name>/gitdir (which holds "<W>/.git").
func linkedWorktrees(commonDir string) (map[string]string, error) {
	out := map[string]string{}
	entries, err := os.ReadDir(filepath.Join(commonDir, "worktrees"))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		gd, err := os.ReadFile(filepath.Join(commonDir, "worktrees", e.Name(), "gitdir"))
		if err != nil {
			return nil, fmt.Errorf("worktree %s: %w", e.Name(), err)
		}
		out[e.Name()] = filepath.Dir(strings.TrimSpace(string(gd)))
	}
	return out, nil
}
```

- [ ] **Step 5: Add go-git to go.mod and run the tests**

Run: `go get github.com/go-git/go-git/v5@latest && go mod tidy && go test -race ./internal/gitx/ -run TestInspect -v`
Expected: PASS (all six tests). If `TestInspectDirtyMatchesGitStatus` disagrees on a staged+modified file, note that go-git reports it in both `Staging` and `Worktree` and our lists carry it in both `Staged` and `Modified` while porcelain shows one line — the cross-check dedupes via the map, so it must still pass.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/gitx/inspect.go internal/gitx/inspect_test.go internal/gitx/testutil_test.go
git commit -m "feat(gitx): Inspect repository, worktree, dirty state and submodules"
```

---

### Task 2: gitx.DestStateOf and IsAncestor

**Files:**
- Create: `internal/gitx/deststate.go`
- Test: `internal/gitx/deststate_test.go`

**Interfaces:**
- Consumes: `Inspect`, `linkedWorktrees` (Task 1).
- Produces: `DestState` (with addition `BranchTipReachable bool`, set by the caller on the source side), `DestStateOf(mainDir, worktreeDir, branch string) (*DestState, error)`, `IsAncestor(repoDir, ancestor, descendant string) (bool, error)` (addition).

- [ ] **Step 1: Write the failing tests**

```go
// internal/gitx/deststate_test.go
package gitx

import (
	"path/filepath"
	"testing"
)

func TestDestStateAbsent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nope")
	st, err := DestStateOf(dir, filepath.Join(dir, ".worktrees", "x"), "x")
	if err != nil {
		t.Fatal(err)
	}
	if st.MainExists || st.WorktreeExists || st.BranchTip != "" {
		t.Errorf("absent main = %+v", st)
	}
}

func TestDestStateExistingMainWithBranch(t *testing.T) {
	dir := t.TempDir()
	repo, root := initRepo(t, dir)
	writeFile(t, filepath.Join(dir, "b.txt"), "b")
	second := commitAll(t, repo, "second")
	gitCLI(t, dir, "branch", "feature", root)
	st, err := DestStateOf(dir, filepath.Join(dir, ".worktrees", "feature"), "feature")
	if err != nil {
		t.Fatal(err)
	}
	if !st.MainExists || st.RootCommit != root {
		t.Errorf("main/root = %v %q", st.MainExists, st.RootCommit)
	}
	if st.RefTips["refs/heads/main"] != second || st.RefTips["refs/heads/feature"] != root {
		t.Errorf("RefTips = %v", st.RefTips)
	}
	if st.BranchTip != root || st.WorktreeExists || st.BranchCheckedOutElsewhere != "" {
		t.Errorf("state = %+v", st)
	}
}

func TestDestStateBranchCheckedOutElsewhere(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	w := addWorktree(t, dir, "feature")
	st, err := DestStateOf(dir, filepath.Join(dir, "elsewhere"), "feature")
	if err != nil {
		t.Fatal(err)
	}
	if st.BranchCheckedOutElsewhere != w {
		t.Errorf("BranchCheckedOutElsewhere = %q, want %q", st.BranchCheckedOutElsewhere, w)
	}
	// The main checkout counts too.
	st, err = DestStateOf(dir, filepath.Join(dir, "elsewhere"), "main")
	if err != nil {
		t.Fatal(err)
	}
	if st.BranchCheckedOutElsewhere != dir {
		t.Errorf("main branch: BranchCheckedOutElsewhere = %q, want %q", st.BranchCheckedOutElsewhere, dir)
	}
}

func TestDestStateSameDirCleanAndBranch(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	st, err := DestStateOf(dir, dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !st.WorktreeExists || st.WorktreeBranch != "main" || !st.Clean || st.BranchCheckedOutElsewhere != "" {
		t.Errorf("W==M clean: %+v", st)
	}
	writeFile(t, filepath.Join(dir, "dirty.txt"), "x")
	st, err = DestStateOf(dir, dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if st.Clean {
		t.Error("untracked file should make Clean false")
	}
}

func TestIsAncestor(t *testing.T) {
	dir := t.TempDir()
	repo, root := initRepo(t, dir)
	writeFile(t, filepath.Join(dir, "b.txt"), "b")
	second := commitAll(t, repo, "second")
	for _, c := range []struct {
		a, d string
		want bool
	}{{root, second, true}, {second, root, false}, {root, root, true}} {
		got, err := IsAncestor(dir, c.a, c.d)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("IsAncestor(%s,%s) = %v, want %v", c.a[:7], c.d[:7], got, c.want)
		}
	}
	if _, err := IsAncestor(dir, "0000000000000000000000000000000000000000", second); err == nil {
		t.Error("unknown ancestor hash must be an error, not false")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/gitx/ -run 'TestDestState|TestIsAncestor' -v`
Expected: FAIL — `undefined: DestStateOf`, `undefined: IsAncestor`.

- [ ] **Step 3: Implement**

```go
// internal/gitx/deststate.go
package gitx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

type DestState struct {
	MainExists                bool
	RootCommit                string
	RefTips                   map[string]string // refs/heads/x -> hex
	BranchTip                 string            // "" if absent
	WorktreeExists            bool
	WorktreeBranch            string // branch checked out at worktreeDir if it exists
	Clean                     bool   // for W==M case
	BranchCheckedOutElsewhere string // path, if the branch is checked out in another worktree
	// BranchTipReachable is NOT computed by DestStateOf: the orchestrator
	// sets it on the source (IsAncestor(srcMain, BranchTip, Tip)) before
	// PlanTransfer, which needs it for the fast-forward decision.
	BranchTipReachable bool
}

// DestStateOf describes what the destination already has (spec §8).
// mainDir/worktreeDir are destination paths; branch the session branch.
func DestStateOf(mainDir, worktreeDir, branch string) (*DestState, error) {
	st := &DestState{RefTips: map[string]string{}}
	if _, err := os.Stat(filepath.Join(mainDir, ".git")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, err := os.Lstat(worktreeDir); err == nil {
				st.WorktreeExists = true
			}
			return st, nil
		}
		return nil, err
	}
	st.MainExists = true
	repo, err := git.PlainOpenWithOptions(mainDir, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", mainDir, err)
	}
	if head, err := repo.Head(); err == nil {
		rc, err := firstParentRoot(repo, head.Hash())
		if err != nil {
			return nil, err
		}
		st.RootCommit = rc
	} else if !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, fmt.Errorf("HEAD of %s: %w", mainDir, err)
	}
	refs, err := repo.References()
	if err != nil {
		return nil, err
	}
	err = refs.ForEach(func(r *plumbing.Reference) error {
		if r.Name().IsBranch() && r.Type() == plumbing.HashReference {
			st.RefTips[r.Name().String()] = r.Hash().String()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if branch != "" {
		st.BranchTip = st.RefTips["refs/heads/"+branch]
	}

	if _, err := os.Lstat(worktreeDir); err == nil {
		st.WorktreeExists = true
		if wi, err := Inspect(worktreeDir); err == nil && wi.Root == filepath.Clean(worktreeDir) {
			st.WorktreeBranch = wi.Branch
			if wi.Root == wi.MainDir {
				st.Clean = len(wi.Dirty.Staged)+len(wi.Dirty.Modified)+len(wi.Dirty.Untracked)+len(wi.Dirty.Deleted) == 0
			}
		}
	}

	// Where is the branch checked out? The main checkout and every linked
	// worktree other than worktreeDir itself count.
	if branch != "" {
		want := "ref: refs/heads/" + branch
		commonDir := filepath.Join(mainDir, ".git")
		if filepath.Clean(worktreeDir) != filepath.Clean(mainDir) {
			if h, err := os.ReadFile(filepath.Join(commonDir, "HEAD")); err == nil && strings.TrimSpace(string(h)) == want {
				st.BranchCheckedOutElsewhere = filepath.Clean(mainDir)
			}
		}
		others, err := linkedWorktrees(commonDir)
		if err != nil {
			return nil, err
		}
		for name, path := range others {
			if filepath.Clean(path) == filepath.Clean(worktreeDir) {
				continue
			}
			h, err := os.ReadFile(filepath.Join(commonDir, "worktrees", name, "HEAD"))
			if err == nil && strings.TrimSpace(string(h)) == want {
				st.BranchCheckedOutElsewhere = path
			}
		}
	}
	return st, nil
}

// IsAncestor reports whether ancestor is reachable from descendant in the
// repository at repoDir. Both hashes must exist there.
func IsAncestor(repoDir, ancestor, descendant string) (bool, error) {
	repo, err := git.PlainOpenWithOptions(repoDir, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return false, fmt.Errorf("open %s: %w", repoDir, err)
	}
	a, err := repo.CommitObject(plumbing.NewHash(ancestor))
	if err != nil {
		return false, fmt.Errorf("commit %s: %w", ancestor, err)
	}
	d, err := repo.CommitObject(plumbing.NewHash(descendant))
	if err != nil {
		return false, fmt.Errorf("commit %s: %w", descendant, err)
	}
	return a.IsAncestor(d)
}
```

(`a.IsAncestor(d)` is a method on `*object.Commit`; the `object` package is not imported because no identifier from it is named.)

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/gitx/ -run 'TestDestState|TestIsAncestor' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gitx/deststate.go internal/gitx/deststate_test.go
git commit -m "feat(gitx): DestStateOf and IsAncestor"
```

---

### Task 3: gitx.PlanTransfer — the spec §8 decision table

**Files:**
- Create: `internal/gitx/plan.go`
- Test: `internal/gitx/plan_test.go`

**Interfaces:**
- Consumes: `Info`, `DestState`, `session.PathMap` (`ApplyPath`).
- Produces: `Mode`, `ModeNotRepo/ModeFreshMain/ModeExistingMain`, `Plan` (with additions `IndexRel string`, `StagedBlobs []string`, `PackEntryID int`, `IndexEntryID int`, `DirtyEntries map[string]int`), `RefuseError`, `PlanTransfer(src *Info, dst *DestState, pm session.PathMap) (*Plan, error)`.

- [ ] **Step 1: Write the failing tests (table-driven over the decision table)**

```go
// internal/gitx/plan_test.go
package gitx

import (
	"errors"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/session"
)

const (
	rootA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rootB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tip   = "cccccccccccccccccccccccccccccccccccccccc"
	older = "dddddddddddddddddddddddddddddddddddddddd"
)

func linkedInfo() *Info {
	return &Info{Root: "/home/alice/github/x/.worktrees/feat", CommonDir: "/home/alice/github/x/.git", MainDir: "/home/alice/github/x",
		IsLinked: true, WorktreeName: "feat", Branch: "feat", Head: tip, RootCommit: rootA,
		Dirty: Dirty{Modified: []string{"a.go"}, Untracked: []string{"b.go"}}}
}

func mainInfo() *Info {
	return &Info{Root: "/home/alice/github/x", CommonDir: "/home/alice/github/x/.git", MainDir: "/home/alice/github/x",
		Branch: "main", Head: tip, RootCommit: rootA}
}

func TestPlanTransferTable(t *testing.T) {
	pm := session.NewPathMap(session.Mapping{From: "/home/alice", To: "/home/bob"})
	cases := []struct {
		name    string
		src     *Info
		dst     *DestState
		mode    Mode
		ff      bool
		pack    bool
		refuse  string // substring of RefuseError.Reason, "" = no refusal
	}{
		{"not a repo", nil, &DestState{}, ModeNotRepo, false, false, ""},
		{"fresh main, linked", linkedInfo(), &DestState{}, ModeFreshMain, false, false, ""},
		{"fresh main, W exists", linkedInfo(), &DestState{WorktreeExists: true}, "", false, false, "already exists"},
		{"existing main, branch absent", linkedInfo(), &DestState{MainExists: true, RootCommit: rootA, RefTips: map[string]string{"refs/heads/main": older}}, ModeExistingMain, false, true, ""},
		{"existing main, different root", linkedInfo(), &DestState{MainExists: true, RootCommit: rootB}, "", false, false, "different repository"},
		{"existing main, branch at tip", linkedInfo(), &DestState{MainExists: true, RootCommit: rootA, BranchTip: tip, RefTips: map[string]string{"refs/heads/feat": tip}}, ModeExistingMain, false, false, ""},
		{"existing main, fast-forward", linkedInfo(), &DestState{MainExists: true, RootCommit: rootA, BranchTip: older, BranchTipReachable: true, RefTips: map[string]string{"refs/heads/feat": older}}, ModeExistingMain, true, true, ""},
		{"existing main, diverged", linkedInfo(), &DestState{MainExists: true, RootCommit: rootA, BranchTip: older, BranchTipReachable: false}, "", false, false, "not a fast-forward"},
		{"existing main, W exists", linkedInfo(), &DestState{MainExists: true, RootCommit: rootA, WorktreeExists: true}, "", false, false, "already exists"},
		{"existing main, branch elsewhere", linkedInfo(), &DestState{MainExists: true, RootCommit: rootA, BranchCheckedOutElsewhere: "/home/bob/github/x/.worktrees/old"}, "", false, false, "checked out"},
		{"W==M clean same branch", mainInfo(), &DestState{MainExists: true, RootCommit: rootA, WorktreeExists: true, WorktreeBranch: "main", Clean: true, BranchTip: older, BranchTipReachable: true}, ModeExistingMain, true, true, ""},
		{"W==M dirty", mainInfo(), &DestState{MainExists: true, RootCommit: rootA, WorktreeExists: true, WorktreeBranch: "main", Clean: false, BranchTip: tip}, "", false, false, "not clean"},
		{"W==M other branch", mainInfo(), &DestState{MainExists: true, RootCommit: rootA, WorktreeExists: true, WorktreeBranch: "dev", Clean: true, BranchTip: tip}, "", false, false, "checked out"},
		{"dirty submodule", func() *Info { i := linkedInfo(); i.Submodules = []string{"v/s"}; i.DirtySubmodules = []string{"v/s"}; return i }(), &DestState{}, "", false, false, "submodule"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := PlanTransfer(c.src, c.dst, pm)
			if c.refuse != "" {
				var re *RefuseError
				if !errors.As(err, &re) {
					t.Fatalf("err = %v, want *RefuseError", err)
				}
				if !containsFold(re.Reason, c.refuse) {
					t.Fatalf("Reason = %q, want it to mention %q", re.Reason, c.refuse)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.Mode != c.mode || p.FastForward != c.ff || p.NeedPack != c.pack {
				t.Errorf("mode/ff/pack = %v/%v/%v, want %v/%v/%v", p.Mode, p.FastForward, p.NeedPack, c.mode, c.ff, c.pack)
			}
			if c.src != nil {
				if p.DstMain != "/home/bob/github/x" {
					t.Errorf("DstMain = %q", p.DstMain)
				}
				if c.src.IsLinked && p.DstWorktree != "/home/bob/github/x/.worktrees/feat" {
					t.Errorf("DstWorktree = %q", p.DstWorktree)
				}
				if c.src.IsLinked && p.IndexRel != ".git/worktrees/feat/index" {
					t.Errorf("IndexRel = %q", p.IndexRel)
				}
				if !c.src.IsLinked && p.IndexRel != ".git/index" {
					t.Errorf("IndexRel = %q", p.IndexRel)
				}
			}
		})
	}
}

func TestPlanTransferHaveTipsDeduped(t *testing.T) {
	p, err := PlanTransfer(linkedInfo(), &DestState{MainExists: true, RootCommit: rootA,
		RefTips: map[string]string{"refs/heads/main": older, "refs/heads/dev": older, "refs/heads/z": rootA}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.HaveTips) != 2 {
		t.Errorf("HaveTips = %v, want 2 distinct hashes", p.HaveTips)
	}
}

func containsFold(s, sub string) bool {
	return len(sub) == 0 || len(s) >= len(sub) && (stringsIndexFold(s, sub) >= 0)
}

func stringsIndexFold(s, sub string) int {
	ls, lsub := []rune(s), []rune(sub)
	for i := 0; i+len(lsub) <= len(ls); i++ {
		match := true
		for j := range lsub {
			a, b := ls[i+j], lsub[j]
			if 'A' <= a && a <= 'Z' {
				a += 'a' - 'A'
			}
			if 'A' <= b && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/gitx/ -run TestPlanTransfer -v`
Expected: FAIL — `undefined: PlanTransfer`.

- [ ] **Step 3: Implement**

```go
// internal/gitx/plan.go
package gitx

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/session"
)

type Mode string

const (
	ModeNotRepo      Mode = "not-repo"
	ModeFreshMain    Mode = "fresh-main"    // M absent: transfer everything
	ModeExistingMain Mode = "existing-main" // M present: pack + attach
)

type Plan struct {
	Mode                 Mode
	SrcMain, SrcWorktree string
	DstMain, DstWorktree string
	Linked               bool
	WorktreeName         string
	Branch               string
	Tip                  string
	Detached             bool
	NeedPack             bool
	HaveTips             []string // destination tips to exclude from the pack
	FastForward          bool     // branch exists on dest and is an ancestor of Tip
	Dirty                Dirty

	// Additions (Plan 03):
	IndexRel     string         // ".git/index" or ".git/worktrees/<n>/index", relative to SrcMain/DstMain
	StagedBlobs  []string       // blob hashes referenced by the index but not by Tip (filled by StagedBlobsOf)
	PackEntryID  int            // manifest entry id of the pack, 0 = none (existing-main only)
	IndexEntryID int            // manifest entry id of the index file (existing-main only)
	DirtyEntries map[string]int // dst path -> manifest entry id of dirty worktree files (existing-main only)
}

type RefuseError struct{ Reason string }

func (e *RefuseError) Error() string { return "refused: " + e.Reason }

func refuse(format string, a ...any) error { return &RefuseError{Reason: fmt.Sprintf(format, a...)} }

// PlanTransfer decides the mode or returns a *RefuseError (spec §8).
// src == nil means the session cwd is not a repository; the caller fills
// SrcWorktree/DstWorktree from the session cwd in that case.
func PlanTransfer(src *Info, dst *DestState, pm session.PathMap) (*Plan, error) {
	if src == nil {
		return &Plan{Mode: ModeNotRepo}, nil
	}
	if len(src.DirtySubmodules) > 0 {
		return nil, refuse("submodule(s) with uncommitted changes: %s", strings.Join(src.DirtySubmodules, ", "))
	}
	p := &Plan{
		SrcMain: src.MainDir, SrcWorktree: src.Root,
		DstMain: pm.ApplyPath(src.MainDir), DstWorktree: pm.ApplyPath(src.Root),
		Linked: src.IsLinked, WorktreeName: src.WorktreeName,
		Branch: src.Branch, Tip: src.Head, Detached: src.Detached, Dirty: src.Dirty,
		IndexRel: ".git/index",
	}
	if src.IsLinked {
		p.IndexRel = path.Join(".git/worktrees", src.WorktreeName, "index")
	}
	if !dst.MainExists {
		if dst.WorktreeExists {
			return nil, refuse("destination worktree directory %s already exists", p.DstWorktree)
		}
		p.Mode = ModeFreshMain
		return p, nil
	}
	p.Mode = ModeExistingMain
	if dst.RootCommit != src.RootCommit {
		return nil, refuse("%s on the destination is a different repository (root commit %s, source %s)", p.DstMain, short(dst.RootCommit), short(src.RootCommit))
	}
	seen := map[string]bool{}
	for _, h := range dst.RefTips {
		if !seen[h] {
			seen[h] = true
			p.HaveTips = append(p.HaveTips, h)
		}
	}
	sort.Strings(p.HaveTips)
	switch {
	case src.Detached:
		p.NeedPack = !seen[src.Head]
	case dst.BranchTip == "":
		p.NeedPack = true
	case dst.BranchTip == src.Head:
		p.NeedPack = false
	case dst.BranchTipReachable:
		p.FastForward, p.NeedPack = true, true
	default:
		return nil, refuse("branch %s on the destination (%s) is not a fast-forward of the source (%s)", src.Branch, short(dst.BranchTip), short(src.Head))
	}
	if src.IsLinked {
		if dst.WorktreeExists {
			return nil, refuse("destination worktree directory %s already exists", p.DstWorktree)
		}
		if dst.BranchCheckedOutElsewhere != "" {
			return nil, refuse("branch %s is already checked out on the destination at %s", src.Branch, dst.BranchCheckedOutElsewhere)
		}
		return p, nil
	}
	// W == M
	if !dst.WorktreeExists {
		return nil, refuse("destination main checkout %s has no working tree", p.DstMain)
	}
	if dst.WorktreeBranch != src.Branch {
		return nil, refuse("destination checkout %s has branch %q checked out, session branch is %q", p.DstMain, dst.WorktreeBranch, src.Branch)
	}
	if !dst.Clean {
		return nil, refuse("destination checkout %s is not clean", p.DstMain)
	}
	return p, nil
}

func short(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/gitx/ -run TestPlanTransfer -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gitx/plan.go internal/gitx/plan_test.go
git commit -m "feat(gitx): PlanTransfer implements the spec §8 decision table"
```

---

### Task 4: gitx.Files — what to move for each mode

**Files:**
- Create: `internal/gitx/files.go`
- Test: `internal/gitx/files_test.go`

**Interfaces:**
- Consumes: `Plan` (Task 3), `session.FileEntry`, `session.CatRepo`, `session.CatWorktree`.
- Produces: `Files(p *Plan, excludes []string, includeIgnored bool) ([]session.FileEntry, error)`.

Rules (spec §8): fresh-main walks `SrcMain` (category `repo` for `.git/**` and for all of a non-linked checkout; category `worktree` for files under `SrcWorktree` when linked) excluding every `OtherWorktrees` directory, `<common>/worktrees/<other>/`, `--exclude` globs (matched with `path.Match` against the slash path relative to the walk root), and gitignored files (unless `includeIgnored`; in-repo `.gitignore` files and `.git/info/exclude` only). existing-main returns only the dirty files (`Staged`, `Modified`, `Untracked`, never `Deleted`) rooted at `SrcWorktree` plus the index file rooted at `SrcMain` (`Rel = p.IndexRel`). not-repo walks `SrcWorktree` as `worktree` with excludes only.

- [ ] **Step 1: Write the failing tests**

```go
// internal/gitx/files_test.go
package gitx

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mithro/go-claude-teleport/internal/session"
)

func rels(entries []session.FileEntry, cat session.Category) []string {
	var out []string
	for _, e := range entries {
		if cat == "" || e.Category == cat {
			out = append(out, e.Rel)
		}
	}
	sort.Strings(out)
	return out
}

func has(entries []session.FileEntry, rel string) bool {
	for _, e := range entries {
		if e.Rel == rel {
			return true
		}
	}
	return false
}

func TestFilesFreshMainLinked(t *testing.T) {
	dir := t.TempDir()
	repo, _ := initRepo(t, dir)
	writeFile(t, filepath.Join(dir, ".gitignore"), "*.log\nbuild/\n")
	commitAll(t, repo, "ignore")
	w := addWorktree(t, dir, "feat")
	addWorktree(t, dir, "other")
	writeFile(t, filepath.Join(w, "src.go"), "package x")
	writeFile(t, filepath.Join(w, "debug.log"), "ignored")
	writeFile(t, filepath.Join(w, "build", "out.bin"), "ignored dir")
	writeFile(t, filepath.Join(w, "secret.pem"), "excluded by glob")
	writeFile(t, filepath.Join(dir, "main.log"), "ignored in main")

	info, err := Inspect(w)
	if err != nil {
		t.Fatal(err)
	}
	p, err := PlanTransfer(info, &DestState{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := Files(p, []string{"*.pem"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range files {
		if e.Root != dir && e.Root != w {
			t.Errorf("entry %q has unexpected root %q", e.Rel, e.Root)
		}
	}
	// Main checkout content and git metadata travel as "repo".
	for _, want := range []string{"README.md", ".git/HEAD", ".git/config", ".git/worktrees/feat/HEAD", ".git/worktrees/feat/index", ".git/worktrees/feat/gitdir", ".git/worktrees/feat/commondir"} {
		if !has(files, want) {
			t.Errorf("missing repo entry %q; have %v", want, rels(files, session.CatRepo))
		}
	}
	// Our worktree travels as "worktree", including its .git file.
	if diff := cmp.Diff([]string{"", ".git", "README.md", "src.go"}, rels(files, session.CatWorktree)); diff != "" {
		t.Errorf("worktree entries (-want +got):\n%s", diff)
	}
	// Excluded: other worktree dir + metadata, ignored files, glob.
	for _, no := range []string{".worktrees/other", ".worktrees/other/README.md", ".git/worktrees/other/HEAD", "main.log", "debug.log", "build", "build/out.bin", "secret.pem"} {
		if has(files, no) {
			t.Errorf("entry %q must not be transferred", no)
		}
	}
	// includeIgnored brings the ignored files back but never other worktrees.
	files, err = Files(p, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !has(files, "debug.log") || !has(files, "build/out.bin") || has(files, ".worktrees/other/README.md") {
		t.Errorf("includeIgnored: %v", rels(files, ""))
	}
}

func TestFilesEntryMetadata(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	if err := os.Symlink("README.md", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "README.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	info, _ := Inspect(dir)
	p, _ := PlanTransfer(info, &DestState{}, nil)
	files, err := Files(p, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range files {
		switch e.Rel {
		case "link":
			if e.Symlink != "README.md" || e.Mode&os.ModeSymlink == 0 {
				t.Errorf("symlink entry = %+v", e)
			}
		case "README.md":
			if e.Mode.Perm() != 0o755 || e.Size != 6 || e.Category != session.CatRepo || e.Rewrite {
				t.Errorf("README entry = %+v", e)
			}
		}
	}
}

func TestFilesExistingMainOnlyDirtyPlusIndex(t *testing.T) {
	dir := t.TempDir()
	repo, _ := initRepo(t, dir)
	writeFile(t, filepath.Join(dir, "gone.txt"), "x")
	commitAll(t, repo, "second")
	w := addWorktree(t, dir, "feat")
	writeFile(t, filepath.Join(w, "README.md"), "modified\n")
	writeFile(t, filepath.Join(w, "new.txt"), "untracked\n")
	writeFile(t, filepath.Join(w, "staged.txt"), "staged\n")
	gitCLI(t, w, "add", "staged.txt")
	if err := os.Remove(filepath.Join(w, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	info, _ := Inspect(w)
	p, err := PlanTransfer(info, &DestState{MainExists: true, RootCommit: info.RootCommit, RefTips: map[string]string{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := Files(p, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"README.md", "new.txt", "staged.txt"}, rels(files, session.CatWorktree)); diff != "" {
		t.Errorf("dirty worktree entries (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{".git/worktrees/feat/index"}, rels(files, session.CatRepo)); diff != "" {
		t.Errorf("repo entries (-want +got):\n%s", diff)
	}
	for _, e := range files {
		if e.Category == session.CatWorktree && e.Root != w || e.Category == session.CatRepo && e.Root != dir {
			t.Errorf("bad root for %+v", e)
		}
	}
}

func TestFilesNotRepo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "notes.txt"), "plain")
	writeFile(t, filepath.Join(dir, "skip.tmp"), "excluded")
	p := &Plan{Mode: ModeNotRepo, SrcWorktree: dir, DstWorktree: dir}
	files, err := Files(p, []string{"*.tmp"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"", "notes.txt"}, rels(files, session.CatWorktree)); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/gitx/ -run TestFiles -v`
Expected: FAIL — `undefined: Files`.

- [ ] **Step 3: Implement**

```go
// internal/gitx/files.go
package gitx

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// walkOptions controls one directory walk.
type walkOptions struct {
	root      string
	category  session.Category
	skipDirs  map[string]bool // absolute dirs never entered
	excludes  []string        // path.Match patterns on the slash-relative path
	matcher   gitignore.Matcher // nil = no ignore handling
	worktree  string           // when set, entries under it get CatWorktree
}

// Files lists what to move for the plan (spec §8).
func Files(p *Plan, excludes []string, includeIgnored bool) ([]session.FileEntry, error) {
	switch p.Mode {
	case ModeNotRepo:
		return walk(walkOptions{root: p.SrcWorktree, category: session.CatWorktree, excludes: excludes})
	case ModeFreshMain:
		return filesFreshMain(p, excludes, includeIgnored)
	case ModeExistingMain:
		return filesExistingMain(p, excludes)
	}
	return nil, fmt.Errorf("gitx.Files: unknown mode %q", p.Mode)
}

func filesFreshMain(p *Plan, excludes []string, includeIgnored bool) ([]session.FileEntry, error) {
	info, err := Inspect(p.SrcWorktree)
	if err != nil {
		return nil, err
	}
	skip := map[string]bool{}
	for _, o := range info.OtherWorktrees {
		skip[o] = true
	}
	others, err := linkedWorktrees(info.CommonDir)
	if err != nil {
		return nil, err
	}
	for name := range others {
		if name != info.WorktreeName {
			skip[filepath.Join(info.CommonDir, "worktrees", name)] = true
		}
	}
	var mainMatcher gitignore.Matcher
	if !includeIgnored {
		if mainMatcher, err = ignoreMatcher(p.SrcMain); err != nil {
			return nil, err
		}
	}
	entries, err := walk(walkOptions{root: p.SrcMain, category: session.CatRepo, skipDirs: skip, excludes: excludes, matcher: mainMatcher})
	if err != nil {
		return nil, err
	}
	if !p.Linked {
		return entries, nil
	}
	// A linked worktree usually lives under M/.worktrees/<n> and was just
	// walked as part of M — unless it lives elsewhere. Either way, list it
	// separately as CatWorktree with its own ignore rules, and drop any
	// duplicates from the main walk.
	var wtMatcher gitignore.Matcher
	if !includeIgnored {
		if wtMatcher, err = ignoreMatcher(p.SrcWorktree); err != nil {
			return nil, err
		}
	}
	wt, err := walk(walkOptions{root: p.SrcWorktree, category: session.CatWorktree, excludes: excludes, matcher: wtMatcher})
	if err != nil {
		return nil, err
	}
	var out []session.FileEntry
	for _, e := range entries {
		abs := e.Path()
		if abs == p.SrcWorktree || strings.HasPrefix(abs, p.SrcWorktree+string(filepath.Separator)) {
			continue
		}
		out = append(out, e)
	}
	return append(out, wt...), nil
}

func filesExistingMain(p *Plan, excludes []string) ([]session.FileEntry, error) {
	var rel []string
	rel = append(rel, p.Dirty.Staged...)
	rel = append(rel, p.Dirty.Modified...)
	rel = append(rel, p.Dirty.Untracked...)
	sort.Strings(rel)
	var out []session.FileEntry
	seen := map[string]bool{}
	for _, r := range rel {
		if seen[r] || excluded(r, excludes) {
			continue
		}
		seen[r] = true
		e, err := entryFor(p.SrcWorktree, r, session.CatWorktree)
		if err != nil {
			if os.IsNotExist(err) {
				continue // staged then deleted from disk: nothing to carry
			}
			return nil, err
		}
		out = append(out, e)
	}
	idx, err := entryFor(p.SrcMain, p.IndexRel, session.CatRepo)
	if err != nil {
		return nil, fmt.Errorf("index file: %w", err)
	}
	return append(out, idx), nil
}

// ignoreMatcher reads every .gitignore under root plus .git/info/exclude.
func ignoreMatcher(root string) (gitignore.Matcher, error) {
	fsys := osfs.New(root)
	ps, err := gitignore.ReadPatterns(fsys, nil)
	if err != nil {
		return nil, fmt.Errorf("read .gitignore under %s: %w", root, err)
	}
	return gitignore.NewMatcher(ps), nil
}

func excluded(rel string, globs []string) bool {
	for _, g := range globs {
		if ok, _ := path.Match(g, rel); ok {
			return true
		}
		if ok, _ := path.Match(g, path.Base(rel)); ok {
			return true
		}
	}
	return false
}

func entryFor(root, rel string, cat session.Category) (session.FileEntry, error) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	st, err := os.Lstat(abs)
	if err != nil {
		return session.FileEntry{}, err
	}
	e := session.FileEntry{Root: root, Rel: rel, Category: cat, Size: st.Size(), Mode: st.Mode(), ModTime: st.ModTime()}
	if st.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(abs)
		if err != nil {
			return session.FileEntry{}, err
		}
		e.Symlink = target
		e.Size = 0
	}
	if st.IsDir() {
		e.Size = 0
	}
	return e, nil
}

func walk(o walkOptions) ([]session.FileEntry, error) {
	var out []session.FileEntry
	err := filepath.WalkDir(o.root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if o.skipDirs[abs] {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(o.root, abs)
		if err != nil {
			return err
		}
		if rel == "." {
			rel = ""
		}
		rel = filepath.ToSlash(rel)
		if rel != "" {
			if excluded(rel, o.excludes) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			inGit := rel == ".git" || strings.HasPrefix(rel, ".git/")
			if o.matcher != nil && !inGit && o.matcher.Match(strings.Split(rel, "/"), d.IsDir()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		e, err := entryFor(o.root, rel, o.category)
		if err != nil {
			return err
		}
		if e.Mode&(os.ModeSocket|os.ModeNamedPipe|os.ModeDevice|os.ModeCharDevice) != 0 {
			return nil // spec §7.1: sockets, fifos, devices are skipped (listed by the manifest builder)
		}
		out = append(out, e)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", o.root, err)
	}
	return out, nil
}
```

`go-billy` is an existing transitive dependency of go-git (`go mod tidy` promotes it to a direct requirement; that is not a new dependency).

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/gitx/ -run TestFiles -v`
Expected: PASS. If `TestFilesFreshMainLinked` reports `.worktrees/feat/...` entries duplicated as `repo`, the de-duplication loop in `filesFreshMain` is comparing against an unclean path — `Inspect` returns cleaned absolute paths; make sure `p.SrcWorktree` is the same string (the test passes `info.Root`).

- [ ] **Step 5: Commit**

```bash
git add internal/gitx/files.go internal/gitx/files_test.go
git commit -m "feat(gitx): Files lists repo/worktree entries per transfer mode"
```

---

### Task 5: gitx.WritePack and StagedBlobsOf

**Files:**
- Create: `internal/gitx/pack.go`
- Test: `internal/gitx/pack_test.go`

**Interfaces:**
- Consumes: `Plan.StagedBlobs`, `Plan.HaveTips`, `Plan.Tip`.
- Produces: `WritePack(ctx context.Context, repoDir string, want []string, have []string, w io.Writer) error` (writes zero bytes when nothing is missing); `StagedBlobsOf(repoDir, indexPath, tip string) ([]string, error)` (addition: blobs the index references that the tip's tree does not — staged-but-uncommitted content must ride in the pack or `git diff --cached` on the destination cannot show it).

- [ ] **Step 1: Write the failing tests**

```go
// internal/gitx/pack_test.go
package gitx

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
)

func TestWritePackMissingObjectsOnly(t *testing.T) {
	src := t.TempDir()
	repo, root := initRepo(t, src)
	writeFile(t, filepath.Join(src, "b.txt"), "b\n")
	second := commitAll(t, repo, "second")
	writeFile(t, filepath.Join(src, "c.txt"), "c\n")
	third := commitAll(t, repo, "third")

	// Destination has only the root commit.
	dst := t.TempDir()
	initRepoAt(t, dst, src, root)

	var buf bytes.Buffer
	if err := WritePack(context.Background(), src, []string{third}, []string{root}, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected a non-empty pack")
	}
	dstRepo, err := git.PlainOpen(dst)
	if err != nil {
		t.Fatal(err)
	}
	if err := packfile.UpdateObjectStorage(dstRepo.Storer, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{second, third} {
		if _, err := dstRepo.CommitObject(plumbing.NewHash(h)); err != nil {
			t.Errorf("commit %s missing on dest after pack: %v", h[:7], err)
		}
	}
	gitCLI(t, dst, "cat-file", "-e", third) // the real git can read it too
	gitCLI(t, dst, "fsck", "--no-dangling")
}

func TestWritePackNothingMissingWritesNothing(t *testing.T) {
	src := t.TempDir()
	_, root := initRepo(t, src)
	var buf bytes.Buffer
	if err := WritePack(context.Background(), src, []string{root}, []string{root}, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected zero bytes, got %d", buf.Len())
	}
}

func TestWritePackIgnoresUnknownHaveTips(t *testing.T) {
	src := t.TempDir()
	_, root := initRepo(t, src)
	var buf bytes.Buffer
	// A destination-only commit the source has never seen must not error.
	if err := WritePack(context.Background(), src, []string{root}, []string{"1111111111111111111111111111111111111111"}, &buf); err != nil {
		t.Fatal(err)
	}
}

func TestStagedBlobsOf(t *testing.T) {
	src := t.TempDir()
	_, root := initRepo(t, src)
	writeFile(t, filepath.Join(src, "staged.txt"), "staged content\n")
	gitCLI(t, src, "add", "staged.txt")
	blobs, err := StagedBlobsOf(src, filepath.Join(src, ".git", "index"), root)
	if err != nil {
		t.Fatal(err)
	}
	want := gitCLI(t, src, "hash-object", "staged.txt")
	if len(blobs) != 1 || blobs[0] != want[:40] {
		t.Errorf("StagedBlobs = %v, want [%s]", blobs, want[:40])
	}
	// The pack for tip+staged carries the staged blob.
	var buf bytes.Buffer
	if err := WritePack(context.Background(), src, append([]string{root}, blobs...), []string{root}, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Error("pack with a staged blob must not be empty")
	}
}

// initRepoAt creates a destination clone of src at dir, truncated to commit
// `at` (a shared history prefix), using the git CLI (tests only).
func initRepoAt(t *testing.T, dir, src, at string) {
	t.Helper()
	gitCLI(t, filepath.Dir(dir), "clone", "-q", src, dir)
	gitCLI(t, dir, "reset", "-q", "--hard", at)
	gitCLI(t, dir, "reflog", "expire", "--expire=now", "--all")
	gitCLI(t, dir, "gc", "-q", "--prune=now")
	gitCLI(t, dir, "remote", "remove", "origin")
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/gitx/ -run 'TestWritePack|TestStagedBlobs' -v`
Expected: FAIL — `undefined: WritePack`, `undefined: StagedBlobsOf`.

- [ ] **Step 3: Implement**

```go
// internal/gitx/pack.go
package gitx

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/revlist"
)

// WritePack encodes the objects reachable from want but not from have into
// w as a packfile. Hashes in have that the repository does not contain are
// skipped (they are destination-only commits). When nothing is missing,
// nothing is written.
func WritePack(ctx context.Context, repoDir string, want []string, have []string, w io.Writer) error {
	repo, err := git.PlainOpenWithOptions(repoDir, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return fmt.Errorf("open %s: %w", repoDir, err)
	}
	s := repo.Storer
	var wantH, haveH []plumbing.Hash
	for _, h := range want {
		wantH = append(wantH, plumbing.NewHash(h))
	}
	for _, h := range have {
		ph := plumbing.NewHash(h)
		if err := s.HasEncodedObject(ph); err == nil {
			haveH = append(haveH, ph)
		}
	}
	hashes, err := revlist.Objects(s, wantH, haveH)
	if err != nil {
		return fmt.Errorf("revlist %s: %w", repoDir, err)
	}
	if len(hashes) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	enc := packfile.NewEncoder(w, s, false)
	if _, err := enc.Encode(hashes, 10); err != nil {
		return fmt.Errorf("encode pack (%d objects): %w", len(hashes), err)
	}
	return nil
}

// StagedBlobsOf returns the blob hashes in the index at indexPath that are
// not present in the tree of commit tip, sorted. repoDir is M (objects).
func StagedBlobsOf(repoDir, indexPath, tip string) ([]string, error) {
	f, err := os.Open(indexPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	idx := &index.Index{}
	if err := index.NewDecoder(f).Decode(idx); err != nil {
		return nil, fmt.Errorf("decode %s: %w", indexPath, err)
	}
	repo, err := git.PlainOpenWithOptions(repoDir, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", repoDir, err)
	}
	inTree := map[plumbing.Hash]bool{}
	if tip != "" {
		c, err := repo.CommitObject(plumbing.NewHash(tip))
		if err != nil {
			return nil, fmt.Errorf("commit %s: %w", tip, err)
		}
		tree, err := c.Tree()
		if err != nil {
			return nil, err
		}
		err = tree.Files().ForEach(func(f *object.File) error { inTree[f.Hash] = true; return nil })
		if err != nil {
			return nil, err
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range idx.Entries {
		if inTree[e.Hash] || seen[e.Hash.String()] {
			continue
		}
		seen[e.Hash.String()] = true
		out = append(out, e.Hash.String())
	}
	sort.Strings(out)
	return out, nil
}

// SourceFacts is what the source repository must answer before the plan
// can be made (it is computed on the source host by remote.GitSourceFacts).
type SourceFacts struct {
	DestTipReachable bool     // destTip is an ancestor of tip ("" destTip -> false)
	StagedBlobs      []string // StagedBlobsOf(mainDir, mainDir/indexRel, tip)
}

// SourceFactsOf combines IsAncestor and StagedBlobsOf. A destTip the
// source does not have is simply "not reachable" (a diverged branch).
func SourceFactsOf(mainDir, indexRel, tip, destTip string) (*SourceFacts, error) {
	f := &SourceFacts{}
	if destTip != "" && destTip != tip {
		ok, err := IsAncestor(mainDir, destTip, tip)
		if err == nil {
			f.DestTipReachable = ok
		} else if !errors.Is(err, plumbing.ErrObjectNotFound) {
			return nil, err
		}
	}
	if destTip == tip {
		f.DestTipReachable = true
	}
	blobs, err := StagedBlobsOf(mainDir, filepath.Join(mainDir, filepath.FromSlash(indexRel)), tip)
	if err != nil {
		return nil, err
	}
	f.StagedBlobs = blobs
	return f, nil
}
```

Add `"errors"` and `"path/filepath"` to pack.go's imports. Add to `pack_test.go`:

```go
func TestSourceFactsOfDivergedDestTipIsUnreachable(t *testing.T) {
	src := t.TempDir()
	repo, root := initRepo(t, src)
	writeFile(t, filepath.Join(src, "b.txt"), "b\n")
	second := commitAll(t, repo, "second")
	f, err := SourceFactsOf(src, ".git/index", second, root)
	if err != nil || !f.DestTipReachable {
		t.Fatalf("ancestor dest tip: %+v %v", f, err)
	}
	f, err = SourceFactsOf(src, ".git/index", second, "2222222222222222222222222222222222222222")
	if err != nil || f.DestTipReachable {
		t.Fatalf("unknown dest tip must be unreachable, not an error: %+v %v", f, err)
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/gitx/ -run 'TestWritePack|TestStagedBlobs' -v`
Expected: PASS. `git fsck` on the destination proves the pack is readable by the real git.

- [ ] **Step 5: Commit**

```bash
git add internal/gitx/pack.go internal/gitx/pack_test.go
git commit -m "feat(gitx): WritePack of missing objects and StagedBlobsOf"
```

---

### Task 6: gitx.Attach — destination side

**Files:**
- Create: `internal/gitx/attach.go`
- Test: `internal/gitx/attach_test.go`

**Interfaces:**
- Consumes: `Plan`, `DestStateOf`, `IsAncestor`.
- Produces: `Attach(ctx context.Context, p *Plan, packPath string, dirtyFiles map[string]string) error`. `dirtyFiles` maps destination path → staged file; the key equal to `filepath.Join(p.DstMain, p.IndexRel)` is the transferred index.

Behaviour (spec §8):
- **fresh-main, linked**: write `DstWorktree/.git` = `gitdir: <DstMain>/.git/worktrees/<name>\n` and `<DstMain>/.git/worktrees/<name>/gitdir` = `<DstWorktree>/.git\n`. Idempotent (same content → no-op). Non-linked: nothing.
- **existing-main**: index the pack (if `packPath != ""` and non-empty); create or fast-forward `refs/heads/<Branch>` (CheckAndSet against the tip recorded in `HaveTips`/`FastForward`; refuse if it moved since preflight); if linked: create `<DstMain>/.git/worktrees/<name>/{HEAD,commondir,gitdir}` + `DstWorktree/.git`, then hard-reset the new worktree to `Tip` (go-git populates index and files) — skipped when the worktree metadata already exists with an `index` (a re-run); if `W==M`: re-verify clean + branch, hard reset to `Tip` if HEAD differs (a fast-forward on a clean tree); then copy the transferred index over `<gitdir>/index` and every other `dirtyFiles` entry into place (mkdir parents, preserve the staged file's mode). Never deletes.

- [ ] **Step 1: Write the failing tests**

```go
// internal/gitx/attach_test.go
package gitx

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// copyTree copies src to dst recursively (tests only) to simulate the tar
// transfer of a fresh-main teleport.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			l, _ := os.Readlink(p)
			return os.Symlink(l, target)
		default:
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, b, info.Mode().Perm())
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAttachFreshMainRepairsWorktreeMetadata(t *testing.T) {
	srcHome := filepath.Join(t.TempDir(), "home", "alice")
	dstHome := filepath.Join(t.TempDir(), "home", "bob")
	srcMain := filepath.Join(srcHome, "x")
	initRepo(t, srcMain)
	w := addWorktree(t, srcMain, "feat")
	writeFile(t, filepath.Join(w, "staged.txt"), "s")
	gitCLI(t, w, "add", "staged.txt")
	writeFile(t, filepath.Join(w, "untracked.txt"), "u")

	dstMain := filepath.Join(dstHome, "x")
	copyTree(t, srcMain, dstMain) // as the tar transfer would (metadata still points at srcHome)

	info, _ := Inspect(w)
	pm := pathMap(srcHome, dstHome)
	p, err := PlanTransfer(info, &DestState{}, pm)
	if err != nil {
		t.Fatal(err)
	}
	if err := Attach(context.Background(), p, "", nil); err != nil {
		t.Fatal(err)
	}
	dstW := filepath.Join(dstMain, ".worktrees", "feat")
	gotDotGit, _ := os.ReadFile(filepath.Join(dstW, ".git"))
	if want := "gitdir: " + filepath.Join(dstMain, ".git", "worktrees", "feat") + "\n"; string(gotDotGit) != want {
		t.Errorf("W/.git = %q, want %q", gotDotGit, want)
	}
	gotGitdir, _ := os.ReadFile(filepath.Join(dstMain, ".git", "worktrees", "feat", "gitdir"))
	if want := filepath.Join(dstW, ".git") + "\n"; string(gotGitdir) != want {
		t.Errorf("gitdir = %q, want %q", gotGitdir, want)
	}
	// The real git agrees: worktree list shows the destination path, and
	// status is identical to the source's.
	list := gitCLI(t, dstMain, "worktree", "list", "--porcelain")
	if !strings.Contains(list, "worktree "+dstW+"\n") {
		t.Errorf("git worktree list:\n%s", list)
	}
	if diff := cmp.Diff(porcelain(t, w), porcelain(t, dstW)); diff != "" {
		t.Errorf("status differs (-src +dst):\n%s", diff)
	}
	// Idempotent.
	if err := Attach(context.Background(), p, "", nil); err != nil {
		t.Fatal(err)
	}
}

func TestAttachExistingMainCreatesWorktreeAndAppliesDirty(t *testing.T) {
	srcMain := t.TempDir()
	repo, root := initRepo(t, srcMain)
	w := addWorktree(t, srcMain, "feat")
	writeFile(t, filepath.Join(w, "b.txt"), "b\n")
	gitCLI(t, w, "add", "b.txt")
	gitCLI(t, w, "commit", "-q", "-m", "feat work")
	writeFile(t, filepath.Join(w, "README.md"), "modified\n")
	writeFile(t, filepath.Join(w, "new.txt"), "untracked\n")
	writeFile(t, filepath.Join(w, "staged.txt"), "staged\n")
	gitCLI(t, w, "add", "staged.txt")
	_ = repo

	dstMain := filepath.Join(t.TempDir(), "x")
	initRepoAt(t, dstMain, srcMain, root)

	info, _ := Inspect(w)
	ds, err := DestStateOf(dstMain, filepath.Join(dstMain, ".worktrees", "feat"), "feat")
	if err != nil {
		t.Fatal(err)
	}
	pm := pathMap(srcMain, dstMain)
	p, err := PlanTransfer(info, ds, pm)
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != ModeExistingMain || !p.NeedPack {
		t.Fatalf("plan = %+v", p)
	}
	p.StagedBlobs, err = StagedBlobsOf(srcMain, filepath.Join(srcMain, p.IndexRel), p.Tip)
	if err != nil {
		t.Fatal(err)
	}
	var pack bytes.Buffer
	if err := WritePack(context.Background(), srcMain, append([]string{p.Tip}, p.StagedBlobs...), p.HaveTips, &pack); err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	packPath := filepath.Join(staging, "objects.pack")
	if err := os.WriteFile(packPath, pack.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty := map[string]string{}
	for i, rel := range []string{"README.md", "new.txt", "staged.txt"} {
		staged := filepath.Join(staging, string(rune('a'+i)))
		b, _ := os.ReadFile(filepath.Join(w, rel))
		if err := os.WriteFile(staged, b, 0o644); err != nil {
			t.Fatal(err)
		}
		dirty[filepath.Join(p.DstWorktree, rel)] = staged
	}
	idxStaged := filepath.Join(staging, "index")
	b, _ := os.ReadFile(filepath.Join(srcMain, p.IndexRel))
	if err := os.WriteFile(idxStaged, b, 0o644); err != nil {
		t.Fatal(err)
	}
	dirty[filepath.Join(p.DstMain, p.IndexRel)] = idxStaged

	if err := Attach(context.Background(), p, packPath, dirty); err != nil {
		t.Fatal(err)
	}
	dstW := p.DstWorktree
	if diff := cmp.Diff(porcelain(t, w), porcelain(t, dstW)); diff != "" {
		t.Errorf("status differs (-src +dst):\n%s", diff)
	}
	if got := strings.TrimSpace(gitCLI(t, dstW, "rev-parse", "HEAD")); got != p.Tip {
		t.Errorf("dest HEAD = %s, want %s", got, p.Tip)
	}
	if got := strings.TrimSpace(gitCLI(t, dstW, "rev-parse", "--abbrev-ref", "HEAD")); got != "feat" {
		t.Errorf("dest branch = %s", got)
	}
	if got := gitCLI(t, dstW, "diff", "--cached", "--name-only"); strings.TrimSpace(got) != "staged.txt" {
		t.Errorf("staged diff = %q", got)
	}
	gitCLI(t, dstMain, "fsck", "--no-dangling")
	// Re-running attach after success is a no-op that does not clobber the dirty state.
	if err := Attach(context.Background(), p, packPath, dirty); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(porcelain(t, w), porcelain(t, dstW)); diff != "" {
		t.Errorf("status after re-run (-src +dst):\n%s", diff)
	}
}

func TestAttachSameDirFastForward(t *testing.T) {
	srcMain := t.TempDir()
	repo, root := initRepo(t, srcMain)
	writeFile(t, filepath.Join(srcMain, "b.txt"), "b\n")
	second := commitAll(t, repo, "second")
	writeFile(t, filepath.Join(srcMain, "new.txt"), "untracked\n")

	dstMain := filepath.Join(t.TempDir(), "x")
	initRepoAt(t, dstMain, srcMain, root)

	info, _ := Inspect(srcMain)
	ds, _ := DestStateOf(dstMain, dstMain, "main")
	ds.BranchTipReachable, _ = IsAncestor(srcMain, ds.BranchTip, info.Head)
	p, err := PlanTransfer(info, ds, pathMap(srcMain, dstMain))
	if err != nil {
		t.Fatal(err)
	}
	if !p.FastForward {
		t.Fatalf("expected fast-forward plan, got %+v", p)
	}
	var pack bytes.Buffer
	if err := WritePack(context.Background(), srcMain, []string{p.Tip}, p.HaveTips, &pack); err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	packPath := filepath.Join(staging, "objects.pack")
	os.WriteFile(packPath, pack.Bytes(), 0o600)
	newStaged := filepath.Join(staging, "n")
	os.WriteFile(newStaged, []byte("untracked\n"), 0o644)
	idxStaged := filepath.Join(staging, "index")
	b, _ := os.ReadFile(filepath.Join(srcMain, ".git", "index"))
	os.WriteFile(idxStaged, b, 0o644)
	dirty := map[string]string{filepath.Join(dstMain, "new.txt"): newStaged, filepath.Join(dstMain, ".git", "index"): idxStaged}
	if err := Attach(context.Background(), p, packPath, dirty); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(gitCLI(t, dstMain, "rev-parse", "HEAD")); got != second {
		t.Errorf("HEAD = %s, want %s", got, second)
	}
	if diff := cmp.Diff(porcelain(t, srcMain), porcelain(t, dstMain)); diff != "" {
		t.Errorf("status differs (-src +dst):\n%s", diff)
	}
}

func TestAttachSameDirRefusesWhenDirtiedSincePreflight(t *testing.T) {
	srcMain := t.TempDir()
	_, root := initRepo(t, srcMain)
	dstMain := filepath.Join(t.TempDir(), "x")
	initRepoAt(t, dstMain, srcMain, root)
	info, _ := Inspect(srcMain)
	ds, _ := DestStateOf(dstMain, dstMain, "main")
	p, err := PlanTransfer(info, ds, pathMap(srcMain, dstMain))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dstMain, "sneaky.txt"), "x") // dirtied after preflight
	err = Attach(context.Background(), p, "", nil)
	if err == nil || !strings.Contains(err.Error(), "not clean") {
		t.Fatalf("err = %v, want not-clean refusal", err)
	}
}
```

Add to `internal/gitx/testutil_test.go`:

```go
func pathMap(from, to string) session.PathMap {
	return session.NewPathMap(session.Mapping{From: from, To: to})
}
```

(and import `github.com/mithro/go-claude-teleport/internal/session` there).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/gitx/ -run TestAttach -v`
Expected: FAIL — `undefined: Attach`.

- [ ] **Step 3: Implement**

```go
// internal/gitx/attach.go
package gitx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
)

// Attach performs the destination side of spec §8.
func Attach(ctx context.Context, p *Plan, packPath string, dirtyFiles map[string]string) error {
	switch p.Mode {
	case ModeNotRepo:
		return nil
	case ModeFreshMain:
		if !p.Linked {
			return nil
		}
		return repairLinkedMetadata(p)
	case ModeExistingMain:
		return attachExisting(ctx, p, packPath, dirtyFiles)
	}
	return fmt.Errorf("gitx.Attach: unknown mode %q", p.Mode)
}

func worktreeGitDir(p *Plan) string {
	return filepath.Join(p.DstMain, ".git", "worktrees", p.WorktreeName)
}

// writeIfDifferent writes content to path unless it already holds it.
func writeIfDifferent(path, content string, perm os.FileMode) error {
	if cur, err := os.ReadFile(path); err == nil && string(cur) == content {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), perm)
}

// repairLinkedMetadata is what `git worktree repair` does: the two
// absolute-path files are rewritten for the destination paths.
func repairLinkedMetadata(p *Plan) error {
	gd := worktreeGitDir(p)
	if err := writeIfDifferent(filepath.Join(p.DstWorktree, ".git"), "gitdir: "+gd+"\n", 0o644); err != nil {
		return fmt.Errorf("write %s/.git: %w", p.DstWorktree, err)
	}
	if err := writeIfDifferent(filepath.Join(gd, "gitdir"), filepath.Join(p.DstWorktree, ".git")+"\n", 0o644); err != nil {
		return fmt.Errorf("write %s/gitdir: %w", gd, err)
	}
	return nil
}

func attachExisting(ctx context.Context, p *Plan, packPath string, dirtyFiles map[string]string) error {
	repo, err := git.PlainOpenWithOptions(p.DstMain, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return fmt.Errorf("open %s: %w", p.DstMain, err)
	}
	if packPath != "" {
		if err := indexPack(repo, packPath); err != nil {
			return err
		}
	}
	tip := plumbing.NewHash(p.Tip)
	if err := repo.Storer.HasEncodedObject(tip); err != nil {
		return fmt.Errorf("tip %s not present in %s after pack: %w", short(p.Tip), p.DstMain, err)
	}
	if !p.Detached {
		if err := ensureBranch(repo, p, tip); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var gitdir string
	if p.Linked {
		gitdir = worktreeGitDir(p)
		if err := createLinkedWorktree(p, tip); err != nil {
			return err
		}
	} else {
		gitdir = filepath.Join(p.DstMain, ".git")
		if err := fastForwardMainCheckout(p, tip); err != nil {
			return err
		}
	}
	indexDst := filepath.Join(p.DstMain, p.IndexRel)
	if staged, ok := dirtyFiles[indexDst]; ok {
		if err := copyFile(staged, filepath.Join(gitdir, "index")); err != nil {
			return fmt.Errorf("apply index: %w", err)
		}
	}
	for dst, staged := range dirtyFiles {
		if dst == indexDst {
			continue
		}
		if err := copyFile(staged, dst); err != nil {
			return fmt.Errorf("apply dirty file %s: %w", dst, err)
		}
	}
	return nil
}

func indexPack(repo *git.Repository, packPath string) error {
	st, err := os.Stat(packPath)
	if err != nil {
		return err
	}
	if st.Size() == 0 {
		return nil // WritePack found nothing missing
	}
	f, err := os.Open(packPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := packfile.UpdateObjectStorage(repo.Storer, f); err != nil {
		return fmt.Errorf("index pack %s: %w", packPath, err)
	}
	return nil
}

func ensureBranch(repo *git.Repository, p *Plan, tip plumbing.Hash) error {
	name := plumbing.NewBranchReferenceName(p.Branch)
	cur, err := repo.Reference(name, false)
	switch {
	case errors.Is(err, plumbing.ErrReferenceNotFound):
		return repo.Storer.SetReference(plumbing.NewHashReference(name, tip))
	case err != nil:
		return fmt.Errorf("ref %s: %w", name, err)
	case cur.Hash() == tip:
		return nil
	}
	if !p.FastForward {
		return &RefuseError{Reason: fmt.Sprintf("branch %s moved on the destination since preflight (%s), not a fast-forward", p.Branch, short(cur.Hash().String()))}
	}
	ok, err := IsAncestor(p.DstMain, cur.Hash().String(), p.Tip)
	if err != nil {
		return err
	}
	if !ok {
		return &RefuseError{Reason: fmt.Sprintf("branch %s on the destination (%s) is no longer an ancestor of %s", p.Branch, short(cur.Hash().String()), short(p.Tip))}
	}
	return repo.Storer.CheckAndSetReference(plumbing.NewHashReference(name, tip), cur)
}

// createLinkedWorktree writes the metadata git keeps for a linked worktree
// and populates the working tree from tip. A second call (re-run) finds the
// index already present and leaves the tree alone.
func createLinkedWorktree(p *Plan, tip plumbing.Hash) error {
	gd := worktreeGitDir(p)
	if _, err := os.Stat(filepath.Join(gd, "index")); err == nil {
		if _, err := os.Stat(filepath.Join(p.DstWorktree, ".git")); err == nil {
			return nil
		}
	}
	if err := os.MkdirAll(gd, 0o755); err != nil {
		return err
	}
	head := tip.String() + "\n"
	if !p.Detached {
		head = "ref: refs/heads/" + p.Branch + "\n"
	}
	if err := writeIfDifferent(filepath.Join(gd, "HEAD"), head, 0o644); err != nil {
		return err
	}
	if err := writeIfDifferent(filepath.Join(gd, "commondir"), "../..\n", 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(p.DstWorktree, 0o755); err != nil {
		return err
	}
	if err := repairLinkedMetadata(p); err != nil {
		return err
	}
	wrepo, err := git.PlainOpenWithOptions(p.DstWorktree, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return fmt.Errorf("open new worktree %s: %w", p.DstWorktree, err)
	}
	wt, err := wrepo.Worktree()
	if err != nil {
		return err
	}
	if err := wt.Reset(&git.ResetOptions{Mode: git.HardReset, Commit: tip}); err != nil {
		return fmt.Errorf("populate %s at %s: %w", p.DstWorktree, short(p.Tip), err)
	}
	return nil
}

// fastForwardMainCheckout handles W == M: the destination checkout must
// still be clean and on the session branch; then HEAD moves to tip.
func fastForwardMainCheckout(p *Plan, tip plumbing.Hash) error {
	ds, err := DestStateOf(p.DstMain, p.DstMain, p.Branch)
	if err != nil {
		return err
	}
	cur, err := Inspect(p.DstMain)
	if err != nil {
		return err
	}
	if cur.Head == p.Tip {
		return nil // already there (re-run after the dirty state was applied)
	}
	if ds.WorktreeBranch != p.Branch {
		return &RefuseError{Reason: fmt.Sprintf("destination checkout %s is on %q, not %q", p.DstMain, ds.WorktreeBranch, p.Branch)}
	}
	if !ds.Clean {
		return &RefuseError{Reason: fmt.Sprintf("destination checkout %s is not clean", p.DstMain)}
	}
	repo, err := git.PlainOpenWithOptions(p.DstMain, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return err
	}
	if err := wt.Reset(&git.ResetOptions{Mode: git.HardReset, Commit: tip}); err != nil {
		return fmt.Errorf("fast-forward %s to %s: %w", p.DstMain, short(p.Tip), err)
	}
	return nil
}

func copyFile(src, dst string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".claude-teleport.tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, st.Mode().Perm())
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
	return os.Rename(tmp, dst)
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/gitx/ -run TestAttach -v`
Expected: PASS. Known trap: if `TestAttachExistingMainCreatesWorktreeAndAppliesDirty` shows `staged.txt` as `??` instead of `A ` on the destination, the transferred index was not copied — check the map key (`filepath.Join(p.DstMain, p.IndexRel)`) matches exactly what the test builds.

- [ ] **Step 5: Commit**

```bash
git add internal/gitx/attach.go internal/gitx/attach_test.go internal/gitx/testutil_test.go
git commit -m "feat(gitx): Attach repairs, creates or fast-forwards the destination checkout"
```

---

### Task 7: gitx end-to-end with a home rewrite, and PR A

**Files:**
- Test: `internal/gitx/e2e_test.go`

**Interfaces:**
- Consumes: everything in gitx.

- [ ] **Step 1: Write the end-to-end test (fresh-main through Files → copy → Attach, with `/home/alice` → `/home/bob`)**

```go
// internal/gitx/e2e_test.go
package gitx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mithro/go-claude-teleport/internal/session"
)

func TestFreshMainRoundTripThroughFiles(t *testing.T) {
	base := t.TempDir()
	srcHome := filepath.Join(base, "home", "alice")
	dstHome := filepath.Join(base, "home", "bob")
	srcMain := filepath.Join(srcHome, "github", "x")
	initRepo(t, srcMain)
	w := addWorktree(t, srcMain, "feat")
	addWorktree(t, srcMain, "other")
	writeFile(t, filepath.Join(w, "work.go"), "package work\n")
	gitCLI(t, w, "add", "work.go")
	writeFile(t, filepath.Join(w, "scratch.txt"), "untracked\n")

	info, err := Inspect(w)
	if err != nil {
		t.Fatal(err)
	}
	pm := session.NewPathMap(session.Mapping{From: srcHome, To: dstHome})
	p, err := PlanTransfer(info, &DestState{}, pm)
	if err != nil {
		t.Fatal(err)
	}
	files, err := Files(p, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the transfer: every entry lands at pm.ApplyPath(Root)/Rel.
	for _, e := range files {
		dst := filepath.Join(pm.ApplyPath(e.Root), filepath.FromSlash(e.Rel))
		switch {
		case e.Mode.IsDir():
			if err := os.MkdirAll(dst, e.Mode.Perm()); err != nil {
				t.Fatal(err)
			}
		case e.Symlink != "":
			os.MkdirAll(filepath.Dir(dst), 0o755)
			if err := os.Symlink(e.Symlink, dst); err != nil {
				t.Fatal(err)
			}
		default:
			b, err := os.ReadFile(e.Path())
			if err != nil {
				t.Fatal(err)
			}
			os.MkdirAll(filepath.Dir(dst), 0o755)
			if err := os.WriteFile(dst, b, e.Mode.Perm()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := Attach(context.Background(), p, "", nil); err != nil {
		t.Fatal(err)
	}
	dstW := p.DstWorktree
	if !strings.HasPrefix(dstW, dstHome) {
		t.Fatalf("DstWorktree %q not under %q", dstW, dstHome)
	}
	if diff := cmp.Diff(porcelain(t, w), porcelain(t, dstW)); diff != "" {
		t.Errorf("status (-src +dst):\n%s", diff)
	}
	list := gitCLI(t, p.DstMain, "worktree", "list", "--porcelain")
	if strings.Contains(list, "other") {
		t.Errorf("other worktree leaked into the destination:\n%s", list)
	}
	if strings.Contains(list, srcHome) {
		t.Errorf("source home leaked into destination metadata:\n%s", list)
	}
	di, err := Inspect(dstW)
	if err != nil {
		t.Fatal(err)
	}
	if di.Branch != "feat" || di.Head != info.Head || di.MainDir != p.DstMain {
		t.Errorf("dest Inspect = %+v", di)
	}
	gitCLI(t, p.DstMain, "fsck", "--no-dangling")
}
```

- [ ] **Step 2: Run the whole package**

Run: `go vet ./internal/gitx/ && go test -race ./internal/gitx/ -v`
Expected: PASS for every test in the package.

- [ ] **Step 3: Commit and open PR A**

```bash
git add internal/gitx/e2e_test.go
git commit -m "test(gitx): fresh-main round trip with a home rewrite"
git push -u origin gitx
gh pr create --title "gitx: go-git inventory, pack and attach (Plan 03 PR A)" --body "Implements spec §8. Tasks 1–7 of docs/superpowers/plans/2026-08-27-claude-teleport-03-orchestration.md"
```

---

### Task 8: tmuxx — copy the control-mode client from go-tmux-saver

**Files:**
- Create: `internal/tmuxx/parse.go`, `internal/tmuxx/quote.go`, `internal/tmuxx/transport.go`, `internal/tmuxx/client.go`, `internal/tmuxx/testutil.go`
- Create: `internal/tmuxx/parse_test.go`, `internal/tmuxx/quote_test.go`, `internal/tmuxx/fake_test.go`, `internal/tmuxx/client_test.go`
- Create: `internal/tmuxx/testdata/probe.transcript`, `internal/tmuxx/testdata/transcript-3.5a.txt`

Source: `/home/tim/github/mithro/go-tmux-saver/internal/tmuxctl/` (same author, Apache-2.0). Copy `parse.go`, `quote.go`, `parse_test.go`, `quote_test.go`, `fake_test.go`, `testdata/*` **verbatim** except: package name `tmuxx`, and this header added as the first lines of every copied `.go` file:

```go
// Copied from github.com/mithro/go-tmux-saver internal/tmuxctl
// (Apache-2.0, same author). Keep in sync by hand; do not import.
```

`transport.go` is split out of the original `transport.go`/`client.go` with these **deliberate changes** (they are the only ones):

1. `Dial(ctx, socket, session)` becomes `DialControl(ctx, socketPath)` — addressed by **socket path** (`tmux -S <path>`), not `-L <name>`, and attaches without `-t` (control mode needs a session to attach; the most recently used one is fine because every command we send names its target explicitly).
2. The `trace` import and `trace.Time` calls are removed.
3. `noServerRunning` takes the socket path and never runs the `has-session` text probe: a missing socket or `ECONNREFUSED` is "no server"; any other stat/connect error is returned as an error (spec: "no silent fallbacks").
4. `Dialer` type added.

- [ ] **Step 1: Copy the files and adapt the tests**

`internal/tmuxx/transport.go`:

```go
// Copied from github.com/mithro/go-tmux-saver internal/tmuxctl
// (Apache-2.0, same author). Keep in sync by hand; do not import.

// Package tmuxx talks to a tmux server over a single control-mode
// connection, discovers servers, and opens/captures/types into panes.
package tmuxx

import (
	"context"
	"fmt"
	"strings"
)

// Transport runs tmux commands and returns reply lines.
type Transport interface {
	Run(ctx context.Context, cmd string) ([]string, error)
	Close() error
}

// Dialer opens a Transport to the server on socketPath.
type Dialer func(ctx context.Context, socketPath string) (Transport, error)

// CmdError is returned when tmux answers a command with %error.
type CmdError struct {
	Cmd   string
	Lines []string
}

func (e *CmdError) Error() string {
	return fmt.Sprintf("tmux %q: %s", e.Cmd, strings.Join(e.Lines, " "))
}

// Fake is a Transport backed by a command→reply map, for tests.
type Fake struct {
	Replies map[string][]string
	Default []string // reply for commands not in Replies (nil = error)
	Calls   []string
}

var _ Transport = (*Fake)(nil)

// Run answers from Replies, then Default. A command with neither fails with
// a plain error that is deliberately NOT a *CmdError: production code
// degrades gracefully on tmux %error, so a forgotten stub returning
// *CmdError would let a test silently exercise the degraded path.
func (f *Fake) Run(_ context.Context, cmd string) ([]string, error) {
	f.Calls = append(f.Calls, cmd)
	if r, ok := f.Replies[cmd]; ok {
		return append([]string(nil), r...), nil
	}
	if f.Default != nil {
		return append([]string(nil), f.Default...), nil
	}
	return nil, fmt.Errorf("tmuxx.Fake: no reply configured for %q — add it to Replies or set Default", cmd)
}

func (f *Fake) Close() error { return nil }
```

`internal/tmuxx/client.go`:

```go
// Copied from github.com/mithro/go-tmux-saver internal/tmuxctl
// (Apache-2.0, same author). Keep in sync by hand; do not import.

package tmuxx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
)

// Client is a live control-mode connection to one tmux server.
type Client struct {
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	stderr        *bytes.Buffer
	replies       chan Reply
	parseErr      chan error
	mu            sync.Mutex
	desynced      atomic.Bool
	parseOnce     sync.Once
	finalParseErr error
}

var _ Transport = (*Client)(nil)

// ErrDesynced is returned by Run once a previous command's context was
// cancelled before its reply arrived; the connection cannot be recovered.
var ErrDesynced = errors.New("control connection desynchronised after a cancelled command; dial again")

// ErrNoServer is wrapped by DialControl when no server listens on the socket.
var ErrNoServer = errors.New("no tmux server")

// DialControl starts `tmux -S <socketPath> -C attach-session -f no-output`
// and consumes the initial attach block. It never starts a server: the
// socket is probed first and a missing/stale socket returns ErrNoServer.
func DialControl(ctx context.Context, socketPath string) (Transport, error) {
	if err := probeSocket(ctx, socketPath); err != nil {
		return nil, err
	}
	cmd := exec.Command("tmux", "-S", socketPath, "-C", "attach-session", "-f", "no-output")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start tmux control client: %w", err)
	}
	c := &Client{cmd: cmd, stdin: stdin, stderr: &stderr, replies: make(chan Reply, 64), parseErr: make(chan error, 1)}
	go func() {
		c.parseErr <- ParseReplies(stdout, c.replies)
		close(c.replies)
	}()
	reply, err := c.next(ctx)
	if err != nil {
		c.Close()
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("attach on socket %s: %w: %s", socketPath, err, msg)
		}
		return nil, fmt.Errorf("attach on socket %s: %w", socketPath, err)
	}
	if reply.Err {
		c.Close()
		return nil, fmt.Errorf("attach on socket %s: %s", socketPath, strings.Join(reply.Lines, " "))
	}
	return c, nil
}

// probeSocket classifies the socket by errno, never by tmux's text.
func probeSocket(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("socket %s does not exist: %w", path, ErrNoServer)
		}
		return fmt.Errorf("stat socket %s: %w", path, err)
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stale socket %s: %w", path, ErrNoServer)
		}
		return fmt.Errorf("connect socket %s: %w", path, err)
	}
	conn.Close()
	return nil
}

func (c *Client) next(ctx context.Context) (Reply, error) {
	select {
	case r, ok := <-c.replies:
		if !ok {
			c.parseOnce.Do(func() { c.finalParseErr = <-c.parseErr })
			if c.finalParseErr != nil {
				return Reply{}, fmt.Errorf("control connection closed: %w", c.finalParseErr)
			}
			return Reply{}, fmt.Errorf("control connection closed")
		}
		return r, nil
	case <-ctx.Done():
		c.desynced.Store(true)
		return Reply{}, ctx.Err()
	}
}

// Run sends one command and returns its reply lines. Commands are serialised.
func (c *Client) Run(ctx context.Context, cmd string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.desynced.Load() {
		return nil, fmt.Errorf("run %q: %w", cmd, ErrDesynced)
	}
	if _, err := io.WriteString(c.stdin, cmd+"\n"); err != nil {
		return nil, fmt.Errorf("write %q: %w", cmd, err)
	}
	r, err := c.next(ctx)
	if err != nil {
		return nil, err
	}
	if r.Err {
		return nil, &CmdError{Cmd: cmd, Lines: r.Lines}
	}
	return r.Lines, nil
}

// Close detaches (stdin EOF → %exit) and waits for the client to exit.
func (c *Client) Close() error {
	c.stdin.Close()
	return c.cmd.Wait()
}
```

`internal/tmuxx/testutil.go` (build tag `tmuxlive`; the socket name is `claude-teleport-test-<pid>[-<test>]`):

```go
//go:build tmuxlive

// Copied from github.com/mithro/go-tmux-saver internal/tmuxctl
// (Apache-2.0, same author). Keep in sync by hand; do not import.

package tmuxx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// StartTestServer starts a throwaway tmux server (session "default",
// window "h") on socket claude-teleport-test-<pid>-<test> under a private
// TMUX_TMPDIR and kills it when the test ends. Returns the socket PATH and
// the socket directory. Skips if tmux is not installed. -f /dev/null keeps
// the developer's ~/.tmux.conf (hooks, continuum) out of the test.
func StartTestServer(t testing.TB) (socketPath, socketDir string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tmp := t.TempDir()
	t.Setenv("TMUX_TMPDIR", tmp)
	socketDir = filepath.Join(tmp, fmt.Sprintf("tmux-%d", os.Getuid()))
	name := "claude-teleport-test-" + fmt.Sprint(os.Getpid()) + "-" + strings.ReplaceAll(t.Name(), "/", "_")
	if len(name) > 40 {
		name = name[:40] // unix socket paths are limited to ~108 bytes
	}
	if out, err := exec.Command("tmux", "-L", name, "-f", "/dev/null", "new-session", "-d", "-s", "default", "-n", "h", "tail -f /dev/null").CombinedOutput(); err != nil {
		t.Fatalf("start tmux: %v: %s", err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", name, "kill-server").Run() })
	return filepath.Join(socketDir, name), socketDir
}
```

`internal/tmuxx/client_test.go` — copy `TestClientRoundTrip`, `TestClientContextTimeout`, `TestDialBadSession`→renamed `TestDialControlNoServer` (asserts `errors.Is(err, ErrNoServer)` for a path under `t.TempDir()`), `TestClientDesyncAfterCancel`, `TestNextSurfacesParseErrDetail` from the original, with `Dial(ctx, sock, "default")` replaced by `DialControl(ctx, sockPath)` and the file tagged `//go:build tmuxlive` except the two white-box tests (`TestClientDesyncAfterCancel`, `TestNextSurfacesParseErrDetail`) and `TestDialControlNoServer`, which go into `client_unit_test.go` without a tag. `TestNoServerClassifiesBySocketState` becomes:

```go
func TestProbeSocketClassifies(t *testing.T) {
	dir := t.TempDir()
	if err := probeSocket(context.Background(), filepath.Join(dir, "missing")); !errors.Is(err, ErrNoServer) {
		t.Errorf("missing socket: %v, want ErrNoServer", err)
	}
	stale := filepath.Join(dir, "stale")
	l, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	if err := probeSocket(context.Background(), stale); !errors.Is(err, ErrNoServer) {
		t.Errorf("stale socket: %v, want ErrNoServer", err)
	}
	live := filepath.Join(dir, "live")
	l2, err := net.Listen("unix", live)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if err := probeSocket(context.Background(), live); err != nil {
		t.Errorf("live socket: %v, want nil", err)
	}
}
```

- [ ] **Step 2: Run the untagged and tagged tests**

Run: `go vet ./internal/tmuxx/ && go test -race ./internal/tmuxx/ -v && go test -race -tags tmuxlive ./internal/tmuxx/ -v`
Expected: PASS (the parse/quote/fake tests are the originals; `TestParseProbeTranscript` needs the copied `testdata/`). Tagged run PASSes with tmux installed, otherwise skips.

- [ ] **Step 3: Commit**

```bash
git add internal/tmuxx
git commit -m "feat(tmuxx): control-mode client copied from go-tmux-saver, addressed by socket path"
```

---

### Task 9: tmuxx.Describe, ListSessions, FindServer, ListServers

**Files:**
- Create: `internal/tmuxx/describe.go`, `internal/tmuxx/servers.go`
- Test: `internal/tmuxx/describe_test.go`, `internal/tmuxx/servers_test.go`

**Interfaces:**
- Consumes: `Transport`, `Quote`, `DialControl`, `ErrNoServer`.
- Produces: `Facts`, `Describe(ctx, t, paneID) (*Facts, error)`, `SessionInfo{Name, Group string}` and `ListSessions(ctx, t) ([]SessionInfo, error)` (additions), `FindServer(socketDir, preferredName, override string) (string, error)`, `ListServers(socketDir string) ([]string, error)`, and `var Dial Dialer = DialControl` (the liveness probe used by FindServer; tests swap it).

Describe's format is tab-separated with `pane_title` **last** because it is the only field that may legally contain a tab (session and window names are vis-encoded by tmux; `pane_current_path` is a path). The line is split into 11 fields with `SplitN(…, 11)`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/tmuxx/describe_test.go
package tmuxx

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const describeCmd = `list-panes -a -F "#{session_name}	#{session_group}	#{window_id}	#{window_index}	#{window_name}	#{pane_id}	#{pane_current_path}	#{pane_current_command}	#{pane_pid}	#{history_size}	#{pane_title}"`

func TestDescribe(t *testing.T) {
	f := &Fake{Replies: map[string][]string{
		describeCmd: {
			"main\t\t@0\t0\tshell\t%0\t/home/alice\tbash\t4242\t120\talice@laptop",
			"main\tmain\t@3\t2\tclaude\t%7\t/home/alice/github/x/.worktrees/feat\tclaude\t5150\t9001\t✳ feat\twith tab",
		},
		`show-options -wv -t "@3" automatic-rename`: {"off"},
	}}
	got, err := Describe(context.Background(), f, "%7")
	if err != nil {
		t.Fatal(err)
	}
	want := &Facts{SessionName: "main", Group: "main", WindowID: "@3", WindowIndex: 2, WindowName: "claude", AutoRename: false,
		PaneID: "%7", PaneTitle: "✳ feat\twith tab", PaneCwd: "/home/alice/github/x/.worktrees/feat", PaneCommand: "claude", PanePID: 5150, HistorySize: 9001}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

func TestDescribeAutoRenameOnByDefault(t *testing.T) {
	f := &Fake{Replies: map[string][]string{
		describeCmd:                                   {"main\t\t@0\t0\tshell\t%0\t/home/alice\tbash\t4242\t120\tt"},
		`show-options -wv -t "@0" automatic-rename`: {""},
	}}
	got, err := Describe(context.Background(), f, "%0")
	if err != nil {
		t.Fatal(err)
	}
	if !got.AutoRename {
		t.Error("unset automatic-rename must read as on (tmux default)")
	}
}

func TestDescribeUnknownPane(t *testing.T) {
	f := &Fake{Replies: map[string][]string{describeCmd: {"main\t\t@0\t0\tshell\t%0\t/home/alice\tbash\t4242\t120\tt"}}}
	if _, err := Describe(context.Background(), f, "%99"); err == nil {
		t.Fatal("expected an error for an unknown pane")
	}
}

func TestListSessions(t *testing.T) {
	f := &Fake{Replies: map[string][]string{`list-sessions -F "#{session_name}	#{session_group}"`: {"main\tmain", "main-2\tmain", "other\t"}}}
	got, err := ListSessions(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	want := []SessionInfo{{"main", "main"}, {"main-2", "main"}, {"other", ""}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}
```

```go
// internal/tmuxx/servers_test.go
package tmuxx

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDial treats any socket path in `live` as a running server.
func fakeDial(live map[string]bool) Dialer {
	return func(_ context.Context, p string) (Transport, error) {
		if live[p] {
			return &Fake{Default: []string{}}, nil
		}
		return nil, ErrNoServer
	}
}

func mkSocket(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	return p
}

func TestFindServerOrder(t *testing.T) {
	dir := t.TempDir()
	main := mkSocket(t, dir, "main")
	def := mkSocket(t, dir, "default")
	other := mkSocket(t, dir, "other")
	restore := Dial
	defer func() { Dial = restore }()

	Dial = fakeDial(map[string]bool{main: true, def: true, other: true})
	if got, _ := FindServer(dir, "main", ""); got != main {
		t.Errorf("preferred name: %q, want %q", got, main)
	}
	if got, _ := FindServer(dir, "", ""); got != def {
		t.Errorf("default: %q, want %q", got, def)
	}
	if got, _ := FindServer(dir, "", "other"); got != other {
		t.Errorf("override: %q, want %q", got, other)
	}
	// Preferred dead, default dead, exactly one live → that one.
	Dial = fakeDial(map[string]bool{other: true})
	if got, err := FindServer(dir, "main", ""); got != other || err != nil {
		t.Errorf("single live: %q %v, want %q", got, err, other)
	}
	// Two live candidates, neither preferred nor default → error listing them.
	Dial = fakeDial(map[string]bool{other: true, main: true})
	_, err := FindServer(dir, "nope", "")
	if err == nil || !strings.Contains(err.Error(), "main") || !strings.Contains(err.Error(), "other") {
		t.Errorf("ambiguous: err = %v, want list of sockets", err)
	}
	// Override that is dead is an error, never a fallback.
	Dial = fakeDial(map[string]bool{main: true})
	if _, err := FindServer(dir, "", "other"); err == nil {
		t.Error("dead override must error")
	}
	// Nothing live at all.
	Dial = fakeDial(nil)
	if _, err := FindServer(dir, "", ""); !errors.Is(err, ErrNoServer) {
		t.Errorf("none: %v, want ErrNoServer", err)
	}
}

func TestListServersMissingDir(t *testing.T) {
	got, err := ListServers(filepath.Join(t.TempDir(), "absent"))
	if err != nil || len(got) != 0 {
		t.Errorf("got %v %v", got, err)
	}
	dir := t.TempDir()
	mkSocket(t, dir, "a")
	os.WriteFile(filepath.Join(dir, "notasocket"), nil, 0o600)
	got, err = ListServers(dir)
	if err != nil || len(got) != 1 || filepath.Base(got[0]) != "a" {
		t.Errorf("got %v %v", got, err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tmuxx/ -run 'TestDescribe|TestListSessions|TestFindServer|TestListServers' -v`
Expected: FAIL — `undefined: Describe`, `undefined: FindServer`, …

- [ ] **Step 3: Implement**

```go
// internal/tmuxx/describe.go
package tmuxx

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

type Facts struct {
	SocketPath  string
	SessionName string
	Group       string // session_group or ""
	WindowID    string
	WindowIndex int
	WindowName  string
	AutoRename  bool
	PaneID      string
	PaneTitle   string
	PaneCwd     string
	PaneCommand string
	PanePID     int
	HistorySize int
}

// describeFormat: pane_title is last on purpose — it is the only field that
// may contain a literal tab (names are vis-encoded by tmux).
const describeFormat = "#{session_name}\t#{session_group}\t#{window_id}\t#{window_index}\t#{window_name}\t#{pane_id}\t#{pane_current_path}\t#{pane_current_command}\t#{pane_pid}\t#{history_size}\t#{pane_title}"

// Describe collects the spec §9 source facts for one pane.
func Describe(ctx context.Context, t Transport, paneID string) (*Facts, error) {
	lines, err := t.Run(ctx, `list-panes -a -F "`+describeFormat+`"`)
	if err != nil {
		return nil, fmt.Errorf("list-panes: %w", err)
	}
	for _, l := range lines {
		f := strings.SplitN(l, "\t", 11)
		if len(f) != 11 {
			return nil, fmt.Errorf("malformed list-panes line: %q", l)
		}
		if f[5] != paneID {
			continue
		}
		wi, err := strconv.Atoi(f[3])
		if err != nil {
			return nil, fmt.Errorf("window_index in %q: %w", l, err)
		}
		pid, err := strconv.Atoi(f[8])
		if err != nil {
			return nil, fmt.Errorf("pane_pid in %q: %w", l, err)
		}
		hs, err := strconv.Atoi(f[9])
		if err != nil {
			return nil, fmt.Errorf("history_size in %q: %w", l, err)
		}
		facts := &Facts{SessionName: f[0], Group: f[1], WindowID: f[2], WindowIndex: wi, WindowName: f[4],
			PaneID: f[5], PaneCwd: f[6], PaneCommand: f[7], PanePID: pid, HistorySize: hs, PaneTitle: f[10]}
		ar, err := t.Run(ctx, fmt.Sprintf("show-options -wv -t %s automatic-rename", Quote(facts.WindowID)))
		if err != nil {
			return nil, fmt.Errorf("show-options automatic-rename: %w", err)
		}
		facts.AutoRename = true // tmux default
		if len(ar) > 0 && strings.TrimSpace(ar[0]) == "off" {
			facts.AutoRename = false
		}
		return facts, nil
	}
	return nil, fmt.Errorf("pane %s not found on this server", paneID)
}

// SessionInfo is one row of list-sessions.
type SessionInfo struct{ Name, Group string }

// ListSessions lists every session with its group ("" when ungrouped).
func ListSessions(ctx context.Context, t Transport) ([]SessionInfo, error) {
	lines, err := t.Run(ctx, `list-sessions -F "#{session_name}\t#{session_group}"`)
	if err != nil {
		return nil, fmt.Errorf("list-sessions: %w", err)
	}
	var out []SessionInfo
	for _, l := range lines {
		if l == "" {
			continue
		}
		name, group, _ := strings.Cut(l, "\t")
		out = append(out, SessionInfo{Name: name, Group: group})
	}
	return out, nil
}
```

Note the test's `describeCmd` constant uses literal tab characters inside the format string while the implementation writes `\t` escapes in a Go string — both produce a real tab byte on the wire, which tmux accepts in a double-quoted argument (Quote passes tabs through; the format string is a constant, not user data, so it is not Quoted).

```go
// internal/tmuxx/servers.go
package tmuxx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Dial is the liveness probe FindServer uses; tests replace it.
var Dial Dialer = DialControl

// ListServers returns the socket paths under socketDir (sorted). A missing
// directory means no servers.
func ListServers(socketDir string) ([]string, error) {
	entries, err := os.ReadDir(socketDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSocket != 0 {
			out = append(out, filepath.Join(socketDir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// alive reports whether a control-mode handshake succeeds on path.
func alive(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t, err := Dial(ctx, path)
	if err != nil {
		return false
	}
	t.Close()
	return true
}

// FindServer implements spec §9 discovery over socketDir: override (must be
// alive), else preferredName, else "default", else the only live socket,
// else an error listing what was found. It never starts a server.
func FindServer(socketDir string, preferredName string, override string) (string, error) {
	if override != "" {
		p := filepath.Join(socketDir, override)
		if !alive(p) {
			return "", fmt.Errorf("--tmux-socket %s: no live server at %s: %w", override, p, ErrNoServer)
		}
		return p, nil
	}
	for _, name := range []string{preferredName, "default"} {
		if name == "" {
			continue
		}
		if p := filepath.Join(socketDir, name); alive(p) {
			return p, nil
		}
	}
	all, err := ListServers(socketDir)
	if err != nil {
		return "", err
	}
	var live []string
	for _, p := range all {
		if alive(p) {
			live = append(live, p)
		}
	}
	switch len(live) {
	case 0:
		return "", fmt.Errorf("no live tmux server under %s: %w", socketDir, ErrNoServer)
	case 1:
		return live[0], nil
	}
	return "", fmt.Errorf("several tmux servers under %s and none is named %q or \"default\": %s (use --tmux-socket NAME)", socketDir, preferredName, strings.Join(live, ", "))
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/tmuxx/ -run 'TestDescribe|TestListSessions|TestFindServer|TestListServers' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tmuxx/describe.go internal/tmuxx/describe_test.go internal/tmuxx/servers.go internal/tmuxx/servers_test.go
git commit -m "feat(tmuxx): Describe pane facts, ListSessions and spec §9 server discovery"
```

---

### Task 10: tmuxx.OpenWindow and KillWindow

**Files:**
- Create: `internal/tmuxx/window.go`
- Test: `internal/tmuxx/window_test.go`

**Interfaces:**
- Consumes: `Transport`, `Quote`, `ListSessions`, `session.TmuxRef`.
- Produces: `Plan`, `OpenWindow(ctx, t, p) (*session.TmuxRef, error)`, `KillWindow(ctx, t, windowID) error`, `BaseSession(sessions []SessionInfo, group string) (string, bool)` (addition; the CanonicalMember rule from go-tmux-saver: the member named like the group wins, else the lexically smallest).

- [ ] **Step 1: Write the failing tests**

```go
// internal/tmuxx/window_test.go
package tmuxx

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const listSessionsCmd = `list-sessions -F "#{session_name}	#{session_group}"`

func TestOpenWindowCreatesSessionWhenGroupAbsent(t *testing.T) {
	f := &Fake{Replies: map[string][]string{
		listSessionsCmd: {"other\t"},
		`new-session -d -s "work" -n "claude" -c "/home/bob/github/x" -P -F "#{pane_id}	#{window_id}	#{session_name}"`: {"%3\t@1\twork"},
		`set-option -w -t "@1" automatic-rename off`: {},
	}}
	ref, err := OpenWindow(context.Background(), f, &Plan{SocketPath: "/tmp/tmux-1000/default", Group: "work", WindowName: "claude", AutoRename: false, Cwd: "/home/bob/github/x", CreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{listSessionsCmd,
		`new-session -d -s "work" -n "claude" -c "/home/bob/github/x" -P -F "#{pane_id}	#{window_id}	#{session_name}"`,
		`set-option -w -t "@1" automatic-rename off`}, f.Calls); diff != "" {
		t.Errorf("calls (-want +got):\n%s", diff)
	}
	if ref.Session != "work" || ref.WindowID != "@1" || ref.PaneID != "%3" || ref.SocketPath != "/tmp/tmux-1000/default" {
		t.Errorf("ref = %+v", ref)
	}
}

func TestOpenWindowUsesGroupBaseSession(t *testing.T) {
	f := &Fake{Replies: map[string][]string{
		listSessionsCmd: {"work-7\twork", "work\twork", "other\t"},
		`new-window -t "=work:" -n "claude" -c "/home/bob/github/x" -P -F "#{pane_id}	#{window_id}	#{session_name}"`: {"%9\t@4\twork"},
	}}
	ref, err := OpenWindow(context.Background(), f, &Plan{Group: "work", WindowName: "claude", AutoRename: true, Cwd: "/home/bob/github/x"})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Session != "work" || ref.WindowID != "@4" || ref.PaneID != "%9" {
		t.Errorf("ref = %+v", ref)
	}
	for _, c := range f.Calls {
		if c == `set-option -w -t "@4" automatic-rename off` {
			t.Error("automatic-rename must not be touched when AutoRename is true")
		}
	}
}

func TestOpenWindowHostileNamesAreQuoted(t *testing.T) {
	f := &Fake{Replies: map[string][]string{
		listSessionsCmd: {},
		`new-session -d -s "evil;kill-server" -n "w \"q\"" -c "/home/bob/a b" -P -F "#{pane_id}	#{window_id}	#{session_name}"`: {"%0\t@0\tevil;kill-server"},
	}}
	if _, err := OpenWindow(context.Background(), f, &Plan{Group: "evil;kill-server", WindowName: `w "q"`, AutoRename: true, Cwd: "/home/bob/a b", CreateSession: true}); err != nil {
		t.Fatal(err)
	}
}

func TestBaseSession(t *testing.T) {
	s := []SessionInfo{{"work-7", "work"}, {"work-3", "work"}, {"other", ""}}
	if got, ok := BaseSession(s, "work"); !ok || got != "work-3" {
		t.Errorf("lexically smallest: %q %v", got, ok)
	}
	s = append(s, SessionInfo{"work", "work"})
	if got, _ := BaseSession(s, "work"); got != "work" {
		t.Errorf("name == group wins: %q", got)
	}
	if got, ok := BaseSession(s, "other"); !ok || got != "other" {
		t.Errorf("ungrouped session named G: %q %v", got, ok)
	}
	if _, ok := BaseSession(s, "none"); ok {
		t.Error("absent group must not be found")
	}
}

func TestKillWindow(t *testing.T) {
	f := &Fake{Replies: map[string][]string{`kill-window -t "@4"`: {}}}
	if err := KillWindow(context.Background(), f, "@4"); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tmuxx/ -run 'TestOpenWindow|TestBaseSession|TestKillWindow' -v`
Expected: FAIL — `undefined: OpenWindow`.

- [ ] **Step 3: Implement**

```go
// internal/tmuxx/window.go
package tmuxx

import (
	"context"
	"fmt"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/session"
)

type Plan struct {
	SocketPath    string
	Group         string
	WindowName    string
	AutoRename    bool
	Cwd           string
	CreateSession bool // no session in Group exists
}

// BaseSession picks the session to add windows to for group G: the member
// named G if present, else the lexically smallest member; a session named
// G that is not grouped also counts.
func BaseSession(sessions []SessionInfo, group string) (string, bool) {
	best := ""
	for _, s := range sessions {
		if s.Name == group {
			return s.Name, true
		}
		if s.Group == group && (best == "" || s.Name < best) {
			best = s.Name
		}
	}
	return best, best != ""
}

const newWindowFormat = "#{pane_id}\t#{window_id}\t#{session_name}"

// OpenWindow creates the destination window (spec §9). The live session
// list decides between new-session and new-window; p.CreateSession is the
// preflight expectation and only affects logging by the caller.
func OpenWindow(ctx context.Context, t Transport, p *Plan) (*session.TmuxRef, error) {
	sessions, err := ListSessions(ctx, t)
	if err != nil {
		return nil, err
	}
	var cmd string
	if base, ok := BaseSession(sessions, p.Group); ok {
		cmd = fmt.Sprintf(`new-window -t %s -n %s -c %s -P -F "%s"`, Quote("="+base+":"), Quote(p.WindowName), Quote(p.Cwd), newWindowFormat)
	} else {
		cmd = fmt.Sprintf(`new-session -d -s %s -n %s -c %s -P -F "%s"`, Quote(p.Group), Quote(p.WindowName), Quote(p.Cwd), newWindowFormat)
	}
	lines, err := t.Run(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("open window: %w", err)
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("open window: empty reply to %q", cmd)
	}
	f := strings.SplitN(lines[0], "\t", 3)
	if len(f) != 3 || !strings.HasPrefix(f[0], "%") || !strings.HasPrefix(f[1], "@") {
		return nil, fmt.Errorf("open window: unexpected reply %q", lines[0])
	}
	ref := &session.TmuxRef{SocketPath: p.SocketPath, Session: f[2], WindowID: f[1], PaneID: f[0]}
	if !p.AutoRename {
		if _, err := t.Run(ctx, fmt.Sprintf("set-option -w -t %s automatic-rename off", Quote(ref.WindowID))); err != nil {
			return nil, fmt.Errorf("automatic-rename off: %w", err)
		}
	}
	return ref, nil
}

// KillWindow removes a window by id.
func KillWindow(ctx context.Context, t Transport, windowID string) error {
	if _, err := t.Run(ctx, fmt.Sprintf("kill-window -t %s", Quote(windowID))); err != nil {
		return fmt.Errorf("kill-window %s: %w", windowID, err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/tmuxx/ -run 'TestOpenWindow|TestBaseSession|TestKillWindow' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tmuxx/window.go internal/tmuxx/window_test.go
git commit -m "feat(tmuxx): OpenWindow into a session group, KillWindow"
```

---

### Task 11: tmuxx pane operations — Capture, SendKeys, TypeCommand, State, Prober

**Files:**
- Create: `internal/tmuxx/pane.go`, `internal/tmuxx/prober.go`
- Test: `internal/tmuxx/pane_test.go`

**Interfaces:**
- Consumes: `procx.Table` (`Get`, `Subtree`), `procx.Scan`, `session.PaneProbe`.
- Produces: `PaneState`, `Capture`, `SendKeys`, `TypeCommand`, `State`, `Prober`, `ShellQuote(argv []string) string` (addition — POSIX single-quoting used by TypeCommand).

`State` finds the pane's foreground process: BFS over `procs.Subtree(panePID)` skipping the pane pid itself; the first process whose `Comm` is not a shell (`bash`, `zsh`, `sh`, `fish`, `dash`) wins; if none, the shell itself is reported.

- [ ] **Step 1: Write the failing tests**

```go
// internal/tmuxx/pane_test.go
package tmuxx

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mithro/go-claude-teleport/internal/procx"
)

func TestCaptureJoinsLinesWithNewline(t *testing.T) {
	f := &Fake{Replies: map[string][]string{`capture-pane -epJ -S - -t "%7"`: {"line1", "", "line3"}}}
	got, err := Capture(context.Background(), f, "%7")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "line1\n\nline3\n" {
		t.Errorf("capture = %q", got)
	}
}

func TestSendKeysQuotesEachKey(t *testing.T) {
	f := &Fake{Replies: map[string][]string{`send-keys -t "%7" "/exit"`: {}, `send-keys -t "%7" Enter`: {}}}
	if err := SendKeys(context.Background(), f, "%7", "/exit"); err != nil {
		t.Fatal(err)
	}
	if err := SendKeys(context.Background(), f, "%7", "Enter"); err != nil {
		t.Fatal(err)
	}
}

func TestTypeCommandLeadingSpaceAndShellQuoting(t *testing.T) {
	want := `send-keys -t "%7" " 'claude-teleport' 'placeholder' '--resume' '3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c' '--saved-output' '/home/bob/it'\''s.txt' '--now'" Enter`
	f := &Fake{Replies: map[string][]string{want: {}}}
	err := TypeCommand(context.Background(), f, "%7", []string{"claude-teleport", "placeholder", "--resume", "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c", "--saved-output", "/home/bob/it's.txt", "--now"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{want}, f.Calls); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

// fakeProc writes a /proc-shaped tree for procx.Scan (pid, ppid, comm, cmdline).
func fakeProc(t *testing.T, procs [][4]string) *procx.Table {
	t.Helper()
	root := t.TempDir()
	for _, p := range procs {
		dir := filepath.Join(root, p[0])
		os.MkdirAll(dir, 0o755)
		stat := p[0] + " (" + p[2] + ") S " + p[1] + " 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 12345 0 0"
		os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644)
		os.WriteFile(filepath.Join(dir, "cmdline"), []byte(p[3]), 0o644)
	}
	tb, err := procx.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func TestStateForegroundChild(t *testing.T) {
	tb := fakeProc(t, [][4]string{
		{"100", "1", "bash", "bash\x00"},
		{"200", "100", "claude-teleport", "claude-teleport\x00placeholder\x00--resume\x003f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c\x00"},
	})
	f := &Fake{Replies: map[string][]string{
		`list-panes -t "%7" -F "#{pane_pid}"`:                         {"100"},
		`capture-pane -p -S -50 -t "%7"`:                              {"a", "b"},
	}}
	st, err := State(context.Background(), f, "%7", tb)
	if err != nil {
		t.Fatal(err)
	}
	want := &PaneState{PaneID: "%7", Command: "claude-teleport", PID: 200,
		Argv: []string{"claude-teleport", "placeholder", "--resume", "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"}, Content: []string{"a", "b"}}
	if diff := cmp.Diff(want, st); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	if _, ok := procx.IsPlaceholderArgv(st.Argv); !ok {
		t.Error("placeholder argv must keep being recognised by procx.IsPlaceholderArgv")
	}
}

func TestStateBareShell(t *testing.T) {
	tb := fakeProc(t, [][4]string{{"100", "1", "zsh", "-zsh\x00"}})
	f := &Fake{Replies: map[string][]string{`list-panes -t "%7" -F "#{pane_pid}"`: {"100"}, `capture-pane -p -S -50 -t "%7"`: {}}}
	st, err := State(context.Background(), f, "%7", tb)
	if err != nil {
		t.Fatal(err)
	}
	if st.Command != "zsh" || st.PID != 100 {
		t.Errorf("state = %+v", st)
	}
}

func TestProber(t *testing.T) {
	tb := fakeProc(t, [][4]string{{"100", "1", "bash", "bash\x00"}, {"200", "100", "claude", "claude\x00"}})
	f := &Fake{Replies: map[string][]string{
		`list-panes -t "%7" -F "#{pane_pid}"`: {"100"},
		`capture-pane -p -S -50 -t "%7"`:      {},
		`list-panes -t "=main:2" -F "#{pane_id}"`: {"%7", "%8"},
		`list-panes -a -F "#{session_name} #{window_id} #{pane_id}"`: {"main @1 %7", "main @1 %8"},
	}}
	p := Prober(context.Background(), f, tb, "/tmp/tmux-1000/default")
	argv, pid, ok := p.PaneCommand("%7")
	if !ok || pid != 200 || len(argv) != 1 || argv[0] != "claude" {
		t.Errorf("PaneCommand = %v %d %v", argv, pid, ok)
	}
	if _, _, ok := p.PaneCommand("%99"); ok {
		t.Error("unknown pane must be ok=false")
	}
	panes, err := p.FindWindow("main", "2")
	if err != nil || len(panes) != 2 {
		t.Errorf("FindWindow = %v %v", panes, err)
	}
	if p.SocketPath() != "/tmp/tmux-1000/default" {
		t.Error("SocketPath")
	}
	all, err := p.ListPanes()
	if err != nil || len(all) != 2 || all[1].Session != "main" || all[1].WindowID != "@1" || all[1].PaneID != "%8" {
		t.Errorf("ListPanes = %v %v", all, err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tmuxx/ -run 'TestCapture|TestSendKeys|TestTypeCommand|TestState|TestProber' -v`
Expected: FAIL — `undefined: Capture`, …

- [ ] **Step 3: Implement**

```go
// internal/tmuxx/pane.go
package tmuxx

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/procx"
)

type PaneState struct {
	PaneID  string
	Command string
	Argv    []string
	PID     int
	Content []string // last 50 lines
}

// Capture returns the pane's whole scrollback with escapes (-e), joined
// wrapped lines (-J), trailing spaces preserved (-p prints).
func Capture(ctx context.Context, t Transport, paneID string) ([]byte, error) {
	lines, err := t.Run(ctx, fmt.Sprintf("capture-pane -epJ -S - -t %s", Quote(paneID)))
	if err != nil {
		return nil, fmt.Errorf("capture-pane %s: %w", paneID, err)
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

// SendKeys sends keys to the pane. Key names tmux knows (Enter, C-c, …)
// are passed bare; everything else is Quoted as literal text.
func SendKeys(ctx context.Context, t Transport, paneID string, keys ...string) error {
	parts := []string{"send-keys", "-t", Quote(paneID)}
	for _, k := range keys {
		if isKeyName(k) {
			parts = append(parts, k)
		} else {
			parts = append(parts, Quote(k))
		}
	}
	if _, err := t.Run(ctx, strings.Join(parts, " ")); err != nil {
		return fmt.Errorf("send-keys %s: %w", paneID, err)
	}
	return nil
}

var keyNames = map[string]bool{"Enter": true, "Escape": true, "Tab": true, "BSpace": true, "Space": true, "Up": true, "Down": true, "Left": true, "Right": true}

func isKeyName(k string) bool {
	return keyNames[k] || strings.HasPrefix(k, "C-") || strings.HasPrefix(k, "M-")
}

// ShellQuote renders argv as single-quoted words for a POSIX shell.
func ShellQuote(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(parts, " ")
}

// TypeCommand types argv into the pane's shell and presses Enter. The
// leading space keeps it out of history-ignore-space shells' history.
func TypeCommand(ctx context.Context, t Transport, paneID string, argv []string) error {
	cmd := fmt.Sprintf("send-keys -t %s %s Enter", Quote(paneID), Quote(" "+ShellQuote(argv)))
	if _, err := t.Run(ctx, cmd); err != nil {
		return fmt.Errorf("type command into %s: %w", paneID, err)
	}
	return nil
}

var shells = map[string]bool{"bash": true, "zsh": true, "sh": true, "fish": true, "dash": true}

// State reports the pane's foreground process (first non-shell process in
// the pane's subtree, else the shell) and its last 50 lines.
func State(ctx context.Context, t Transport, paneID string, procs *procx.Table) (*PaneState, error) {
	lines, err := t.Run(ctx, fmt.Sprintf(`list-panes -t %s -F "#{pane_pid}"`, Quote(paneID)))
	if err != nil {
		return nil, fmt.Errorf("list-panes %s: %w", paneID, err)
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("pane %s: no such pane", paneID)
	}
	panePID, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return nil, fmt.Errorf("pane %s: pane_pid %q: %w", paneID, lines[0], err)
	}
	st := &PaneState{PaneID: paneID}
	shell, ok := procs.Get(panePID)
	if !ok {
		return nil, fmt.Errorf("pane %s: pid %d not in the process table", paneID, panePID)
	}
	st.Command, st.Argv, st.PID = shell.Comm, shell.Cmdline, shell.PID
	for _, pid := range procs.Subtree(panePID) {
		if pid == panePID {
			continue
		}
		p, _ := procs.Get(pid)
		if shells[p.Comm] {
			continue
		}
		st.Command, st.Argv, st.PID = p.Comm, p.Cmdline, p.PID
		break
	}
	content, err := t.Run(ctx, fmt.Sprintf("capture-pane -p -S -50 -t %s", Quote(paneID)))
	if err != nil {
		return nil, fmt.Errorf("capture-pane %s: %w", paneID, err)
	}
	st.Content = content
	return st, nil
}
```

```go
// internal/tmuxx/prober.go
package tmuxx

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/session"
)

type prober struct {
	ctx    context.Context
	t      Transport
	procs  *procx.Table
	socket string
}

// Prober adapts a Transport to session.PaneProbe.
func Prober(ctx context.Context, t Transport, procs *procx.Table, socketPath string) session.PaneProbe {
	return &prober{ctx: ctx, t: t, procs: procs, socket: socketPath}
}

func (p *prober) PaneCommand(paneID string) ([]string, int, bool) {
	st, err := State(p.ctx, p.t, paneID, p.procs)
	if err != nil {
		return nil, 0, false
	}
	return st.Argv, st.PID, true
}

func (p *prober) FindWindow(sess, window string) ([]string, error) {
	target := "=" + sess + ":" + window
	if _, err := strconv.Atoi(window); err != nil {
		target = "=" + sess + ":=" + window // exact window-name match
	}
	lines, err := p.t.Run(p.ctx, fmt.Sprintf(`list-panes -t %s -F "#{pane_id}"`, Quote(target)))
	if err != nil {
		return nil, fmt.Errorf("window %s %s: %w", sess, window, err)
	}
	var out []string
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("window %s %s: no panes", sess, window)
	}
	return out, nil
}

func (p *prober) SocketPath() string { return p.socket }

// ListPanes implements session.PaneProbe.ListPanes (Plan 01 addition) for
// suspended-pane discovery in session.Load: every pane on the server.
func (p *prober) ListPanes() ([]session.PaneInfo, error) {
	lines, err := p.t.Run(p.ctx, `list-panes -a -F "#{session_name} #{window_id} #{pane_id}"`)
	if err != nil {
		return nil, fmt.Errorf("list-panes -a: %w", err)
	}
	var out []session.PaneInfo
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) != 3 {
			continue
		}
		out = append(out, session.PaneInfo{Session: f[0], WindowID: f[1], PaneID: f[2]})
	}
	return out, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/tmuxx/ -run 'TestCapture|TestSendKeys|TestTypeCommand|TestState|TestProber' -v`
Expected: PASS. (`fakeProc` writes the `/proc/<pid>/stat` format procx.Scan parses: `pid (comm) state ppid …` with start time as field 22 — if procx.Scan requires more fields, extend the fake stat line with zeros; the parser only reads fields 2–4 and 22.)

- [ ] **Step 5: Commit**

```bash
git add internal/tmuxx/pane.go internal/tmuxx/prober.go internal/tmuxx/pane_test.go
git commit -m "feat(tmuxx): Capture, SendKeys, TypeCommand, State and the session.PaneProbe adapter"
```

---

### Task 12: tmuxx live test against a real tmux server

**Files:**
- Test: `internal/tmuxx/live_test.go` (build tag `tmuxlive`)

- [ ] **Step 1: Write the live test**

```go
//go:build tmuxlive

package tmuxx

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/procx"
)

func TestLiveOpenCaptureTypeKill(t *testing.T) {
	sock, dir := StartTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	found, err := FindServer(dir, "nope", "")
	if err != nil || found != sock {
		t.Fatalf("FindServer = %q %v, want %q", found, err, sock)
	}
	tr, err := DialControl(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	cwd := t.TempDir()

	// 1. New group → new-session.
	ref, err := OpenWindow(ctx, tr, &Plan{SocketPath: sock, Group: "work", WindowName: "claude", AutoRename: false, Cwd: cwd, CreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Session != "work" {
		t.Fatalf("ref = %+v", ref)
	}
	facts, err := Describe(ctx, tr, ref.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	if facts.WindowName != "claude" || facts.AutoRename || facts.PaneCwd != cwd {
		t.Errorf("facts = %+v", facts)
	}

	// 2. Existing grouped session → new-window in the base session.
	if _, err := tr.Run(ctx, `new-session -d -t "work" -s "work-2"`); err != nil {
		t.Fatal(err)
	}
	ref2, err := OpenWindow(ctx, tr, &Plan{SocketPath: sock, Group: "work", WindowName: "second", AutoRename: true, Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if ref2.Session != "work" || ref2.WindowID == ref.WindowID {
		t.Errorf("ref2 = %+v", ref2)
	}

	// 3. Type a command, capture it, inspect state.
	if err := TypeCommand(ctx, tr, ref.PaneID, []string{"printf", "teleport-marker-%s\\n", "ok"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, err := Capture(ctx, tr, ref.PaneID)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "teleport-marker-ok") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("typed command did not run; pane:\n%s", out)
		}
		time.Sleep(100 * time.Millisecond)
	}
	tb, err := procx.Scan("/proc")
	if err != nil {
		t.Fatal(err)
	}
	st, err := State(ctx, tr, ref.PaneID, tb)
	if err != nil {
		t.Fatal(err)
	}
	if !shells[st.Command] {
		t.Errorf("idle pane foreground = %q, want a shell", st.Command)
	}
	if len(st.Content) == 0 || !strings.Contains(strings.Join(st.Content, "\n"), "teleport-marker-ok") {
		t.Errorf("State.Content = %q", st.Content)
	}

	// 4. Kill the second window; the first survives.
	if err := KillWindow(ctx, tr, ref2.WindowID); err != nil {
		t.Fatal(err)
	}
	if _, err := Describe(ctx, tr, ref2.PaneID); err == nil {
		t.Error("killed pane still described")
	}
	if _, err := Describe(ctx, tr, ref.PaneID); err != nil {
		t.Errorf("first pane gone: %v", err)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test -race -tags tmuxlive ./internal/tmuxx/ -run TestLive -v`
Expected: PASS with tmux ≥ 3.3 installed; SKIP otherwise.

- [ ] **Step 3: Commit**

```bash
git add internal/tmuxx/live_test.go
git commit -m "test(tmuxx): live tmux server test for open/capture/type/kill"
```

---

## Part B — internal/remote: the Plan 03 operations

Plan 02 delivered `remote.Local`, `remote.Client`, `remote.Server` with the git/tmux/claude ops returning `&Error{Code: "unavailable"}` and `internal/remote/plan03_types.go` holding opaque aliases for the gitx/tmuxx types. Tasks 13–16 replace the aliases, implement the ops on `Local`, add the ops to the `Server` dispatch table and `Client`, and add the streams. Names of Plan 02 internals used below are Plan 02's actual identifiers (Plan 02 Tasks 14–16): `l.paths` (the `session.Paths`), `l.selfExe`, `l.opts` (`LocalOptions`; log through `l.opts.Logf`, which `NewLocal` guarantees non-nil), `l.jobDir(jobID)`/`l.stagingDir(jobID)`, `c.call(ctx, op string, args any, result any) error` on `Client`, and the `dispatch` map (`map[string]handler`, `handler func(ctx, ep Endpoint, args json.RawMessage) (any, error)`) in `server.go`.

**Contract this plan relies on from Plan 02 (verify at the start of Task 13 by reading `internal/remote/local.go` and add whichever is missing there, with a test):**

1. `Local.ManifestDiff(ctx, m, jobID)` saves `m` to `job.Dir(l.paths.DataDir, jobID)/manifest.json` before diffing (the dest must hold the manifest for `Install` and for stream receive).
2. `Local.JournalPut(ctx, j)` writes the journal into `job.Dir(l.paths.DataDir, j.ID)/job.json` on that host; `JournalGet` reads it back.
3. `Local.Install(ctx, m, jobID)` runs `transfer.Diff` again and calls `transfer.Install(ctx, m, st, job.StagingDir(...), l.paths, extras)` where `extras` is read from `jobs/<id>/extras.json`, written by `Local.PutInstallExtras(ctx, jobID, extra)` / `Client.PutInstallExtras` (op `install-extras`; Plan 02 Task 15/16, not on `Endpoint`). The orchestrator's `install` step therefore calls `PutInstallExtras` with `Plan.Extras` before `Install` (Task 20 `runInstall`, via `interface{ PutInstallExtras(context.Context, string, transfer.InstallExtras) error }`). `planView` (Task 16) is used only for the streams (`Statuses`, `Git`); its `Extras` field is informational.
4. `Client.OpenStream(ctx, kind, jobID, streamID)` runs `<exe> remote stream <kind> <jobID> <streamID>` over ssh and returns the process's stdin/stdout as one `io.ReadWriteCloser`; `Close` closes stdin, drains, and returns the remote exit error.

---

### Task 13: remote — real gitx/tmuxx types, failure markers, git ops

**Files:**
- Delete: `internal/remote/plan03_types.go`
- Create: `internal/remote/failure_markers.go`, `internal/remote/local_git.go`
- Modify: `internal/remote/endpoint.go` (the `Endpoint` interface: import `gitx`/`tmuxx`, add the methods listed under Interface additions), `internal/remote/local.go` (remove the "unavailable" stubs for the ops implemented here)
- Test: `internal/remote/failure_markers_test.go`, `internal/remote/local_git_test.go`

**Interfaces:**
- Consumes: `gitx.Inspect`, `gitx.DestStateOf`, `gitx.Files`, `gitx.Attach`, `gitx.Plan`, `job.StagingDir`.
- Produces: `FailureMarkers []string`, `HasFailureMarker(text string) (string, bool)`; `Local.InventoryGit`, `Local.GitDestState`, `Local.GitFiles(ctx, p *gitx.Plan, excludes []string, includeIgnored bool) ([]session.FileEntry, error)`, `Local.GitAttach`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/remote/failure_markers_test.go
package remote

import "testing"

func TestHasFailureMarker(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"╭─ Welcome to Claude Code ─╮\n> ", ""},
		{"Not logged in · Please run /login", "Not logged in"},
		{"Error: Invalid API key · Fix external API key", "Invalid API key"},
		{"No conversation found with session ID: 3f2a9c1e", "No conversation found"},
		{"Session 3f2a… not found for this project", "not found for"},
		{"Unable to resume session", "Unable to resume"},
		{"OAuth token has expired. Please run /login", "OAuth token has expired"},
	}
	for _, c := range cases {
		got, ok := HasFailureMarker(c.text)
		if (c.want != "") != ok || got != c.want {
			t.Errorf("HasFailureMarker(%q) = %q,%v want %q", c.text, got, ok, c.want)
		}
	}
}
```

```go
// internal/remote/local_git_test.go
package remote

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
)

func testPaths(t *testing.T) session.Paths {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home", "alice")
	os.MkdirAll(home, 0o700)
	return session.Paths{Home: home, ConfigDir: filepath.Join(home, ".claude"), GlobalJSON: filepath.Join(home, ".claude.json"), DataDir: filepath.Join(home, ".local", "share", "claude-teleport")}
}

func gitc(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=alice", "GIT_AUTHOR_EMAIL=a@laptop.example", "GIT_COMMITTER_NAME=alice", "GIT_COMMITTER_EMAIL=a@laptop.example", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestLocalInventoryGitAndDestState(t *testing.T) {
	p := testPaths(t)
	repo := filepath.Join(p.Home, "x")
	os.MkdirAll(repo, 0o755)
	gitc(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a"), 0o644)
	gitc(t, repo, "add", "a.txt")
	gitc(t, repo, "commit", "-q", "-m", "init")
	l := NewLocal(p, "/usr/local/bin/claude-teleport", LocalOptions{ProcRoot: "/proc"})
	info, err := l.InventoryGit(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if info.Branch != "main" || info.MainDir != repo {
		t.Errorf("info = %+v", info)
	}
	if _, err := l.InventoryGit(context.Background(), t.TempDir()); err == nil {
		t.Error("non-repo must return an error")
	} else if e, ok := err.(*Error); !ok || e.Code != "not-found" {
		t.Errorf("non-repo error = %v, want *Error{Code: not-found}", err)
	}
	ds, err := l.GitDestState(context.Background(), repo, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !ds.MainExists || !ds.Clean {
		t.Errorf("dest state = %+v", ds)
	}
}

func TestLocalGitAttachUsesStagingPaths(t *testing.T) {
	p := testPaths(t)
	main := filepath.Join(p.Home, "x")
	os.MkdirAll(main, 0o755)
	gitc(t, main, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(main, "a.txt"), []byte("a"), 0o644)
	gitc(t, main, "add", "a.txt")
	gitc(t, main, "commit", "-q", "-m", "init")
	tip := strings.TrimSpace(gitc(t, main, "rev-parse", "HEAD"))
	root := tip
	jobID := "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"
	staging := job.StagingDir(p.DataDir, jobID)
	os.MkdirAll(staging, 0o700)
	os.WriteFile(filepath.Join(staging, "7"), []byte("untracked\n"), 0o644)
	idx, _ := os.ReadFile(filepath.Join(main, ".git", "index"))
	os.WriteFile(filepath.Join(staging, "8"), idx, 0o644)

	w := filepath.Join(main, ".worktrees", "feat")
	plan := &gitx.Plan{Mode: gitx.ModeExistingMain, SrcMain: "/home/alice/x", SrcWorktree: "/home/alice/x/.worktrees/feat",
		DstMain: main, DstWorktree: w, Linked: true, WorktreeName: "feat", Branch: "feat", Tip: tip, NeedPack: false,
		IndexRel: ".git/worktrees/feat/index", IndexEntryID: 8,
		DirtyEntries: map[string]int{filepath.Join(w, "new.txt"): 7}}
	_ = root
	l := NewLocal(p, "/usr/local/bin/claude-teleport", LocalOptions{ProcRoot: "/proc"})
	if err := l.GitAttach(context.Background(), plan, jobID); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(w, "new.txt")); err != nil || string(b) != "untracked\n" {
		t.Errorf("dirty file not applied: %v %q", err, b)
	}
	if got := strings.TrimSpace(gitc(t, w, "rev-parse", "--abbrev-ref", "HEAD")); got != "feat" {
		t.Errorf("worktree branch = %q", got)
	}
	if !strings.Contains(gitc(t, main, "worktree", "list"), w) {
		t.Error("git does not list the new worktree")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/remote/ -run 'TestHasFailureMarker|TestLocalInventoryGit|TestLocalGitAttach' -v`
Expected: FAIL — `undefined: HasFailureMarker`; `InventoryGit` returns the "unavailable" error.

- [ ] **Step 3: Delete the aliases, import the real types, implement the git ops**

Delete `internal/remote/plan03_types.go`. In `internal/remote/endpoint.go` replace every alias with the real type (`*gitx.Info`, `*gitx.DestState`, `*gitx.Plan`, `*tmuxx.Facts`, `*tmuxx.Plan`, `*tmuxx.PaneState`) and set `LocalOptions.Tmux tmuxx.Dialer`. Add to the `Endpoint` interface:

```go
	// Plan 03 additions (see "Interface additions").
	GitFiles(ctx context.Context, p *gitx.Plan, excludes []string, includeIgnored bool) ([]session.FileEntry, error)
	GitSourceFacts(ctx context.Context, mainDir, indexRel, tip, destTip string) (*gitx.SourceFacts, error)
	BuildManifest(ctx context.Context, jobID string, id session.ID, srcHost, dstHost string, files []session.FileEntry, pm session.PathMap) (*transfer.Manifest, error)
	SessionExtras(ctx context.Context, id session.ID, pm session.PathMap) (*transfer.InstallExtras, error)
	TmuxSessions(ctx context.Context, socketPath string) ([]tmuxx.SessionInfo, error)
	KillWindow(ctx context.Context, ref *session.TmuxRef) error
	ClaudeStatus(ctx context.Context, id session.ID) (*session.Registry, bool, error)
	ListSessions(ctx context.Context) ([]SessionSummary, error)
	Cleanup(ctx context.Context, jobID string) error
```

with

```go
// SessionSummary is one row of `list` (local or --host).
type SessionSummary struct {
	ID     session.ID    `json:"id"`
	State  string        `json:"state"`
	Cwd    string        `json:"cwd"`
	Branch string        `json:"branch"`
	Name   string        `json:"name"`
	Tmux   string        `json:"tmux"`
	Version string       `json:"version"`
	LastTS string        `json:"last_ts"`
}
```

```go
// internal/remote/failure_markers.go
package remote

import "strings"

// FailureMarkers are substrings of Claude Code's output that mean the
// destination did NOT resume (spec §6.2). Update when Claude changes its
// wording; TestHasFailureMarker pins the current set.
var FailureMarkers = []string{
	"Not logged in",
	"Please run /login",
	"Invalid API key",
	"No conversation found",
	"not found for",
	"Unable to resume",
	"OAuth token has expired",
}

// HasFailureMarker returns the first marker found in text.
func HasFailureMarker(text string) (string, bool) {
	for _, m := range FailureMarkers {
		if strings.Contains(text, m) {
			return m, true
		}
	}
	return "", false
}
```

```go
// internal/remote/local_git.go
package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
)

func (l *Local) InventoryGit(ctx context.Context, cwd string) (*gitx.Info, error) {
	info, err := gitx.Inspect(cwd)
	if errors.Is(err, gitx.ErrNotRepo) {
		return nil, &Error{Code: "not-found", Message: err.Error()}
	}
	if err != nil {
		return nil, &Error{Code: "internal", Message: fmt.Sprintf("inventory-git %s: %v", cwd, err)}
	}
	return info, nil
}

func (l *Local) GitDestState(ctx context.Context, mainDir, worktreeDir, branch string) (*gitx.DestState, error) {
	ds, err := gitx.DestStateOf(mainDir, worktreeDir, branch)
	if err != nil {
		return nil, &Error{Code: "internal", Message: fmt.Sprintf("git-dest-state %s: %v", mainDir, err)}
	}
	return ds, nil
}

func (l *Local) GitFiles(ctx context.Context, p *gitx.Plan, excludes []string, includeIgnored bool) ([]session.FileEntry, error) {
	files, err := gitx.Files(p, excludes, includeIgnored)
	if err != nil {
		return nil, &Error{Code: "internal", Message: fmt.Sprintf("git-files: %v", err)}
	}
	return files, nil
}

// GitSourceFacts answers the two source-side questions PlanTransfer and
// the pack need: is the destination's branch tip an ancestor of ours, and
// which staged blobs are not in the tip's tree.
func (l *Local) GitSourceFacts(ctx context.Context, mainDir, indexRel, tip, destTip string) (*gitx.SourceFacts, error) {
	f, err := gitx.SourceFactsOf(mainDir, indexRel, tip, destTip)
	if err != nil {
		return nil, &Error{Code: "internal", Message: fmt.Sprintf("git-source-facts: %v", err)}
	}
	return f, nil
}

// GitAttach resolves the pack and dirty files from this host's staging
// directory (jobs are keyed by session id, staging by job id) and calls
// gitx.Attach. The pack arrives via the pack stream (Task 16) as
// staging/<job>/objects.pack; dirty files are staged manifest entries.
func (l *Local) GitAttach(ctx context.Context, p *gitx.Plan, jobID string) error {
	staging := job.StagingDir(l.paths.DataDir, jobID)
	packPath := ""
	if p.NeedPack {
		packPath = filepath.Join(staging, "objects.pack")
		if _, err := os.Stat(packPath); err != nil {
			return &Error{Code: "not-found", Message: fmt.Sprintf("git-attach: pack %s: %v", packPath, err)}
		}
	}
	dirty := map[string]string{}
	for dst, id := range p.DirtyEntries {
		dirty[dst] = filepath.Join(staging, strconv.Itoa(id))
	}
	if p.IndexEntryID != 0 {
		dirty[filepath.Join(p.DstMain, p.IndexRel)] = filepath.Join(staging, strconv.Itoa(p.IndexEntryID))
	}
	for dst, staged := range dirty {
		if _, err := os.Stat(staged); err != nil {
			return &Error{Code: "not-found", Message: fmt.Sprintf("git-attach: staged file for %s: %v", dst, err)}
		}
	}
	if err := gitx.Attach(ctx, p, packPath, dirty); err != nil {
		var re *gitx.RefuseError
		if errors.As(err, &re) {
			return &Error{Code: "conflict", Message: re.Reason}
		}
		return &Error{Code: "internal", Message: fmt.Sprintf("git-attach: %v", err)}
	}
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go build ./... && go test -race ./internal/remote/ -run 'TestHasFailureMarker|TestLocalInventoryGit|TestLocalGitAttach' -v`
Expected: the build succeeds everywhere the aliases were used (Server/Client marshal the real structs as JSON — `gitx.Plan` and `tmuxx.*` are plain structs), tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/remote
git commit -m "feat(remote): real gitx/tmuxx types, failure markers, git ops on Local"
```

---

### Task 14: remote — tmux and Claude ops on Local

**Files:**
- Create: `internal/remote/local_tmux.go`, `internal/remote/local_claude.go`
- Modify: `internal/remote/local.go` (`LocalOptions` gains `TmuxSocketDir string`, `Sleep func(time.Duration)`; `Local` gains `procs func() (*procx.Table, error)` built from `opts.ProcRoot`)
- Test: `internal/remote/local_tmux_test.go`, `internal/remote/local_claude_test.go`

**Interfaces:**
- Consumes: `tmuxx.*`, `procx.Scan`, `procx.WaitGone`, `procx.RegistryForSession`, `session.Registry`, `HasFailureMarker`.
- Produces: `Local.InventoryTmux`, `TmuxSessions`, `OpenWindow`, `Capture`, `TypeCommand`, `PaneState`, `KillWindow`, `StartClaude`, `ConfirmClaude`, `ExitClaude`, `ClaudeStatus`.

- [ ] **Step 1: Write the failing tests (Fake transport; a fake registry dir; a fake /proc)**

```go
// internal/remote/local_tmux_test.go
package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

const describeCmd = `list-panes -a -F "#{session_name}	#{session_group}	#{window_id}	#{window_index}	#{window_name}	#{pane_id}	#{pane_current_path}	#{pane_current_command}	#{pane_pid}	#{history_size}	#{pane_title}"`

func fakeDialer(f *tmuxx.Fake) tmuxx.Dialer {
	return func(context.Context, string) (tmuxx.Transport, error) { return f, nil }
}

func TestLocalInventoryTmuxDescribesPane(t *testing.T) {
	p := testPaths(t)
	f := &tmuxx.Fake{Replies: map[string][]string{
		describeCmd: {"main\tmain\t@3\t2\tclaude\t%7\t/home/alice/x\tclaude\t5150\t9\tt"},
		`show-options -wv -t "@3" automatic-rename`: {"off"},
	}}
	l := NewLocal(p, "/usr/local/bin/claude-teleport", LocalOptions{ProcRoot: "/proc", Tmux: fakeDialer(f), TmuxSocketDir: t.TempDir()})
	facts, err := l.InventoryTmux(context.Background(), &session.TmuxRef{SocketPath: "/tmp/tmux-1000/default", Session: "main", WindowID: "@3", PaneID: "%7"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if facts.SocketPath != "/tmp/tmux-1000/default" || facts.Group != "main" || facts.AutoRename {
		t.Errorf("facts = %+v", facts)
	}
}

func TestLocalInventoryTmuxUnavailable(t *testing.T) {
	l := NewLocal(testPaths(t), "x", LocalOptions{ProcRoot: "/proc"})
	_, err := l.InventoryTmux(context.Background(), nil, "")
	if e, ok := err.(*Error); !ok || e.Code != "unavailable" {
		t.Fatalf("err = %v, want unavailable", err)
	}
}

func TestLocalCaptureWritesJobFile(t *testing.T) {
	p := testPaths(t)
	f := &tmuxx.Fake{Replies: map[string][]string{`capture-pane -epJ -S - -t "%7"`: {"hello", "world"}}}
	l := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc", Tmux: fakeDialer(f)})
	jobID := "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"
	if err := l.Capture(context.Background(), &session.TmuxRef{SocketPath: "/s", PaneID: "%7"}, jobID); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(job.Dir(p.DataDir, jobID), "capture.txt"))
	if err != nil || string(b) != "hello\nworld\n" {
		t.Errorf("capture.txt = %q %v", b, err)
	}
}

func TestLocalOpenWindowAndKill(t *testing.T) {
	p := testPaths(t)
	f := &tmuxx.Fake{Replies: map[string][]string{
		`list-sessions -F "#{session_name}	#{session_group}"`: {},
		`new-session -d -s "work" -n "claude" -c "/home/alice/x" -P -F "#{pane_id}	#{window_id}	#{session_name}"`: {"%1\t@1\twork"},
		`kill-window -t "@1"`: {},
	}}
	l := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc", Tmux: fakeDialer(f)})
	ref, err := l.OpenWindow(context.Background(), &tmuxx.Plan{SocketPath: "/s", Group: "work", WindowName: "claude", AutoRename: true, Cwd: "/home/alice/x", CreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if ref.PaneID != "%1" || ref.SocketPath != "/s" {
		t.Errorf("ref = %+v", ref)
	}
	if err := l.KillWindow(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
}
```

```go
// internal/remote/local_claude_test.go
package remote

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

const sid = "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"

// fakeProcRoot writes /proc/<pid>/stat + cmdline for procx.Scan.
func fakeProcRoot(t *testing.T, procs [][4]string) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range procs {
		dir := filepath.Join(root, p[0])
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "stat"), []byte(p[0]+" ("+p[2]+") S "+p[1]+" 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 777 0 0"), 0o644)
		os.WriteFile(filepath.Join(dir, "cmdline"), []byte(p[3]), 0o644)
	}
	return root
}

func writeRegistry(t *testing.T, p session.Paths, pid int, status, tmux string) {
	t.Helper()
	os.MkdirAll(p.SessionsDir(), 0o700)
	b, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": sid, "cwd": "/home/alice/x", "procStart": "777", "version": "2.1.247", "status": status, "tmux": tmux, "updatedAt": time.Now().UnixMilli()})
	os.WriteFile(filepath.Join(p.SessionsDir(), "5150.json"), b, 0o600)
}

func TestConfirmClaudeSucceedsWhenIdleInOurPane(t *testing.T) {
	p := testPaths(t)
	proc := fakeProcRoot(t, [][4]string{{"100", "1", "bash", "bash\x00"}, {"5150", "100", "claude", "claude\x00--resume\x00" + sid + "\x00"}})
	f := &tmuxx.Fake{Replies: map[string][]string{`capture-pane -epJ -S - -t "%7"`: {"╭ Welcome ╮", "> "}}}
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: fakeDialer(f), Sleep: func(time.Duration) {}})
	ref := &session.TmuxRef{SocketPath: "/s", Session: "work", WindowID: "@1", PaneID: "%7"}
	writeRegistry(t, p, 5150, "busy", "work:@1.%7")
	go func() { time.Sleep(50 * time.Millisecond); writeRegistry(t, p, 5150, "idle", "work:@1.%7") }()
	reg, err := l.ConfirmClaude(context.Background(), ref, session.ID(sid), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if reg.PID != 5150 || reg.Status != "idle" {
		t.Errorf("reg = %+v", reg)
	}
}

func TestConfirmClaudeFailsOnMarker(t *testing.T) {
	p := testPaths(t)
	proc := fakeProcRoot(t, [][4]string{{"5150", "1", "claude", "claude\x00"}})
	f := &tmuxx.Fake{Replies: map[string][]string{`capture-pane -epJ -S - -t "%7"`: {"Not logged in · Please run /login"}}}
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: fakeDialer(f), Sleep: func(time.Duration) {}})
	writeRegistry(t, p, 5150, "idle", "work:@1.%7")
	_, err := l.ConfirmClaude(context.Background(), &session.TmuxRef{SocketPath: "/s", Session: "work", WindowID: "@1", PaneID: "%7"}, session.ID(sid), time.Second)
	if err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("err = %v, want marker failure", err)
	}
}

func TestConfirmClaudeRejectsWrongPane(t *testing.T) {
	p := testPaths(t)
	proc := fakeProcRoot(t, [][4]string{{"5150", "1", "claude", "claude\x00"}})
	f := &tmuxx.Fake{Replies: map[string][]string{`capture-pane -epJ -S - -t "%7"`: {"> "}}}
	slept := 0
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: fakeDialer(f), Sleep: func(time.Duration) { slept++ }})
	writeRegistry(t, p, 5150, "idle", "other:@9.%9")
	_, err := l.ConfirmClaude(context.Background(), &session.TmuxRef{SocketPath: "/s", Session: "work", WindowID: "@1", PaneID: "%7"}, session.ID(sid), 600*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "pane") {
		t.Fatalf("err = %v, want timeout mentioning the pane mismatch", err)
	}
	if slept == 0 {
		t.Error("expected polling")
	}
}

func TestExitClaudeInTmuxTypesSlashExit(t *testing.T) {
	p := testPaths(t)
	proc := fakeProcRoot(t, [][4]string{{"100", "1", "bash", "bash\x00"}}) // 5150 already gone
	f := &tmuxx.Fake{Replies: map[string][]string{`send-keys -t "%7" "/exit"`: {}, `send-keys -t "%7" Enter`: {}}}
	var slept []time.Duration
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: fakeDialer(f), Sleep: func(d time.Duration) { slept = append(slept, d) }})
	if err := l.ExitClaude(context.Background(), &session.TmuxRef{SocketPath: "/s", PaneID: "%7"}, 5150, "777", time.Second); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 2 || f.Calls[0] != `send-keys -t "%7" "/exit"` || f.Calls[1] != `send-keys -t "%7" Enter` {
		t.Errorf("calls = %v", f.Calls)
	}
	if len(slept) == 0 || slept[0] != 500*time.Millisecond {
		t.Errorf("expected a 500ms pause between /exit and Enter, got %v", slept)
	}
}

func TestExitClaudeTimesOut(t *testing.T) {
	p := testPaths(t)
	proc := fakeProcRoot(t, [][4]string{{"5150", "1", "claude", "claude\x00"}})
	f := &tmuxx.Fake{Default: []string{}}
	l := NewLocal(p, "x", LocalOptions{ProcRoot: proc, Tmux: fakeDialer(f), Sleep: func(time.Duration) {}})
	err := l.ExitClaude(context.Background(), &session.TmuxRef{SocketPath: "/s", PaneID: "%7"}, 5150, "777", 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "5150") {
		t.Fatalf("err = %v, want timeout naming the pid", err)
	}
}

func TestClaudeStatus(t *testing.T) {
	p := testPaths(t)
	l := NewLocal(p, "x", LocalOptions{ProcRoot: fakeProcRoot(t, nil)})
	if _, ok, err := l.ClaudeStatus(context.Background(), session.ID(sid)); ok || err != nil {
		t.Fatalf("absent: %v %v", ok, err)
	}
	writeRegistry(t, p, 5150, "idle", "")
	reg, ok, err := l.ClaudeStatus(context.Background(), session.ID(sid))
	if err != nil || !ok || reg.Status != "idle" {
		t.Fatalf("present: %+v %v %v", reg, ok, err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/remote/ -run 'TestLocalInventoryTmux|TestLocalCapture|TestLocalOpenWindow|TestConfirmClaude|TestExitClaude|TestClaudeStatus' -v`
Expected: FAIL — the ops still return "unavailable"; `LocalOptions` has no `Sleep`/`TmuxSocketDir`.

- [ ] **Step 3: Implement**

Add to `LocalOptions` in `internal/remote/local.go`:

```go
	TmuxSocketDir string               // /tmp/tmux-<uid> or $TMUX_TMPDIR/tmux-<uid>; cli computes it
	Sleep         func(time.Duration)  // nil = time.Sleep
```

and in `NewLocal` default `opts.Sleep = time.Sleep` when nil and set `l.procs = func() (*procx.Table, error) { return procx.Scan(opts.ProcRoot) }`.

```go
// internal/remote/local_tmux.go
package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

// dial opens a control-mode transport; callers must Close it.
func (l *Local) dial(ctx context.Context, socketPath string) (tmuxx.Transport, error) {
	if l.opts.Tmux == nil {
		return nil, &Error{Code: "unavailable", Message: "tmux is not available on this host"}
	}
	t, err := l.opts.Tmux(ctx, socketPath)
	if errors.Is(err, tmuxx.ErrNoServer) {
		return nil, &Error{Code: "unavailable", Message: err.Error()}
	}
	if err != nil {
		return nil, &Error{Code: "internal", Message: fmt.Sprintf("tmux dial %s: %v", socketPath, err)}
	}
	return t, nil
}

// InventoryTmux describes the pane in ref, or — with ref == nil — only
// discovers the server (spec §9) and returns Facts with SocketPath set.
func (l *Local) InventoryTmux(ctx context.Context, ref *session.TmuxRef, preferredSocket string) (*tmuxx.Facts, error) {
	if l.opts.Tmux == nil {
		return nil, &Error{Code: "unavailable", Message: "tmux is not available on this host"}
	}
	if ref == nil {
		sock, err := tmuxx.FindServer(l.opts.TmuxSocketDir, preferredSocket, "")
		if err != nil {
			return nil, &Error{Code: "unavailable", Message: err.Error()}
		}
		return &tmuxx.Facts{SocketPath: sock}, nil
	}
	t, err := l.dial(ctx, ref.SocketPath)
	if err != nil {
		return nil, err
	}
	defer t.Close()
	facts, err := tmuxx.Describe(ctx, t, ref.PaneID)
	if err != nil {
		return nil, &Error{Code: "not-found", Message: err.Error()}
	}
	facts.SocketPath = ref.SocketPath
	return facts, nil
}

func (l *Local) TmuxSessions(ctx context.Context, socketPath string) ([]tmuxx.SessionInfo, error) {
	t, err := l.dial(ctx, socketPath)
	if err != nil {
		return nil, err
	}
	defer t.Close()
	return tmuxx.ListSessions(ctx, t)
}

func (l *Local) OpenWindow(ctx context.Context, p *tmuxx.Plan) (*session.TmuxRef, error) {
	t, err := l.dial(ctx, p.SocketPath)
	if err != nil {
		return nil, err
	}
	defer t.Close()
	ref, err := tmuxx.OpenWindow(ctx, t, p)
	if err != nil {
		return nil, &Error{Code: "internal", Message: err.Error()}
	}
	return ref, nil
}

// Capture writes jobs/<jobID>/capture.txt on this host.
func (l *Local) Capture(ctx context.Context, ref *session.TmuxRef, jobID string) error {
	t, err := l.dial(ctx, ref.SocketPath)
	if err != nil {
		return err
	}
	defer t.Close()
	data, err := tmuxx.Capture(ctx, t, ref.PaneID)
	if err != nil {
		return &Error{Code: "internal", Message: err.Error()}
	}
	dir := job.Dir(l.paths.DataDir, jobID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "capture.txt.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "capture.txt"))
}

func (l *Local) TypeCommand(ctx context.Context, ref *session.TmuxRef, argv []string) error {
	t, err := l.dial(ctx, ref.SocketPath)
	if err != nil {
		return err
	}
	defer t.Close()
	return tmuxx.TypeCommand(ctx, t, ref.PaneID, argv)
}

func (l *Local) PaneState(ctx context.Context, ref *session.TmuxRef) (*tmuxx.PaneState, error) {
	t, err := l.dial(ctx, ref.SocketPath)
	if err != nil {
		return nil, err
	}
	defer t.Close()
	procs, err := l.procs()
	if err != nil {
		return nil, err
	}
	st, err := tmuxx.State(ctx, t, ref.PaneID, procs)
	if err != nil {
		return nil, &Error{Code: "not-found", Message: err.Error()}
	}
	return st, nil
}

func (l *Local) KillWindow(ctx context.Context, ref *session.TmuxRef) error {
	t, err := l.dial(ctx, ref.SocketPath)
	if err != nil {
		return err
	}
	defer t.Close()
	return tmuxx.KillWindow(ctx, t, ref.WindowID)
}
```

```go
// internal/remote/local_claude.go
package remote

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"time"

	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

const confirmPoll = 250 * time.Millisecond

// StartClaude types the placeholder/claude argv into the destination pane.
func (l *Local) StartClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, jobID string, argv []string) error {
	l.opts.Logf("start: typing %q into %s", strings.Join(argv, " "), ref.PaneID)
	return l.TypeCommand(ctx, ref, argv)
}

// ClaudeStatus returns the registry entry for id when its pid is alive.
func (l *Local) ClaudeStatus(ctx context.Context, id session.ID) (*session.Registry, bool, error) {
	reg, ok, err := procx.RegistryForSession(l.paths.SessionsDir(), id)
	if err != nil || !ok {
		return nil, false, err
	}
	procs, err := l.procs()
	if err != nil {
		return nil, false, err
	}
	if !procs.Alive(reg.PID, reg.ProcStart) {
		return nil, false, nil
	}
	return reg, true, nil
}

// ConfirmClaude implements spec §6.2: registry entry alive in our pane,
// no failure marker in the pane, status idle — all within timeout.
func (l *Local) ConfirmClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, timeout time.Duration) (*session.Registry, error) {
	wantTmux := ""
	if ref != nil {
		wantTmux = fmt.Sprintf("%s:%s.%s", ref.Session, ref.WindowID, ref.PaneID)
	}
	deadline := time.Now().Add(timeout)
	last := "no registry entry for the session yet"
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ref != nil {
			t, err := l.dial(ctx, ref.SocketPath)
			if err != nil {
				return nil, err
			}
			text, err := tmuxx.Capture(ctx, t, ref.PaneID)
			t.Close()
			if err != nil {
				return nil, &Error{Code: "internal", Message: err.Error()}
			}
			if m, hit := HasFailureMarker(string(text)); hit {
				return nil, &Error{Code: "conflict", Message: fmt.Sprintf("destination Claude did not resume: pane shows %q", m)}
			}
		}
		reg, ok, err := l.ClaudeStatus(ctx, id)
		if err != nil {
			return nil, err
		}
		switch {
		case !ok:
			last = "no live registry entry for the session"
		case wantTmux != "" && reg.Tmux != wantTmux:
			last = fmt.Sprintf("registry pane %q is not our pane %q", reg.Tmux, wantTmux)
		case reg.Status != "idle":
			last = fmt.Sprintf("registry status is %q, waiting for idle", reg.Status)
		default:
			return reg, nil
		}
		if time.Now().After(deadline) {
			return nil, &Error{Code: "conflict", Message: fmt.Sprintf("destination Claude not confirmed within %s: %s", timeout, last)}
		}
		l.opts.Sleep(confirmPoll)
	}
}

// ExitClaude implements spec §6.3: in tmux, /exit + Enter then wait for
// the pid to go; without a pane, SIGTERM then wait.
func (l *Local) ExitClaude(ctx context.Context, ref *session.TmuxRef, pid int, startTime string, timeout time.Duration) error {
	procs, err := l.procs()
	if err != nil {
		return err
	}
	if !procs.Alive(pid, startTime) {
		return nil // already gone
	}
	if ref != nil {
		t, err := l.dial(ctx, ref.SocketPath)
		if err != nil {
			return err
		}
		defer t.Close()
		if err := tmuxx.SendKeys(ctx, t, ref.PaneID, "/exit"); err != nil {
			return err
		}
		l.opts.Sleep(500 * time.Millisecond)
		if err := tmuxx.SendKeys(ctx, t, ref.PaneID, "Enter"); err != nil {
			return err
		}
	} else {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			return fmt.Errorf("SIGTERM pid %d: %w", pid, err)
		}
	}
	if err := procx.WaitGone(l.procs, pid, startTime, timeout, confirmPoll, l.opts.Sleep); err != nil {
		return &Error{Code: "conflict", Message: fmt.Sprintf("claude (pid %d) still running after %s: %v", pid, timeout, err)}
	}
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/remote/ -run 'TestLocalInventoryTmux|TestLocalCapture|TestLocalOpenWindow|TestConfirmClaude|TestExitClaude|TestClaudeStatus' -v`
Expected: PASS. `TestConfirmClaudeRejectsWrongPane` relies on the injected `Sleep` never sleeping and the 600 ms deadline being real wall-clock time — it runs the loop until `time.Now()` passes the deadline (a few thousand iterations; fine).

- [ ] **Step 5: Commit**

```bash
git add internal/remote
git commit -m "feat(remote): tmux and Claude ops on Local (confirm, exit, capture, open, type)"
```

---

### Task 15: remote.RunPtyResume — the no-tmux confirmation

**Files:**
- Create: `internal/remote/local_pty.go`
- Modify: `go.mod` (add `github.com/creack/pty`)
- Test: `internal/remote/local_pty_test.go`

**Why `github.com/creack/pty`:** spec §9 requires running `claude --resume <sid>` under a pty on the destination when there is no tmux; Claude refuses to run interactively without a tty. Go's standard library has no `openpty`/`posix_openpt` wrapper; `creack/pty` is a ~300-line pure-Go package (no cgo, `CGO_ENABLED=0` stays true) that does exactly this. It is used only here.

`RunPtyResume(ctx, id, cwd, timeout)`: start `claude --resume <id>` in `cwd` under a pty with `CLAUDE_CONFIG_DIR=<l.paths.ConfigDir>` (only when the config dir is not `$HOME/.claude`) and `TERM=xterm-256color`; read output into a 64 KiB ring; every 250 ms check `HasFailureMarker` on the ring and `ClaudeStatus(id)` for `idle`; on success write `"/exit\r"`, wait up to `timeout` for exit, else SIGKILL and error; on marker → error with the marker; on timeout → error with the last 20 lines.

- [ ] **Step 1: Write the failing test (uses test/fakeclaude on PATH)**

```go
// internal/remote/local_pty_test.go
package remote

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildFakeClaude compiles test/fakeclaude into a temp dir and prepends it
// to PATH as `claude`. Shared by the orchestrator tests (Task 22).
func buildFakeClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/mithro/go-claude-teleport/test/fakeclaude")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakeclaude: %v\n%s", err, out)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin
}

func TestRunPtyResumeConfirmsAndExits(t *testing.T) {
	buildFakeClaude(t)
	p := testPaths(t)
	t.Setenv("CLAUDE_CONFIG_DIR", p.ConfigDir)
	cwd := filepath.Join(p.Home, "x")
	os.MkdirAll(cwd, 0o755)
	// Seed a transcript so --resume finds the session.
	proj := p.ProjectDir(cwd)
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(`{"type":"user","sessionId":"`+sid+`","cwd":"`+cwd+`","timestamp":"2026-08-27T10:00:00Z","message":{"role":"user","content":"hi"}}`+"\n"), 0o600)
	l := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := l.RunPtyResume(ctx, sid, cwd, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	// fakeclaude removes its registry file on clean exit.
	entries, _ := os.ReadDir(p.SessionsDir())
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			t.Errorf("registry entry %s left behind", e.Name())
		}
	}
}

func TestRunPtyResumeReportsLoggedOut(t *testing.T) {
	buildFakeClaude(t)
	p := testPaths(t)
	t.Setenv("CLAUDE_CONFIG_DIR", p.ConfigDir)
	t.Setenv("FAKECLAUDE_FAIL", "not-logged-in")
	cwd := filepath.Join(p.Home, "x")
	os.MkdirAll(cwd, 0o755)
	l := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc"})
	err := l.RunPtyResume(context.Background(), sid, cwd, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("err = %v, want the marker", err)
	}
}
```

`FAKECLAUDE_FAIL=not-logged-in` is the Plan 01 fakeclaude switch (Plan 01 Task 19) that prints exactly `Not logged in · Please run /login` to stdout and exits 1 (spec §12: "can be told (env) to fail like a logged-out Claude").

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/remote/ -run TestRunPtyResume -v`
Expected: FAIL — "unavailable".

- [ ] **Step 3: Implement**

Run: `go get github.com/creack/pty@v1.1.24 && go mod tidy`

```go
// internal/remote/local_pty.go
package remote

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/mithro/go-claude-teleport/internal/session"
)

// ring keeps the last n bytes written to it.
type ring struct {
	mu  sync.Mutex
	buf []byte
	n   int
}

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.n {
		r.buf = r.buf[len(r.buf)-r.n:]
	}
	return len(p), nil
}

func (r *ring) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

// RunPtyResume runs `claude --resume <id>` under a pty in cwd, confirms
// per spec §6.2, then exits it (spec §9, no-tmux destination).
func (l *Local) RunPtyResume(ctx context.Context, id session.ID, cwd string, timeout time.Duration) error {
	cmd := exec.CommandContext(ctx, "claude", "--resume", string(id))
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	if filepath.Clean(l.paths.ConfigDir) != filepath.Join(l.paths.Home, ".claude") {
		cmd.Env = append(cmd.Env, "CLAUDE_CONFIG_DIR="+l.paths.ConfigDir)
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return &Error{Code: "internal", Message: fmt.Sprintf("pty-resume: start claude: %v", err)}
	}
	defer f.Close()
	out := &ring{n: 64 * 1024}
	done := make(chan error, 1)
	go func() { _, _ = io.Copy(out, f); done <- cmd.Wait() }()
	l.opts.Logf("pty-resume: started claude --resume %s (pid %d) in %s", id, cmd.Process.Pid, cwd)

	deadline := time.Now().Add(timeout)
	for {
		if m, hit := HasFailureMarker(out.String()); hit {
			cmd.Process.Kill()
			<-done
			return &Error{Code: "conflict", Message: fmt.Sprintf("destination Claude did not resume: output shows %q", m)}
		}
		select {
		case err := <-done:
			return &Error{Code: "conflict", Message: fmt.Sprintf("claude exited before confirming (%v); last output:\n%s", err, tail(out.String(), 20))}
		default:
		}
		reg, ok, err := l.ClaudeStatus(ctx, id)
		if err != nil {
			cmd.Process.Kill()
			<-done
			return err
		}
		if ok && reg.PID == cmd.Process.Pid && reg.Status == "idle" {
			break
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			<-done
			return &Error{Code: "conflict", Message: fmt.Sprintf("destination Claude not confirmed within %s; last output:\n%s", timeout, tail(out.String(), 20))}
		}
		l.opts.Sleep(confirmPoll)
	}
	if _, err := f.Write([]byte("/exit\r")); err != nil {
		return fmt.Errorf("pty-resume: write /exit: %w", err)
	}
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		cmd.Process.Kill()
		<-done
		return &Error{Code: "conflict", Message: fmt.Sprintf("claude (pid %d) did not exit within %s after /exit", cmd.Process.Pid, timeout)}
	}
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/remote/ -run TestRunPtyResume -v`
Expected: PASS (fakeclaude writes its registry entry with `status: idle` once it shows the prompt and honours `/exit`).

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/remote/local_pty.go internal/remote/local_pty_test.go
git commit -m "feat(remote): RunPtyResume confirms a no-tmux destination under a pty (adds creack/pty)"
```

---

### Task 16: remote — streams, transfer helpers, Server/Client wiring, PR B

**Files:**
- Create: `internal/remote/streams.go`, `internal/remote/local_transfer.go`, `internal/remote/ops_plan03.go`
- Modify: `internal/remote/server.go` (register the ops), `internal/remote/client.go` (add the methods), `internal/remote/local.go` (`OpenStream` for the four kinds)
- Test: `internal/remote/streams_test.go`, `internal/remote/ops_plan03_test.go`

**Interfaces:**
- Consumes: `transfer.Build/Load/Send/Receive/Need`, `gitx.WritePack`, `session.ReadIndexEntry/ExtractHistory/ReadProjectEntry/RewriteJSON`, `job.Open/Dir/StagingDir`.
- Produces: `planView` (unexported projection of the journal plan), `PipeStream`, stream semantics below, `Local.BuildManifest`, `Local.SessionExtras`, `Local.Cleanup`, `Local.ListSessions`, the `Server` ops and `Client` methods for every Plan 03 op.

**Stream semantics** (`OpenStream(ctx, kind, jobID, streamID)`; `streamID` carries the direction):

| kind | streamID | host role | what the host does |
|---|---|---|---|
| `tar` | `send:<n>` | source | `transfer.Send` of `Need(manifest, planView.Statuses)` to the stream |
| `tar` | `recv:<n>` | dest | `transfer.Receive` from the stream into `staging/<job>/` |
| `pack` | `send:<n>` | source | `gitx.WritePack(planView.Git.SrcMain, [Tip]+StagedBlobs, HaveTips)` to the stream |
| `pack` | `recv:<n>` | dest | copy the stream to `staging/<job>/objects.pack.part`, rename to `objects.pack` |
| `capture` | `send:<n>` | source | stream `jobs/<job>/capture.txt` |
| `log` | `send:<n>` | any | stream `jobs/<job>/log.txt` |

The driver always pumps: `io.Copy(dstStream, srcStream)`; when one side is `Local`, its `OpenStream` returns a `PipeStream` whose goroutine runs the same code `ServeStream` runs. The manifest is `job.Dir(dataDir, jobID)/manifest.json` on both hosts (saved by `BuildManifest` on the source and `ManifestDiff` on the dest).

- [ ] **Step 1: Write the failing tests**

```go
// internal/remote/streams_test.go
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

func TestPipeStreamPropagatesRunError(t *testing.T) {
	s := PipeStream(func(r io.Reader, w io.Writer) error { io.Copy(io.Discard, r); return io.ErrUnexpectedEOF })
	s.Write([]byte("x"))
	if err := s.Close(); err == nil {
		t.Fatal("Close must return the run error")
	}
}

func TestTarStreamSourceToDestInProcess(t *testing.T) {
	src, dst := testPaths(t), testPaths(t)
	jobID := sid
	// One session file on the source.
	proj := src.ProjectDir("/home/alice/x")
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(`{"cwd":"/home/alice/x"}`+"\n"), 0o600)
	files := []session.FileEntry{{Root: src.ConfigDir, Rel: "projects/" + session.Munge("/home/alice/x") + "/" + sid + ".jsonl", Category: session.CatSession, Size: 24, Mode: 0o600, Rewrite: true}}
	srcEP := NewLocal(src, "x", LocalOptions{ProcRoot: "/proc"})
	dstEP := NewLocal(dst, "x", LocalOptions{ProcRoot: "/proc"})
	pm := session.NewPathMap(session.Mapping{From: src.Home, To: dst.Home})
	m, err := srcEP.BuildManifest(context.Background(), jobID, session.ID(sid), "laptop.example", "big-storage.example", files, pm)
	if err != nil {
		t.Fatal(err)
	}
	st, err := dstEP.ManifestDiff(context.Background(), m, jobID)
	if err != nil {
		t.Fatal(err)
	}
	// Both journals carry the statuses in the plan projection.
	for _, ep := range []Endpoint{srcEP, dstEP} {
		j, err := job.New(ep.Paths().DataDir, jobID)
		if err != nil {
			t.Fatal(err)
		}
		j.Plan, _ = json.Marshal(map[string]any{"statuses": st})
		if err := ep.JournalPut(context.Background(), j); err != nil {
			t.Fatal(err)
		}
	}
	r, err := srcEP.OpenStream(context.Background(), StreamTar, jobID, "send:1")
	if err != nil {
		t.Fatal(err)
	}
	w, err := dstEP.OpenStream(context.Background(), StreamTar, jobID, "recv:1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(w, r); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(job.StagingDir(dst.DataDir, jobID), "1")
	b, err := os.ReadFile(staged)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(dst.Home)) {
		t.Errorf("staged content not rewritten: %q", b)
	}
	st2, _ := dstEP.ManifestDiff(context.Background(), m, jobID)
	if st2[1] != transfer.StagedSame {
		t.Errorf("after receive status = %v, want staged-same", st2[1])
	}
}

func TestPackStreamRecvWritesObjectsPack(t *testing.T) {
	dst := testPaths(t)
	dstEP := NewLocal(dst, "x", LocalOptions{ProcRoot: "/proc"})
	w, err := dstEP.OpenStream(context.Background(), StreamPack, sid, "recv:1")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("PACKdata"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(job.StagingDir(dst.DataDir, sid), "objects.pack"))
	if err != nil || string(b) != "PACKdata" {
		t.Errorf("objects.pack = %q %v", b, err)
	}
}
```

```go
// internal/remote/ops_plan03_test.go
package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// roundTrip drives Serve over in-memory pipes and returns a Client-like
// caller for one op, decoding into result.
func roundTrip(t *testing.T, ep Endpoint, op string, args any, result any) *Error {
	t.Helper()
	cr, cw := io.Pipe() // client -> server
	sr, sw := io.Pipe() // server -> client
	go Serve(context.Background(), cr, sw, ep)
	a, _ := json.Marshal(args)
	req, _ := json.Marshal(Request{ID: 1, Op: op, Args: a})
	go func() { cw.Write(append(req, '\n')); cw.Close() }()
	line, err := bufio.NewReader(sr).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		return resp.Error
	}
	if result != nil {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			t.Fatal(err)
		}
	}
	return nil
}

func TestServeInventoryGitOp(t *testing.T) {
	p := testPaths(t)
	repo := filepath.Join(p.Home, "x")
	os.MkdirAll(repo, 0o755)
	gitc(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a"), []byte("a"), 0o644)
	gitc(t, repo, "add", "a")
	gitc(t, repo, "commit", "-q", "-m", "i")
	ep := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc"})
	var info gitx.Info
	if e := roundTrip(t, ep, "inventory-git", inventoryGitArgs{Cwd: repo}, &info); e != nil {
		t.Fatal(e)
	}
	if info.Branch != "main" {
		t.Errorf("info = %+v", info)
	}
	if e := roundTrip(t, ep, "inventory-git", inventoryGitArgs{Cwd: t.TempDir()}, nil); e == nil || e.Code != "not-found" {
		t.Errorf("non-repo over the wire = %v", e)
	}
}

func TestServeSessionExtrasOp(t *testing.T) {
	p := testPaths(t)
	cwd := filepath.Join(p.Home, "x")
	proj := p.ProjectDir(cwd)
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, "sessions-index.json"), []byte(`{"version":1,"entries":[{"sessionId":"`+sid+`","fullPath":"`+proj+`/`+sid+`.jsonl","projectPath":"`+cwd+`","messageCount":1}],"originalPath":"`+cwd+`"}`), 0o600)
	os.WriteFile(p.HistoryFile(), []byte(`{"display":"hi","timestamp":1,"project":"`+cwd+`","sessionId":"`+sid+`"}`+"\n"), 0o600)
	os.WriteFile(p.GlobalJSON, []byte(`{"projects":{"`+cwd+`":{"hasTrustDialogAccepted":true}}}`), 0o600)
	ep := NewLocal(p, "x", LocalOptions{ProcRoot: "/proc"})
	pm := session.NewPathMap(session.Mapping{From: p.Home, To: "/home/bob"})
	var out extrasResult
	if e := roundTrip(t, ep, "session-extras", sessionExtrasArgs{ID: session.ID(sid), PathMap: pm}, &out); e != nil {
		t.Fatal(e)
	}
	if out.Extras.IndexEntry == nil || out.Extras.IndexEntry.ProjectPath != "/home/bob/x" {
		t.Errorf("index entry = %+v", out.Extras.IndexEntry)
	}
	if len(out.Extras.History) != 1 || !containsBytes(out.Extras.History[0], "/home/bob/x") {
		t.Errorf("history = %s", out.Extras.History)
	}
	if out.Extras.ProjectCwd != "/home/bob/x" || out.Extras.ProjectEntry["hasTrustDialogAccepted"] != true {
		t.Errorf("project = %q %v", out.Extras.ProjectCwd, out.Extras.ProjectEntry)
	}
}

func containsBytes(b json.RawMessage, s string) bool { return len(b) > 0 && string(b) != "" && bytesContains(b, s) }

func bytesContains(b []byte, s string) bool {
	return len(s) == 0 || len(b) >= len(s) && func() bool {
		for i := 0; i+len(s) <= len(b); i++ {
			if string(b[i:i+len(s)]) == s {
				return true
			}
		}
		return false
	}()
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/remote/ -run 'TestPipeStream|TestTarStream|TestPackStream|TestServeInventoryGit|TestServeSessionExtras' -v`
Expected: FAIL — `undefined: PipeStream`, `BuildManifest`, `inventoryGitArgs`, …

- [ ] **Step 3: Implement streams and helpers**

```go
// internal/remote/streams.go
package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// planView is the slice of orchestrate.Plan (stored opaquely in the
// journal) that remote needs. JSON keys match orchestrate.Plan's tags.
type planView struct {
	Statuses map[int]transfer.Status  `json:"statuses"`
	Git      *gitx.Plan               `json:"git"`
	Extras   *transfer.InstallExtras  `json:"extras"`
}

func (l *Local) planView(jobID string) (*planView, error) {
	j, ok, err := job.Open(l.paths.DataDir, jobID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &Error{Code: "not-found", Message: fmt.Sprintf("no journal for job %s on this host", jobID)}
	}
	var v planView
	if len(j.Plan) > 0 {
		if err := json.Unmarshal(j.Plan, &v); err != nil {
			return nil, fmt.Errorf("decode plan of job %s: %w", jobID, err)
		}
	}
	return &v, nil
}

// pipeStream adapts a run(r, w) function to io.ReadWriteCloser: bytes
// written go to r; bytes run writes to w come out of Read; Close ends
// the input, waits for run, and returns its error.
type pipeStream struct {
	inR, outR *io.PipeReader
	inW, outW *io.PipeWriter
	done      chan error
}

// PipeStream runs fn in a goroutine connected to the returned stream.
func PipeStream(fn func(r io.Reader, w io.Writer) error) io.ReadWriteCloser {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &pipeStream{inR: inR, inW: inW, outR: outR, outW: outW, done: make(chan error, 1)}
	go func() {
		err := fn(inR, outW)
		outW.CloseWithError(err)
		inR.CloseWithError(err)
		s.done <- err
	}()
	return s
}

func (s *pipeStream) Read(p []byte) (int, error)  { return s.outR.Read(p) }
func (s *pipeStream) Write(p []byte) (int, error) { return s.inW.Write(p) }
func (s *pipeStream) Close() error {
	s.inW.Close()
	err := <-s.done
	s.outR.Close()
	return err
}

// splitStreamID parses "send:<n>" / "recv:<n>".
func splitStreamID(id string) (dir string, err error) {
	dir, _, ok := strings.Cut(id, ":")
	if !ok || (dir != "send" && dir != "recv") {
		return "", &Error{Code: "usage", Message: fmt.Sprintf("stream id %q must be send:<n> or recv:<n>", id)}
	}
	return dir, nil
}

// runStream is the single implementation behind ServeStream and
// Local.OpenStream.
func (l *Local) runStream(ctx context.Context, kind StreamKind, jobID, streamID string, r io.Reader, w io.Writer) error {
	dir, err := splitStreamID(streamID)
	if err != nil {
		return err
	}
	jobDir := job.Dir(l.paths.DataDir, jobID)
	staging := job.StagingDir(l.paths.DataDir, jobID)
	switch {
	case kind == StreamTar && dir == "send":
		m, err := transfer.Load(filepath.Join(jobDir, "manifest.json"))
		if err != nil {
			return err
		}
		v, err := l.planView(jobID)
		if err != nil {
			return err
		}
		need := transfer.Need(m, v.Statuses)
		return transfer.Send(ctx, m, need, w, func(e transfer.Entry, n int64) { l.opts.Logf("send %s (%d bytes)", e.Src, n) })
	case kind == StreamTar && dir == "recv":
		m, err := transfer.Load(filepath.Join(jobDir, "manifest.json"))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(staging, 0o700); err != nil {
			return err
		}
		return transfer.Receive(ctx, m, r, staging, func(e transfer.Entry, n int64) { l.opts.Logf("recv %s (%d bytes)", e.Dst, n) })
	case kind == StreamPack && dir == "send":
		v, err := l.planView(jobID)
		if err != nil {
			return err
		}
		if v.Git == nil {
			return &Error{Code: "usage", Message: "pack stream: journal plan has no git plan"}
		}
		want := append([]string{v.Git.Tip}, v.Git.StagedBlobs...)
		return gitx.WritePack(ctx, v.Git.SrcMain, want, v.Git.HaveTips, w)
	case kind == StreamPack && dir == "recv":
		if err := os.MkdirAll(staging, 0o700); err != nil {
			return err
		}
		part := filepath.Join(staging, "objects.pack.part")
		f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, r); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		return os.Rename(part, filepath.Join(staging, "objects.pack"))
	case kind == StreamCapture && dir == "send":
		return copyFileTo(filepath.Join(jobDir, "capture.txt"), w)
	case kind == StreamLog && dir == "send":
		return copyFileTo(filepath.Join(jobDir, "log.txt"), w)
	}
	return &Error{Code: "usage", Message: fmt.Sprintf("unsupported stream %s %s", kind, streamID)}
}

func copyFileTo(path string, w io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// OpenStream (Local) runs the stream in-process.
func (l *Local) OpenStream(ctx context.Context, kind StreamKind, jobID, streamID string) (io.ReadWriteCloser, error) {
	if _, err := splitStreamID(streamID); err != nil {
		return nil, err
	}
	return PipeStream(func(r io.Reader, w io.Writer) error { return l.runStream(ctx, kind, jobID, streamID, r, w) }), nil
}

// ServeStream handles `remote stream <kind> <job> <id>`.
func ServeStream(ctx context.Context, kind StreamKind, jobID, streamID string, stdin io.Reader, stdout io.Writer, ep Endpoint) error {
	l, ok := ep.(*Local)
	if !ok {
		return &Error{Code: "internal", Message: "ServeStream needs a *Local endpoint"}
	}
	return l.runStream(ctx, kind, jobID, streamID, stdin, stdout)
}
```

If Plan 02 already defines `ServeStream`/`Local.OpenStream`, replace their bodies with the above (same signatures).

```go
// internal/remote/local_transfer.go
package remote

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// BuildManifest hashes files on this host and saves jobs/<jobID>/manifest.json.
func (l *Local) BuildManifest(ctx context.Context, jobID string, id session.ID, srcHost, dstHost string, files []session.FileEntry, pm session.PathMap) (*transfer.Manifest, error) {
	m, err := transfer.Build(ctx, jobID, id, srcHost, dstHost, files, pm)
	if err != nil {
		return nil, &Error{Code: "internal", Message: fmt.Sprintf("build manifest: %v", err)}
	}
	dir := job.Dir(l.paths.DataDir, jobID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := m.Save(filepath.Join(dir, "manifest.json")); err != nil {
		return nil, err
	}
	return m, nil
}

// SessionExtras collects the merge inputs of spec §7.5 with paths rewritten.
func (l *Local) SessionExtras(ctx context.Context, id session.ID, pm session.PathMap) (*transfer.InstallExtras, error) {
	s, err := session.Load(l.paths, id, l.opts.Probe)
	if err != nil {
		return nil, &Error{Code: "not-found", Message: err.Error()}
	}
	ex := &transfer.InstallExtras{ProjectCwd: pm.ApplyPath(s.LaunchCwd)}
	if ie, ok, err := session.ReadIndexEntry(s.ProjectDir, id); err != nil {
		return nil, err
	} else if ok {
		ie.FullPath = pm.ApplyPath(ie.FullPath)
		ie.ProjectPath = pm.ApplyPath(ie.ProjectPath)
		ex.IndexEntry = ie
	}
	lines, err := session.ExtractHistory(l.paths.HistoryFile(), id)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, line := range lines {
		var out bytes.Buffer
		if _, err := session.RewriteJSON(bytes.NewReader(line), &out, pm); err != nil {
			return nil, err
		}
		ex.History = append(ex.History, bytes.TrimSpace(out.Bytes()))
	}
	if pe, ok, err := session.ReadProjectEntry(l.paths.GlobalJSON, s.LaunchCwd); err != nil {
		return nil, err
	} else if ok {
		ex.ProjectEntry = pe
	}
	return ex, nil
}

// Cleanup removes staging/<jobID> after a successful job.
func (l *Local) Cleanup(ctx context.Context, jobID string) error {
	return os.RemoveAll(job.StagingDir(l.paths.DataDir, jobID))
}

// ListSessions scans the projects tree and the registry (spec §5 `list`).
func (l *Local) ListSessions(ctx context.Context) ([]SessionSummary, error) {
	regs, err := session.ReadRegistry(l.paths.SessionsDir())
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	procs, err := l.procs()
	if err != nil {
		return nil, err
	}
	byID := map[string]session.Registry{}
	for _, r := range regs {
		if procs.Alive(r.PID, r.ProcStart) {
			byID[r.SessionID] = r
		}
	}
	projects, err := os.ReadDir(l.paths.ProjectsDir())
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var out []SessionSummary
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(l.paths.ProjectsDir(), p.Name()))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			name := f.Name()
			if filepath.Ext(name) != ".jsonl" {
				continue
			}
			id, err := session.ParseID(name[:len(name)-len(".jsonl")])
			if err != nil {
				continue
			}
			meta, err := session.ReadMeta(filepath.Join(l.paths.ProjectsDir(), p.Name(), name))
			if err != nil {
				return nil, err
			}
			sum := SessionSummary{ID: id, State: session.StateIdle.String(), Cwd: meta.LaunchCwd, Branch: meta.Branch, Version: meta.Version, LastTS: meta.LastTS}
			if r, ok := byID[string(id)]; ok {
				sum.State, sum.Name, sum.Tmux = session.StateRunning.String(), r.Name, r.Tmux
			}
			out = append(out, sum)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastTS > out[j].LastTS })
	return out, nil
}
```

(Suspended detection for `list` needs a pane scan; the cli's local `list` from Plan 01 already does that with `session.Resolve` and a `Prober` — `ListSessions` reports running/idle, which is what `--host` shows.)

- [ ] **Step 4: Wire the ops into Server and Client**

```go
// internal/remote/ops_plan03.go
package remote

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// Argument/result shapes for the Plan 03 ops. Every op is registered in
// plan03Ops below; Client methods call the same names.
type (
	inventoryGitArgs   struct{ Cwd string }
	gitDestStateArgs   struct{ MainDir, WorktreeDir, Branch string }
	gitFilesArgs       struct {
		Plan           *gitx.Plan
		Excludes       []string
		IncludeIgnored bool
	}
	gitAttachArgs      struct {
		Plan  *gitx.Plan
		JobID string
	}
	gitSourceFactsArgs struct{ MainDir, IndexRel, Tip, DestTip string }
	inventoryTmuxArgs  struct {
		Ref             *session.TmuxRef
		PreferredSocket string
	}
	tmuxSessionsArgs   struct{ SocketPath string }
	openWindowArgs     struct{ Plan *tmuxx.Plan }
	captureArgs        struct {
		Ref   *session.TmuxRef
		JobID string
	}
	startClaudeArgs    struct {
		Ref   *session.TmuxRef
		ID    session.ID
		JobID string
		Argv  []string
	}
	confirmClaudeArgs  struct {
		Ref     *session.TmuxRef
		ID      session.ID
		Timeout time.Duration
	}
	exitClaudeArgs     struct {
		Ref       *session.TmuxRef
		PID       int
		StartTime string
		Timeout   time.Duration
	}
	typeCommandArgs    struct {
		Ref  *session.TmuxRef
		Argv []string
	}
	paneStateArgs      struct{ Ref *session.TmuxRef }
	killWindowArgs     struct{ Ref *session.TmuxRef }
	claudeStatusArgs   struct{ ID session.ID }
	claudeStatusResult struct {
		Registry *session.Registry
		OK       bool
	}
	ptyResumeArgs      struct {
		ID      session.ID
		Cwd     string
		Timeout time.Duration
	}
	buildManifestArgs  struct {
		JobID            string
		ID               session.ID
		SrcHost, DstHost string
		Files            []session.FileEntry
		PathMap          session.PathMap
	}
	sessionExtrasArgs  struct {
		ID      session.ID
		PathMap session.PathMap
	}
	extrasResult       struct{ Extras *transfer.InstallExtras }
	cleanupArgs        struct{ JobID string }
	filesResult        struct{ Files []session.FileEntry }
	sessionsResult     struct{ Sessions []SessionSummary }
	tmuxSessionsResult struct{ Sessions []tmuxx.SessionInfo }
)

// localHandler decodes args, runs the op on the Local, returns a result.
// (Plan 02's server.go already declares `handler` and the generic `decode[T]`;
// this file reuses Plan 02's `decode` and must not redeclare either.)
type localHandler func(ctx context.Context, l *Local, args json.RawMessage) (any, error)

// plan03Ops is merged into Server's dispatch table (see server.go). It holds
// ONLY ops Plan 02's `dispatch` table does not know. Plan 02 already
// dispatches `inventory-git`, `git-dest-state`, `git-attach`,
// `inventory-tmux`, `tmux-open`, `tmux-capture`, `tmux-keys`, `shape-state`
// (PaneState), `claude-start`, `claude-confirm`, `claude-exit` and
// `claude-pty-resume` through the Endpoint methods, and its `Client` already
// implements those methods with Plan 02's `*Args`/`*Result` structs — once
// the aliases are real types and the Local stubs are replaced (Task 13/14)
// they work unchanged. So: DELETE the entries below whose key is one of
// those twelve ops, and DELETE the Client methods below that Plan 02 already
// defines (InventoryGit, GitDestState, GitAttach, InventoryTmux, OpenWindow,
// Capture, StartClaude, ConfirmClaude, ExitClaude, TypeCommand, PaneState,
// RunPtyResume) before compiling; keep git-files, git-source-facts,
// tmux-sessions, tmux-kill, claude-status, build-manifest, session-extras,
// cleanup, list-sessions (and Task 23's delete-installed).
var plan03Ops = map[string]localHandler{
	"inventory-git": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[inventoryGitArgs](a)
		if err != nil {
			return nil, err
		}
		return l.InventoryGit(ctx, v.Cwd)
	},
	"git-dest-state": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[gitDestStateArgs](a)
		if err != nil {
			return nil, err
		}
		return l.GitDestState(ctx, v.MainDir, v.WorktreeDir, v.Branch)
	},
	"git-files": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[gitFilesArgs](a)
		if err != nil {
			return nil, err
		}
		files, err := l.GitFiles(ctx, v.Plan, v.Excludes, v.IncludeIgnored)
		return filesResult{Files: files}, err
	},
	"git-attach": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[gitAttachArgs](a)
		if err != nil {
			return nil, err
		}
		return struct{}{}, l.GitAttach(ctx, v.Plan, v.JobID)
	},
	"git-source-facts": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[gitSourceFactsArgs](a)
		if err != nil {
			return nil, err
		}
		return l.GitSourceFacts(ctx, v.MainDir, v.IndexRel, v.Tip, v.DestTip)
	},
	"inventory-tmux": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[inventoryTmuxArgs](a)
		if err != nil {
			return nil, err
		}
		return l.InventoryTmux(ctx, v.Ref, v.PreferredSocket)
	},
	"tmux-sessions": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[tmuxSessionsArgs](a)
		if err != nil {
			return nil, err
		}
		s, err := l.TmuxSessions(ctx, v.SocketPath)
		return tmuxSessionsResult{Sessions: s}, err
	},
	"tmux-open": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[openWindowArgs](a)
		if err != nil {
			return nil, err
		}
		return l.OpenWindow(ctx, v.Plan)
	},
	"tmux-capture": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[captureArgs](a)
		if err != nil {
			return nil, err
		}
		return struct{}{}, l.Capture(ctx, v.Ref, v.JobID)
	},
	"tmux-keys": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[typeCommandArgs](a)
		if err != nil {
			return nil, err
		}
		return struct{}{}, l.TypeCommand(ctx, v.Ref, v.Argv)
	},
	"shape-state": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) { // Plan 02 op name; duplicate of dispatch[OpPaneState] — delete
		v, err := decode[paneStateArgs](a)
		if err != nil {
			return nil, err
		}
		return l.PaneState(ctx, v.Ref)
	},
	"tmux-kill": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[killWindowArgs](a)
		if err != nil {
			return nil, err
		}
		return struct{}{}, l.KillWindow(ctx, v.Ref)
	},
	"claude-start": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[startClaudeArgs](a)
		if err != nil {
			return nil, err
		}
		return struct{}{}, l.StartClaude(ctx, v.Ref, v.ID, v.JobID, v.Argv)
	},
	"claude-confirm": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[confirmClaudeArgs](a)
		if err != nil {
			return nil, err
		}
		return l.ConfirmClaude(ctx, v.Ref, v.ID, v.Timeout)
	},
	"claude-exit": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[exitClaudeArgs](a)
		if err != nil {
			return nil, err
		}
		return struct{}{}, l.ExitClaude(ctx, v.Ref, v.PID, v.StartTime, v.Timeout)
	},
	"claude-status": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[claudeStatusArgs](a)
		if err != nil {
			return nil, err
		}
		r, ok, err := l.ClaudeStatus(ctx, v.ID)
		return claudeStatusResult{Registry: r, OK: ok}, err
	},
	"claude-pty-resume": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) { // Plan 02 op name; duplicate of dispatch[OpRunPtyResume] — delete
		v, err := decode[ptyResumeArgs](a)
		if err != nil {
			return nil, err
		}
		return struct{}{}, l.RunPtyResume(ctx, v.ID, v.Cwd, v.Timeout)
	},
	"build-manifest": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[buildManifestArgs](a)
		if err != nil {
			return nil, err
		}
		return l.BuildManifest(ctx, v.JobID, v.ID, v.SrcHost, v.DstHost, v.Files, v.PathMap)
	},
	"session-extras": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[sessionExtrasArgs](a)
		if err != nil {
			return nil, err
		}
		ex, err := l.SessionExtras(ctx, v.ID, v.PathMap)
		return extrasResult{Extras: ex}, err
	},
	"cleanup": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		v, err := decode[cleanupArgs](a)
		if err != nil {
			return nil, err
		}
		return struct{}{}, l.Cleanup(ctx, v.JobID)
	},
	"list-sessions": func(ctx context.Context, l *Local, a json.RawMessage) (any, error) {
		s, err := l.ListSessions(ctx)
		return sessionsResult{Sessions: s}, err
	},
}

// ---- Client side -------------------------------------------------------

func (c *Client) InventoryGit(ctx context.Context, cwd string) (*gitx.Info, error) {
	var out gitx.Info
	if err := c.call(ctx, "inventory-git", inventoryGitArgs{Cwd: cwd}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GitDestState(ctx context.Context, mainDir, worktreeDir, branch string) (*gitx.DestState, error) {
	var out gitx.DestState
	if err := c.call(ctx, "git-dest-state", gitDestStateArgs{mainDir, worktreeDir, branch}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GitFiles(ctx context.Context, p *gitx.Plan, excludes []string, includeIgnored bool) ([]session.FileEntry, error) {
	var out filesResult
	if err := c.call(ctx, "git-files", gitFilesArgs{p, excludes, includeIgnored}, &out); err != nil {
		return nil, err
	}
	return out.Files, nil
}

func (c *Client) GitAttach(ctx context.Context, p *gitx.Plan, jobID string) error {
	return c.call(ctx, "git-attach", gitAttachArgs{p, jobID}, nil)
}

func (c *Client) GitSourceFacts(ctx context.Context, mainDir, indexRel, tip, destTip string) (*gitx.SourceFacts, error) {
	var out gitx.SourceFacts
	if err := c.call(ctx, "git-source-facts", gitSourceFactsArgs{mainDir, indexRel, tip, destTip}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) InventoryTmux(ctx context.Context, ref *session.TmuxRef, preferredSocket string) (*tmuxx.Facts, error) {
	var out tmuxx.Facts
	if err := c.call(ctx, "inventory-tmux", inventoryTmuxArgs{ref, preferredSocket}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) TmuxSessions(ctx context.Context, socketPath string) ([]tmuxx.SessionInfo, error) {
	var out tmuxSessionsResult
	if err := c.call(ctx, "tmux-sessions", tmuxSessionsArgs{socketPath}, &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

func (c *Client) OpenWindow(ctx context.Context, p *tmuxx.Plan) (*session.TmuxRef, error) {
	var out session.TmuxRef
	if err := c.call(ctx, "tmux-open", openWindowArgs{p}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Capture(ctx context.Context, ref *session.TmuxRef, jobID string) error {
	return c.call(ctx, "tmux-capture", captureArgs{ref, jobID}, nil)
}

func (c *Client) StartClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, jobID string, argv []string) error {
	return c.call(ctx, "claude-start", startClaudeArgs{ref, id, jobID, argv}, nil)
}

func (c *Client) ConfirmClaude(ctx context.Context, ref *session.TmuxRef, id session.ID, timeout time.Duration) (*session.Registry, error) {
	var out session.Registry
	if err := c.call(ctx, "claude-confirm", confirmClaudeArgs{ref, id, timeout}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ExitClaude(ctx context.Context, ref *session.TmuxRef, pid int, startTime string, timeout time.Duration) error {
	return c.call(ctx, "claude-exit", exitClaudeArgs{ref, pid, startTime, timeout}, nil)
}

func (c *Client) TypeCommand(ctx context.Context, ref *session.TmuxRef, argv []string) error {
	return c.call(ctx, "tmux-keys", typeCommandArgs{ref, argv}, nil)
}

func (c *Client) PaneState(ctx context.Context, ref *session.TmuxRef) (*tmuxx.PaneState, error) {
	var out tmuxx.PaneState
	if err := c.call(ctx, OpPaneState, paneStateArgs{ref}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) KillWindow(ctx context.Context, ref *session.TmuxRef) error {
	return c.call(ctx, "tmux-kill", killWindowArgs{ref}, nil)
}

func (c *Client) ClaudeStatus(ctx context.Context, id session.ID) (*session.Registry, bool, error) {
	var out claudeStatusResult
	if err := c.call(ctx, "claude-status", claudeStatusArgs{id}, &out); err != nil {
		return nil, false, err
	}
	return out.Registry, out.OK, nil
}

func (c *Client) RunPtyResume(ctx context.Context, id session.ID, cwd string, timeout time.Duration) error {
	return c.call(ctx, OpRunPtyResume, ptyResumeArgs{id, cwd, timeout}, nil)
}

func (c *Client) BuildManifest(ctx context.Context, jobID string, id session.ID, srcHost, dstHost string, files []session.FileEntry, pm session.PathMap) (*transfer.Manifest, error) {
	var out transfer.Manifest
	if err := c.call(ctx, "build-manifest", buildManifestArgs{jobID, id, srcHost, dstHost, files, pm}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SessionExtras(ctx context.Context, id session.ID, pm session.PathMap) (*transfer.InstallExtras, error) {
	var out extrasResult
	if err := c.call(ctx, "session-extras", sessionExtrasArgs{id, pm}, &out); err != nil {
		return nil, err
	}
	return out.Extras, nil
}

func (c *Client) Cleanup(ctx context.Context, jobID string) error {
	return c.call(ctx, "cleanup", cleanupArgs{jobID}, nil)
}

func (c *Client) ListSessions(ctx context.Context) ([]SessionSummary, error) {
	var out sessionsResult
	if err := c.call(ctx, "list-sessions", struct{}{}, &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}
```

In `internal/remote/server.go`, where Plan 02 dispatches an op by name on a `*Local`, add: look up `plan03Ops[req.Op]` before returning the "unknown op" error, and call it with the `*Local` behind the served `Endpoint` (`Serve` receives an `Endpoint`; assert `ep.(*Local)` once at the top of `Serve` for these handlers — a `Server` always wraps a `Local`). `Error` values returned by the handlers must be sent as-is (`Code` preserved); other errors become `Code: "internal"`.

- [ ] **Step 5: Run the whole remote package and the sshtest round trip**

Run: `go vet ./... && go test -race ./internal/remote/... -v`
Expected: PASS, including Plan 02's existing tests (the aliases are gone, the JSON shapes of `gitx.*`/`tmuxx.*` round-trip). If Plan 02's `sshtest`-based Client test enumerates ops, add `inventory-git` to it: `Client.InventoryGit` over the in-process ssh server against a temp repo returns `Branch == "main"`.

- [ ] **Step 6: Commit and open PR B**

```bash
git add internal/remote
git commit -m "feat(remote): tar/pack streams, manifest/extras helpers, Plan 03 ops on Server and Client"
git push -u origin tmuxx-remote
gh pr create --title "tmuxx + remote ops (Plan 03 PR B)" --body "Implements spec §9 and the remote ops of §4.3. Tasks 8–16 of docs/superpowers/plans/2026-08-27-claude-teleport-03-orchestration.md"
```

---

## Part C — orchestration, commands, integration, README

### Task 17: orchestrate — Options, Plan, errors, placeholder argv

**Files:**
- Create: `internal/orchestrate/options.go`, `internal/orchestrate/placeholder_argv.go`
- Test: `internal/orchestrate/options_test.go`

**Interfaces:**
- Consumes: `session.*`, `remote.HostInfo`, `gitx.Plan`, `tmuxx.Plan`, `tmuxx.Facts`, `claudecfg.Report`, `transfer.Entry/Status/InstallExtras`.
- Produces: `Options` (+ additions `Target`, `Via`, `SSHOptions`), `Plan` (+ json tags and additions), `RefusedError`, `UnreachableError`, `PlaceholderArgv`, `SuspendArgv`, `PlanFromJournal`, `(*Plan).ToJSON`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/orchestrate/options_test.go
package orchestrate

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

const sid = "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"

func TestPlaceholderArgv(t *testing.T) {
	got := PlaceholderArgv(session.ID(sid), "/home/bob/.local/share/claude-teleport/jobs/"+sid+"/capture.txt", true, "", "")
	want := []string{"claude-teleport", "placeholder", "--resume", sid, "--saved-output", "/home/bob/.local/share/claude-teleport/jobs/" + sid + "/capture.txt", "--now"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	got = PlaceholderArgv(session.ID(sid), "", false, "big-storage.example", "2026-08-27T10:00:00Z")
	want = []string{"claude-teleport", "placeholder", "--resume", sid, "--teleported-to", "big-storage.example", "--teleported-at", "2026-08-27T10:00:00Z"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

func TestSuspendArgvPrefersClaudeResume(t *testing.T) {
	if got := SuspendArgv(session.ID(sid), "/c.txt", true); got[0] != "claude-resume" || got[1] != sid || got[3] != "/c.txt" {
		t.Errorf("claude-resume argv = %v", got)
	}
	if got := SuspendArgv(session.ID(sid), "/c.txt", false); got[0] != "claude-teleport" || got[1] != "placeholder" || got[len(got)-1] == "--now" {
		t.Errorf("placeholder argv = %v", got)
	}
}

func TestPlanRoundTripsThroughJournal(t *testing.T) {
	p := &Plan{JobID: sid, TargetState: "running", Statuses: map[int]transfer.Status{}}
	p.Options.Direction = "to"
	p.Options.Target = "alice@big-storage.example"
	p.Options.Via = []string{"jump.example"}
	raw, err := p.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]json.RawMessage
	json.Unmarshal(raw, &probe)
	for _, key := range []string{"options", "statuses", "git", "extras", "target_state"} {
		if _, ok := probe[key]; !ok {
			t.Errorf("plan JSON lacks %q (remote.planView depends on it)", key)
		}
	}
	j := &job.Journal{ID: sid, Plan: raw}
	got, err := PlanFromJournal(j)
	if err != nil {
		t.Fatal(err)
	}
	if got.Options.Target != "alice@big-storage.example" || got.Options.Via[0] != "jump.example" || got.TargetState != "running" {
		t.Errorf("round trip = %+v", got.Options)
	}
}

func TestErrorTypes(t *testing.T) {
	var re *RefusedError
	if !errors.As(refusef("x %d", 1), &re) || re.Error() != "refused: x 1" {
		t.Error("RefusedError")
	}
	var ue *UnreachableError
	if !errors.As(&UnreachableError{Host: "big-storage.example", Err: errors.New("dial")}, &ue) {
		t.Error("UnreachableError")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/orchestrate/ -v`
Expected: FAIL — package does not exist yet.

- [ ] **Step 3: Implement**

```go
// internal/orchestrate/options.go
// Package orchestrate is the teleport state machine (spec §6).
package orchestrate

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

type Options struct {
	Direction      string           `json:"direction"` // "to" | "from"
	Selector       session.Selector `json:"selector"`
	DestPath       string           `json:"dest_path"`
	Maps           []session.Mapping `json:"maps"`
	State          string           `json:"state"` // auto|running|suspended|idle
	AllowDrift     bool             `json:"allow_drift"`
	Force          bool             `json:"force"`
	TmuxSocket     string           `json:"tmux_socket"`
	NoTmux         bool             `json:"no_tmux"`
	Excludes       []string         `json:"excludes"`
	IncludeIgnored bool             `json:"include_ignored"`
	ExitTimeout    time.Duration    `json:"exit_timeout"`
	StartTimeout   time.Duration    `json:"start_timeout"`
	BangMode       bool             `json:"bang_mode"` // running inside the session ($CLAUDE_PID == source pid)

	// Additions (Plan 03): what the runner needs to re-dial the remote.
	Target     string            `json:"target"` // [user@]host[:port] of the remote endpoint
	Via        []string          `json:"via"`
	SSHOptions map[string]string `json:"ssh_options"`
}

// Plan is the immutable outcome of preflight plus the few facts later
// steps record (DestRef, CreatedWindow, DestRegistry, CaptureEntryID).
// JSON tags are load-bearing: remote.planView reads "statuses", "git",
// "extras" straight from the journal.
type Plan struct {
	Options      Options              `json:"options"`
	Session      *session.Session     `json:"session"`
	SourceInfo   remote.HostInfo      `json:"source_info"`
	DestInfo     remote.HostInfo      `json:"dest_info"`
	PathMap      session.PathMap      `json:"path_map"`
	Git          *gitx.Plan           `json:"git"`
	Tmux         *tmuxx.Plan          `json:"tmux"` // nil = no tmux on dest
	TargetState  string               `json:"target_state"`
	Drift        claudecfg.Report     `json:"drift"`
	ManifestPath string               `json:"manifest_path"`
	Collisions   []transfer.Entry     `json:"collisions"`

	// Additions (Plan 03):
	JobID          string                  `json:"job_id"`
	SourceFacts    *tmuxx.Facts            `json:"source_facts"` // nil when the source has no pane
	Files          []session.FileEntry     `json:"files"`        // everything the manifest is built from (rebuilt with the capture at step 3)
	Statuses       map[int]transfer.Status `json:"statuses"`
	Extras         *transfer.InstallExtras `json:"extras"`
	CaptureEntryID int                     `json:"capture_entry_id"`
	DestCwd        string                  `json:"dest_cwd"`
	DestCapture    string                  `json:"dest_capture"` // capture.txt path on the destination
	DestRef        *session.TmuxRef        `json:"dest_ref"`
	CreatedSession bool                    `json:"created_session"`
	CreatedWindow  bool                    `json:"created_window"`
	DestRegistry   *session.Registry       `json:"dest_registry"`
	StartedAt      time.Time               `json:"started_at"`
}

func (p *Plan) ToJSON() (json.RawMessage, error) { return json.Marshal(p) }

// PlanFromJournal decodes the plan a journal carries.
func PlanFromJournal(j *job.Journal) (*Plan, error) {
	var p Plan
	if err := json.Unmarshal(j.Plan, &p); err != nil {
		return nil, fmt.Errorf("decode plan of job %s: %w", j.ID, err)
	}
	return &p, nil
}

// RefusedError is a preflight refusal (exit 3): nothing was touched.
type RefusedError struct{ Reason string }

func (e *RefusedError) Error() string { return "refused: " + e.Reason }

func refusef(format string, a ...any) error { return &RefusedError{Reason: fmt.Sprintf(format, a...)} }

// UnreachableError covers dial failures and version mismatch (exit 4).
type UnreachableError struct {
	Host string
	Err  error
}

func (e *UnreachableError) Error() string { return fmt.Sprintf("%s: %v", e.Host, e.Err) }
func (e *UnreachableError) Unwrap() error { return e.Err }

// sourceState is a nil-safe accessor used by the steps.
func (p *Plan) sourceState() session.State {
	if p.Session == nil {
		return session.StateIdle
	}
	return p.Session.State
}
```

```go
// internal/orchestrate/placeholder_argv.go
package orchestrate

import "github.com/mithro/go-claude-teleport/internal/session"

// PlaceholderArgv builds the built-in placeholder command line (spec §11).
// The argv keeps `--resume <uuid>` adjacent so go-tmux-saver's process
// resolver and procx.IsPlaceholderArgv keep recognising the pane.
func PlaceholderArgv(id session.ID, savedOutput string, now bool, teleportedTo, teleportedAt string) []string {
	argv := []string{"claude-teleport", "placeholder", "--resume", string(id)}
	if savedOutput != "" {
		argv = append(argv, "--saved-output", savedOutput)
	}
	if now {
		argv = append(argv, "--now")
	}
	if teleportedTo != "" {
		argv = append(argv, "--teleported-to", teleportedTo)
		if teleportedAt != "" {
			argv = append(argv, "--teleported-at", teleportedAt)
		}
	}
	return argv
}

// SuspendArgv is what the `suspended` end state types (spec §9): go-tmux-
// saver's claude-resume when the destination has it, else our placeholder
// without --now.
func SuspendArgv(id session.ID, savedOutput string, hasClaudeResume bool) []string {
	if hasClaudeResume {
		argv := []string{"claude-resume", string(id)}
		if savedOutput != "" {
			argv = append(argv, "--saved-output", savedOutput)
		}
		return argv
	}
	return PlaceholderArgv(id, savedOutput, false, "", "")
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/orchestrate/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrate
git commit -m "feat(orchestrate): Options, Plan journal shape, errors and placeholder argv"
```

---

### Task 18: orchestrate.Preflight

**Files:**
- Create: `internal/orchestrate/preflight.go`
- Test: `internal/orchestrate/preflight_test.go` (uses the in-process fixtures from `internal/orchestrate/fixture_test.go`, written here and reused by Task 22)

**Interfaces:**
- Consumes: every `remote.Endpoint` method listed in the interfaces doc plus the Task 13/16 additions.
- Produces: `Preflight(ctx, o Options, src, dst remote.Endpoint, jobID string) (*Plan, error)`.

Preflight (spec §6 step 1), in order:

1. `Hello` both; `SourceInfo.Version != DestInfo.Version` → `UnreachableError`.
2. `src.ResolveSession(o.Selector)`; `BangMode` requires `State == running`.
3. `src.InventorySession(id)` → files + usage.
4. Path map: `o.Maps`, then `LaunchCwd → DestPath`, then `SrcHome → DstHome`, then `SrcDataDir → DstDataDir` (so the capture lands in the destination's job dir), only for pairs that differ.
5. `claudecfg.Compare(src.InventoryHost, dst.InventoryHost, usage)`, `Downgrade()` under `AllowDrift`; `Blocking` → refuse with the rendered table.
6. Git: `src.InventoryGit`; `not-found` → not-repo plan rooted at the cwd. Else `dst.GitDestState(pm(M), pm(W), branch)`, `src.GitSourceFacts(M, indexRel, tip, ds.BranchTip)` → `ds.BranchTipReachable`, `gitx.PlanTransfer` (a `*gitx.RefuseError` → `RefusedError`), `StagedBlobs`.
7. `src.GitFiles(plan, Excludes, IncludeIgnored)`.
8. tmux: unless `NoTmux`/`!DestInfo.HasTmux`: `dst.InventoryTmux(nil, preferred)` (preferred = `--tmux-socket`, else the basename of the source socket) — `unavailable` → no tmux; source facts from `src.InventoryTmux(sess.Tmux, "")` when the source has a pane; group = `Group` or `SessionName` (no source pane: `"claude"`); `dst.TmuxSessions` → `CreateSession`.
9. Target state: `auto` → source state. No tmux and target ≠ `idle` → refuse naming the options.
10. `src.SessionExtras(id, pm)`.
11. `src.BuildManifest(jobID, …, files, pm)`; fill `Git.DirtyEntries`/`IndexEntryID` (existing-main) and `Extras.Memory`.
12. `dst.ManifestDiff` → `Statuses`; `transfer.Blocking(m, st, Force)` minus memory entries → refuse listing them.

`Preflight` writes only under the two hosts' job directories (`manifest.json`, `job.json` is written by the caller). With `--dry-run` the caller stops after printing the plan.

- [ ] **Step 1: Write the fixtures and the failing tests**

```go
// internal/orchestrate/fixture_test.go
package orchestrate

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

// host is one side of an in-process teleport: its own home, config dir,
// data dir and a Local endpoint. Tmux is either nil (no tmux) or a fake.
type host struct {
	name  string
	paths session.Paths
	opts  remote.LocalOptions
	ep    *remote.Local
	tmux  *fakeTmux
}

func newHost(t *testing.T, name, user string, tm *fakeTmux) *host {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home", user)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	p := session.Paths{Home: home, ConfigDir: filepath.Join(home, ".claude"), GlobalJSON: filepath.Join(home, ".claude.json"), DataDir: filepath.Join(home, ".local", "share", "claude-teleport")}
	os.MkdirAll(p.ConfigDir, 0o700)
	opts := remote.LocalOptions{ProcRoot: "/proc", TmuxSocketDir: filepath.Join(home, "tmux-sockets"), Logf: t.Logf}
	if tm != nil {
		tm.env = func(paneID, sess, win string) []string {
			return []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + p.ConfigDir, "TMUX_PANE=" + paneID, "TMUX=" + tm.socket + ",1,0", "FAKECLAUDE_TMUX=" + sess + ":" + win + "." + paneID, "PATH=" + os.Getenv("PATH"), "XDG_DATA_HOME=" + filepath.Join(home, ".local", "share")}
		}
		opts.Tmux = func(context.Context, string) (tmuxx.Transport, error) { return tm, nil }
		os.MkdirAll(opts.TmuxSocketDir, 0o700)
		tm.socket = filepath.Join(opts.TmuxSocketDir, "default")
		os.WriteFile(tm.socket, nil, 0o600) // FindServer lists it; Dial is the fake above
		t.Cleanup(tm.killAll)
	}
	h := &host{name: name, paths: p, opts: opts, tmux: tm}
	h.ep = remote.NewLocal(p, selfExe(t), opts)
	return h
}

// refreshProbe rebuilds the endpoint with a pane probe over a fresh
// process-table snapshot; call it after starting Claude in a pane and
// before ResolveSession.
func (h *host) refreshProbe(t *testing.T) {
	t.Helper()
	procs, err := procx.Scan("/proc")
	if err != nil {
		t.Fatal(err)
	}
	o := h.opts
	o.Probe = tmuxx.Prober(context.Background(), h.tmux, procs, h.tmux.socket)
	h.ep = remote.NewLocal(h.paths, selfExe(t), o)
}

var builtExe string

// selfExe builds cmd/claude-teleport once per test binary and puts it and
// test/fakeclaude (as `claude`) on PATH.
func selfExe(t *testing.T) string {
	t.Helper()
	if builtExe != "" {
		return builtExe
	}
	dir, err := os.MkdirTemp("", "claude-teleport-test-bin")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range [][2]string{{"claude-teleport", "github.com/mithro/go-claude-teleport/cmd/claude-teleport"}, {"claude", "github.com/mithro/go-claude-teleport/test/fakeclaude"}} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(dir, b[0]), b[1])
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", b[0], err, out)
		}
	}
	os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	builtExe = filepath.Join(dir, "claude-teleport")
	return builtExe
}

// seedSession creates a transcript for sid in cwd on h with one user turn
// via `claude -p --session-id`, so --resume works later.
func seedSession(t *testing.T, h *host, cwd string) {
	t.Helper()
	os.MkdirAll(cwd, 0o755)
	cmd := exec.Command("claude", "-p", "--session-id", sid, "remember the word pineapple")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "HOME="+h.paths.Home, "CLAUDE_CONFIG_DIR="+h.paths.ConfigDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
}

// startClaudeInPane opens a window in h's fake tmux and resumes sid there;
// returns once the registry reports the session idle.
func startClaudeInPane(t *testing.T, h *host, group, cwd string) *session.TmuxRef {
	t.Helper()
	ctx := context.Background()
	ref, err := tmuxx.OpenWindow(ctx, h.tmux, &tmuxx.Plan{SocketPath: h.tmux.socket, Group: group, WindowName: "claude", AutoRename: false, Cwd: cwd, CreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := tmuxx.TypeCommand(ctx, h.tmux, ref.PaneID, []string{"claude", "--resume", sid}); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, h, "idle")
	h.refreshProbe(t)
	return ref
}

func waitRegistry(t *testing.T, h *host, status string) *session.Registry {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		reg, ok, err := h.ep.ClaudeStatus(context.Background(), session.ID(sid))
		if err != nil {
			t.Fatal(err)
		}
		if ok && (status == "" || reg.Status == status) {
			return reg
		}
		if time.Now().After(deadline) {
			t.Fatalf("session never reached status %q on %s", status, h.name)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.MarshalIndent(v, "", "  ")
	os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
```

`FAKECLAUDE_TMUX` is Plan 01's (Task 19) override of the registry `tmux` field that fakeclaude otherwise derives from `$TMUX`/`$TMUX_PANE` via `tmux display-message`; the fake tmux here has no `tmux` binary to query, so the pane env hands it the full `session:@win.%pane` string.

The fake tmux server double:

```go
// internal/orchestrate/faketmux_test.go
package orchestrate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// fakeTmux implements tmuxx.Transport by interpreting the exact command
// lines tmuxx sends and running each pane as `sh -s` fed through a pipe
// (typed keys go to the pane's stdin, so a running fakeclaude receives
// "/exit" exactly as it would under tmux). capture-pane returns what the
// pane wrote. pane_pid is the sh pid, so procx sees a real process tree.
type fakeTmux struct {
	mu       sync.Mutex
	socket   string
	sessions map[string]string // name -> group
	windows  map[string]*fakeWindow
	panes    map[string]*fakePane
	nextW    int
	nextP    int
	env      func(paneID, sess, win string) []string
}

type fakeWindow struct {
	id, session, name string
	index             int
	autoRename        bool
}

type fakePane struct {
	id, windowID, cwd string
	cmd               *exec.Cmd
	stdin             io.WriteCloser
	out               *lockedBuffer
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *lockedBuffer) lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := strings.TrimRight(l.b.String(), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func newFakeTmux() *fakeTmux {
	return &fakeTmux{sessions: map[string]string{}, windows: map[string]*fakeWindow{}, panes: map[string]*fakePane{}}
}

func (f *fakeTmux) Close() error { return nil }

// splitArgs tokenises a tmux command line: bare words and "…" words with
// \\, \", \$, \n, \r escapes (the inverse of tmuxx.Quote).
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inQ, have := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQ && c == '\\' && i+1 < len(s):
			i++
			switch s[i] {
			case 'n':
				cur.WriteByte('\n')
			case 'r':
				cur.WriteByte('\r')
			default:
				cur.WriteByte(s[i])
			}
		case c == '"':
			inQ, have = !inQ, true
		case c == ' ' && !inQ:
			if have || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
		}
	}
	if have || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func flag(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func (f *fakeTmux) Run(_ context.Context, cmd string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := splitArgs(cmd)
	if len(a) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	switch a[0] {
	case "list-sessions":
		var out []string
		for name, group := range f.sessions {
			out = append(out, name+"\t"+group)
		}
		return out, nil
	case "new-session":
		sess := flag(a, "-s")
		f.sessions[sess] = ""
		return f.newWindow(sess, flag(a, "-n"), flag(a, "-c"))
	case "new-window":
		target := strings.TrimSuffix(strings.TrimPrefix(flag(a, "-t"), "="), ":")
		if _, ok := f.sessions[target]; !ok {
			return nil, fmt.Errorf("can't find session: %s", target)
		}
		return f.newWindow(target, flag(a, "-n"), flag(a, "-c"))
	case "set-option":
		if w, ok := f.windows[flag(a, "-t")]; ok && a[len(a)-2] == "automatic-rename" {
			w.autoRename = a[len(a)-1] != "off"
			return nil, nil
		}
		return nil, fmt.Errorf("set-option: %q", cmd)
	case "show-options":
		if w, ok := f.windows[flag(a, "-t")]; ok {
			if w.autoRename {
				return []string{"on"}, nil
			}
			return []string{"off"}, nil
		}
		return nil, fmt.Errorf("show-options: no window %q", flag(a, "-t"))
	case "list-panes":
		format := flag(a, "-F")
		target := flag(a, "-t")
		var out []string
		for _, p := range f.panes {
			w := f.windows[p.windowID]
			switch {
			case target == "" && contains(a, "-a"):
				out = append(out, f.describe(w, p, format))
			case target == p.id:
				out = append(out, strconv.Itoa(p.cmd.Process.Pid))
			case strings.HasPrefix(target, "="):
				sw := strings.TrimPrefix(target, "=")
				sess, win, _ := strings.Cut(sw, ":")
				win = strings.TrimPrefix(win, "=")
				if w.session == sess && (win == strconv.Itoa(w.index) || win == w.name) {
					out = append(out, p.id)
				}
			}
		}
		if len(out) == 0 && target != "" {
			return nil, fmt.Errorf("can't find pane: %s", target)
		}
		return out, nil
	case "send-keys":
		p, ok := f.panes[flag(a, "-t")]
		if !ok {
			return nil, fmt.Errorf("can't find pane: %s", flag(a, "-t"))
		}
		var text strings.Builder
		for _, k := range a[3:] {
			if k == "Enter" {
				text.WriteByte('\n')
			} else {
				text.WriteString(k)
			}
		}
		_, err := io.WriteString(p.stdin, text.String())
		return nil, err
	case "capture-pane":
		p, ok := f.panes[flag(a, "-t")]
		if !ok {
			return nil, fmt.Errorf("can't find pane: %s", flag(a, "-t"))
		}
		return p.out.lines(), nil
	case "kill-window":
		w, ok := f.windows[flag(a, "-t")]
		if !ok {
			return nil, fmt.Errorf("can't find window: %s", flag(a, "-t"))
		}
		for id, p := range f.panes {
			if p.windowID == w.id {
				syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
				p.stdin.Close()
				delete(f.panes, id)
			}
		}
		delete(f.windows, w.id)
		return nil, nil
	}
	return nil, fmt.Errorf("fakeTmux: unsupported command %q", cmd)
}

func contains(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}

func (f *fakeTmux) newWindow(sess, name, cwd string) ([]string, error) {
	idx := 0
	for _, w := range f.windows {
		if w.session == sess && w.index >= idx {
			idx = w.index + 1
		}
	}
	w := &fakeWindow{id: fmt.Sprintf("@%d", f.nextW), session: sess, name: name, index: idx, autoRename: true}
	f.nextW++
	p := &fakePane{id: fmt.Sprintf("%%%d", f.nextP), windowID: w.id, cwd: cwd, out: &lockedBuffer{}}
	f.nextP++
	cmd := exec.Command("sh", "-s")
	cmd.Dir = cwd
	cmd.Env = f.env(p.id, sess, w.id)
	cmd.Stdout, cmd.Stderr = p.out, p.out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p.cmd, p.stdin = cmd, stdin
	f.windows[w.id] = w
	f.panes[p.id] = p
	return []string{p.id + "\t" + w.id + "\t" + sess}, nil
}

// describe renders the Describe format for one pane (only that format
// is supported: session, group, window id, index, name, pane id, cwd,
// command, pid, history size, title).
func (f *fakeTmux) describe(w *fakeWindow, p *fakePane, format string) string {
	if !strings.HasPrefix(format, "#{session_name}") {
		return ""
	}
	return strings.Join([]string{w.session, f.sessions[w.session], w.id, strconv.Itoa(w.index), w.name, p.id, p.cwd, "sh", strconv.Itoa(p.cmd.Process.Pid), strconv.Itoa(len(p.out.lines())), "title"}, "\t")
}

// killAll ends every pane process (test cleanup).
func (f *fakeTmux) killAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.panes {
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		p.stdin.Close()
	}
}
```

(`sh -s` here is the test's stand-in for the interactive shell a real tmux pane runs; the bytes written to its stdin are exactly the keystrokes `send-keys` would deliver, which is the point of the double — it is not a shell invocation with interpolated data.)

```go
// internal/orchestrate/preflight_test.go
package orchestrate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/session"
)

func gitc(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=alice", "GIT_AUTHOR_EMAIL=a@laptop.example", "GIT_COMMITTER_NAME=alice", "GIT_COMMITTER_EMAIL=a@laptop.example", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func makeRepo(t *testing.T, dir string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	gitc(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644)
	gitc(t, dir, "add", ".")
	gitc(t, dir, "commit", "-q", "-m", "init")
}

func baseOptions() Options {
	return Options{Direction: "to", Selector: session.Selector{ID: session.ID(sid)}, State: "auto", ExitTimeout: 10 * time.Second, StartTimeout: 20 * time.Second, Target: "bob@big-storage.example"}
}

func TestPreflightIdleSessionFreshMainWithHomeRewrite(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := filepath.Join(src.paths.Home, "github", "x")
	makeRepo(t, cwd)
	os.WriteFile(filepath.Join(cwd, "scratch.txt"), []byte("untracked"), 0o644)
	seedSession(t, src, cwd)

	o := baseOptions()
	o.State = "idle"
	p, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatal(err)
	}
	if p.Git == nil || p.Git.Mode != gitx.ModeFreshMain {
		t.Fatalf("git plan = %+v", p.Git)
	}
	if p.Git.DstMain != filepath.Join(dst.paths.Home, "github", "x") {
		t.Errorf("DstMain = %q", p.Git.DstMain)
	}
	if p.Tmux != nil || p.TargetState != "idle" {
		t.Errorf("tmux/target = %+v %q", p.Tmux, p.TargetState)
	}
	if len(p.Statuses) == 0 || len(p.Collisions) != 0 {
		t.Errorf("statuses=%d collisions=%v", len(p.Statuses), p.Collisions)
	}
	if _, err := os.Stat(p.ManifestPath); err != nil {
		t.Errorf("manifest not saved on the driver: %v", err)
	}
	if p.Extras == nil || p.Extras.ProjectCwd != filepath.Join(dst.paths.Home, "github", "x") {
		t.Errorf("extras = %+v", p.Extras)
	}
	if !p.PathMap.Empty() && p.PathMap.ApplyPath(src.paths.Home+"/a") != dst.paths.Home+"/a" {
		t.Errorf("path map = %+v", p.PathMap)
	}
}

func TestPreflightRefusesWithoutTmuxUnlessIdle(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := filepath.Join(src.paths.Home, "x")
	seedSession(t, src, cwd)
	o := baseOptions()
	o.State = "running"
	_, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	var re *RefusedError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want RefusedError", err)
	}
}

func TestPreflightDriftRefusalAndOverride(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := filepath.Join(src.paths.Home, "x")
	seedSession(t, src, cwd)
	writeJSON(t, filepath.Join(src.paths.ConfigDir, "settings.json"), map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": "echo hi"}}}}}})
	o := baseOptions()
	o.State = "idle"
	_, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	var re *RefusedError
	if !errors.As(err, &re) {
		t.Fatalf("hooks drift must refuse, got %v", err)
	}
	o.AllowDrift = true
	p, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatal(err)
	}
	if p.Drift.Blocking || len(p.Drift.Diffs) == 0 {
		t.Errorf("drift after override = %+v", p.Drift)
	}
}

func TestPreflightCollisionRefusal(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := filepath.Join(src.paths.Home, "x")
	seedSession(t, src, cwd)
	// A different transcript with the same id already on the destination.
	dstProj := dst.paths.ProjectDir(filepath.Join(dst.paths.Home, "x"))
	os.MkdirAll(dstProj, 0o700)
	os.WriteFile(filepath.Join(dstProj, sid+".jsonl"), []byte(`{"type":"user","sessionId":"`+sid+`","cwd":"/elsewhere"}`+"\n"), 0o600)
	o := baseOptions()
	o.State = "idle"
	_, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	var re *RefusedError
	if !errors.As(err, &re) || !containsStr(re.Reason, sid+".jsonl") {
		t.Fatalf("err = %v, want collision refusal naming the transcript", err)
	}
	o.Force = true
	if _, err := Preflight(context.Background(), o, src.ep, dst.ep, sid); err != nil {
		t.Fatalf("--force must allow same-session overwrite: %v", err)
	}
}

func TestPreflightGitDivergenceRefusal(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := filepath.Join(src.paths.Home, "x")
	makeRepo(t, cwd)
	seedSession(t, src, cwd)
	dstRepo := filepath.Join(dst.paths.Home, "x")
	gitc(t, filepath.Dir(dstRepo), "clone", "-q", cwd, dstRepo)
	os.WriteFile(filepath.Join(dstRepo, "other.txt"), []byte("diverge\n"), 0o644)
	gitc(t, dstRepo, "add", ".")
	gitc(t, dstRepo, "commit", "-q", "-m", "diverged")
	o := baseOptions()
	o.State = "idle"
	_, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	var re *RefusedError
	if !errors.As(err, &re) || !containsStr(re.Reason, "fast-forward") {
		t.Fatalf("err = %v, want non-fast-forward refusal", err)
	}
}

func containsStr(s, sub string) bool { return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/orchestrate/ -run TestPreflight -v`
Expected: FAIL — `undefined: Preflight`.

- [ ] **Step 3: Implement**

```go
// internal/orchestrate/preflight.go
package orchestrate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// isCode reports whether err is a remote.Error with the given code.
func isCode(err error, code string) bool {
	var re *remote.Error
	return errors.As(err, &re) && re.Code == code
}

// Preflight is spec §6 step 1. It touches nothing outside the two hosts'
// job directories.
func Preflight(ctx context.Context, o Options, src, dst remote.Endpoint, jobID string) (*Plan, error) {
	p := &Plan{Options: o, JobID: jobID}
	var err error
	if p.SourceInfo, err = src.Hello(ctx); err != nil {
		return nil, &UnreachableError{Host: "source", Err: err}
	}
	if p.DestInfo, err = dst.Hello(ctx); err != nil {
		return nil, &UnreachableError{Host: o.Target, Err: err}
	}
	if p.SourceInfo.Version != p.DestInfo.Version {
		return nil, &UnreachableError{Host: o.Target, Err: fmt.Errorf("claude-teleport version mismatch: source %s, destination %s — install the same version on both hosts", p.SourceInfo.Version, p.DestInfo.Version)}
	}
	if !p.DestInfo.HasClaude {
		return nil, refusef("claude is not installed on %s", p.DestInfo.Hostname)
	}

	if p.Session, err = src.ResolveSession(ctx, o.Selector); err != nil {
		return nil, err
	}
	sess := p.Session
	if o.BangMode && sess.State != session.StateRunning {
		return nil, refusef("!-mode requires the session to be running (it is %s)", sess.State)
	}
	inv, usage, err := src.InventorySession(ctx, sess.ID)
	if err != nil {
		return nil, err
	}

	// Path map (spec §7.2), longest prefix first.
	var maps []session.Mapping
	maps = append(maps, o.Maps...)
	if o.DestPath != "" {
		maps = append(maps, session.Mapping{From: sess.LaunchCwd, To: o.DestPath})
	}
	if p.SourceInfo.Home != p.DestInfo.Home {
		maps = append(maps, session.Mapping{From: p.SourceInfo.Home, To: p.DestInfo.Home})
	}
	if p.SourceInfo.DataDir != p.DestInfo.DataDir {
		maps = append(maps, session.Mapping{From: p.SourceInfo.DataDir, To: p.DestInfo.DataDir})
	}
	p.PathMap = session.NewPathMap(maps...)
	p.DestCwd = p.PathMap.ApplyPath(sess.LaunchCwd)
	p.DestCapture = filepath.Join(job.Dir(p.DestInfo.DataDir, jobID), "capture.txt")

	// Drift (spec §10).
	srcVersion := p.SourceInfo.ClaudeVersion
	if sess.Registry != nil && sess.Registry.Version != "" {
		srcVersion = sess.Registry.Version
	}
	srcCfg, err := src.InventoryHost(ctx, sess.LaunchCwd, srcVersion)
	if err != nil {
		return nil, err
	}
	dstCfg, err := dst.InventoryHost(ctx, p.DestCwd, p.DestInfo.ClaudeVersion)
	if err != nil {
		return nil, err
	}
	p.Drift = claudecfg.Compare(srcCfg, dstCfg, usage)
	if o.AllowDrift {
		p.Drift = p.Drift.Downgrade()
	}
	if p.Drift.Blocking {
		var buf bytes.Buffer
		p.Drift.Render(&buf)
		return nil, refusef("configuration drift would change the session's behaviour on %s (use --allow-config-drift to proceed):\n%s", p.DestInfo.Hostname, buf.String())
	}

	// Git (spec §8).
	gi, err := src.InventoryGit(ctx, sess.LaunchCwd)
	switch {
	case isCode(err, "not-found"):
		p.Git = &gitx.Plan{Mode: gitx.ModeNotRepo, SrcWorktree: sess.LaunchCwd, DstWorktree: p.DestCwd}
	case err != nil:
		return nil, err
	default:
		ds, err := dst.GitDestState(ctx, p.PathMap.ApplyPath(gi.MainDir), p.PathMap.ApplyPath(gi.Root), gi.Branch)
		if err != nil {
			return nil, err
		}
		indexRel := ".git/index"
		if gi.IsLinked {
			indexRel = ".git/worktrees/" + gi.WorktreeName + "/index"
		}
		facts, err := src.GitSourceFacts(ctx, gi.MainDir, indexRel, gi.Head, ds.BranchTip)
		if err != nil {
			return nil, err
		}
		ds.BranchTipReachable = facts.DestTipReachable
		gp, err := gitx.PlanTransfer(gi, ds, p.PathMap)
		var re *gitx.RefuseError
		if errors.As(err, &re) {
			return nil, refusef("git: %s", re.Reason)
		}
		if err != nil {
			return nil, err
		}
		gp.StagedBlobs = facts.StagedBlobs
		p.Git = gp
	}
	gitFiles, err := src.GitFiles(ctx, p.Git, o.Excludes, o.IncludeIgnored)
	if err != nil {
		return nil, err
	}

	// tmux (spec §9).
	if !o.NoTmux && p.DestInfo.HasTmux {
		if sess.Tmux != nil {
			if p.SourceFacts, err = src.InventoryTmux(ctx, sess.Tmux, ""); err != nil {
				return nil, err
			}
		}
		preferred := o.TmuxSocket
		if preferred == "" && sess.Tmux != nil {
			preferred = filepath.Base(sess.Tmux.SocketPath)
		}
		dfacts, err := dst.InventoryTmux(ctx, nil, preferred)
		switch {
		case isCode(err, "unavailable"):
			p.Tmux = nil
		case err != nil:
			return nil, err
		default:
			tp := &tmuxx.Plan{SocketPath: dfacts.SocketPath, Group: "claude", WindowName: "claude", AutoRename: true, Cwd: p.DestCwd}
			if p.SourceFacts != nil {
				tp.Group, tp.WindowName, tp.AutoRename = p.SourceFacts.Group, p.SourceFacts.WindowName, p.SourceFacts.AutoRename
				if tp.Group == "" {
					tp.Group = p.SourceFacts.SessionName
				}
			}
			sessions, err := dst.TmuxSessions(ctx, dfacts.SocketPath)
			if err != nil {
				return nil, err
			}
			_, exists := tmuxx.BaseSession(sessions, tp.Group)
			tp.CreateSession = !exists
			p.Tmux = tp
		}
	}

	// Target state.
	p.TargetState = o.State
	if p.TargetState == "" || p.TargetState == "auto" {
		p.TargetState = sess.State.String()
	}
	if p.Tmux == nil && p.TargetState != "idle" {
		return nil, refusef("no usable tmux server on %s (or --no-tmux): only --state idle is possible, not %q", p.DestInfo.Hostname, p.TargetState)
	}

	// Merge inputs and manifest (spec §7).
	if p.Extras, err = src.SessionExtras(ctx, sess.ID, p.PathMap); err != nil {
		return nil, err
	}
	p.Files = append(append(append([]session.FileEntry{}, inv.Files...), inv.Memory...), gitFiles...)
	m, err := src.BuildManifest(ctx, jobID, sess.ID, p.SourceInfo.Hostname, p.DestInfo.Hostname, p.Files, p.PathMap)
	if err != nil {
		return nil, err
	}
	p.annotateManifest(m, inv)
	p.ManifestPath = filepath.Join(job.Dir(driverDataDir(src, dst, o), jobID), "manifest.json")
	if err := m.Save(p.ManifestPath); err != nil {
		return nil, err
	}
	if p.Statuses, err = dst.ManifestDiff(ctx, m, jobID); err != nil {
		return nil, err
	}
	memory := map[int]bool{}
	for _, e := range p.Extras.Memory {
		memory[e.ID] = true
	}
	for _, e := range transfer.Blocking(m, p.Statuses, o.Force) {
		if !memory[e.ID] {
			p.Collisions = append(p.Collisions, e)
		}
	}
	if len(p.Collisions) > 0 {
		var b strings.Builder
		for _, e := range p.Collisions {
			fmt.Fprintf(&b, "  %s (%s)\n", e.Dst, p.Statuses[e.ID])
		}
		return nil, refusef("%d destination file(s) already exist with different content on %s:\n%s", len(p.Collisions), p.DestInfo.Hostname, b.String())
	}
	return p, nil
}

// annotateManifest records which manifest entries the git attach step
// applies itself (existing-main) and which are memory files.
func (p *Plan) annotateManifest(m *transfer.Manifest, inv *session.Inventory) {
	memoryRoots := map[string]bool{}
	for _, e := range inv.Memory {
		memoryRoots[e.Path()] = true
	}
	p.Extras.Memory = nil
	if p.Git.Mode == gitx.ModeExistingMain {
		p.Git.DirtyEntries = map[string]int{}
	}
	indexSrc := filepath.Join(p.Git.SrcMain, filepath.FromSlash(p.Git.IndexRel))
	for _, e := range m.Entries {
		if memoryRoots[e.Src] {
			p.Extras.Memory = append(p.Extras.Memory, e)
			continue
		}
		if p.Git.Mode != gitx.ModeExistingMain {
			continue
		}
		switch {
		case e.Category == session.CatRepo && e.Src == indexSrc:
			p.Git.IndexEntryID = e.ID
		case e.Category == session.CatWorktree:
			p.Git.DirtyEntries[e.Dst] = e.ID
		}
	}
}

// driverDataDir is the local data dir: the source's for --to, the
// destination's for --from.
func driverDataDir(src, dst remote.Endpoint, o Options) string {
	if o.Direction == "from" {
		return dst.Paths().DataDir
	}
	return src.Paths().DataDir
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/orchestrate/ -run TestPreflight -v`
Expected: PASS. `TestPreflightCollisionRefusal` expects the transcript to be `PresentDifferent` (not a byte-prefix) → refused; with `--force`, `transfer.Blocking` allows the same-session transcript (its `FFAllowed` is true).

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrate
git commit -m "feat(orchestrate): Preflight builds the immutable plan (spec §6 step 1)"
```

---

### Task 19: orchestrate.Render — the human plan

**Files:**
- Create: `internal/orchestrate/render.go`
- Test: `internal/orchestrate/render_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/orchestrate/render_test.go
package orchestrate

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/claudecfg"
	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

func TestRenderMentionsEveryDecision(t *testing.T) {
	p := &Plan{
		Options:     Options{Direction: "to", Target: "bob@big-storage.example", Via: []string{"jump.example"}},
		Session:     &session.Session{ID: session.ID(sid), LaunchCwd: "/home/alice/github/x/.worktrees/feat", Branch: "feat", State: session.StateRunning},
		SourceInfo:  remote.HostInfo{Hostname: "laptop.example", Home: "/home/alice", ClaudeVersion: "2.1.247"},
		DestInfo:    remote.HostInfo{Hostname: "big-storage.example", Home: "/home/bob", ClaudeVersion: "2.1.250"},
		PathMap:     session.NewPathMap(session.Mapping{From: "/home/alice", To: "/home/bob"}),
		Git:         &gitx.Plan{Mode: gitx.ModeExistingMain, DstMain: "/home/bob/github/x", DstWorktree: "/home/bob/github/x/.worktrees/feat", Branch: "feat", Tip: "cccccccccccccccccccccccccccccccccccccccc", NeedPack: true, FastForward: true, Dirty: gitx.Dirty{Modified: []string{"a.go"}, Untracked: []string{"b.go"}}},
		Tmux:        &tmuxx.Plan{SocketPath: "/tmp/tmux-1001/default", Group: "work", WindowName: "claude", CreateSession: true},
		TargetState: "running",
		Drift:       claudecfg.Report{Diffs: []claudecfg.Difference{{Class: claudecfg.Warn, Key: "claude.version", Source: "2.1.247", Dest: "2.1.250"}}},
		Statuses:    map[int]transfer.Status{1: transfer.Absent, 2: transfer.PresentSame, 3: transfer.FFCandidate},
	}
	var buf bytes.Buffer
	p.Render(&buf)
	out := buf.String()
	for _, want := range []string{"3f2a9c1e", "laptop.example", "big-storage.example", "via jump.example", "/home/alice -> /home/bob", "existing-main", "fast-forward", "packfile", "a.go", "b.go", "new session \"work\"", "window \"claude\"", "running", "claude.version", "2 to send", "1 already present", "1 fast-forward"} {
		if !strings.Contains(out, want) {
			t.Errorf("render lacks %q:\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/orchestrate/ -run TestRender -v`
Expected: FAIL — `p.Render undefined`.

- [ ] **Step 3: Implement**

```go
// internal/orchestrate/render.go
package orchestrate

import (
	"fmt"
	"io"
	"strings"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// Render prints the plan for humans (spec §6 step 1, §8 "every decision
// above is shown").
func (p *Plan) Render(w io.Writer) {
	s := p.Session
	fmt.Fprintf(w, "Session  %s (%s) on %s\n", s.ID.Short(), s.State, p.SourceInfo.Hostname)
	fmt.Fprintf(w, "  cwd    %s\n", s.LaunchCwd)
	if s.Branch != "" {
		fmt.Fprintf(w, "  branch %s\n", s.Branch)
	}
	via := ""
	if len(p.Options.Via) > 0 {
		via = " via " + strings.Join(p.Options.Via, ", ")
	}
	fmt.Fprintf(w, "Move     %s %s%s\n", strings.ToUpper(p.Options.Direction[:1])+p.Options.Direction[1:], p.DestInfo.Hostname, via)
	fmt.Fprintf(w, "  claude %s -> %s\n", p.SourceInfo.ClaudeVersion, p.DestInfo.ClaudeVersion)
	if !p.PathMap.Empty() {
		fmt.Fprintln(w, "Paths")
		for _, m := range p.PathMap {
			fmt.Fprintf(w, "  %s -> %s\n", m.From, m.To)
		}
	}
	fmt.Fprintln(w, "Git")
	switch p.Git.Mode {
	case gitx.ModeNotRepo:
		fmt.Fprintf(w, "  not a repository: %s copied as plain files to %s\n", p.Git.SrcWorktree, p.Git.DstWorktree)
	case gitx.ModeFreshMain:
		fmt.Fprintf(w, "  fresh-main: %s is absent on the destination; the whole repository is transferred\n", p.Git.DstMain)
		if p.Git.Linked {
			fmt.Fprintf(w, "  linked worktree %s is re-attached at %s\n", p.Git.WorktreeName, p.Git.DstWorktree)
		}
	case gitx.ModeExistingMain:
		fmt.Fprintf(w, "  existing-main: %s already exists on the destination (same root commit)\n", p.Git.DstMain)
		switch {
		case p.Git.NeedPack && p.Git.FastForward:
			fmt.Fprintf(w, "  branch %s is fast-forward'ed to %s with a packfile of the missing objects\n", p.Git.Branch, short(p.Git.Tip))
		case p.Git.NeedPack:
			fmt.Fprintf(w, "  branch %s is created at %s from a packfile of the missing objects\n", p.Git.Branch, short(p.Git.Tip))
		default:
			fmt.Fprintf(w, "  branch %s is already at %s; no packfile needed\n", p.Git.Branch, short(p.Git.Tip))
		}
		if p.Git.Linked {
			fmt.Fprintf(w, "  linked worktree is created at %s\n", p.Git.DstWorktree)
		} else {
			fmt.Fprintf(w, "  the main checkout %s is fast-forwarded in place (it must stay clean)\n", p.Git.DstMain)
		}
	}
	d := p.Git.Dirty
	if n := len(d.Staged) + len(d.Modified) + len(d.Untracked) + len(d.Deleted); n > 0 {
		fmt.Fprintf(w, "  dirty state carried: %d staged, %d modified, %d untracked, %d deleted\n", len(d.Staged), len(d.Modified), len(d.Untracked), len(d.Deleted))
		for _, f := range d.Staged {
			fmt.Fprintf(w, "    A %s\n", f)
		}
		for _, f := range d.Modified {
			fmt.Fprintf(w, "    M %s\n", f)
		}
		for _, f := range d.Untracked {
			fmt.Fprintf(w, "    ? %s\n", f)
		}
		for _, f := range d.Deleted {
			fmt.Fprintf(w, "    D %s (stays present on the destination; nothing is ever deleted)\n", f)
		}
	}
	fmt.Fprintln(w, "tmux")
	switch {
	case p.Tmux == nil:
		fmt.Fprintln(w, "  none on the destination: Claude is confirmed under a pty and left idle")
	case p.Tmux.CreateSession:
		fmt.Fprintf(w, "  new session %q on %s, window %q in %s\n", p.Tmux.Group, p.Tmux.SocketPath, p.Tmux.WindowName, p.Tmux.Cwd)
	default:
		fmt.Fprintf(w, "  existing group %q on %s, new window %q in %s\n", p.Tmux.Group, p.Tmux.SocketPath, p.Tmux.WindowName, p.Tmux.Cwd)
	}
	fmt.Fprintf(w, "End state  %s\n", p.TargetState)
	if len(p.Drift.Diffs) > 0 {
		fmt.Fprintln(w, "Configuration differences")
		p.Drift.Render(w)
	}
	var absent, same, ff, staged int
	for _, st := range p.Statuses {
		switch st {
		case transfer.Absent, transfer.StagedMismatch:
			absent++
		case transfer.PresentSame:
			same++
		case transfer.FFCandidate:
			ff++
		case transfer.StagedSame:
			staged++
		}
	}
	fmt.Fprintf(w, "Files      %d to send, %d already present, %d fast-forward, %d already staged\n", absent, same, ff, staged)
}

func short(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}
```

- [ ] **Step 4: Run the test**

Run: `go test -race ./internal/orchestrate/ -run TestRender -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrate/render.go internal/orchestrate/render_test.go
git commit -m "feat(orchestrate): Render the plan for humans"
```

---

### Task 20: orchestrate.Steps — the ten steps with re-verification

**Files:**
- Create: `internal/orchestrate/steps.go`
- Modify: `internal/orchestrate/preflight.go` (`annotateManifest` takes a set of memory source paths so the capture step can call it again)
- Test: `internal/orchestrate/steps_test.go` (unit tests of Verify logic with a scripted endpoint; the real flow is Task 22)

**Interfaces:**
- Consumes: `job.Step`, `job.Journal`, every `remote.Endpoint` method, `transfer.Load/Need/Blocking`, `procx.IsPlaceholderArgv`, `PlaceholderArgv`, `SuspendArgv`.
- Produces: `Steps(p *Plan, j *job.Journal, src, dst remote.Endpoint, selfExe string, logf func(string, ...any)) []job.Step`, `StepNames []string`.

Per-step contract (spec §6 table; Verify re-checks reality and returns done=true to skip Run):

| step | Verify | Run |
|---|---|---|
| preflight | journal present on both hosts | `JournalPut` both |
| freeze | source not running → done | `src.Freeze(pid, procStart)`; gone pid → log, continue |
| capture | no source pane → done; else never (cheap) | `src.Capture`; rebuild manifest with the capture entry; `ManifestDiff`; persist |
| transfer | `ManifestDiff` → `Need` empty | pump `tar` stream src→dst; re-diff; error if still needed |
| install | every non-deferred entry `present-same` | `dst.Install(manifest minus deferred entries)` |
| git-attach | not-repo → done; fresh-main → dest worktree inspects with the branch; existing-main → never | pump `pack` stream if `NeedPack`; `dst.GitAttach` |
| start | registry alive in our pane → confirm only | open window (reuse an idle one we opened), type placeholder `--now`, confirm; no-tmux: `RunPtyResume` |
| shape | running → done; suspended → pane shows placeholder; idle → window gone / shell | `/exit`, then type suspend argv / kill our window |
| thaw+exit | source not running → done; pid gone and pane shows placeholder → done | thaw; `!`-mode waits for idle; `/exit` or SIGTERM; type placeholder onto a bare shell |
| record | step already done | history on both hosts; `Cleanup` staging |

- [ ] **Step 1: Write the failing unit tests for the Verify decisions**

```go
// internal/orchestrate/steps_test.go
package orchestrate

import (
	"context"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/session"
)

func TestStepNamesMatchSpecOrder(t *testing.T) {
	want := []string{"preflight", "freeze", "capture", "transfer", "install", "git-attach", "start", "shape", "thaw+exit", "record"}
	if len(StepNames) != len(want) {
		t.Fatalf("StepNames = %v", StepNames)
	}
	for i := range want {
		if StepNames[i] != want[i] {
			t.Errorf("step %d = %q, want %q", i, StepNames[i], want[i])
		}
	}
}

func TestFreezeAndThawVerifySkipWhenSourceNotRunning(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	p := &Plan{Session: &session.Session{ID: session.ID(sid), State: session.StateIdle}, TargetState: "idle"}
	j := &job.Journal{ID: sid}
	steps := Steps(p, j, src.ep, dst.ep, selfExe(t), t.Logf)
	byName := map[string]job.Step{}
	for _, s := range steps {
		byName[s.Name] = s
	}
	for _, name := range []string{"freeze", "capture", "thaw+exit"} {
		done, err := byName[name].Verify(context.Background())
		if err != nil || !done {
			t.Errorf("%s.Verify on an idle source = %v %v, want done", name, done, err)
		}
	}
	done, err := byName["shape"].Verify(context.Background())
	if err != nil || !done {
		t.Errorf("shape.Verify with no tmux and target idle = %v %v, want done", done, err)
	}
}

func TestRecordVerifyUsesJournal(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	p := &Plan{Session: &session.Session{ID: session.ID(sid)}}
	j := &job.Journal{ID: sid}
	steps := Steps(p, j, src.ep, dst.ep, selfExe(t), t.Logf)
	rec := steps[len(steps)-1]
	if done, _ := rec.Verify(context.Background()); done {
		t.Error("record must run when the journal has no done record step")
	}
	j.Step("record").Status = job.Done
	if done, _ := rec.Verify(context.Background()); !done {
		t.Error("record must be skipped once the journal says done")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/orchestrate/ -run 'TestStepNames|TestFreezeAndThaw|TestRecordVerify' -v`
Expected: FAIL — `undefined: Steps`, `undefined: StepNames`.

- [ ] **Step 3: Implement**

First change `annotateManifest`'s signature in `preflight.go` to `func (p *Plan) annotateManifest(m *transfer.Manifest, memorySrcs map[string]bool)` and build `memorySrcs` at the call site from `inv.Memory` (`memorySrcs[e.Path()] = true`); the body uses `memorySrcs[e.Src]` instead of `memoryRoots[e.Src]`.

```go
// internal/orchestrate/steps.go
package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

// StepNames is the spec §6 order.
var StepNames = []string{"preflight", "freeze", "capture", "transfer", "install", "git-attach", "start", "shape", "thaw+exit", "record"}

var shells = map[string]bool{"bash": true, "zsh": true, "sh": true, "fish": true, "dash": true}

type runner struct {
	p        *Plan
	j        *job.Journal
	src, dst remote.Endpoint
	selfExe  string
	logf     func(string, ...any)
}

// Steps builds the job.Step list for the plan (spec §6 table).
func Steps(p *Plan, j *job.Journal, src, dst remote.Endpoint, selfExe string, logf func(string, ...any)) []job.Step {
	r := &runner{p: p, j: j, src: src, dst: dst, selfExe: selfExe, logf: logf}
	return []job.Step{
		{Name: "preflight", Verify: r.verifyPreflight, Run: r.runPreflight},
		{Name: "freeze", Verify: r.verifyFreeze, Run: r.runFreeze},
		{Name: "capture", Verify: r.verifyCapture, Run: r.runCapture},
		{Name: "transfer", Verify: r.verifyTransfer, Run: r.runTransfer},
		{Name: "install", Verify: r.verifyInstall, Run: r.runInstall},
		{Name: "git-attach", Verify: r.verifyGitAttach, Run: r.runGitAttach},
		{Name: "start", Verify: r.verifyStart, Run: r.runStart},
		{Name: "shape", Verify: r.verifyShape, Run: r.runShape},
		{Name: "thaw+exit", Verify: r.verifyThawExit, Run: r.runThawExit},
		{Name: "record", Verify: r.verifyRecord, Run: r.runRecord},
	}
}

// persist saves the plan into the journal here and on both hosts.
func (r *runner) persist(ctx context.Context) error {
	raw, err := r.p.ToJSON()
	if err != nil {
		return err
	}
	r.j.Plan = raw
	r.j.UpdatedAt = time.Now()
	if r.j.Dir != "" {
		if err := r.j.Save(); err != nil {
			return err
		}
	}
	if err := r.src.JournalPut(ctx, r.j); err != nil {
		return fmt.Errorf("journal put (source): %w", err)
	}
	if err := r.dst.JournalPut(ctx, r.j); err != nil {
		return fmt.Errorf("journal put (destination): %w", err)
	}
	return nil
}

func (r *runner) manifest() (*transfer.Manifest, error) { return transfer.Load(r.p.ManifestPath) }

func (r *runner) id() session.ID { return r.p.Session.ID }

func (r *runner) attempt(step string) string { return strconv.Itoa(r.j.Step(step).Attempts) }

func isCodeErr(err error, code string) bool {
	var re *remote.Error
	return errors.As(err, &re) && re.Code == code
}

// ---- 1 preflight --------------------------------------------------------

func (r *runner) verifyPreflight(ctx context.Context) (bool, error) {
	_, okS, err := r.src.JournalGet(ctx, r.p.JobID)
	if err != nil {
		return false, err
	}
	_, okD, err := r.dst.JournalGet(ctx, r.p.JobID)
	if err != nil {
		return false, err
	}
	return okS && okD, nil
}

func (r *runner) runPreflight(ctx context.Context) error { return r.persist(ctx) }

// ---- 2 freeze -----------------------------------------------------------

func (r *runner) verifyFreeze(ctx context.Context) (bool, error) {
	return r.p.sourceState() != session.StateRunning, nil
}

func (r *runner) runFreeze(ctx context.Context) error {
	reg := r.p.Session.Registry
	if _, ok, err := r.src.ClaudeStatus(ctx, r.id()); err != nil {
		return err
	} else if !ok {
		r.logf("freeze: source claude (pid %d) is no longer running; nothing to stop", reg.PID)
		return nil
	}
	r.logf("freeze: SIGSTOP pid %d", reg.PID)
	return r.src.Freeze(ctx, reg.PID, reg.ProcStart)
}

// ---- 3 capture ----------------------------------------------------------

func (r *runner) verifyCapture(ctx context.Context) (bool, error) {
	return r.p.Session.Tmux == nil, nil
}

func (r *runner) runCapture(ctx context.Context) error {
	if err := r.src.Capture(ctx, r.p.Session.Tmux, r.p.JobID); err != nil {
		return err
	}
	files := append([]session.FileEntry{}, r.p.Files...)
	capture := session.FileEntry{Root: job.Dir(r.p.SourceInfo.DataDir, r.p.JobID), Rel: "capture.txt", Category: session.CatCapture, Mode: 0o600}
	if r.p.CaptureEntryID == 0 {
		files = append(files, capture)
		r.p.Files = files
	}
	m, err := r.src.BuildManifest(ctx, r.p.JobID, r.id(), r.p.SourceInfo.Hostname, r.p.DestInfo.Hostname, files, r.p.PathMap)
	if err != nil {
		return err
	}
	memory := map[string]bool{}
	for _, e := range r.p.Extras.Memory {
		memory[e.Src] = true
	}
	r.p.annotateManifest(m, memory)
	for _, e := range m.Entries {
		if e.Category == session.CatCapture {
			r.p.CaptureEntryID = e.ID
			r.p.DestCapture = e.Dst
		}
	}
	if err := m.Save(r.p.ManifestPath); err != nil {
		return err
	}
	if r.p.Statuses, err = r.dst.ManifestDiff(ctx, m, r.p.JobID); err != nil {
		return err
	}
	return r.persist(ctx)
}

// ---- 4 transfer ---------------------------------------------------------

func (r *runner) verifyTransfer(ctx context.Context) (bool, error) {
	m, err := r.manifest()
	if err != nil {
		return false, err
	}
	if r.p.Statuses, err = r.dst.ManifestDiff(ctx, m, r.p.JobID); err != nil {
		return false, err
	}
	if err := r.persist(ctx); err != nil {
		return false, err
	}
	need := transfer.Need(m, r.p.Statuses)
	r.logf("transfer: %d of %d entries still needed", len(need), len(m.Entries))
	return len(need) == 0, nil
}

func (r *runner) pump(ctx context.Context, kind remote.StreamKind, n string) error {
	rd, err := r.src.OpenStream(ctx, kind, r.p.JobID, "send:"+n)
	if err != nil {
		return fmt.Errorf("open %s stream on source: %w", kind, err)
	}
	wr, err := r.dst.OpenStream(ctx, kind, r.p.JobID, "recv:"+n)
	if err != nil {
		rd.Close()
		return fmt.Errorf("open %s stream on destination: %w", kind, err)
	}
	_, copyErr := io.Copy(wr, rd)
	srcErr := rd.Close()
	dstErr := wr.Close()
	for _, e := range []error{copyErr, srcErr, dstErr} {
		if e != nil {
			return fmt.Errorf("%s stream: %w", kind, e)
		}
	}
	return nil
}

func (r *runner) runTransfer(ctx context.Context) error {
	if err := r.pump(ctx, remote.StreamTar, r.attempt("transfer")); err != nil {
		return err
	}
	m, err := r.manifest()
	if err != nil {
		return err
	}
	if r.p.Statuses, err = r.dst.ManifestDiff(ctx, m, r.p.JobID); err != nil {
		return err
	}
	if need := transfer.Need(m, r.p.Statuses); len(need) > 0 {
		return fmt.Errorf("%d entries still missing on the destination after the transfer (first: %s)", len(need), mustEntry(m, need[0]).Dst)
	}
	return r.persist(ctx)
}

func mustEntry(m *transfer.Manifest, id int) transfer.Entry {
	e, _ := m.ByID(id)
	return e
}

// ---- 5 install ----------------------------------------------------------

// deferred lists manifest ids that git-attach applies itself.
func (r *runner) deferred() map[int]bool {
	d := map[int]bool{}
	if r.p.Git != nil && r.p.Git.Mode == gitx.ModeExistingMain {
		for _, id := range r.p.Git.DirtyEntries {
			d[id] = true
		}
		if r.p.Git.IndexEntryID != 0 {
			d[r.p.Git.IndexEntryID] = true
		}
	}
	return d
}

func (r *runner) installManifest() (*transfer.Manifest, error) {
	m, err := r.manifest()
	if err != nil {
		return nil, err
	}
	d := r.deferred()
	im := *m
	im.Entries = nil
	for _, e := range m.Entries {
		if !d[e.ID] {
			im.Entries = append(im.Entries, e)
		}
	}
	return &im, nil
}

func (r *runner) verifyInstall(ctx context.Context) (bool, error) {
	im, err := r.installManifest()
	if err != nil {
		return false, err
	}
	st, err := r.dst.ManifestDiff(ctx, im, r.p.JobID)
	if err != nil {
		return false, err
	}
	memory := map[int]bool{}
	for _, e := range r.p.Extras.Memory {
		memory[e.ID] = true
	}
	for _, e := range im.Entries {
		if memory[e.ID] {
			continue
		}
		if st[e.ID] != transfer.PresentSame {
			return false, nil
		}
	}
	return true, nil
}

func (r *runner) runInstall(ctx context.Context) error {
	im, err := r.installManifest()
	if err != nil {
		return err
	}
	// Plan 02's Local.Install reads the merge inputs from jobs/<id>/extras.json
	// on the destination; both remote.Local and remote.Client expose
	// PutInstallExtras (op install-extras) outside the Endpoint interface.
	pe, ok := r.dst.(interface {
		PutInstallExtras(context.Context, string, transfer.InstallExtras) error
	})
	if !ok {
		return fmt.Errorf("install: destination endpoint %T has no PutInstallExtras", r.dst)
	}
	if r.p.Extras != nil {
		if err := pe.PutInstallExtras(ctx, r.p.JobID, *r.p.Extras); err != nil {
			return fmt.Errorf("install: put extras: %w", err)
		}
	}
	rep, err := r.dst.Install(ctx, im, r.p.JobID)
	if err != nil {
		return err
	}
	r.logf("install: %d installed, %d already present, %d fast-forwarded, index merged %d, history +%d, project entry added %v, memory copied %d (differs %d)",
		rep.Installed, rep.SkippedSame, rep.FastForwarded, rep.IndexMerged, rep.HistoryAdded, rep.ProjectEntryAdded, len(rep.MemoryCopied), len(rep.MemoryDiffers))
	for _, m := range rep.MemoryDiffers {
		r.logf("install: memory file differs on the destination and was left alone: %s", m)
	}
	return nil
}

// ---- 6 git-attach -------------------------------------------------------

func (r *runner) verifyGitAttach(ctx context.Context) (bool, error) {
	g := r.p.Git
	switch g.Mode {
	case gitx.ModeNotRepo:
		return true, nil
	case gitx.ModeFreshMain:
		ds, err := r.dst.GitDestState(ctx, g.DstMain, g.DstWorktree, g.Branch)
		if err != nil {
			return false, err
		}
		if g.Detached {
			return ds.WorktreeExists && ds.MainExists, nil
		}
		return ds.WorktreeExists && ds.WorktreeBranch == g.Branch, nil
	}
	return false, nil
}

func (r *runner) runGitAttach(ctx context.Context) error {
	if r.p.Git.NeedPack {
		if err := r.pump(ctx, remote.StreamPack, r.attempt("git-attach")); err != nil {
			return err
		}
	}
	return r.dst.GitAttach(ctx, r.p.Git, r.p.JobID)
}

// ---- 7 start ------------------------------------------------------------

func refString(ref *session.TmuxRef) string {
	return fmt.Sprintf("%s:%s.%s", ref.Session, ref.WindowID, ref.PaneID)
}

func (r *runner) captureOnDest() string {
	if r.p.CaptureEntryID == 0 {
		return ""
	}
	return r.p.DestCapture
}

func (r *runner) verifyStart(ctx context.Context) (bool, error) {
	if r.p.Tmux == nil {
		return r.p.DestRegistry != nil, nil
	}
	if r.p.DestRef == nil {
		return false, nil
	}
	reg, ok, err := r.dst.ClaudeStatus(ctx, r.id())
	if err != nil {
		return false, err
	}
	if !ok || reg.Tmux != refString(r.p.DestRef) {
		return false, nil
	}
	r.logf("start: session already alive in %s; confirming only", reg.Tmux)
	if r.p.DestRegistry, err = r.dst.ConfirmClaude(ctx, r.p.DestRef, r.id(), r.p.Options.StartTimeout); err != nil {
		return false, err
	}
	return true, r.persist(ctx)
}

func (r *runner) runStart(ctx context.Context) error {
	if r.p.Tmux == nil {
		r.logf("start: no tmux on the destination; resuming under a pty in %s", r.p.DestCwd)
		if err := r.dst.RunPtyResume(ctx, r.id(), r.p.DestCwd, r.p.Options.StartTimeout); err != nil {
			return err
		}
		r.p.DestRegistry = &session.Registry{SessionID: string(r.id()), Status: "exited"}
		return r.persist(ctx)
	}
	if r.p.DestRef != nil {
		st, err := r.dst.PaneState(ctx, r.p.DestRef)
		switch {
		case isCodeErr(err, "not-found"):
			r.p.DestRef = nil
		case err != nil:
			return err
		case !shells[st.Command]:
			return fmt.Errorf("destination pane %s we opened earlier now runs %q; refusing to type over it", r.p.DestRef.PaneID, st.Command)
		}
	}
	if r.p.DestRef == nil {
		sessions, err := r.dst.TmuxSessions(ctx, r.p.Tmux.SocketPath)
		if err != nil {
			return err
		}
		_, exists := tmuxx.BaseSession(sessions, r.p.Tmux.Group)
		ref, err := r.dst.OpenWindow(ctx, r.p.Tmux)
		if err != nil {
			return err
		}
		r.p.DestRef, r.p.CreatedWindow, r.p.CreatedSession = ref, true, !exists
		r.logf("start: opened %s (new session: %v)", refString(ref), !exists)
		if err := r.persist(ctx); err != nil {
			return err
		}
	}
	argv := PlaceholderArgv(r.id(), r.captureOnDest(), true, "", "")
	if err := r.dst.StartClaude(ctx, r.p.DestRef, r.id(), r.p.JobID, argv); err != nil {
		return err
	}
	reg, err := r.dst.ConfirmClaude(ctx, r.p.DestRef, r.id(), r.p.Options.StartTimeout)
	if err != nil {
		return err
	}
	r.logf("start: confirmed pid %d (claude %s) in %s", reg.PID, reg.Version, reg.Tmux)
	r.p.DestRegistry = reg
	r.p.StartedAt = time.Now()
	return r.persist(ctx)
}

// ---- 8 shape ------------------------------------------------------------

func (r *runner) verifyShape(ctx context.Context) (bool, error) {
	switch r.p.TargetState {
	case "running":
		return true, nil
	case "suspended":
		if r.p.DestRef == nil {
			return false, nil
		}
		st, err := r.dst.PaneState(ctx, r.p.DestRef)
		if err != nil {
			return false, err
		}
		_, ok := procx.IsPlaceholderArgv(st.Argv)
		return ok, nil
	case "idle":
		if r.p.Tmux == nil {
			return true, nil
		}
		st, err := r.dst.PaneState(ctx, r.p.DestRef)
		if isCodeErr(err, "not-found") {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return !r.p.CreatedWindow && shells[st.Command], nil
	}
	return false, fmt.Errorf("unknown target state %q", r.p.TargetState)
}

func (r *runner) runShape(ctx context.Context) error {
	reg := r.p.DestRegistry
	if _, ok, err := r.dst.ClaudeStatus(ctx, r.id()); err != nil {
		return err
	} else if ok {
		r.logf("shape: /exit on the destination (pid %d)", reg.PID)
		if err := r.dst.ExitClaude(ctx, r.p.DestRef, reg.PID, reg.ProcStart, r.p.Options.ExitTimeout); err != nil {
			return err
		}
	}
	switch r.p.TargetState {
	case "suspended":
		argv := SuspendArgv(r.id(), r.captureOnDest(), r.p.DestInfo.HasClaudeResume)
		r.logf("shape: typing %v", argv)
		return r.dst.TypeCommand(ctx, r.p.DestRef, argv)
	case "idle":
		if !r.p.CreatedWindow {
			return nil
		}
		st, err := r.dst.PaneState(ctx, r.p.DestRef)
		if err != nil {
			return err
		}
		if !shells[st.Command] {
			r.logf("shape: window %s still runs %q; leaving it", r.p.DestRef.WindowID, st.Command)
			return nil
		}
		r.logf("shape: killing window %s we created", r.p.DestRef.WindowID)
		return r.dst.KillWindow(ctx, r.p.DestRef)
	}
	return nil
}

// ---- 9 thaw+exit --------------------------------------------------------

func (r *runner) sourceAlive(ctx context.Context) (bool, error) {
	reg, ok, err := r.src.ClaudeStatus(ctx, r.id())
	if err != nil || !ok {
		return false, err
	}
	return reg.PID == r.p.Session.Registry.PID, nil
}

func (r *runner) verifyThawExit(ctx context.Context) (bool, error) {
	if r.p.sourceState() != session.StateRunning {
		return true, nil
	}
	alive, err := r.sourceAlive(ctx)
	if err != nil || alive {
		return false, err
	}
	if r.p.Session.Tmux == nil {
		return true, nil
	}
	st, err := r.src.PaneState(ctx, r.p.Session.Tmux)
	if isCodeErr(err, "not-found") {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	_, ok := procx.IsPlaceholderArgv(st.Argv)
	return ok, nil
}

func (r *runner) runThawExit(ctx context.Context) error {
	reg := r.p.Session.Registry
	alive, err := r.sourceAlive(ctx)
	if err != nil {
		return err
	}
	if alive {
		if err := r.src.Thaw(ctx, reg.PID); err != nil && !isCodeErr(err, "not-found") {
			return err
		}
		r.logf("thaw: SIGCONT pid %d", reg.PID)
		if r.p.Options.BangMode {
			if err := r.waitSourceIdle(ctx); err != nil {
				return err
			}
		}
		r.logf("exit: asking the source claude (pid %d) to exit", reg.PID)
		if err := r.src.ExitClaude(ctx, r.p.Session.Tmux, reg.PID, reg.ProcStart, r.p.Options.ExitTimeout); err != nil {
			return err
		}
	}
	if r.p.Session.Tmux == nil {
		return nil
	}
	st, err := r.src.PaneState(ctx, r.p.Session.Tmux)
	if err != nil {
		return err
	}
	if _, ok := procx.IsPlaceholderArgv(st.Argv); ok {
		return nil
	}
	if !shells[st.Command] {
		r.logf("exit: source pane runs %q, not typing the placeholder", st.Command)
		return nil
	}
	capture := ""
	if r.p.CaptureEntryID != 0 {
		capture = filepath.Join(job.Dir(r.p.SourceInfo.DataDir, r.p.JobID), "capture.txt")
	}
	argv := PlaceholderArgv(r.id(), capture, false, r.p.DestInfo.Hostname, time.Now().UTC().Format(time.RFC3339))
	return r.src.TypeCommand(ctx, r.p.Session.Tmux, argv)
}

// waitSourceIdle (!-mode): the foreground exits once this step starts,
// Claude records the command's result and returns to the prompt.
func (r *runner) waitSourceIdle(ctx context.Context) error {
	deadline := time.Now().Add(r.p.Options.ExitTimeout)
	for {
		reg, ok, err := r.src.ClaudeStatus(ctx, r.id())
		if err != nil {
			return err
		}
		if !ok || reg.Status == "idle" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("source claude did not return to the prompt within %s (status %q)", r.p.Options.ExitTimeout, reg.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// ---- 10 record ----------------------------------------------------------

func (r *runner) verifyRecord(ctx context.Context) (bool, error) {
	return r.j.Step("record").Status == job.Done, nil
}

func (r *runner) runRecord(ctx context.Context) error {
	rec := job.HistoryRecord{At: time.Now().UTC(), SessionID: string(r.id()), Direction: r.p.Options.Direction,
		From: r.p.SourceInfo.Hostname, To: r.p.DestInfo.Hostname, Outcome: "success", Note: "end state " + r.p.TargetState}
	if err := r.src.Record(ctx, r.p.JobID, rec); err != nil {
		return err
	}
	if err := r.dst.Record(ctx, r.p.JobID, rec); err != nil {
		return err
	}
	if err := r.dst.Cleanup(ctx, r.p.JobID); err != nil {
		return err
	}
	return r.src.Cleanup(ctx, r.p.JobID)
}
```

- [ ] **Step 4: Run the tests**

Run: `go vet ./internal/orchestrate/ && go test -race ./internal/orchestrate/ -run 'TestStepNames|TestFreezeAndThaw|TestRecordVerify' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrate
git commit -m "feat(orchestrate): the ten job steps with reality re-verification"
```

---

### Task 21: RunJob, the runner, and the teleport / continue commands

**Files:**
- Create: `internal/orchestrate/runner.go`
- Create: `internal/cli/teleport.go`, `internal/cli/endpoints.go`
- Modify: `internal/cli/root.go` (register the root run function and the `continue`, `internal-runner` subcommands next to Plan 01's `placeholder`, `version`, `internal-freezer` registrations)
- Test: `internal/orchestrate/runner_test.go`, `internal/cli/teleport_test.go`

**Interfaces:**
- Consumes: `job.Open/New/Run/FollowLog`, `procx.SpawnDetached`, `sshx.*`, `remote.NewClient`, `tmuxx.DialControl/FindServer/Prober`.
- Produces: `orchestrate.EndpointFactory`, `orchestrate.RunJob(ctx, dataDir, jobID string, factory EndpointFactory, selfExe string, logf func(string, ...any)) error`, `orchestrate.ExitCode(j *job.Journal) int`, `orchestrate.FailedStep(j *job.Journal) (string, bool)`, `Options.LocalDest *session.Paths` (test hook: a second Local endpoint instead of ssh).

- [ ] **Step 1: Write the failing tests**

```go
// internal/orchestrate/runner_test.go
package orchestrate

import (
	"context"
	"errors"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/remote"
)

func TestExitCodeMapping(t *testing.T) {
	j := &job.Journal{Outcome: "success", Finished: true}
	if ExitCode(j) != 0 {
		t.Error("success -> 0")
	}
	j = &job.Journal{Outcome: "failed", Finished: true, Steps: []job.StepState{{Name: "transfer", Status: job.Failed}}}
	if ExitCode(j) != 1 {
		t.Error("failed transfer -> 1")
	}
	j = &job.Journal{Outcome: "failed", Finished: true, Steps: []job.StepState{{Name: "start", Status: job.Failed, Error: "Not logged in"}}}
	if ExitCode(j) != 5 {
		t.Error("failed start -> 5")
	}
	if name, ok := FailedStep(j); !ok || name != "start" {
		t.Error("FailedStep")
	}
}

func TestRunJobMarksOutcomeAndThawsOnFailure(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := src.paths.Home + "/x"
	seedSession(t, src, cwd)
	o := baseOptions()
	o.State = "idle"
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
	boom := errors.New("simulated dropped connection")
	factory := func(context.Context, Options) (remote.Endpoint, remote.Endpoint, func(), error) {
		return src.ep, &failingEndpoint{Endpoint: dst.ep, failOpenStream: boom}, func() {}, nil
	}
	err = RunJob(context.Background(), src.paths.DataDir, sid, factory, selfExe(t), t.Logf)
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("RunJob err = %v, want the stream failure", err)
	}
	j2, _, _ := job.Open(src.paths.DataDir, sid)
	if j2.Outcome != "failed" || !j2.Finished {
		t.Errorf("journal after failure = %+v", j2)
	}
	if name, _ := FailedStep(j2); name != "transfer" {
		t.Errorf("failed step = %q", name)
	}
	// continue: a factory without the fault finishes the job.
	factory = func(context.Context, Options) (remote.Endpoint, remote.Endpoint, func(), error) { return src.ep, dst.ep, func() {}, nil }
	if err := RunJob(context.Background(), src.paths.DataDir, sid, factory, selfExe(t), t.Logf); err != nil {
		t.Fatal(err)
	}
	j3, _, _ := job.Open(src.paths.DataDir, sid)
	if j3.Outcome != "success" || ExitCode(j3) != 0 {
		t.Errorf("journal after continue = %+v", j3)
	}
}

// failingEndpoint wraps an Endpoint and fails OpenStream once.
type failingEndpoint struct {
	remote.Endpoint
	failOpenStream error
}

func (f *failingEndpoint) OpenStream(ctx context.Context, kind remote.StreamKind, jobID, streamID string) (io.ReadWriteCloser, error) {
	if f.failOpenStream != nil {
		err := f.failOpenStream
		f.failOpenStream = nil
		return nil, err
	}
	return f.Endpoint.OpenStream(ctx, kind, jobID, streamID)
}
```

(add `"io"` to the imports.)

```go
// internal/cli/teleport_test.go
package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestTeleportUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{},                                                // no direction
		{"--to", "a.example", "--from", "b.example"},      // both
		{"--to", "a.example", "--state", "sideways"},      // bad state
		{"--to", "a.example", "--map", "notapair"},        // bad map
	} {
		var out, errb bytes.Buffer
		code := Main(args, strings.NewReader(""), &out, &errb, []string{"HOME=/home/alice", "PATH=/usr/bin"})
		if code != ExitUsage {
			t.Errorf("Main(%v) = %d (%s), want %d", args, code, errb.String(), ExitUsage)
		}
	}
}

func TestParseMaps(t *testing.T) {
	m, err := parseMaps([]string{"/home/alice/a=/srv/a", "/x=/y"})
	if err != nil || len(m) != 2 || m[0].From != "/home/alice/a" || m[1].To != "/y" {
		t.Fatalf("parseMaps = %v %v", m, err)
	}
	if _, err := parseMaps([]string{"relative=/y"}); err == nil {
		t.Error("relative source must be rejected")
	}
}

func TestInternalRunnerUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Main([]string{"internal-runner"}, strings.NewReader(""), &out, &errb, []string{"HOME=/home/alice"}); code != ExitUsage {
		t.Errorf("internal-runner without a job dir = %d", code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/orchestrate/ -run 'TestExitCode|TestRunJob' -v; go test ./internal/cli/ -run 'TestTeleportUsage|TestParseMaps|TestInternalRunner' -v`
Expected: FAIL — `undefined: RunJob`, `undefined: parseMaps`; `Main` with `--to` returns something other than `ExitUsage` for the bad-state case because the flags do not exist yet.

- [ ] **Step 3: Implement RunJob**

```go
// internal/orchestrate/runner.go
package orchestrate

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// EndpointFactory builds (source, destination) for the options; closeFn
// releases ssh connections. The cli supplies it; tests supply Locals.
type EndpointFactory func(ctx context.Context, o Options) (src, dst remote.Endpoint, closeFn func(), err error)

// RunJob is the detached runner's main: load the journal and plan, build
// the endpoints, run the steps, record the outcome. On failure the source
// is thawed (the freezer would also thaw on our death).
func RunJob(ctx context.Context, dataDir, jobID string, factory EndpointFactory, selfExe string, logf func(string, ...any)) error {
	j, ok, err := job.Open(dataDir, jobID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no job %s under %s", jobID, dataDir)
	}
	p, err := PlanFromJournal(j)
	if err != nil {
		return err
	}
	src, dst, closeFn, err := factory(ctx, p.Options)
	if err != nil {
		return err
	}
	defer closeFn()
	j.RunnerPID = os.Getpid()
	j.Finished, j.Outcome = false, ""
	if err := j.Save(); err != nil {
		return err
	}
	logf("runner %d: job %s (%s -> %s), continuing at %s", os.Getpid(), jobID, p.SourceInfo.Hostname, p.DestInfo.Hostname, firstIncomplete(j))
	runErr := job.Run(ctx, j, Steps(p, j, src, dst, selfExe, logf), logf)
	if runErr != nil {
		j.Outcome = "failed"
		if p.sourceState() == session.StateRunning && p.Session.Registry != nil {
			if terr := src.Thaw(context.Background(), p.Session.Registry.PID); terr != nil {
				logf("thaw after failure: %v", terr)
			} else {
				logf("thawed source claude (pid %d) after failure", p.Session.Registry.PID)
			}
		}
		if name, _ := FailedStep(j); name != "" {
			logf("FAILED at step %s: %v", name, runErr)
			logf("next: claude-teleport status %s | claude-teleport continue %s | claude-teleport abandon %s", jobID, jobID, jobID)
		}
	} else {
		j.Outcome = "success"
		logf("done: session %s is now on %s (%s)", p.Session.ID.Short(), p.DestInfo.Hostname, p.TargetState)
	}
	j.Finished = true
	j.UpdatedAt = time.Now()
	if err := j.Save(); err != nil {
		return err
	}
	_ = src.JournalPut(context.Background(), j)
	_ = dst.JournalPut(context.Background(), j)
	return runErr
}

func firstIncomplete(j *job.Journal) string {
	if name, ok := j.FirstIncomplete(); ok {
		return name
	}
	return "preflight"
}

// FailedStep names the step marked Failed, if any.
func FailedStep(j *job.Journal) (string, bool) {
	for _, s := range j.Steps {
		if s.Status == job.Failed {
			return s.Name, true
		}
	}
	return "", false
}

// ExitCode maps a finished journal to the spec §5 exit code.
func ExitCode(j *job.Journal) int {
	switch j.Outcome {
	case "success":
		return 0
	case "failed":
		if name, _ := FailedStep(j); name == "start" {
			return 5
		}
		return 1
	}
	return 1
}
```

Add to `Options` in `options.go`:

```go
	// LocalDest, when set, makes the destination a second in-process Local
	// endpoint with these paths instead of an ssh client (tests only; no
	// flag exposes it).
	LocalDest *session.Paths `json:"local_dest,omitempty"`
```

- [ ] **Step 4: Implement the endpoint factory and the commands**

```go
// internal/cli/endpoints.go
package cli

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/kevinburke/ssh_config"

	"github.com/mithro/go-claude-teleport/internal/orchestrate"
	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/sshx"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

// tmuxSocketDir is where tmux keeps sockets for this uid.
func tmuxSocketDir(env map[string]string) string {
	base := env["TMUX_TMPDIR"]
	if base == "" {
		base = "/tmp"
	}
	return filepath.Join(base, fmt.Sprintf("tmux-%d", os.Getuid()))
}

// localEndpoint builds this host's Local with a pane probe when a tmux
// server is reachable ($TMUX first, then spec §9 discovery).
func (a *app) localEndpoint(ctx context.Context, p session.Paths) *remote.Local {
	opts := remote.LocalOptions{ProcRoot: "/proc", Tmux: tmuxx.DialControl, TmuxSocketDir: tmuxSocketDir(a.env), Logf: a.logf}
	sock := ""
	if t := a.env["TMUX"]; t != "" {
		sock = strings.SplitN(t, ",", 2)[0]
	} else if s, err := tmuxx.FindServer(opts.TmuxSocketDir, "", ""); err == nil {
		sock = s
	}
	if sock != "" {
		if tr, err := tmuxx.DialControl(ctx, sock); err == nil {
			if procs, err := procx.Scan("/proc"); err == nil {
				opts.Probe = tmuxx.Prober(ctx, tr, procs, sock)
				a.closers = append(a.closers, tr.Close)
			}
		}
	}
	return remote.NewLocal(p, a.selfExe, opts)
}

func loadSSHConfig(home string) (*ssh_config.Config, error) {
	f, err := os.Open(filepath.Join(home, ".ssh", "config"))
	if os.IsNotExist(err) {
		return &ssh_config.Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ssh_config.Decode(f)
}

// dialRemote resolves and dials target through via, returning the remote
// endpoint (a `claude-teleport remote serve` on the far side).
func (a *app) dialRemote(ctx context.Context, target string, via []string, overrides map[string]string) (*remote.Client, func(), error) {
	t, err := sshx.ParseTarget(target)
	if err != nil {
		return nil, nil, err
	}
	for _, v := range via {
		vt, err := sshx.ParseTarget(v)
		if err != nil {
			return nil, nil, fmt.Errorf("--via %s: %w", v, err)
		}
		t.Via = append(t.Via, vt)
	}
	cfg, err := loadSSHConfig(a.paths.Home)
	if err != nil {
		return nil, nil, err
	}
	u, _ := user.Current()
	localUser := ""
	if u != nil {
		localUser = u.Username
	}
	r, err := sshx.Resolve(t, cfg, overrides, localUser)
	if err != nil {
		return nil, nil, err
	}
	strict := overrides["StrictHostKeyChecking"]
	if strict == "" {
		strict = "yes"
	}
	c, err := sshx.Dial(ctx, r, cfg, overrides, sshx.Options{
		KnownHostsFile: filepath.Join(a.paths.Home, ".ssh", "known_hosts"),
		AgentSocket:    a.env["SSH_AUTH_SOCK"],
		StrictHostKey:  strict,
		ConnectTimeout: 30 * time.Second,
		Logf:           a.logf,
	})
	if err != nil {
		return nil, nil, &orchestrate.UnreachableError{Host: target, Err: err}
	}
	ep, err := remote.NewClient(ctx, c, "claude-teleport", a.logf)
	if err != nil {
		c.Close()
		return nil, nil, &orchestrate.UnreachableError{Host: target, Err: err}
	}
	return ep, func() { ep.Close(); c.Close() }, nil
}

// endpoints is the orchestrate.EndpointFactory: local on this side,
// ssh (or LocalDest) on the other, ordered by Direction.
func (a *app) endpoints(ctx context.Context, o orchestrate.Options) (remote.Endpoint, remote.Endpoint, func(), error) {
	local := a.localEndpoint(ctx, a.paths)
	var other remote.Endpoint
	closeFn := func() {}
	if o.LocalDest != nil {
		other = a.localEndpoint(ctx, *o.LocalDest)
	} else {
		c, cl, err := a.dialRemote(ctx, o.Target, o.Via, o.SSHOptions)
		if err != nil {
			return nil, nil, nil, err
		}
		other, closeFn = c, cl
	}
	if o.Direction == "from" {
		return other, local, closeFn, nil
	}
	return local, other, closeFn, nil
}
```

The `app` struct is Plan 01's per-invocation context (`internal/cli/cli.go`: `stdin`, `stdout`, `stderr`, `env map[string]string`, `configDir string`, `flags *teleportFlags`). This task adds the fields `paths session.Paths`, `selfExe string`, `logf func(string, ...any)` and `closers []func() error`: `Main` sets `a.selfExe` from `os.Executable()` (fallback `"claude-teleport"`) and `a.logf` to print to `a.stderr`; `a.paths` is filled by a `PersistentPreRunE` on the root command that calls Plan 01's `a.resolvePaths()` (so `--config-dir` is honoured); `Main` calls every `closers` entry after `root.Execute()`.

```go
// internal/cli/teleport.go
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/orchestrate"
	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// teleportFlags and its flag registration are Plan 01's (root.go, Task 20);
// this task only adds IncludeIgnored / --include-ignored there (see the note
// after the code).

func parseMaps(specs []string) ([]session.Mapping, error) {
	var out []session.Mapping
	for _, s := range specs {
		from, to, ok := strings.Cut(s, "=")
		if !ok || !filepath.IsAbs(from) || !filepath.IsAbs(to) {
			return nil, fmt.Errorf("--map %q: want ABSOLUTE_SRC=ABSOLUTE_DST", s)
		}
		out = append(out, session.Mapping{From: filepath.Clean(from), To: filepath.Clean(to)})
	}
	return out, nil
}

func parseSSHOptions(specs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, s := range specs {
		k, v, ok := strings.Cut(s, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("-o %q: want KEY=VALUE", s)
		}
		out[k] = v
	}
	return out, nil
}

// teleportOptions validates the flags into orchestrate.Options.
func (a *app) teleportOptions(f teleportFlags, args []string) (orchestrate.Options, error) {
	if (f.To == "") == (f.From == "") {
		return orchestrate.Options{}, errors.New("exactly one of --to HOST or --from HOST is required")
	}
	switch f.State {
	case "auto", "running", "suspended", "idle":
	default:
		return orchestrate.Options{}, fmt.Errorf("--state %q: want auto|running|suspended|idle", f.State)
	}
	if f.NoTmux && f.State != "idle" && f.State != "auto" {
		return orchestrate.Options{}, fmt.Errorf("--no-tmux only allows --state idle, not %q", f.State)
	}
	maps, err := parseMaps(f.Maps)
	if err != nil {
		return orchestrate.Options{}, err
	}
	sshOpts, err := parseSSHOptions(f.SSHOptions)
	if err != nil {
		return orchestrate.Options{}, err
	}
	sel, err := session.ParseSelector(args, session.Env{SessionID: a.env["CLAUDE_CODE_SESSION_ID"], PID: a.env["CLAUDE_PID"], TmuxPane: a.env["TMUX_PANE"], Tmux: a.env["TMUX"]})
	if err != nil {
		return orchestrate.Options{}, err
	}
	o := orchestrate.Options{Direction: "to", Target: f.To, Selector: sel, DestPath: f.DestPath, Maps: maps, State: f.State,
		AllowDrift: f.AllowDrift, Force: f.Force, TmuxSocket: f.TmuxSocket, NoTmux: f.NoTmux, Excludes: f.Excludes, IncludeIgnored: f.IncludeIgnored,
		ExitTimeout: f.ExitTimeout, StartTimeout: f.StartTimeout, Via: f.Via, SSHOptions: sshOpts}
	if f.From != "" {
		o.Direction, o.Target = "from", f.From
	}
	if f.DestPath != "" && !filepath.IsAbs(f.DestPath) {
		return orchestrate.Options{}, fmt.Errorf("--dest-path %q must be absolute", f.DestPath)
	}
	return o, nil
}

// runTeleport is the root command (spec §5, §6).
func (a *app) runTeleport(ctx context.Context, f teleportFlags, args []string) int {
	o, err := a.teleportOptions(f, args)
	if err != nil {
		fmt.Fprintln(a.stderr, "usage:", err)
		return ExitUsage
	}
	src, dst, closeFn, err := a.endpoints(ctx, o)
	if err != nil {
		return a.fail(err)
	}
	sess, err := src.ResolveSession(ctx, o.Selector)
	if err != nil {
		closeFn()
		return a.fail(err)
	}
	jobID := string(sess.ID)
	if pid, _ := strconv.Atoi(a.env["CLAUDE_PID"]); pid != 0 && sess.Registry != nil && sess.Registry.PID == pid {
		if o.Direction != "to" {
			closeFn()
			fmt.Fprintln(a.stderr, "usage: running inside the session being moved (!-mode) only works with --to")
			return ExitUsage
		}
		o.BangMode = true
	}
	if j, ok, err := job.Open(a.paths.DataDir, jobID); err != nil {
		closeFn()
		return a.fail(err)
	} else if ok && j.Outcome != "success" && j.Outcome != "abandoned" {
		closeFn()
		fmt.Fprintf(a.stdout, "job %s is %s at step %s; continuing it (use `abandon` to start over)\n", sess.ID.Short(), stateWord(j), firstIncompleteName(j))
		return a.continueJob(ctx, j, o.BangMode)
	}
	plan, err := orchestrate.Preflight(ctx, o, src, dst, jobID)
	if err != nil {
		closeFn()
		return a.fail(err)
	}
	plan.Render(a.stdout)
	if f.DryRun {
		closeFn()
		fmt.Fprintln(a.stdout, "dry run: nothing was moved")
		return ExitOK
	}
	j, err := job.New(a.paths.DataDir, jobID)
	if err != nil {
		closeFn()
		return a.fail(err)
	}
	j.SessionID, j.Direction = jobID, o.Direction
	j.SourceHost, j.DestHost = plan.SourceInfo.Hostname, plan.DestInfo.Hostname
	j.CreatedAt, j.UpdatedAt = time.Now(), time.Now()
	if j.Plan, err = plan.ToJSON(); err != nil {
		closeFn()
		return a.fail(err)
	}
	if err := j.Save(); err != nil {
		closeFn()
		return a.fail(err)
	}
	closeFn() // the runner re-dials
	return a.spawnAndFollow(ctx, j, o.BangMode)
}

func stateWord(j *job.Journal) string {
	if j.Outcome == "failed" {
		return "failed"
	}
	return "in progress"
}

func firstIncompleteName(j *job.Journal) string {
	if n, ok := j.FirstIncomplete(); ok {
		return n
	}
	return "preflight"
}

// fail prints err and maps it to an exit code.
func (a *app) fail(err error) int {
	var re *orchestrate.RefusedError
	var ue *orchestrate.UnreachableError
	switch {
	case errors.As(err, &re):
		fmt.Fprintln(a.stderr, "claude-teleport:", err)
		return ExitRefused
	case errors.As(err, &ue):
		fmt.Fprintln(a.stderr, "claude-teleport:", err)
		return ExitUnreachable
	case errors.Is(err, session.ErrNotFound):
		fmt.Fprintln(a.stderr, "claude-teleport:", err)
		return ExitUsage
	}
	fmt.Fprintln(a.stderr, "claude-teleport:", err)
	return ExitFailed
}

// spawnAndFollow starts the detached runner and follows its log.
func (a *app) spawnAndFollow(ctx context.Context, j *job.Journal, bang bool) int {
	env := make([]string, 0, len(a.env))
	for k, v := range a.env {
		env = append(env, k+"="+v)
	}
	pid, err := procx.SpawnDetached([]string{a.selfExe, "internal-runner", j.Dir}, "/", j.LogPath(), env)
	if err != nil {
		return a.fail(fmt.Errorf("start runner: %w", err))
	}
	j.RunnerPID = pid
	if err := j.Save(); err != nil {
		return a.fail(err)
	}
	a.logf("runner pid %d, log %s", pid, j.LogPath())
	return a.follow(ctx, j, bang)
}

// follow streams log.txt until the job finishes (or, in !-mode, until
// step thaw+exit begins — after which the parent Claude must be free to
// read our exit), then maps the journal to an exit code.
func (a *app) follow(ctx context.Context, j *job.Journal, bang bool) int {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	reload := func() *job.Journal {
		jj, ok, err := job.Open(a.paths.DataDir, j.ID)
		if err != nil || !ok {
			return j
		}
		return jj
	}
	done := func() bool {
		jj := reload()
		if bang {
			st := jj.Step("thaw+exit")
			return jj.Finished || st.Status != job.Pending
		}
		return jj.Finished
	}
	if bang {
		for !done() {
			select {
			case <-ctx.Done():
				fmt.Fprintf(a.stderr, "interrupted; the runner keeps going: claude-teleport status %s\n", j.ID)
				return ExitInterrupted
			case <-time.After(250 * time.Millisecond):
			}
		}
	} else if err := job.FollowLog(ctx, j.LogPath(), a.stdout, done); err != nil {
		if ctx.Err() != nil {
			fmt.Fprintf(a.stderr, "\ninterrupted; the runner keeps going. Watch it with: claude-teleport status %s\n", j.ID)
			return ExitInterrupted
		}
		return a.fail(err)
	}
	jj := reload()
	if bang && !jj.Finished {
		p, err := orchestrate.PlanFromJournal(jj)
		if err == nil {
			fmt.Fprintf(a.stdout, "teleported session %s to %s (%s); this Claude will now exit.\n", session.ID(j.ID).Short(), p.DestInfo.Hostname, p.TargetState)
		}
		return ExitOK
	}
	code := orchestrate.ExitCode(jj)
	if code != 0 {
		if name, ok := orchestrate.FailedStep(jj); ok {
			fmt.Fprintf(a.stderr, "teleport failed at step %s: %s\n", name, jj.Step(name).Error)
			if name == "start" {
				fmt.Fprintln(a.stderr, "the destination Claude did not resume — log in there (`claude` then /login) and run: claude-teleport continue", j.ID)
			} else {
				fmt.Fprintf(a.stderr, "next: claude-teleport status %s | continue %s | abandon %s\n", j.ID, j.ID, j.ID)
			}
		}
		if bang {
			for _, l := range tailLog(jj.LogPath(), 20) {
				fmt.Fprintln(a.stderr, l)
			}
		}
	}
	return code
}

func tailLog(path string, n int) []string {
	lines, err := job.TailLog(path, n)
	if err != nil {
		return nil
	}
	return lines
}

// continueJob attaches to a live runner or respawns a dead one.
func (a *app) continueJob(ctx context.Context, j *job.Journal, bang bool) int {
	alive := func(pid int) bool {
		t, err := procx.Scan("/proc")
		if err != nil {
			return false
		}
		p, ok := t.Get(pid)
		return ok && strings.Contains(strings.Join(p.Cmdline, " "), "internal-runner")
	}
	if j.RunnerAlive(alive) {
		fmt.Fprintf(a.stdout, "attaching to runner %d\n", j.RunnerPID)
		return a.follow(ctx, j, bang)
	}
	return a.spawnAndFollow(ctx, j, bang)
}

func newContinueCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "continue <sid>",
		Short: "resume an interrupted teleport",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := session.ParseID(args[0])
			if err != nil {
				return usageErr(err)
			}
			j, ok, err := job.Open(a.paths.DataDir, string(id))
			if err != nil {
				return err
			}
			if !ok {
				return usageErr(fmt.Errorf("no job for session %s", id.Short()))
			}
			if j.Outcome == "success" {
				fmt.Fprintf(a.stdout, "job %s already finished successfully\n", id.Short())
				return nil
			}
			if j.Outcome == "abandoned" {
				return usageErr(fmt.Errorf("job %s was abandoned; start a new teleport", id.Short()))
			}
			return exitErr(a.continueJob(cmd.Context(), j, false))
		},
	}
}

func newInternalRunnerCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:    "internal-runner <job-dir>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jobDir := filepath.Clean(args[0])
			dataDir := filepath.Dir(filepath.Dir(jobDir))
			id := filepath.Base(jobDir)
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, os.Interrupt)
			defer stop()
			logf := func(format string, v ...any) {
				fmt.Fprintf(a.stderr, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, v...))
			}
			a.logf = logf
			if err := orchestrate.RunJob(ctx, dataDir, id, a.endpoints, a.selfExe, logf); err != nil {
				return exitErr(ExitFailed)
			}
			return nil
		},
	}
}
```

`usageErr(err)` and `exitErr(code)` are two small adapters over Plan 01's `cli.ExitError`/`cli.Exit` (Plan 01 Task 1), defined in `teleport.go`:

```go
// usageErr maps a bad invocation to exit 2 through Plan 01's ExitError.
func usageErr(err error) error { return Exit(ExitUsage, "%v", err) }

// errReported marks an error whose message a.fail already printed.
var errReported = errors.New("")

// exitErr turns an already-reported exit code into the error Main maps.
func exitErr(code int) error {
	if code == ExitOK {
		return nil
	}
	return &ExitError{Code: code, Err: errReported}
}
```

and Plan 01's `Main` (cli.go) is changed to print `ee.Err` only when `ee.Err.Error() != ""` so the message is not printed twice.

Flags: Plan 01's `root.go` already defines `teleportFlags` (exported fields `To, From, Via, SSHOptions, DestPath, Maps, State, AllowDrift, Force, TmuxSocket, NoTmux, Excludes, DryRun, ExitTimeout, StartTimeout, LogFile, JSON, Verbose, Quiet`) and registers every flag in `rootCmd()`; do **not** add a second `teleportFlags`/`addTeleportFlags` — delete that block from `teleport.go` above, add `IncludeIgnored bool` + `f.BoolVar(&tf.IncludeIgnored, "include-ignored", false, "also transfer gitignored files")` to Plan 01's struct/`rootCmd`, and read the exported names in `teleportOptions`/`runTeleport` (already written that way above). Register in `root.go`: replace the Plan 01 `RunE` body (the "transport not implemented yet" stub) with `return exitErr(a.runTeleport(cmd.Context(), tf, args))` after `tf.validate(args)`; `root.AddCommand(newContinueCmd(a), newInternalRunnerCmd(a))`. Plan 02's `AddTransportCommands` (transport.go) registered a provisional `internal-runner` that fails with "no steps registered" and exported `var RunnerSteps` for this plan: delete that command block, the `RunnerSteps` var and `TestInternalRunnerWithoutStepsIsExplicit` — `newInternalRunnerCmd` is the real runner. `internal-runner` writes its log lines to `a.stderr`, which `SpawnDetached` pointed at `log.txt`.

- [ ] **Step 5: Run the tests**

Run: `go vet ./... && go test -race ./internal/orchestrate/ -run 'TestExitCode|TestRunJob' -v && go test -race ./internal/cli/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/orchestrate/runner.go internal/orchestrate/runner_test.go internal/orchestrate/options.go internal/cli
git commit -m "feat(cli): teleport, continue and the detached internal-runner"
```

---

### Task 22: orchestrator end-to-end tests (fake tmux, real tmux, killed runner)

**Files:**
- Test: `internal/orchestrate/e2e_test.go`, `internal/orchestrate/e2e_live_test.go` (tag `tmuxlive`), `internal/orchestrate/e2e_runner_test.go`

- [ ] **Step 1: Write the end-to-end tests with the fake tmux**

```go
// internal/orchestrate/e2e_test.go
package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/session"
)

// teleport runs preflight + all steps in-process and returns the journal.
func teleport(t *testing.T, o Options, src, dst *host) (*Plan, *job.Journal) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	driver := src
	if o.Direction == "from" {
		driver = dst
	}
	p, err := Preflight(ctx, o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	j, err := job.New(driver.paths.DataDir, sid)
	if err != nil {
		t.Fatal(err)
	}
	j.Plan, _ = p.ToJSON()
	j.Save()
	factory := func(context.Context, Options) (remote.Endpoint, remote.Endpoint, func(), error) { return src.ep, dst.ep, func() {}, nil }
	if err := RunJob(ctx, driver.paths.DataDir, sid, factory, selfExe(t), t.Logf); err != nil {
		t.Fatalf("run: %v", err)
	}
	jj, _, _ := job.Open(driver.paths.DataDir, sid)
	p, _ = PlanFromJournal(jj)
	return p, jj
}

func readTranscript(t *testing.T, h *host, cwd string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.paths.ProjectDir(cwd), sid+".jsonl"))
	if err != nil {
		t.Fatalf("transcript on %s: %v", h.name, err)
	}
	return string(b)
}

func TestE2ERunningWorktreeFreshMain(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", newFakeTmux())
	dst := newHost(t, "big-storage.example", "bob", newFakeTmux())
	main := filepath.Join(src.paths.Home, "github", "x")
	makeRepo(t, main)
	w := filepath.Join(main, ".worktrees", "feat")
	gitc(t, main, "worktree", "add", "-q", "-b", "feat", w)
	os.WriteFile(filepath.Join(w, "wip.go"), []byte("package wip\n"), 0o644)
	seedSession(t, src, w)
	startClaudeInPane(t, src, "work", w)

	o := baseOptions()
	p, j := teleport(t, o, src, dst)
	if j.Outcome != "success" {
		t.Fatalf("outcome %q", j.Outcome)
	}
	// Destination: transcript rewritten, worktree attached, Claude running in group "work".
	dstW := filepath.Join(dst.paths.Home, "github", "x", ".worktrees", "feat")
	tr := readTranscript(t, dst, dstW)
	if strings.Contains(tr, src.paths.Home) || !strings.Contains(tr, dstW) {
		t.Errorf("destination transcript not rewritten:\n%s", tr)
	}
	if diff := cmp.Diff(strings.TrimSpace(gitc(t, w, "status", "--porcelain")), strings.TrimSpace(gitc(t, dstW, "status", "--porcelain"))); diff != "" {
		t.Errorf("git status differs (-src +dst):\n%s", diff)
	}
	reg := waitRegistry(t, dst, "idle")
	if !strings.HasPrefix(reg.Tmux, "work:") || reg.Cwd != dstW {
		t.Errorf("dest registry = %+v", reg)
	}
	if p.Git.Mode != gitx.ModeFreshMain || !p.CreatedSession || !p.CreatedWindow {
		t.Errorf("plan = mode %s created %v/%v", p.Git.Mode, p.CreatedSession, p.CreatedWindow)
	}
	// Source: Claude exited, pane shows the placeholder with --teleported-to.
	if _, ok, _ := src.ep.ClaudeStatus(context.Background(), session.ID(sid)); ok {
		t.Error("source claude still running")
	}
	st, err := src.ep.PaneState(context.Background(), p.Session.Tmux)
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := procx.IsPlaceholderArgv(st.Argv); !ok || id != sid || !contains(st.Argv, "--teleported-to") {
		t.Errorf("source pane argv = %v", st.Argv)
	}
	// Source files untouched; history recorded on both; staging gone.
	if _, err := os.Stat(filepath.Join(src.paths.ProjectDir(w), sid+".jsonl")); err != nil {
		t.Error("source transcript removed")
	}
	for _, h := range []*host{src, dst} {
		if _, err := os.Stat(filepath.Join(job.Dir(h.paths.DataDir, sid), "history.jsonl")); err != nil {
			t.Errorf("history missing on %s", h.name)
		}
	}
	if _, err := os.Stat(job.StagingDir(dst.paths.DataDir, sid)); !os.IsNotExist(err) {
		t.Error("staging not cleaned up")
	}
}

func TestE2EFromDirection(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", newFakeTmux())
	dst := newHost(t, "big-storage.example", "bob", newFakeTmux())
	cwd := filepath.Join(src.paths.Home, "proj")
	makeRepo(t, cwd)
	seedSession(t, src, cwd)
	startClaudeInPane(t, src, "main", cwd)
	o := baseOptions()
	o.Direction, o.Target = "from", "alice@laptop.example"
	_, j := teleport(t, o, src, dst)
	if j.Outcome != "success" || j.Dir != job.Dir(dst.paths.DataDir, sid) {
		t.Fatalf("outcome %q dir %s", j.Outcome, j.Dir)
	}
	waitRegistry(t, dst, "idle")
}

func TestE2EMainCheckoutExistingMainFastForward(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", newFakeTmux())
	dst := newHost(t, "big-storage.example", "bob", newFakeTmux())
	cwd := filepath.Join(src.paths.Home, "proj")
	makeRepo(t, cwd)
	dstRepo := filepath.Join(dst.paths.Home, "proj")
	gitc(t, filepath.Dir(dstRepo), "clone", "-q", cwd, dstRepo)
	gitc(t, dstRepo, "remote", "remove", "origin")
	os.WriteFile(filepath.Join(cwd, "more.txt"), []byte("more\n"), 0o644)
	gitc(t, cwd, "add", ".")
	gitc(t, cwd, "commit", "-q", "-m", "more")
	tip := strings.TrimSpace(gitc(t, cwd, "rev-parse", "HEAD"))
	os.WriteFile(filepath.Join(cwd, "untracked.txt"), []byte("u\n"), 0o644)
	seedSession(t, src, cwd)
	startClaudeInPane(t, src, "main", cwd)
	p, j := teleport(t, baseOptions(), src, dst)
	if j.Outcome != "success" || p.Git.Mode != gitx.ModeExistingMain || !p.Git.FastForward {
		t.Fatalf("outcome %q plan %+v", j.Outcome, p.Git)
	}
	if got := strings.TrimSpace(gitc(t, dstRepo, "rev-parse", "HEAD")); got != tip {
		t.Errorf("dest HEAD %s, want %s", got, tip)
	}
	if got := gitc(t, dstRepo, "status", "--porcelain"); !strings.Contains(got, "?? untracked.txt") {
		t.Errorf("dest status:\n%s", got)
	}
}

func TestE2ESuspendedSourceStaysSuspended(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", newFakeTmux())
	dst := newHost(t, "big-storage.example", "bob", newFakeTmux())
	cwd := filepath.Join(src.paths.Home, "proj")
	seedSession(t, src, cwd)
	ctx := context.Background()
	ref, _ := src.tmux.Run(ctx, `new-session -d -s "work" -n "claude" -c "`+cwd+`" -P -F "#{pane_id}	#{window_id}	#{session_name}"`)
	paneID := strings.Split(ref[0], "\t")[0]
	if _, err := src.tmux.Run(ctx, `send-keys -t "`+paneID+`" " claude-teleport placeholder --resume `+sid+`" Enter`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	src.refreshProbe(t)
	o := baseOptions()
	o.Selector = session.Selector{TmuxSess: "work", TmuxWindow: "claude"}
	p, j := teleport(t, o, src, dst)
	if j.Outcome != "success" || p.Session.State != session.StateSuspended || p.TargetState != "suspended" {
		t.Fatalf("outcome %q state %s target %s", j.Outcome, p.Session.State, p.TargetState)
	}
	st, err := dst.ep.PaneState(ctx, p.DestRef)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := procx.IsPlaceholderArgv(st.Argv); !ok {
		t.Errorf("dest pane should show the placeholder, argv = %v", st.Argv)
	}
	if _, ok, _ := dst.ep.ClaudeStatus(ctx, session.ID(sid)); ok {
		t.Error("dest claude should have exited for the suspended state")
	}
}

func TestE2EIdleNoTmuxDestination(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", newFakeTmux())
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := filepath.Join(src.paths.Home, "proj")
	seedSession(t, src, cwd)
	startClaudeInPane(t, src, "main", cwd)
	o := baseOptions()
	o.State = "idle"
	p, j := teleport(t, o, src, dst)
	if j.Outcome != "success" || p.Tmux != nil {
		t.Fatalf("outcome %q tmux %+v", j.Outcome, p.Tmux)
	}
	readTranscript(t, dst, filepath.Join(dst.paths.Home, "proj"))
	if _, ok, _ := dst.ep.ClaudeStatus(context.Background(), session.ID(sid)); ok {
		t.Error("dest claude must not be running after the pty confirmation")
	}
}

func TestE2EReTeleportBackFastForwardsTranscript(t *testing.T) {
	a := newHost(t, "laptop.example", "alice", newFakeTmux())
	b := newHost(t, "big-storage.example", "bob", newFakeTmux())
	cwd := filepath.Join(a.paths.Home, "proj")
	seedSession(t, a, cwd)
	startClaudeInPane(t, a, "main", cwd)
	if _, j := teleport(t, baseOptions(), a, b); j.Outcome != "success" {
		t.Fatal("first teleport failed")
	}
	// Work happens on b: the transcript grows.
	bCwd := filepath.Join(b.paths.Home, "proj")
	bTr := filepath.Join(b.paths.ProjectDir(bCwd), sid+".jsonl")
	f, _ := os.OpenFile(bTr, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"type":"user","sessionId":"` + sid + `","cwd":"` + bCwd + `","timestamp":"2026-08-27T11:00:00Z","message":{"role":"user","content":"more work"}}` + "\n")
	f.Close()
	// Teleport back: a already has an older copy -> fast-forward.
	o := baseOptions()
	o.Target = "alice@laptop.example"
	b.refreshProbe(t)
	p, j := teleport(t, o, b, a)
	if j.Outcome != "success" {
		t.Fatalf("teleport back: %q", j.Outcome)
	}
	if !strings.Contains(readTranscript(t, a, cwd), "more work") {
		t.Error("source transcript was not fast-forwarded")
	}
	_ = p
}
```

- [ ] **Step 2: Write the killed-runner test (real detached runner, no tmux, LocalDest)**

```go
// internal/orchestrate/e2e_runner_test.go
package orchestrate

import (
	"context"
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

func procState(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "gone"
	}
	f := strings.Fields(b[strings.LastIndexByte(string(b), ')')+1:])
	return f[0]
}

func TestRunnerKilledMidTransferThawsAndContinues(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	cwd := filepath.Join(src.paths.Home, "proj")
	seedSession(t, src, cwd)
	// A large untracked file makes the transfer take long enough to kill.
	big := make([]byte, 64<<20)
	os.WriteFile(filepath.Join(cwd, "big.bin"), big, 0o644)
	// A running (not-in-tmux) source Claude.
	claude := exec.Command("claude", "--resume", sid)
	claude.Dir = cwd
	claude.Env = append(os.Environ(), "HOME="+src.paths.Home, "CLAUDE_CONFIG_DIR="+src.paths.ConfigDir)
	claude.Stdin, _ = os.Open(os.DevNull)
	if err := claude.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { claude.Process.Kill(); claude.Wait() })
	waitRegistry(t, src, "idle")

	o := baseOptions()
	o.State = "idle"
	o.LocalDest = &dst.paths
	p, err := Preflight(context.Background(), o, src.ep, dst.ep, sid)
	if err != nil {
		t.Fatal(err)
	}
	j, _ := job.New(src.paths.DataDir, sid)
	j.Plan, _ = p.ToJSON()
	j.Save()
	env := append(os.Environ(), "HOME="+src.paths.Home, "CLAUDE_CONFIG_DIR="+src.paths.ConfigDir, "XDG_DATA_HOME="+filepath.Join(src.paths.Home, ".local", "share"))
	pid, err := procx.SpawnDetached([]string{selfExe(t), "internal-runner", j.Dir}, "/", j.LogPath(), env)
	if err != nil {
		t.Fatal(err)
	}
	// Wait for step transfer to start, assert the source is stopped, kill the runner.
	deadline := time.Now().Add(30 * time.Second)
	for {
		jj, _, _ := job.Open(src.paths.DataDir, sid)
		if jj != nil && jj.Step("transfer").Status == job.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runner never reached transfer; log:\n%s", strings.Join(tailLines(t, j.LogPath()), "\n"))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st := procState(claude.Process.Pid); st != "T" {
		t.Errorf("source claude state during transfer = %s, want T (stopped)", st)
	}
	syscall.Kill(pid, syscall.SIGKILL)
	deadline = time.Now().Add(5 * time.Second)
	for procState(claude.Process.Pid) == "T" {
		if time.Now().After(deadline) {
			t.Fatal("source claude left SIGSTOPped after the runner was killed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// continue: a fresh runner finishes the job.
	pid2, err := procx.SpawnDetached([]string{selfExe(t), "internal-runner", j.Dir}, "/", j.LogPath(), env)
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Minute)
	for {
		jj, _, _ := job.Open(src.paths.DataDir, sid)
		if jj != nil && jj.Finished {
			if jj.Outcome != "success" {
				t.Fatalf("continue outcome %q; log:\n%s", jj.Outcome, strings.Join(tailLines(t, j.LogPath()), "\n"))
			}
			break
		}
		if procState(pid2) == "gone" {
			t.Fatalf("second runner died; log:\n%s", strings.Join(tailLines(t, j.LogPath()), "\n"))
		}
		if time.Now().After(deadline) {
			t.Fatal("continue did not finish")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dst.paths.Home, "proj", "big.bin")); err != nil {
		t.Error("big.bin not on the destination")
	}
	if _, ok, _ := src.ep.ClaudeStatus(context.Background(), session.ID(sid)); ok {
		t.Error("source claude should have been SIGTERMed (no tmux) after the teleport")
	}
}

func tailLines(t *testing.T, path string) []string {
	lines, _ := job.TailLog(path, 40)
	return lines
}
```

(`src.ep` has no pane probe because the host was created with `nil` tmux; `ResolveSession` finds the running Claude through the registry alone, which is the not-in-tmux path under test.)

- [ ] **Step 3: Write the live-tmux variant**

```go
//go:build tmuxlive

// internal/orchestrate/e2e_live_test.go
package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/remote"
	"github.com/mithro/go-claude-teleport/internal/tmuxx"
)

// Both hosts share one real tmux server (the pane shells inherit the
// server's environment, so the server is started with the DESTINATION's
// HOME/CLAUDE_CONFIG_DIR and the source pane exports its own).
func TestLiveTeleportRunning(t *testing.T) {
	src := newHost(t, "laptop.example", "alice", nil)
	dst := newHost(t, "big-storage.example", "bob", nil)
	t.Setenv("HOME", dst.paths.Home)
	t.Setenv("CLAUDE_CONFIG_DIR", dst.paths.ConfigDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dst.paths.Home, ".local", "share"))
	sock, dir := tmuxx.StartTestServer(t)
	for _, h := range []*host{src, dst} {
		h.opts.Tmux, h.opts.TmuxSocketDir = tmuxx.DialControl, dir
		h.ep = remote.NewLocal(h.paths, selfExe(t), h.opts)
	}
	cwd := filepath.Join(src.paths.Home, "proj")
	seedSession(t, src, cwd)
	ctx := context.Background()
	tr, err := tmuxx.DialControl(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ref, err := tmuxx.OpenWindow(ctx, tr, &tmuxx.Plan{SocketPath: sock, Group: "work", WindowName: "claude", AutoRename: false, Cwd: cwd, CreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	export := "export HOME=" + src.paths.Home + " CLAUDE_CONFIG_DIR=" + src.paths.ConfigDir + " XDG_DATA_HOME=" + filepath.Join(src.paths.Home, ".local", "share")
	tr.Run(ctx, `send-keys -t `+tmuxx.Quote(ref.PaneID)+` `+tmuxx.Quote(" "+export)+` Enter`)
	tmuxx.TypeCommand(ctx, tr, ref.PaneID, []string{"claude", "--resume", sid})
	waitRegistry(t, src, "idle")
	procs := mustProcs(t)
	o := src.opts
	o.Probe = tmuxx.Prober(ctx, tr, procs, sock)
	src.ep = remote.NewLocal(src.paths, selfExe(t), o)

	p, j := teleport(t, baseOptions(), src, dst)
	if j.Outcome != "success" {
		t.Fatalf("outcome %q", j.Outcome)
	}
	reg := waitRegistry(t, dst, "idle")
	facts, err := tmuxx.Describe(ctx, tr, p.DestRef.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	if facts.SessionName != "work" && facts.Group != "work" || facts.WindowName != "claude" || facts.AutoRename {
		t.Errorf("dest window facts = %+v", facts)
	}
	if reg.Tmux != p.DestRef.Session+":"+p.DestRef.WindowID+"."+p.DestRef.PaneID {
		t.Errorf("dest registry tmux = %q, ref %+v", reg.Tmux, p.DestRef)
	}
	out, _ := tmuxx.Capture(ctx, tr, ref.PaneID)
	if !strings.Contains(string(out), "placeholder --resume "+sid) {
		t.Errorf("source pane should show the typed placeholder:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(job.Dir(dst.paths.DataDir, sid), "capture.txt")); err != nil {
		t.Error("capture.txt not transferred to the destination job dir")
	}
}

func mustProcs(t *testing.T) *procx.Table {
	t.Helper()
	procs, err := procx.Scan("/proc")
	if err != nil {
		t.Fatal(err)
	}
	return procs
}
```

- [ ] **Step 4: Run everything**

Run: `go test -race ./internal/orchestrate/ -v -timeout 10m && go test -race -tags tmuxlive ./internal/orchestrate/ -run TestLive -v -timeout 10m`
Expected: PASS. Timing notes: fakeclaude must reach `idle` within 15 s (`waitRegistry`); the killed-runner test's 64 MiB file is hashed twice (preflight + capture rebuild — no capture here, so once) and streamed once.

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrate
git commit -m "test(orchestrate): end-to-end teleports with a fake tmux, a live tmux and a killed runner"
```

---

### Task 23: abandon, inspect, list --host, doctor

**Files:**
- Create: `internal/cli/list_host.go`
- Modify: `internal/cli/abandon.go` (Plan 02 Task 21: replace `abandonCmd()` with `newAbandonCmd(a)` below, which adds the destination side), `internal/cli/inspect.go` (Plan 01 Task 20: replace `(*app).inspectCmd()` with `newInspectCmd(a)`, which subsumes it and Plan 02's `addInspectHost` — delete `addInspectHost` from `remotecfg.go` and its call in `AddTransportCommands`), `internal/cli/doctor.go` (Plan 01 Task 21: replace `(*app).doctorCmd()` with `newDoctorCmd(a)`; keep `localChecks`/`claudeVersionFn`), `internal/cli/list.go` (Plan 01: add `--host`, call `a.listRemote`)
- Modify: `internal/cli/root.go` (register `newAbandonCmd(a)`, `newInspectCmd(a)`, `newDoctorCmd(a)` in place of `a.inspectCmd()`/`a.doctorCmd()`; remove `root.AddCommand(abandonCmd())` from Plan 02's `AddTransportCommands`)
- Test: `internal/cli/abandon_test.go`, `internal/cli/inspect_test.go`

**Interfaces:**
- Consumes: `job.Open`, `transfer.Load`, `orchestrate.Preflight/Render`, `remote.Endpoint.ListSessions/Hello`, `tmuxx.ListServers`, `claudecfg.Report.JSON`.
- Produces: the four commands.

Behaviour:
- `abandon <sid> [--delete-destination-files]`: mark the journal `abandoned` (refuse if the runner is alive: "stop it first"), remove the destination's staging dir (`Cleanup`), and with the flag delete on the destination **only** paths listed in `manifest.json` whose status was `absent` at preflight and that now hold exactly the manifest hash (never anything that pre-existed or changed since) — implemented as a new remote op `delete-installed` (`Endpoint.DeleteInstalled(ctx, m *transfer.Manifest, ids []int) (deleted []string, err error)`, addition; `Local` verifies the SHA-256 before each `os.Remove` and removes now-empty parent directories it created under the config dir only).
- `inspect [<session>]` (`--json`): resolve locally, print what a teleport would move (`session.InventoryFiles` summary by category and total size, git `Inspect`, tmux facts if any) and, with `--host HOST`, run `Preflight` against it with `--dry-run` semantics and `Render` the plan plus the drift table; exit 3 if refused.
- `list --host HOST`: `ListSessions` on the remote; same table as the local `list` (Plan 01) with an extra host column; `--json`.
- `doctor [<host>]`: local checks (claude on PATH + version, tmux servers via `ListServers`, config dir readable, data dir writable, `$SSH_AUTH_SOCK` present); with a host: dial, `Hello`, print version parity, protocol, tmux/claude presence, and try `ListSessions`. Exit 4 if unreachable, 1 if any check fails.

- [ ] **Step 1: Write the failing tests**

```go
// internal/cli/abandon_test.go
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mithro/go-claude-teleport/internal/job"
)

func TestAbandonMarksJournal(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, ".local", "share", "claude-teleport")
	const sid = "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"
	j, err := job.New(dataDir, sid)
	if err != nil {
		t.Fatal(err)
	}
	j.Outcome = "failed"
	j.Plan = []byte(`{"options":{"direction":"to","target":"bob@big-storage.example","local_dest":{"Home":"` + home + `/bob","ConfigDir":"` + home + `/bob/.claude","GlobalJSON":"` + home + `/bob/.claude.json","DataDir":"` + home + `/bob/.local/share/claude-teleport"}},"job_id":"` + sid + `"}`)
	j.Save()
	os.MkdirAll(filepath.Join(home, "bob"), 0o700)
	var out, errb bytes.Buffer
	code := Main([]string{"abandon", sid}, strings.NewReader(""), &out, &errb, []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")})
	if code != ExitOK {
		t.Fatalf("abandon = %d: %s", code, errb.String())
	}
	jj, _, _ := job.Open(dataDir, sid)
	if jj.Outcome != "abandoned" || !jj.Finished {
		t.Errorf("journal = %+v", jj)
	}
	if code := Main([]string{"continue", sid}, strings.NewReader(""), &out, &errb, []string{"HOME=" + home}); code != ExitUsage {
		t.Errorf("continue after abandon = %d, want usage error", code)
	}
}
```

```go
// internal/cli/inspect_test.go
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectLocalIdleSession(t *testing.T) {
	home := t.TempDir()
	const sid = "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c"
	cwd := filepath.Join(home, "proj")
	os.MkdirAll(cwd, 0o755)
	cfg := filepath.Join(home, ".claude")
	proj := filepath.Join(cfg, "projects", "-"+strings.ReplaceAll(strings.ReplaceAll(strings.TrimPrefix(cwd, "/"), "/", "-"), ".", "-"))
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(`{"type":"user","sessionId":"`+sid+`","cwd":"`+cwd+`","gitBranch":"","version":"2.1.247","timestamp":"2026-08-27T10:00:00Z","message":{"role":"user","content":"hi"}}`+"\n"), 0o600)
	var out, errb bytes.Buffer
	code := Main([]string{"inspect", sid}, strings.NewReader(""), &out, &errb, []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")})
	if code != ExitOK {
		t.Fatalf("inspect = %d: %s", code, errb.String())
	}
	for _, want := range []string{"3f2a9c1e", cwd, "idle", "not a git repository", "session files"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("inspect output lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if code := Main([]string{"inspect", sid, "--json"}, strings.NewReader(""), &out, &errb, []string{"HOME=" + home}); code != ExitOK || !strings.HasPrefix(strings.TrimSpace(out.String()), "{") {
		t.Errorf("--json = %d %q", code, out.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestAbandon|TestInspect' -v`
Expected: FAIL — Plan 02's local-only `abandon` does not touch the destination and Plan 01's `inspect` has no plan/drift output (`undefined: newAbandonCmd`, `undefined: newInspectCmd`).

- [ ] **Step 3: Implement**

```go
// internal/cli/abandon.go
package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/job"
	"github.com/mithro/go-claude-teleport/internal/orchestrate"
	"github.com/mithro/go-claude-teleport/internal/procx"
	"github.com/mithro/go-claude-teleport/internal/session"
	"github.com/mithro/go-claude-teleport/internal/transfer"
)

func newAbandonCmd(a *app) *cobra.Command {
	var deleteFiles bool
	cmd := &cobra.Command{
		Use:   "abandon <sid>",
		Short: "give up on an interrupted teleport; optionally delete what it installed on the destination",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			id, err := session.ParseID(args[0])
			if err != nil {
				return usageErr(err)
			}
			j, ok, err := job.Open(a.paths.DataDir, string(id))
			if err != nil {
				return err
			}
			if !ok {
				return usageErr(fmt.Errorf("no job for session %s", id.Short()))
			}
			alive := func(pid int) bool {
				t, err := procx.Scan("/proc")
				if err != nil {
					return false
				}
				p, ok := t.Get(pid)
				return ok && strings.Contains(strings.Join(p.Cmdline, " "), "internal-runner")
			}
			if j.RunnerAlive(alive) {
				return fmt.Errorf("runner %d is still working on job %s; stop it first (kill %d) or let it finish", j.RunnerPID, id.Short(), j.RunnerPID)
			}
			p, err := orchestrate.PlanFromJournal(j)
			if err != nil {
				return err
			}
			src, dst, closeFn, err := a.endpoints(ctx, p.Options)
			if err != nil {
				return exitErr(a.fail(err))
			}
			defer closeFn()
			if err := dst.Cleanup(ctx, j.ID); err != nil {
				return err
			}
			fmt.Fprintf(a.stdout, "removed staging for %s on %s\n", id.Short(), p.DestInfo.Hostname)
			if deleteFiles {
				m, err := transfer.Load(p.ManifestPath)
				if err != nil {
					return fmt.Errorf("manifest: %w", err)
				}
				var ids []int
				for _, e := range m.Entries {
					if p.Statuses[e.ID] == transfer.Absent {
						ids = append(ids, e.ID)
					}
				}
				deleted, err := dst.DeleteInstalled(ctx, m, ids)
				for _, d := range deleted {
					fmt.Fprintf(a.stdout, "deleted %s\n", d)
				}
				if err != nil {
					return err
				}
				fmt.Fprintf(a.stdout, "deleted %d file(s) that this job installed and that were unchanged since\n", len(deleted))
			}
			j.Outcome, j.Finished, j.UpdatedAt = "abandoned", true, time.Now()
			if err := j.Save(); err != nil {
				return err
			}
			_ = src.JournalPut(ctx, j)
			_ = dst.JournalPut(ctx, j)
			_ = src.Record(ctx, j.ID, job.HistoryRecord{At: time.Now().UTC(), SessionID: j.ID, Direction: j.Direction, From: j.SourceHost, To: j.DestHost, Outcome: "abandoned"})
			fmt.Fprintf(a.stdout, "job %s abandoned; the source session is untouched\n", id.Short())
			return nil
		},
	}
	cmd.Flags().BoolVar(&deleteFiles, "delete-destination-files", false, "delete the files this job installed on the destination (only those absent before and unchanged since)")
	return cmd
}
```

`Endpoint.DeleteInstalled` — add to `internal/remote/endpoint.go`, `Local` (`local_transfer.go`), the op table (`"delete-installed"`, args `{Manifest *transfer.Manifest; IDs []int}`, result `{Deleted []string}`) and `Client`, following Task 16's pattern exactly. The Local body:

```go
// DeleteInstalled removes manifest entries by id when the file at Dst
// still has the manifest's SHA-256 (or is a symlink/dir the job created
// and is now empty). Anything else is left alone and reported.
func (l *Local) DeleteInstalled(ctx context.Context, m *transfer.Manifest, ids []int) ([]string, error) {
	var deleted []string
	var dirs []string
	for _, id := range ids {
		e, ok := m.ByID(id)
		if !ok {
			continue
		}
		st, err := os.Lstat(e.Dst)
		if err != nil {
			continue
		}
		switch {
		case st.IsDir():
			dirs = append(dirs, e.Dst)
		case e.Symlink != "":
			if target, err := os.Readlink(e.Dst); err == nil && target == e.Symlink {
				if err := os.Remove(e.Dst); err == nil {
					deleted = append(deleted, e.Dst)
				}
			}
		default:
			sum, err := sha256File(e.Dst)
			if err != nil || sum != e.SHA256 {
				l.opts.Logf("delete-installed: %s changed since install; kept", e.Dst)
				continue
			}
			if err := os.Remove(e.Dst); err != nil {
				return deleted, err
			}
			deleted = append(deleted, e.Dst)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs))) // deepest first
	for _, d := range dirs {
		if err := os.Remove(d); err == nil { // only succeeds when empty
			deleted = append(deleted, d)
		}
	}
	return deleted, nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
```

(imports `crypto/sha256`, `encoding/hex`, `io` in `local_transfer.go`.)

```go
// internal/cli/inspect.go
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/gitx"
	"github.com/mithro/go-claude-teleport/internal/orchestrate"
	"github.com/mithro/go-claude-teleport/internal/session"
)

type inspectReport struct {
	Session   *session.Session   `json:"session"`
	Files     map[string]int     `json:"files_by_category"`
	Bytes     int64              `json:"bytes"`
	Skipped   []session.Skipped  `json:"skipped"`
	Git       *gitx.Info         `json:"git,omitempty"`
	GitError  string             `json:"git_error,omitempty"`
	Plan      *orchestrate.Plan  `json:"plan,omitempty"`
	Refused   string             `json:"refused,omitempty"`
}

func newInspectCmd(a *app) *cobra.Command {
	var host string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "inspect [<session>]",
		Short: "show everything a teleport would move, and the drift report against --host",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sel, err := session.ParseSelector(args, session.Env{SessionID: a.env["CLAUDE_CODE_SESSION_ID"], PID: a.env["CLAUDE_PID"], TmuxPane: a.env["TMUX_PANE"], Tmux: a.env["TMUX"]})
			if err != nil {
				return usageErr(err)
			}
			local := a.localEndpoint(ctx, a.paths)
			sess, err := local.ResolveSession(ctx, sel)
			if err != nil {
				return exitErr(a.fail(err))
			}
			rep := &inspectReport{Session: sess, Files: map[string]int{}}
			inv, _, err := local.InventorySession(ctx, sess.ID)
			if err != nil {
				return err
			}
			for _, f := range append(inv.Files, inv.Memory...) {
				rep.Files[string(f.Category)]++
				rep.Bytes += f.Size
			}
			rep.Skipped = inv.Skipped
			if gi, err := local.InventoryGit(ctx, sess.LaunchCwd); err == nil {
				rep.Git = gi
			} else {
				rep.GitError = err.Error()
			}
			code := ExitOK
			if host != "" {
				o := orchestrate.Options{Direction: "to", Target: host, Selector: session.Selector{ID: sess.ID}, State: "auto", ExitTimeout: 30 * time.Second, StartTimeout: 90 * time.Second}
				src, dst, closeFn, err := a.endpoints(ctx, o)
				if err != nil {
					return exitErr(a.fail(err))
				}
				defer closeFn()
				plan, err := orchestrate.Preflight(ctx, o, src, dst, string(sess.ID))
				var re *orchestrate.RefusedError
				switch {
				case errors.As(err, &re):
					rep.Refused, code = re.Reason, ExitRefused
				case err != nil:
					return exitErr(a.fail(err))
				default:
					rep.Plan = plan
				}
			}
			if asJSON {
				b, err := json.MarshalIndent(rep, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(a.stdout, string(b))
				return exitErr(code)
			}
			fmt.Fprintf(a.stdout, "Session %s (%s)\n  cwd     %s\n  branch  %s\n  claude  %s\n", sess.ID.Short(), sess.State, sess.LaunchCwd, sess.Branch, sess.Version)
			if sess.Tmux != nil {
				fmt.Fprintf(a.stdout, "  tmux    %s:%s.%s on %s\n", sess.Tmux.Session, sess.Tmux.WindowID, sess.Tmux.PaneID, sess.Tmux.SocketPath)
			}
			fmt.Fprintf(a.stdout, "Files   %d session files, %d memory files, %d bytes; %d skipped\n", rep.Files[string(session.CatSession)], len(inv.Memory), rep.Bytes, len(rep.Skipped))
			for _, s := range rep.Skipped {
				fmt.Fprintf(a.stdout, "  skipped %s: %s\n", s.Path, s.Reason)
			}
			switch {
			case rep.Git != nil:
				g := rep.Git
				kind := "main checkout"
				if g.IsLinked {
					kind = "linked worktree " + g.WorktreeName
				}
				fmt.Fprintf(a.stdout, "Git     %s of %s, branch %s at %s\n", kind, g.MainDir, g.Branch, g.Head[:7])
				fmt.Fprintf(a.stdout, "  dirty %d staged, %d modified, %d untracked, %d deleted; %d other worktree(s)\n", len(g.Dirty.Staged), len(g.Dirty.Modified), len(g.Dirty.Untracked), len(g.Dirty.Deleted), len(g.OtherWorktrees))
			default:
				fmt.Fprintf(a.stdout, "Git     not a git repository (%s is copied as plain files)\n", sess.LaunchCwd)
			}
			if rep.Plan != nil {
				fmt.Fprintf(a.stdout, "\nPlan against %s\n", host)
				rep.Plan.Render(a.stdout)
			}
			if rep.Refused != "" {
				fmt.Fprintf(a.stdout, "\nTeleport to %s would be refused: %s\n", host, rep.Refused)
			}
			return exitErr(code)
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "also run preflight against HOST and show the plan and drift")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}
```

```go
// internal/cli/list_host.go
package cli

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// listRemote is called by Plan 01's `list` command when --host is set
// (add `--host HOST` to that command and call this when it is non-empty).
func (a *app) listRemote(cmd *cobra.Command, host string, asJSON bool) error {
	ctx := cmd.Context()
	ep, closeFn, err := a.dialRemote(ctx, host, nil, nil)
	if err != nil {
		return exitErr(a.fail(err))
	}
	defer closeFn()
	rows, err := ep.ListSessions(ctx)
	if err != nil {
		return err
	}
	if asJSON {
		b, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(a.stdout, string(b))
		return nil
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tSESSION\tSTATE\tCWD\tBRANCH\tTMUX\tLAST")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", host, r.ID.Short(), r.State, r.Cwd, r.Branch, r.Tmux, r.LastTS)
	}
	return tw.Flush()
}
```

```go
// internal/cli/doctor.go
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mithro/go-claude-teleport/internal/tmuxx"
	"github.com/mithro/go-claude-teleport/internal/version"
)

func newDoctorCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor [<host>]",
		Short: "check local (and remote) prerequisites",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			failed := 0
			check := func(ok bool, format string, v ...any) {
				mark := "ok  "
				if !ok {
					mark = "FAIL"
					failed++
				}
				fmt.Fprintf(a.stdout, "%s %s\n", mark, fmt.Sprintf(format, v...))
			}
			fmt.Fprintf(a.stdout, "local: claude-teleport %s (protocol %d)\n", version.Version, version.Protocol)
			if p, err := exec.LookPath("claude"); err == nil {
				out, verr := exec.Command(p, "--version").Output()
				check(verr == nil, "claude at %s: %s", p, strings.TrimSpace(string(out)))
			} else {
				check(false, "claude not on PATH")
			}
			servers, err := tmuxx.ListServers(tmuxSocketDir(a.env))
			check(err == nil, "tmux servers under %s: %d (%s)", tmuxSocketDir(a.env), len(servers), strings.Join(servers, ", "))
			_, err = os.ReadDir(a.paths.ConfigDir)
			check(err == nil, "config dir %s readable", a.paths.ConfigDir)
			err = os.MkdirAll(a.paths.DataDir, 0o700)
			check(err == nil, "data dir %s writable", a.paths.DataDir)
			check(a.env["SSH_AUTH_SOCK"] != "", "SSH_AUTH_SOCK set (agent keys available)")
			if len(args) == 1 {
				ep, closeFn, err := a.dialRemote(cmd.Context(), args[0], nil, nil)
				if err != nil {
					check(false, "ssh %s: %v", args[0], err)
					return exitErr(ExitUnreachable)
				}
				defer closeFn()
				hi, err := ep.Hello(cmd.Context())
				if err != nil {
					check(false, "hello %s: %v", args[0], err)
					return exitErr(ExitUnreachable)
				}
				check(hi.Version == version.Version, "remote claude-teleport %s (local %s)", hi.Version, version.Version)
				check(hi.HasClaude, "remote claude: %s", hi.ClaudeVersion)
				check(true, "remote tmux present: %v; claude-resume present: %v; home %s; config %s", hi.HasTmux, hi.HasClaudeResume, hi.Home, hi.ConfigDir)
				rows, err := ep.ListSessions(cmd.Context())
				check(err == nil, "remote sessions listable: %d", len(rows))
			}
			if failed > 0 {
				return exitErr(ExitFailed)
			}
			return nil
		},
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `go vet ./... && go test -race ./internal/cli/ ./internal/remote/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli internal/remote
git commit -m "feat(cli): abandon with manifest-bounded deletion, inspect, list --host, doctor"
```

---

### Task 24: docker integration harness — image, compose, keys

**Files:**
- Create: `test/integration/Dockerfile`, `test/integration/docker-compose.yml`, `test/integration/sshd_config`, `test/integration/entrypoint.sh`, `test/integration/build.sh`
- Create: `test/integration/README.md` (how to run locally; short)

No Go code in this task; its test is `docker compose config` and a manual `ssh` hop (Task 25 automates it).

- [ ] **Step 1: Write the image**

```dockerfile
# test/integration/Dockerfile
FROM debian:trixie-slim

ARG CLAUDE_VERSION=""
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends openssh-server tmux git ca-certificates curl procps \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /run/sshd \
 && useradd --create-home --shell /bin/bash alice \
 && useradd --create-home --shell /bin/bash bob \
 && for u in alice bob; do mkdir -p /home/$u/.ssh && chmod 700 /home/$u/.ssh && chown -R $u:$u /home/$u; done

COPY sshd_config /etc/ssh/sshd_config
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 755 /usr/local/bin/entrypoint.sh

# Layer 2 only: the real Claude Code at a pinned version or "latest", via
# the native installer, as alice. Layer 1 images leave CLAUDE_VERSION empty
# and get fakeclaude bind-mounted as /usr/local/bin/claude instead.
USER alice
RUN if [ -n "$CLAUDE_VERSION" ]; then \
      curl -fsSL https://claude.ai/install.sh | bash -s "$CLAUDE_VERSION" \
      && ln -s /home/alice/.local/bin/claude /home/alice/claude-real; \
    fi
USER root

EXPOSE 22
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
```

```
# test/integration/sshd_config
Port 22
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
PermitRootLogin no
AllowTcpForwarding yes
AcceptEnv CLAUDE_CONFIG_DIR ANTHROPIC_BASE_URL ANTHROPIC_API_KEY
UsePAM no
Subsystem sftp /usr/lib/openssh/sftp-server
```

```sh
#!/bin/sh
# test/integration/entrypoint.sh — install the per-run key, start sshd.
set -eu
# alice is the main user on every host; bob exists on every host too so
# the "different $HOME" scenario can teleport alice@source -> bob@dest.
for u in alice bob; do
  if [ -f /run/keys/id_ed25519.pub ]; then
    cp /run/keys/id_ed25519.pub /home/$u/.ssh/authorized_keys
    cp /run/keys/id_ed25519 /home/$u/.ssh/id_ed25519
    chmod 600 /home/$u/.ssh/authorized_keys /home/$u/.ssh/id_ed25519
    chown $u:$u /home/$u/.ssh/authorized_keys /home/$u/.ssh/id_ed25519
  fi
done
ssh-keygen -A >/dev/null
# Real Claude (layer 2) lives in alice's ~/.local/bin; put it on PATH for sshd sessions.
if [ -x /home/alice/.local/bin/claude ]; then
  echo 'PATH=/home/alice/.local/bin:/usr/local/bin:/usr/bin:/bin' > /home/alice/.ssh/environment
  sed -i 's/^#\?PermitUserEnvironment.*/PermitUserEnvironment yes/' /etc/ssh/sshd_config || true
  grep -q PermitUserEnvironment /etc/ssh/sshd_config || echo 'PermitUserEnvironment yes' >> /etc/ssh/sshd_config
fi
exec /usr/sbin/sshd -D -e
```

```yaml
# test/integration/docker-compose.yml
# Three hosts: source and jump on `public`; jump and dest on `private`.
# `dest` resolves only on `private`, so source must go --via jump.
x-common: &common
  build:
    context: .
    args:
      CLAUDE_VERSION: ${CLAUDE_VERSION:-}
  image: claude-teleport-it:${CLAUDE_VERSION:-fake}
  volumes:
    - ./keys:/run/keys:ro
    - ../../dist/claude-teleport:/usr/local/bin/claude-teleport:ro
  environment:
    CLAUDE_CONFIG_DIR: /home/alice/.claude
    DISABLE_AUTOUPDATER: "1"
    DISABLE_TELEMETRY: "1"
    DISABLE_ERROR_REPORTING: "1"
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1"
    ANTHROPIC_BASE_URL: http://api:8080
    ANTHROPIC_API_KEY: dummy-key
  init: true

services:
  source:
    <<: *common
    hostname: source
    networks: [public]
    volumes:
      - ./keys:/run/keys:ro
      - ../../dist/claude-teleport:/usr/local/bin/claude-teleport:ro
      - ../../dist/fakeclaude:/usr/local/bin/claude:ro
  jump:
    <<: *common
    hostname: jump
    networks: [public, private]
  dest:
    <<: *common
    hostname: dest
    networks: [private]
    volumes:
      - ./keys:/run/keys:ro
      - ../../dist/claude-teleport:/usr/local/bin/claude-teleport:ro
      - ../../dist/fakeclaude:/usr/local/bin/claude:ro
  api:
    profiles: [realclaude]
    image: claude-teleport-fakeapi:local
    build:
      context: ../..
      dockerfile: test/integration/Dockerfile.fakeapi
    hostname: api
    networks: [public, private]
    command: ["/fakeapi-server", "-addr", ":8080", "-log", "/log"]
    volumes:
      - ./api-log:/log

networks:
  public: {}
  private:
    internal: true
```

For layer 2 the `source`/`dest` services must NOT mount fakeclaude over the real `claude`; the `realclaude` compose override handles that:

```yaml
# test/integration/docker-compose.realclaude.yml
services:
  source:
    volumes:
      - ./keys:/run/keys:ro
      - ../../dist/claude-teleport:/usr/local/bin/claude-teleport:ro
  dest:
    volumes:
      - ./keys:/run/keys:ro
      - ../../dist/claude-teleport:/usr/local/bin/claude-teleport:ro
```

```dockerfile
# test/integration/Dockerfile.fakeapi
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /fakeapi-server ./test/fakeapi-server
FROM debian:trixie-slim
COPY --from=build /fakeapi-server /fakeapi-server
EXPOSE 8080
```

```sh
#!/bin/sh
# test/integration/build.sh — build the binaries for the container arch,
# generate the per-run key, and (re)build the images.
# Usage: test/integration/build.sh [CLAUDE_VERSION]
set -eu
cd "$(dirname "$0")/../.."
arch=$(docker info --format '{{.Architecture}}' | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
mkdir -p dist test/integration/keys test/integration/api-log
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -ldflags "-X github.com/mithro/go-claude-teleport/internal/version.Version=it-$(git rev-parse --short HEAD)" -o dist/claude-teleport ./cmd/claude-teleport
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o dist/fakeclaude ./test/fakeclaude
rm -f test/integration/keys/id_ed25519 test/integration/keys/id_ed25519.pub
ssh-keygen -q -t ed25519 -N '' -C claude-teleport-it -f test/integration/keys/id_ed25519
chmod 644 test/integration/keys/id_ed25519   # the container copies it into alice's ~/.ssh with 600
export CLAUDE_VERSION="${1:-}"
docker compose -f test/integration/docker-compose.yml build
```

`test/integration/README.md`:

```markdown
# Integration harness

Three containers — `source`, `jump`, `dest` — with sshd, tmux and git.
`dest` is only reachable through `jump`.

    test/integration/build.sh                # fakeclaude image (layer 1)
    go test -tags integration ./test/integration/ -v -timeout 30m

    test/integration/build.sh 2.1.247        # real Claude Code (layer 2)
    CLAUDE_VERSION=2.1.247 go test -tags 'integration realclaude' ./test/integration/ -run TestReal -v -timeout 30m

Everything under `keys/` and `api-log/` is generated per run and git-ignored.
```

Add `test/integration/keys/`, `test/integration/api-log/`, `dist/` to `.gitignore`.

- [ ] **Step 2: Validate**

Run: `cd test/integration && docker compose config >/dev/null && docker compose --profile realclaude -f docker-compose.yml -f docker-compose.realclaude.yml config >/dev/null && echo ok`
Expected: `ok`. Then `test/integration/build.sh && docker compose -f test/integration/docker-compose.yml up -d && docker compose -f test/integration/docker-compose.yml exec source ssh -o StrictHostKeyChecking=accept-new -J alice@jump alice@dest claude-teleport version` prints the `it-<sha>` version, and `docker compose exec source getent hosts dest` prints nothing (dest is not resolvable from source). `docker compose down`.

- [ ] **Step 3: Commit**

```bash
git add test/integration .gitignore
git commit -m "test(integration): docker compose harness with source, jump and dest"
```

---

### Task 25: integration layer 1 — fakeclaude scenarios through the jump host

**Files:**
- Create: `test/integration/harness_test.go`, `test/integration/integration_test.go` (both build tag `integration`)

The Go test drives `docker compose` (tests may exec `docker`), runs commands inside the containers with `docker compose exec -T -u <user> <svc> …`, and asserts on files, the registry and tmux inside `dest`.

- [ ] **Step 1: Write the harness**

```go
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

var composeFiles = []string{"-f", "docker-compose.yml"}

func compose(t testing.TB, args ...string) string {
	t.Helper()
	cmd := exec.Command("docker", append(append([]string{"compose"}, composeFiles...), args...)...)
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// execAs runs argv inside svc as user (no shell); returns combined output
// and the exit code.
func execAs(t testing.TB, svc, user string, argv ...string) (string, int) {
	t.Helper()
	args := append([]string{"compose"}, composeFiles...)
	args = append(args, "exec", "-T", "-u", user, svc)
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
		t.Fatalf("[%s] %s\nexit %d:\n%s", svc, script, code, out)
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
	cmd := exec.Command("docker", append(append([]string{"compose"}, composeFiles...), "up", "-d", "--wait")...)
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "compose up: %v\n%s", err, out)
		os.Exit(2)
	}
	code := m.Run()
	exec.Command("docker", append(append([]string{"compose"}, composeFiles...), "down", "-v", "--remove-orphans")...).Run()
	os.Exit(code)
}

const teleportOpts = "-o StrictHostKeyChecking=accept-new --via jump --start-timeout 60s"

// reset wipes Claude and claude-teleport state and every project dir on
// all hosts (for both users) and kills tmux servers.
func reset(t testing.TB) {
	t.Helper()
	for _, svc := range []string{"source", "jump", "dest"} {
		for _, u := range []string{"alice", "bob"} {
			shCode(t, svc, u, "tmux kill-server 2>/dev/null || true; rm -rf ~/.claude ~/.claude.json ~/.local/share/claude-teleport ~/proj* ~/repo*; mkdir -p ~/.claude")
		}
	}
}

func newSID(t testing.TB) string {
	t.Helper()
	b := make([]byte, 16)
	f, _ := os.Open("/dev/urandom")
	f.Read(b)
	f.Close()
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// seed creates a session transcript in cwd on svc via fakeclaude -p.
func seed(t testing.TB, svc, user, cwd, sid string) {
	t.Helper()
	sh(t, svc, user, "mkdir -p "+cwd+" && cd "+cwd+" && claude -p --session-id "+sid+" 'remember the word pineapple' >/dev/null")
}

// makeRepo creates a git repo with one commit at dir on svc.
func makeRepo(t testing.TB, svc, user, dir string) {
	t.Helper()
	sh(t, svc, user, "mkdir -p "+dir+" && cd "+dir+" && git init -q -b main && git config user.email a@laptop.example && git config user.name alice && echo hi > README.md && git add . && git commit -q -m init")
}

// startInTmux starts `claude --resume sid` in a new tmux window and waits
// for the registry to report idle. Returns "session:window" of the pane.
func startInTmux(t testing.TB, svc, user, cwd, sid, group string, extraEnv string) {
	t.Helper()
	sh(t, svc, user, "tmux -f /dev/null new-session -d -s "+group+" -n claude -c "+cwd)
	sh(t, svc, user, "tmux send-keys -t "+group+":claude \" "+extraEnv+" claude --resume "+sid+"\" Enter")
	waitRegistry(t, svc, user, sid, "idle")
}

// registry returns the registry JSON for sid on svc, or "".
func registry(t testing.TB, svc, user, sid string) string {
	t.Helper()
	out, _ := shCode(t, svc, user, "grep -l '\"sessionId\": *\""+sid+"\"' ~/.claude/sessions/*.json 2>/dev/null | head -1 | xargs -r cat")
	return strings.TrimSpace(out)
}

func waitRegistry(t testing.TB, svc, user, sid, status string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if r := registry(t, svc, user, sid); r != "" && strings.Contains(r, `"status": "`+status+`"`) || r != "" && strings.Contains(r, `"status":"`+status+`"`) {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("[%s] session %s never reached %s", svc, sid, status)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func transcriptPath(home, cwd, sid string) string {
	munged := strings.NewReplacer("/", "-", ".", "-").Replace(cwd)
	return home + "/.claude/projects/" + munged + "/" + sid + ".jsonl"
}

func teleport(t testing.TB, svc, user, args string) (string, int) {
	t.Helper()
	return shCode(t, svc, user, "claude-teleport "+args+" "+teleportOpts)
}
```

- [ ] **Step 2: Write the scenarios**

```go
//go:build integration

// test/integration/integration_test.go
package integration

import (
	"strings"
	"testing"
	"time"
)

func TestToWorktreeFreshMainRunning(t *testing.T) {
	reset(t)
	sid := newSID(t)
	makeRepo(t, "source", "alice", "/home/alice/repo")
	sh(t, "source", "alice", "cd /home/alice/repo && git worktree add -q -b feat .worktrees/feat && echo wip > .worktrees/feat/wip.txt")
	w := "/home/alice/repo/.worktrees/feat"
	seed(t, "source", "alice", w, sid)
	startInTmux(t, "source", "alice", w, sid, "work", "")

	out, code := teleport(t, "source", "alice", sid+" --to dest")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	sh(t, "dest", "alice", "test -f "+transcriptPath("/home/alice", w, sid))
	srcStatus := sh(t, "source", "alice", "cd "+w+" && git status --porcelain")
	dstStatus := sh(t, "dest", "alice", "cd "+w+" && git status --porcelain")
	if srcStatus != dstStatus {
		t.Errorf("git status differs:\nsource:\n%s\ndest:\n%s", srcStatus, dstStatus)
	}
	if !strings.Contains(sh(t, "dest", "alice", "cd /home/alice/repo && git worktree list"), w) {
		t.Error("dest git does not list the worktree")
	}
	reg := waitRegistry(t, "dest", "alice", sid, "idle")
	if !strings.Contains(reg, `"tmux": "work:`) && !strings.Contains(reg, `"tmux":"work:`) {
		t.Errorf("dest registry tmux field: %s", reg)
	}
	panes := sh(t, "dest", "alice", "tmux list-panes -a -F '#{session_name} #{window_name} #{pane_current_command}'")
	if !strings.Contains(panes, "work claude claude") {
		t.Errorf("dest panes:\n%s", panes)
	}
	if registry(t, "source", "alice", sid) != "" {
		t.Error("source claude still registered")
	}
	if pane := sh(t, "source", "alice", "tmux capture-pane -p -t work:claude"); !strings.Contains(pane, "placeholder --resume "+sid) || !strings.Contains(pane, "--teleported-to") {
		t.Errorf("source pane:\n%s", pane)
	}
	if out := sh(t, "source", "alice", "cat ~/.local/share/claude-teleport/jobs/"+sid+"/history.jsonl"); !strings.Contains(out, `"outcome":"success"`) {
		t.Errorf("history: %s", out)
	}
}

func TestFromMainCheckoutExistingMain(t *testing.T) {
	reset(t)
	sid := newSID(t)
	makeRepo(t, "source", "alice", "/home/alice/repo")
	// dest has a clone at the initial commit (bundle carried by the test).
	sh(t, "source", "alice", "cd /home/alice/repo && git bundle create /tmp/repo.bundle main")
	compose(t, "cp", "source:/tmp/repo.bundle", "/tmp/claude-teleport-it.bundle")
	compose(t, "cp", "/tmp/claude-teleport-it.bundle", "dest:/tmp/repo.bundle")
	sh(t, "dest", "alice", "git clone -q -b main /tmp/repo.bundle /home/alice/repo && cd /home/alice/repo && git remote remove origin")
	sh(t, "source", "alice", "cd /home/alice/repo && echo more > more.txt && git add . && git commit -q -m more && echo u > untracked.txt")
	tip := strings.TrimSpace(sh(t, "source", "alice", "cd /home/alice/repo && git rev-parse HEAD"))
	seed(t, "source", "alice", "/home/alice/repo", sid)
	startInTmux(t, "source", "alice", "/home/alice/repo", sid, "main", "")

	// Driver is the destination: --from source through jump.
	out, code := teleport(t, "dest", "alice", sid+" --from source")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := strings.TrimSpace(sh(t, "dest", "alice", "cd /home/alice/repo && git rev-parse HEAD")); got != tip {
		t.Errorf("dest HEAD %s want %s", got, tip)
	}
	if st := sh(t, "dest", "alice", "cd /home/alice/repo && git status --porcelain"); !strings.Contains(st, "?? untracked.txt") {
		t.Errorf("dest status:\n%s", st)
	}
	waitRegistry(t, "dest", "alice", sid, "idle")
}

func TestSuspendedAndIdleEndStates(t *testing.T) {
	reset(t)
	sid := newSID(t)
	seed(t, "source", "alice", "/home/alice/proj", sid)
	startInTmux(t, "source", "alice", "/home/alice/proj", sid, "main", "")
	out, code := teleport(t, "source", "alice", sid+" --to dest --state suspended")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	cmd := sh(t, "dest", "alice", "tmux list-panes -a -F '#{pane_current_command}'")
	if !strings.Contains(cmd, "claude-teleport") {
		t.Errorf("dest pane should run the placeholder, got %q", cmd)
	}
	if registry(t, "dest", "alice", sid) != "" {
		t.Error("suspended: dest claude must have exited")
	}

	// Teleport it back idle: the source (now suspended) is the destination of a --from.
	reset(t)
	sid = newSID(t)
	seed(t, "source", "alice", "/home/alice/proj", sid)
	startInTmux(t, "source", "alice", "/home/alice/proj", sid, "main", "")
	out, code = teleport(t, "source", "alice", sid+" --to dest --state idle")
	if code != 0 {
		t.Fatalf("idle: exit %d:\n%s", code, out)
	}
	if wins, _ := shCode(t, "dest", "alice", "tmux list-windows -a"); strings.Contains(wins, "claude") {
		t.Errorf("idle: window we created should be gone:\n%s", wins)
	}
	sh(t, "dest", "alice", "test -f "+transcriptPath("/home/alice", "/home/alice/proj", sid))
}

func TestDifferentHomeRewrite(t *testing.T) {
	reset(t)
	sid := newSID(t)
	makeRepo(t, "source", "alice", "/home/alice/repo")
	seed(t, "source", "alice", "/home/alice/repo", sid)
	startInTmux(t, "source", "alice", "/home/alice/repo", sid, "main", "")
	out, code := teleport(t, "source", "alice", sid+" --to bob@dest")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	tr := sh(t, "dest", "bob", "cat "+transcriptPath("/home/bob", "/home/bob/repo", sid))
	if strings.Contains(tr, "/home/alice") {
		t.Errorf("transcript still mentions /home/alice:\n%s", tr)
	}
	sh(t, "dest", "bob", "cd /home/bob/repo && git status --porcelain")
	reg := waitRegistry(t, "dest", "bob", sid, "idle")
	if !strings.Contains(reg, "/home/bob/repo") {
		t.Errorf("bob's registry cwd: %s", reg)
	}
}

func TestReTeleportFastForwardAndDivergence(t *testing.T) {
	reset(t)
	sid := newSID(t)
	seed(t, "source", "alice", "/home/alice/proj", sid)
	startInTmux(t, "source", "alice", "/home/alice/proj", sid, "main", "")
	if out, code := teleport(t, "source", "alice", sid+" --to dest --state idle"); code != 0 {
		t.Fatalf("first: %d\n%s", code, out)
	}
	// More work on dest, then bring it back: fast-forward.
	sh(t, "dest", "alice", "cd /home/alice/proj && claude -p --resume "+sid+" 'second thought' >/dev/null")
	sh(t, "source", "alice", "tmux kill-server || true")
	if out, code := teleport(t, "source", "alice", sid+" --from dest --state idle"); code != 0 {
		t.Fatalf("back: %d\n%s", code, out)
	}
	if tr := sh(t, "source", "alice", "cat "+transcriptPath("/home/alice", "/home/alice/proj", sid)); !strings.Contains(tr, "second thought") {
		t.Errorf("source transcript not fast-forwarded:\n%s", tr)
	}
	// Diverge both copies: refused with exit 3.
	sh(t, "source", "alice", "cd /home/alice/proj && claude -p --resume "+sid+" 'source only' >/dev/null")
	sh(t, "dest", "alice", "cd /home/alice/proj && claude -p --resume "+sid+" 'dest only' >/dev/null")
	out, code := teleport(t, "source", "alice", sid+" --to dest --state idle")
	if code != 3 || !strings.Contains(out, sid+".jsonl") {
		t.Errorf("divergence: exit %d\n%s", code, out)
	}
}

func TestDriftRefusalAndOverride(t *testing.T) {
	reset(t)
	sid := newSID(t)
	seed(t, "source", "alice", "/home/alice/proj", sid)
	sh(t, "source", "alice", `printf '{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo hi"}]}]}}' > ~/.claude/settings.json`)
	out, code := teleport(t, "source", "alice", sid+" --to dest --state idle")
	if code != 3 || !strings.Contains(out, "hooks") {
		t.Fatalf("drift: exit %d\n%s", code, out)
	}
	out, code = teleport(t, "source", "alice", sid+" --to dest --state idle --allow-config-drift")
	if code != 0 {
		t.Fatalf("override: exit %d\n%s", code, out)
	}
}

func TestBangModeStopsParentDuringTransfer(t *testing.T) {
	reset(t)
	sid := newSID(t)
	seed(t, "source", "alice", "/home/alice/proj", sid)
	sh(t, "source", "alice", "head -c 64000000 /dev/urandom > /home/alice/proj/big.bin")
	// fakeclaude runs the child exactly as Claude's `!` mode would.
	startInTmux(t, "source", "alice", "/home/alice/proj", sid, "main",
		"FAKECLAUDE_RUN_CHILD='claude-teleport --to dest "+teleportOpts+"'")
	pid := strings.TrimSpace(sh(t, "source", "alice", "grep -l '"+sid+"' ~/.claude/sessions/*.json | head -1 | xargs cat | sed -n 's/.*\"pid\": *\\([0-9]*\\).*/\\1/p'"))
	// The child starts when fakeclaude reaches the prompt; poll for the stopped parent.
	sawStopped := false
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := shCode(t, "source", "alice", "cut -d' ' -f3 /proc/"+pid+"/stat")
		if strings.TrimSpace(st) == "T" {
			sawStopped = true
		}
		if strings.Contains(st, "No such file") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !sawStopped {
		t.Error("parent fakeclaude was never observed SIGSTOPped during the transfer")
	}
	if _, code := shCode(t, "source", "alice", "test -d /proc/"+pid); code == 0 {
		t.Error("parent fakeclaude should have exited after the teleport")
	}
	waitRegistry(t, "dest", "alice", sid, "idle")
	if pane := sh(t, "source", "alice", "tmux capture-pane -p -t main:claude"); !strings.Contains(pane, "placeholder --resume "+sid) {
		t.Errorf("source pane:\n%s", pane)
	}
}

func TestKilledRunnerNeverLeavesSourceStoppedThenContinue(t *testing.T) {
	reset(t)
	sid := newSID(t)
	seed(t, "source", "alice", "/home/alice/proj", sid)
	sh(t, "source", "alice", "head -c 128000000 /dev/urandom > /home/alice/proj/big.bin")
	startInTmux(t, "source", "alice", "/home/alice/proj", sid, "main", "")
	pid := strings.TrimSpace(sh(t, "source", "alice", "grep -l '"+sid+"' ~/.claude/sessions/*.json | head -1 | xargs cat | sed -n 's/.*\"pid\": *\\([0-9]*\\).*/\\1/p'"))
	sh(t, "source", "alice", "nohup claude-teleport "+sid+" --to dest "+teleportOpts+" > /tmp/fg.log 2>&1 &")
	deadline := time.Now().Add(60 * time.Second)
	for {
		st, _ := shCode(t, "source", "alice", `grep -q '"name": *"transfer"[^}]*"status": *"running"' ~/.local/share/claude-teleport/jobs/`+sid+`/job.json && echo yes`)
		if strings.Contains(st, "yes") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("transfer never started")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if st := sh(t, "source", "alice", "cut -d' ' -f3 /proc/"+pid+"/stat"); strings.TrimSpace(st) != "T" {
		t.Errorf("source claude should be stopped during transfer, state %s", st)
	}
	sh(t, "source", "alice", "pkill -9 -f 'claude-teleport internal-runner' || true")
	deadline = time.Now().Add(5 * time.Second)
	for {
		st := strings.TrimSpace(sh(t, "source", "alice", "cut -d' ' -f3 /proc/"+pid+"/stat"))
		if st != "T" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("source claude left SIGSTOPped after the runner was killed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	out, code := shCode(t, "source", "alice", "claude-teleport continue "+sid)
	if code != 0 {
		t.Fatalf("continue: %d\n%s", code, out)
	}
	sh(t, "dest", "alice", "test -f /home/alice/proj/big.bin")
	waitRegistry(t, "dest", "alice", sid, "idle")
}

func TestDroppedSSHThenContinue(t *testing.T) {
	reset(t)
	sid := newSID(t)
	seed(t, "source", "alice", "/home/alice/proj", sid)
	sh(t, "source", "alice", "head -c 128000000 /dev/urandom > /home/alice/proj/big.bin")
	startInTmux(t, "source", "alice", "/home/alice/proj", sid, "main", "")
	sh(t, "source", "alice", "nohup claude-teleport "+sid+" --to dest "+teleportOpts+" > /tmp/fg.log 2>&1 &")
	deadline := time.Now().Add(60 * time.Second)
	for {
		st, _ := shCode(t, "source", "alice", `grep -q '"name": *"transfer"[^}]*"status": *"running"' ~/.local/share/claude-teleport/jobs/`+sid+`/job.json && echo yes`)
		if strings.Contains(st, "yes") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("transfer never started")
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Drop the hop: freezing jump kills every ssh channel through it.
	compose(t, "pause", "jump")
	time.Sleep(3 * time.Second)
	compose(t, "unpause", "jump")
	// The runner fails once the connection is declared lost; then continue.
	deadline = time.Now().Add(3 * time.Minute)
	for {
		if _, code := shCode(t, "source", "alice", "pgrep -f 'claude-teleport internal-runner' >/dev/null"); code != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runner did not stop after the network drop")
		}
		time.Sleep(500 * time.Millisecond)
	}
	out, code := shCode(t, "source", "alice", "claude-teleport continue "+sid)
	if code != 0 {
		t.Fatalf("continue: %d\n%s", code, out)
	}
	waitRegistry(t, "dest", "alice", sid, "idle")
}

func TestAbandonDeletesOnlyManifestFiles(t *testing.T) {
	reset(t)
	sid := newSID(t)
	seed(t, "source", "alice", "/home/alice/proj", sid)
	sh(t, "dest", "alice", "mkdir -p /home/alice/proj && echo keep > /home/alice/proj/preexisting.txt")
	sh(t, "source", "alice", "echo mine > /home/alice/proj/mine.txt")
	// Make start fail on the destination (logged-out fakeclaude) so the job stops after install.
	sh(t, "dest", "alice", "echo 'export FAKECLAUDE_FAIL=not-logged-in' >> ~/.bashrc")
	out, code := teleport(t, "source", "alice", sid+" --to dest --state idle")
	if code != 5 {
		t.Fatalf("expected exit 5 (not resumed), got %d\n%s", code, out)
	}
	sh(t, "dest", "alice", "test -f /home/alice/proj/mine.txt && test -f "+transcriptPath("/home/alice", "/home/alice/proj", sid))
	out, code = shCode(t, "source", "alice", "claude-teleport abandon "+sid+" --delete-destination-files -o StrictHostKeyChecking=accept-new")
	if code != 0 {
		t.Fatalf("abandon: %d\n%s", code, out)
	}
	if _, code := shCode(t, "dest", "alice", "test -f /home/alice/proj/mine.txt"); code == 0 {
		t.Error("installed file should have been deleted")
	}
	if _, code := shCode(t, "dest", "alice", "test -f "+transcriptPath("/home/alice", "/home/alice/proj", sid)); code == 0 {
		t.Error("installed transcript should have been deleted")
	}
	sh(t, "dest", "alice", "test -f /home/alice/proj/preexisting.txt")
	sh(t, "dest", "alice", "sed -i '/FAKECLAUDE_FAIL/d' ~/.bashrc")
}
```

`abandon` needs the ssh options too: `--via` and `-o` are persistent flags on the root command so every subcommand that dials accepts them (`teleportFlags.Via`/`SSHOptions` are read from the root's persistent flag set). `docker compose pause jump` is used for the dropped-ssh scenario instead of `docker network disconnect` because pausing freezes the hop's sshd without altering DNS, which is exactly a lost connection from the tool's point of view; the ssh layer's bounded re-dial (Plan 02) fails, the runner marks `transfer` failed and thaws, and `continue` resends only what staging lacks.

- [ ] **Step 3: Run**

Run: `test/integration/build.sh && go test -tags integration ./test/integration/ -v -timeout 40m`
Expected: PASS for all nine tests (≈10–15 minutes). If `TestBangModeStopsParentDuringTransfer` never sees `T`, the transfer finished too fast: the 64 MB file is hashed at preflight (before freeze) and streamed after; increase to 256 MB rather than adding sleeps.

- [ ] **Step 4: Commit**

```bash
git add test/integration
git commit -m "test(integration): layer 1 fakeclaude scenarios through the jump host"
```

---

### Task 26: integration layer 2 — real Claude Code against fakeapi

**Files:**
- Create: `test/integration/realclaude_test.go` (build tags `integration && realclaude`)

- [ ] **Step 1: Write the test**

```go
//go:build integration && realclaude

// test/integration/realclaude_test.go
package integration

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func init() {
	composeFiles = []string{"-f", "docker-compose.yml", "-f", "docker-compose.realclaude.yml", "--profile", "realclaude"}
}

// latestAPIRequest returns the newest request body fakeapi logged whose
// path is /v1/messages.
func latestAPIRequest(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir("api-log")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.Contains(e.Name(), "v1-messages") && !strings.Contains(e.Name(), "count_tokens") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Fatal("fakeapi logged no /v1/messages request")
	}
	sort.Strings(names)
	b, err := os.ReadFile(filepath.Join("api-log", names[len(names)-1]))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRealClaudeResumeCarriesConversation(t *testing.T) {
	reset(t)
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
	reset(t)
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
}
```

`fakeapi-server` (Plan 02, `test/fakeapi-server/main.go`) names its log files `<unix-nanos>-<path with / replaced by ->.json`; if it uses another scheme, adapt `latestAPIRequest`'s filter to that scheme (the content check is what matters).

- [ ] **Step 2: Run against both versions**

Run:
```
test/integration/build.sh 2.1.247 && CLAUDE_VERSION=2.1.247 go test -tags 'integration realclaude' ./test/integration/ -run TestReal -v -timeout 40m
test/integration/build.sh latest  && CLAUDE_VERSION=latest  go test -tags 'integration realclaude' ./test/integration/ -run TestReal -v -timeout 40m
```
Expected: PASS for both. A failure on `latest` means the on-disk format drifted: that is a real failure to investigate (spec §12), not something to skip.

- [ ] **Step 3: Commit**

```bash
git add test/integration/realclaude_test.go
git commit -m "test(integration): layer 2 real Claude Code resume through fakeapi"
```

---

### Task 27: CI workflows — integration on every PR, real-claude on main and weekly

**Files:**
- Modify: `.github/workflows/test.yml` (Plan 01 created it with `vet`, `go test -race`, packaging tests)

- [ ] **Step 1: Write the workflow**

```yaml
# .github/workflows/test.yml
name: test
on:
  push:
    branches: [main]
  pull_request:
  schedule:
    - cron: "0 6 * * 1"   # Mondays 06:00 UTC: real-claude matrix

jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: sudo apt-get update && sudo apt-get install -y tmux git
      - run: go vet ./...
      - run: go test -race ./...
      - run: go test -race -tags tmuxlive ./internal/tmuxx/ ./internal/orchestrate/
      - name: packaging helper tests
        run: python3 -m unittest discover -s packaging -p 'version_test.py' -v

  integration:
    runs-on: ubuntu-latest
    needs: unit
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: test/integration/build.sh
      - run: go test -tags integration ./test/integration/ -v -timeout 40m
      - name: container logs on failure
        if: failure()
        run: docker compose -f test/integration/docker-compose.yml logs --no-color | tail -n 500

  real-claude:
    if: github.event_name == 'schedule' || (github.event_name == 'push' && github.ref == 'refs/heads/main')
    runs-on: ubuntu-latest
    needs: unit
    strategy:
      fail-fast: false
      matrix:
        claude: ["2.1.247", "latest"]
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: test/integration/build.sh ${{ matrix.claude }}
      - run: CLAUDE_VERSION=${{ matrix.claude }} go test -tags 'integration realclaude' ./test/integration/ -run TestReal -v -timeout 40m
      - name: container logs on failure
        if: failure()
        run: docker compose -f test/integration/docker-compose.yml -f test/integration/docker-compose.realclaude.yml --profile realclaude logs --no-color | tail -n 500
```

Neither job carries `continue-on-error`: a `latest` failure fails the workflow (spec §12).

- [ ] **Step 2: Validate**

Run: `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/test.yml'))" && echo ok` (or `actionlint` if installed).
Expected: `ok`. Push the branch and confirm the `integration` job runs on the PR and `real-claude` is skipped there.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/test.yml
git commit -m "ci: docker integration on every PR; real Claude matrix on main and weekly"
```

---

### Task 28: README

**Files:**
- Modify: `README.md` (complete rewrite; Plan 01 left a stub)

- [ ] **Step 1: Write it**

````markdown
# claude-teleport

Move one in-progress [Claude Code](https://docs.anthropic.com/en/docs/claude-code)
session — its transcript and every file Claude needs to resume it, the git
worktree it is working in, and the tmux window it lives in — from one
machine to another over ssh, confirm it resumed there, and leave the source
in a state you can resume locally or teleport back later.

> **Not** `claude --teleport`. Anthropic's own `claude --teleport` moves a
> session between claude.ai *cloud* environments. `claude-teleport` moves a
> session between *your own machines*. The two are unrelated.

```
alice@laptop:~/github/x/.worktrees/feat$ claude-teleport --to big-storage.example --via jump.example
Session  3f2a9c1e (running) on laptop.example
  cwd    /home/alice/github/x/.worktrees/feat
  branch feat
Move     To big-storage.example via jump.example
  claude 2.1.247 -> 2.1.247
Git
  existing-main: /home/alice/github/x already exists on the destination (same root commit)
  branch feat is fast-forward'ed to 9c1e7b4 with a packfile of the missing objects
  linked worktree is created at /home/alice/github/x/.worktrees/feat
  dirty state carried: 0 staged, 1 modified, 1 untracked, 0 deleted
tmux
  existing group "work" on /tmp/tmux-1000/default, new window "claude" in /home/alice/github/x/.worktrees/feat
End state  running
Files      214 to send, 0 already present, 0 fast-forward, 0 already staged
… (log follows) …
done: session 3f2a9c1e is now on big-storage.example (running)
```

## Install

Static binaries and `.deb` packages are attached to every release; the apt
repository is at `https://mith.ro/go-claude-teleport/`. The **same version**
must be installed on both machines (`claude-teleport doctor big-storage.example`
checks). Claude Code must already be installed and logged in on the
destination; the tool never installs or logs in.

```
sudo apt install claude-teleport            # after adding the repository
# or
go install github.com/mithro/go-claude-teleport/cmd/claude-teleport@latest
```

## Usage

```
claude-teleport [<session>] --to   <host> [--via <jump>]... [options]
claude-teleport [<session>] --from <host> [--via <jump>]... [options]
claude-teleport <tmux-session> <window> --to|--from <host> ...
claude-teleport continue <sid>            resume an interrupted job
claude-teleport status  [<sid>]           journal and manifest of a job
claude-teleport abandon <sid> [--delete-destination-files]
claude-teleport inspect [<session>] [--host <host>] [--json]
claude-teleport list [--host <host>] [--json]
claude-teleport compare-config <host> [--session <session>]
claude-teleport doctor [<host>]
claude-teleport placeholder --resume <sid> [--saved-output F] [--now] [--teleported-to H]
claude-teleport version
```

`--teleport-to` / `--teleport-from` are the canonical spellings; `--to` /
`--from` are aliases. Exactly one is required.

### Choosing the session

1. No argument: the session you are in (`$CLAUDE_CODE_SESSION_ID`), else the
   Claude running in the current tmux pane, else an error listing candidates.
2. A full session uuid.
3. A unique uuid prefix (≥ 4 hex characters) or a registry name.
4. Two words `<tmux-session> <window>` (window by index or name): the
   pane's running Claude, or the placeholder it left behind.

With `--from`, selection runs on the remote machine with the same rules.

### Running it from inside the session

Type `! claude-teleport --to big-storage.example` at the Claude prompt. The
command notices it is a child of the session being moved (`!`-mode): the
session is frozen while its files are copied, the command exits with a
one-line summary once the destination has confirmed, Claude records that
result, and then exits itself. The pane is left holding a placeholder that
says where the session went. `!`-mode only works with `--to`.

### Options

| Flag | Meaning |
|---|---|
| `--via HOST` | jump host(s), repeatable, outermost first; composes with `ProxyJump` from `~/.ssh/config` |
| `-o KEY=VALUE` | ssh option override (`User`, `Port`, `IdentityFile`, `StrictHostKeyChecking=accept-new`, …) |
| `--dest-path DIR` | put the session's cwd at DIR on the destination instead of the same path |
| `--map SRC=DST` | extra absolute path prefix rewrite, repeatable |
| `--state auto\|running\|suspended\|idle` | destination end state; `auto` (default) preserves the source state |
| `--allow-config-drift` | turn blocking configuration drift into warnings |
| `--force` | allow non-fast-forward replacement of an existing copy of *this* session |
| `--tmux-socket NAME` | destination tmux socket name (default: same as the source) |
| `--no-tmux` | do not use tmux on the destination (end state must be `idle`) |
| `--exclude GLOB` | omit matching files from the repository transfer, repeatable |
| `--include-ignored` | also transfer gitignored files |
| `--dry-run` | preflight and plan only; nothing touched, nothing frozen |
| `--exit-timeout D` / `--start-timeout D` | bounded waits (defaults 30s / 90s) |
| `--config-dir DIR` | local `CLAUDE_CONFIG_DIR` override |
| `--log FILE` | additional log destination |
| `--json` | machine-readable output for `status`, `list`, `inspect`, `compare-config` |
| `-v` / `-q` | log level |

### Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | teleport failed; the job is left resumable (`continue`) |
| 2 | usage |
| 3 | preflight refused (drift, collision, unsupported state) — nothing was touched |
| 4 | remote unreachable or version mismatch |
| 5 | the destination Claude did not resume (e.g. not logged in there); resumable from that step |
| 6 | interrupted; the runner keeps going, the job is resumable |

## What moves, what never moves

Moved (paths under `$CLAUDE_CONFIG_DIR`, default `~/.claude`):

- `projects/<cwd>/<sid>.jsonl` — the transcript, plus the session's
  `subagents/` and `tool-results/` directories
- `file-history/<sid>/`, `tasks/<sid>/` (without the lock), `session-env/<sid>/`,
  `todos/<sid>*`
- the session's entry in `sessions-index.json` and its lines in
  `history.jsonl` (merged, never replacing other sessions' entries)
- `projects/<cwd>/memory/` — copied only if absent on the destination;
  otherwise diffed and reported
- the project's entry in `~/.claude.json` (`projects["<cwd>"]`) — added if
  absent so the trust dialog and allow-list survive; never modified if present
- the git repository / worktree (see below) and a capture of the tmux pane

**Never moved**, enforced in code and tests: `.credentials.json`,
`sessions/` (the live-process registry), `*.key`, `settings.json`,
`plugins/`, and `~/.claude.json` as a whole. Nothing is ever sent to any
Claude API.

Absolute paths inside the moved JSON files are rewritten when the two
machines differ (`--dest-path`, `--map`, and `$HOME` → `$HOME`), including
the munged project directory name. Unknown fields and numbers survive the
rewrite untouched.

### Git

`M` is the main repository directory, `W` the worktree the session runs in
(`W == M` for a plain checkout).

- **`M` absent on the destination**: the whole repository is transferred
  (minus other linked worktrees and `--exclude`d or gitignored files), then
  `W`'s linked-worktree metadata is repaired for the destination paths —
  exactly what `git worktree repair` does. Staged, modified and untracked
  files arrive as they were.
- **`M` present on the destination**: it must be the same repository (same
  root commit). Only the missing objects travel, as one packfile. The
  branch is created or fast-forwarded — never rewound. A linked worktree is
  created at `W` (refused if `W` exists or the branch is checked out
  elsewhere); a plain checkout is fast-forwarded in place (refused unless
  clean and on the same branch). The dirty state is then applied on top.
  Uncommitted deletions are reported but never performed.
- **Not a repository**: the cwd is copied as plain files.

`inspect --host` and `--dry-run` print every one of these decisions before
anything moves. The tool uses go-git in-process; it never runs `git`.

### tmux

The destination server is found by the source's socket *name*, then
`default`, then the only live server under `/tmp/tmux-<uid>/`; otherwise
preflight fails listing what it found. A server is never started. The
window goes into the source's session group (created as `new-session -d`
if absent), with the same window name and `automatic-rename` setting. The
source pane's scrollback is replayed above Claude's banner so the pane
looks as it did.

End states: `running` (Claude at the prompt in the new window),
`suspended` (Claude exited, go-tmux-saver's `claude-resume` or the built-in
placeholder typed into the pane — Enter resumes), `idle` (Claude exited,
the window we created removed). Without tmux on the destination only
`idle` is possible: Claude is resumed under a pty, confirmed, and exited.

## Configuration drift

Before moving anything, both machines' Claude configuration is compared and
the session's transcript is scanned for what it actually used:

| Difference | Effect |
|---|---|
| any hook difference (settings or an installed plugin's hooks) | **refuse** |
| a *used* MCP server, plugin, skill or sub-agent type missing or different | **refuse** |
| `permissions.deny` or `defaultMode` differs | **refuse** |
| Claude version, model, effort, unused servers/plugins/skills, `allowedTools`, `env`, keybindings, CLAUDE.md/agents/commands | warn |
| destination lacks the project entry | info (it is carried over) |

`--allow-config-drift` downgrades every refusal to a warning.
`compare-config HOST` prints the table without a session (everything counts
as used). Nothing from the configuration is ever copied.

## The state machine, and what happens when it breaks

A teleport is a job keyed by the session id, journaled under
`~/.local/share/claude-teleport/jobs/<sid>/` on both machines. It runs in a
detached runner; the foreground command streams its log. Steps:

1. **preflight** — resolve, compare versions and configuration, plan git
   and tmux, check every destination path for collisions, print the plan
2. **freeze** — a running source Claude is `SIGSTOP`ped by a tiny freezer
   process that `SIGCONT`s it if the runner dies for any reason
3. **capture** — the source pane's scrollback
4. **transfer** — one gzip'd tar stream over a dedicated ssh channel, each
   file verified by size and SHA-256 into a staging directory
5. **install** — rewrite paths, move into place, merge index/history/project entry
6. **git-attach** — repair or create the worktree, index the pack, apply the dirty state
7. **start** — open the window, start Claude, **confirm** it resumed
   (registry entry alive in our pane, no login/API/"no conversation" errors
   on screen, prompt reached)
8. **shape** — reach the requested end state
9. **thaw+exit** — `SIGCONT`, `/exit` the source Claude, type the
   placeholder into its pane
10. **record** — history on both machines

Every step first re-checks reality (files, processes, panes); the journal
is a hint. On failure the runner thaws the source, marks the step, and
exits 1. **Nothing is ever deleted and nothing is rolled back.** Then:

```
claude-teleport status  <sid>     # what happened, which step, the manifest
claude-teleport continue <sid>    # pick up at the first incomplete step (re-running the original command does the same)
claude-teleport abandon <sid>     # give up; --delete-destination-files removes only what this job installed and that is unchanged since
```

The only existing file ever overwritten on the destination is an older copy
of the *same* session's transcript when the incoming one extends it (a
fast-forward — how teleporting back works). Anything else that already
exists with different content stops the job at preflight.

## Requirements

- Linux on both machines (macOS may work on the local side; untested);
  tmux ≥ 3.3 where tmux is used.
- ssh reachable with keys (agent or `~/.ssh/id_*`), host keys in
  `known_hosts` (or `-o StrictHostKeyChecking=accept-new`). `ProxyJump` is
  honoured; `ProxyCommand` is not — use `--via`.
- The same account (or at least a logged-in Claude) on the destination.

## Development

```
go test -race ./...                                  # unit tests
go test -race -tags tmuxlive ./internal/...          # against a throwaway tmux server
test/integration/build.sh && go test -tags integration ./test/integration/ -v   # docker: source, jump, dest
```

Apache-2.0. The tmux control-mode client is copied from
[go-tmux-saver](https://github.com/mithro/go-tmux-saver) (same author).
````

- [ ] **Step 2: Check every flag in the README exists**

Run: `for f in $(grep -o -- '--[a-z-]*' README.md | sort -u); do go run ./cmd/claude-teleport --help 2>&1 | grep -q -- "$f" || echo "missing in --help: $f"; done`
Expected: no output except `--teleported-to`/`--saved-output`/`--now`/`--resume`/`--session`/`--delete-destination-files`/`--host`/`--json` which belong to subcommands (check those with `go run ./cmd/claude-teleport <sub> --help`).

- [ ] **Step 3: Commit and open PR C**

```bash
git add README.md
git commit -m "docs: README per spec §13"
git push -u origin orchestrate-integration
gh pr create --title "orchestrator, commands, docker integration, README (Plan 03 PR C)" --body "Implements spec §5 (remaining commands), §6, §12 layers 1–2, §13. Tasks 17–28 of docs/superpowers/plans/2026-08-27-claude-teleport-03-orchestration.md"
```

---

## Interface additions

Everything below is new relative to `2026-08-27-claude-teleport-00-interfaces.md`; nothing there was renamed or re-typed.

**internal/gitx**
- `Info.DirtySubmodules []string`
- `DestState.BranchTipReachable bool` (set by the caller from the source side)
- `IsAncestor(repoDir, ancestor, descendant string) (bool, error)`
- `Plan.IndexRel string`, `Plan.StagedBlobs []string`, `Plan.PackEntryID int`, `Plan.IndexEntryID int`, `Plan.DirtyEntries map[string]int`
- `StagedBlobsOf(repoDir, indexPath, tip string) ([]string, error)`
- `SourceFacts{DestTipReachable bool; StagedBlobs []string}`, `SourceFactsOf(mainDir, indexRel, tip, destTip string) (*SourceFacts, error)`
- `WritePack` writes zero bytes when nothing is missing; `Attach` treats an empty pack file as "no pack"
- `PlanTransfer(nil, …)` returns a `ModeNotRepo` plan; the caller fills `SrcWorktree`/`DstWorktree`

**internal/tmuxx**
- `DialControl` takes a socket **path** (`tmux -S`); `ErrNoServer`
- `var Dial Dialer = DialControl` (FindServer's liveness probe; tests swap it) — `internal/cli` dials through this var too, so a test that swaps it also covers `remote serve`'s own probe
- `SessionInfo{Name, Group string}`, `ListSessions(ctx, t) ([]SessionInfo, error)`, `BaseSession(sessions, group) (string, bool)`
- `ShellQuote(argv []string) string`
- `RefString(ref *session.TmuxRef) string` — the one spelling of `<session>:<window>.<pane>`; every caller that formats a pane ref (steps, `list`, the registry comparison in `verifyStart`) uses it
- `IsShell(comm string) bool` — the shared "is this pane running a shell" predicate (`orchestrate` had a private copy; C4/B6)
- `PanePID(ctx, t, paneID) (int, error)` — the pid tmux itself started in the pane; the group the pty's foreground reverts to after a stopped job is thawed
- `RestoreForeground(ctx, t Transport, paneID string, pid int, opts ForegroundOptions) error`, `ForegroundOptions{ProcRoot, Logf, Sleep, Timeout, Poll}`, `ForegroundPoll`, `ForegroundTimeout`, `ErrNotRestored` — the ONE implementation of "ask the pane's shell to `fg` the thawed job back into the pty" (ruling R-P3-F1). `remote.Local.restoreForeground` delegates to it; nothing else re-implements the check-then-type dance
- `FreezerRestore(socketPath, paneID string) procx.RestoreFunc` — that same restore, packaged as the hook the freezer helper runs after the SIGCONT of its owner-died path (it dials the pane's own server; `procx` cannot import `tmuxx`, so `internal/cli` hands it in)
- `StartTestServer(t) (socketPath, socketDir string)` under build tag `tmuxlive`
- **Knowing deviation from the "no package reads the environment" rule:** `utf8Env` (client.go) reads `LC_ALL`/`LC_CTYPE`/`LANG` out of the environment it is given and forces a UTF-8 locale for the `tmux -C` child. It is not a directory or a default a caller could pass instead — it is the locale of the very process being spawned, and a non-UTF-8 one makes tmux mangle non-ASCII pane names and content. Documented here rather than plumbed through every call site.

**internal/job**
- `ValidateID(id string) error` — the one rule for what may appear as `jobs/<id>`/`staging/<id>` on either host (non-empty, no separators, no `.`/`..`, no control characters); the wire dispatch and every `Local` method that turns an id into a path call it (rulings R-P3-23i/R-P3-23n)

**internal/sshx**
- `DefaultKeepaliveInterval = 15 * time.Second`, `DefaultKeepaliveCountMax = 3` and `Options.KeepaliveInterval time.Duration` / `Options.KeepaliveCountMax int` (OpenSSH's `ServerAliveInterval`/`ServerAliveCountMax`). Unlike OpenSSH they are ON by default: a teleport runs unattended, and a half-open link must fail the job into a continuable journal rather than hang it. `interval <= 0` disables them; `-o ServerAliveInterval=`/`-o ServerAliveCountMax=` and the same two keywords in `~/.ssh/config` override, and `ServerAliveCountMax=0` is an error rather than a silent default
- `sshtest.Options.GlobalRequestDelay func(n int) time.Duration` — delays the n-th global request so a test can simulate a link that stops answering keepalives

**internal/procx**
- `(*Freezer).Warnings() string` — whatever the freezer helper wrote to stderr (its degrade-to-bare-pid notes), so the caller can put them in `log.txt` on the success path too
- `PaneRef{SocketPath, PaneID string}` with `Empty() bool`, and `RestoreFunc func(pid int) error`; `Freeze(selfExe string, pid int, startTime string, ref PaneRef)` and `RunFreezerHelper(pid int, startTime string, control *os.File, restore RestoreFunc)` take them (ruling R-P3-F1). The ref reaches the helper through its argv (`internal-freezer <pid> <start> [socket pane]`, hence `internal/cli`'s `cobra.RangeArgs(2, 4)`); the hook runs only on the owner-died path, since the ordinary thaw's owner does the restore itself

**internal/remote**
- `Endpoint` gains: `GitFiles`, `GitSourceFacts`, `BuildManifest`, `SessionExtras`, `TmuxSessions`, `KillWindow`, `ClaudeStatus`, `ListSessions`, `Cleanup`, `DeleteInstalled`, `RemoveJob` (signatures in Tasks 13, 16, 23; `RemoveJob` removes `jobs/<id>/` entirely — the wire dispatch handler refuses any id not prefixed `inspect-`, ruling R-P3-23i)
- `Endpoint.Freeze(ctx, pid int, startTime string, ref *session.TmuxRef) error` — the pane joins the freeze so the freezer helper can restore its foreground unaided if this process dies (ruling R-P3-F1); `FreezeArgs.Ref *session.TmuxRef` carries it over the wire
- `SessionSummary` struct
- `LocalOptions.TmuxSocketDir string`, `LocalOptions.Sleep func(time.Duration)`
- `FailureMarkers`, `HasFailureMarker`
- `PipeStream(fn func(r io.Reader, w io.Writer) error) io.ReadWriteCloser`
- Stream ids carry a direction: `send:<n>` / `recv:<n>`; `tar`/`pack` receive into `staging/<job>/`, `pack` lands at `staging/<job>/objects.pack`
- `Local.ManifestDiff` and `Local.BuildManifest` persist `jobs/<job>/manifest.json`; `Local.Install` reads `InstallExtras` from `jobs/<job>/extras.json` exactly as Plan 02 wrote it (the orchestrator calls Plan 02's `PutInstallExtras` first); `planView` serves the streams only
- Ops added to the protocol: `git-files`, `git-source-facts`, `tmux-sessions`, `tmux-kill`, `claude-status`, `build-manifest`, `session-extras`, `cleanup`, `list-sessions`, `delete-installed`, `remove-job` (Plan 02's existing ops, incl. `shape-state` and `claude-pty-resume`, keep their names, `dispatch` entries and `Client` methods)
- Dependency added: `github.com/creack/pty` (RunPtyResume only)

**internal/transfer**
- Plan 02's `Build` already takes `Size` from the bytes it hashes and `Mode`/`ModTime` from `os.Stat` when the `FileEntry` leaves them zero, which is what the capture entry (`runCapture`, built on the driver and hashed by `BuildManifest` on the source) relies on.
- `InstallReport.InstalledIDs []int` (`json:"InstalledIDs"`, ruling R-P3-23a: ids `Install` placed at `Dst` from scratch) and `InstallReport.FastForwardedIDs []int` (ruling R-P3-23h: ids `Install` fast-forwarded — folded into `Plan.InstalledIDs`, below, only when already recorded there)
- `InstallExtras.Force bool` (the driver's `--force`, relayed to the destination) and `InstallReport.ForceOverwritten int` (entries replaced wholesale under it, hash-verified) — B12
- `Manifest.Roots []Root` (`json:"roots,omitempty"`) and `Root{Path string; MayPreExist bool}` (rulings R-P3-B1d/B1e): the destination repository root(s) a `CatRepo`/`CatWorktree` entry's `Dst` must lie under — `gitx.Plan`'s own `DstMain`/`DstWorktree`, populated by `orchestrate`'s `annotateManifest`. `MayPreExist` marks not-a-repo mode's single root (the driver's chosen destination cwd, which legitimately already exists and holds unrelated files); it relaxes ONLY the provenance rule, never a containment rule
- `GitRoots(dstMain, dstWorktree string, mayPreExist bool) []Root` — builds that field (dedup, empties dropped)
- `Diff(ctx, m, stagingDir string, p session.Paths) (map[int]Status, error)` (R-P3-B1e item 5) — the destination's own `Paths` let `Diff` run the very same validator `Install` runs, so a category/root/symlink refusal is diagnosed at preflight instead of being mistaken for a content collision. A manifest with any refused entry is not classified at all: `Diff` returns `*RefusalError`
- `Refusal{ID, Dst, Category, Reason}`, `RefusalError{Refusals []Refusal}`, `IsRefusal(err) bool` — the "this entry may never be written here" verdict. `remote.Local.ManifestDiff`/`Install` relay it as a `remote.Error` with code `refused`; `orchestrate.Preflight` turns that into a `RefusedError` (exit 3)
- `Refuse(dst, format, …) *RefusalError`, `ResolveRealPath(path) (string, error)`, `CheckDestDir(p session.Paths, dir string) error`, `JobCreatedRoot(p session.Paths, jobID, dir string) (bool, error)` (R-P3-B1f N1) — the destination's own path rules, exported so `internal/remote` can apply the identical resolved-root reasoning to the `DstMain`/`DstWorktree`/`WorktreeName`/`IndexRel`/dirty-file paths a wire `gitx.Plan` names, BEFORE `gitx.Attach` writes anything (git-attach is the destination's second write path)
- `jobs/<id>/installed.json` on the DESTINATION (`{"version":1,"entries":[{dst, sha256, category, kind, symlink}]}`, 0600, R-P3-B1f N3) — what `Install` actually placed, written on every exit path (a partial install's placements stay deletable). `Uninstall`/`UninstallIDs`/`DeleteInstalled` may remove ONLY what it records, and only while the content still matches; the manifest and the caller's ids can narrow that set, never widen it. Removed with the job directory by `abandon`/`RemoveJob`, and untouched by `Cleanup` (staging only)
- `Root.MayPreExist` is corroborated, never believed (R-P3-B1f N2): honoured only when the root really is an existing directory AND the manifest's own `CatSession` entries place the transcript under `projects/<session.Munge(root)>`
- `jobs/<id>/roots.json` on the DESTINATION (`{"version":1,"roots":[…]}`, 0600) — the repo roots THIS job created there. `Install` claims a root once the whole manifest has validated and still before placing anything under it (R-P3-B1f N5), recording and comparing EvalSymlinks-resolved paths (N4); "freshness" is that record, not a directory's current contents, so the destination's own re-runs (verifyInstall's re-diff, a resumed job) still recognise a legitimately non-empty root as their own. Removed with the job directory by `abandon`/`RemoveJob`

**internal/orchestrate**
- `Options.Target string`, `Options.Via []string`, `Options.SSHOptions map[string]string`, `Options.LocalDest *session.Paths` (tests)
- `Plan` JSON tags (`options`, `statuses`, `git`, `extras`, … — `remote.planView` depends on them) and fields `JobID`, `SourceFacts`, `Files`, `Statuses`, `InstalledIDs` (`json:"installed_ids"`, ruling R-P3-23a — the durable record of what THIS job installed; `Statuses` is overwritten by later manifest-diffs and cannot answer that once a job finishes), `Extras`, `CaptureEntryID`, `DestCwd`, `DestCapture`, `DestRef`, `CreatedSession`, `CreatedWindow`, `DestRegistry`, `StartedAt`
- `(*Plan).ToJSON()`, `PlanFromJournal(j)`, `RefusedError`, `UnreachableError`, `PlaceholderArgv`, `SuspendArgv`, `StepNames`, `EndpointFactory`, `RunJob`, `ExitCode`, `FailedStep`
- `Plan.RecordedSrc`/`Plan.RecordedDst bool` (`json:"recorded_src"`/`"recorded_dst"`) — each host's history row is appended once per job, so a re-run of the record step cannot duplicate it (finding A8)
- `ExitOK = 0`, `ExitFailed = 1`, `ExitNotResumed = 5` — the spec §5 codes a journal can decide, defined once here next to `ExitCode` and re-exported by `internal/cli` (finding A13)
- `Steps(p, j, src, dst, logf)` and `RunJob(ctx, dataDir, jobID, factory, logf)` take no `selfExe`: no step re-execs the binary (finding A9)

**internal/session** (Plan 01 types, extended here)
- `NewPaths(home, configDir, xdgDataHome string, configDirFromEnv bool) Paths` — the fourth argument, and the `Paths.ConfigDirFromEnv bool` field it sets, record that `CLAUDE_CONFIG_DIR` was actually SET rather than `ConfigDir` merely defaulting to `~/.claude`. Claude Code picks `$HOME/.claude.json` vs `<ConfigDir>/.claude.json` by the variable's presence, not its value, and `remote.claudeEnv` decides from it whether a claude this host starts sees the variable at all. `internal/cli` is the only reader of the environment and so the only caller that can pass `true`
- `Registry.Entrypoint string` (`json:"entrypoint"`) — how the session was launched: `"cli"` for a terminal session, `"sdk-cli"` for a `claude -p` run. Verified against real Claude Code 2.1.247 and 2.1.259: `kind` is `"interactive"` for both, so this is the only field that tells a print run apart

**test/fakeclaude**
- (no additions) `FAKECLAUDE_TMUX`, `FAKECLAUDE_FAIL=not-logged-in` and `FAKECLAUDE_RUN_CHILD` are Plan 01 Task 19's env contract, used as defined there

## Self-review

**Spec coverage (sections in scope):**

| Spec | Task(s) |
|---|---|
| §5 `continue`, `abandon`, `inspect`, `list --host`, `doctor`, root teleport, exit codes | 21, 23 |
| §6 steps 1–10, runner, journal on both hosts, re-verification | 18, 20, 21 |
| §6.1 freezer use, `!`-mode silence until step 9 | 20 (`runFreeze`), 21 (`follow`) |
| §6.2 confirmation (registry + markers + idle) | 14 (`ConfirmClaude`), 15 (pty) |
| §6.3 exit in tmux / `!`-mode / not-in-tmux | 14 (`ExitClaude`), 20 (`runThawExit`) |
| §6.4 source never modified; teleport back = fast-forward | 22 (`TestE2EReTeleportBack…`), 25 |
| §7.1 capture in the manifest | 20 (`runCapture`) |
| §8 inventory, decision table, pack, attach, `inspect` shows decisions | 1–7, 19, 23 |
| §9 facts, discovery, placement, start, end states, no-tmux | 9–11, 14–15, 18, 20 |
| §11 placeholder argv keeps `--resume <uuid>` | 11 (`TestStateForegroundChild`), 17 |
| §12 layer 1 scenarios (all thirteen bullets) | 25 (`TestTo…`, `TestFrom…`, `TestSuspendedAndIdle…`, `TestDifferentHome…`, `TestReTeleport…`, `TestDrift…`, `TestBangMode…`, `TestKilledRunner…`, `TestDroppedSSH…`, `TestAbandon…`) |
| §12 layer 2 (pinned + latest, fakeapi, tmux registry field) | 24, 26, 27 |
| §13 workflows, README | 27, 28 |

Gaps knowingly left for follow-up (outside this plan's scope): `status` and `compare-config` are Plan 02/01 deliverables; `release.yml`/nfpm are Plan 01.

**Placeholder scan:** every code step carries the code; every Plan 01/02 identifier used here (`app` and its fields, `ExitError`/`Exit` behind `usageErr`/`exitErr`, `teleportFlags`, `l.paths`/`l.opts`/`c.call`/`dispatch`/`decode`, `PutInstallExtras`, `FAKECLAUDE_FAIL`/`FAKECLAUDE_TMUX`/`FAKECLAUDE_RUN_CHILD`) is the name those plans define.

**Type consistency:** `DestState.BranchTipReachable` (Task 2) is what `Preflight` sets (Task 18); `Plan.IndexRel`/`IndexEntryID`/`DirtyEntries` (Task 3) are read by `Local.GitAttach` (Task 13) and written by `annotateManifest` (Task 18/20); stream ids `send:`/`recv:` (Task 16) are what `runner.pump` opens (Task 20); `planView` keys (Task 16) match `Plan`'s JSON tags (Task 17); `SuspendArgv`/`PlaceholderArgv` (Task 17) are what `runShape`/`runThawExit`/`runStart` type (Task 20) and what `procx.IsPlaceholderArgv` recognises (Task 11 test).
