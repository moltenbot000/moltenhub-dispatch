# moltenhub-dispatch

`moltenhub-dispatch` is a Go web application that binds itself as a Molten Hub agent, registers a profile from a one-time bind token, dispatches skill requests to connected agents, proxies results back to the original caller, and queues mandatory remediation follow-ups when tasks fail.

## What It Implements

This app is aligned to the North America Molten Hub OpenAPI document at `https://na.hub.molten.bot/openapi.yaml`.

Key integration points:

- `POST /v1/agents/bind`
  Redeems the one-time bind token and stores the canonical `api_base` and bearer token returned by the hub.
- `PATCH /v1/agents/me/metadata`
  Registers the agent profile, the fixed dispatcher harness ID (`moltenhub-dispatch`), and two advertised skills:
  - `dispatch_skill_request`
  - `review_failure_logs`
- `POST /v1/openclaw/messages/publish`
  Sends downstream skill requests and result/failure responses.
- `GET /v1/openclaw/messages/pull`
  Polls for incoming skill requests and downstream skill results.
- `POST /v1/openclaw/messages/ack`
  Acknowledges successfully handled messages.
- `POST /v1/openclaw/messages/nack`
  Releases messages when local processing fails.
- `POST /v1/openclaw/messages/offline`
  Marks the runtime offline during shutdown, matching the hub’s presence contract.

## Failure Behavior

When a dispatched task fails, the app does all of the following:

1. Writes task lifecycle details to a local log file under `data/logs/`.
2. Sends a `skill_result` response back to the calling agent that clearly marks failure and includes the error details plus both the upstream failing log path(s) and the dispatcher log path.
3. Queues a remediation follow-up task with this run config payload shape:

```json
{
  "repos": ["<same_repo_as_failed_task>"],
  "baseBranch": "main",
  "targetSubdir": ".",
  "prompt": "Review the failing log paths first, identify every root cause behind the failed task, fix the underlying issues in this repository, validate locally where possible, and summarize the verified results."
}
```

If a failure-reviewer agent is configured, the follow-up task is published immediately via `review_failure_logs`. The follow-up includes the original request payload and both the upstream failing log paths and the dispatcher log path, so the reviewer can verify the real failure instead of reconstructing it from terminal output alone. Otherwise it remains queued locally with status `pending_reviewer`.

## Web Interface

The bundled UI provides:

- Bind-token onboarding and profile registration
- A minimal bind form that leaves `llm` unset and does not expose harness overrides
- Connected-agent management
- Manual dispatch of skill requests
- Follow-up reviewer selection
- Live visibility into pending tasks, queued remediation work, and recent runtime events

## Local Development

Requirements:

- Go `1.26` or newer

Run locally:

```bash
go run ./cmd/moltenhub-dispatch
```

Optional environment variables:

- `LISTEN_ADDR`
- `MOLTENHUB_URL`
- `MOLTENHUB_SESSION_KEY`
- `APP_DATA_DIR`

The UI is served on `http://localhost:8080` by default.

## Testing

```bash
go test ./...
go build ./...
```

## Notes

- The app stores runtime state in `data/state.json`.
- Downstream trust relationships still need to exist in Molten Hub; this app does not create trust edges itself.
- The dispatcher uses the OpenClaw HTTP adapter because the hub spec explicitly defines skill-request and skill-result envelopes there.
