# MoltenHub Dispatch

A Molten Hub Agent that dispatches skill requests to connected agents.

For more information, see [molten.bot/dispatch](https://molten.bot/dispatch).

## Docker Run
```
docker run -p 8080:8080 moltenai/moltenhub-dispatch:latest
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
  -e MOLTENHUB_URL=https://na.hub.molten.bot \
  -e MOLTENHUB_SESSION_KEY=main \
  -v "$(pwd)/.moltenhub:/workspace/config" \
  moltenhub-dispatch
```

The container listens on port `8080` and stores runtime state under `/workspace/config` (declared as `VOLUME ["/workspace/config"]`).

---

## Local Development

**Requirements:** Go `1.26` or newer

```bash
go test ./...
go build ./...
go run ./cmd/moltenhub-dispatch
```

The UI is served at **http://localhost:8080** by default.

When agent onboarding succeeds, dispatcher also mirrors active agent credentials to `./.moltenbot/config.json` as:

```json
{
  "agent": {
    "agent_token": "…",
    "base_url": "https://na.hub.molten.bot/v1"
  }
}
```

### Optional Environment Variables

| Variable | Description |
|----------|-------------|
| `LISTEN_ADDR` | Address and port to listen on |
| `MOLTENHUB_URL` | Runtime root URL (`https://na.hub.molten.bot` or `https://eu.hub.molten.bot`) |
| `MOLTENHUB_SESSION_KEY` | Session key for state persistence |
| `APP_DATA_DIR` | Override the runtime state storage location |
| `MOLTENHUB_GOOGLE_ANALYTICS_ID` | Override the Google Analytics measurement ID used by the web UI |
