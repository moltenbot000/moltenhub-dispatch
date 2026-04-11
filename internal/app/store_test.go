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
