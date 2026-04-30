package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveStorePathReturnsConfigJSONByDefault(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path, err := ResolveStorePath(dir)
	if err != nil {
		t.Fatalf("resolve store path: %v", err)
	}
	if want := filepath.Join(dir, "config.json"); path != want {
		t.Fatalf("store path = %q, want %q", path, want)
	}
}

func TestDefaultSettingsUsesMoltenhubHiddenDataDir(t *testing.T) {
	t.Parallel()

	settings := DefaultSettings()
	if got, want := settings.DataDir, defaultDataDir; got != want {
		t.Fatalf("default data dir = %q, want %q", got, want)
	}
}

func TestDefaultSettingsUsesGoogleAnalyticsMeasurementID(t *testing.T) {
	t.Parallel()

	settings := DefaultSettings()
	if got, want := settings.GoogleAnalyticsMeasurementID, defaultGoogleAnalyticsMeasureID; got != want {
		t.Fatalf("google analytics measurement id = %q, want %q", got, want)
	}
}

func TestDefaultSettingsUsesRegionEnvOverride(t *testing.T) {
	t.Setenv(moltenHubRegionEnvVar, HubRegionEU)

	settings := DefaultSettings()
	if got, want := settings.HubRegion, HubRegionEU; got != want {
		t.Fatalf("hub_region = %q, want %q", got, want)
	}
	if got, want := settings.HubURL, "https://eu.hub.molten.bot"; got != want {
		t.Fatalf("hub_url = %q, want %q", got, want)
	}
}

func TestDefaultSettingsUsesFixedMainSessionKey(t *testing.T) {
	t.Setenv("MOLTENHUB_SESSION_KEY", "ignored")

	settings := DefaultSettings()
	if got, want := settings.SessionKey, "main"; got != want {
		t.Fatalf("session_key = %q, want %q", got, want)
	}
}

func TestNewStorePrefersGoogleAnalyticsEnvOverrideOverPersistedSetting(t *testing.T) {
	t.Setenv("MOLTENHUB_GOOGLE_ANALYTICS_ID", "G-OVERRIDE123")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := []byte(`{
  "settings": {
    "hub_region": "na",
    "hub_url": "https://na.hub.molten.bot",
    "google_analytics_measurement_id": "G-PERSISTED999"
  }
}`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewStore(path, DefaultSettings())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	state := store.Snapshot()
	if got, want := state.Settings.GoogleAnalyticsMeasurementID, "G-OVERRIDE123"; got != want {
		t.Fatalf("google analytics measurement id = %q, want %q", got, want)
	}
}

func TestResolveStorePathMigratesLegacyStateJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "state.json")
	legacyData := []byte(`{"settings":{"hub_region":"eu"}}`)
	if err := os.WriteFile(legacyPath, legacyData, 0o644); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}

	path, err := ResolveStorePath(dir)
	if err != nil {
		t.Fatalf("resolve store path: %v", err)
	}
	if want := filepath.Join(dir, "config.json"); path != want {
		t.Fatalf("store path = %q, want %q", path, want)
	}

	configData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migrated config: %v", err)
	}
	if string(configData) != string(legacyData) {
		t.Fatalf("migrated config mismatch: got %q want %q", string(configData), string(legacyData))
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("expected legacy state to be migrated away, stat err=%v", err)
	}
}

func TestNewStoreNormalizesLegacySessionAliases(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := []byte(`{
  "settings": {
    "hub_region": "na",
    "hub_url": "https://na.hub.molten.bot"
  },
  "session": {
    "bind_token": "legacy-agent-token",
    "base_url": "https://na.hub.molten.bot/v1"
  }
}`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewStore(path, DefaultSettings())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	state := store.Snapshot()
	if got, want := state.Session.AgentToken, "legacy-agent-token"; got != want {
		t.Fatalf("agent_token = %q, want %q", got, want)
	}
	if got, want := state.Session.BindToken, "legacy-agent-token"; got != want {
		t.Fatalf("bind_token = %q, want %q", got, want)
	}
	if got := state.Session.APIBase; got != "" {
		t.Fatalf("expected api_base to stay in-memory-only, got %q", got)
	}
	if got := state.Session.BaseURL; got != "" {
		t.Fatalf("expected base_url to stay in-memory-only, got %q", got)
	}
}

func TestStorePersistsHubURLAgentTokenAndScheduledMessages(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	nextRunAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)

	store, err := NewStore(path, DefaultSettings())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := store.Update(func(state *AppState) error {
		state.Settings.HubRegion = HubRegionEU
		state.Settings.HubURL = "https://eu.hub.molten.bot"
		state.Session.AgentToken = "agent-token"
		state.Session.MetadataURL = "https://runtime.eu.hub.molten.bot/profile"
		state.Session.APIBase = "https://runtime.eu.hub.molten.bot"
		state.Connection.Error = "temporary disconnect"
		state.PendingTasks = []PendingTask{{ID: "task-1"}}
		state.ScheduledMessages = []ScheduledMessage{
			{
				ID:                     "schedule-1",
				Status:                 ScheduledMessageStatusActive,
				ParentRequestID:        "parent-req",
				OriginalSkillName:      "run_task",
				TargetAgentRef:         "worker-a",
				TargetAgentUUID:        "worker-uuid",
				TargetAgentURI:         "molten://agent/worker-a",
				TargetAgentDisplayName: "Worker A",
				NextRunAt:              nextRunAt,
				Frequency:              15 * time.Minute,
				DispatchPayload:        map[string]any{"input": "scheduled work"},
				DispatchPayloadFormat:  "json",
			},
		}
		state.RecentEvents = []RuntimeEvent{{Title: "Task failed"}}
		return nil
	}); err != nil {
		t.Fatalf("update store: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var persisted map[string]any
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("decode config: %v", err)
	}

	settings, ok := persisted["settings"].(map[string]any)
	if !ok {
		t.Fatalf("settings payload missing: %#v", persisted)
	}
	if got, want := settings["hub_url"], "https://eu.hub.molten.bot"; got != want {
		t.Fatalf("hub_url = %#v, want %q", got, want)
	}
	if _, ok := settings["hub_region"]; ok {
		t.Fatalf("did not expect hub_region in persisted config: %#v", settings)
	}

	session, ok := persisted["session"].(map[string]any)
	if !ok {
		t.Fatalf("session payload missing: %#v", persisted)
	}
	if got, want := session["agent_token"], "agent-token"; got != want {
		t.Fatalf("agent_token = %#v, want %q", got, want)
	}
	if _, ok := session["bind_token"]; ok {
		t.Fatalf("did not expect bind_token alias in persisted config: %#v", session)
	}

	scheduledMessages, ok := persisted["scheduled_messages"].([]any)
	if !ok || len(scheduledMessages) != 1 {
		t.Fatalf("expected one persisted scheduled message: %#v", persisted["scheduled_messages"])
	}
	scheduled, ok := scheduledMessages[0].(map[string]any)
	if !ok {
		t.Fatalf("scheduled message payload missing: %#v", scheduledMessages[0])
	}
	if got, want := scheduled["id"], "schedule-1"; got != want {
		t.Fatalf("scheduled id = %#v, want %q", got, want)
	}
	if got, want := scheduled["target_agent_uuid"], "worker-uuid"; got != want {
		t.Fatalf("scheduled target_agent_uuid = %#v, want %q", got, want)
	}
	if payload, ok := scheduled["dispatch_payload"].(map[string]any); !ok || payload["input"] != "scheduled work" {
		t.Fatalf("scheduled dispatch payload = %#v", scheduled["dispatch_payload"])
	}

	for _, forbidden := range []string{
		"connection",
		"pending_tasks",
		"recent_events",
		"connected_agents",
		"flash",
		"listen_addr",
		"data_dir",
		"task_timeout",
		"poll_interval",
		"metadata_url",
		"api_base",
		"base_url",
	} {
		if strings.Contains(string(raw), "\""+forbidden+"\"") {
			t.Fatalf("did not expect %q in persisted config: %s", forbidden, string(raw))
		}
	}
}

func TestNewStoreLoadsPersistedScheduledMessages(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := []byte(`{
  "settings": {
    "hub_url": "https://na.hub.molten.bot"
  },
  "session": {
    "agent_token": "agent-token"
  },
  "scheduled_messages": [
    {
      "id": "schedule-1",
      "status": "active",
      "parent_request_id": "parent-req",
      "original_skill_name": "run_task",
      "target_agent_ref": "worker-a",
      "target_agent_uuid": "worker-uuid",
      "target_agent_uri": "molten://agent/worker-a",
      "next_run_at": "2030-01-02T03:04:05Z",
      "frequency": 900000000000,
      "dispatch_payload": {
        "input": "scheduled work"
      },
      "dispatch_payload_format": "json"
    }
  ]
}`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewStore(path, DefaultSettings())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	state := store.Snapshot()
	if len(state.ScheduledMessages) != 1 {
		t.Fatalf("scheduled messages = %d, want 1", len(state.ScheduledMessages))
	}
	scheduled := state.ScheduledMessages[0]
	if got, want := scheduled.ID, "schedule-1"; got != want {
		t.Fatalf("id = %q, want %q", got, want)
	}
	if got, want := scheduled.TargetAgentUUID, "worker-uuid"; got != want {
		t.Fatalf("target_agent_uuid = %q, want %q", got, want)
	}
	if got, want := scheduled.Frequency, 15*time.Minute; got != want {
		t.Fatalf("frequency = %v, want %v", got, want)
	}
	if got := scheduled.DispatchPayload["input"]; got != "scheduled work" {
		t.Fatalf("dispatch payload = %#v", scheduled.DispatchPayload)
	}
}

func TestNewStorePrefersAPPDataDirOverrideOverPersistedSetting(t *testing.T) {
	t.Setenv("APP_DATA_DIR", "/workspace/config")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := []byte(`{
  "settings": {
    "hub_region": "na",
    "hub_url": "https://na.hub.molten.bot",
    "data_dir": "/data"
  }
}`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewStore(path, DefaultSettings())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	state := store.Snapshot()
	if got, want := state.Settings.DataDir, "/workspace/config"; got != want {
		t.Fatalf("data_dir = %q, want %q", got, want)
	}
}

func TestNewStorePrefersRegionEnvOverrideOverPersistedRuntime(t *testing.T) {
	t.Setenv(moltenHubRegionEnvVar, HubRegionEU)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := []byte(`{
  "settings": {
    "hub_region": "na",
    "hub_url": "https://na.hub.molten.bot"
  }
}`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewStore(path, DefaultSettings())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	state := store.Snapshot()
	if got, want := state.Settings.HubRegion, HubRegionEU; got != want {
		t.Fatalf("hub_region = %q, want %q", got, want)
	}
	if got, want := state.Settings.HubURL, "https://eu.hub.molten.bot"; got != want {
		t.Fatalf("hub_url = %q, want %q", got, want)
	}
}

func TestNewStoreRejectsNonHubSessionEndpoints(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := []byte(`{
  "settings": {
    "hub_region": "na",
    "hub_url": "http://127.0.0.1:37581"
  },
  "session": {
    "base_url": "http://127.0.0.1:37581/v1",
    "metadata_url": "http://127.0.0.1:37581/v1/agents/me/metadata"
  },
  "connection": {
    "base_url": "http://127.0.0.1:37581/v1",
    "domain": "127.0.0.1:37581"
  }
}`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store, err := NewStore(path, DefaultSettings())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	state := store.Snapshot()
	if got, want := state.Settings.HubURL, "https://na.hub.molten.bot"; got != want {
		t.Fatalf("hub_url = %q, want %q", got, want)
	}
	if state.Session.APIBase != "" {
		t.Fatalf("expected invalid api_base to be cleared, got %q", state.Session.APIBase)
	}
	if state.Session.MetadataURL != "" {
		t.Fatalf("expected invalid metadata_url to be cleared, got %q", state.Session.MetadataURL)
	}
	if got, want := state.Connection.BaseURL, "https://na.hub.molten.bot"; got != want {
		t.Fatalf("connection base_url = %q, want %q", got, want)
	}
	if got, want := state.Connection.Domain, "na.hub.molten.bot"; got != want {
		t.Fatalf("connection domain = %q, want %q", got, want)
	}
}
