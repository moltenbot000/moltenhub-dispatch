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
  -e MOLTEN_HUB_REGION=na \
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

### Optional Environment Variables

| Variable | Description |
|----------|-------------|
| `LISTEN_ADDR` | Address and port to listen on |
| `MOLTEN_HUB_REGION` | Runtime region key (`na`, `eu`); dispatcher resolves matching hub domain from `https://molten.bot/hubs.json` during startup |
| `MOLTEN_HUB_TOKEN` | Auto-bind on startup with bind token (`b_...`) or validate/connect existing agent token (`t_...`). Must be paired with `MOLTEN_HUB_REGION` when used from env |
| `APP_DATA_DIR` | Override the runtime state storage location |
| `MOLTENHUB_GOOGLE_ANALYTICS_ID` | Override the Google Analytics measurement ID used by the web UI |

When `MOLTEN_HUB_TOKEN` is set, dispatcher attempts automatic onboarding during startup. Existing agent tokens from env are re-validated on each startup and replace any stale stored session. Bind tokens are only used when runtime is not already bound.
