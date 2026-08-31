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
