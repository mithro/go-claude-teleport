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
environment as above) now observes **two** requests per `claude -p` /
`claude -p --resume` invocation, reproducible across repeated runs against a
fresh `CLAUDE_CONFIG_DIR`:

| # | Method | Path |
|---|---|---|
| 1 | GET | `/api/hello` |
| 2 | POST | `/v1/messages?beta=true` |

`GET /api/hello` now precedes `POST /v1/messages` on every invocation (both
the initial `-p` call and the `--resume` call), each against its own fresh
`CLAUDE_CONFIG_DIR`/cwd pair reused between the two calls in the same test.
No handler changes were needed — `/api/hello` was already served (200,
`{"ok":true,...}`) per the "kept (cheap)" note above; the fake server was
already prepared for this. `internal/fakeapi/realclaude_test.go` now expects
2 requests after the first call and 4 total after `--resume`, asserting
`/api/hello` then `/v1/messages` in each pair; the prior-context assertion
(the last `/v1/messages` body must contain both "say hello" and "what did I
say?") is unchanged.
