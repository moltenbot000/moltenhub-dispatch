package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

type fakeHubClient struct {
	mu                    sync.Mutex
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
	connectSessions       []hub.RealtimeSession
	connectSessionKeys    []string
	connectCalls          int
	publishErr            error
}

type fakeHubClientCallCounts struct {
	updateMetadata int
	pull           int
	connect        int
	ping           int
	baseURL        int
}

type fakeRealtimeSession struct {
	messages   []hub.PullResponse
	receiveErr error
	acked      []string
	nacked     []string
}

func (f *fakeHubClient) BindAgent(_ context.Context, req hub.BindRequest) (hub.BindResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bindRequests = append(f.bindRequests, req)
	return f.bindResponse, nil
}

func (f *fakeHubClient) UpdateMetadata(_ context.Context, _ string, req hub.UpdateMetadataRequest) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capabilitiesCalls++
	if f.capabilitiesErr != nil && (f.capabilitiesErrOnCall == 0 || f.capabilitiesErrOnCall == f.capabilitiesCalls) {
		return nil, f.capabilitiesErr
	}
	if f.capabilitiesResponse != nil {
		return f.capabilitiesResponse, nil
	}
	return map[string]any{"advertised_skills": []any{}}, nil
}

func (f *fakeHubClient) PublishRuntimeMessage(_ context.Context, _ string, req hub.PublishRequest) (hub.PublishResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishCalls = append(f.publishCalls, req)
	if f.publishErr != nil {
		return hub.PublishResponse{}, f.publishErr
	}
	return hub.PublishResponse{MessageID: "message-1"}, nil
}

func (f *fakeHubClient) PullRuntimeMessage(_ context.Context, _ string, _ time.Duration) (hub.PullResponse, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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

func (f *fakeHubClient) AckRuntimeMessage(_ context.Context, _ string, _ string) error {
	return nil
}

func (f *fakeHubClient) NackRuntimeMessage(_ context.Context, _ string, _ string) error {
	return nil
}

func (f *fakeHubClient) MarkOffline(_ context.Context, _ string, req hub.OfflineRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	f.mu.Lock()
	defer f.mu.Unlock()
	f.currentBaseURL = baseURL
	f.baseURLCalls = append(f.baseURLCalls, baseURL)
}

func (f *fakeHubClient) ConnectRuntimeMessages(_ context.Context, _ string, sessionKey string) (hub.RealtimeSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectCalls++
	f.connectSessionKeys = append(f.connectSessionKeys, sessionKey)
	if len(f.baseURLCalls) == 0 {
		f.baseURLCalls = append(f.baseURLCalls, f.currentBaseURL)
	}
	if len(f.connectSessions) > 0 && f.connectErr == nil {
		session := f.connectSessions[0]
		f.connectSessions = f.connectSessions[1:]
		return session, nil
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
	f.mu.Lock()
	defer f.mu.Unlock()
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

func (f *fakeHubClient) callCounts() fakeHubClientCallCounts {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fakeHubClientCallCounts{
		updateMetadata: len(f.updateMetadataCalls),
		pull:           f.pullCalls,
		connect:        f.connectCalls,
		ping:           f.pingCalls,
		baseURL:        len(f.baseURLCalls),
	}
}

func (f *fakeHubClient) updateMetadataRequests() []hub.UpdateMetadataRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]hub.UpdateMetadataRequest(nil), f.updateMetadataCalls...)
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
	fake.bindResponse.Endpoints.RuntimePull = "https://runtime.na.hub.molten.bot/v1/runtime/messages/pull"
	fake.bindResponse.Endpoints.RuntimePush = "https://runtime.na.hub.molten.bot/v1/runtime/messages/publish"
	fake.bindResponse.Endpoints.RuntimeOffline = "https://runtime.na.hub.molten.bot/v1/runtime/messages/offline"

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

func TestRefreshAgentProfileFetchesAndPersistsCapabilitiesIdentity(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	if err := service.store.Update(func(state *AppState) error {
		state.Session = Session{
			AgentToken:      "agent-token",
			Handle:          "",
			HandleFinalized: true,
			DisplayName:     "",
			Emoji:           "",
			ProfileBio:      "",
		}
		return nil
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	fake.capabilitiesResponse = map[string]any{
		"metadata": map[string]any{
			"handle":           "dispatch-agent",
			"display_name":     "Dispatch Agent",
			"emoji":            "🤖",
			"profile_markdown": "Dispatches skill requests.",
		},
	}

	profile, err := service.RefreshAgentProfile(context.Background())
	if err != nil {
		t.Fatalf("refresh agent profile: %v", err)
	}

	if profile.Handle != "dispatch-agent" || profile.DisplayName != "Dispatch Agent" {
		t.Fatalf("unexpected profile: %#v", profile)
	}
	state := service.store.Snapshot()
	if state.Session.Handle != "dispatch-agent" {
		t.Fatalf("expected persisted handle, got %q", state.Session.Handle)
	}
	if state.Session.DisplayName != "Dispatch Agent" || state.Session.Emoji != "🤖" || state.Session.ProfileBio != "Dispatches skill requests." {
		t.Fatalf("unexpected persisted session profile: %#v", state.Session)
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
	if strings.Contains(string(configData), "\"bind_token\"") {
		t.Fatalf("did not expect persisted bind_token alias in config.json, got %s", string(configData))
	}
	if strings.Contains(string(configData), "\"base_url\"") {
		t.Fatalf("did not expect persisted base_url in config.json, got %s", string(configData))
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
	if failurePayload["detail"] != "no connected agent matched \"missing-agent\"" {
		t.Fatalf("unexpected caller detail: %#v", failurePayload["detail"])
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

func TestHandleDispatchResolutionFailureUsesReplyTargetWhenCallerMetadataMissing(t *testing.T) {
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

	replyTarget := "molten://agent/caller-reply-target"
	message := hub.PullResponse{
		DeliveryID: "delivery-1",
		OpenClawMessage: hub.OpenClawMessage{
			Type:        "skill_request",
			SkillName:   dispatchSkillName,
			RequestID:   "parent-req",
			ReplyTarget: replyTarget,
			Payload: map[string]any{
				"target_agent_uuid": "missing-agent",
				"skill_name":        "run_task",
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
		t.Fatalf("expected one caller failure publish, got %d", len(fake.publishCalls))
	}
	if got := fake.publishCalls[0].ToAgentURI; got != replyTarget {
		t.Fatalf("expected failure response to use reply_target URI %q, got %q", replyTarget, got)
	}
	if got := fake.publishCalls[0].ToAgentUUID; got != "" {
		t.Fatalf("expected no caller UUID when reply_target URI is used, got %q", got)
	}
	failurePayload, ok := fake.publishCalls[0].Message.Payload.(map[string]any)
	if !ok {
		t.Fatalf("unexpected caller failure payload type: %T", fake.publishCalls[0].Message.Payload)
	}
	if got := failurePayload["status"]; got != "failed" {
		t.Fatalf("expected failed status in caller failure payload, got %#v", got)
	}
	if got := failurePayload["error_detail"]; got == nil {
		t.Fatalf("expected error_detail in caller failure payload, got %#v", failurePayload)
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
	if got := failurePayload["message"]; got != "Task failed while dispatching to a connected agent. Error: task execution failed" {
		t.Fatalf("unexpected caller failure message: %#v", got)
	}
	if got := failurePayload["Failure:"]; got != "Task failed while dispatching to a connected agent. Error: task execution failed" {
		t.Fatalf("unexpected caller Failure: field: %#v", got)
	}
	if got := failurePayload["Error details:"]; got == "" {
		t.Fatalf("expected caller Error details: field, got %#v", got)
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
	if len(state.RecentEvents) == 0 {
		t.Fatal("expected downstream failure to append recent activity event")
	}
	if got := state.RecentEvents[0].Title; got != "Task failed" {
		t.Fatalf("expected failure event title, got %#v", got)
	}
	if got := state.RecentEvents[0].Level; got != "error" {
		t.Fatalf("expected failure event level, got %#v", got)
	}
	if got := state.RecentEvents[0].TaskID; got != "task-1" {
		t.Fatalf("expected failure event task id, got %#v", got)
	}
}

func TestHandleDownstreamSuccessAppendsCompletionRecentActivity(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "molten://dispatch/self"
		state.PendingTasks = []PendingTask{
			{
				ID:                     "task-1",
				ParentRequestID:        "parent-req",
				ChildRequestID:         "child-req",
				OriginalSkillName:      "run_task",
				TargetAgentDisplayName: "Worker A",
				TargetAgentEmoji:       "🛠",
				TargetAgentUUID:        "worker-uuid",
				CallerAgentUUID:        "caller-uuid",
				CallerRequestID:        "parent-req",
				LogPath:                filepath.Join(service.settings.DataDir, "logs", "task-1.log"),
				CreatedAt:              time.Now().Add(-time.Minute),
				ExpiresAt:              time.Now().Add(time.Minute),
			},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	if err := service.handleInboundMessage(context.Background(), hub.PullResponse{
		DeliveryID: "delivery-1",
		OpenClawMessage: hub.OpenClawMessage{
			Type:      "skill_result",
			SkillName: "run_task",
			RequestID: "child-req",
			ReplyTo:   "parent-req",
			OK:        boolPtr(true),
			Payload:   map[string]any{"status": "succeeded"},
		},
	}); err != nil {
		t.Fatalf("handle inbound success message: %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected successful result to be forwarded to caller, got %d publishes", len(fake.publishCalls))
	}
	if got := fake.publishCalls[0].Message.RequestID; got != "parent-req" {
		t.Fatalf("expected forwarded result request id parent-req, got %q", got)
	}
	if got := fake.publishCalls[0].Message.ReplyTo; got != "parent-req" {
		t.Fatalf("expected forwarded result reply_to parent-req, got %q", got)
	}

	state := service.store.Snapshot()
	if len(state.PendingTasks) != 0 {
		t.Fatalf("expected pending task to clear after success handling, got %d", len(state.PendingTasks))
	}
	if len(state.RecentEvents) == 0 {
		t.Fatal("expected downstream success to append recent activity event")
	}
	if got := state.RecentEvents[0].Title; got != "Task completed" {
		t.Fatalf("expected completion event title, got %#v", got)
	}
	if got := state.RecentEvents[0].Level; got != "info" {
		t.Fatalf("expected completion event level, got %#v", got)
	}
	if got := state.RecentEvents[0].OriginalSkillName; got != "run_task" {
		t.Fatalf("expected completion event skill name, got %#v", got)
	}
	if got := state.RecentEvents[0].TargetAgentDisplayName; got != "Worker A" {
		t.Fatalf("expected completion event target display name, got %#v", got)
	}
	if got := state.RecentEvents[0].TargetAgentEmoji; got != "🛠" {
		t.Fatalf("expected completion event target emoji, got %#v", got)
	}
}

func TestHandleTaskStatusUpdateRecordsDownstreamProgress(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.PendingTasks = []PendingTask{
			{
				ID:                     "task-1",
				ParentRequestID:        "parent-req",
				ChildRequestID:         "child-req",
				HubTaskID:              "hub-task-1",
				OriginalSkillName:      "run_task",
				TargetAgentDisplayName: "Worker A",
				TargetAgentUUID:        "worker-uuid",
				LogPath:                filepath.Join(service.settings.DataDir, "logs", "task-1.log"),
				CreatedAt:              time.Now().Add(-time.Minute),
				ExpiresAt:              time.Now().Add(time.Minute),
			},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	if err := service.handleInboundMessage(context.Background(), hub.PullResponse{
		DeliveryID: "delivery-1",
		MessageID:  "status-message-1",
		OpenClawMessage: hub.OpenClawMessage{
			Protocol:  "a2a.v1",
			Type:      "task_status_update",
			RequestID: "child-req",
			Status:    "working",
			A2AState:  "TASK_STATE_WORKING",
			Message:   "Task running.",
			StatusUpdate: map[string]any{
				"taskId":    "hub-task-1",
				"contextId": "parent-req",
				"status": map[string]any{
					"state": "TASK_STATE_WORKING",
					"message": map[string]any{
						"parts": []any{map[string]any{"text": "Task running."}},
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("handle task status update: %v", err)
	}

	if len(fake.publishCalls) != 0 {
		t.Fatalf("expected no caller result publish for progress update, got %d", len(fake.publishCalls))
	}
	state := service.store.Snapshot()
	if len(state.PendingTasks) != 1 {
		t.Fatalf("expected pending task to remain, got %d", len(state.PendingTasks))
	}
	pending := state.PendingTasks[0]
	if pending.DownstreamStatus != "working" {
		t.Fatalf("downstream status = %q, want working", pending.DownstreamStatus)
	}
	if pending.DownstreamTaskState != "TASK_STATE_WORKING" {
		t.Fatalf("downstream task state = %q, want TASK_STATE_WORKING", pending.DownstreamTaskState)
	}
	if pending.DownstreamMessage != "Task running." {
		t.Fatalf("downstream message = %q, want Task running.", pending.DownstreamMessage)
	}
	if pending.DownstreamUpdatedAt.IsZero() {
		t.Fatal("expected downstream updated timestamp")
	}
	if len(state.RecentEvents) == 0 {
		t.Fatal("expected progress recent event")
	}
	if got := state.RecentEvents[0].Title; got != "Task progress" {
		t.Fatalf("event title = %q, want Task progress", got)
	}
	if got := state.RecentEvents[0].Detail; got != "Task running." {
		t.Fatalf("event detail = %q, want Task running.", got)
	}
	if got := state.RecentEvents[0].HubTaskID; got != "hub-task-1" {
		t.Fatalf("event hub task id = %q, want hub-task-1", got)
	}
	if got := state.RecentEvents[0].ChildRequestID; got != "child-req" {
		t.Fatalf("event child request id = %q, want child-req", got)
	}
}

func TestHandleUnmatchedTaskMessagesKeepRequestAlias(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(t)
	if err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		return nil
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	if err := service.handleInboundMessage(context.Background(), hub.PullResponse{
		DeliveryID: "delivery-1",
		OpenClawMessage: hub.OpenClawMessage{
			Type:      "task_status_update",
			RequestID: "child-req",
			Status:    "working",
		},
	}); err != nil {
		t.Fatalf("handle unmatched status update: %v", err)
	}

	if err := service.handleInboundMessage(context.Background(), hub.PullResponse{
		DeliveryID: "delivery-2",
		OpenClawMessage: hub.OpenClawMessage{
			Type:      "skill_result",
			RequestID: "child-req",
			OK:        boolPtr(true),
		},
	}); err != nil {
		t.Fatalf("handle unmatched skill result: %v", err)
	}

	state := service.store.Snapshot()
	if len(state.RecentEvents) < 2 {
		t.Fatalf("expected unmatched recent events, got %#v", state.RecentEvents)
	}
	for _, event := range state.RecentEvents[:2] {
		if event.ChildRequestID != "child-req" {
			t.Fatalf("event child request id = %q, want child-req in %#v", event.ChildRequestID, event)
		}
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
	if got := failurePayload["message"]; got != "Task failed while dispatching to a connected agent. Error: error connecting to api.github.com" {
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
	if detail, ok := failurePayload["detail"].(string); !ok || !strings.Contains(detail, "githubstatus.com") {
		t.Fatalf("expected caller detail to include network diagnostic, got %#v", failurePayload["detail"])
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

func TestPublishFailureToCallerStillPublishesWhenTaskLogWriteFails(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	logDir := filepath.Join(service.settings.DataDir, "logs", "task-log-dir")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("create task log dir: %v", err)
	}

	err = service.publishFailureToCaller(context.Background(), service.store.Snapshot(), PendingTask{
		ID:                "task-1",
		ParentRequestID:   "parent-req",
		CallerAgentUUID:   "caller-uuid",
		CallerRequestID:   "parent-req",
		OriginalSkillName: "run_task",
		LogPath:           logDir,
	}, failureFromError("Task dispatch failed before it reached a connected agent.", errors.New("downstream panic")))
	if err != nil {
		t.Fatalf("publish failure to caller: %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected one caller failure publish, got %d", len(fake.publishCalls))
	}
	message := fake.publishCalls[0].Message
	if got := strings.ToLower(strings.TrimSpace(message.Status)); got != "failed" {
		t.Fatalf("expected failed message status, got %#v", message)
	}
	if !strings.Contains(strings.ToLower(message.Error), "failed") {
		t.Fatalf("expected failure message summary to clearly state failure, got %#v", message.Error)
	}
	payload, ok := message.Payload.(map[string]any)
	if !ok {
		t.Fatalf("unexpected caller failure payload type: %T", message.Payload)
	}
	if got := payload["status"]; got != "failed" {
		t.Fatalf("expected payload status=failed, got %#v", got)
	}
	if got := payload["error_detail"]; got == nil {
		t.Fatalf("expected payload error_detail, got %#v", payload)
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
	worker := testConnectedAgent("worker-a", "Worker A", "worker-uuid", Skill{Name: "run_task"})
	worker.Metadata.Emoji = "🛠"
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.ConnectedAgents = []ConnectedAgent{worker}
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
	if got := task.Status; got != PendingTaskStatusInQueue {
		t.Fatalf("expected task status %q, got %#v", PendingTaskStatusInQueue, task)
	}
	if got := task.HubTaskID; got != "message-1" {
		t.Fatalf("expected hub task id message-1, got %#v", task)
	}
	if got := task.TargetAgentDisplayName; got != "Worker A" {
		t.Fatalf("expected target display name, got %#v", task)
	}
	if got := task.TargetAgentEmoji; got != "🛠" {
		t.Fatalf("expected target emoji, got %#v", task)
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

	state := service.store.Snapshot()
	if len(state.RecentEvents) == 0 {
		t.Fatal("expected dispatch to append recent event")
	}
	if got := state.RecentEvents[0].OriginalSkillName; got != "run_task" {
		t.Fatalf("expected recent event skill name, got %#v", state.RecentEvents[0])
	}
	if got := state.RecentEvents[0].TargetAgentDisplayName; got != "Worker A" {
		t.Fatalf("expected recent event target display name, got %#v", state.RecentEvents[0])
	}
	if got := state.RecentEvents[0].TargetAgentEmoji; got != "🛠" {
		t.Fatalf("expected recent event target emoji, got %#v", state.RecentEvents[0])
	}
}

func TestDispatchFromUIPassesA2APreferenceToHubPublish(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
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

	task, err := service.DispatchFromUI(context.Background(), DispatchRequest{
		RequestID:      "a2a-msg-1",
		TargetAgentRef: "worker-a",
		SkillName:      "run_task",
		Payload:        testDispatchPrompt,
		PayloadFormat:  "markdown",
		PreferA2A:      true,
	})
	if err != nil {
		t.Fatalf("dispatch from ui: %v", err)
	}
	if !task.PreferA2A {
		t.Fatal("expected task to retain A2A preference")
	}
	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected one publish call, got %d", len(fake.publishCalls))
	}
	if !fake.publishCalls[0].PreferA2A {
		t.Fatal("expected hub publish to prefer A2A")
	}
}

func TestDispatchFromUISchedulesMessageWithoutImmediatePublish(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	worker := testConnectedAgent("worker-a", "Worker A", "worker-uuid", Skill{Name: "run_task"})
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.ConnectedAgents = []ConnectedAgent{worker}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	runAt := time.Now().UTC().Add(time.Hour)
	task, err := service.DispatchFromUI(context.Background(), DispatchRequest{
		RequestID:      "ui-req",
		TargetAgentRef: "worker-a",
		SkillName:      "run_task",
		Payload:        map[string]any{"input": "scheduled work"},
		PayloadFormat:  "json",
		ScheduledAt:    runAt,
		Frequency:      15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("schedule dispatch from ui: %v", err)
	}

	if task.Status != ScheduledMessageStatusActive {
		t.Fatalf("expected scheduled task status, got %#v", task.Status)
	}
	if len(fake.publishCalls) != 0 {
		t.Fatalf("expected no immediate publish, got %d", len(fake.publishCalls))
	}
	state := service.store.Snapshot()
	if len(state.ScheduledMessages) != 1 {
		t.Fatalf("expected one scheduled message, got %d", len(state.ScheduledMessages))
	}
	if got := state.ScheduledMessages[0].Frequency; got != 15*time.Minute {
		t.Fatalf("unexpected frequency: %v", got)
	}
	if got, want := state.ScheduledMessages[0].Cron, "*/15 * * * *"; got != want {
		t.Fatalf("unexpected cron: %q, want %q", got, want)
	}
	if got := state.ScheduledMessages[0].TargetAgentUUID; got != "worker-uuid" {
		t.Fatalf("unexpected scheduled target: %#v", state.ScheduledMessages[0])
	}
}

func TestDispatchFromUISchedulesSecondRepeatCron(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	worker := testConnectedAgent("worker-a", "Worker A", "worker-uuid", Skill{Name: "run_task"})
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.ConnectedAgents = []ConnectedAgent{worker}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	_, err = service.DispatchFromUI(context.Background(), DispatchRequest{
		RequestID:      "ui-req",
		TargetAgentRef: "worker-a",
		SkillName:      "run_task",
		ScheduledAt:    time.Now().UTC().Add(time.Minute),
		Frequency:      30 * time.Second,
	})
	if err != nil {
		t.Fatalf("schedule dispatch from ui: %v", err)
	}
	if len(fake.publishCalls) != 0 {
		t.Fatalf("expected no immediate publish, got %d", len(fake.publishCalls))
	}
	state := service.store.Snapshot()
	if len(state.ScheduledMessages) != 1 {
		t.Fatalf("expected one scheduled message, got %d", len(state.ScheduledMessages))
	}
	if got, want := state.ScheduledMessages[0].Cron, "*/30 * * * * *"; got != want {
		t.Fatalf("unexpected cron: %q, want %q", got, want)
	}
}

func TestProcessDueScheduledMessagesPublishesAndAdvancesRecurringSchedule(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	worker := testConnectedAgent("worker-a", "Worker A", "worker-uuid", Skill{Name: "run_task"})
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.ConnectedAgents = []ConnectedAgent{worker}
		state.ScheduledMessages = []ScheduledMessage{
			{
				ID:                    "schedule-1",
				Status:                ScheduledMessageStatusActive,
				ParentRequestID:       "parent-req",
				OriginalSkillName:     "run_task",
				TargetAgentRef:        "worker-a",
				TargetAgentUUID:       "worker-uuid",
				CallerAgentUUID:       "caller-uuid",
				CallerRequestID:       "parent-req",
				NextRunAt:             time.Now().UTC().Add(-time.Minute),
				Frequency:             10 * time.Minute,
				DispatchPayload:       map[string]any{"input": "scheduled work"},
				DispatchPayloadFormat: "json",
			},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	if err := service.processDueScheduledMessages(context.Background()); err != nil {
		t.Fatalf("process scheduled messages: %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected one scheduled publish, got %d", len(fake.publishCalls))
	}
	if got := fake.publishCalls[0].ToAgentUUID; got != "worker-uuid" {
		t.Fatalf("unexpected publish target: %#v", fake.publishCalls[0])
	}
	if got := fake.publishCalls[0].Message.SkillName; got != "run_task" {
		t.Fatalf("unexpected scheduled skill: %q", got)
	}
	state := service.store.Snapshot()
	if len(state.ScheduledMessages) != 1 {
		t.Fatalf("expected recurring schedule to remain, got %d", len(state.ScheduledMessages))
	}
	if !state.ScheduledMessages[0].NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("expected next run to advance, got %s", state.ScheduledMessages[0].NextRunAt)
	}
	if len(state.PendingTasks) != 1 {
		t.Fatalf("expected pending task for scheduled dispatch, got %d", len(state.PendingTasks))
	}
}

func TestProcessDueScheduledMessagesUsesPersistedTargetAfterRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	settings := DefaultSettings()
	settings.DataDir = dir
	store, err := NewStore(filepath.Join(dir, "config.json"), settings)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatalf("create logs dir: %v", err)
	}
	err = store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.ScheduledMessages = []ScheduledMessage{
			{
				ID:                     "schedule-1",
				Status:                 ScheduledMessageStatusActive,
				ParentRequestID:        "parent-req",
				OriginalSkillName:      "run_task",
				TargetAgentRef:         "worker-a",
				TargetAgentDisplayName: "Worker A",
				TargetAgentEmoji:       "🛠",
				TargetAgentUUID:        "worker-uuid",
				TargetAgentURI:         "molten://agent/worker-a",
				CallerRequestID:        "parent-req",
				NextRunAt:              time.Now().UTC().Add(-time.Minute),
				DispatchPayload:        map[string]any{"input": "scheduled work"},
				DispatchPayloadFormat:  "json",
			},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	reloadedStore, err := NewStore(filepath.Join(dir, "config.json"), settings)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}
	fake := &fakeHubClient{}
	service := NewService(reloadedStore, fake)

	if err := service.processDueScheduledMessages(context.Background()); err != nil {
		t.Fatalf("process scheduled messages: %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected one scheduled publish, got %d", len(fake.publishCalls))
	}
	if got := fake.publishCalls[0].ToAgentUUID; got != "worker-uuid" {
		t.Fatalf("unexpected publish target uuid: %#v", fake.publishCalls[0])
	}
	if got := fake.publishCalls[0].ToAgentURI; got != "molten://agent/worker-a" {
		t.Fatalf("unexpected publish target uri: %#v", fake.publishCalls[0])
	}
	state := service.store.Snapshot()
	if len(state.ScheduledMessages) != 0 {
		t.Fatalf("expected one-time schedule to be removed, got %d", len(state.ScheduledMessages))
	}
	if len(state.PendingTasks) != 1 {
		t.Fatalf("expected pending task for scheduled dispatch, got %d", len(state.PendingTasks))
	}
	if got := state.PendingTasks[0].TargetAgentDisplayName; got != "Worker A" {
		t.Fatalf("expected stored target display name, got %#v", state.PendingTasks[0])
	}
}

func TestDeleteScheduledMessageRemovesSchedule(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.ScheduledMessages = []ScheduledMessage{
			{ID: "schedule-1", OriginalSkillName: "run_task", TargetAgentDisplayName: "Worker A", NextRunAt: time.Now().UTC().Add(time.Hour)},
			{ID: "schedule-2", OriginalSkillName: "run_task", TargetAgentDisplayName: "Worker B", NextRunAt: time.Now().UTC().Add(time.Hour)},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	if err := service.DeleteScheduledMessage("schedule-1"); err != nil {
		t.Fatalf("delete schedule: %v", err)
	}

	state := service.store.Snapshot()
	if len(state.ScheduledMessages) != 1 || state.ScheduledMessages[0].ID != "schedule-2" {
		t.Fatalf("unexpected schedules after delete: %#v", state.ScheduledMessages)
	}
	if len(state.RecentEvents) == 0 || state.RecentEvents[0].Title != "Scheduled message deleted" {
		t.Fatalf("expected delete event, got %#v", state.RecentEvents)
	}
	if err := service.DeleteScheduledMessage("missing-schedule"); err == nil {
		t.Fatal("expected missing schedule error")
	}
}

func TestHandleSkillRequestSchedulesRecurringMessageAndAcknowledgesCaller(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	worker := testConnectedAgent("worker-a", "Worker A", "worker-uuid", Skill{Name: "run_task"})
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.ConnectedAgents = []ConnectedAgent{worker}
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
				"agent":          "worker-a",
				"skill_name":     "run_task",
				"message":        "scheduled work",
				"frequency":      "30m",
				"payload_format": "markdown",
			},
		},
	}

	if err := service.handleInboundMessage(context.Background(), message); err != nil {
		t.Fatalf("handle scheduled inbound message: %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected one caller ack publish, got %d", len(fake.publishCalls))
	}
	if got := fake.publishCalls[0].ToAgentUUID; got != "caller-uuid" {
		t.Fatalf("unexpected ack target: %q", got)
	}
	ackPayload, ok := fake.publishCalls[0].Message.Payload.(map[string]any)
	if !ok {
		t.Fatalf("unexpected ack payload type: %T", fake.publishCalls[0].Message.Payload)
	}
	if got := ackPayload["scheduled"]; got != true {
		t.Fatalf("expected scheduled ack payload, got %#v", ackPayload)
	}
	state := service.store.Snapshot()
	if len(state.ScheduledMessages) != 1 {
		t.Fatalf("expected one scheduled message, got %d", len(state.ScheduledMessages))
	}
	if got := state.ScheduledMessages[0].DispatchPayload["input"]; got != "scheduled work" {
		t.Fatalf("unexpected scheduled payload: %#v", state.ScheduledMessages[0].DispatchPayload)
	}
}

func TestHandleSkillRequestAcceptsNestedScheduleObject(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	worker := testConnectedAgent("worker-a", "Worker A", "worker-uuid", Skill{Name: "run_task"})
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.ConnectedAgents = []ConnectedAgent{worker}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	before := time.Now().UTC()
	message := hub.PullResponse{
		DeliveryID:    "delivery-1",
		FromAgentUUID: "caller-uuid",
		OpenClawMessage: hub.OpenClawMessage{
			Type:      "skill_request",
			SkillName: dispatchSkillName,
			RequestID: "parent-req",
			Payload: map[string]any{
				"agent":      "worker-a",
				"skill_name": "run_task",
				"payload": map[string]any{
					"input": "scheduled work",
				},
				"schedule": map[string]any{
					"after": 900,
					"every": "45m",
				},
			},
		},
	}

	if err := service.handleInboundMessage(context.Background(), message); err != nil {
		t.Fatalf("handle nested schedule inbound message: %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected caller schedule ack, got %d publishes", len(fake.publishCalls))
	}
	state := service.store.Snapshot()
	if len(state.ScheduledMessages) != 1 {
		t.Fatalf("expected one scheduled message, got %d", len(state.ScheduledMessages))
	}
	scheduled := state.ScheduledMessages[0]
	if got := scheduled.Frequency; got != 45*time.Minute {
		t.Fatalf("frequency = %v, want 45m", got)
	}
	if scheduled.NextRunAt.Before(before.Add(14*time.Minute)) || scheduled.NextRunAt.After(before.Add(16*time.Minute)) {
		t.Fatalf("next run = %s, want about 15m after %s", scheduled.NextRunAt, before)
	}
	if got := scheduled.DispatchPayload["input"]; got != "scheduled work" {
		t.Fatalf("unexpected scheduled payload: %#v", scheduled.DispatchPayload)
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
	worker := testConnectedAgent("worker-a", "Worker A", "worker-uuid", Skill{Name: "run_task"})
	worker.Metadata.Emoji = "🛠"
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.ConnectedAgents = []ConnectedAgent{worker}
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
	if got := state.PendingTasks[0].Status; got != PendingTaskStatusInQueue {
		t.Fatalf("unexpected pending task status: %#v", state.PendingTasks[0])
	}
	if got := state.PendingTasks[0].HubTaskID; got != "message-1" {
		t.Fatalf("unexpected pending task hub task id: %#v", state.PendingTasks[0])
	}
	if got := state.PendingTasks[0].TargetAgentDisplayName; got != "Worker A" {
		t.Fatalf("unexpected pending task display name: %#v", state.PendingTasks[0])
	}
	if got := state.PendingTasks[0].TargetAgentEmoji; got != "🛠" {
		t.Fatalf("unexpected pending task emoji: %#v", state.PendingTasks[0])
	}
	if len(state.RecentEvents) == 0 {
		t.Fatal("expected forwarded dispatch event in recent activity")
	}
	if got := state.RecentEvents[0].OriginalSkillName; got != "run_task" {
		t.Fatalf("expected recent event skill name, got %#v", state.RecentEvents[0])
	}
	if got := state.RecentEvents[0].TargetAgentDisplayName; got != "Worker A" {
		t.Fatalf("expected recent event target display name, got %#v", state.RecentEvents[0])
	}
	if got := state.RecentEvents[0].TargetAgentEmoji; got != "🛠" {
		t.Fatalf("expected recent event target emoji, got %#v", state.RecentEvents[0])
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

func TestHandleSkillRequestDecodesJSONStringTaskPayloadWhenFormatJSON(t *testing.T) {
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
				"target_agent_ref": "worker-a",
				"skill_name":       "code_for_me",
				"payload":          `{"prompt":"` + testDispatchPrompt + `","retry":true}`,
				"payload_format":   "json",
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
	if got := fake.publishCalls[0].Message.PayloadFormat; got != "json" {
		t.Fatalf("expected downstream payload format json, got %q", got)
	}

	payload, ok := fake.publishCalls[0].Message.Payload.(map[string]any)
	if !ok {
		t.Fatalf("expected downstream payload map, got %T", fake.publishCalls[0].Message.Payload)
	}
	if got := payload["prompt"]; got != testDispatchPrompt {
		t.Fatalf("unexpected downstream prompt payload: %#v", payload)
	}
	if got := payload["retry"]; got != true {
		t.Fatalf("unexpected downstream retry payload: %#v", payload)
	}
	if _, exists := payload["input"]; exists {
		t.Fatalf("did not expect wrapped markdown input field for JSON payload: %#v", payload)
	}
}

func TestHandleSkillRequestDecodesJSONStringTaskPayloadWithTabbedPromptWhenFormatJSON(t *testing.T) {
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
				"target_agent_ref": "worker-a",
				"skill_name":       "code_for_me",
				"payload":          "{\"prompt\":\"Review\tlogs\"}",
				"payload_format":   "json",
			},
		},
	}

	if err := service.handleInboundMessage(context.Background(), message); err != nil {
		t.Fatalf("handle inbound message: %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected one downstream publish call, got %d", len(fake.publishCalls))
	}
	if got := fake.publishCalls[0].Message.PayloadFormat; got != "json" {
		t.Fatalf("expected downstream payload format json, got %q", got)
	}
	payload, ok := fake.publishCalls[0].Message.Payload.(map[string]any)
	if !ok {
		t.Fatalf("expected downstream payload map, got %T", fake.publishCalls[0].Message.Payload)
	}
	if got := payload["prompt"]; got != "Review\tlogs" {
		t.Fatalf("unexpected downstream prompt payload: %#v", payload)
	}
}

func TestHandleSkillRequestRejectsInvalidJSONStringTaskPayloadWhenFormatJSON(t *testing.T) {
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
				"target_agent_ref": "worker-a",
				"skill_name":       "code_for_me",
				"payload":          `{"prompt":`,
				"payload_format":   "json",
			},
		},
	}

	if err := service.handleInboundMessage(context.Background(), message); err != nil {
		t.Fatalf("handle inbound message: %v", err)
	}

	if len(fake.publishCalls) != 1 {
		t.Fatalf("expected one caller failure publish, got %d", len(fake.publishCalls))
	}
	if got := fake.publishCalls[0].ToAgentUUID; got != "caller-uuid" {
		t.Fatalf("expected caller failure target UUID, got %q", got)
	}
	if got := fake.publishCalls[0].Message.Type; got != "skill_result" {
		t.Fatalf("expected skill_result failure response, got %q", got)
	}

	payload, ok := fake.publishCalls[0].Message.Payload.(map[string]any)
	if !ok {
		t.Fatalf("unexpected caller failure payload type: %T", fake.publishCalls[0].Message.Payload)
	}
	if got := payload["status"]; got != "failed" {
		t.Fatalf("expected failed status payload, got %#v", got)
	}
	summary, ok := payload["Failure:"].(string)
	if !ok || !strings.Contains(summary, "Failed to decode the dispatch request payload.") {
		t.Fatalf("unexpected Failure: payload field: %#v", payload["Failure:"])
	}
	detail, ok := payload["Error details:"].(string)
	if !ok || !strings.Contains(detail, "payload_format json requires valid JSON payload") {
		t.Fatalf("unexpected Error details: payload field: %#v", payload["Error details:"])
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
	if payload["message"] != "Task failed: downstream worker returned a non-zero exit code. Error: panic: boom" {
		t.Fatalf("unexpected failure message: %#v", payload["message"])
	}
	if payload["ok"] != false {
		t.Fatalf("expected ok=false, got %#v", payload["ok"])
	}
	if payload["failure"] != true {
		t.Fatalf("expected failure marker, got %#v", payload["failure"])
	}
	if got := payload["Failure"]; got != "Task failed: downstream worker returned a non-zero exit code. Error: panic: boom" {
		t.Fatalf("unexpected Failure field payload: %#v", got)
	}
	if got := payload["Failure:"]; got != "Task failed: downstream worker returned a non-zero exit code. Error: panic: boom" {
		t.Fatalf("unexpected Failure: field payload: %#v", got)
	}
	detail, ok := payload["error_details"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected error_details type: %T", payload["error_details"])
	}
	if detail["stderr"] != "stacktrace" || detail["exit_code"] != 1 {
		t.Fatalf("unexpected error_details payload: %#v", detail)
	}
	errorDetailsField, ok := payload["Error details"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected Error details field type: %T", payload["Error details"])
	}
	if errorDetailsField["stderr"] != "stacktrace" || errorDetailsField["exit_code"] != 1 {
		t.Fatalf("unexpected Error details field payload: %#v", errorDetailsField)
	}
	errorDetailsWithColon, ok := payload["Error details:"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected Error details: field type: %T", payload["Error details:"])
	}
	if errorDetailsWithColon["stderr"] != "stacktrace" || errorDetailsWithColon["exit_code"] != 1 {
		t.Fatalf("unexpected Error details: field payload: %#v", errorDetailsWithColon)
	}
}

func TestCallerFailurePayloadFallsBackToFailureSummaryWhenDetailMissing(t *testing.T) {
	t.Parallel()

	payload := callerFailurePayload(failureReport{}, nil)

	if got := payload["Failure:"]; got != "Task failed." {
		t.Fatalf("unexpected fallback Failure: payload: %#v", got)
	}
	if got := payload["Error details:"]; got != "Task failed." {
		t.Fatalf("unexpected fallback Error details: payload: %#v", got)
	}
	if got := payload["error_details"]; got != "Task failed." {
		t.Fatalf("unexpected fallback error_details payload: %#v", got)
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
		state.Session.RuntimePullURL = "https://runtime.na.hub.molten.bot/runtime/messages/pull"
		state.Session.RuntimePushURL = "https://runtime.na.hub.molten.bot/runtime/messages/publish"
		state.Session.RuntimeOfflineURL = "https://runtime.na.hub.molten.bot/runtime/messages/offline"
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

func TestPollOnceKeepsConnectedDuringBriefHubFailure(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Connection = ConnectionState{
			Status:    ConnectionStatusConnected,
			Transport: ConnectionTransportHTTPLong,
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	fake.pullErr = errors.New("decode pull response envelope: json: cannot unmarshal string into Go struct field .message of type hub.a2aMessagePayload")

	err = service.PollOnce(context.Background())
	if err == nil {
		t.Fatal("expected poll failure")
	}

	state := service.store.Snapshot()
	if state.Connection.Status != ConnectionStatusConnected {
		t.Fatalf("expected connected status during grace period, got %#v", state.Connection)
	}
	if state.Connection.Transport != ConnectionTransportHTTPLong {
		t.Fatalf("expected existing transport during grace period, got %#v", state.Connection)
	}
	if state.Connection.Error == "" {
		t.Fatalf("expected connection error detail, got %#v", state.Connection)
	}
}

func TestPollOnceMarksDisconnectedAfterHubFailureGrace(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Connection = ConnectionState{
			Status:    ConnectionStatusConnected,
			Transport: ConnectionTransportHTTPLong,
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	service.hubFailureStartedAt = time.Now().UTC().Add(-hubDisconnectGrace)
	fake.pullErr = errors.New("dial tcp 10.0.0.1:443: connect: connection refused")

	err = service.PollOnce(context.Background())
	if err == nil {
		t.Fatal("expected poll failure")
	}

	state := service.store.Snapshot()
	if state.Connection.Status != ConnectionStatusDisconnected {
		t.Fatalf("expected disconnected status after grace period, got %#v", state.Connection)
	}
	if state.Connection.Transport != ConnectionTransportOffline {
		t.Fatalf("expected offline transport after grace period, got %#v", state.Connection)
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
		if fake.callCounts().pull > 0 {
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

func TestRunHubLoopRetriesWebsocketUpgradeDuringHTTPFallbackWindow(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	service.hubPingRetryDelay = 2 * time.Millisecond
	service.hubPingCheckTimeout = 250 * time.Millisecond
	service.wsFallbackWindow = 250 * time.Millisecond
	service.wsUpgradeRetryDelay = 20 * time.Millisecond
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
		counts := fake.callCounts()
		if counts.connect >= 2 && counts.pull > 0 {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("expected websocket upgrade retries during HTTP fallback; connect_calls=%d pull_calls=%d", fake.connectCalls, fake.pullCalls)
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	<-done

	if fake.connectCalls < 2 {
		t.Fatalf("expected at least two websocket connect attempts during fallback window, got %d", fake.connectCalls)
	}
	if fake.pullCalls == 0 {
		t.Fatalf("expected HTTP long-poll fallback to continue while websocket retries were in progress, got %d pulls", fake.pullCalls)
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
		counts := fake.callCounts()
		if counts.updateMetadata > 0 {
			metadataObserved = true
		}
		if metadataObserved && counts.pull > 0 {
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

	metadataCalls := fake.updateMetadataRequests()
	metadata := metadataCalls[0].Metadata
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

func TestRunHubLoopFallsBackToHTTPLongPollAfterRealtimeDisconnect(t *testing.T) {
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
		if fake.callCounts().pull > 0 {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("expected HTTP fallback after realtime disconnect; connect_calls=%d pull_calls=%d", fake.connectCalls, fake.pullCalls)
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	<-done
	if fake.connectCalls != 1 {
		t.Fatalf("expected one websocket connect attempt before HTTP fallback, got %d", fake.connectCalls)
	}
	if fake.pullCalls == 0 {
		t.Fatalf("expected clean websocket disconnect to fall back to HTTP polling")
	}

	state := service.store.Snapshot()
	if state.Connection.Status != ConnectionStatusConnected {
		t.Fatalf("expected connected status after realtime disconnect fallback, got %#v", state.Connection)
	}
	if state.Connection.Transport != ConnectionTransportHTTPLong {
		t.Fatalf("expected http long-poll transport after realtime disconnect fallback, got %#v", state.Connection)
	}
}

func TestRunHubLoopReconnectsAfterKeepaliveCloseWithStableSessionKeyAndNoDuplicateAckOrPublish(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	service.hubPingRetryDelay = 2 * time.Millisecond
	service.hubPingCheckTimeout = 250 * time.Millisecond
	service.wsFallbackWindow = 250 * time.Millisecond
	service.wsUpgradeRetryDelay = 10 * time.Millisecond
	firstSession := &fakeRealtimeSession{
		messages: []hub.PullResponse{{
			DeliveryID: "delivery-1",
			OpenClawMessage: hub.OpenClawMessage{
				Type: "ack",
			},
		}},
		receiveErr: errors.New("websocket session closed after pong timeout"),
	}
	secondSession := &fakeRealtimeSession{receiveErr: errors.New("websocket session closed after pong timeout")}
	fake.connectSessions = []hub.RealtimeSession{firstSession, secondSession}
	fake.pingDetail = "https://na.hub.molten.bot/ping status=204"
	fake.pullOK = false

	err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.APIBase = "https://na.hub.molten.bot/v1"
		state.Settings.SessionKey = "stable-session"
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
		if fake.callCounts().connect >= 2 {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("expected websocket reconnect after realtime close; connect_calls=%d", fake.connectCalls)
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	<-done

	if got := firstSession.acked; len(got) != 1 || got[0] != "delivery-1" {
		t.Fatalf("expected one ack for first delivery, got %#v", got)
	}
	if len(fake.publishCalls) != 0 {
		t.Fatalf("expected no duplicate publish behavior for ack-only delivery, got %d publishes", len(fake.publishCalls))
	}
	for _, key := range fake.connectSessionKeys {
		if key != "stable-session" {
			t.Fatalf("expected stable session key on reconnect, got keys %#v", fake.connectSessionKeys)
		}
	}
}

func TestRunHubLoopFallsBackToHTTPLongPollAfterRealtimeSessionCanceled(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	service.hubPingRetryDelay = 2 * time.Millisecond
	service.hubPingCheckTimeout = 250 * time.Millisecond
	fake.connectSession = &fakeRealtimeSession{receiveErr: context.Canceled}
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
		if fake.callCounts().pull > 0 {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("expected HTTP fallback after realtime canceled session; connect_calls=%d pull_calls=%d", fake.connectCalls, fake.pullCalls)
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	<-done
	if fake.pullCalls == 0 {
		t.Fatal("expected realtime canceled session to trigger HTTP fallback polling")
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

	sawWebsocketPresence := false
	deadline := time.After(2 * time.Second)
	for {
		for _, call := range fake.updateMetadataRequests() {
			presence, _ := call.Metadata["presence"].(map[string]any)
			if presence["transport"] == ConnectionTransportWebSocket {
				sawWebsocketPresence = true
			}
			if sawWebsocketPresence && presence["transport"] == ConnectionTransportHTTPLong {
				cancel()
				<-done
				return
			}
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("expected websocket presence sync followed by http long-poll fallback sync, got %#v", fake.updateMetadataCalls)
		case <-time.After(10 * time.Millisecond):
		}
	}
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

func TestHandleInboundMessageDisplaysTextMessageInActivity(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(t)

	err := service.handleInboundMessage(context.Background(), hub.PullResponse{
		DeliveryID: "delivery-1",
		OpenClawMessage: hub.OpenClawMessage{
			Type:      "text_message",
			RequestID: "message-1",
			Payload:   "hello from another agent",
		},
	})
	if err != nil {
		t.Fatalf("handle inbound message: %v", err)
	}

	state := service.store.Snapshot()
	if len(state.RecentEvents) == 0 {
		t.Fatal("expected recent event")
	}
	if got := state.RecentEvents[0].Title; got != "Message received" {
		t.Fatalf("event title = %q, want Message received", got)
	}
	if got := state.RecentEvents[0].Detail; got != "hello from another agent" {
		t.Fatalf("event detail = %q, want text payload", got)
	}
}

func TestHandleInboundMessageIgnoresAcknowledgementsInActivity(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(t)

	messages := []hub.PullResponse{
		{
			DeliveryID: "delivery-1",
			OpenClawMessage: hub.OpenClawMessage{
				Type:      "ack",
				RequestID: "request-1",
			},
		},
		{
			DeliveryID: "delivery-2",
			OpenClawMessage: hub.OpenClawMessage{
				Kind:      "delivery_ack",
				RequestID: "request-2",
			},
		},
	}
	for _, message := range messages {
		if err := service.handleInboundMessage(context.Background(), message); err != nil {
			t.Fatalf("handle acknowledgement message: %v", err)
		}
	}

	state := service.store.Snapshot()
	if len(state.RecentEvents) != 0 {
		t.Fatalf("expected acknowledgements not to append recent activity, got %#v", state.RecentEvents)
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

func TestRefreshConnectedAgentsUsesControlPlaneTalkablePeers(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.capabilitiesResponse = map[string]any{
		"control_plane": map[string]any{
			"talkable_peers": []any{
				map[string]any{
					"agent_uuid":   "self-uuid",
					"agent_id":     "self-agent",
					"agent_uri":    "https://hub.example/v1/agents/self-uuid",
					"display_name": "Self Agent",
					"emoji":        "🙂",
				},
				map[string]any{
					"agent_uuid":   "peer-uuid",
					"agent_id":     "peer-agent",
					"agent_uri":    "https://hub.example/v1/agents/peer-uuid",
					"display_name": "Codex Beast",
					"emoji":        "🤖",
				},
			},
		},
		"communication": map[string]any{
			"talkable_peers": []any{
				map[string]any{
					"agent_uuid":   "fallback-uuid",
					"agent_id":     "fallback-agent",
					"agent_uri":    "https://hub.example/v1/agents/fallback-uuid",
					"display_name": "Fallback Agent",
					"emoji":        "🛰️",
				},
			},
		},
	}
	if err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.APIBase = "https://na.hub.molten.bot/v1"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "https://hub.example/v1/agents/self-uuid"
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
		t.Fatalf("expected one connected agent from control_plane.talkable_peers, got %#v", agents)
	}
	agent := agents[0]
	if got, want := agent.AgentID, "peer-agent"; got != want {
		t.Fatalf("agent id = %q, want %q", got, want)
	}
	if got, want := agent.URI, "https://hub.example/v1/agents/peer-uuid"; got != want {
		t.Fatalf("agent uri = %q, want %q", got, want)
	}
	if got, want := ConnectedAgentDisplayName(agent), "Codex Beast"; got != want {
		t.Fatalf("display name = %q, want %q", got, want)
	}
	if got, want := ConnectedAgentEmoji(agent), "🤖"; got != want {
		t.Fatalf("emoji = %q, want %q", got, want)
	}
}

func TestRefreshConnectedAgentsFallsBackToCommunicationTalkablePeers(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.capabilitiesResponse = map[string]any{
		"control_plane": map[string]any{
			"talkable_peers": []any{},
		},
		"communication": map[string]any{
			"talkable_peers": []any{
				map[string]any{
					"agent_uuid":   "self-uuid",
					"agent_id":     "self-agent",
					"agent_uri":    "https://hub.example/v1/agents/self-uuid",
					"display_name": "Self Agent",
				},
				map[string]any{
					"agent_uri":    "https://hub.example/v1/agents/peer-uuid",
					"display_name": "Peer Without ID",
					"emoji":        "🛰️",
				},
			},
		},
	}
	if err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.APIBase = "https://na.hub.molten.bot/v1"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "https://hub.example/v1/agents/self-uuid"
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
		t.Fatalf("expected one connected agent from communication.talkable_peers fallback, got %#v", agents)
	}
	agent := agents[0]
	if got := agent.AgentID; got != "" {
		t.Fatalf("agent id = %q, want empty", got)
	}
	if got, want := agent.URI, "https://hub.example/v1/agents/peer-uuid"; got != want {
		t.Fatalf("agent uri = %q, want %q", got, want)
	}
	if got, want := ConnectedAgentDisplayName(agent), "Peer Without ID"; got != want {
		t.Fatalf("display name = %q, want %q", got, want)
	}
	if got, want := ConnectedAgentEmoji(agent), "🛰️"; got != want {
		t.Fatalf("emoji = %q, want %q", got, want)
	}
}

func TestRefreshConnectedAgentsRetainsPeerCatalogSkillsWhenTalkablePeersExist(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.capabilitiesResponse = map[string]any{
		"control_plane": map[string]any{
			"talkable_peers": []any{
				map[string]any{
					"agent_uuid": "self-uuid",
					"agent_id":   "self-agent",
					"agent_uri":  "https://hub.example/v1/agents/self-uuid",
				},
				map[string]any{
					"agent_uuid": "peer-uuid",
					"agent_id":   "peer-agent",
					"agent_uri":  "https://hub.example/v1/agents/peer-uuid",
				},
			},
		},
		"peer_skill_catalog": []any{
			map[string]any{
				"agent_uuid": "self-uuid",
				"agent_id":   "self-agent",
				"uri":        "https://hub.example/v1/agents/self-uuid",
			},
			map[string]any{
				"agent_uuid": "peer-uuid",
				"agent_id":   "peer-agent",
				"uri":        "https://hub.example/v1/agents/peer-uuid",
				"metadata": map[string]any{
					"skills": []map[string]any{
						{"name": "review_openapi", "description": "Review Hub API integration behavior."},
					},
				},
			},
		},
	}
	if err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.APIBase = "https://na.hub.molten.bot/v1"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "https://hub.example/v1/agents/self-uuid"
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
		t.Fatalf("expected one peer agent, got %#v", agents)
	}
	if skills := ConnectedAgentSkills(agents[0]); len(skills) != 1 || skills[0].Name != "review_openapi" {
		t.Fatalf("expected peer skill catalog skills to survive talkable peer fallback, got %#v", skills)
	}
}

func TestRefreshConnectedAgentsMergesPeerCatalogSkillsWithTalkablePeerProfile(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.capabilitiesResponse = map[string]any{
		"control_plane": map[string]any{
			"talkable_peers": []any{
				map[string]any{
					"agent_uuid": "self-uuid",
					"agent_id":   "self-agent",
					"agent_uri":  "https://hub.example/v1/agents/self-uuid",
				},
				map[string]any{
					"agent_uuid":   "peer-uuid",
					"agent_id":     "peer-agent",
					"agent_uri":    "https://hub.example/v1/agents/peer-uuid",
					"display_name": "Codex Beast",
					"emoji":        "🤖",
				},
			},
		},
		"peer_skill_catalog": []any{
			map[string]any{
				"agent_uuid": "self-uuid",
				"agent_id":   "self-agent",
				"uri":        "https://hub.example/v1/agents/self-uuid",
			},
			map[string]any{
				"agent_uuid": "peer-uuid",
				"agent_id":   "peer-agent",
				"uri":        "https://hub.example/v1/agents/peer-uuid",
				"metadata": map[string]any{
					"skills": []map[string]any{
						{"name": "review_openapi", "description": "Review Hub API integration behavior."},
					},
				},
			},
		},
	}
	if err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.APIBase = "https://na.hub.molten.bot/v1"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "https://hub.example/v1/agents/self-uuid"
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
		t.Fatalf("expected one peer agent, got %#v", agents)
	}
	agent := agents[0]
	if got, want := ConnectedAgentDisplayName(agent), "Codex Beast"; got != want {
		t.Fatalf("display name = %q, want %q", got, want)
	}
	if got, want := ConnectedAgentEmoji(agent), "🤖"; got != want {
		t.Fatalf("emoji = %q, want %q", got, want)
	}
	if skills := ConnectedAgentSkills(agent); len(skills) != 1 || skills[0].Name != "review_openapi" {
		t.Fatalf("expected merged agent to keep peer skill catalog skills, got %#v", skills)
	}
}

func TestRefreshConnectedAgentsPrefersTalkablePeerPresenceOverPeerCatalog(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.capabilitiesResponse = map[string]any{
		"control_plane": map[string]any{
			"talkable_peers": []any{
				map[string]any{
					"agent_uuid": "self-uuid",
					"agent_id":   "self-agent",
					"agent_uri":  "https://hub.example/v1/agents/self-uuid",
				},
				map[string]any{
					"agent_uuid": "peer-uuid",
					"agent_id":   "peer-agent",
					"agent_uri":  "https://hub.example/v1/agents/peer-uuid",
					"presence": map[string]any{
						"status": "offline",
					},
				},
			},
		},
		"peer_skill_catalog": []any{
			map[string]any{
				"agent_uuid": "self-uuid",
				"agent_id":   "self-agent",
				"uri":        "https://hub.example/v1/agents/self-uuid",
			},
			map[string]any{
				"agent_uuid": "peer-uuid",
				"agent_id":   "peer-agent",
				"uri":        "https://hub.example/v1/agents/peer-uuid",
				"status":     "online",
				"metadata": map[string]any{
					"presence": map[string]any{
						"status": "online",
					},
					"skills": []map[string]any{
						{"name": "review_openapi", "description": "Review Hub API integration behavior."},
					},
				},
			},
		},
	}
	if err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.APIBase = "https://na.hub.molten.bot/v1"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "https://hub.example/v1/agents/self-uuid"
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
		t.Fatalf("expected one peer agent, got %#v", agents)
	}
	agent := agents[0]
	if got, want := ConnectedAgentPresenceStatus(agent), "offline"; got != want {
		t.Fatalf("presence = %q, want %q", got, want)
	}
	if got, want := strings.ToLower(strings.TrimSpace(agent.Status)), "offline"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if skills := ConnectedAgentSkills(agent); len(skills) != 1 || skills[0].Name != "review_openapi" {
		t.Fatalf("expected peer catalog skills to survive presence merge, got %#v", skills)
	}
}

func TestRefreshConnectedAgentsDoesNotInferOnlineWhenPresenceMissing(t *testing.T) {
	t.Parallel()

	service, fake := newTestService(t)
	fake.capabilitiesResponse = map[string]any{
		"peer_skill_catalog": []any{
			map[string]any{
				"agent_uuid": "self-uuid",
				"agent_id":   "self-agent",
				"uri":        "https://hub.example/v1/agents/self-uuid",
			},
			map[string]any{
				"agent_uuid":   "peer-uuid",
				"agent_id":     "peer-agent",
				"uri":          "https://hub.example/v1/agents/peer-uuid",
				"display_name": "Peer Agent",
			},
		},
	}
	if err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "agent-token"
		state.Session.APIBase = "https://na.hub.molten.bot/v1"
		state.Session.AgentUUID = "self-uuid"
		state.Session.AgentURI = "https://hub.example/v1/agents/self-uuid"
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
		t.Fatalf("expected one peer agent, got %#v", agents)
	}
	agent := agents[0]
	if got := strings.TrimSpace(agent.Status); got != "" {
		t.Fatalf("status = %q, want empty when capability presence/status is missing", got)
	}
	if agent.Metadata != nil && agent.Metadata.Presence != nil {
		t.Fatalf("did not expect synthesized presence when capabilities omit it, got %#v", agent.Metadata.Presence)
	}
	if got, want := ConnectedAgentPresenceStatus(agent), "offline"; got != want {
		t.Fatalf("presence = %q, want %q", got, want)
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
	if got, want := ConnectedAgentDisplayName(agent), "Peer Agent"; got != want {
		t.Fatalf("display name = %q, want %q", got, want)
	}
	if got, want := ConnectedAgentEmoji(agent), "🛠"; got != want {
		t.Fatalf("emoji = %q, want %q", got, want)
	}
	if got, want := ConnectedAgentPresenceStatus(agent), "online"; got != want {
		t.Fatalf("presence = %q, want %q", got, want)
	}
	skills := ConnectedAgentSkills(agent)
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
	if skills := ConnectedAgentSkills(agents[0]); len(skills) != 1 || skills[0].Name != "review_failure_logs" {
		t.Fatalf("expected metadata.advertised_skills fallback, got %#v", skills)
	}
	if skills := ConnectedAgentSkills(agents[1]); len(skills) != 1 || skills[0].Name != "review_openapi" {
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
	if got, want := ConnectedAgentDisplayName(agent), "Peer Agent"; got != want {
		t.Fatalf("display name = %q, want %q", got, want)
	}
	if got, want := ConnectedAgentEmoji(agent), "🛠"; got != want {
		t.Fatalf("emoji = %q, want %q", got, want)
	}
	if got, want := ConnectedAgentPresenceStatus(agent), "online"; got != want {
		t.Fatalf("presence = %q, want %q", got, want)
	}
	if skills := ConnectedAgentSkills(agent); len(skills) != 1 || skills[0].Name != "review_failure_logs" {
		t.Fatalf("expected root-level skills from capabilities response, got %#v", skills)
	}
}

func TestRefreshConnectedAgentsAcceptsDisplayNameAliasesFromPeerCatalog(t *testing.T) {
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
				"agent_uuid":  "peer-uuid",
				"agent_id":    "peer-agent",
				"uri":         "molten://agent/peer",
				"displayName": "Peer Agent",
				"emoji":       "🧪",
				"skills": []map[string]any{
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
	if len(agents) != 1 {
		t.Fatalf("expected one connected agent, got %#v", agents)
	}
	if got, want := ConnectedAgentDisplayName(agents[0]), "Peer Agent"; got != want {
		t.Fatalf("display name = %q, want %q", got, want)
	}
	if got, want := ConnectedAgentEmoji(agents[0]), "🧪"; got != want {
		t.Fatalf("emoji = %q, want %q", got, want)
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
