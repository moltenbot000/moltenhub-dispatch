package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

type fakeHubClient struct {
	bindResponse        hub.BindResponse
	bindRequests        []hub.BindRequest
	updateMetadataCalls []hub.UpdateMetadataRequest
	publishCalls        []hub.PublishRequest
	offlineCalls        []hub.OfflineRequest
	baseURLCalls        []string
	currentBaseURL      string
	expectedMetadataURL string
	expectedPullURL     string
	expectedOfflineURL  string
	pullMessage         hub.PullResponse
	pullOK              bool
	pullErr             error
	publishErr          error
}

func (f *fakeHubClient) BindAgent(_ context.Context, req hub.BindRequest) (hub.BindResponse, error) {
	f.bindRequests = append(f.bindRequests, req)
	return f.bindResponse, nil
}

func (f *fakeHubClient) UpdateMetadata(_ context.Context, _ string, req hub.UpdateMetadataRequest) (map[string]any, error) {
	if f.expectedMetadataURL != "" && f.currentBaseURL != f.expectedMetadataURL {
		return nil, &hub.APIError{
			StatusCode: 401,
			Code:       "unauthorized",
			Message:    "missing or invalid bearer token",
		}
	}
	f.updateMetadataCalls = append(f.updateMetadataCalls, req)
	return map[string]any{"status": "ok"}, nil
}

func (f *fakeHubClient) GetCapabilities(_ context.Context, _ string) (map[string]any, error) {
	return map[string]any{"advertised_skills": []any{}}, nil
}

func (f *fakeHubClient) PublishOpenClaw(_ context.Context, _ string, req hub.PublishRequest) (hub.PublishResponse, error) {
	f.publishCalls = append(f.publishCalls, req)
	if f.publishErr != nil {
		return hub.PublishResponse{}, f.publishErr
	}
	return hub.PublishResponse{MessageID: "message-1"}, nil
}

func (f *fakeHubClient) PullOpenClaw(_ context.Context, _ string, _ time.Duration) (hub.PullResponse, bool, error) {
	if f.expectedPullURL != "" && f.currentBaseURL != f.expectedPullURL {
		return hub.PullResponse{}, false, &hub.APIError{
			StatusCode: 401,
			Code:       "unauthorized",
			Message:    "missing or invalid bearer token",
		}
	}
	return f.pullMessage, f.pullOK, f.pullErr
}

func (f *fakeHubClient) AckOpenClaw(_ context.Context, _ string, _ string) error {
	return nil
}

func (f *fakeHubClient) NackOpenClaw(_ context.Context, _ string, _ string) error {
	return nil
}

func (f *fakeHubClient) MarkOffline(_ context.Context, _ string, req hub.OfflineRequest) error {
	if f.expectedOfflineURL != "" && f.currentBaseURL != f.expectedOfflineURL {
		return &hub.APIError{
			StatusCode: 401,
			Code:       "unauthorized",
			Message:    "missing or invalid bearer token",
		}
	}
	f.offlineCalls = append(f.offlineCalls, req)
	return nil
}

func (f *fakeHubClient) SetBaseURL(baseURL string) {
	f.currentBaseURL = baseURL
	f.baseURLCalls = append(f.baseURLCalls, baseURL)
}

func TestBindAndRegisterAdvertisesDispatchSkills(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.bindResponse = hub.BindResponse{
		AgentToken: "agent-token",
		AgentUUID:  "agent-uuid",
		AgentURI:   "molten://dispatch/agent",
		Handle:     "dispatch-agent",
		APIBase:    "https://na.hub.molten.bot",
	}

	err := service.BindAndRegister(context.Background(), BindProfile{
		HubRegion:       HubRegionNA,
		BindToken:       "bind-token",
		Handle:          "dispatch-agent",
		ProfileMarkdown: "Dispatches skill requests to connected agents.",
		LLM:             "openai/gpt-5.4",
		Harness:         "moltenhub-dispatch@test",
	})
	if err != nil {
		t.Fatalf("bind and register: %v", err)
	}

	if len(fake.updateMetadataCalls) != 1 {
		t.Fatalf("expected metadata update, got %d", len(fake.updateMetadataCalls))
	}
	skills, ok := fake.updateMetadataCalls[0].Metadata["skills"].([]map[string]string)
	if !ok {
		t.Fatalf("unexpected skills type: %T", fake.updateMetadataCalls[0].Metadata["skills"])
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 advertised skills, got %d", len(skills))
	}

	state := service.store.Snapshot()
	if state.Session.AgentToken != "agent-token" {
		t.Fatalf("expected persisted token, got %q", state.Session.AgentToken)
	}
	if state.Settings.HubRegion != HubRegionNA {
		t.Fatalf("expected hub region %q, got %q", HubRegionNA, state.Settings.HubRegion)
	}
	if len(fake.bindRequests) != 1 || fake.bindRequests[0].HubURL != "https://na.hub.molten.bot" {
		t.Fatalf("expected bind request against na runtime, got %#v", fake.bindRequests)
	}
}

func TestBindAndRegisterUsesCanonicalAPIBaseForMetadata(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.bindResponse = hub.BindResponse{
		AgentToken: "agent-token",
		AgentUUID:  "agent-uuid",
		AgentURI:   "molten://dispatch/agent",
		Handle:     "dispatch-agent",
		APIBase:    "https://runtime.na.hub.molten.bot",
	}
	fake.expectedMetadataURL = fake.bindResponse.APIBase

	err := service.BindAndRegister(context.Background(), BindProfile{
		HubRegion:       HubRegionNA,
		BindToken:       "bind-token",
		Handle:          "dispatch-agent",
		ProfileMarkdown: "Dispatches skill requests to connected agents.",
	})
	if err != nil {
		t.Fatalf("bind and register: %v", err)
	}

	if len(fake.baseURLCalls) < 3 {
		t.Fatalf("expected base URL to switch from hub URL to api_base, got %#v", fake.baseURLCalls)
	}
	if fake.baseURLCalls[0] != "https://na.hub.molten.bot" {
		t.Fatalf("expected initial client base URL to use the selected hub runtime, got %#v", fake.baseURLCalls)
	}
	if fake.baseURLCalls[1] != "https://na.hub.molten.bot" {
		t.Fatalf("expected bind request against runtime hub URL, got %#v", fake.baseURLCalls)
	}
	if fake.baseURLCalls[2] != fake.bindResponse.APIBase {
		t.Fatalf("expected metadata to use api_base, got %#v", fake.baseURLCalls)
	}
}

func TestBindAndRegisterPersistsSelectedRuntime(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.bindResponse = hub.BindResponse{
		AgentToken: "agent-token",
		AgentUUID:  "agent-uuid",
		AgentURI:   "molten://dispatch/agent",
		Handle:     "dispatch-agent",
		APIBase:    "https://eu.hub.molten.bot",
	}

	err := service.BindAndRegister(context.Background(), BindProfile{
		HubRegion:       HubRegionEU,
		BindToken:       "bind-token",
		Handle:          "dispatch-agent",
		ProfileMarkdown: "Dispatches skill requests to connected agents.",
	})
	if err != nil {
		t.Fatalf("bind and register: %v", err)
	}

	state := service.store.Snapshot()
	if state.Settings.HubRegion != HubRegionEU {
		t.Fatalf("expected hub region %q, got %q", HubRegionEU, state.Settings.HubRegion)
	}
	if state.Settings.HubURL != "https://eu.hub.molten.bot" {
		t.Fatalf("expected eu hub url, got %q", state.Settings.HubURL)
	}
	if state.Session.HubURL != "https://eu.hub.molten.bot" {
		t.Fatalf("expected session eu hub url, got %q", state.Session.HubURL)
	}
	if len(fake.bindRequests) != 1 || fake.bindRequests[0].HubURL != "https://eu.hub.molten.bot" {
		t.Fatalf("expected bind request against eu runtime, got %#v", fake.bindRequests)
	}
}

func TestHandleDownstreamFailureSendsDetailedFailureAndQueuesFollowUp(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "molten://dispatch/self"
		state.ConnectedAgents = []ConnectedAgent{
			{
				ID:              "reviewer",
				Name:            "reviewer",
				AgentUUID:       "reviewer-uuid",
				FailureReviewer: true,
			},
		}
		state.PendingTasks = []PendingTask{
			{
				ID:                "task-1",
				ChildRequestID:    "child-req",
				OriginalSkillName: "run_task",
				CallerAgentUUID:   "caller-uuid",
				CallerRequestID:   "parent-req",
				Repo:              "/tmp/repo",
				LogPath:           filepath.Join(service.settings.DataDir, "logs", "task-1.log"),
				CreatedAt:         time.Now().Add(-time.Minute),
				ExpiresAt:         time.Now().Add(time.Minute),
				DispatchPayload: map[string]any{
					"repo":      "/tmp/repo",
					"log_paths": []string{"/tmp/original.log", "/tmp/original.log"},
					"input":     "Issue an offline to moltenbot hub",
				},
			},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	err = os.WriteFile(filepath.Join(service.settings.DataDir, "logs", "task-1.log"), []byte("boom"), 0o644)
	if err != nil {
		t.Fatalf("write log: %v", err)
	}

	message := hub.PullResponse{
		DeliveryID:    "delivery-1",
		FromAgentUUID: "worker-uuid",
		OpenClawMessage: hub.OpenClawMessage{
			Type:        "skill_result",
			SkillName:   "run_task",
			RequestID:   "child-req",
			ReplyTo:     "parent-req",
			OK:          boolPtr(false),
			Error:       "task execution failed",
			ErrorDetail: map[string]any{"stderr": "panic: boom"},
			Payload:     map[string]any{"status": "failed"},
		},
	}

	if err := service.handleInboundMessage(context.Background(), message); err != nil {
		t.Fatalf("handle inbound message: %v", err)
	}

	if len(fake.publishCalls) != 2 {
		t.Fatalf("expected caller failure + follow-up publish, got %d", len(fake.publishCalls))
	}

	failureMessage := fake.publishCalls[0].Message
	if failureMessage.Type != "skill_result" {
		t.Fatalf("unexpected caller message type: %s", failureMessage.Type)
	}
	if failureMessage.Error == "" {
		t.Fatal("expected detailed failure error")
	}
	failurePayload, ok := failureMessage.Payload.(map[string]any)
	if !ok {
		t.Fatalf("unexpected caller failure payload type: %T", failureMessage.Payload)
	}
	if got := failurePayload["log_paths"].([]string); len(got) != 2 || got[0] != "/tmp/original.log" {
		t.Fatalf("unexpected caller failure log paths: %#v", failurePayload["log_paths"])
	}

	followUpMessage := fake.publishCalls[1].Message
	if followUpMessage.SkillName != failureReviewSkillName {
		t.Fatalf("unexpected follow-up skill: %s", followUpMessage.SkillName)
	}

	state := service.store.Snapshot()
	if len(state.FollowUpTasks) != 1 {
		t.Fatalf("expected 1 follow-up task, got %d", len(state.FollowUpTasks))
	}
	if len(state.PendingTasks) != 0 {
		t.Fatalf("expected task to be cleared, got %d pending", len(state.PendingTasks))
	}
	if got := state.FollowUpTasks[0].RunConfig.Repos; len(got) != 1 || got[0] != "/tmp/repo" {
		t.Fatalf("unexpected run config repos: %#v", got)
	}
	if got := state.FollowUpTasks[0].LogPaths; len(got) != 2 || got[0] != "/tmp/original.log" || got[1] != filepath.Join(service.settings.DataDir, "logs", "task-1.log") {
		t.Fatalf("unexpected follow-up log paths: %#v", got)
	}
	if got := state.FollowUpTasks[0].OriginalRequest["input"]; got != "Issue an offline to moltenbot hub" {
		t.Fatalf("unexpected follow-up original request: %#v", state.FollowUpTasks[0].OriginalRequest)
	}

	payload, ok := followUpMessage.Payload.(map[string]any)
	if !ok {
		t.Fatalf("unexpected follow-up payload type: %T", followUpMessage.Payload)
	}
	if got := payload["log_paths"].([]string); len(got) != 2 || got[0] != "/tmp/original.log" {
		t.Fatalf("unexpected published follow-up log paths: %#v", payload["log_paths"])
	}
	originalRequest, ok := payload["original_request"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected original_request payload type: %T", payload["original_request"])
	}
	if originalRequest["skill_name"] != "run_task" {
		t.Fatalf("unexpected original_request skill: %#v", originalRequest)
	}
	if originalRequest["repo"] != "/tmp/repo" {
		t.Fatalf("unexpected original_request repo: %#v", originalRequest)
	}
}

func TestNewServiceUsesPersistedAPIBaseForRuntimeCalls(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	settings := DefaultSettings()
	settings.DataDir = dir
	store, err := NewStore(filepath.Join(dir, "state.json"), settings)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	err = store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.APIBase = "https://runtime.na.hub.molten.bot"
		state.Settings.HubURL = "https://na.hub.molten.bot"
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	fake := &fakeHubClient{
		expectedPullURL:    "https://runtime.na.hub.molten.bot",
		expectedOfflineURL: "https://runtime.na.hub.molten.bot",
	}
	service := NewService(store, fake)

	if err := service.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll once: %v", err)
	}
	if err := service.MarkOffline(context.Background(), "shutdown"); err != nil {
		t.Fatalf("mark offline: %v", err)
	}
	if len(fake.offlineCalls) != 1 {
		t.Fatalf("expected one offline call, got %d", len(fake.offlineCalls))
	}
	if len(fake.baseURLCalls) != 1 || fake.baseURLCalls[0] != "https://runtime.na.hub.molten.bot" {
		t.Fatalf("expected service to initialize client with persisted api_base, got %#v", fake.baseURLCalls)
	}
}

func newTestService(t *testing.T) (*Service, *fakeHubClient) {
	t.Helper()

	dir := t.TempDir()
	settings := DefaultSettings()
	settings.DataDir = dir
	store, err := NewStore(filepath.Join(dir, "state.json"), settings)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	fake := &fakeHubClient{}
	service := NewService(store, fake)
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatalf("create logs dir: %v", err)
	}
	return service, fake
}
