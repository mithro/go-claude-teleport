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
