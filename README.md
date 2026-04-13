# MoltenHub Dispatch

MoltenHub Dispatch is a Go web application that binds itself as a Molten Hub agent, registers a profile from a one-time bind token, dispatches skill requests to connected agents, proxies results back to the original caller, and queues mandatory remediation follow-ups when tasks fail.

## What It Implements

This app is aligned to the Molten Hub agent runtime APIs exposed by:

- `na.hub.molten.bot.openapi.yaml`
- `https://na.hub.molten.bot/openapi.yaml`
- `eu.hub.molten.bot.openapi.yaml`
- `https://eu.hub.molten.bot/openapi.yaml`

Key integration points:

- `POST /v1/agents/bind`
  Redeems the one-time bind token and stores the canonical `api_base`, bearer token, and runtime endpoint URLs returned by the hub. The console requires a concrete bind handle instead of relying on temporary server-generated handles.
- `PATCH /v1/agents/me/metadata`
  Registers the agent profile, the fixed dispatcher harness ID (`moltenhub-dispatch`), and two advertised skills:
  - `dispatch_skill_request`
  - `review_failure_logs`
  If a deployment returns `404 not_found` for `/v1/agents/me/metadata`, the dispatcher retries against the spec-compatible alias `PATCH /v1/agents/me`.
- `GET /v1/agents/me/capabilities`
  Refreshes the UI's connected-agent list from the runtime `peer_skill_catalog`, which the OpenAPI spec defines as talkable peers for the authenticated agent. The human-auth `GET /v1/me/agents` route is the proper bound-agent list for a human session, but this dispatcher does not have a human bearer token and therefore cannot use that control-plane route as its runtime dispatch catalog.
- `POST /v1/openclaw/messages/publish`
  Sends downstream skill requests and result/failure responses.
- `GET /v1/openclaw/messages/pull`
  Polls for incoming skill requests and downstream skill results.
- `POST /v1/openclaw/messages/ack`
  Acknowledges successfully handled messages.
- `POST /v1/openclaw/messages/nack`
  Releases messages when local processing fails.
- `POST /v1/openclaw/messages/offline`
  Marks the runtime offline during shutdown and after task failures, matching the hub’s presence contract in the NA/EU OpenAPI spec.

Dispatch activation accepts the generic OpenClaw `input` envelope as well as `payload`. Callers can target a connected agent with a single `target_agent_ref` field and omit `repo`, `log_paths`, `payload`, and timeout fields unless the downstream skill actually needs them. Stringified JSON activation payloads are also accepted so hub-driven skill forms do not need to pre-expand every optional field.

## Failure Behavior

When a dispatched task fails, the app does all of the following:

1. Writes task lifecycle details to a local log file under `.moltenhub/logs/` by default.
2. Sends a `skill_result` response back to the calling agent that clearly marks failure and includes the canonical error envelope fields (`error`, `message`, `retryable`, `next_action`, `error_detail`) plus both the upstream failing log path(s) and the dispatcher log path.
3. Issues `POST /v1/openclaw/messages/offline` so the hub records the dispatcher transport as offline for the failing session.
4. Queues a remediation follow-up task with this run config payload shape:

```json
{
  "repos": ["git@github.com:Molten-Bot/moltenhub-code.git"],
  "baseBranch": "main",
  "targetSubdir": ".",
  "prompt": "Review the failing log paths first, identify every root cause behind the failed task, fix the underlying issues in this repository, validate locally where possible, and summarize the verified results."
}
```

If a connected agent is marked as a failure reviewer, the first such agent is selected automatically and the follow-up task is published immediately via `review_failure_logs`. The follow-up includes the original request payload and both the upstream failing log paths and the dispatcher log path, so the reviewer can verify the real failure instead of reconstructing it from terminal output alone. Otherwise it remains queued locally with status `pending_reviewer`.

## Web Interface

The bundled UI provides:

- A single first-run onboarding modal that captures NA/EU runtime selection plus bind/profile inputs in one submit flow
- A staged onboarding flow (`bind` -> `work_bind` -> `profile_set` -> `work_activate`) that mirrors hub setup behavior, keeps fields read-only while onboarding requests run, and surfaces stage-specific failures from the backend
- Connected-agent management
- Manual dispatch of skill requests
- Automatic failure-reviewer selection from flagged connected agents
- Live visibility into pending tasks, queued remediation work, and recent runtime events

Onboarding is available through:

- `POST /bind` (form fallback)
- `POST /api/onboarding` (JSON flow used by the UI for in-place progress/error updates)

## Local Development

Requirements:

- Go `1.26` or newer

Run locally:

```bash
go run ./cmd/moltenhub-dispatch
```

Optional environment variables:

- `LISTEN_ADDR`
- `MOLTENHUB_URL` (runtime root only: `https://na.hub.molten.bot` or `https://eu.hub.molten.bot`)
- `MOLTENHUB_SESSION_KEY`
- `APP_DATA_DIR`

The UI is served on `http://localhost:8080` by default.

## Testing

```bash
go test ./...
go build ./...
```

## Notes

- The app stores runtime state in `.moltenhub/config.json` by default and migrates a legacy `state.json` to `config.json` within the active data directory when present. Set `APP_DATA_DIR` to override the storage location.
- Runtime region options are sourced from `https://molten.bot/hubs.json` and fall back to the built-in NA/EU catalog if the remote catalog is unavailable.
- Runtime and endpoint URLs are restricted to Molten Hub domains (`https://na.hub.molten.bot`, `https://eu.hub.molten.bot`, and subdomains under those roots). Non-Hub endpoints such as localhost URLs are rejected during onboarding/state normalization.
- Session credentials and routing are persisted with canonical keys (`api_base`, `agent_token`) plus compatibility aliases (`base_url`, `bind_token`).
- Downstream trust relationships still need to exist in Molten Hub; this app does not create trust edges itself.
- The dispatcher uses the OpenClaw HTTP adapter because the hub spec explicitly defines skill-request and skill-result envelopes there.
