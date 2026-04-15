package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

type fakeHubClient struct {
	bindResponse          hub.BindResponse
	bindRequests          []hub.BindRequest
	updateMetadataCalls   []hub.UpdateMetadataRequest
	updateMetadataErr     error
	capabilitiesCalls     int
	capabilitiesResponse  map[string]any
	capabilitiesErr       error
	capabilitiesErrOnCall int
	publishCalls          []hub.PublishRequest
	offlineCalls          []hub.OfflineRequest
	baseURLCalls          []string
	runtimeEndpoints      []hub.RuntimeEndpoints
	currentBaseURL        string
	expectedMetadataURL   string
	expectedPullURL       string
	expectedOfflineURL    string
	pullMessage           hub.PullResponse
	pullOK                bool
	pullCalls             int
	pullErr               error
	pingDetail            string
	pingErr               error
	pingErrSequence       []error
	pingCalls             int
	connectErr            error
	connectSession        hub.RealtimeSession
	connectCalls          int
	publishErr            error
}

type fakeRealtimeSession struct {
	messages   []hub.PullResponse
	receiveErr error
	acked      []string
	nacked     []string
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
	if f.updateMetadataErr != nil {
		return nil, f.updateMetadataErr
	}
	f.updateMetadataCalls = append(f.updateMetadataCalls, req)
	return map[string]any{"status": "ok"}, nil
}

func (f *fakeHubClient) GetCapabilities(_ context.Context, _ string) (map[string]any, error) {
	f.capabilitiesCalls++
	if f.capabilitiesErr != nil && (f.capabilitiesErrOnCall == 0 || f.capabilitiesErrOnCall == f.capabilitiesCalls) {
		return nil, f.capabilitiesErr
	}
	if f.capabilitiesResponse != nil {
		return f.capabilitiesResponse, nil
	}
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
	f.pullCalls++
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

func (f *fakeHubClient) ConnectOpenClaw(_ context.Context, _ string, _ string) (hub.RealtimeSession, error) {
	f.connectCalls++
	if len(f.baseURLCalls) == 0 {
		f.baseURLCalls = append(f.baseURLCalls, f.currentBaseURL)
	}
	if f.connectSession != nil && f.connectErr == nil {
		return f.connectSession, nil
	}
	if f.connectErr != nil {
		return nil, f.connectErr
	}
	return &fakeRealtimeSession{}, errors.New("websocket unavailable")
}

func (f *fakeHubClient) CheckPing(_ context.Context) (string, error) {
	f.pingCalls++
	if len(f.pingErrSequence) > 0 {
		err := f.pingErrSequence[0]
		f.pingErrSequence = f.pingErrSequence[1:]
		if err != nil {
			return "", err
		}
	}
	if f.pingErr != nil {
		return "", f.pingErr
	}
	if strings.TrimSpace(f.pingDetail) != "" {
		return strings.TrimSpace(f.pingDetail), nil
	}
	return "https://na.hub.molten.bot/ping status=204", nil
}

func (f *fakeRealtimeSession) Receive(_ context.Context) (hub.PullResponse, error) {
	if len(f.messages) == 0 {
		if f.receiveErr != nil {
			return hub.PullResponse{}, f.receiveErr
		}
		return hub.PullResponse{}, context.Canceled
	}
	message := f.messages[0]
	f.messages = f.messages[1:]
	return message, nil
}

func (f *fakeRealtimeSession) Ack(_ context.Context, deliveryID string) error {
	f.acked = append(f.acked, deliveryID)
	return nil
}

func (f *fakeRealtimeSession) Nack(_ context.Context, deliveryID string) error {
	f.nacked = append(f.nacked, deliveryID)
	return nil
}

func (f *fakeRealtimeSession) Close() error {
	return nil
}

func (f *fakeHubClient) SetRuntimeEndpoints(endpoints hub.RuntimeEndpoints) {
	f.runtimeEndpoints = append(f.runtimeEndpoints, endpoints)
}

func testConnectedAgent(agentID, displayName, agentUUID string, skills ...Skill) ConnectedAgent {
	agent := ConnectedAgent{
		AgentID:   agentID,
		Handle:    agentID,
		AgentUUID: agentUUID,
		Metadata: &hub.AgentMetadata{
			DisplayName: displayName,
			Skills:      testSkillMetadata(skills...),
		},
	}
	if displayName == "" {
		agent.Metadata = &hub.AgentMetadata{
			Skills: testSkillMetadata(skills...),
		}
	}
	return agent
}

const testDispatchPrompt = "Review the Hub API integration behavior."

func testSkillMetadata(skills ...Skill) []map[string]any {
	if len(skills) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(skills))
	for _, skill := range skills {
		if strings.TrimSpace(skill.Name) == "" {
			continue
		}
		entry := map[string]any{"name": skill.Name}
		if strings.TrimSpace(skill.Description) != "" {
			entry["description"] = skill.Description
		}
		out = append(out, entry)
	}
	return out
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
	fake.bindResponse.Endpoints.Metadata = "https://runtime.na.hub.molten.bot/profile"
	fake.bindResponse.Endpoints.OpenClawPull = "https://runtime.na.hub.molten.bot/openclaw/pull"
	fake.bindResponse.Endpoints.OpenClawPush = "https://runtime.na.hub.molten.bot/openclaw/publish"
	fake.bindResponse.Endpoints.Offline = "https://runtime.na.hub.molten.bot/openclaw/offline"

	err := service.BindAndRegister(context.Background(), BindProfile{
		BindToken:       "bind-token",
		Handle:          "dispatch-agent",
		DisplayName:     "Dispatch Agent",
		Emoji:           "🤖",
		ProfileMarkdown: "Dispatches skill requests to connected agents.",
	})
	if err != nil {
		t.Fatalf("bind and register: %v", err)
	}

	if len(fake.updateMetadataCalls) != 1 {
		t.Fatalf("expected metadata update, got %d", len(fake.updateMetadataCalls))
	}
	if fake.capabilitiesCalls != 2 {
		t.Fatalf("expected credential + activation capability checks, got %d", fake.capabilitiesCalls)
	}
	skills, ok := fake.updateMetadataCalls[0].Metadata["skills"].([]Skill)
	if !ok {
		t.Fatalf("unexpected skills type: %T", fake.updateMetadataCalls[0].Metadata["skills"])
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 advertised skill, got %d", len(skills))
	}
	if skills[0].Name != dispatchSkillName {
		t.Fatalf("unexpected advertised skills: %#v", skills)
	}
	if _, ok := fake.updateMetadataCalls[0].Metadata["llm"]; ok {
		t.Fatal("expected llm to be omitted from metadata")
	}
	if got := fake.updateMetadataCalls[0].Metadata["harness"]; got != dispatcherHarness {
		t.Fatalf("unexpected harness: %#v", got)
	}
	if got := fake.updateMetadataCalls[0].Metadata["display_name"]; got != "Dispatch Agent" {
		t.Fatalf("unexpected display name: %#v", got)
	}
	if got := fake.updateMetadataCalls[0].Metadata["emoji"]; got != "🤖" {
		t.Fatalf("unexpected emoji: %#v", got)
	}

	state := service.store.Snapshot()
	if state.Session.AgentToken != "agent-token" {
		t.Fatalf("expected persisted token, got %q", state.Session.AgentToken)
	}
	if state.Session.DisplayName != "Dispatch Agent" {
		t.Fatalf("expected persisted display name, got %q", state.Session.DisplayName)
	}
	if state.Session.Emoji != "🤖" {
		t.Fatalf("expected persisted emoji, got %q", state.Session.Emoji)
	}
	if state.Settings.HubRegion != HubRegionNA {
		t.Fatalf("expected hub region %q, got %q", HubRegionNA, state.Settings.HubRegion)
	}
	if len(fake.bindRequests) != 1 || fake.bindRequests[0].HubURL != "https://na.hub.molten.bot" {
		t.Fatalf("expected bind request against na runtime, got %#v", fake.bindRequests)
	}
	if len(fake.runtimeEndpoints) < 2 {
		t.Fatalf("expected runtime endpoints to be configured from bind response, got %#v", fake.runtimeEndpoints)
	}
	lastEndpoints := fake.runtimeEndpoints[len(fake.runtimeEndpoints)-1]
	if lastEndpoints.MetadataURL != "https://runtime.na.hub.molten.bot/profile" {
		t.Fatalf("unexpected metadata endpoint: %#v", lastEndpoints)
	}
}

func TestBindAndRegisterUsesSubmittedHandle(t *testing.T) {
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
		BindToken:       "bind-token",
		Handle:          "dispatch-agent",
		DisplayName:     "Dispatch Agent",
		ProfileMarkdown: "Dispatches skill requests to connected agents.",
	})
	if err != nil {
		t.Fatalf("bind and register: %v", err)
	}

	if len(fake.bindRequests) != 1 || fake.bindRequests[0].Handle != "dispatch-agent" {
		t.Fatalf("expected submitted bind handle, got %#v", fake.bindRequests)
	}
	if len(fake.updateMetadataCalls) != 1 {
		t.Fatalf("expected metadata update, got %d", len(fake.updateMetadataCalls))
	}
	if fake.updateMetadataCalls[0].Handle != "dispatch-agent" {
		t.Fatalf("expected finalized handle, got %q", fake.updateMetadataCalls[0].Handle)
	}

	state := service.store.Snapshot()
	if state.Session.Handle != "dispatch-agent" {
		t.Fatalf("expected handle to persist, got %q", state.Session.Handle)
	}
	if !state.Session.HandleFinalized {
		t.Fatal("expected submitted handle to be finalized during bind")
	}
}

func TestBindAndRegisterSupportsTemporaryHandleWhenHandleIsOmitted(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.bindResponse = hub.BindResponse{
		AgentToken: "agent-token",
		Handle:     "tmp-agent-123",
		APIBase:    "https://na.hub.molten.bot",
	}

	err := service.BindAndRegister(context.Background(), BindProfile{
		BindToken: "bind-token",
	})
	if err != nil {
		t.Fatalf("bind and register: %v", err)
	}
	if len(fake.bindRequests) != 1 {
		t.Fatalf("expected one bind request, got %#v", fake.bindRequests)
	}
	if fake.bindRequests[0].Handle != "" {
		t.Fatalf("expected empty bind handle for temporary handle flow, got %#v", fake.bindRequests[0])
	}
	if len(fake.updateMetadataCalls) != 1 {
		t.Fatalf("expected one metadata update, got %d", len(fake.updateMetadataCalls))
	}
	if fake.updateMetadataCalls[0].Handle != "" {
		t.Fatalf("expected metadata handle omitted until finalization, got %q", fake.updateMetadataCalls[0].Handle)
	}

	state := service.store.Snapshot()
	if state.Session.Handle != "tmp-agent-123" {
		t.Fatalf("expected temporary handle to persist, got %q", state.Session.Handle)
	}
	if state.Session.HandleFinalized {
		t.Fatal("did not expect temporary handle to be finalized")
	}
}

func TestBindAndRegisterSupportsExistingAgentFlow(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.capabilitiesResponse = map[string]any{
		"agent_uuid": "agent-uuid",
		"agent_uri":  "molten://dispatch/agent",
		"handle":     "dispatch-agent",
		"metadata": map[string]any{
			"display_name": "Remote Agent",
			"emoji":        "🛰️",
		},
		"peer_skill_catalog": []any{},
	}

	err := service.BindAndRegister(context.Background(), BindProfile{
		AgentMode:       OnboardingModeExisting,
		AgentToken:      "agent-token",
		DisplayName:     "Dispatch Agent",
		Emoji:           "🤖",
		ProfileMarkdown: "Dispatches skill requests to connected agents.",
	})
	if err != nil {
		t.Fatalf("connect existing agent: %v", err)
	}

	if len(fake.bindRequests) != 0 {
		t.Fatalf("did not expect bind request for existing agent flow, got %#v", fake.bindRequests)
	}
	if fake.capabilitiesCalls != 2 {
		t.Fatalf("expected credential + activation capability checks, got %d", fake.capabilitiesCalls)
	}
	if len(fake.updateMetadataCalls) != 1 {
		t.Fatalf("expected metadata update, got %d", len(fake.updateMetadataCalls))
	}

	state := service.store.Snapshot()
	if state.Session.AgentToken != "agent-token" {
		t.Fatalf("expected persisted token, got %q", state.Session.AgentToken)
	}
	if state.Session.Handle != "dispatch-agent" {
		t.Fatalf("expected persisted handle, got %q", state.Session.Handle)
	}
	if !state.Session.HandleFinalized {
		t.Fatal("expected existing agent handle to be finalized")
	}
	if state.Session.DisplayName != "Dispatch Agent" {
		t.Fatalf("expected submitted display name to persist, got %q", state.Session.DisplayName)
	}
	if state.Settings.HubRegion != HubRegionNA {
		t.Fatalf("expected hub region %q, got %q", HubRegionNA, state.Settings.HubRegion)
	}
}

func TestBindAndRegisterExistingAgentReportsVerificationFailureStage(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.capabilitiesErr = errors.New("unauthorized")

	err := service.BindAndRegister(context.Background(), BindProfile{
		AgentMode:  OnboardingModeExisting,
		AgentToken: "agent-token",
	})
	if err == nil {
		t.Fatal("expected onboarding error")
	}
	if got, want := OnboardingStageFromError(err), OnboardingStepWorkBind; got != want {
		t.Fatalf("onboarding stage = %q, want %q", got, want)
	}
	if len(fake.bindRequests) != 0 {
		t.Fatalf("did not expect bind request for existing agent flow, got %#v", fake.bindRequests)
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
	fake.bindResponse.Endpoints.Metadata = "https://runtime.na.hub.molten.bot/profile"
	fake.expectedMetadataURL = fake.bindResponse.APIBase

	err := service.BindAndRegister(context.Background(), BindProfile{
		BindToken:       "bind-token",
		Handle:          "dispatch-agent",
		DisplayName:     "Dispatch Agent",
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
	if len(fake.runtimeEndpoints) == 0 || fake.runtimeEndpoints[len(fake.runtimeEndpoints)-1].MetadataURL != "https://runtime.na.hub.molten.bot/profile" {
		t.Fatalf("expected metadata endpoint from bind response, got %#v", fake.runtimeEndpoints)
	}
}

func TestBindAndRegisterDerivesAPIBaseFromRuntimeMetadataEndpoint(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.bindResponse = hub.BindResponse{
		AgentToken: "agent-token",
		AgentUUID:  "agent-uuid",
		AgentURI:   "molten://dispatch/agent",
		Handle:     "dispatch-agent",
	}
	fake.bindResponse.Endpoints.Metadata = "https://runtime.na.hub.molten.bot/runtime/profile"
	fake.expectedMetadataURL = "https://runtime.na.hub.molten.bot"

	err := service.BindAndRegister(context.Background(), BindProfile{
		BindToken:       "bind-token",
		Handle:          "dispatch-agent",
		DisplayName:     "Dispatch Agent",
		ProfileMarkdown: "Dispatches skill requests to connected agents.",
	})
	if err != nil {
		t.Fatalf("bind and register: %v", err)
	}

	state := service.store.Snapshot()
	if got := state.Session.APIBase; got != "https://runtime.na.hub.molten.bot" {
		t.Fatalf("expected derived api_base, got %q", got)
	}
	if got := fake.baseURLCalls[len(fake.baseURLCalls)-1]; got != "https://runtime.na.hub.molten.bot" {
		t.Fatalf("expected derived runtime api_base before metadata update, got %#v", fake.baseURLCalls)
	}
}

func TestBindAndRegisterDefaultsAPIBaseToVersionedHubEndpoint(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.bindResponse = hub.BindResponse{
		AgentToken: "agent-token",
		AgentUUID:  "agent-uuid",
		AgentURI:   "molten://dispatch/agent",
		Handle:     "dispatch-agent",
	}
	fake.expectedMetadataURL = "https://na.hub.molten.bot/v1"

	err := service.BindAndRegister(context.Background(), BindProfile{
		BindToken:       "bind-token",
		Handle:          "dispatch-agent",
		DisplayName:     "Dispatch Agent",
		ProfileMarkdown: "Dispatches skill requests to connected agents.",
	})
	if err != nil {
		t.Fatalf("bind and register: %v", err)
	}

	state := service.store.Snapshot()
	if got, want := state.Session.APIBase, "https://na.hub.molten.bot/v1"; got != want {
		t.Fatalf("api_base = %q, want %q", got, want)
	}
	if got, want := state.Session.BaseURL, "https://na.hub.molten.bot/v1"; got != want {
		t.Fatalf("base_url = %q, want %q", got, want)
	}
}

func TestBindAndRegisterRejectsUnsupportedAPIBase(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.bindResponse = hub.BindResponse{
		AgentToken: "agent-token",
		AgentUUID:  "agent-uuid",
		AgentURI:   "molten://dispatch/agent",
		Handle:     "dispatch-agent",
		APIBase:    "http://127.0.0.1:37581/v1",
	}

	err := service.BindAndRegister(context.Background(), BindProfile{
		BindToken: "bind-token",
		Handle:    "dispatch-agent",
	})
	if err == nil {
		t.Fatal("expected bind to fail for unsupported api_base")
	}
	if !strings.Contains(err.Error(), "unsupported api_base") {
		t.Fatalf("expected unsupported api_base error, got %v", err)
	}
	if len(fake.updateMetadataCalls) != 0 {
		t.Fatalf("expected metadata update to be skipped, got %d calls", len(fake.updateMetadataCalls))
	}
	state := service.store.Snapshot()
	if state.Session.AgentToken != "" {
		t.Fatalf("expected session token to stay empty, got %q", state.Session.AgentToken)
	}
}

func TestBindAndRegisterPersistsSelectedRuntime(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.UpdateSettings(func(settings *Settings) error {
		settings.HubRegion = HubRegionEU
		settings.HubURL = "https://eu.hub.molten.bot"
		return nil
	})
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	fake.bindResponse = hub.BindResponse{
		AgentToken: "agent-token",
		AgentUUID:  "agent-uuid",
		AgentURI:   "molten://dispatch/agent",
		Handle:     "dispatch-agent",
		APIBase:    "https://eu.hub.molten.bot",
	}

	err = service.BindAndRegister(context.Background(), BindProfile{
		BindToken:       "bind-token",
		Handle:          "dispatch-agent",
		DisplayName:     "Dispatch Agent",
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

func TestBindAndRegisterPersistsBoundSessionWhenMetadataUpdateFails(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.bindResponse = hub.BindResponse{
		AgentToken: "agent-token",
		AgentUUID:  "agent-uuid",
		AgentURI:   "molten://dispatch/agent",
		Handle:     "dispatch-agent",
		APIBase:    "https://na.hub.molten.bot",
	}
	fake.updateMetadataErr = errors.New("hub metadata unavailable")

	err := service.BindAndRegister(context.Background(), BindProfile{
		BindToken:       "bind-token",
		Handle:          "dispatch-agent",
		DisplayName:     "Dispatch Agent",
		Emoji:           "🤖",
		ProfileMarkdown: "Dispatches skill requests to connected agents.",
	})
	if err == nil {
		t.Fatal("expected metadata failure")
	}

	state := service.store.Snapshot()
	if state.Session.AgentToken != "agent-token" {
		t.Fatalf("expected token to persist after bind success, got %q", state.Session.AgentToken)
	}
	if state.Session.Handle != "dispatch-agent" {
		t.Fatalf("expected handle to persist after bind success, got %q", state.Session.Handle)
	}
	if state.Session.DisplayName != "Dispatch Agent" {
		t.Fatalf("expected display name to persist after bind success, got %q", state.Session.DisplayName)
	}
	if stage := OnboardingStageFromError(err); stage != OnboardingStepProfileSet {
		t.Fatalf("expected profile_set stage, got %q", stage)
	}
}

func TestBindAndRegisterReportsActivationFailureStage(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.bindResponse = hub.BindResponse{
		AgentToken: "agent-token",
		AgentUUID:  "agent-uuid",
		AgentURI:   "molten://dispatch/agent",
		Handle:     "dispatch-agent",
		APIBase:    "https://na.hub.molten.bot",
	}
	fake.capabilitiesErr = errors.New("capabilities unavailable")
	fake.capabilitiesErrOnCall = 2

	err := service.BindAndRegister(context.Background(), BindProfile{
		BindToken:       "bind-token",
		Handle:          "dispatch-agent",
		DisplayName:     "Dispatch Agent",
		ProfileMarkdown: "Dispatches skill requests to connected agents.",
	})
	if err == nil {
		t.Fatal("expected activation failure")
	}
	if stage := OnboardingStageFromError(err); stage != OnboardingStepWorkActivate {
		t.Fatalf("expected work_activate stage, got %q", stage)
	}

	state := service.store.Snapshot()
	if state.Session.AgentToken != "agent-token" {
		t.Fatalf("expected token to persist after bind success, got %q", state.Session.AgentToken)
	}
}

func TestBindAndRegisterReportsCredentialVerificationFailureStage(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.bindResponse = hub.BindResponse{
		AgentToken: "agent-token",
		AgentUUID:  "agent-uuid",
		AgentURI:   "molten://dispatch/agent",
		Handle:     "dispatch-agent",
		APIBase:    "https://na.hub.molten.bot",
	}
	fake.capabilitiesErr = errors.New("capabilities unavailable")
	fake.capabilitiesErrOnCall = 1

	err := service.BindAndRegister(context.Background(), BindProfile{
		BindToken:       "bind-token",
		Handle:          "dispatch-agent",
		DisplayName:     "Dispatch Agent",
		ProfileMarkdown: "Dispatches skill requests to connected agents.",
	})
	if err == nil {
		t.Fatal("expected credential verification failure")
	}
	if stage := OnboardingStageFromError(err); stage != OnboardingStepWorkBind {
		t.Fatalf("expected work_bind stage, got %q", stage)
	}

	state := service.store.Snapshot()
	if state.Session.AgentToken != "agent-token" {
		t.Fatalf("expected token to persist after bind success, got %q", state.Session.AgentToken)
	}
}

func TestBindAndRegisterFailsBindStageWhenBindResponseMissingToken(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.bindResponse = hub.BindResponse{
		AgentUUID: "agent-uuid",
		AgentURI:  "molten://dispatch/agent",
		Handle:    "dispatch-agent",
		APIBase:   "https://na.hub.molten.bot",
	}

	err := service.BindAndRegister(context.Background(), BindProfile{
		BindToken:       "bind-token",
		Handle:          "dispatch-agent",
		DisplayName:     "Dispatch Agent",
		ProfileMarkdown: "Dispatches skill requests to connected agents.",
	})
	if err == nil {
		t.Fatal("expected bind-stage failure for missing token")
	}
	if stage := OnboardingStageFromError(err); stage != OnboardingStepBind {
		t.Fatalf("expected bind stage, got %q", stage)
	}
	if !strings.Contains(err.Error(), "bind response missing agent token") {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.capabilitiesCalls != 0 {
		t.Fatalf("expected no capability checks without token, got %d", fake.capabilitiesCalls)
	}
}

func TestUpdateAgentProfileUpdatesMetadataAndStoredProfile(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.Handle = "dispatch-agent"
		state.Session.HandleFinalized = true
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	err = service.UpdateAgentProfile(context.Background(), AgentProfile{
		DisplayName:     "Dispatch Agent",
		Emoji:           "💯",
		ProfileMarkdown: "What this runtime is for",
	})
	if err != nil {
		t.Fatalf("update profile: %v", err)
	}

	if len(fake.updateMetadataCalls) != 1 {
		t.Fatalf("expected one metadata update, got %d", len(fake.updateMetadataCalls))
	}
	if fake.updateMetadataCalls[0].Handle != "dispatch-agent" {
		t.Fatalf("expected stored handle to be reused, got %q", fake.updateMetadataCalls[0].Handle)
	}

	state := service.store.Snapshot()
	if state.Session.DisplayName != "Dispatch Agent" {
		t.Fatalf("unexpected persisted display name: %q", state.Session.DisplayName)
	}
	if state.Session.Emoji != "💯" {
		t.Fatalf("unexpected persisted emoji: %q", state.Session.Emoji)
	}
	if state.Session.ProfileBio != "What this runtime is for" {
		t.Fatalf("unexpected persisted bio: %q", state.Session.ProfileBio)
	}
}

func TestUpdateAgentProfileRejectsHandleChangeAfterBind(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.Handle = "dispatch-agent"
		state.Session.HandleFinalized = true
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	err = service.UpdateAgentProfile(context.Background(), AgentProfile{
		Handle: "other-handle",
	})
	if err == nil {
		t.Fatal("expected immutable handle error")
	}
}

func TestUpdateAgentProfileFinalizesTemporaryHandle(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.Handle = "tmp-agent-123"
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	err = service.UpdateAgentProfile(context.Background(), AgentProfile{
		Handle:          "dispatch-agent",
		DisplayName:     "Dispatch Agent",
		ProfileMarkdown: "Finalized handle.",
	})
	if err != nil {
		t.Fatalf("update profile: %v", err)
	}

	if len(fake.updateMetadataCalls) != 1 {
		t.Fatalf("expected one metadata update, got %d", len(fake.updateMetadataCalls))
	}
	if fake.updateMetadataCalls[0].Handle != "dispatch-agent" {
		t.Fatalf("expected finalized handle in metadata update, got %q", fake.updateMetadataCalls[0].Handle)
	}

	state := service.store.Snapshot()
	if state.Session.Handle != "dispatch-agent" {
		t.Fatalf("expected finalized handle to persist, got %q", state.Session.Handle)
	}
	if !state.Session.HandleFinalized {
		t.Fatal("expected finalized handle flag to persist")
	}
}

func TestUpdateAgentProfileDoesNotResubmitUnchangedTemporaryHandle(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.Handle = "tmp-agent-123"
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	err = service.UpdateAgentProfile(context.Background(), AgentProfile{
		Handle:          "tmp-agent-123",
		DisplayName:     "Dispatch Agent",
		ProfileMarkdown: "Still temporary.",
	})
	if err != nil {
		t.Fatalf("update profile: %v", err)
	}

	if len(fake.updateMetadataCalls) != 1 {
		t.Fatalf("expected one metadata update, got %d", len(fake.updateMetadataCalls))
	}
	if fake.updateMetadataCalls[0].Handle != "" {
		t.Fatalf("expected unchanged temporary handle to be omitted, got %q", fake.updateMetadataCalls[0].Handle)
	}

	state := service.store.Snapshot()
	if state.Session.Handle != "tmp-agent-123" {
		t.Fatalf("expected temporary handle to remain unchanged, got %q", state.Session.Handle)
	}
	if state.Session.HandleFinalized {
		t.Fatal("did not expect unchanged temporary handle to become finalized")
	}
}

func TestUpdateAgentProfileUsesPersistedSessionRoutingAfterRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	settings := DefaultSettings()
	settings.DataDir = dir
	store, err := NewStore(filepath.Join(dir, "config.json"), settings)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	err = store.Update(func(state *AppState) error {
		state.Settings.HubURL = "https://na.hub.molten.bot"
		state.Session.AgentToken = "agent-token"
		state.Session.Handle = "dispatch-agent"
		state.Session.HandleFinalized = true
		state.Session.APIBase = "https://runtime.na.hub.molten.bot"
		state.Session.MetadataURL = "https://runtime.na.hub.molten.bot/profile"
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	fake := &fakeHubClient{
		currentBaseURL:      "https://na.hub.molten.bot",
		expectedMetadataURL: "https://runtime.na.hub.molten.bot",
	}
	service := NewService(store, fake)
	fake.currentBaseURL = "https://na.hub.molten.bot"

	err = service.UpdateAgentProfile(context.Background(), AgentProfile{
		DisplayName:     "Dispatch Agent",
		Emoji:           "🤖",
		ProfileMarkdown: "Updated after restart.",
	})
	if err != nil {
		t.Fatalf("update profile: %v", err)
	}

	if len(fake.updateMetadataCalls) != 1 {
		t.Fatalf("expected one metadata update, got %d", len(fake.updateMetadataCalls))
	}
	if fake.updateMetadataCalls[0].Handle != "dispatch-agent" {
		t.Fatalf("expected persisted handle to be reused, got %q", fake.updateMetadataCalls[0].Handle)
	}
	if got := fake.baseURLCalls[len(fake.baseURLCalls)-1]; got != "https://runtime.na.hub.molten.bot" {
		t.Fatalf("expected runtime api_base before profile update, got %#v", fake.baseURLCalls)
	}

	configData, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(configData), "\"agent_token\": \"agent-token\"") {
		t.Fatalf("expected persisted agent token in config.json, got %s", string(configData))
	}
	if !strings.Contains(string(configData), "\"bind_token\": \"agent-token\"") {
		t.Fatalf("expected persisted bind token alias in config.json, got %s", string(configData))
	}
	if !strings.Contains(string(configData), "\"base_url\": \"https://runtime.na.hub.molten.bot\"") {
		t.Fatalf("expected persisted base_url alias in config.json, got %s", string(configData))
	}
}

func TestUpdateAgentProfileDerivesRuntimeAPIBaseFromPersistedEndpointAfterRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	settings := DefaultSettings()
	settings.DataDir = dir
	store, err := NewStore(filepath.Join(dir, "config.json"), settings)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	err = store.Update(func(state *AppState) error {
		state.Settings.HubURL = "https://na.hub.molten.bot"
		state.Session.AgentToken = "agent-token"
		state.Session.Handle = "dispatch-agent"
		state.Session.HandleFinalized = true
		state.Session.MetadataURL = "https://runtime.na.hub.molten.bot/runtime/profile"
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	fake := &fakeHubClient{
		currentBaseURL:      "https://na.hub.molten.bot",
		expectedMetadataURL: "https://runtime.na.hub.molten.bot",
	}
	service := NewService(store, fake)
	fake.currentBaseURL = "https://na.hub.molten.bot"

	err = service.UpdateAgentProfile(context.Background(), AgentProfile{
		DisplayName:     "Dispatch Agent",
		Emoji:           "🤖",
		ProfileMarkdown: "Updated after restart.",
	})
	if err != nil {
		t.Fatalf("update profile: %v", err)
	}

	if got := fake.baseURLCalls[len(fake.baseURLCalls)-1]; got != "https://runtime.na.hub.molten.bot" {
		t.Fatalf("expected derived runtime api_base before profile update, got %#v", fake.baseURLCalls)
	}

	state := service.store.Snapshot()
	if got := state.Session.AgentToken; got != "agent-token" {
		t.Fatalf("expected persisted agent token, got %q", got)
	}
}

func TestHandleDispatchResolutionFailureSendsDetailedFailureWithoutFollowUp(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "molten://dispatch/self"
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	message := hub.PullResponse{
		DeliveryID:    "delivery-1",
		FromAgentUUID: "caller-uuid",
		OpenClawMessage: hub.OpenClawMessage{
			Type:      "skill_request",
			SkillName: dispatchSkillName,
			RequestID: "parent-req",
			Payload: map[string]any{
				"target_agent_uuid": "missing-agent",
				"skill_name":        "run_task",
				"repo":              "/tmp/repo",
				"log_paths":         []string{"/tmp/repo/logs/failure.log"},
				"payload": map[string]any{
					"input": testDispatchPrompt,
				},
				"payload_format": "json",
			},
		},
	}

	if err := service.handleInboundMessage(context.Background(), message); err != nil {
		t.Fatalf("handle inbound message: %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected caller failure publish only, got %d", len(fake.publishCalls))
	}
	if len(fake.offlineCalls) != 1 {
		t.Fatalf("expected one offline call, got %d", len(fake.offlineCalls))
	}
	if fake.offlineCalls[0].Reason == "" {
		t.Fatal("expected offline reason to describe the task failure")
	}
	if fake.offlineCalls[0].SessionKey != service.settings.SessionKey {
		t.Fatalf("expected offline session key %q, got %q", service.settings.SessionKey, fake.offlineCalls[0].SessionKey)
	}

	failurePayload, ok := fake.publishCalls[0].Message.Payload.(map[string]any)
	if !ok {
		t.Fatalf("unexpected caller failure payload type: %T", fake.publishCalls[0].Message.Payload)
	}
	if failurePayload["status"] != "failed" {
		t.Fatalf("unexpected caller failure status: %#v", failurePayload)
	}
	if failurePayload["error"] != "no connected agent matched \"missing-agent\"" {
		t.Fatalf("unexpected caller error: %#v", failurePayload["error"])
	}
	if failurePayload["retryable"] != false {
		t.Fatalf("expected retryable=false, got %#v", failurePayload["retryable"])
	}
	if failurePayload["next_action"] != "" {
		t.Fatalf("expected empty next_action, got %#v", failurePayload["next_action"])
	}
	if got := fake.publishCalls[0].Message.ErrorDetail; got != "no connected agent matched \"missing-agent\"" {
		t.Fatalf("unexpected caller error detail: %#v", fake.publishCalls[0].Message.ErrorDetail)
	}
	if got := fake.publishCalls[0].Message.Error; got != "Task dispatch failed before it reached a connected agent. Error: no connected agent matched \"missing-agent\"" {
		t.Fatalf("unexpected caller failure summary: %#v", got)
	}
	if got := fake.publishCalls[0].Message.RequestID; got != "parent-req" {
		t.Fatalf("unexpected caller failure request id: %q", got)
	}
	if got := fake.publishCalls[0].Message.ReplyTo; got != "parent-req" {
		t.Fatalf("unexpected caller failure reply_to_request_id: %q", got)
	}

}

func TestHandleDownstreamFailureSendsDetailedFailureWithoutFollowUp(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "molten://dispatch/self"
		state.PendingTasks = []PendingTask{
			{
				ID:                  "task-1",
				ParentRequestID:     "parent-req",
				ChildRequestID:      "child-req",
				OriginalSkillName:   "run_task",
				CallerAgentUUID:     "caller-uuid",
				CallerRequestID:     "parent-req",
				Repo:                "/tmp/repo",
				LogPath:             filepath.Join(service.settings.DataDir, "logs", "task-1.log"),
				CreatedAt:           time.Now().Add(-time.Minute),
				ExpiresAt:           time.Now().Add(time.Minute),
				ExecutionRetryCount: 1,
				DispatchPayload: map[string]any{
					"repo":      "/tmp/repo",
					"log_paths": []string{"/tmp/original.log", "/tmp/original.log"},
					"input":     testDispatchPrompt,
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
			Payload:     map[string]any{"status": "failed", "retryable": true, "next_action": "inspect_logs"},
		},
	}

	if err := service.handleInboundMessage(context.Background(), message); err != nil {
		t.Fatalf("handle inbound message: %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected caller failure publish only, got %d", len(fake.publishCalls))
	}
	if len(fake.offlineCalls) != 1 {
		t.Fatalf("expected one offline call, got %d", len(fake.offlineCalls))
	}
	if fake.offlineCalls[0].SessionKey != service.settings.SessionKey {
		t.Fatalf("expected offline session key %q, got %q", service.settings.SessionKey, fake.offlineCalls[0].SessionKey)
	}

	failureMessage := fake.publishCalls[0].Message
	if failureMessage.Type != "skill_result" {
		t.Fatalf("unexpected caller message type: %s", failureMessage.Type)
	}
	if failureMessage.Error != "Task failed while dispatching to a connected agent. Error: task execution failed" {
		t.Fatalf("unexpected caller failure summary: %q", failureMessage.Error)
	}
	failurePayload, ok := failureMessage.Payload.(map[string]any)
	if !ok {
		t.Fatalf("unexpected caller failure payload type: %T", failureMessage.Payload)
	}
	if got := failurePayload["log_paths"].([]string); len(got) != 2 || got[0] != "/tmp/original.log" {
		t.Fatalf("unexpected caller failure log paths: %#v", failurePayload["log_paths"])
	}
	if got := failurePayload["ok"]; got != false {
		t.Fatalf("expected caller failure ok=false, got %#v", got)
	}
	if got := failurePayload["failure"]; got != true {
		t.Fatalf("expected caller failure marker, got %#v", got)
	}
	if got := failurePayload["message"]; got != "Task failed while dispatching to a connected agent." {
		t.Fatalf("unexpected caller failure message: %#v", got)
	}
	if got := failurePayload["retryable"]; got != true {
		t.Fatalf("unexpected caller retryable field: %#v", got)
	}
	if got := failurePayload["next_action"]; got != "inspect_logs" {
		t.Fatalf("unexpected caller next_action field: %#v", got)
	}
	if got := failurePayload["error_detail"].(map[string]any)["stderr"]; got != "panic: boom" {
		t.Fatalf("unexpected caller failure detail: %#v", failurePayload["error_detail"])
	}
	errorDetail, ok := failureMessage.ErrorDetail.(map[string]any)
	if !ok || errorDetail["stderr"] != "panic: boom" {
		t.Fatalf("unexpected caller error detail: %#v", failureMessage.ErrorDetail)
	}

	state := service.store.Snapshot()
	if len(state.PendingTasks) != 0 {
		t.Fatalf("expected task to be cleared, got %d pending", len(state.PendingTasks))
	}
}

func TestHandleDownstreamFailureFinalizesImmediately(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "molten://dispatch/self"
		state.PendingTasks = []PendingTask{
			{
				ID:                "task-1",
				ParentRequestID:   "parent-req",
				ChildRequestID:    "child-req",
				OriginalSkillName: "run_task",
				TargetAgentUUID:   "worker-uuid",
				CallerAgentUUID:   "caller-uuid",
				CallerRequestID:   "parent-req",
				Repo:              "/tmp/repo",
				LogPath:           filepath.Join(service.settings.DataDir, "logs", "task-1.log"),
				CreatedAt:         time.Now().Add(-time.Minute),
				ExpiresAt:         time.Now().Add(time.Minute),
				DispatchPayload: map[string]any{
					"repo":      "/tmp/repo",
					"log_paths": []string{"/tmp/original.log"},
					"input":     testDispatchPrompt,
				},
			},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	firstFailure := hub.PullResponse{
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
			Payload:     map[string]any{"status": "failed", "retryable": true, "next_action": "inspect_logs"},
		},
	}

	if err := service.handleInboundMessage(context.Background(), firstFailure); err != nil {
		t.Fatalf("first inbound failure should finalize task failure: %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected caller failure publish only, got %d", len(fake.publishCalls))
	}
	if got := fake.publishCalls[0].Message.Type; got != "skill_result" {
		t.Fatalf("expected caller failure publish, got %q", got)
	}
	if len(fake.offlineCalls) != 1 {
		t.Fatalf("expected one offline call after failure, got %d", len(fake.offlineCalls))
	}

	state := service.store.Snapshot()
	if len(state.PendingTasks) != 0 {
		t.Fatalf("expected pending task to clear after failure handling, got %d", len(state.PendingTasks))
	}
}

func TestExpirePendingTasksFinalizesTimedOutTaskImmediately(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "molten://dispatch/self"
		state.PendingTasks = []PendingTask{
			{
				ID:                "task-1",
				ParentRequestID:   "parent-req",
				ChildRequestID:    "child-req",
				OriginalSkillName: "run_task",
				TargetAgentUUID:   "worker-uuid",
				CallerAgentUUID:   "caller-uuid",
				CallerRequestID:   "parent-req",
				Repo:              "/tmp/repo",
				LogPath:           filepath.Join(service.settings.DataDir, "logs", "task-1.log"),
				CreatedAt:         time.Now().Add(-2 * time.Minute),
				ExpiresAt:         time.Now().Add(-time.Minute),
				DispatchPayload: map[string]any{
					"repo":      "/tmp/repo",
					"log_paths": []string{"/tmp/original.log"},
					"input":     testDispatchPrompt,
				},
			},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	if err := service.expirePendingTasks(context.Background()); err != nil {
		t.Fatalf("timeout should finalize task failure: %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected caller timeout failure publish only, got %d", len(fake.publishCalls))
	}
	if got := fake.publishCalls[0].Message.Type; got != "skill_result" {
		t.Fatalf("expected caller timeout failure publish, got %q", got)
	}
	if len(fake.offlineCalls) != 1 {
		t.Fatalf("expected one offline call after timeout failure, got %d", len(fake.offlineCalls))
	}

	state := service.store.Snapshot()
	if len(state.PendingTasks) != 0 {
		t.Fatalf("expected pending task to clear after timeout failure, got %d", len(state.PendingTasks))
	}
}

func TestHandleDownstreamPlaintextRunnerFailureReturnsErrorDetailsWithoutFollowUp(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "molten://dispatch/self"
		state.PendingTasks = []PendingTask{
			{
				ID:                "task-1",
				ParentRequestID:   "parent-req",
				ChildRequestID:    "child-req",
				OriginalSkillName: "run_task",
				TargetAgentUUID:   "worker-uuid",
				CallerAgentUUID:   "caller-uuid",
				CallerRequestID:   "parent-req",
				Repo:              "/tmp/repo",
				LogPath:           filepath.Join(service.settings.DataDir, "logs", "task-1.log"),
				CreatedAt:         time.Now().Add(-time.Minute),
				ExpiresAt:         time.Now().Add(time.Minute),
				DispatchPayload: map[string]any{
					"repo":      "/tmp/repo",
					"log_paths": []string{"/tmp/original.log"},
					"input":     testDispatchPrompt,
				},
			},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	firstFailure := hub.PullResponse{
		DeliveryID:    "delivery-1",
		FromAgentUUID: "worker-uuid",
		OpenClawMessage: hub.OpenClawMessage{
			Type:      "skill_result",
			SkillName: "run_task",
			RequestID: "child-req",
			ReplyTo:   "parent-req",
			Payload: "error connecting to api.github.com\n" +
				"check your internet connection or https://githubstatus.com",
		},
	}

	if err := service.handleInboundMessage(context.Background(), firstFailure); err != nil {
		t.Fatalf("failure should finalize task failure: %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected caller failure publish only, got %d", len(fake.publishCalls))
	}

	failurePayload, ok := fake.publishCalls[0].Message.Payload.(map[string]any)
	if !ok {
		t.Fatalf("unexpected caller failure payload type: %T", fake.publishCalls[0].Message.Payload)
	}
	if got := failurePayload["status"]; got != "failed" {
		t.Fatalf("unexpected caller failure status: %#v", got)
	}
	if got := failurePayload["message"]; got != "Task failed while dispatching to a connected agent." {
		t.Fatalf("unexpected caller failure message: %#v", got)
	}
	if got := failurePayload["error"]; got != "error connecting to api.github.com" {
		t.Fatalf("unexpected caller failure error: %#v", got)
	}
	if got := fake.publishCalls[0].Message.Error; got != "Task failed while dispatching to a connected agent. Error: error connecting to api.github.com" {
		t.Fatalf("unexpected caller failure summary: %#v", got)
	}
	if got := failurePayload["failure"]; got != true {
		t.Fatalf("expected failure marker in caller payload, got %#v", got)
	}
	failureDetail, ok := failurePayload["error_detail"].(string)
	if !ok || !strings.Contains(failureDetail, "githubstatus.com") {
		t.Fatalf("expected caller failure detail to include network diagnostic, got %#v", failurePayload["error_detail"])
	}
	if errorDetails, ok := failurePayload["error_details"].(string); !ok || !strings.Contains(errorDetails, "githubstatus.com") {
		t.Fatalf("expected caller error_details to include network diagnostic, got %#v", failurePayload["error_details"])
	}
	if detail, ok := fake.publishCalls[0].Message.ErrorDetail.(string); !ok || !strings.Contains(detail, "githubstatus.com") {
		t.Fatalf("expected caller top-level error detail to include network diagnostic, got %#v", fake.publishCalls[0].Message.ErrorDetail)
	}

	if len(fake.offlineCalls) != 1 {
		t.Fatalf("expected one offline call, got %d", len(fake.offlineCalls))
	}

	state := service.store.Snapshot()
	if len(state.PendingTasks) != 0 {
		t.Fatalf("expected pending task to clear after failure handling, got %d", len(state.PendingTasks))
	}
}

func TestHandleDispatchResolutionFailureReturnsCallerPublishErrorWithoutFollowUp(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.publishErr = errors.New("publish failure response failed")
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "molten://dispatch/self"
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	message := hub.PullResponse{
		DeliveryID:    "delivery-1",
		FromAgentUUID: "caller-uuid",
		OpenClawMessage: hub.OpenClawMessage{
			Type:      "skill_request",
			SkillName: dispatchSkillName,
			RequestID: "parent-req",
			Payload: map[string]any{
				"target_agent_uuid": "missing-agent",
				"skill_name":        "run_task",
				"repo":              "/tmp/repo",
				"log_paths":         []string{"/tmp/repo/logs/failure.log"},
				"payload": map[string]any{
					"input": testDispatchPrompt,
				},
				"payload_format": "json",
			},
		},
	}

	err = service.handleInboundMessage(context.Background(), message)
	if err == nil {
		t.Fatal("expected publish failure error")
	}
	if !strings.Contains(err.Error(), "publish failure response failed") {
		t.Fatalf("expected caller publish failure in error, got %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected one failed caller publish, got %d", len(fake.publishCalls))
	}
	if len(fake.offlineCalls) != 1 {
		t.Fatalf("expected one offline call, got %d", len(fake.offlineCalls))
	}

}

func TestHandleDownstreamFailureReturnsCallerPublishErrorWithoutFollowUp(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.publishErr = errors.New("publish failure response failed")
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "molten://dispatch/self"
		state.PendingTasks = []PendingTask{
			{
				ID:                  "task-1",
				ParentRequestID:     "parent-req",
				ChildRequestID:      "child-req",
				OriginalSkillName:   "run_task",
				CallerAgentUUID:     "caller-uuid",
				CallerRequestID:     "parent-req",
				Repo:                "/tmp/repo",
				LogPath:             filepath.Join(service.settings.DataDir, "logs", "task-1.log"),
				CreatedAt:           time.Now().Add(-time.Minute),
				ExpiresAt:           time.Now().Add(time.Minute),
				ExecutionRetryCount: 1,
				DispatchPayload: map[string]any{
					"repo":      "/tmp/repo",
					"log_paths": []string{"/tmp/original.log"},
					"input":     testDispatchPrompt,
				},
			},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	message := hub.PullResponse{
		DeliveryID:    "delivery-1",
		FromAgentUUID: "worker-uuid",
		OpenClawMessage: hub.OpenClawMessage{
			Type:      "skill_result",
			SkillName: "run_task",
			RequestID: "child-req",
			ReplyTo:   "parent-req",
			OK:        boolPtr(false),
			Error:     "task execution failed",
			Payload:   map[string]any{"status": "failed"},
		},
	}

	err = service.handleInboundMessage(context.Background(), message)
	if err == nil {
		t.Fatal("expected publish failure error")
	}
	if !strings.Contains(err.Error(), "publish failure response failed") {
		t.Fatalf("expected caller publish failure in error, got %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected one failed caller publish, got %d", len(fake.publishCalls))
	}
	if len(fake.offlineCalls) != 1 {
		t.Fatalf("expected one offline call, got %d", len(fake.offlineCalls))
	}

	state := service.store.Snapshot()
	if len(state.PendingTasks) != 0 {
		t.Fatalf("expected failed task to be cleared after final failure handling, got %d pending", len(state.PendingTasks))
	}
}

func TestDispatchFromUIFailureMarksOfflineWithoutFollowUp(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.publishErr = errors.New("publish downstream failed")
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.ConnectedAgents = []ConnectedAgent{
			testConnectedAgent("worker-a", "Worker A", "worker-uuid", Skill{Name: "run_task"}),
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	_, err = service.DispatchFromUI(context.Background(), DispatchRequest{
		RequestID:     "ui-req",
		SkillName:     "run_task",
		Repo:          "/tmp/repo",
		LogPaths:      []string{"/tmp/repo/logs/failure.log"},
		Payload:       testDispatchPrompt,
		PayloadFormat: "markdown",
	})
	if err == nil {
		t.Fatal("expected dispatch failure")
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected initial dispatch publish only, got %d", len(fake.publishCalls))
	}
	if len(fake.offlineCalls) != 1 {
		t.Fatalf("expected one offline call, got %d", len(fake.offlineCalls))
	}
	if fake.offlineCalls[0].Reason == "" {
		t.Fatal("expected offline reason to describe the task failure")
	}
	if fake.offlineCalls[0].SessionKey != service.settings.SessionKey {
		t.Fatalf("expected offline session key %q, got %q", service.settings.SessionKey, fake.offlineCalls[0].SessionKey)
	}

	state := service.store.Snapshot()
	if len(state.PendingTasks) != 0 {
		t.Fatalf("expected no pending tasks after failed dispatch, got %d", len(state.PendingTasks))
	}
}

func TestDispatchFromUIInfersDefaultSkillForTargetAgent(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.ConnectedAgents = []ConnectedAgent{testConnectedAgent("worker-a", "Worker A", "worker-uuid", Skill{Name: "run_task"})}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	task, err := service.DispatchFromUI(context.Background(), DispatchRequest{
		RequestID:      "ui-req",
		TargetAgentRef: "worker-a",
	})
	if err != nil {
		t.Fatalf("dispatch from ui: %v", err)
	}

	if task.OriginalSkillName != "run_task" {
		t.Fatalf("expected inferred skill name, got %#v", task)
	}
	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected one publish call, got %d", len(fake.publishCalls))
	}
	if got := fake.publishCalls[0].Message.SkillName; got != "run_task" {
		t.Fatalf("unexpected published skill name: %q", got)
	}
	if got := fake.publishCalls[0].Message.Payload; got != nil {
		t.Fatalf("expected nil payload for target-only dispatch, got %#v", got)
	}
	if got := fake.publishCalls[0].Message.PayloadFormat; got != "" {
		t.Fatalf("expected empty payload format for target-only dispatch, got %q", got)
	}
}

func TestDispatchFromUIRequiresSelectionWhenTargetAndSkillAreBlank(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	_, err = service.DispatchFromUI(context.Background(), DispatchRequest{
		RequestID:      "ui-req",
		TargetAgentRef: "   ",
	})
	if err == nil {
		t.Fatal("expected validation error for empty target + skill")
	}
	if got := err.Error(); got != DispatchSelectionRequiredMessage {
		t.Fatalf("unexpected error: %q", got)
	}
}

func TestHandleSkillRequestAcceptsTargetAgentRefViaInput(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.ConnectedAgents = []ConnectedAgent{testConnectedAgent("worker-a", "Worker A", "worker-uuid", Skill{Name: "run_task"})}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	message := hub.PullResponse{
		DeliveryID:    "delivery-1",
		FromAgentUUID: "caller-uuid",
		OpenClawMessage: hub.OpenClawMessage{
			Type:      "skill_request",
			SkillName: dispatchSkillName,
			RequestID: "parent-req",
			Input: map[string]any{
				"target_agent_ref": "worker-a",
			},
		},
	}

	if err := service.handleInboundMessage(context.Background(), message); err != nil {
		t.Fatalf("handle inbound message: %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected one downstream publish call, got %d", len(fake.publishCalls))
	}
	if got := fake.publishCalls[0].ToAgentUUID; got != "worker-uuid" {
		t.Fatalf("unexpected target agent UUID: %#v", fake.publishCalls[0])
	}
	if got := fake.publishCalls[0].Message.SkillName; got != "run_task" {
		t.Fatalf("expected inferred downstream skill, got %q", got)
	}
	if got := fake.publishCalls[0].Message.Payload; got != nil {
		t.Fatalf("expected nil downstream payload for target-only activation, got %#v", got)
	}
	if got := fake.publishCalls[0].Message.PayloadFormat; got != "" {
		t.Fatalf("expected empty payload format for target-only activation, got %q", got)
	}

	state := service.store.Snapshot()
	if len(state.PendingTasks) != 1 {
		t.Fatalf("expected one pending task, got %d", len(state.PendingTasks))
	}
	if got := state.PendingTasks[0].TargetAgentUUID; got != "worker-uuid" {
		t.Fatalf("unexpected pending task target UUID: %#v", state.PendingTasks[0])
	}
	if got := state.PendingTasks[0].OriginalSkillName; got != "run_task" {
		t.Fatalf("unexpected pending task skill name: %#v", state.PendingTasks[0])
	}
}

func TestHandleSkillRequestRequiresSelectionWhenTargetAndSkillAreBlank(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	message := hub.PullResponse{
		DeliveryID:    "delivery-1",
		FromAgentUUID: "caller-uuid",
		OpenClawMessage: hub.OpenClawMessage{
			Type:      "skill_request",
			SkillName: dispatchSkillName,
			RequestID: "parent-req",
			Payload: map[string]any{
				"target_agent_ref": "   ",
			},
		},
	}

	if err := service.handleInboundMessage(context.Background(), message); err != nil {
		t.Fatalf("handle inbound message: %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected one caller failure publish, got %d", len(fake.publishCalls))
	}
	failurePayload, ok := fake.publishCalls[0].Message.Payload.(map[string]any)
	if !ok {
		t.Fatalf("unexpected failure payload type: %T", fake.publishCalls[0].Message.Payload)
	}
	if got := failurePayload["error"]; got != DispatchSelectionRequiredMessage {
		t.Fatalf("unexpected failure error payload: %#v", got)
	}
	if got := failurePayload["status"]; got != "failed" {
		t.Fatalf("unexpected failure status payload: %#v", got)
	}
	if got := fake.publishCalls[0].Message.Error; got != "Task dispatch failed before it reached a connected agent. Error: Please select agent, skill to dispatch a request." {
		t.Fatalf("unexpected caller failure summary: %#v", got)
	}

}

func TestHandleSkillRequestAcceptsJSONStringActivationPayload(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		payload func() hub.OpenClawMessage
	}{
		{
			name: "payload",
			payload: func() hub.OpenClawMessage {
				return hub.OpenClawMessage{
					Type:      "skill_request",
					SkillName: dispatchSkillName,
					RequestID: "parent-payload",
					Payload:   `{"target_agent_ref":"worker-a"}`,
				}
			},
		},
		{
			name: "input",
			payload: func() hub.OpenClawMessage {
				return hub.OpenClawMessage{
					Type:      "skill_request",
					SkillName: dispatchSkillName,
					RequestID: "parent-input",
					Input:     `{"target_agent_ref":"worker-a"}`,
				}
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			service, fake := newTestService(t)
			err := service.store.Update(func(state *AppState) error {
				state.Session.AgentToken = "agent-token"
				state.ConnectedAgents = []ConnectedAgent{testConnectedAgent("worker-a", "Worker A", "worker-uuid", Skill{Name: "run_task"})}
				return nil
			})
			if err != nil {
				t.Fatalf("seed store: %v", err)
			}

			message := hub.PullResponse{
				DeliveryID:      "delivery-" + tc.name,
				FromAgentUUID:   "caller-uuid",
				OpenClawMessage: tc.payload(),
			}

			if err := service.handleInboundMessage(context.Background(), message); err != nil {
				t.Fatalf("handle inbound message: %v", err)
			}

			if len(fake.publishCalls) != 1 {
				t.Fatalf("expected one downstream publish call, got %d", len(fake.publishCalls))
			}
			if got := fake.publishCalls[0].ToAgentUUID; got != "worker-uuid" {
				t.Fatalf("unexpected target agent UUID: %#v", fake.publishCalls[0])
			}
			if got := fake.publishCalls[0].Message.SkillName; got != "run_task" {
				t.Fatalf("expected inferred downstream skill, got %q", got)
			}
			if got := fake.publishCalls[0].Message.Payload; got != nil {
				t.Fatalf("expected nil downstream payload for target-only activation, got %#v", got)
			}
		})
	}
}

func TestHandleSkillRequestAcceptsSelectedTaskAliasAndInlinePayload(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.ConnectedAgents = []ConnectedAgent{testConnectedAgent("worker-a", "Worker A", "worker-uuid", Skill{Name: "code_for_me"})}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	message := hub.PullResponse{
		DeliveryID:    "delivery-1",
		FromAgentUUID: "caller-uuid",
		OpenClawMessage: hub.OpenClawMessage{
			Type:      "skill_request",
			SkillName: dispatchSkillName,
			RequestID: "parent-req",
			Payload: map[string]any{
				"selectedTask": "code_for_me",
				"repo":         "/tmp/repo",
				"logPaths":     []string{"/tmp/repo/logs/failure.log"},
				"prompt":       testDispatchPrompt,
				"baseBranch":   "main",
				"targetSubdir": ".",
			},
		},
	}

	if err := service.handleInboundMessage(context.Background(), message); err != nil {
		t.Fatalf("handle inbound message: %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected one downstream publish call, got %d", len(fake.publishCalls))
	}
	if got := fake.publishCalls[0].ToAgentUUID; got != "worker-uuid" {
		t.Fatalf("unexpected target agent UUID: %#v", fake.publishCalls[0])
	}
	if got := fake.publishCalls[0].Message.SkillName; got != "code_for_me" {
		t.Fatalf("expected selected task alias to resolve downstream skill, got %q", got)
	}
	if got := fake.publishCalls[0].Message.PayloadFormat; got != "json" {
		t.Fatalf("expected inline payload to dispatch as json, got %q", got)
	}

	payload, ok := fake.publishCalls[0].Message.Payload.(map[string]any)
	if !ok {
		t.Fatalf("expected downstream payload map, got %T", fake.publishCalls[0].Message.Payload)
	}
	if got := payload["prompt"]; got != testDispatchPrompt {
		t.Fatalf("unexpected downstream prompt payload: %#v", payload)
	}
	if got := payload["baseBranch"]; got != "main" {
		t.Fatalf("unexpected downstream baseBranch payload: %#v", payload)
	}
	if got := payload["targetSubdir"]; got != "." {
		t.Fatalf("unexpected downstream targetSubdir payload: %#v", payload)
	}
	if got := payload["repo"]; got != "/tmp/repo" {
		t.Fatalf("unexpected downstream repo payload: %#v", payload)
	}
	logPaths, ok := payload["log_paths"].([]string)
	if !ok || len(logPaths) != 1 || logPaths[0] != "/tmp/repo/logs/failure.log" {
		t.Fatalf("unexpected downstream log_paths payload: %#v", payload["log_paths"])
	}
	if _, exists := payload["selectedTask"]; exists {
		t.Fatalf("did not expect selectedTask control field in downstream payload: %#v", payload)
	}
	if _, exists := payload["logPaths"]; exists {
		t.Fatalf("did not expect logPaths control field in downstream payload: %#v", payload)
	}
}

func TestHandleSkillRequestAcceptsSelectedAgentAndSkillAliases(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.ConnectedAgents = []ConnectedAgent{testConnectedAgent("worker-a", "Worker A", "worker-uuid", Skill{Name: "code_for_me"})}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	message := hub.PullResponse{
		DeliveryID:    "delivery-1",
		FromAgentUUID: "caller-uuid",
		OpenClawMessage: hub.OpenClawMessage{
			Type:      "skill_request",
			SkillName: dispatchSkillName,
			RequestID: "parent-req",
			Payload: map[string]any{
				"selectedAgentRef":  "worker-a",
				"selectedSkillName": "code_for_me",
				"repo":              "/tmp/repo",
				"logPaths":          []string{"/tmp/repo/logs/failure.log"},
				"prompt":            testDispatchPrompt,
			},
		},
	}

	if err := service.handleInboundMessage(context.Background(), message); err != nil {
		t.Fatalf("handle inbound message: %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected one downstream publish call, got %d", len(fake.publishCalls))
	}
	if got := fake.publishCalls[0].ToAgentUUID; got != "worker-uuid" {
		t.Fatalf("unexpected target agent UUID: %#v", fake.publishCalls[0])
	}
	if got := fake.publishCalls[0].Message.SkillName; got != "code_for_me" {
		t.Fatalf("expected selected skill alias to resolve downstream skill, got %q", got)
	}
	if got := fake.publishCalls[0].Message.PayloadFormat; got != "json" {
		t.Fatalf("expected inline payload to dispatch as json, got %q", got)
	}

	payload, ok := fake.publishCalls[0].Message.Payload.(map[string]any)
	if !ok {
		t.Fatalf("expected downstream payload map, got %T", fake.publishCalls[0].Message.Payload)
	}
	if got := payload["prompt"]; got != testDispatchPrompt {
		t.Fatalf("unexpected downstream prompt payload: %#v", payload)
	}
	if got := payload["repo"]; got != "/tmp/repo" {
		t.Fatalf("unexpected downstream repo payload: %#v", payload)
	}
	logPaths, ok := payload["log_paths"].([]string)
	if !ok || len(logPaths) != 1 || logPaths[0] != "/tmp/repo/logs/failure.log" {
		t.Fatalf("unexpected downstream log_paths payload: %#v", payload["log_paths"])
	}
	if _, exists := payload["selectedAgentRef"]; exists {
		t.Fatalf("did not expect selectedAgentRef control field in downstream payload: %#v", payload)
	}
	if _, exists := payload["selectedSkillName"]; exists {
		t.Fatalf("did not expect selectedSkillName control field in downstream payload: %#v", payload)
	}
}

func TestHandleDownstreamFailureStillQueuesFollowUpWhenCallerPublishFails(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.publishErr = errors.New("publish caller failure")
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "molten://dispatch/self"
		state.PendingTasks = []PendingTask{
			{
				ID:                  "task-1",
				ParentRequestID:     "parent-req",
				ChildRequestID:      "child-req",
				OriginalSkillName:   "run_task",
				CallerAgentUUID:     "caller-uuid",
				CallerRequestID:     "parent-req",
				Repo:                "/tmp/repo",
				LogPath:             filepath.Join(service.settings.DataDir, "logs", "task-1.log"),
				CreatedAt:           time.Now().Add(-time.Minute),
				ExpiresAt:           time.Now().Add(time.Minute),
				ExecutionRetryCount: 1,
				DispatchPayload: map[string]any{
					"repo":      "/tmp/repo",
					"log_paths": []string{"/tmp/original.log"},
					"input":     testDispatchPrompt,
				},
			},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	err = service.handleInboundMessage(context.Background(), hub.PullResponse{
		DeliveryID: "delivery-1",
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
	})
	if err == nil {
		t.Fatal("expected publish failure error")
	}

	if len(fake.offlineCalls) != 1 {
		t.Fatalf("expected one offline call despite caller publish failure, got %d", len(fake.offlineCalls))
	}

}

func TestFailureFromErrorPreservesStructuredHubAPIErrorDetails(t *testing.T) {
	t.Parallel()

	report := failureFromError("Task dispatch failed before it reached a connected agent.", &hub.APIError{
		StatusCode: 409,
		Code:       "agent_exists",
		Message:    "handle already claimed",
		Retryable:  true,
		NextAction: "retry_with_different_handle",
		Detail:     map[string]any{"handle": "dispatch-agent"},
	})

	if report.Error != "hub API 409 agent_exists: handle already claimed" {
		t.Fatalf("unexpected error string: %q", report.Error)
	}
	detail, ok := report.Detail.(map[string]any)
	if !ok {
		t.Fatalf("unexpected error detail type: %T", report.Detail)
	}
	if detail["status_code"] != 409 {
		t.Fatalf("unexpected status code: %#v", detail)
	}
	if detail["next_action"] != "retry_with_different_handle" {
		t.Fatalf("unexpected next action: %#v", detail)
	}
	if !report.Retryable {
		t.Fatal("expected retryable API error to propagate")
	}
	if report.NextAction != "retry_with_different_handle" {
		t.Fatalf("unexpected report next action: %q", report.NextAction)
	}
	nested, ok := detail["error_detail"].(map[string]any)
	if !ok || nested["handle"] != "dispatch-agent" {
		t.Fatalf("unexpected nested detail: %#v", detail["error_detail"])
	}
}

func TestFailureFromMessageUsesDownstreamFailureEnvelope(t *testing.T) {
	t.Parallel()

	report := failureFromMessage(hub.OpenClawMessage{
		Type:    "skill_result",
		Payload: map[string]any{"status": "failed", "message": "Task failed in worker", "error": "panic: boom", "retryable": true, "next_action": "retry_after_fix", "error_detail": map[string]any{"stderr": "stacktrace"}},
	})

	if report.Message != "Task failed in worker" {
		t.Fatalf("unexpected failure message: %q", report.Message)
	}
	if report.Error != "panic: boom" {
		t.Fatalf("unexpected failure error: %q", report.Error)
	}
	if !report.Retryable {
		t.Fatal("expected retryable flag from downstream payload")
	}
	if report.NextAction != "retry_after_fix" {
		t.Fatalf("unexpected next action: %q", report.NextAction)
	}
	detail, ok := report.Detail.(map[string]any)
	if !ok || detail["stderr"] != "stacktrace" {
		t.Fatalf("unexpected failure detail: %#v", report.Detail)
	}
}

func TestFailureFromMessageUsesErrorDetailsAlias(t *testing.T) {
	t.Parallel()

	report := failureFromMessage(hub.OpenClawMessage{
		Type: "skill_result",
		Payload: map[string]any{
			"status":        "failed",
			"error_details": map[string]any{"stderr": "panic: boom"},
		},
	})

	if report.Error != "panic: boom" {
		t.Fatalf("unexpected failure error: %q", report.Error)
	}
	detail, ok := report.Detail.(map[string]any)
	if !ok || detail["stderr"] != "panic: boom" {
		t.Fatalf("unexpected failure detail: %#v", report.Detail)
	}
}

func TestCallerFailurePayloadIncludesExplicitFailureDetails(t *testing.T) {
	t.Parallel()

	payload := callerFailurePayload(failureReport{
		Message: "downstream worker returned a non-zero exit code",
		Error:   "panic: boom",
		Detail:  map[string]any{"stderr": "stacktrace", "exit_code": 1},
	}, []string{"/tmp/task.log"})

	if payload["status"] != "failed" {
		t.Fatalf("expected failed status, got %#v", payload["status"])
	}
	if payload["message"] != "Task failed: downstream worker returned a non-zero exit code" {
		t.Fatalf("unexpected failure message: %#v", payload["message"])
	}
	if payload["ok"] != false {
		t.Fatalf("expected ok=false, got %#v", payload["ok"])
	}
	if payload["failure"] != true {
		t.Fatalf("expected failure marker, got %#v", payload["failure"])
	}
	detail, ok := payload["error_details"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected error_details type: %T", payload["error_details"])
	}
	if detail["stderr"] != "stacktrace" || detail["exit_code"] != 1 {
		t.Fatalf("unexpected error_details payload: %#v", detail)
	}
}

func TestCallerFailureErrorIncludesExplicitFailureSummary(t *testing.T) {
	t.Parallel()

	got := callerFailureError(failureReport{
		Message: "downstream worker returned a non-zero exit code",
		Error:   "panic: boom",
	})

	if got != "Task failed: downstream worker returned a non-zero exit code. Error: panic: boom" {
		t.Fatalf("unexpected caller failure error: %q", got)
	}
}

func TestMessageSucceededTreatsPlaintextRunnerErrorAsFailure(t *testing.T) {
	t.Parallel()

	message := hub.OpenClawMessage{
		Type: "skill_result",
		Payload: "error connecting to api.github.com\n" +
			"check your internet connection or https://githubstatus.com",
	}

	if messageSucceeded(message) {
		t.Fatalf("expected plaintext runner error payload to be treated as failure: %#v", message)
	}

	report := failureFromMessage(message)
	if report.Error != "error connecting to api.github.com" {
		t.Fatalf("unexpected failure error: %q", report.Error)
	}
	if detail, ok := report.Detail.(string); !ok || !strings.Contains(detail, "githubstatus.com") {
		t.Fatalf("expected plaintext failure detail to be preserved, got %#v", report.Detail)
	}
}

func TestMessageSucceededTreatsNonZeroExitCodePayloadAsFailure(t *testing.T) {
	t.Parallel()

	message := hub.OpenClawMessage{
		Type: "skill_result",
		Payload: map[string]any{
			"exit_code": 1,
			"stderr":    "error connecting to api.github.com",
		},
	}

	if messageSucceeded(message) {
		t.Fatalf("expected non-zero exit code payload to be treated as failure: %#v", message)
	}
}

func TestMessageSucceededTreatsErrorDetailsAliasPayloadAsFailure(t *testing.T) {
	t.Parallel()

	message := hub.OpenClawMessage{
		Type: "skill_result",
		Payload: map[string]any{
			"error_details": map[string]any{"stderr": "panic: boom"},
		},
	}

	if messageSucceeded(message) {
		t.Fatalf("expected error_details payload to be treated as failure: %#v", message)
	}
}

func TestNormalizePayloadFormatCanonicalizesToHubEnum(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		format  string
		payload any
		want    string
	}{
		{
			name:    "nil payload omits format",
			format:  "json",
			payload: nil,
			want:    "",
		},
		{
			name:    "string payload defaults to markdown",
			format:  "",
			payload: testDispatchPrompt,
			want:    "markdown",
		},
		{
			name:    "text alias maps to markdown",
			format:  "text",
			payload: testDispatchPrompt,
			want:    "markdown",
		},
		{
			name:    "json remains json",
			format:  "json",
			payload: map[string]any{"input": testDispatchPrompt},
			want:    "json",
		},
		{
			name:    "unknown format for string payload falls back to markdown",
			format:  "xml",
			payload: testDispatchPrompt,
			want:    "markdown",
		},
		{
			name:    "unknown format for object payload falls back to json",
			format:  "xml",
			payload: map[string]any{"input": testDispatchPrompt},
			want:    "json",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizePayloadFormat(tc.format, tc.payload); got != tc.want {
				t.Fatalf("normalizePayloadFormat(%q, %#v) = %q, want %q", tc.format, tc.payload, got, tc.want)
			}
		})
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
		state.Session.MetadataURL = "https://runtime.na.hub.molten.bot/profile"
		state.Session.OpenClawPullURL = "https://runtime.na.hub.molten.bot/openclaw/pull"
		state.Session.OpenClawPushURL = "https://runtime.na.hub.molten.bot/openclaw/publish"
		state.Session.OfflineURL = "https://runtime.na.hub.molten.bot/openclaw/offline"
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
	if fake.offlineCalls[0].SessionKey != service.settings.SessionKey {
		t.Fatalf("expected offline session key %q, got %q", service.settings.SessionKey, fake.offlineCalls[0].SessionKey)
	}
	if len(fake.baseURLCalls) == 0 {
		t.Fatal("expected service to configure a runtime api_base")
	}
	for _, got := range fake.baseURLCalls {
		if got != "https://runtime.na.hub.molten.bot" {
			t.Fatalf("expected persisted runtime api_base on every sync, got %#v", fake.baseURLCalls)
		}
	}
	if len(fake.runtimeEndpoints) == 0 {
		t.Fatal("expected service to configure runtime endpoints")
	}
	for _, endpoints := range fake.runtimeEndpoints {
		if endpoints.MetadataURL != "https://runtime.na.hub.molten.bot/profile" {
			t.Fatalf("expected persisted runtime endpoints on every sync, got %#v", fake.runtimeEndpoints)
		}
	}

	state := service.store.Snapshot()
	if state.Connection.Status != ConnectionStatusDisconnected || state.Connection.Transport != ConnectionTransportOffline {
		t.Fatalf("expected offline connection state after explicit offline mark, got %#v", state.Connection)
	}
}

func TestMarkOnlineUpdatesHubPresenceMetadata(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.Handle = "dispatch-agent"
		state.Session.DisplayName = "Dispatch Agent"
		state.Session.Emoji = "🤖"
		state.Session.ProfileBio = "Routes tasks."
		state.Session.APIBase = "https://runtime.na.hub.molten.bot"
		state.Settings.HubURL = "https://na.hub.molten.bot"
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	if err := service.MarkOnline(context.Background(), ConnectionTransportHTTPLong); err != nil {
		t.Fatalf("mark online: %v", err)
	}

	if len(fake.updateMetadataCalls) != 1 {
		t.Fatalf("expected one metadata update, got %d", len(fake.updateMetadataCalls))
	}
	metadata := fake.updateMetadataCalls[0].Metadata
	presence, ok := metadata["presence"].(map[string]any)
	if !ok {
		t.Fatalf("expected presence metadata, got %#v", metadata["presence"])
	}
	if got, want := presence["status"], "online"; got != want {
		t.Fatalf("presence status = %#v, want %q", got, want)
	}
	if got, want := presence["ready"], true; got != want {
		t.Fatalf("presence ready = %#v, want %v", got, want)
	}
	if got, want := presence["transport"], ConnectionTransportHTTPLong; got != want {
		t.Fatalf("presence transport = %#v, want %q", got, want)
	}
	if got, want := presence["session_key"], service.settings.SessionKey; got != want {
		t.Fatalf("presence session_key = %#v, want %q", got, want)
	}

	state := service.store.Snapshot()
	if state.Connection.Status != ConnectionStatusConnected || state.Connection.Transport != ConnectionTransportHTTPLong {
		t.Fatalf("expected connected http-long-poll state after mark online, got %#v", state.Connection)
	}
	if state.Session.OfflineMarked {
		t.Fatalf("expected offline marker cleared after mark online, got %#v", state.Session)
	}
}

func TestPollOnceMarksHTTPConnectivityAfterSuccessfulPull(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	fake.pullOK = false

	if err := service.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll once: %v", err)
	}

	state := service.store.Snapshot()
	if state.Connection.Status != ConnectionStatusConnected {
		t.Fatalf("expected connected status, got %#v", state.Connection)
	}
	if state.Connection.Transport != ConnectionTransportHTTPLong {
		t.Fatalf("expected http transport, got %#v", state.Connection)
	}
}

func TestPollOnceMarksDisconnectedWhenHubIsUnreachable(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	fake.pullErr = errors.New("dial tcp 10.0.0.1:443: connect: connection refused")

	err = service.PollOnce(context.Background())
	if err == nil {
		t.Fatal("expected poll failure")
	}

	state := service.store.Snapshot()
	if state.Connection.Status != ConnectionStatusDisconnected {
		t.Fatalf("expected disconnected status, got %#v", state.Connection)
	}
	if state.Connection.Transport != ConnectionTransportOffline {
		t.Fatalf("expected offline transport, got %#v", state.Connection)
	}
	if state.Connection.Error == "" {
		t.Fatalf("expected connection error detail, got %#v", state.Connection)
	}
}

func TestPollOnceWithTimeoutReturnsPollError(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	fake.pullErr = errors.New("dial tcp 10.0.0.1:443: connect: connection refused")

	err = service.pollOnceWithTimeout(context.Background())
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("poll once with timeout error = %v, want connection refused", err)
	}
}

func TestWaitForHubReachableRetriesPingUntilLive(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	service.hubPingRetryDelay = 2 * time.Millisecond
	service.hubPingCheckTimeout = 250 * time.Millisecond
	fake.pingErrSequence = []error{
		errors.New("GET https://na.hub.molten.bot/ping returned status=503"),
		nil,
	}
	fake.pingDetail = "https://na.hub.molten.bot/ping status=204"

	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.APIBase = "https://na.hub.molten.bot/v1"
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	if err := service.waitForHubReachable(context.Background()); err != nil {
		t.Fatalf("wait for hub reachable: %v", err)
	}

	if fake.pingCalls != 2 {
		t.Fatalf("expected 2 ping checks, got %d", fake.pingCalls)
	}

	state := service.store.Snapshot()
	if state.Connection.Transport != ConnectionTransportReachable {
		t.Fatalf("expected reachable transport after successful ping, got %#v", state.Connection)
	}
	if state.Connection.Status != ConnectionStatusDisconnected {
		t.Fatalf("expected disconnected status while connecting, got %#v", state.Connection)
	}
	if state.Connection.Detail != "https://na.hub.molten.bot/ping status=204" {
		t.Fatalf("unexpected ping detail: %#v", state.Connection)
	}
	if state.Connection.Domain != "na.hub.molten.bot" {
		t.Fatalf("unexpected hub domain: %#v", state.Connection)
	}
}

func TestNoteRealtimeFallbackKeepsHubReachableWhileWebsocketFallsBack(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.APIBase = "https://na.hub.molten.bot/v1"
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	service.noteRealtimeFallback(errors.New("websocket unavailable"))

	state := service.store.Snapshot()
	if state.Connection.Status != ConnectionStatusDisconnected {
		t.Fatalf("expected disconnected status during fallback, got %#v", state.Connection)
	}
	if state.Connection.Transport != ConnectionTransportReachable {
		t.Fatalf("expected reachable transport during fallback, got %#v", state.Connection)
	}
	if state.Connection.Error != "websocket unavailable" {
		t.Fatalf("unexpected websocket fallback error: %#v", state.Connection)
	}
	if !strings.Contains(state.Connection.Detail, "falling back to HTTP long polling") {
		t.Fatalf("unexpected websocket fallback detail: %#v", state.Connection)
	}
}

func TestNoteHubInteractionDoesNotDowngradeConnectedWebsocketOnHTTPSuccess(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.APIBase = "https://na.hub.molten.bot/v1"
		state.Connection = ConnectionState{
			Status:    ConnectionStatusConnected,
			Transport: ConnectionTransportWebSocket,
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	service.noteHubInteraction(nil, ConnectionTransportHTTP)

	state := service.store.Snapshot()
	if state.Connection.Transport != ConnectionTransportWebSocket {
		t.Fatalf("expected websocket transport to survive incidental HTTP success, got %#v", state.Connection)
	}
}

func TestRunHubLoopFallsBackToHTTPLongPollWhenWebsocketUnavailable(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	service.hubPingRetryDelay = 2 * time.Millisecond
	service.hubPingCheckTimeout = 250 * time.Millisecond
	fake.connectErr = errors.New("websocket unavailable")
	fake.pingDetail = "https://na.hub.molten.bot/ping status=204"
	fake.pullOK = false

	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.APIBase = "https://na.hub.molten.bot/v1"
		state.Settings.PollInterval = 10 * time.Millisecond
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.RunHubLoop(ctx)
	}()

	deadline := time.After(2 * time.Second)
	for {
		if fake.pullCalls > 0 {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("expected pull fallback to run at least once; connect calls=%d ping calls=%d", len(fake.baseURLCalls), fake.pingCalls)
		case <-time.After(10 * time.Millisecond):
		}
	}
	time.Sleep(120 * time.Millisecond)

	cancel()
	<-done
	if fake.connectCalls != 1 {
		t.Fatalf("expected one websocket connect attempt during HTTP fallback window, got %d", fake.connectCalls)
	}

	state := service.store.Snapshot()
	if state.Connection.Status != ConnectionStatusConnected {
		t.Fatalf("expected connected status after HTTP fallback poll, got %#v", state.Connection)
	}
	if state.Connection.Transport != ConnectionTransportHTTPLong {
		t.Fatalf("expected http long-poll transport after websocket fallback, got %#v", state.Connection)
	}
	if state.Connection.Error != "" {
		t.Fatalf("expected successful HTTP fallback to clear connection error, got %#v", state.Connection)
	}
}

func TestRunHubLoopMarksPresenceOnlineBeforeDispatching(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	service.hubPingRetryDelay = 2 * time.Millisecond
	service.hubPingCheckTimeout = 250 * time.Millisecond
	fake.connectErr = errors.New("websocket unavailable")
	fake.pingDetail = "https://na.hub.molten.bot/ping status=204"
	fake.pullOK = false

	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.DisplayName = "Dispatch Agent"
		state.Session.APIBase = "https://na.hub.molten.bot/v1"
		state.Settings.PollInterval = 10 * time.Millisecond
		state.Session.OfflineMarked = true
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.RunHubLoop(ctx)
	}()

	deadline := time.After(2 * time.Second)
	metadataObserved := false
	for {
		if len(fake.updateMetadataCalls) > 0 {
			metadataObserved = true
		}
		if metadataObserved && fake.pullCalls > 0 {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("expected startup presence sync before dispatch loop; metadata_calls=%d pull_calls=%d", len(fake.updateMetadataCalls), fake.pullCalls)
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	<-done

	metadata := fake.updateMetadataCalls[0].Metadata
	presence, ok := metadata["presence"].(map[string]any)
	if !ok {
		t.Fatalf("expected presence metadata, got %#v", metadata["presence"])
	}
	if got, want := presence["status"], "online"; got != want {
		t.Fatalf("presence status = %#v, want %q", got, want)
	}
	if got, want := presence["transport"], ConnectionTransportHTTPLong; got != want {
		t.Fatalf("presence transport = %#v, want %q", got, want)
	}
	if fake.pullCalls == 0 {
		t.Fatalf("expected dispatch loop to continue into pull fallback after presence sync, got %d pulls", fake.pullCalls)
	}
}

func TestEnsurePresenceOnlineResyncsWhenTransportChanges(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.DisplayName = "Dispatch Agent"
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	if err := service.ensurePresenceOnline(context.Background(), ConnectionTransportHTTPLong); err != nil {
		t.Fatalf("ensure presence online http: %v", err)
	}
	if err := service.ensurePresenceOnline(context.Background(), ConnectionTransportHTTPLong); err != nil {
		t.Fatalf("ensure presence online http repeat: %v", err)
	}
	if err := service.ensurePresenceOnline(context.Background(), ConnectionTransportWebSocket); err != nil {
		t.Fatalf("ensure presence online websocket: %v", err)
	}

	if got, want := len(fake.updateMetadataCalls), 2; got != want {
		t.Fatalf("update metadata calls = %d, want %d", got, want)
	}

	firstPresence, ok := fake.updateMetadataCalls[0].Metadata["presence"].(map[string]any)
	if !ok {
		t.Fatalf("expected first presence metadata, got %#v", fake.updateMetadataCalls[0].Metadata["presence"])
	}
	if got, want := firstPresence["transport"], ConnectionTransportHTTPLong; got != want {
		t.Fatalf("first presence transport = %#v, want %q", got, want)
	}

	secondPresence, ok := fake.updateMetadataCalls[1].Metadata["presence"].(map[string]any)
	if !ok {
		t.Fatalf("expected second presence metadata, got %#v", fake.updateMetadataCalls[1].Metadata["presence"])
	}
	if got, want := secondPresence["transport"], ConnectionTransportWebSocket; got != want {
		t.Fatalf("second presence transport = %#v, want %q", got, want)
	}
}

func TestRunHubLoopRetriesWebsocketBeforeHTTPLongPollAfterRealtimeDisconnect(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	service.hubPingRetryDelay = 2 * time.Millisecond
	service.hubPingCheckTimeout = 250 * time.Millisecond
	fake.connectSession = &fakeRealtimeSession{receiveErr: io.EOF}
	fake.pingDetail = "https://na.hub.molten.bot/ping status=204"
	fake.pullOK = false

	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.APIBase = "https://na.hub.molten.bot/v1"
		state.Settings.PollInterval = 10 * time.Millisecond
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.RunHubLoop(ctx)
	}()

	deadline := time.After(2 * time.Second)
	for {
		if fake.connectCalls >= 2 {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("expected websocket reconnect before HTTP fallback; connect_calls=%d pull_calls=%d", fake.connectCalls, fake.pullCalls)
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	<-done
	if fake.pullCalls != 0 {
		t.Fatalf("expected clean websocket disconnect to retry websocket without HTTP fallback, got %d pulls", fake.pullCalls)
	}
}

func TestRunHubLoopMarksPresenceWebsocketWhenRealtimeConnects(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	service.hubPingRetryDelay = 2 * time.Millisecond
	service.hubPingCheckTimeout = 250 * time.Millisecond
	fake.connectSession = &fakeRealtimeSession{receiveErr: io.EOF}
	fake.pingDetail = "https://na.hub.molten.bot/ping status=204"

	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.APIBase = "https://na.hub.molten.bot/v1"
		state.Settings.PollInterval = 10 * time.Millisecond
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.RunHubLoop(ctx)
	}()

	deadline := time.After(2 * time.Second)
	for {
		if len(fake.updateMetadataCalls) > 0 {
			presence, _ := fake.updateMetadataCalls[len(fake.updateMetadataCalls)-1].Metadata["presence"].(map[string]any)
			if presence["transport"] == ConnectionTransportWebSocket {
				break
			}
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("expected websocket presence sync, got %#v", fake.updateMetadataCalls)
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	<-done
}

func TestHandleInboundMessageAcceptsKindWhenTypeIsOmitted(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.AgentUUID = "self-uuid"
		state.PendingTasks = []PendingTask{
			{
				ID:                "task-1",
				ParentRequestID:   "parent-req",
				ChildRequestID:    "child-req",
				OriginalSkillName: "run_task",
				CallerAgentUUID:   "caller-uuid",
				CallerRequestID:   "parent-req",
				Repo:              "/tmp/repo",
				LogPath:           filepath.Join(service.settings.DataDir, "logs", "task-1.log"),
				CreatedAt:         time.Now().Add(-time.Minute),
				ExpiresAt:         time.Now().Add(time.Minute),
			},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	err = service.handleInboundMessage(context.Background(), hub.PullResponse{
		DeliveryID: "delivery-1",
		OpenClawMessage: hub.OpenClawMessage{
			Kind:      "skill_result",
			RequestID: "child-req",
			OK:        boolPtr(true),
			Payload:   map[string]any{"status": "ok"},
		},
	})
	if err != nil {
		t.Fatalf("handle inbound message: %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected one caller publish, got %d", len(fake.publishCalls))
	}
	if fake.publishCalls[0].Message.RequestID != "parent-req" {
		t.Fatalf("unexpected reply request id: %q", fake.publishCalls[0].Message.RequestID)
	}
}

func TestConsumeRealtimeSessionMarksWebsocketConnectivityAndAcksDeliveries(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.AgentUUID = "self-uuid"
		state.PendingTasks = []PendingTask{
			{
				ID:                "task-1",
				ParentRequestID:   "parent-req",
				ChildRequestID:    "child-req",
				OriginalSkillName: "run_task",
				CallerAgentUUID:   "caller-uuid",
				CallerRequestID:   "parent-req",
				Repo:              "/tmp/repo",
				LogPath:           filepath.Join(service.settings.DataDir, "logs", "task-1.log"),
				CreatedAt:         time.Now().Add(-time.Minute),
				ExpiresAt:         time.Now().Add(time.Minute),
			},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	session := &fakeRealtimeSession{
		messages: []hub.PullResponse{
			{
				DeliveryID: "delivery-1",
				OpenClawMessage: hub.OpenClawMessage{
					Kind:      "skill_result",
					RequestID: "child-req",
					OK:        boolPtr(true),
					Payload:   map[string]any{"status": "ok"},
				},
			},
		},
	}

	err = service.consumeRealtimeSession(context.Background(), session)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled sentinel from fake session drain, got %v", err)
	}

	if len(session.acked) != 1 || session.acked[0] != "delivery-1" {
		t.Fatalf("unexpected ack calls: %#v", session.acked)
	}

	state := service.store.Snapshot()
	if state.Connection.Status != ConnectionStatusConnected {
		t.Fatalf("expected connected status, got %#v", state.Connection)
	}
	if state.Connection.Transport != ConnectionTransportWebSocket {
		t.Fatalf("expected websocket transport, got %#v", state.Connection)
	}
}

func TestSetAndConsumeFlashState(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(t)

	if err := service.SetFlash("error", "hub API 401 unauthorized: missing or invalid bearer token"); err != nil {
		t.Fatalf("set flash: %v", err)
	}

	snapshot := service.Snapshot()
	if got := snapshot.Flash.Level; got != "error" {
		t.Fatalf("flash level = %q, want error", got)
	}
	if got := snapshot.Flash.Message; got != "hub API 401 unauthorized: missing or invalid bearer token" {
		t.Fatalf("unexpected flash message: %q", got)
	}

	flash, err := service.ConsumeFlash()
	if err != nil {
		t.Fatalf("consume flash: %v", err)
	}
	if flash.Level != "error" || flash.Message == "" {
		t.Fatalf("unexpected consumed flash: %#v", flash)
	}

	if got := service.Snapshot().Flash; got.Message != "" || got.Level != "" {
		t.Fatalf("expected consumed flash to be cleared, got %#v", got)
	}
}

func TestSetFlashNormalizesInfoLevel(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(t)

	if err := service.SetFlash("warn", "settings updated"); err != nil {
		t.Fatalf("set flash: %v", err)
	}

	flash := service.Snapshot().Flash
	if flash.Level != "info" {
		t.Fatalf("expected info level fallback, got %#v", flash)
	}
	if flash.Message != "settings updated" {
		t.Fatalf("unexpected flash message: %#v", flash)
	}
}

func TestRefreshConnectedAgentsUsesPeerSkillCatalogFromCapabilities(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	ready := true
	fake.capabilitiesResponse = map[string]any{
		"peer_skill_catalog": []any{
			map[string]any{
				"agent_uuid": "self-uuid",
				"agent_id":   "self-agent",
				"uri":        "molten://agent/self",
			},
			map[string]any{
				"agent": map[string]any{
					"agent_uuid": "peer-uuid",
					"agent_id":   "peer-agent",
					"handle":     "peer-agent",
					"uri":        "molten://agent/peer",
					"metadata": map[string]any{
						"display_name": "Peer Agent",
						"emoji":        "🛠",
						"skills": []map[string]any{
							{"name": "review_failure_logs", "description": "Review logs"},
						},
						"presence": map[string]any{
							"status": "online",
							"ready":  ready,
						},
					},
				},
			},
		},
	}
	if err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.APIBase = "https://na.hub.molten.bot/v1"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "molten://agent/self"
		state.Session.Handle = "self-agent"
		return nil
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	agents, err := service.RefreshConnectedAgents(context.Background())
	if err != nil {
		t.Fatalf("refresh connected agents: %v", err)
	}
	if fake.capabilitiesCalls != 1 {
		t.Fatalf("expected one capabilities call, got %d", fake.capabilitiesCalls)
	}
	if len(agents) != 1 {
		t.Fatalf("expected one connected agent, got %#v", agents)
	}
	agent := agents[0]
	if got, want := agent.AgentID, "peer-agent"; got != want {
		t.Fatalf("agent id = %q, want %q", got, want)
	}
	if got, want := connectedAgentDisplayName(agent), "Peer Agent"; got != want {
		t.Fatalf("display name = %q, want %q", got, want)
	}
	if got, want := connectedAgentEmoji(agent), "🛠"; got != want {
		t.Fatalf("emoji = %q, want %q", got, want)
	}
	if got, want := connectedAgentPresenceStatus(agent), "online"; got != want {
		t.Fatalf("presence = %q, want %q", got, want)
	}
	skills := connectedAgentSkills(agent)
	if len(skills) != 1 || skills[0].Name != "review_failure_logs" {
		t.Fatalf("expected skills from hub metadata, got %#v", skills)
	}

	state := service.store.Snapshot()
	if len(state.ConnectedAgents) != 1 || state.ConnectedAgents[0].AgentID != "peer-agent" {
		t.Fatalf("expected connected agents snapshot to be refreshed, got %#v", state.ConnectedAgents)
	}
}

func TestRefreshConnectedAgentsAcceptsPeerCatalogSkillFields(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.capabilitiesResponse = map[string]any{
		"peer_skill_catalog": []any{
			map[string]any{
				"agent_uuid": "self-uuid",
				"agent_id":   "self-agent",
				"uri":        "molten://agent/self",
			},
			map[string]any{
				"agent_uuid": "peer-a-uuid",
				"agent_id":   "peer-a",
				"handle":     "peer-a",
				"uri":        "molten://agent/peer-a",
				"metadata": map[string]any{
					"display_name": "Peer A",
					"advertised_skills": []map[string]any{
						{"name": "review_failure_logs", "description": "Review logs"},
					},
				},
			},
			map[string]any{
				"agent_uuid": "peer-b-uuid",
				"agent_id":   "peer-b",
				"handle":     "peer-b",
				"uri":        "molten://agent/peer-b",
				"advertised_skills": []map[string]any{
					{"name": "review_openapi", "description": "Review Hub API integration behavior."},
				},
			},
		},
	}
	if err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.APIBase = "https://na.hub.molten.bot/v1"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "molten://agent/self"
		state.Session.Handle = "self-agent"
		return nil
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	agents, err := service.RefreshConnectedAgents(context.Background())
	if err != nil {
		t.Fatalf("refresh connected agents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected two peer agents, got %#v", agents)
	}
	if skills := connectedAgentSkills(agents[0]); len(skills) != 1 || skills[0].Name != "review_failure_logs" {
		t.Fatalf("expected metadata.advertised_skills fallback, got %#v", skills)
	}
	if skills := connectedAgentSkills(agents[1]); len(skills) != 1 || skills[0].Name != "review_openapi" {
		t.Fatalf("expected agent.advertised_skills fallback, got %#v", skills)
	}
}

func TestRefreshConnectedAgentsAcceptsHubProfileFieldsAtAgentRoot(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	ready := true
	fake.capabilitiesResponse = map[string]any{
		"peer_skill_catalog": []any{
			map[string]any{
				"agent_uuid": "self-uuid",
				"agent_id":   "self-agent",
				"uri":        "molten://agent/self",
			},
			map[string]any{
				"agent_uuid":   "peer-uuid",
				"agent_id":     "peer-agent",
				"handle":       "peer-agent",
				"uri":          "molten://agent/peer",
				"display_name": "Peer Agent",
				"emoji":        "🛠",
				"presence": map[string]any{
					"status": "online",
					"ready":  ready,
				},
				"skills": []map[string]any{
					{"name": "review_failure_logs", "description": "Review logs"},
				},
			},
		},
	}
	if err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.APIBase = "https://na.hub.molten.bot/v1"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "molten://agent/self"
		state.Session.Handle = "self-agent"
		return nil
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	agents, err := service.RefreshConnectedAgents(context.Background())
	if err != nil {
		t.Fatalf("refresh connected agents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected one connected agent, got %#v", agents)
	}
	agent := agents[0]
	if agent.Metadata == nil {
		t.Fatalf("expected metadata synthesized from root capability fields, got %#v", agent)
	}
	if got, want := connectedAgentDisplayName(agent), "Peer Agent"; got != want {
		t.Fatalf("display name = %q, want %q", got, want)
	}
	if got, want := connectedAgentEmoji(agent), "🛠"; got != want {
		t.Fatalf("emoji = %q, want %q", got, want)
	}
	if got, want := connectedAgentPresenceStatus(agent), "online"; got != want {
		t.Fatalf("presence = %q, want %q", got, want)
	}
	if skills := connectedAgentSkills(agent); len(skills) != 1 || skills[0].Name != "review_failure_logs" {
		t.Fatalf("expected root-level skills from capabilities response, got %#v", skills)
	}
}

func TestRefreshConnectedAgentsReturnsCapabilitiesEndpointError(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.capabilitiesErr = &hub.APIError{
		StatusCode: 401,
		Code:       "unauthorized",
		Message:    "missing or invalid bearer token",
	}
	if err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.APIBase = "https://na.hub.molten.bot/v1"
		return nil
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	_, err := service.RefreshConnectedAgents(context.Background())
	if err == nil {
		t.Fatal("expected refresh error")
	}
	if !strings.Contains(err.Error(), "/v1/agents/me/capabilities") {
		t.Fatalf("expected capabilities route in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "missing or invalid bearer token") {
		t.Fatalf("expected bearer-token detail in error, got %v", err)
	}

	state := service.store.Snapshot()
	if state.Connection.Status != ConnectionStatusDisconnected || state.Connection.Transport != ConnectionTransportOffline {
		t.Fatalf("expected failed refresh to mark connection offline, got %#v", state.Connection)
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
