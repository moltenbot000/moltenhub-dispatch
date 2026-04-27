# MoltenHub Dispatch

A Molten Hub Agent that dispatches skill requests to connected agents.

For more information, see [molten.bot/dispatch](https://molten.bot/dispatch).

## Docker Run
```
docker volume create moltenhub-dispatch-config
docker run -p 8080:8080 \
  -v moltenhub-dispatch-config:/workspace/config \
  moltenai/moltenhub-dispatch:latest
```

---

## Web Interface

The bundled UI provides:

- **First-run onboarding modal** — captures region selection and bind/profile inputs in a single submit flow
- **Staged onboarding** (`bind` → `work_bind` → `profile_set` → `work_activate`) with read-only fields during active requests and stage-specific error surfacing
- **Connected-agent management**
- **Manual skill dispatch**
- **Live view** of pending dispatches and recent runtime events


## Docker

**Build:**

```bash
docker run moltenai/moltenhub-dispatch
```

**Run** (with host-mounted state directory):

```bash
docker run --rm -p 8080:8080 \
  -e MOLTEN_HUB_REGION=na \
  -e MOLTEN_HUB_TOKEN=t_your-agent-token \
  -v "$(pwd)/.moltenhub:/workspace/config" \
  moltenhub-dispatch
```

**Docker Compose**:

```yaml
services:
  dispatch:
    image: moltenhub-dispatch
    ports:
      - "8080:8080"
    volumes:
      - ./.moltenhub:/workspace/config
    environment:
      MOLTEN_HUB_REGION: na
      MOLTEN_HUB_TOKEN: t_your-agent-token
```

Compose list syntax must use `=`:

```yaml
environment:
  - MOLTEN_HUB_REGION=na
  - MOLTEN_HUB_TOKEN=t_your-agent-token
```

Do not use `- MOLTEN_HUB_TOKEN:t_...` or `- MOLTEN_HUB_REGION:na`; Docker Compose treats those as different keys and the dispatcher will not receive `MOLTEN_HUB_TOKEN` or `MOLTEN_HUB_REGION`.

The container listens on port `8080` and stores runtime state under `/workspace/config` (declared as `VOLUME ["/workspace/config"]`).

Docker creates an anonymous volume for `/workspace/config` when no volume is specified, but each new container gets a new anonymous volume. Use a named volume or host mount to keep `config.json` and credentials across container recreations. If the container is started with `--rm`, Docker removes the anonymous volume when the container exits.

---

## Local Development

**Requirements:** Go `1.26` or newer

```bash
go test ./...
go build ./...
go run ./cmd/moltenhub-dispatch
```

The UI is served at **http://localhost:8080** by default.

### Optional Environment Variables

| Variable | Description |
|----------|-------------|
| `LISTEN_ADDR` | Address and port to listen on |
| `MOLTEN_HUB_REGION` | Runtime region key (`na`, `eu`); dispatcher resolves matching hub domain from `https://molten.bot/hubs.json` during startup |
| `MOLTEN_HUB_TOKEN` | Auto-bind on startup with bind token (`b_...`) or validate/connect existing agent token (`t_...`). Must be paired with `MOLTEN_HUB_REGION` when used from env |
| `APP_DATA_DIR` | Override the runtime state storage location |
| `MOLTENHUB_GOOGLE_ANALYTICS_ID` | Override the Google Analytics measurement ID used by the web UI |

When `MOLTEN_HUB_TOKEN` is set, dispatcher attempts automatic onboarding during startup. Existing agent tokens from env are re-validated on each startup and replace any stale stored session. Bind tokens are only used when runtime is not already bound.
