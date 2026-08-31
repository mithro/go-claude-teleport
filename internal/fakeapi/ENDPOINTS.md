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

## Update 2026-08-31, Claude Code 2.1.251 (task 18, `go test -tags realclaude`)

Re-running the same spike (implementer for task 18, same machine, same
version) sometimes observed a `GET /api/hello` before `POST /v1/messages` on
each `claude -p` / `claude -p --resume` invocation, where the 2026-08-29 spike
observed none. A controller re-probe from the same binary reproduced **both**
the one-request and the two-request behaviour depending on ambient
environment (which env vars were left unset vs. inherited from the calling
shell). So the count and order of auxiliary requests (`/api/hello` and
potentially others in future Claude Code versions) is **not** a stable
upstream fact and must not be pinned by tests.

The stable facts, confirmed across both variants:

- `claude -p ...` makes at least one `POST /v1/messages?beta=true` whose body
  contains `"stream":true` and the prompt text.
- `claude -p --resume <sid> ...` makes at least one further
  `POST /v1/messages` whose body contains both the original prompt and the
  new one (prior context is carried across resume).
- No other endpoint's presence or absence, or its position relative to
  `/v1/messages`, is guaranteed.

Consequences for this package and its tests:

- `/api/hello`, `/v1/models` and `/v1/messages/count_tokens` remain served
  (cheap, harmless) regardless of whether a given `claude` invocation hits
  them — no handler changes were needed here.
- `internal/fakeapi/realclaude_test.go` filters recorded requests down to
  `POST /v1/messages` (by path prefix) before asserting anything, and only
  asserts on that filtered list: at least one such request per `-p` call, and
  the prior-context check on the last one after `--resume`. It logs the full
  observed path list (`t.Logf`) on every run so future drift is visible in
  `-v` output without failing the build.
- `internal/fakeapi/realclaude.go`'s `spikeEnv` drops every ambient
  `CLAUDE_CODE_*` variable by prefix (not an enumerated list), plus
  `CLAUDECODE`, `CLAUDE_PID` and `CLAUDE_EFFORT`, before appending the fixed
  spike set — this is believed to be what caused the two request-count
  variants above, though the auxiliary-request behavior itself remains
  unpinned regardless.
