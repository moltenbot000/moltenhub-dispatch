package app

import (
	"context"
	"strings"
	"testing"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

func TestBindFromEnvIfNeededBindsNewAgentToken(t *testing.T) {
	t.Setenv(moltenHubTokenEnvVar, "b_bind-123")
	t.Setenv(moltenHubRegionEnvVar, HubRegionEU)

	store, err := NewStore(t.TempDir()+"/config.json", DefaultSettings())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	fake := &fakeHubClient{
		bindResponse: hub.BindResponse{
			AgentToken: "agent-token",
			AgentUUID:  "agent-uuid",
			AgentURI:   "molten://agents/dispatch",
			Handle:     "dispatch-agent",
			APIBase:    "https://na.hub.molten.bot/v1",
		},
	}
	service := NewService(store, fake)

	if err := service.BindFromEnvIfNeeded(context.Background()); err != nil {
		t.Fatalf("bind from env: %v", err)
	}

	if len(fake.bindRequests) != 1 {
		t.Fatalf("bind requests = %d, want 1", len(fake.bindRequests))
	}
	if got := fake.bindRequests[0].BindToken; got != "b_bind-123" {
		t.Fatalf("bind token = %q, want %q", got, "b_bind-123")
	}
	if got := fake.bindRequests[0].HubURL; got != "https://eu.hub.molten.bot" {
		t.Fatalf("hub url = %q, want %q", got, "https://eu.hub.molten.bot")
	}

	state := service.Snapshot()
	if got := state.Session.AgentToken; got != "agent-token" {
		t.Fatalf("session agent token = %q, want %q", got, "agent-token")
	}
	if got := state.Settings.HubRegion; got != HubRegionEU {
		t.Fatalf("settings hub region = %q, want %q", got, HubRegionEU)
	}
	if got := state.Flash.Message; got != "Agent bound from "+moltenHubTokenEnvVar+"." {
		t.Fatalf("flash message = %q", got)
	}
}

func TestBindFromEnvIfNeededSkipsWhenSessionAlreadyBound(t *testing.T) {
	t.Setenv(moltenHubTokenEnvVar, "b_bind-123")
	t.Setenv(moltenHubRegionEnvVar, HubRegionNA)

	store, err := NewStore(t.TempDir()+"/config.json", DefaultSettings())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := store.Update(func(state *AppState) error {
		state.Session.AgentToken = "persisted-token"
		return nil
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	fake := &fakeHubClient{}
	service := NewService(store, fake)

	if err := service.BindFromEnvIfNeeded(context.Background()); err != nil {
		t.Fatalf("bind from env: %v", err)
	}
	if len(fake.bindRequests) != 0 {
		t.Fatalf("bind requests = %d, want 0", len(fake.bindRequests))
	}
}

func TestBindFromEnvIfNeededReconnectsExistingAgentTokenEvenWhenSessionAlreadyBound(t *testing.T) {
	t.Setenv(moltenHubTokenEnvVar, "t_env-agent-123")
	t.Setenv(moltenHubRegionEnvVar, HubRegionEU)

	store, err := NewStore(t.TempDir()+"/config.json", DefaultSettings())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := store.Update(func(state *AppState) error {
		state.Settings.HubRegion = HubRegionNA
		state.Settings.HubURL = hubURLForRegion(HubRegionNA)
		state.Session.AgentToken = "persisted-token"
		state.Session.BindToken = "persisted-token"
		state.Session.Handle = "persisted-agent"
		return nil
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	fake := &fakeHubClient{
		capabilitiesResponse: map[string]any{
			"handle":        "env-agent",
			"display_name":  "Env Agent",
			"profile_bio":   "Connected from env",
			"advertised_skills": []any{},
		},
	}
	service := NewService(store, fake)

	if err := service.BindFromEnvIfNeeded(context.Background()); err != nil {
		t.Fatalf("bind from env: %v", err)
	}

	if len(fake.bindRequests) != 0 {
		t.Fatalf("bind requests = %d, want 0", len(fake.bindRequests))
	}
	if fake.capabilitiesCalls < 2 {
		t.Fatalf("capabilities calls = %d, want at least 2", fake.capabilitiesCalls)
	}

	state := service.Snapshot()
	if got := state.Session.AgentToken; got != "t_env-agent-123" {
		t.Fatalf("session agent token = %q, want %q", got, "t_env-agent-123")
	}
	if got := state.Settings.HubRegion; got != HubRegionEU {
		t.Fatalf("settings hub region = %q, want %q", got, HubRegionEU)
	}
	if got := state.Flash.Message; got != "Existing agent connected from "+moltenHubTokenEnvVar+"." {
		t.Fatalf("flash message = %q", got)
	}
}

func TestBindFromEnvIfNeededReportsFailure(t *testing.T) {
	t.Setenv(moltenHubTokenEnvVar, "t_agent-123")
	t.Setenv(moltenHubRegionEnvVar, HubRegionNA)

	store, err := NewStore(t.TempDir()+"/config.json", DefaultSettings())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	fake := &fakeHubClient{
		capabilitiesErr: &hub.APIError{
			StatusCode: 401,
			Code:       "unauthorized",
			Message:    "missing or invalid bearer token",
		},
	}
	service := NewService(store, fake)

	err = service.BindFromEnvIfNeeded(context.Background())
	if err == nil {
		t.Fatal("expected bind error")
	}
	if !strings.Contains(err.Error(), "automatic hub binding from "+moltenHubTokenEnvVar+" failed") {
		t.Fatalf("unexpected error: %v", err)
	}

	state := service.Snapshot()
	if !strings.Contains(state.Flash.Message, "automatic hub binding from "+moltenHubTokenEnvVar+" failed") {
		t.Fatalf("flash message = %q", state.Flash.Message)
	}
	if len(state.RecentEvents) == 0 || state.RecentEvents[0].Title != "Automatic bind failed" {
		t.Fatalf("recent events = %#v", state.RecentEvents)
	}
}

func TestBindFromEnvIfNeededRequiresRegion(t *testing.T) {
	t.Setenv(moltenHubTokenEnvVar, "b_bind-123")

	store, err := NewStore(t.TempDir()+"/config.json", DefaultSettings())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	fake := &fakeHubClient{}
	service := NewService(store, fake)

	err = service.BindFromEnvIfNeeded(context.Background())
	if err == nil {
		t.Fatal("expected bind error")
	}
	if !strings.Contains(err.Error(), moltenHubRegionEnvVar+" is required when "+moltenHubTokenEnvVar+" is set") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.bindRequests) != 0 {
		t.Fatalf("bind requests = %d, want 0", len(fake.bindRequests))
	}

	state := service.Snapshot()
	if state.Flash.Message == "" {
		t.Fatal("expected flash message")
	}
	if len(state.RecentEvents) == 0 || state.RecentEvents[0].Title != "Automatic bind failed" {
		t.Fatalf("recent events = %#v", state.RecentEvents)
	}
}
