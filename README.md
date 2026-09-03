# claude-teleport

Move one in-progress [Claude Code](https://docs.anthropic.com/en/docs/claude-code)
session — its transcript and every file Claude needs to resume it, the git
worktree it is working in, and the tmux window it lives in — from one
machine to another over ssh, confirm it resumed there, and leave the source
in a state you can resume locally or teleport back later.

> **Not** `claude --teleport`. Anthropic's own `claude --teleport` moves a
> session between claude.ai *cloud* environments. `claude-teleport` moves a
> session between *your own machines*. The two are unrelated.

## Quick start

```
claude-teleport --to big-storage.example
claude-teleport --from laptop.example
claude-teleport --to big-storage.example --via jump.example
```

With no `<session>` argument, the session you are typing this into (or the
Claude running in the current tmux pane) is the one that moves. `--to` and
`--from` are aliases for the canonical `--teleport-to`/`--teleport-from`;
exactly one is required.

Type `! claude-teleport --to big-storage.example` at the Claude prompt to
run it from inside the session being moved (`!`-mode: the command notices
it is a child of the session it is about to move — `$CLAUDE_PID` matches
the session's own registry entry). The parent Claude is `SIGSTOP`ped for
the duration of the freeze/capture/transfer/install/git-attach/start/shape
steps and `SIGCONT`ed as soon as the destination is confirmed (or the job
fails); on success the command exits with a one-line summary and this
Claude exits too; on failure it exits non-zero with the error and this
Claude simply continues. `!`-mode only works with `--to`, and only while
the session is `running`.

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

Static binaries and `.deb` packages are attached to every
[GitHub Release](https://github.com/mithro/go-claude-teleport/releases); an
apt repository is published at `https://mith.ro/go-claude-teleport/`:

```sh
curl -fsSL https://mith.ro/go-claude-teleport/go-claude-teleport.gpg \
  | sudo tee /etc/apt/keyrings/mithro-go-claude-teleport.gpg > /dev/null
echo "deb [signed-by=/etc/apt/keyrings/mithro-go-claude-teleport.gpg] https://mith.ro/go-claude-teleport/ ./" \
  | sudo tee /etc/apt/sources.list.d/mithro-go-claude-teleport.list
sudo apt update && sudo apt install claude-teleport
```

Or build it yourself:

```sh
go install github.com/mithro/go-claude-teleport/cmd/claude-teleport@latest
```

`claude-teleport` (matching `--version`/protocol) must be installed on
**both** machines; `claude-teleport doctor <host>` checks this. Claude Code
itself must already be installed **and logged in** on the destination — the
tool never installs, configures or authenticates Claude Code, and never
reads or transfers credentials. A destination that isn't logged in makes
the teleport fail at the confirmation step (exit 5); nothing is lost, and
`claude-teleport continue <sid>` resumes once you've logged in there.

## Usage

```
claude-teleport [<session>] --to   <host> [--via <jump>]... [options]
claude-teleport [<session>] --from <host> [--via <jump>]... [options]
claude-teleport <tmux-session> <window> --to|--from <host> ...
claude-teleport continue <sid>            resume an interrupted job
claude-teleport status  <sid>             journal and manifest of a job
claude-teleport abandon <sid> [--delete-destination-files]
claude-teleport inspect [<session>] [--host <host>] [--json]
claude-teleport list [--host <host>] [--json]
claude-teleport compare-config <host> [--session <session>]
claude-teleport doctor [<host>]
claude-teleport placeholder --resume <sid> [--saved-output F] [--now] [--teleported-to H] [--teleported-at TS]
claude-teleport version
```

`--teleport-to` / `--teleport-from` are the canonical spellings; `--to` /
`--from` are aliases. Exactly one is required for a teleport.

### Choosing the session

1. No argument: the session you are in (`$CLAUDE_CODE_SESSION_ID`), else the
   Claude running in the current tmux pane, else an error listing candidates.
2. A full session uuid.
3. A unique uuid prefix (≥ 4 hex characters) or a registry name.
4. Two words `<tmux-session> <window>` (window by index or name): the
   pane's running Claude, or the placeholder it left behind.

With `--from`, selection runs on the remote machine with the same rules.

### Options

| Flag | Meaning |
|---|---|
| `--via HOST` | jump host(s), repeatable, outermost first; composes with `ProxyJump` from `~/.ssh/config` |
| `-o KEY=VALUE` | ssh option override (`User`, `Port`, `IdentityFile`, `StrictHostKeyChecking=accept-new`, …) |
| `--dest-path DIR` | put the session's cwd at DIR on the destination instead of the same path (must be absolute) |
| `--map SRC=DST` | extra absolute path prefix rewrite, repeatable |
| `--state auto\|running\|suspended\|idle` | destination end state; `auto` (default) preserves the source state |
| `--allow-config-drift` | turn blocking configuration drift into warnings |
| `--force` | allow non-fast-forward replacement of an existing copy of *this* session |
| `--tmux-socket NAME` | destination tmux socket name (default: same as the source) |
| `--no-tmux` | do not use tmux on the destination (end state must be `idle`) |
| `--exclude GLOB` | omit matching files from the repository transfer, repeatable |
| `--include-ignored` | also transfer gitignored files (see [Caveats](#caveats--known-limitations)) |
| `--dry-run` | preflight and plan only; nothing touched, nothing frozen |
| `--exit-timeout D` / `--start-timeout D` | bounded waits (defaults 30s / 90s) |
| `--config-dir DIR` | local `CLAUDE_CONFIG_DIR` override |
| `--log FILE` | additional log destination |
| `--json` | machine-readable output for `status`, `list`, `inspect`, `compare-config` |
| `-v` / `-q` | log level |

`--via`/`-o` aren't only for a teleport: `list --host`, `doctor <host>`,
`inspect --host` and `compare-config` all dial their target the same way
and accept the same `--via`/`-o` flags.

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
  otherwise diffed and reported, and left alone either way (never deleted
  by `abandon`)
- the project's entry in `~/.claude.json` (`projects["<cwd>"]`) — added if
  absent so the trust dialog and allow-list survive; never modified if present
- the git repository / worktree (see below) and a capture of the tmux pane

**Never moved**, enforced in code and tests: `.credentials.json`,
`sessions/` (the live-process registry of *other* sessions), `*.key`,
`settings.json`, `plugins/`, and `~/.claude.json` as a whole. Nothing is
ever sent to any Claude API.

Absolute paths inside the moved JSON files are rewritten when the two
machines differ, including the munged project-directory name. `$HOME` is
mapped to the destination's `$HOME` automatically when they differ; the
paths are otherwise identical by default. Unknown fields and numbers
survive the rewrite untouched.

### Path mapping

By default the session's cwd (and everything under it) lands at the same
absolute path on the destination. To put it somewhere else:

- `--dest-path DIR` — the session's own cwd only, on the destination.
- `--map SRC=DST` — any other absolute path prefix, repeatable.

`--map` entries and the automatic `$HOME`-to-`$HOME` rewrite are combined
and applied longest-prefix-first, regardless of flag order — so a `--map`
for a subtree of `$HOME` always wins over the whole-`$HOME` rewrite for the
paths it covers.

`inspect --host` and `--dry-run` print the resulting map (`Paths` section)
before anything moves.

## Git

`M` is the main repository directory, `W` the worktree the session runs in
(`W == M` for a plain checkout).

| Case | What happens |
|---|---|
| `M` absent on the destination | the whole repository is transferred (minus other linked worktrees and `--exclude`d or gitignored files); `W`'s linked-worktree metadata is repaired for the destination paths — exactly what `git worktree repair` does, e.g. `~/repo/.worktrees/x` becomes the destination's main repo `~/repo` plus a repaired worktree at `~/repo/.worktrees/x` |
| `M` present on the destination | it must be the same repository (same root commit), else refused at preflight; only the missing objects travel, as one packfile; the branch is created or fast-forwarded — **never rewound**; a linked worktree is created at `W` (refused if `W` exists or the branch is checked out elsewhere), or a plain checkout is fast-forwarded in place (refused unless clean and on the same branch); the dirty state (staged/modified/untracked) is then applied on top; uncommitted deletions are reported but never performed |
| not a git repository | the cwd is copied as plain files |

The tool uses go-git in-process; it never runs `git`. `inspect --host` and
`--dry-run` print every one of these decisions, and the caveats below,
before anything moves.

## tmux

The destination server is found by the source's socket *name*
(`--tmux-socket` overrides it), then `default`, then the only live server
under `/tmp/tmux-<uid>/`; otherwise preflight fails listing what it found.
**A server is never started.** The window goes into the source's session
group (created as `new-session -d` if absent), with the same window name
and `automatic-rename` setting. The source pane's scrollback is replayed
above Claude's banner so the pane looks as it did.

| `--state` | Destination result |
|---|---|
| `running` | Claude running in the new window, confirmed |
| `suspended` | confirmed, then `/exit`'d, then go-tmux-saver's `claude-resume` is typed into the pane if present there, else the built-in `placeholder` — Enter resumes |
| `idle` | confirmed, then `/exit`'d, then the window we created is removed |
| `auto` (default) | the same state the source session was in |

A source session already in a suspended state (its pane's foreground
command is go-tmux-saver's `claude-resume` or `claude-teleport
placeholder`) teleports its saved transcript, not a live process; exiting
it on the source is then a no-op — the placeholder it already shows keeps
working locally.

Without tmux on the destination (or `--no-tmux`), only `--state idle` is
possible: Claude is resumed under a pty on the ssh session, confirmed the
same way, then exited.

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
`claude-teleport compare-config HOST [--session <session>]` prints the same
table on its own — without `--session` everything counts as used;
`--dest-cwd` sets the working directory the *remote* side is inventoried
under (project-scoped hooks/MCP servers are keyed by cwd — pass it when the
project lives at a different path on the destination). `HOST` may also be
an absolute path to a local Claude config directory (`--dest-home` for its
home), running the whole comparison locally. Nothing from the configuration
is ever copied.

## The state machine, and what happens when it breaks

A teleport is a job keyed by the session id, journaled under
`~/.local/share/claude-teleport/jobs/<sid>/` on both machines. It runs in a
detached runner; the foreground command streams its log. Steps:

1. **preflight** — resolve, compare versions and configuration, plan git
   and tmux, check every destination path for collisions, print the plan
2. **freeze** — a running source Claude is `SIGSTOP`ped by a tiny freezer
   process that, if the runner dies for any reason, `SIGCONT`s it and hands
   its terminal back (it types `fg` into the source pane it was given at
   freeze time, and nowhere else), so a dead runner leaves a live,
   usable session behind
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

The source is only exited (step 9) once step 7 has positively confirmed the
destination session is alive and Claude has resumed — a teleport never
leaves you with neither end usable. Every step first re-checks reality
(files, processes, panes); the journal is a hint. On failure the runner
thaws the source, marks the step, and exits 1. **Nothing is ever deleted
and nothing is rolled back.** Then:

```
claude-teleport status  <sid>     # what happened, which step, the manifest
claude-teleport continue <sid>    # pick up at the first incomplete step (re-running the original command does the same)
claude-teleport abandon <sid>     # give up; --delete-destination-files removes only what this job installed and that is unchanged since
```

`abandon --delete-destination-files` refuses if the destination session is
still running there (exit the session on the destination first); it never
deletes memory files, and it deletes only manifest entries this specific
job recorded as installed, re-verifying each file's hash first, so anything
touched since is left alone.

The only existing file ever overwritten on the destination is an older copy
of the *same* session's transcript when the incoming one extends it (a
fast-forward — how teleporting back works). Anything else that already
exists with different content stops the job at preflight.

### `--dry-run` and `inspect`

`--dry-run` runs preflight only (step 1): resolve, compare, plan, print —
nothing is touched, nothing is frozen. `claude-teleport inspect [<session>]`
lists a session's state, directories and every file that would be
transferred without needing a destination; `inspect [<session>] --host
<host>` runs the exact preflight a teleport would and prints the same plan
and drift table `--dry-run` would show (or the refusal, exit 3), against a
throwaway job id that never touches this session's own journal.

`inspect --host <host>` inspects a LOCAL session and preflights it TOWARD
that host — it never inspects a session living there. To see what is on
another machine, use `claude-teleport list --host <host>` (and teleport it
here with `--from <host>` to inspect it locally).

## Caveats / known limitations

- **Staged deletions do not travel.** Only staged blobs and dirty
  working-tree files are sent; a `git rm --cached` on the source is not
  replayed on the destination.
- **`--include-ignored` is inert in existing-main mode.** It only affects a
  fresh-main transfer; when the repository already exists on the
  destination, gitignored destination content is preserved as-is and
  ignored source files are not force-sent.
- **Submodule gitlinks transfer stale.** The gitlink (commit pointer)
  moves; submodule working trees are not synced.
- **The `relativeworktrees` git extension is unsupported** and fails loudly
  at the git-attach step rather than silently mishandling the worktree.
- **`.git/info/exclude`-only cleanliness is not verified** — a working tree
  that's "clean" only because of local, untracked excludes can look dirtier
  (or cleaner) than intended on the destination.
- **Memory files are copied only if absent** on the destination; if one
  already exists with different content it is left alone and reported, and
  `abandon --delete-destination-files` never removes it either way.
- **A Claude that IS the pane's own command may not stay frozen.** The
  freeze is a `SIGSTOP` on the process group, and tmux sends `SIGCONT` to
  a stopped pane process of its own accord — so when Claude was started as
  the pane's command (`tmux new-window claude …`, no shell in between) it
  can be woken up again mid-teleport. Running Claude from a shell inside
  the pane — the normal case — is unaffected. The teleport still completes:
  the source is re-frozen only until step 9, which thaws it and asks it to
  exit.
- **`-p` (print) session confirmation is best-effort.** Non-interactive
  runs are confirmed via the registry's `status` turning `busy` after a
  turn rather than a prompt actually being reached on screen.

## Security

- `.credentials.json` and `sessions/*.key` are never read or transferred —
  the destination must already be logged in on its own.
- Configuration drift output redacts secret values; only hashes of
  hooks/plugin/CLAUDE.md content are compared and shown.
- Every path written on the destination is checked to stay under that
  session's own destination directories; wire job ids are validated before
  being used as filesystem paths.
- Nothing from either machine's Claude configuration or transcript is ever
  sent to any Claude API.
- **An unknown ssh host key is refused**, like `ssh -o
  StrictHostKeyChecking=yes`: the host must already be in `known_hosts`.
  Pass `-o StrictHostKeyChecking=accept-new` for that one invocation to
  accept and record a first-time key — it is appended to `known_hosts` in
  plain (non-hashed) form. A key that CHANGED is refused under both of
  those settings (`-o StrictHostKeyChecking=no` skips host verification
  altogether — it is honoured, and it is not a default).

## Requirements

- Linux on both machines — `/proc` is read directly (process tables, freeze/
  thaw, registry liveness), so this is a hard requirement, not a portability
  gap; tmux ≥ 3.3 where tmux is used, but tmux is optional — `--no-tmux`/
  `--state idle` needs none.
- ssh reachable with keys (agent or `~/.ssh/id_*`), and each host already
  in `known_hosts` — an unknown key is refused unless that invocation
  passes `-o StrictHostKeyChecking=accept-new`, which records it. The link
  is kept honest with keepalives — `ServerAliveInterval` 15s and
  `ServerAliveCountMax` 3 by default, unlike OpenSSH's off-by-default,
  because a teleport runs unattended and a half-open link would hang it
  instead of failing it into a continuable job. Both keywords are read
  from `-o` and from `~/.ssh/config`; `ServerAliveInterval=0` turns
  keepalives off, while `ServerAliveCountMax=0` is a hard error (it reads
  like "tolerate no misses" but would silently mean the default 3).
  `ProxyJump` is honoured; `ProxyCommand` is not — use `--via`.
- The same `claude-teleport` version on both ends (`claude-teleport doctor
  <host>` checks this, plus `claude` on `PATH`, the config directory, and
  more) and a logged-in Claude Code on the destination.

## Development

```sh
go vet ./... && go test -race ./...                                # unit tests
go test -race -tags tmuxlive ./internal/...                        # opt-in: a throwaway tmux server
test/integration/build.sh && go test -tags integration ./test/integration/ -v   # docker: source, jump, dest
python3 -m unittest discover -s packaging -p 'version_test.py'
```

The `tmuxlive`, `integration` and `realclaude` suites are all opt-in build
tags: a plain `go test ./...` never starts a tmux server, a container or a
real Claude. CI runs `tmuxlive` over the same `./internal/...` as above on
every push, and the tagged docker suites on their own schedule.

The tmux control-mode client is copied from
[go-tmux-saver](https://github.com/mithro/go-tmux-saver) (same author).
Licence: Apache-2.0.
