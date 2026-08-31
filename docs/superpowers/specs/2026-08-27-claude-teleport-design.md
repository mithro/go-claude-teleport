# claude-teleport — design

*Status: approved in brainstorming on 2026-08-27; implementation plan to follow.*

## 1. Why

Claude Code sessions accumulate hours of context: the transcript, the
sub-agent transcripts, file-edit history, tasks, the git worktree they are
working in, the tmux window they live in. Today that context is welded to
the machine it was started on. When the work would be better done elsewhere
(a bigger machine, a machine with the hardware attached, a machine that is
not about to be shut down), the only options are to start again or to hand
copy `~/.claude` fragments around by hand and hope `claude --resume` finds
them.

`claude-teleport` moves one in-progress session — everything Claude Code
needs to resume it, the git repository and worktree it was working in, and
the tmux window it was running in — from one machine to another over ssh,
confirms it resumed there, and leaves the source in a state that can be
resumed locally or teleported back later.

## 2. Goals and non-goals

**Goals**

- One command, run on either end (`--to` from the source, `--from` from the
  destination), including from *inside* the session being moved
  (`! claude-teleport --to host`).
- Faithful: the destination Claude sees exactly the conversation the source
  had; the git worktree has the same branch, commits, staged and unstaged
  changes, untracked files; the tmux window has the same name, group and
  scrollback.
- Same paths by default; correct path rewriting when homes differ.
- Never damage the destination: no existing file is ever overwritten
  (single, explicit exception: fast-forwarding a copy of the *same* session).
- Never transfer credentials; never talk to any Claude API.
- Loud, early failure on anything that would make the session behave
  differently on the destination (hooks, MCP servers the session used, …).
- Resumable: an interrupted teleport continues from where it stopped.
- No automatic rollback. Manual, inspectable clean-up instead.
- In-binary: ssh, git, tar, gzip are Go libraries, not subprocesses.
- Tested against the real Claude Code on-disk format, pinned and latest,
  across real machines (containers) separated by a jump host.

**Non-goals**

- Moving sessions between different users on the same machine.
- Merging two divergent copies of a session (that is a hard failure).
- Cloud sessions (`claude --teleport` is Anthropic's own, unrelated feature
  for claude.ai cloud sessions — documented in the README to avoid confusion).
- Copying `settings.json`, plugins, or any global Claude configuration. They
  are compared, not copied.
- Windows. Linux is the target; macOS should work for the local side but is
  not tested.

## 3. Claude Code on-disk model (verified against 2.1.247)

Everything below was verified by inspection on 2026-08-27. Fixture files in
the repository are sanitised copies of real files from this version. Because
this format is undocumented and evolves, every assumption here is a test.

`$CLAUDE_CONFIG_DIR` (default `~/.claude`) contains:

| Path | Contents | Teleported? |
|---|---|---|
| `projects/<munged-cwd>/<sid>.jsonl` | the transcript: one JSON record per line, `type` ∈ {`user`, `assistant`, `system`, `summary`, `ai-title`, `custom-title`, `agent-name`, `last-prompt`, `mode`, `permission-mode`, `attachment`, `file-history-snapshot`, `file-history-delta`, `queue-operation`, `pr-link`, `bridge-session`, …}; records carry `sessionId`, `cwd`, `gitBranch`, `version`, `timestamp` | yes |
| `projects/<munged-cwd>/<sid>/subagents/agent-*.jsonl`, `*.meta.json` | sub-agent transcripts | yes |
| `projects/<munged-cwd>/<sid>/tool-results/*.txt` | persisted large tool outputs | yes |
| `projects/<munged-cwd>/sessions-index.json` | `{version, entries:[{sessionId, fullPath, fileMtime, firstPrompt, summary, messageCount, created, modified, gitBranch, projectPath, isSidechain}], originalPath}` | the session's entry is merged |
| `projects/<munged-cwd>/memory/` | auto-memory for the project (shared by all sessions of that project) | copied only if absent on the destination; otherwise diffed and reported |
| `file-history/<sid>/<hash>@v<N>` | backups of edited files | yes |
| `tasks/<sid>/*.json`, `.lock` | task list | yes (lock excluded) |
| `session-env/<sid>/` | per-session env snapshots | yes |
| `todos/<sid>*.json` | legacy todo files | yes |
| `history.jsonl` | global prompt history: `{display, pastedContents, timestamp, project, sessionId}` | the session's lines are appended (deduped) |
| `sessions/<pid>.json` | **registry** of live Claude processes: `{pid, sessionId, cwd, startedAt, procStart, version, kind, entrypoint, tmux:"<session>:@<win>.%<pane>", messagingSocketPath, name, nameSource, status:"busy"|"idle", updatedAt, statusUpdatedAt, bridgeSessionId, …}` | **never** — read only, to find running sessions and confirm start |
| `sessions/<pid>.<hash>.key` | messaging token | **never** |
| `.credentials.json` | OAuth tokens | **never** |
| `settings.json` | hooks, permissions, env, enabledPlugins, model, effortLevel, … | compared, never copied |
| `plugins/installed_plugins.json`, `plugins/known_marketplaces.json`, `plugins/cache/<marketplace>/<plugin>/<ver>/` | plugin inventory; plugins may ship `hooks/hooks.json` and `.mcp.json` | compared, never copied |
| `CLAUDE.md`, `agents/`, `skills/`, `commands/` | user-level customisations | compared (tree hash), never copied |
| `shell-snapshots/`, `debug/`, `telemetry/`, `statsig/`, `cache/`, `backups/`, `paste-cache/`, `security/`, `stats-cache.json`, `.last-*` | caches and diagnostics | no |

`~/.claude.json` (note: a *file* next to the directory) holds global state.
Only `projects["<absolute cwd>"]` is relevant:
`{allowedTools, mcpServers, enabledMcpjsonServers, disabledMcpjsonServers,
mcpContextUris, hasTrustDialogAccepted, hasClaudeMdExternalIncludesApproved,
…}`. The top-level `mcpServers` map is the user-level MCP configuration.
`oauthAccount`, `userID`, and every cache/telemetry key are never read for
transfer purposes; the file is parsed generically and only the keys named
here are inspected. The session's project entry is **added** on the
destination if absent (so the trust dialog and allow-list survive); if
present it is left alone and differences are reported.

Project-level configuration travels with the repository and is not handled
separately: `<repo>/.mcp.json`, `<repo>/.claude/settings.json`,
`<repo>/.claude/settings.local.json`, `<repo>/CLAUDE.md`.

**Munging**: the project directory name is the absolute cwd with every `/`
and `.` replaced by `-` (`/home/alice/github/x/.worktrees/y` →
`-home-alice-github-x--worktrees-y`). A worktree session therefore has its
own project directory, distinct from the main checkout's.

**Environment inside a session** (what `! claude-teleport` sees):
`CLAUDE_CODE_SESSION_ID`, `CLAUDE_PID`, `CLAUDE_CODE_EXECPATH`
(`…/versions/<ver>`), `CLAUDECODE=1`, `TMUX`, `TMUX_PANE`.

**Environment the tool honours**: `CLAUDE_CONFIG_DIR` (both hosts, so tests
and CI can use throw-away config dirs), `HOME`, `CLAUDE_CODE_SESSION_ID`,
`CLAUDE_PID`, `TMUX`, `TMUX_PANE`, `SSH_AUTH_SOCK`.

**Resume semantics**: `claude --resume <sid>` is run from the session's
launch cwd (the first `cwd` in the transcript); the built-in placeholder
`chdir`s there before exec, as go-tmux-saver's `claude-resume` does.

## 4. Architecture

One static Go binary, `claude-teleport` (module
`github.com/mithro/go-claude-teleport`, `CGO_ENABLED=0`, Go 1.26+). The
same binary runs on both hosts: as the **driver** where the user types, and
as the **remote helper** (`claude-teleport remote <op>`) executed over ssh.

```
cmd/claude-teleport/main.go
internal/cli/          cobra commands, flags, exit codes, usage text
internal/session/      locate a session; inventory its files; parse the transcript;
                       path-rewrite engine; fast-forward check; index/history merge
internal/claudecfg/    host configuration inventory; usage analysis; drift report
internal/gitx/         go-git: repo/worktree inventory, missing-object packs,
                       linked-worktree metadata, dirty-state listing
internal/tmuxx/        tmux control-mode client (copied from go-tmux-saver's
                       internal/tmuxctl, Apache-2.0, attributed); server discovery;
                       session-group/window creation; capture; send-keys
internal/procx/        /proc scan, registry lookup, SIGSTOP/SIGCONT freezer,
                       exit-and-confirm, detached runner spawning
internal/sshx/         x/crypto/ssh client: ssh_config, agent, known_hosts, jump chain
internal/remote/       the JSON-over-ssh helper protocol (client + server)
internal/transfer/     manifest build/diff; tar+gzip streaming with per-file verify
internal/job/          journal (job.json), log, step runner, continue/abandon
internal/orchestrate/  the teleport state machine (steps 1–10)
internal/placeholder/  built-in confirm-before-resume placeholder
internal/fakeapi/      canned Anthropic Messages API server (tests/CI only)
test/fakeclaude/       fake `claude` binary reproducing the on-disk behaviour
test/integration/      docker compose harness and Go tests driving it
packaging/, nfpm.yaml, .github/workflows/   as go-tmux-saver
```

Library choices (all in-process; no `ssh`, `rsync`, `tar`, `gzip` or `git`
subprocess is ever launched by the tool):

| Need | Library |
|---|---|
| ssh transport, agent auth, known_hosts | `golang.org/x/crypto/ssh` (+ `agent`, `knownhosts`) |
| `~/.ssh/config` (Host, HostName, User, Port, IdentityFile, ProxyJump) | `github.com/kevinburke/ssh_config` |
| git | `github.com/go-git/go-git/v5` |
| CLI | `github.com/spf13/cobra` |
| archive, compression, JSON, signals | stdlib |
| test diffs | `github.com/google/go-cmp` |

`tmux` is the one external program the tool talks to, over a single
control-mode connection (`tmux -L <socket> -C`) per run — there is no other
interface to a tmux server.

### 4.1 Roles and directions

Every step is expressed against two abstract endpoints, **Source** and
**Destination**, each of which is either `Local` or `Remote(ssh client)`.
`--to host` makes Source local and Destination remote; `--from host` the
reverse. The orchestrator code is identical for both; only the endpoint
bindings change. `!`-mode (running inside the session being moved) is only
meaningful with `--to`.

### 4.2 ssh

`internal/sshx` builds a client from: the target string (`[user@]host[:port]`),
`~/.ssh/config` (aliases, `HostName`, `User`, `Port`, `IdentityFile`,
`ProxyJump`), `--via` (repeatable; each is itself resolved through
ssh_config), `-o key=value` overrides, `SSH_AUTH_SOCK` for agent keys,
`~/.ssh/id_*` for key files, `~/.ssh/known_hosts` for host verification
(unknown hosts are an error with the fingerprint shown; `-o
StrictHostKeyChecking=accept-new` adds them). Jump chains are built by
dialling hop *n+1* through hop *n*'s connection (`client.Dial("tcp",
"next:port")`), so **the final hostname is resolved by the last jump host**,
never locally. `ProxyCommand` is not supported: a clear error names the
host and suggests `--via`. One ssh connection is opened per remote endpoint
and every channel (control, file streams) is multiplexed over it; a lost
connection is re-dialled (bounded retries with backoff) and the step
re-verified.

### 4.3 Remote helper protocol

`ssh host claude-teleport remote` starts a helper that reads newline-delimited
JSON requests on stdin and writes responses on stdout; stderr is the remote
log (forwarded into the job log). Each request is `{"id":n, "op":"…",
"args":{…}}`; each response `{"id":n, "ok":bool, "result":{…}|"error":{…}}`.
Bulk data (tar streams, captures, packfiles) travels on separate ssh
channels opened by the driver via one command, `claude-teleport remote
stream <kind> <job> <id>` (`<kind>` is `tar`, `capture`, `pack` or `log`),
so the control channel is never blocked.
The first op is always `hello` → `{version, protocol, home, configDir,
uid, hostname, os, arch}`; a protocol mismatch aborts with both versions and
the install hint.

Ops: `hello`, `inventory-host`, `inventory-session`, `inventory-git`,
`inventory-tmux`, `manifest-diff`, `install`, `git-attach`, `tmux-open`,
`tmux-capture`, `tmux-keys`, `claude-start`, `claude-confirm`, `claude-exit`,
`freeze`, `thaw`, `shape-state`, `job-journal-get/put`, `record`. (The bulk
streams above are not JSON ops on this control channel; they are the
separate `remote stream <kind> <job> <id>` command.)

## 5. Command line

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
are aliases. Exactly one of them is required for a teleport.

**Session selector** (`<session>`), in order of resolution:

1. absent → `$CLAUDE_CODE_SESSION_ID` if set (we are inside the session),
   else the Claude running in the current tmux pane (`$TMUX_PANE` matched
   against registry `tmux` fields), else an error listing candidates;
2. a full uuid;
3. a unique uuid prefix (≥ 4 hex chars) or a unique registry `name`;
4. two positional words `<tmux-session> <window>` (window by index or name);
   the pane's Claude (running) or `claude-resume`/`placeholder --resume`
   placeholder (suspended) identifies the session.

With `--from`, selection runs on the remote (the source) with the same rules.

**Options**

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

**Exit codes**: 0 success; 1 teleport failed (job left resumable); 2 usage;
3 preflight refused (drift, collision, unsupported state) — nothing touched;
4 remote unreachable / version mismatch; 5 confirmation failed (destination
Claude did not resume — e.g. not logged in); 6 interrupted (job resumable).

## 6. The teleport state machine

A teleport is a **job** keyed by session id, journaled on both hosts under
`$XDG_DATA_HOME/claude-teleport/jobs/<sid>/` (`job.json`: plan and per-step
status; `log.txt`; `manifest.json`; `capture.txt`). The job runs in a
**detached runner** (`setsid`, own process group, stdio to `log.txt`),
never as a child of Claude. The foreground command starts (or attaches to)
the runner, streams `log.txt`, and exits with the job's result. Re-running
the original command, or `continue <sid>`, attaches to a live runner or
resumes a dead one at the first incomplete step. Every step first
re-verifies its preconditions against the filesystem and process table; the
journal is a hint, reality is the truth.

| # | Step | What it does | Idempotency / re-verification |
|---|---|---|---|
| 1 | **preflight** | resolve the session; `hello`; host, session, git, tmux inventories on both ends; drift report; git plan; tmux plan; collision check of every target path; write the plan to the journal; print it | pure; a persisted plan is reused so later steps cannot make different decisions |
| 2 | **freeze** | if the source Claude is running, the **freezer** stops it (§6.1) | already stopped → no-op |
| 3 | **capture** | `capture-pane -epJ -S -` of the source pane (if any) → `capture.txt` | always redone (cheap) |
| 4 | **transfer** | manifest-driven streaming (§7) into destination staging | resends only missing/mismatched files |
| 5 | **install** | rewrite paths in staging; move into place; merge index/history/project entry | per-file: identical → skip; different → stop |
| 6 | **git-attach** | write linked-worktree metadata / update refs / apply dirty state (§8) | absent-or-identical → write; else stop |
| 7 | **start** | open the destination window (§9), replay capture, start Claude, **confirm** (§6.2) | registry already shows the session alive in our pane → confirm only |
| 8 | **shape** | reach the requested end state (§9) | checks the pane's current command first |
| 9 | **thaw+exit** | `SIGCONT`; exit the source Claude (§6.3); type the placeholder into the source pane | thaw no-op if not stopped; exit skipped if pid gone; placeholder only onto a bare shell |
| 10 | **record** | append to `jobs/<sid>/history.jsonl` on both hosts; mark the job done | append |

On any failure the runner: thaws (via the freezer), marks the step failed
with the error, and exits 1. Nothing is deleted. The user sees the step name,
the error, and the `status`/`continue`/`abandon` commands.

`--dry-run` executes step 1 only.

### 6.1 Freezing safely

A running Claude keeps appending to its transcript (the result of the very
`! claude-teleport` command included). Copying a moving file would produce a
destination that diverges from the source. So a running source Claude is
`SIGSTOP`ped for steps 3–8. Because a stopped process can never be left
stopped by a crash, the stop is owned by a **freezer**: a tiny process the
runner spawns with a pipe; the freezer sends `SIGSTOP`, then blocks on the
pipe; when the runner writes `thaw` *or dies* (pipe EOF), it sends `SIGCONT`
and exits. `procStart` from `/proc/<pid>/stat` is checked before every signal
so a reused pid can never be signalled.

While the parent is stopped, a `!`-mode foreground process must not write
more than the pipe buffer to stdout (the parent is not reading). The
foreground therefore prints nothing until the runner has reached step 9 (or
failed); progress goes to `log.txt`, and the tail of the log is printed once
at the end.

### 6.2 Confirming the destination resumed

Success is declared only when all of the following hold within
`--start-timeout`:

1. a registry file `sessions/<pid>.json` on the destination has
   `sessionId == <sid>`, its `pid` is alive with a matching `procStart`,
   and (tmux case) its `tmux` field names the pane we opened;
2. the pane content (tmux) or pty output (no-tmux) contains none of the
   known failure markers (login prompts, "not logged in", "Invalid API key",
   "No conversation found", version-incompatible transcript errors) — the
   marker list is a fixture, updated when Claude changes its wording;
3. the registry `status` has been observed as `idle` (Claude reached the
   prompt), or `busy` after a user turn for `-p` runs.

Failure here is exit code 5 and the message tells the user to log in on the
destination themselves; the job remains continuable from step 7.

### 6.3 Exiting the source Claude

- **In tmux** (the normal case): `send-keys "/exit"`, 500 ms, `Enter`; poll the
  process table until the pid is gone (bounded by `--exit-timeout`); then
  type ` claude-teleport placeholder --resume <sid> --saved-output
  <capture> --teleported-to <host>` into the pane (leading space keeps it
  out of shell history). Enter on that placeholder resumes the session
  locally; its banner says where and when it was teleported and warns that
  resuming locally forks the history.
- **`!`-mode**: the foreground process is Claude's child and Claude is
  mid-turn. After the runner reaches step 9 it thaws, the foreground exits
  0 with a one-line summary, Claude records the command's result, and the
  runner waits for the registry `status` to become `idle` before sending
  `/exit` as above. If the teleport failed, the runner thaws and the
  foreground exits non-zero with the error; Claude simply continues.
- **Not in tmux**: keystrokes cannot be injected (`TIOCSTI` is disabled on
  current kernels: `/proc/sys/dev/tty/legacy_tiocsti` = 0), so the runner
  sends `SIGTERM`, waits for the pid to disappear, and fails loudly (exit 1,
  job continuable) if it survives. No placeholder can be typed; the summary
  says how to resume locally.

### 6.4 Source state afterwards

The source's session files are **never** modified or removed. Both directions
of a later move work: teleporting *back* is an ordinary teleport whose
destination already has the session, handled by the fast-forward rule
(§7.3). Resuming locally is Enter on the placeholder (or `claude --resume`).

## 7. Transfer

### 7.1 Manifest

The source builds `manifest.json`: for every file to move — session files
(§3), the git repository/worktree (§8), `capture.txt` — the source path,
destination path (after rewriting), size, mode, mtime, SHA-256, and a
category (`session`, `repo`, `worktree`, `capture`). The path-rewrite map
(§7.2) is part of the manifest. Symlinks are recorded as symlinks (never
followed); sockets, fifos and device nodes are skipped and listed.

The **forbidden list** — `.credentials.json`, `sessions/`, `*.key`,
`~/.claude.json` as a whole, `settings.json`, `plugins/` — is enforced in
the manifest builder and verified by a test that feeds it a config dir
containing every forbidden path.

### 7.2 Path rewriting

The map is ordered, longest prefix first: `--map` entries, then
`--dest-path` (source cwd → DIR), then `$HOME_src → $HOME_dest` if they
differ. It is applied to:

- the munged project directory name (recomputed from the rewritten cwd);
- every JSON string value in every `.jsonl`/`.json` session file, by
  decode → walk → re-encode per record with `SetEscapeHTML(false)` and
  `UseNumber()` so unknown fields, key order semantics and numeric precision
  survive; records that fail to parse are copied byte-for-byte and counted
  in the report;
- `sessions-index.json` entries (`fullPath`, `projectPath`), `history.jsonl`
  (`project`), the `~/.claude.json` project key;
- file-history and task files (they contain absolute paths in JSON).

No rewriting happens inside the git repository; git worktree metadata is
regenerated rather than rewritten (§8).

### 7.3 Manifest diff and the fast-forward rule

The destination answers a manifest with, per entry: `absent`, `present-same`
(final path exists with the same hash), `staged-same`, `present-different`,
or `ff-candidate`. `present-different` on any entry stops the job at
preflight (exit 3) with the list — **unless** the entry is the session's own
transcript (or a sidecar of the same session) and the existing destination
file is a byte-prefix of the incoming one (`ff-candidate`): a fast-forward
of the same session, which is allowed and logged. `--force` extends the
exception to the non-prefix case for the same session id only; it never
allows overwriting unrelated files.

**Amendment (Plan 02):** for `.jsonl` transcript/sidecar files, "byte-prefix"
above is a record-wise JSON-equality prefix (`session.IsRecordPrefix`), not a
raw byte-prefix — rewrite normalisation (path rewriting during Send) can
change a rewritten line's exact byte encoding without changing its decoded
JSON value, so a plain byte comparison would wrongly reject a legitimate
fast-forward.

### 7.4 Streaming

Only `absent`/`present-different(ff)`/`staged-mismatch` entries are sent, as
one tar stream, gzip-compressed, over a dedicated ssh channel, in manifest
order (session files first, so a session is usable as early as possible;
then worktree, then the repository). The receiver writes each entry to
`staging/<sid>/<n>.part`, verifies size and SHA-256, then renames to its
staging name. A lost connection loses at most the entry in flight; the next
`manifest-diff` skips everything already verified in staging. Staging lives
under `$XDG_DATA_HOME/claude-teleport/staging/<sid>/` on the destination
and is removed by `abandon`, or after step 10.

### 7.5 Install

For each entry, in order: if the final path is absent → rename from staging
(directories created `0700` for session files, preserving mode for repo
files); if `present-same` → drop the staged copy; if `ff-candidate` →
rename over it (the only overwrite); otherwise stop. Then the merges:
`sessions-index.json` (add or replace the entry for `<sid>`, rewritten
paths, `fileMtime` from the installed file), `history.jsonl` (append the
session's lines not already present, matched on `timestamp`+`sessionId`),
`~/.claude.json` (`projects[<dest cwd>]` added if absent; the file is
rewritten with a temp-file + rename after a backup copy
`~/.claude.json.claude-teleport.bak` is made — the *only* global file the
tool ever writes, and only to add a key).

## 8. Git

Inventory (go-git + direct reads): the session cwd's repository root, its
common dir, whether it is a linked worktree, branch, HEAD, upstream,
`git status`-equivalent dirty state (index vs HEAD, worktree vs index,
untracked, ignored-but-present is *not* transferred unless `--include-ignored`),
submodules (their presence is reported; a dirty submodule is a preflight
refusal). Terms: `M` = main repository directory (parent of the common
`.git`), `W` = the worktree directory the session runs in (`W == M` when
the session is in the main checkout).

Cases, decided at preflight and written into the plan:

- **`M` absent on the destination.** Transfer `M` (all of it, minus other
  linked worktrees under `.worktrees/` and their `.git/worktrees/<n>`
  metadata, minus `--exclude`), then `W` including `M/.git/worktrees/<our
  name>/` (this carries the worktree's `HEAD`, `index`, `ORIG_HEAD`, so
  staged state is exact). On the destination, `git-attach` rewrites the two
  absolute-path files git keeps for a linked worktree — `W/.git`
  (`gitdir: <M>/.git/worktrees/<n>`) and `M/.git/worktrees/<n>/gitdir`
  (`<W>/.git`) — for the destination paths. This is precisely what `git
  worktree repair` does.
- **`M` present on the destination.** Verify it is the same repository
  (identical root commit; refuse otherwise). The destination advertises its
  ref tips; the source walks from the session branch tip (and HEAD, if
  detached) stopping at objects reachable from those tips and encodes one
  packfile of the missing objects (go-git `packfile.Encoder`); it is a
  manifest entry like any other. `git-attach` indexes the pack into `M`'s
  object store, then: if the branch is absent → create it at the tip; if
  present and an ancestor of the tip → fast-forward; else refuse (exit 3 at
  preflight, since tips are known then). If `W != M`: refuse if `W` exists
  or the branch is already checked out in another worktree; otherwise
  create the linked worktree (metadata written directly, index populated
  from the tip via go-git checkout), then apply the dirty state: transferred
  `index` replaces the fresh one, modified and untracked files from the
  manifest are written (they can only land on the checkout we just
  created). If `W == M`: require the destination checkout to be clean and
  on the same branch; fast-forward; write the dirty state; never delete.
- **Not a git repository.** The cwd is transferred as plain files (category
  `worktree`), no attach step.

Every decision above is shown by `inspect`/`--dry-run` before anything moves.

## 9. tmux

Source facts come from the registry `tmux` field (`session:@win.%pane`)
and one control-mode query: session name, `session_group`, window index and
name, `automatic-rename`, pane title, `pane_current_path`, socket path.

Destination server discovery, in order: a server on a socket with the
source's socket *name* (`-L main`); the default socket; if exactly one
server socket exists under `/tmp/tmux-<uid>/` (or `$TMUX_TMPDIR`), that one;
otherwise fail at preflight with the list found. **Never start a server.**

Window placement: group name `G` = source `session_group` if non-empty else
`session_name`. If a destination session belongs to group `G` (or is named
`G`), use the group's base session; otherwise `new-session -d -s G -c
<cwd>` (a session on an existing server, allowed). Then `new-window -t G:
-n <name> -c <cwd>`; if the source window had `automatic-rename off`, set it
off. The pane title is set by Claude itself once running. Scrollback is
replayed by the placeholder's `--saved-output` before it execs Claude, so
the pane looks as it did on the source.

Starting Claude: type ` claude-teleport placeholder --resume <sid>
--saved-output <capture> --now` (`--now` = no confirmation wait: print the
capture, `chdir` to the launch cwd, `exec claude --resume <sid>`).

End states (`--state`, default `auto` = same as the source):

| Requested | Source state | Destination result |
|---|---|---|
| `running` | any | Claude running in the new window (after confirmation) |
| `suspended` | any | confirm as above, then `/exit`, confirm gone, type go-tmux-saver's `claude-resume <sid> --saved-output <capture>` if `claude-resume` exists there, else the built-in placeholder (no `--now`) |
| `idle` | any | confirm as above, then `/exit`, confirm gone, kill the window we created (only if we created it and nothing else runs in it) |
| `auto` | running / suspended / idle | the same state |

A suspended source pane is recognised by its foreground command being
`claude-resume <uuid>` (go-tmux-saver) or `claude-teleport placeholder
--resume <uuid>`; exit is then a no-op on the source (the placeholder is
left, it already resumes locally).

**No tmux on the destination** (or `--no-tmux`): only `idle` is possible.
Confirmation then runs Claude in a pty allocated on the ssh session
(`RequestPty` + `claude --resume <sid>` in the launch cwd), watches the
registry and output exactly as in §6.2, then writes `/exit\r` to the pty
and confirms exit. Any other requested state fails at preflight naming the
available options.

## 10. Configuration drift

`claudecfg.Inventory` on a host collects: Claude version (registry
`version` for a running session, else `claude --version` — the one
subprocess besides tmux, run only at preflight); `settings.json` →
`hooks`, `permissions` (`defaultMode`, `allow`, `deny`), `env`,
`enabledPlugins`, `model`, `effortLevel`; `~/.claude.json` → top-level
`mcpServers`, `projects[cwd]` → `mcpServers`, `enabledMcpjsonServers`,
`disabledMcpjsonServers`, `allowedTools`, `hasTrustDialogAccepted`;
`plugins/installed_plugins.json` → name@marketplace → version, and for each
installed plugin whether it ships `hooks/hooks.json` (hash) and `.mcp.json`
(hash); tree hashes of `CLAUDE.md`, `agents/`, `skills/`, `commands/`;
`keybindings.json` hash.

`claudecfg.Usage` from the transcript (main + sub-agent files): MCP servers
(`tool_use` names `mcp__<server>__<tool>`), skills (`Skill` tool `skill`
argument and `attributionSkill` fields), plugins (`attributionPlugin`),
sub-agent types (`Agent` tool `subagent_type`), permission mode records.

Classification:

| Difference | Class |
|---|---|
| any hook difference (settings or an installed plugin's `hooks/hooks.json`) | **block** |
| a *used* MCP server absent on the destination or configured differently | **block** |
| a *used* plugin absent or at a different version | **block** |
| a *used* skill or sub-agent type absent | **block** |
| `permissions.deny` differs; `defaultMode` differs | **block** |
| Claude version differs | warn |
| model, effortLevel, unused MCP servers/plugins/skills, `allowedTools`, `env`, keybindings, CLAUDE.md/agents/commands trees | warn |
| destination lacks the project entry | info (it is carried over) |

`block` refuses at preflight (exit 3) with the full table; `--allow-config-drift`
downgrades every block to warn. `compare-config` prints the same table
without a session (everything is then "used").

## 11. Placeholder

`claude-teleport placeholder --resume <sid> [--saved-output F] [--now]
[--teleported-to HOST] [--teleported-at TS]` is a port of go-tmux-saver's
`claude-resume` (banner with project, branch, label, last-active; Enter
resumes, Ctrl-C leaves a shell; non-tty stdin resumes immediately) plus:
`--saved-output` prints the capture above the banner, `--now` skips the
wait, `--teleported-to` adds a line saying where the session went and that
resuming here forks it. The argv contains `--resume <uuid>` so go-tmux-saver's
process resolver classifies the pane as a Claude pane and saves/restores it
as such.

## 12. Testing

**Unit tests** (every package, `go test -race ./...`), with fixtures under
`internal/<pkg>/testdata/` captured from Claude Code 2.1.247 and sanitised:
`/home/alice`, `bob@laptop.example`, freshly generated uuids, no real
prompts. Notable tests: manifest never contains a forbidden path; rewrite
round-trips unknown fields and numbers; fast-forward detection; munging;
drift classification table; jump-chain resolution (an in-process ssh
server from x/crypto/ssh with a fake "DNS" that only the jump can resolve);
freezer thaws on runner death; every step's precondition re-verification;
journal continue after each step.

**`test/fakeclaude`**: a Go binary installed as `claude` in tests, reproducing
the observable on-disk behaviour: writes/updates `sessions/<pid>.json`
(including `tmux` from `$TMUX_PANE`, `status`, `procStart`), appends transcript
records with `cwd`/`sessionId`, honours `--resume`, `--session-id`, `-p`,
`--version`, `/exit`, Ctrl-C, and can be told (env) to fail like a
logged-out Claude. With it, the whole orchestrator runs in-process in unit
tests against two `CLAUDE_CONFIG_DIR`s and two fake homes, with the "remote"
endpoint being a local endpoint behind the same interface.

**`internal/fakeapi`**: an HTTP server implementing enough of the Anthropic
Messages API for Claude Code to run without credentials
(`ANTHROPIC_BASE_URL`, `ANTHROPIC_API_KEY=dummy`, telemetry/updates disabled):
`POST /v1/messages` (streaming SSE and non-streaming) returning canned text,
`/v1/messages/count_tokens`, `/v1/models`, `/api/hello`, and 404-with-JSON
for anything else, logging every request body. The exact set of endpoints
Claude Code needs at startup is discovered by a spike task in the plan and
pinned by a test that runs the real binary against it.

**Docker integration** (`test/integration`, `docker compose`): three
containers — `source`, `jump`, `dest` — with sshd, tmux, git; keys generated
per run; `dest` only attached to a private network shared with `jump`, so
`--via jump` is required and `dest` resolves only on `jump`. Layers:

1. *fakeclaude* (every PR, fast): `--to`, `--from`, worktree and main-checkout
   sessions, `M` present/absent, suspended, idle, `!`-mode with a real
   `SIGSTOP`ped parent, different `$HOME`, re-teleport/fast-forward,
   divergence refusal, drift refusal and override, killed runner
   mid-transfer → continue, dropped ssh → continue, killed runner never
   leaves the source stopped, `abandon --delete-destination-files` only
   removes what the manifest lists.
2. *real Claude Code* (main and weekly, matrix `{2.1.247, latest}` installed
   via the native installer at that version): a session is created with
   `claude -p --session-id <sid> "…"` against `fakeapi`, teleported to `dest`
   through `jump`, resumed there with `claude -p --resume <sid> "…"`; the test
   asserts the resumed request body received by `fakeapi` contains the first
   session's messages, and that a tmux-hosted resume on `dest` produces a
   registry entry with the expected `tmux` field. `latest` failing is a real
   failure (upstream format drift) — not allowed-to-fail.

## 13. Repository, CI, release

- Apache-2.0; module `github.com/mithro/go-claude-teleport`; binary
  `claude-teleport`; `.worktrees/` for all branches; small commits; PRs into
  `main` (merge commits only, branch protected, tag ruleset `vXX.ZZZ`,
  `v0.0` on the root commit).
- `.github/workflows/test.yml`: vet, `go test -race ./...`, packaging helper
  tests, docker integration layer 1; layer 2 on `main` pushes and a weekly
  schedule.
- `.github/workflows/release.yml`, `nfpm.yaml`, `packaging/version.py`: as
  go-tmux-saver — next `vX.Y` tag on every push to `main`, static amd64 +
  arm64 binaries, `.deb`s, GitHub Release, signed apt repository on GitHub
  Pages at `https://mith.ro/go-claude-teleport/` (signing gated on
  `APT_GPG_PRIVATE_KEY`; Pages must be enabled by the repository owner).
- README: full usage, every flag, the state machine, what moves and what
  never moves, drift policy, the `!`-mode explanation, failure recovery
  (`status`/`continue`/`abandon`), the `claude --teleport` disambiguation,
  and only example hostnames/paths.

## 14. Security and privacy

- Forbidden paths (§7.1) are enforced in code and tests.
- The tool never reads token/credential values; `~/.claude.json` is parsed
  generically and only the named keys are inspected.
- Host keys are verified against `known_hosts`; unknown hosts are an error
  unless the user opts in per invocation.
- Job journals and logs contain paths and hostnames (the user's own); they
  live under `0700` directories.
- Documentation, fixtures and tests use invented hosts (`laptop.example`,
  `jump.example`, `big-storage.example`) and homes (`/home/alice`); nothing
  from the author's real fleet.

## 15. Assumptions recorded during brainstorming

- The user is the same account on both machines (different usernames are
  handled by the `$HOME` rewrite, but the destination must already be logged
  in to Claude).
- Claude Code is installed and authenticated on the destination; the tool
  will not install or log in.
- go-tmux-saver may or may not be installed on either end; its
  `claude-resume` is used only when present and only for the `suspended`
  end state.
- tmux ≥ 3.3 on both ends when tmux is used (control mode and
  `capture-pane -J`).
- Transcripts can be tens of MB; sub-agent directories hundreds of files;
  repositories GBs — streaming, not in-memory, everywhere.
