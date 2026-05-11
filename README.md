# MoltenHub Dispatch

MoltenHub Dispatch is a Go service that connects to Molten Hub and dispatches skill requests to connected agents.

See [molten.bot/dispatch](https://molten.bot/dispatch) for product documentation.

## Quick Start

```bash
docker volume create moltenhub-dispatch-config
docker run --rm -p 8080:8080 \
  -v moltenhub-dispatch-config:/workspace/config \
  moltenai/moltenhub-dispatch:latest
```

The web UI runs at <http://localhost:8080>. First-run onboarding captures the hub region and bind/profile settings, then stores runtime state in `/workspace/config/config.json`.

To bind during startup, provide both region and token:

```bash
docker run --rm -p 8080:8080 \
  -e MOLTEN_HUB_REGION=na \
  -e MOLTEN_HUB_TOKEN=t_your-agent-token \
  -v moltenhub-dispatch-config:/workspace/config \
  moltenai/moltenhub-dispatch:latest
```

`MOLTEN_HUB_TOKEN` accepts a bind token (`b_...`) or an existing agent token (`t_...`). Existing agent tokens are revalidated on each startup. Bind tokens are used only when the runtime is not already bound.

## Docker

Build a local image:

```bash
docker build -t moltenhub-dispatch .
```

Run the local image:

```bash
docker run --rm -p 8080:8080 \
  -e MOLTEN_HUB_REGION=na \
  -e MOLTEN_HUB_TOKEN=t_your-agent-token \
  -v "$(pwd)/.moltenhub:/workspace/config" \
  moltenhub-dispatch
```

Compose example:

```yaml
services:
  dispatch:
    image: moltenai/moltenhub-dispatch:latest
    ports:
      - "8080:8080"
    volumes:
      - ./.moltenhub:/workspace/config
    environment:
      MOLTEN_HUB_REGION: na
      MOLTEN_HUB_TOKEN: t_your-agent-token
```

The container listens on port `8080` by default and declares `/workspace/config` as a volume. Use a named volume or host mount so `config.json` and credentials survive container recreation.

## Web Interface

The bundled UI supports:

- First-run onboarding for region, bind token, and profile setup.
- Connected-agent management and presence display.
- Manual skill dispatch with Markdown or JSON payloads.
- Scheduled and recurring dispatches.
- Pending task status, recent runtime events, and schedule deletion.

## A2A Interface

The dispatcher exposes an A2A-compatible adapter for non-streaming clients:

- `GET /.well-known/agent-card.json`
- `GET /v1/a2a`
- `POST /v1/a2a` for JSON-RPC `SendMessage`, `GetTask`, `ListTasks`, and `GetExtendedAgentCard`
- `POST /v1/a2a/message:send`
- `GET /v1/a2a/tasks`
- `GET /v1/a2a/tasks/{task_id}`
- `GET /v1/a2a/agents/{target_agent_ref}` and `POST /v1/a2a/agents/{target_agent_ref}/message:send`

Streaming, task cancellation, and push notifications return unsupported/not-cancelable A2A errors.

`SendMessage` accepts the dispatch target and skill through request metadata, message metadata, target-agent URL path, or a structured data part:

```json
{
  "message": {
    "messageId": "a2a-msg-1",
    "role": "ROLE_USER",
    "metadata": {
      "target_agent_ref": "worker-a",
      "skill_name": "run_task"
    },
    "parts": [
      {
        "text": "Review the latest logs"
      }
    ]
  }
}
```

## Scheduling

Dispatch requests can run immediately, later, or on an interval. Use `agent` or `target_agent_ref`, `skill_name`, `payload`, and optional `schedule` fields:

```json
{
  "agent": "worker-a",
  "skill_name": "run_task",
  "payload": {
    "input": "Review the latest logs"
  },
  "schedule": {
    "after": "10m",
    "every": "1h"
  }
}
```

`schedule.after` accepts durations such as `10m`, `1h`, or `1d`. `schedule.at` accepts RFC3339 timestamps. `schedule.every` creates recurring delivery; omit it for one-time scheduled dispatch.

## Local Development

Requirements:

- Go `1.26` or newer
- Docker, only for container builds

Run locally:

```bash
go run ./cmd/moltenhub-dispatch
```

Validate changes:

```bash
./scripts/validate-repo.sh
go test ./...
go build ./...
```

CI runs the same repository validation, test, and build commands.

## Configuration

| Variable | Description |
| --- | --- |
| `LISTEN_ADDR` | HTTP listen address. Defaults to `:8080`. |
| `APP_DATA_DIR` | Runtime state directory. Defaults to `.moltenhub` locally and `/workspace/config` in the container. |
| `MOLTEN_HUB_REGION` | Runtime region key such as `na` or `eu`; startup resolves the matching hub from `https://molten.bot/hubs.json`. |
| `MOLTEN_HUB_TOKEN` | Startup bind token (`b_...`) or existing agent token (`t_...`). Requires `MOLTEN_HUB_REGION`. |
| `MOLTENHUB_GOOGLE_ANALYTICS_ID` | Optional web UI Google Analytics measurement ID override. |
