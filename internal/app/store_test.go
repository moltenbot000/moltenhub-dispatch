package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelectFailureReviewerUsesFirstFlaggedAgent(t *testing.T) {
	t.Parallel()

	state := AppState{
		ConnectedAgents: []ConnectedAgent{
			{ID: "worker-a"},
			{ID: "reviewer-a", FailureReviewer: true},
			{ID: "reviewer-b", FailureReviewer: true},
		},
	}

	reviewer, ok := SelectFailureReviewer(state)
	if !ok {
		t.Fatal("expected a failure reviewer")
	}
	if reviewer.ID != "reviewer-a" {
		t.Fatalf("expected first flagged reviewer, got %q", reviewer.ID)
	}
}

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

func TestDefaultSettingsUsesMoltenhubDataDir(t *testing.T) {
	t.Parallel()

	settings := DefaultSettings()
	if got, want := settings.DataDir, defaultDataDir; got != want {
		t.Fatalf("default data dir = %q, want %q", got, want)
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

func TestResolveStorePathMigratesLegacyDefaultConfigIntoMoltenhubDir(t *testing.T) {
	wd := t.TempDir()
	legacyDir := filepath.Join(wd, legacyDefaultDataDir)
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("create legacy dir: %v", err)
	}
	legacyPath := filepath.Join(legacyDir, "config.json")
	legacyData := []byte(`{"settings":{"hub_region":"na"}}`)
	if err := os.WriteFile(legacyPath, legacyData, 0o644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		if chdirErr := os.Chdir(cwd); chdirErr != nil {
			t.Fatalf("restore cwd: %v", chdirErr)
		}
	}()
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}

	path, err := ResolveStorePath(defaultDataDir)
	if err != nil {
		t.Fatalf("resolve store path: %v", err)
	}
	if want := filepath.Join(defaultDataDir, "config.json"); path != want {
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
		t.Fatalf("expected legacy default config to be migrated away, stat err=%v", err)
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
	if got, want := state.Session.APIBase, "https://na.hub.molten.bot/v1"; got != want {
		t.Fatalf("api_base = %q, want %q", got, want)
	}
	if got, want := state.Session.BaseURL, "https://na.hub.molten.bot/v1"; got != want {
		t.Fatalf("base_url = %q, want %q", got, want)
	}
}
