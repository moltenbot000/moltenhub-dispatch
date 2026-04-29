package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/app"
	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

type stubService struct {
	state             app.AppState
	bindErr           error
	updateProfileErr  error
	refreshProfileErr error
	updateSettingsErr error
	setFlashErr       error
	refreshAgentsErr  error
	dispatchErr       error
	dispatchTask      app.PendingTask
	lastDispatchReq   app.DispatchRequest
	lastProfile       app.AgentProfile
	bindStateOnError  bool
	lastBindProfile   app.BindProfile
	lastFlashLevel    string
	lastFlashMessage  string
}

const testDispatchPrompt = "Review the Hub API integration behavior."

func (s *stubService) Snapshot() app.AppState {
	return s.state
}

func (s *stubService) BindAndRegister(_ context.Context, profile app.BindProfile) error {
	s.lastBindProfile = profile
	if s.bindStateOnError {
		s.state.Session.AgentToken = "agent-token"
		s.state.Session.Handle = profile.Handle
		s.state.Session.HandleFinalized = profile.Handle != ""
		s.state.Session.DisplayName = profile.DisplayName
		s.state.Session.Emoji = profile.Emoji
		s.state.Session.ProfileBio = profile.ProfileMarkdown
		s.state.Session.BoundAt = time.Now().UTC()
	}
	if s.bindErr != nil {
		return s.bindErr
	}
	s.state.Session.AgentToken = "agent-token"
	s.state.Session.Handle = profile.Handle
	s.state.Session.HandleFinalized = profile.Handle != ""
	s.state.Session.DisplayName = profile.DisplayName
	s.state.Session.Emoji = profile.Emoji
	s.state.Session.ProfileBio = profile.ProfileMarkdown
	s.state.Session.BoundAt = time.Now().UTC()
	return nil
}

func (s *stubService) UpdateAgentProfile(_ context.Context, profile app.AgentProfile) error {
	if s.updateProfileErr != nil {
		return s.updateProfileErr
	}
	s.state.Session.DisplayName = profile.DisplayName
	s.state.Session.Emoji = profile.Emoji
	s.state.Session.ProfileBio = profile.ProfileMarkdown
	return nil
}

func (s *stubService) RefreshAgentProfile(_ context.Context) (app.AgentProfile, error) {
	if s.refreshProfileErr != nil {
		return app.AgentProfile{}, s.refreshProfileErr
	}
	profile := app.AgentProfile{
		Handle:          s.state.Session.Handle,
		DisplayName:     s.state.Session.DisplayName,
		Emoji:           s.state.Session.Emoji,
		ProfileMarkdown: s.state.Session.ProfileBio,
	}
	s.lastProfile = profile
	return profile, nil
}

func (s *stubService) DisconnectAgent(_ context.Context) error {
	s.state.Session = app.Session{}
	s.state.Connection = app.ConnectionState{
		Status:    app.ConnectionStatusDisconnected,
		Transport: app.ConnectionTransportOffline,
	}
	return nil
}

func (s *stubService) AddConnectedAgent(app.ConnectedAgent) error {
	return nil
}

func (s *stubService) RefreshConnectedAgents(_ context.Context) ([]app.ConnectedAgent, error) {
	if s.refreshAgentsErr != nil {
		return nil, s.refreshAgentsErr
	}
	return s.state.ConnectedAgents, nil
}

func (s *stubService) DispatchFromUI(_ context.Context, req app.DispatchRequest) (app.PendingTask, error) {
	s.lastDispatchReq = req
	return s.dispatchTask, s.dispatchErr
}

func (s *stubService) UpdateSettings(mutator func(*app.Settings) error) error {
	if s.updateSettingsErr != nil {
		return s.updateSettingsErr
	}
	return mutator(&s.state.Settings)
}

func (s *stubService) SetFlash(level, message string) error {
	if s.setFlashErr != nil {
		return s.setFlashErr
	}
	level = strings.ToLower(strings.TrimSpace(level))
	if level != "error" {
		level = "info"
	}
	s.state.Flash = app.FlashMessage{
		Level:   level,
		Message: strings.TrimSpace(message),
	}
	s.lastFlashLevel = level
	s.lastFlashMessage = strings.TrimSpace(message)
	return nil
}

func (s *stubService) ConsumeFlash() (app.FlashMessage, error) {
	flash := s.state.Flash
	s.state.Flash = app.FlashMessage{}
	return flash, nil
}

func testConnectedAgent(agentID, displayName, agentUUID, uri string, skills ...app.Skill) app.ConnectedAgent {
	online := true
	agent := app.ConnectedAgent{
		AgentID:   agentID,
		Handle:    agentID,
		AgentUUID: agentUUID,
		URI:       uri,
		Metadata: &hub.AgentMetadata{
			DisplayName: displayName,
			Skills:      skillMetadata(skills...),
			Presence:    &hub.AgentPresence{Ready: &online},
		},
	}
	if displayName == "" && len(skills) == 0 {
		agent.Metadata = nil
	}
	return agent
}

func skillMetadata(skills ...app.Skill) []map[string]any {
	if len(skills) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(skills))
	for _, skill := range skills {
		entry := map[string]any{"name": skill.Name}
		if strings.TrimSpace(skill.Description) != "" {
			entry["description"] = skill.Description
		}
		out = append(out, entry)
	}
	return out
}

func TestHandleBindRedirectsOnFailure(t *testing.T) {
	t.Parallel()

	stub := &stubService{bindErr: errors.New("bind failed")}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/bind", strings.NewReader("bind_token=bind-123&handle=codex-beast&display_name=Dispatch+Agent&emoji=%F0%9F%92%AF&profile_markdown=What+this+runtime+is+for"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect response, got %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Fatalf("expected root redirect location, got %q", got)
	}
	if got := stub.state.Flash; got.Level != "error" || got.Message != "bind failed" {
		t.Fatalf("unexpected flash after bind failure: %#v", got)
	}
}

func TestHandleIndexRendersLocalConnectionAsConnected(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `id="local-conn-item"`) {
		t.Fatalf("expected local connection indicator, body=%s", body)
	}
	if !strings.Contains(body, `title="Local: Connected"`) {
		t.Fatalf("expected local connection title to render connected, body=%s", body)
	}
	if !strings.Contains(body, `id="local-conn-dot" class="dot online"`) {
		t.Fatalf("expected local connection dot to render online, body=%s", body)
	}
	if !strings.Contains(body, `Local: Connected</span>`) {
		t.Fatalf("expected local connection tooltip to render connected, body=%s", body)
	}
	if strings.Contains(body, `setLocalConnection(false, "Reconnecting...");`) {
		t.Fatalf("did not expect local connection to downgrade on hub status refresh failures, body=%s", body)
	}
}

func TestHandleIndexRendersGoogleAnalyticsSnippet(t *testing.T) {
	t.Parallel()

	settings := app.DefaultSettings()
	settings.GoogleAnalyticsMeasurementID = "G-TEST123456"

	server, err := New(&stubService{state: app.AppState{Settings: settings}})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `https://www.googletagmanager.com/gtag/js?id=G-TEST123456`) {
		t.Fatalf("expected google analytics bootstrap script, body=%s", body)
	}
	if !strings.Contains(body, `window.gtag("config", "G-TEST123456", { send_page_view: false });`) {
		t.Fatalf("expected google analytics config bootstrap, body=%s", body)
	}
	if !strings.Contains(body, `window.__hubAnalyticsMeasurementID = "G-TEST123456";`) {
		t.Fatalf("expected google analytics measurement id to be exposed to app script, body=%s", body)
	}
	if !strings.Contains(body, `window.gtag("event", "page_view"`) {
		t.Fatalf("expected google analytics page_view tracking, body=%s", body)
	}
}

func TestHandleIndexRendersGoogleAnalyticsInteractionEvents(t *testing.T) {
	t.Parallel()

	settings := app.DefaultSettings()
	settings.GoogleAnalyticsMeasurementID = "G-TEST123456"

	server, err := New(&stubService{state: app.AppState{Settings: settings}})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, want := range []string{
		`const ANALYTICS_APP_NAME = "moltenhub_dispatch";`,
		`const trackAppEvent = (eventName, params = {}) => {`,
		`window.gtag("event", name, eventParams);`,
		`trackAppEvent("theme_change"`,
		`trackAppEvent("agent_settings_open"`,
		`trackAppEvent("onboarding_submit"`,
		`trackAppEvent("dispatch_submit"`,
		`trackAppEvent("dispatch_result"`,
		`trackAppEvent("connected_agents_refresh"`,
		`trackAppEvent("activity_detail_toggle"`,
		`transport_type: "beacon"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected google analytics interaction tracking %q, body=%s", want, body)
		}
	}
}

func TestHandleIndexOmitsGoogleAnalyticsSnippetWhenDisabled(t *testing.T) {
	t.Parallel()

	settings := app.DefaultSettings()
	settings.GoogleAnalyticsMeasurementID = ""

	server, err := New(&stubService{state: app.AppState{Settings: settings}})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "googletagmanager.com/gtag/js") {
		t.Fatalf("did not expect google analytics script when disabled, body=%s", body)
	}
	if strings.Contains(body, `window.__hubAnalyticsMeasurementID = "`) {
		t.Fatalf("did not expect google analytics measurement id when disabled, body=%s", body)
	}
}

func TestHandleBindPassesSubmittedProfileToService(t *testing.T) {
	t.Parallel()

	stub := &stubService{}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/bind", strings.NewReader("bind_token=bind-123&handle=codex-beast&display_name=Dispatch+Agent"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect response, got %d", rec.Code)
	}
	if stub.lastBindProfile.Handle != "codex-beast" {
		t.Fatalf("expected submitted handle to reach service, got %#v", stub.lastBindProfile)
	}
	if stub.lastBindProfile.DisplayName != "Dispatch Agent" {
		t.Fatalf("expected submitted display name to reach service, got %#v", stub.lastBindProfile)
	}
}

func TestHandleBindUsesSubmittedAgentMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		mode           string
		token          string
		wantMode       string
		wantBindToken  string
		wantAgentToken string
	}{
		{
			name:           "new mode routes token to bind flow",
			mode:           "new",
			token:          "bind-123",
			wantMode:       app.OnboardingModeNew,
			wantBindToken:  "bind-123",
			wantAgentToken: "",
		},
		{
			name:           "existing mode routes token to existing flow",
			mode:           "existing",
			token:          "agent-123",
			wantMode:       app.OnboardingModeExisting,
			wantBindToken:  "",
			wantAgentToken: "agent-123",
		},
		{
			name:           "bind prefix overrides omitted mode",
			mode:           "",
			token:          "b_bind-123",
			wantMode:       app.OnboardingModeNew,
			wantBindToken:  "b_bind-123",
			wantAgentToken: "",
		},
		{
			name:           "bind prefix overrides existing mode",
			mode:           "existing",
			token:          "b_bind-123",
			wantMode:       app.OnboardingModeNew,
			wantBindToken:  "b_bind-123",
			wantAgentToken: "",
		},
		{
			name:           "target prefix overrides new mode",
			mode:           "new",
			token:          "t_agent-123",
			wantMode:       app.OnboardingModeExisting,
			wantBindToken:  "",
			wantAgentToken: "t_agent-123",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubService{}
			server, err := New(stub)
			if err != nil {
				t.Fatalf("new server: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/bind", strings.NewReader("agent_mode="+url.QueryEscape(test.mode)+"&bind_token="+url.QueryEscape(test.token)+"&handle=codex-beast&display_name=Dispatch+Agent"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()

			server.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("expected redirect response, got %d", rec.Code)
			}
			if got := stub.lastBindProfile.AgentMode; got != test.wantMode {
				t.Fatalf("agent mode = %q, want %q", got, test.wantMode)
			}
			if got := stub.lastBindProfile.BindToken; got != test.wantBindToken {
				t.Fatalf("bind token = %q, want %q", got, test.wantBindToken)
			}
			if got := stub.lastBindProfile.AgentToken; got != test.wantAgentToken {
				t.Fatalf("agent token = %q, want %q", got, test.wantAgentToken)
			}
		})
	}
}

func TestHandleOnboardingAPIReturnsStageAwareFailure(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{bindErr: app.WrapOnboardingError(app.OnboardingStepProfileSet, errors.New("profile sync failed"))})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/onboarding", strings.NewReader(`{"bind_token":"bind-123","handle":"dispatch-agent"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 response, got %d", rec.Code)
	}

	var body struct {
		OK         bool   `json:"ok"`
		Error      string `json:"error"`
		Onboarding struct {
			Stage string `json:"stage"`
		} `json:"onboarding"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.OK {
		t.Fatalf("expected failure response, got %#v", body)
	}
	if body.Error != "profile sync failed" {
		t.Fatalf("unexpected error message: %#v", body)
	}
	if body.Onboarding.Stage != app.OnboardingStepProfileSet {
		t.Fatalf("unexpected onboarding stage: %#v", body)
	}
}

func TestHandleOnboardingAPIReturnsSuccess(t *testing.T) {
	t.Parallel()

	stub := &stubService{state: app.AppState{Settings: app.DefaultSettings()}}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/onboarding", strings.NewReader(`{"agent_mode":"new","bind_token":"bind-123","handle":"dispatch-agent"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}

	var body struct {
		OK         bool   `json:"ok"`
		Message    string `json:"message"`
		Bound      bool   `json:"bound"`
		Onboarding struct {
			Steps []struct {
				ID     string `json:"id"`
				Detail string `json:"detail"`
			} `json:"steps"`
		} `json:"onboarding"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.OK || !body.Bound {
		t.Fatalf("expected bound success response, got %#v", body)
	}
	if got := stub.lastBindProfile.AgentMode; got != app.OnboardingModeNew {
		t.Fatalf("agent mode = %q, want %q", got, app.OnboardingModeNew)
	}
	if got := stub.lastBindProfile.BindToken; got != "bind-123" {
		t.Fatalf("bind token = %q, want %q", got, "bind-123")
	}
	if got := stub.lastBindProfile.AgentToken; got != "" {
		t.Fatalf("agent token = %q, want empty", got)
	}
	if body.Message != "Agent bound and profile registered." {
		t.Fatalf("unexpected message: %#v", body)
	}
	if len(body.Onboarding.Steps) != 4 {
		t.Fatalf("expected four onboarding steps, got %#v", body.Onboarding.Steps)
	}
	if got, want := body.Onboarding.Steps[0].Detail, "Create this dispatcher's agent credential."; got != want {
		t.Fatalf("bind step detail = %q, want %q", got, want)
	}
}

func TestHandleOnboardingAPIUsesExistingAgentFlowWhenModeExisting(t *testing.T) {
	t.Parallel()

	stub := &stubService{state: app.AppState{Settings: app.DefaultSettings()}}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/onboarding", strings.NewReader(`{"agent_mode":"existing","agent_token":"agent-123","display_name":"Dispatch Agent"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}

	if got := stub.lastBindProfile.AgentMode; got != app.OnboardingModeExisting {
		t.Fatalf("agent mode = %q, want %q", got, app.OnboardingModeExisting)
	}
	if got := stub.lastBindProfile.AgentToken; got != "agent-123" {
		t.Fatalf("agent token = %q, want %q", got, "agent-123")
	}
	if got := stub.lastBindProfile.BindToken; got != "" {
		t.Fatalf("bind token = %q, want empty", got)
	}
	if stub.lastFlashMessage != "Existing agent connected and profile registered." {
		t.Fatalf("unexpected success flash: %q", stub.lastFlashMessage)
	}
}

func TestHandleOnboardingAPIUsesExistingFlowForLegacyToken(t *testing.T) {
	t.Parallel()

	stub := &stubService{state: app.AppState{Settings: app.DefaultSettings()}}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/onboarding", strings.NewReader(`{"agent_mode":"existing","bind_token":"agent-legacy-123","display_name":"Dispatch Agent"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}

	if got := stub.lastBindProfile.AgentMode; got != app.OnboardingModeExisting {
		t.Fatalf("agent mode = %q, want %q", got, app.OnboardingModeExisting)
	}
	if got := stub.lastBindProfile.AgentToken; got != "agent-legacy-123" {
		t.Fatalf("agent token = %q, want %q", got, "agent-legacy-123")
	}
	if got := stub.lastBindProfile.BindToken; got != "" {
		t.Fatalf("bind token = %q, want empty", got)
	}
}

func TestHandleOnboardingAPIUsesSubmittedRuntimeRegion(t *testing.T) {
	t.Parallel()

	stub := &stubService{state: app.AppState{Settings: app.DefaultSettings()}}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/onboarding", strings.NewReader(`{"hub_region":"eu","bind_token":"bind-123","handle":"dispatch-agent"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}

	if got, want := stub.state.Settings.HubRegion, app.HubRegionEU; got != want {
		t.Fatalf("hub region = %q, want %q", got, want)
	}
	if got, want := stub.state.Settings.HubURL, "https://eu.hub.molten.bot"; got != want {
		t.Fatalf("hub url = %q, want %q", got, want)
	}
}

func TestHandleOnboardingAPIRejectsUnsupportedRuntimeRegion(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{state: app.AppState{Settings: app.DefaultSettings()}})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/onboarding", strings.NewReader(`{"hub_region":"apac","bind_token":"bind-123","handle":"dispatch-agent"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 response, got %d", rec.Code)
	}

	var body struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.OK {
		t.Fatalf("expected failure response, got %#v", body)
	}
	if !strings.Contains(body.Error, "unsupported hub runtime selection") {
		t.Fatalf("unexpected error message: %#v", body)
	}
}

func TestHandleIndexRendersAutoDismissingFlash(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Flash: app.FlashMessage{
				Level:   "info",
				Message: "Settings updated.",
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}
	if !strings.Contains(body, `class="flash flash-info" data-auto-dismiss-seconds="12"`) {
		t.Fatalf("expected flash to advertise 12s auto-dismiss timeout, body=%s", body)
	}
	if !strings.Contains(body, `const flash = document.querySelector(".flash[data-auto-dismiss-seconds]");`) {
		t.Fatalf("expected flash auto-dismiss client hook, body=%s", body)
	}
	if !strings.Contains(body, `window.setTimeout(() => {`) || !strings.Contains(body, `: 12000;`) {
		t.Fatalf("expected flash fallback timeout to 12000ms, body=%s", body)
	}
}

func TestHandleIndexRendersConsoleTitleAndSubtitle(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.renderIndex(rec, req, "", false, agentProfileForm{}, nil)

	body := rec.Body.String()
	if !strings.Contains(body, `<title>Molten Hub Dispatch</title>`) {
		t.Fatalf("expected document title to be Molten Hub Dispatch, body=%s", body)
	}
	if !strings.Contains(body, `class="brand-logo rotating-brand-logo brand-logo-visible"`) {
		t.Fatalf("expected page header logo lockup, body=%s", body)
	}
	if !strings.Contains(body, `src="/static/logo.svg"`) {
		t.Fatalf("expected page header to use the bundled logo asset, body=%s", body)
	}
	if strings.Contains(body, `>Dispatch Console</p>`) {
		t.Fatalf("did not expect removed page header eyebrow copy, body=%s", body)
	}
	if !strings.Contains(body, `<h1 class="page-brand-title">Molten Hub Dispatch</h1>`) {
		t.Fatalf("expected page header title lockup to match moltenhub-code, body=%s", body)
	}
	if !strings.Contains(body, `<p class="page-brand-subtitle">Control your team of agents.</p>`) {
		t.Fatalf("expected page header subtitle lockup to match moltenhub-code, body=%s", body)
	}
	if strings.Contains(body, `class="title brand-heading-h1"`) || strings.Contains(body, `class="sub page-subtitle brand-copy-h4-muted-tight"`) {
		t.Fatalf("did not expect legacy dispatch-only title/subtitle styling, body=%s", body)
	}
}

func TestHandleIndexConsumesFlashOnlyOnce(t *testing.T) {
	t.Parallel()

	stub := &stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Flash: app.FlashMessage{
				Level:   "error",
				Message: "hub API 401 unauthorized: missing or invalid bearer token",
			},
		},
	}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	firstReq := httptest.NewRequest(http.MethodGet, "/", nil)
	firstRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(firstRec, firstReq)

	firstBody := firstRec.Body.String()
	if !strings.Contains(firstBody, "missing or invalid bearer token") {
		t.Fatalf("expected first render to include flash message, body=%s", firstBody)
	}

	secondReq := httptest.NewRequest(http.MethodGet, "/", nil)
	secondRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(secondRec, secondReq)

	secondBody := secondRec.Body.String()
	if strings.Contains(secondBody, "missing or invalid bearer token") {
		t.Fatalf("did not expect consumed flash on second render, body=%s", secondBody)
	}
}

func TestHandleIndexIgnoresFlashQueryParams(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/?level=error&message=hub+API+401+unauthorized%3A+missing+or+invalid+bearer+token", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "missing or invalid bearer token") {
		t.Fatalf("did not expect flash query params to render, body=%s", body)
	}
	if strings.Contains(body, `class="flash`) {
		t.Fatalf("did not expect flash banner without app-state flash, body=%s", body)
	}
}

func TestHandleIndexShowsBoundProfileState(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusConnected,
				Transport: app.ConnectionTransportHTTP,
			},
			Session: app.Session{
				AgentToken:      "agent-token",
				Handle:          "codex-beast",
				HandleFinalized: true,
				DisplayName:     "Dispatch Agent",
				Emoji:           "💯",
				ProfileBio:      "What this runtime is for",
			},
			ConnectedAgents: []app.ConnectedAgent{
				testConnectedAgent("worker-a", "Worker A", "worker-uuid", "molten://agent/worker-a"),
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "Agent Profile") {
		t.Fatalf("expected bound profile panel, body=%s", body)
	}
	if strings.Contains(body, `dispatch-overview`) || strings.Contains(body, "Queue the right task with fewer clicks.") {
		t.Fatalf("did not expect removed dispatch workflow overview, body=%s", body)
	}
	if strings.Contains(body, `name="bind_token"`) {
		t.Fatalf("did not expect bind token field after bind, body=%s", body)
	}
	if !strings.Contains(body, `id="agent-settings-handle" name="handle" value="codex-beast"`) || !strings.Contains(body, `readonly`) {
		t.Fatalf("expected readonly handle field, body=%s", body)
	}
	if !strings.Contains(body, `id="agent-settings-display-name" name="display_name" value="Dispatch Agent"`) {
		t.Fatalf("expected display name field, body=%s", body)
	}
	if !strings.Contains(body, `id="bind-form"`) || !strings.Contains(body, `data-bound="true"`) || !strings.Contains(body, `action="/profile"`) {
		t.Fatalf("expected shared profile form for bound agent settings, body=%s", body)
	}
	if !strings.Contains(body, `const agentSettingsDisplayName = document.getElementById("agent-settings-display-name");`) {
		t.Fatalf("expected agent settings display name client hook, body=%s", body)
	}
	if !strings.Contains(body, `fetch("/api/profile"`) {
		t.Fatalf("expected settings modal profile refresh request, body=%s", body)
	}
	if !strings.Contains(body, `void refreshAgentSettingsProfile();`) {
		t.Fatalf("expected settings modal to refresh profile on open, body=%s", body)
	}
	if !strings.Contains(body, `id="hub-conn-item"`) {
		t.Fatalf("expected connection indicator in page, body=%s", body)
	}
	if !strings.Contains(body, `id="local-conn-item"`) {
		t.Fatalf("expected local connection indicator in page, body=%s", body)
	}
	if strings.Contains(body, "Awaiting Bind") {
		t.Fatalf("did not expect removed bind state section, body=%s", body)
	}
	if strings.Contains(body, ">Runtime<") {
		t.Fatalf("did not expect removed runtime panel, body=%s", body)
	}
	if strings.Contains(body, "Save Global Settings") {
		t.Fatalf("did not expect save button for global settings, body=%s", body)
	}
	if strings.Contains(body, "Failure-review follow-ups always target the first connected agent marked as a failure reviewer.") {
		t.Fatalf("did not expect removed failure reviewer hint, body=%s", body)
	}
	if !strings.Contains(body, `id="agent-disconnect-submit" formaction="/disconnect"`) {
		t.Fatalf("expected disconnect action in shared profile form, body=%s", body)
	}
	if strings.Contains(body, `name="hub_region"`) {
		t.Fatalf("did not expect runtime selector once bound, body=%s", body)
	}
	if !strings.Contains(body, `id="onboarding-modal-backdrop"`) || !strings.Contains(body, `aria-hidden="true"`) || !strings.Contains(body, `hidden`) {
		t.Fatalf("expected shared profile modal to stay hidden until settings is opened, body=%s", body)
	}
	if strings.Contains(body, ">1. Agent Settings<") {
		t.Fatalf("did not expect removed agent settings section, body=%s", body)
	}
	if strings.Contains(body, `id="open-agent-settings"`) {
		t.Fatalf("did not expect removed agent settings section button, body=%s", body)
	}
	if !strings.Contains(body, ">Dispatch<") {
		t.Fatalf("expected sub-actions when bound and connected, body=%s", body)
	}
	if !strings.Contains(body, "Markdown and JSON are both supported.") {
		t.Fatalf("expected simplified payload format hint, body=%s", body)
	}
	if strings.Contains(body, "Select a target first. Skills load from the chosen agent's Hub capabilities.") {
		t.Fatalf("did not expect removed dispatch target hint, body=%s", body)
	}
	if strings.Contains(body, "Dispatch sets the payload format automatically.") {
		t.Fatalf("did not expect removed payload auto-format hint, body=%s", body)
	}
	if strings.Contains(body, ">Manual Dispatch<") {
		t.Fatalf("did not expect previous manual dispatch heading, body=%s", body)
	}
	if !strings.Contains(body, `class="prompt-label">Agents</legend>`) {
		t.Fatalf("expected connected agent legend copy, body=%s", body)
	}
	if !strings.Contains(body, "Skills") {
		t.Fatalf("expected skills field label, body=%s", body)
	}
	if !strings.Contains(body, `class="prompt-label">Payload</span>`) {
		t.Fatalf("expected payload field label, body=%s", body)
	}
	if !strings.Contains(body, "Auto-select the first skill of that agent.") {
		t.Fatalf("expected skill auto-select hint, body=%s", body)
	}
	if strings.Contains(body, "Format is detected automatically.") {
		t.Fatalf("did not expect removed payload format hint copy, body=%s", body)
	}
	if !strings.Contains(body, `class="grid manual-dispatch-grid"`) {
		t.Fatalf("expected manual dispatch section to render full-width grid class, body=%s", body)
	}
	if !strings.Contains(body, `id="manual-dispatch-form"`) {
		t.Fatalf("expected manual dispatch form id for async submit handling, body=%s", body)
	}
	if !strings.Contains(body, `manual-dispatch-actions`) {
		t.Fatalf("expected manual dispatch submit action wrapper, body=%s", body)
	}
	if !strings.Contains(body, `class="prompt-actions manual-dispatch-actions"`) {
		t.Fatalf("expected manual dispatch actions to reuse studio prompt action layout, body=%s", body)
	}
	if !strings.Contains(body, `class="prompt-grid manual-dispatch-selection-grid"`) {
		t.Fatalf("expected manual dispatch selection row to reuse studio prompt grid layout, body=%s", body)
	}
	if !strings.Contains(body, `class="panel-header prompt-titlebar flex items-center justify-between gap-2 border-b border-hub-border px-3.5 py-3 text-[0.92rem] font-semibold uppercase tracking-[0.03em]"`) {
		t.Fatalf("expected manual dispatch panel header to reuse studio titlebar layout, body=%s", body)
	}
	if !strings.Contains(body, `class="panel prompt-wrap dispatch-workbench-panel brand-login-card-shell min-h-[220px] overflow-visible rounded-2xl border border-hub-border bg-hub-panel`) {
		t.Fatalf("expected manual dispatch panel shell to reuse studio prompt-wrap layout, body=%s", body)
	}
	if !strings.Contains(body, `class="panel prompt-wrap brand-login-card-shell min-h-[220px] overflow-visible rounded-2xl border border-hub-border bg-hub-panel`) {
		t.Fatalf("expected activity panel shell to reuse studio prompt-wrap layout, body=%s", body)
	}
	if !strings.Contains(body, `<h2 class="panel-section-title">Recent Activity</h2>`) {
		t.Fatalf("expected activity panel heading to reuse studio section title treatment, body=%s", body)
	}
	statusIndex := strings.Index(body, `id="dispatch-submit-status"`)
	actionsIndex := strings.Index(body, `class="prompt-actions-end"`)
	if statusIndex == -1 || actionsIndex == -1 || statusIndex > actionsIndex {
		t.Fatalf("expected dispatch status to render before the action row so buttons sit at the footer, body=%s", body)
	}
	if !strings.Contains(body, `id="dispatch-task-clear"`) {
		t.Fatalf("expected manual dispatch clear button beside submit, body=%s", body)
	}
	if !strings.Contains(body, `aria-label="Clear payload"`) || !strings.Contains(body, `data-lucide="x"`) {
		t.Fatalf("expected clear action to render as icon-only button with accessible label, body=%s", body)
	}
	if strings.Contains(body, `name="repo"`) {
		t.Fatalf("did not expect manual dispatch repo field, body=%s", body)
	}
	if strings.Contains(body, `name="log_paths"`) {
		t.Fatalf("did not expect manual dispatch log paths field, body=%s", body)
	}
	if !strings.Contains(body, `id="skill-payload-field"`) || (!strings.Contains(body, `id="skill-payload-field" hidden`) && !strings.Contains(body, `id="skill-payload-field" class="prompt-field" hidden`)) {
		t.Fatalf("expected hidden manual dispatch payload field, body=%s", body)
	}
	if !strings.Contains(body, `id="skill-payload-input"`) || !strings.Contains(body, `name="payload"`) {
		t.Fatalf("expected manual dispatch payload textarea, body=%s", body)
	}
	if !strings.Contains(body, `placeholder="Describe the task or paste a JSON payload."`) {
		t.Fatalf("expected manual dispatch payload placeholder example, body=%s", body)
	}
	if strings.Contains(body, `data-skill-payload-format-toggle`) {
		t.Fatalf("did not expect manual dispatch payload format toggle buttons, body=%s", body)
	}
	if !strings.Contains(body, `id="skill-payload-validation"`) {
		t.Fatalf("expected payload validation output node, body=%s", body)
	}
	if !strings.Contains(body, `id="skill-payload-format-input" type="hidden" name="payload_format"`) {
		t.Fatalf("expected manual dispatch payload format field, body=%s", body)
	}
	if !strings.Contains(body, `const detectSkillPayloadFormat = (value) => {`) {
		t.Fatalf("expected automatic payload format detection helper in client script, body=%s", body)
	}
	if !strings.Contains(body, `const escapeJSONStringControlWhitespace = (value) => {`) {
		t.Fatalf("expected JSON payload detection to tolerate prompt whitespace, body=%s", body)
	}
	if !strings.Contains(body, `parseSkillPayloadJSON(rawPayload);`) {
		t.Fatalf("expected JSON payload detection to use tolerant parser, body=%s", body)
	}
	if !strings.Contains(body, `Detected JSON payload.`) {
		t.Fatalf("expected JSON detection status messaging in client script, body=%s", body)
	}
	if !strings.Contains(body, `Detected markdown payload.`) {
		t.Fatalf("expected markdown detection status messaging in client script, body=%s", body)
	}
	if !strings.Contains(body, `targetAgentRefInput.value = connectedAgentTargetRef(nextAgents[0]);`) {
		t.Fatalf("expected first connected agent auto-selection logic, body=%s", body)
	}
	if !strings.Contains(body, `agent && agent.metadata && agent.metadata.advertised_skills`) {
		t.Fatalf("expected manual dispatch client to read metadata.advertised_skills, body=%s", body)
	}
	if !strings.Contains(body, `agent && agent.advertised_skills`) {
		t.Fatalf("expected manual dispatch client to read top-level advertised_skills, body=%s", body)
	}
	if strings.Contains(body, `name="timeout_seconds"`) {
		t.Fatalf("did not expect manual dispatch timeout field, body=%s", body)
	}
	if !strings.Contains(body, `class="dispatch-task-button prompt-action-button"`) {
		t.Fatalf("expected dispatch task button to use action button class, body=%s", body)
	}
	if !strings.Contains(body, `class="prompt-action-button prompt-action-clear"`) {
		t.Fatalf("expected clear button to share prompt action button chrome, body=%s", body)
	}
	if !strings.Contains(body, `id="dispatch-task-submit"`) {
		t.Fatalf("expected dispatch submit button id for async submit handling, body=%s", body)
	}
	if !strings.Contains(body, `data-lucide="send"`) || !strings.Contains(body, `data-dispatch-submit-label`) {
		t.Fatalf("expected dispatch submit button to render a leading send icon and label span, body=%s", body)
	}
	if !strings.Contains(body, `data-dispatch-submit-label>Dispatch</span>`) {
		t.Fatalf("expected dispatch submit label text to remain Dispatch, body=%s", body)
	}
	if !strings.Contains(body, `const dispatchTaskClear = document.getElementById("dispatch-task-clear");`) {
		t.Fatalf("expected clear button hook in client script, body=%s", body)
	}
	if strings.Contains(body, `dispatchOverview`) || strings.Contains(body, `DISPATCH_OVERVIEW_STORAGE_KEY`) || strings.Contains(body, `dismissDispatchOverview`) {
		t.Fatalf("did not expect removed dispatch overview client hooks, body=%s", body)
	}
	if !strings.Contains(body, `dispatchTaskClear.disabled = busy;`) {
		t.Fatalf("expected dispatch busy state to disable the clear action, body=%s", body)
	}
	if !strings.Contains(body, `const dispatchLabel = dispatchTaskSubmit.querySelector("[data-dispatch-submit-label]");`) {
		t.Fatalf("expected dispatch submit busy helper to target the inline label span first, body=%s", body)
	}
	if !strings.Contains(body, `dispatchLabel.textContent = busy ? "Dispatching..." : "Dispatch";`) {
		t.Fatalf("expected dispatch submit busy helper to update icon-button label copy, body=%s", body)
	}
	if !strings.Contains(body, `dispatchTaskSubmit.textContent = busy ? "Dispatching..." : "Dispatch";`) {
		t.Fatalf("expected dispatch submit busy helper fallback for non-icon markup, body=%s", body)
	}
	if !strings.Contains(body, `id="dispatch-submit-status"`) || !strings.Contains(body, `connected-agents-refresh-status`) {
		t.Fatalf("expected inline dispatch status region for async dispatch feedback, body=%s", body)
	}
	if !strings.Contains(body, `await fetch("/api/dispatch"`) {
		t.Fatalf("expected manual dispatch async API submit hook, body=%s", body)
	}
	if !strings.Contains(body, app.DispatchSelectionRequiredMessage) {
		t.Fatalf("expected client-side empty-target dispatch guard, body=%s", body)
	}
	if !strings.Contains(body, `Select a skill for the chosen agent before dispatching.`) {
		t.Fatalf("expected client-side empty-skill dispatch guard, body=%s", body)
	}
	if !strings.Contains(body, `const resetManualDispatchForm = () => {`) {
		t.Fatalf("expected manual dispatch reset helper, body=%s", body)
	}
	if !strings.Contains(body, `targetAgentRefInput.value = latestConnectedAgents.length > 0`) {
		t.Fatalf("expected manual dispatch reset to restore the first connected agent, body=%s", body)
	}
	if !strings.Contains(body, `syncConnectedAgentSelection({ forceFirstSkill: true });`) {
		t.Fatalf("expected manual dispatch reset to restore the first skill selection, body=%s", body)
	}
	if !strings.Contains(body, `dispatchTaskClear.addEventListener("click", () => {`) {
		t.Fatalf("expected manual dispatch clear click handler, body=%s", body)
	}
	if !strings.Contains(body, `skillPayloadInput.value = "";`) {
		t.Fatalf("expected manual dispatch flows to clear the payload input, body=%s", body)
	}
	if !strings.Contains(body, `id="sub-actions-notice" class="panel dispatch-gate-panel" hidden`) {
		t.Fatalf("expected sub-action notice to be hidden when bound and connected, body=%s", body)
	}
}

func TestHandleDispatchAcceptsMinimalTargetOnlyForm(t *testing.T) {
	t.Parallel()

	stub := &stubService{
		dispatchTask: app.PendingTask{ID: "task-1"},
	}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/dispatch", strings.NewReader("target_agent_ref=worker-a"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect response, got %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Fatalf("expected root redirect location, got %q", got)
	}
	if got := stub.lastDispatchReq.TargetAgentRef; got != "worker-a" {
		t.Fatalf("unexpected target agent ref: %#v", stub.lastDispatchReq)
	}
	if got := stub.lastDispatchReq.SkillName; got != "" {
		t.Fatalf("expected empty skill name for target-only dispatch, got %#v", got)
	}
	if stub.lastDispatchReq.Payload != nil {
		t.Fatalf("expected nil payload for minimal dispatch, got %#v", stub.lastDispatchReq.Payload)
	}
	if got := stub.lastDispatchReq.PayloadFormat; got != "" {
		t.Fatalf("expected empty payload format for minimal dispatch, got %#v", got)
	}
	if got := stub.state.Flash; got.Level != "info" || got.Message != "Dispatched task task-1" {
		t.Fatalf("unexpected flash after dispatch: %#v", got)
	}
}

func TestHandleIndexIncludesSkillSchemaPlaceholderSupport(t *testing.T) {
	t.Parallel()

	stub := &stubService{
		state: app.AppState{
			Session: app.Session{
				AgentToken:      "agent-token",
				Handle:          "codex-beast",
				HandleFinalized: true,
				DisplayName:     "Dispatch Agent",
				BoundAt:         time.Now().UTC(),
			},
			ConnectedAgents: []app.ConnectedAgent{
				{
					AgentID:   "worker-a",
					Handle:    "worker-a",
					AgentUUID: "worker-uuid",
					URI:       "molten://agent/worker-a",
					Metadata: &hub.AgentMetadata{
						DisplayName: "Worker A",
						Skills: []map[string]any{
							{
								"name":        "run_task",
								"description": "Run task with structured payload.",
								"schema": map[string]any{
									"type":     "object",
									"required": []any{"repo", "prompt"},
								},
							},
						},
					},
				},
			},
		},
	}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `const skillPayloadHint = document.getElementById("skill-payload-hint");`) {
		t.Fatalf("expected payload hint hook for schema-aware readonly copy, body=%s", body)
	}
	if !strings.Contains(body, `const defaultSkillPayloadHint = skillPayloadHint instanceof HTMLElement`) {
		t.Fatalf("expected default payload hint capture before schema overrides, body=%s", body)
	}
	if !strings.Contains(body, `const extractSkillSchema = (skill) => {`) {
		t.Fatalf("expected skill schema extraction helper in client script, body=%s", body)
	}
	if !strings.Contains(body, `for (const key of ["schema", "input_schema", "payload_schema", "inputSchema", "payloadSchema", "parameters", "args_schema", "argsSchema"])`) {
		t.Fatalf("expected schema alias scan in client script, body=%s", body)
	}
	if !strings.Contains(body, `schemaText: trimmedString(schemaText),`) {
		t.Fatalf("expected skill entries to preserve schema text for readonly payload text, body=%s", body)
	}
	if !strings.Contains(body, `Required payload schema shown in readonly payload text box. Markdown and JSON are both supported.`) {
		t.Fatalf("expected schema-specific payload hint copy, body=%s", body)
	}
	if !strings.Contains(body, `const setSkillPayloadSchemaText = (schemaText) => {`) {
		t.Fatalf("expected payload schema text helper in client script, body=%s", body)
	}
	if !strings.Contains(body, `skillPayloadInput.readOnly = nextSchemaText !== "";`) {
		t.Fatalf("expected selected skill schema to make payload text readonly, body=%s", body)
	}
	if !strings.Contains(body, `skillPayloadInput.value = nextSchemaText;`) {
		t.Fatalf("expected selected skill schema to render inside payload text box, body=%s", body)
	}
	if !strings.Contains(body, `formData.delete("payload");`) || !strings.Contains(body, `formData.delete("payload_format");`) {
		t.Fatalf("expected readonly schema reference text to be omitted from dispatch payload, body=%s", body)
	}
}

func TestHandleDispatchAPIAcceptsMinimalTargetOnlyForm(t *testing.T) {
	t.Parallel()

	stub := &stubService{
		dispatchTask: app.PendingTask{ID: "task-1"},
	}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/dispatch", strings.NewReader("target_agent_ref=worker-a"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}

	var body struct {
		OK      bool   `json:"ok"`
		TaskID  string `json:"task_id"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.OK {
		t.Fatalf("expected success response, got %#v", body)
	}
	if body.TaskID != "task-1" {
		t.Fatalf("expected task_id, got %#v", body)
	}
	if body.Message != "Dispatched task task-1" {
		t.Fatalf("unexpected response message: %#v", body)
	}
	if got := stub.lastDispatchReq.TargetAgentRef; got != "worker-a" {
		t.Fatalf("unexpected target agent ref: %#v", stub.lastDispatchReq)
	}
	if got := stub.lastDispatchReq.PayloadFormat; got != "" {
		t.Fatalf("expected empty payload format for target-only dispatch, got %#v", stub.lastDispatchReq)
	}
}

func TestHandleDispatchAPIAcceptsSelectedAgentAndSkillAliases(t *testing.T) {
	t.Parallel()

	stub := &stubService{
		dispatchTask: app.PendingTask{ID: "task-1"},
	}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	form := "selectedAgentRef=worker-a&selectedSkill=code_for_me&payload_format=text&payload=Review+the+Hub+API+integration+behavior."
	req := httptest.NewRequest(http.MethodPost, "/api/dispatch", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}

	if got := stub.lastDispatchReq.TargetAgentRef; got != "worker-a" {
		t.Fatalf("unexpected target agent ref: %#v", stub.lastDispatchReq)
	}
	if got := stub.lastDispatchReq.SkillName; got != "code_for_me" {
		t.Fatalf("unexpected skill name: %#v", stub.lastDispatchReq)
	}
	if got := stub.lastDispatchReq.PayloadFormat; got != "markdown" {
		t.Fatalf("expected payload format markdown, got %#v", stub.lastDispatchReq)
	}
	if got := stub.lastDispatchReq.Payload; got != testDispatchPrompt {
		t.Fatalf("unexpected payload value: %#v", stub.lastDispatchReq.Payload)
	}
}

func TestHandleDispatchAPIAcceptsMultipartFormData(t *testing.T) {
	t.Parallel()

	stub := &stubService{
		dispatchTask: app.PendingTask{ID: "task-1"},
	}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"target_agent_ref": "worker-a",
		"skill_name":       "code_for_me",
		"payload":          "{\"repos\":[\"git@github.com:Molten-Bot/moltenhub-dispatch.git\"],\"prompt\":\"Review the Hub API integration behavior.\"}",
	} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write multipart field %q: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/dispatch", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d body=%s", rec.Code, rec.Body.String())
	}

	if got := stub.lastDispatchReq.TargetAgentRef; got != "worker-a" {
		t.Fatalf("unexpected target agent ref: %#v", stub.lastDispatchReq)
	}
	if got := stub.lastDispatchReq.SkillName; got != "code_for_me" {
		t.Fatalf("unexpected skill name: %#v", stub.lastDispatchReq)
	}
	if got := stub.lastDispatchReq.PayloadFormat; got != "json" {
		t.Fatalf("expected payload format json, got %#v", stub.lastDispatchReq)
	}
	payload, ok := stub.lastDispatchReq.Payload.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON payload map, got %T", stub.lastDispatchReq.Payload)
	}
	if got := payload["prompt"]; got != testDispatchPrompt {
		t.Fatalf("unexpected prompt payload: %#v", payload)
	}
}

func TestHandleDispatchAPIAcceptsJSONPayloadWithTabbedPrompt(t *testing.T) {
	t.Parallel()

	stub := &stubService{
		dispatchTask: app.PendingTask{ID: "task-1"},
	}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	form := url.Values{}
	form.Set("target_agent_ref", "worker-a")
	form.Set("skill_name", "code_for_me")
	form.Set("payload_format", "json")
	form.Set("payload", "{\"prompt\":\"Review\tlogs\"}")
	req := httptest.NewRequest(http.MethodPost, "/api/dispatch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d body=%s", rec.Code, rec.Body.String())
	}

	if got := stub.lastDispatchReq.PayloadFormat; got != "json" {
		t.Fatalf("expected payload format json, got %#v", stub.lastDispatchReq)
	}
	payload, ok := stub.lastDispatchReq.Payload.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON payload map, got %T", stub.lastDispatchReq.Payload)
	}
	if got := payload["prompt"]; got != "Review\tlogs" {
		t.Fatalf("unexpected prompt payload: %#v", payload)
	}
}

func TestHandleDispatchMapsTextPayloadFormatAliasToMarkdown(t *testing.T) {
	t.Parallel()

	stub := &stubService{
		dispatchTask: app.PendingTask{ID: "task-1"},
	}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	form := "target_agent_ref=worker-a&skill_name=run_task&payload_format=text&payload=Review+the+Hub+API+integration+behavior."
	req := httptest.NewRequest(http.MethodPost, "/dispatch", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect response, got %d", rec.Code)
	}
	if got := stub.lastDispatchReq.PayloadFormat; got != "markdown" {
		t.Fatalf("expected payload format markdown, got %#v", got)
	}
	if got := stub.lastDispatchReq.Payload; got != testDispatchPrompt {
		t.Fatalf("unexpected payload value: %#v", got)
	}
}

func TestHandleDispatchAutoPromotesJSONObjectMarkdownPayloadToJSON(t *testing.T) {
	t.Parallel()

	stub := &stubService{
		dispatchTask: app.PendingTask{ID: "task-1"},
	}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	form := "target_agent_ref=worker-a&skill_name=run_task&payload_format=markdown&payload=%7B%22prompt%22%3A%22Review+logs%22%2C%22retry%22%3Atrue%7D"
	req := httptest.NewRequest(http.MethodPost, "/dispatch", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect response, got %d", rec.Code)
	}
	if got := stub.lastDispatchReq.PayloadFormat; got != "json" {
		t.Fatalf("expected payload format json, got %#v", got)
	}
	payload, ok := stub.lastDispatchReq.Payload.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON payload map, got %T", stub.lastDispatchReq.Payload)
	}
	if got := payload["prompt"]; got != "Review logs" {
		t.Fatalf("unexpected prompt payload value: %#v", payload)
	}
	if got := payload["retry"]; got != true {
		t.Fatalf("unexpected retry payload value: %#v", payload)
	}
}

func TestHandleDispatchAutoPromotesJSONArrayPayloadToJSONWhenFormatIsOmitted(t *testing.T) {
	t.Parallel()

	stub := &stubService{
		dispatchTask: app.PendingTask{ID: "task-1"},
	}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	form := "target_agent_ref=worker-a&skill_name=run_task&payload=%5B%7B%22path%22%3A%22logs%2Ffailure.log%22%7D%5D"
	req := httptest.NewRequest(http.MethodPost, "/dispatch", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect response, got %d", rec.Code)
	}
	if got := stub.lastDispatchReq.PayloadFormat; got != "json" {
		t.Fatalf("expected payload format json, got %#v", got)
	}
	payload, ok := stub.lastDispatchReq.Payload.([]any)
	if !ok {
		t.Fatalf("expected JSON payload array, got %T", stub.lastDispatchReq.Payload)
	}
	if len(payload) != 1 {
		t.Fatalf("expected one payload item, got %#v", payload)
	}
	entry, ok := payload[0].(map[string]any)
	if !ok {
		t.Fatalf("expected JSON payload map entry, got %#v", payload[0])
	}
	if got := entry["path"]; got != "logs/failure.log" {
		t.Fatalf("unexpected payload path: %#v", entry)
	}
}

func TestHandleDispatchDropsPayloadFormatWhenPayloadMissing(t *testing.T) {
	t.Parallel()

	stub := &stubService{
		dispatchTask: app.PendingTask{ID: "task-1"},
	}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	form := "target_agent_ref=worker-a&skill_name=run_task&payload_format=text"
	req := httptest.NewRequest(http.MethodPost, "/dispatch", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect response, got %d", rec.Code)
	}
	if stub.lastDispatchReq.Payload != nil {
		t.Fatalf("expected nil payload when payload field is missing, got %#v", stub.lastDispatchReq.Payload)
	}
	if got := stub.lastDispatchReq.PayloadFormat; got != "" {
		t.Fatalf("expected empty payload format when payload field is missing, got %#v", got)
	}
}

func TestHandleDispatchRejectsUnknownPayloadFormat(t *testing.T) {
	t.Parallel()

	stub := &stubService{}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	form := "target_agent_ref=worker-a&skill_name=run_task&payload_format=xml&payload=%3Ctask%2F%3E"
	req := httptest.NewRequest(http.MethodPost, "/dispatch", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect response, got %d", rec.Code)
	}
	if got := stub.state.Flash.Level; got != "error" {
		t.Fatalf("expected error flash level, got %#v", stub.state.Flash)
	}
	if got := stub.state.Flash.Message; got != "payload_format must be one of markdown or json" {
		t.Fatalf("unexpected error flash message: %#v", got)
	}
}

func TestHandleDispatchRejectsInvalidJSONPayload(t *testing.T) {
	t.Parallel()

	stub := &stubService{}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	form := "target_agent_ref=worker-a&skill_name=run_task&payload_format=json&payload=%7B%22prompt%22%3A"
	req := httptest.NewRequest(http.MethodPost, "/dispatch", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect response, got %d", rec.Code)
	}
	if got := stub.lastDispatchReq; got.TargetAgentRef != "" || got.SkillName != "" || got.Payload != nil || got.PayloadFormat != "" {
		t.Fatalf("expected dispatch request to be rejected before service call, got %#v", got)
	}
	if got := stub.state.Flash.Level; got != "error" {
		t.Fatalf("expected error flash level, got %#v", stub.state.Flash)
	}
	if got := stub.state.Flash.Message; !strings.Contains(got, "payload JSON is invalid:") {
		t.Fatalf("expected JSON parse error flash message, got %#v", got)
	}
}

func TestHandleDispatchAPIRejectsUnknownPayloadFormat(t *testing.T) {
	t.Parallel()

	stub := &stubService{}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	form := "target_agent_ref=worker-a&skill_name=run_task&payload_format=xml&payload=%3Ctask%2F%3E"
	req := httptest.NewRequest(http.MethodPost, "/api/dispatch", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 response, got %d", rec.Code)
	}

	var body struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.OK {
		t.Fatalf("expected failure response, got %#v", body)
	}
	if body.Error != "payload_format must be one of markdown or json" {
		t.Fatalf("unexpected API error message: %#v", body)
	}
	if stub.state.Flash != (app.FlashMessage{}) {
		t.Fatalf("did not expect flash mutation for API failures, got %#v", stub.state.Flash)
	}
}

func TestHandleDispatchRejectsEmptySelection(t *testing.T) {
	t.Parallel()

	stub := &stubService{}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/dispatch", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect response, got %d", rec.Code)
	}
	if got := stub.lastDispatchReq; got.TargetAgentRef != "" || got.SkillName != "" || got.Payload != nil || got.PayloadFormat != "" {
		t.Fatalf("expected dispatch request to be rejected before service call, got %#v", got)
	}
	if got := stub.state.Flash.Level; got != "error" {
		t.Fatalf("expected error flash level, got %#v", stub.state.Flash)
	}
	if got := stub.state.Flash.Message; got != app.DispatchSelectionRequiredMessage {
		t.Fatalf("unexpected flash message: %#v", got)
	}
}

func TestHandleDispatchAPIRejectsEmptySelection(t *testing.T) {
	t.Parallel()

	stub := &stubService{}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/dispatch", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 response, got %d", rec.Code)
	}

	var body struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.OK {
		t.Fatalf("expected failure response, got %#v", body)
	}
	if body.Error != app.DispatchSelectionRequiredMessage {
		t.Fatalf("unexpected API error message: %#v", body)
	}
	if got := stub.lastDispatchReq; got.TargetAgentRef != "" || got.SkillName != "" || got.Payload != nil || got.PayloadFormat != "" {
		t.Fatalf("expected dispatch request to be rejected before service call, got %#v", got)
	}
	if stub.state.Flash != (app.FlashMessage{}) {
		t.Fatalf("did not expect flash mutation for API failures, got %#v", stub.state.Flash)
	}
}

func TestHandleIndexShowsConnectAgentsPanelWhenNoConnectedAgents(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusConnected,
				Transport: app.ConnectionTransportHTTP,
			},
			Session: app.Session{
				AgentToken: "agent-token",
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}
	if !strings.Contains(body, `id="sub-actions" hidden`) {
		t.Fatalf("expected manual dispatch container to stay hidden without connected agents, body=%s", body)
	}
	if !strings.Contains(body, "Connect agents in Molten Bot Hub") {
		t.Fatalf("expected connect-agents panel copy, body=%s", body)
	}
	if !strings.Contains(body, "Bound agents are listed in Molten Bot Hub") {
		t.Fatalf("expected talkable-peer clarification in connect-agents panel, body=%s", body)
	}
	if !strings.Contains(body, `class="sub-actions-hub-link" href="https://app.molten.bot/hub"`) {
		t.Fatalf("expected connect-agents panel link to Molten Bot Hub dashboard, body=%s", body)
	}
	if strings.Contains(body, `id="sub-actions-notice-refresh"`) {
		t.Fatalf("did not expect notice panel refresh control on the main page, body=%s", body)
	}
}

func TestHandleIndexRendersPendingTasksPanelInMainUI(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusConnected,
				Transport: app.ConnectionTransportHTTP,
			},
			Session: app.Session{
				AgentToken: "agent-token",
			},
			PendingTasks: []app.PendingTask{
				{
					ID:                     "task-1",
					Status:                 app.PendingTaskStatusInQueue,
					OriginalSkillName:      "run_task",
					TargetAgentDisplayName: "Worker A",
					TargetAgentEmoji:       "🛠",
					TargetAgentUUID:        "worker-uuid",
					LogPath:                "/tmp/logs/task-1.log",
					ExpiresAt:              time.Now().Add(time.Minute),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}
	if !strings.Contains(body, ">Recent Activity<") {
		t.Fatalf("expected consolidated activity panel in main UI, body=%s", body)
	}
	if !strings.Contains(body, "Worker A") || !strings.Contains(body, "🛠") {
		t.Fatalf("expected activity card to render agent display name and emoji, body=%s", body)
	}
	if !strings.Contains(body, `data-lucide="clock-3"`) || !strings.Contains(body, "runtime-event-card-status-icon") {
		t.Fatalf("expected activity card to render in-queue status as an icon, body=%s", body)
	}
	if !strings.Contains(body, "Pending task") {
		t.Fatalf("expected consolidated activity feed to label pending tasks, body=%s", body)
	}
}

func TestHandleIndexIncludesPollingHooksForQueueAndActivity(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Session: app.Session{
				AgentToken: "agent-token",
			},
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusConnected,
				Transport: app.ConnectionTransportHTTP,
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}
	if !strings.Contains(body, `id="activity-feed-list"`) {
		t.Fatalf("expected activity feed list hook, body=%s", body)
	}
	if !strings.Contains(body, `id="activity-feed-list" class="list activity-feed-list"`) {
		t.Fatalf("expected activity feed list to include lane layout class, body=%s", body)
	}
	if !strings.Contains(body, `id="activity-feed-empty"`) {
		t.Fatalf("expected activity feed empty-state hook, body=%s", body)
	}
	if !strings.Contains(body, `id="initial-pending-tasks-data" type="application/json"`) {
		t.Fatalf("expected initial pending-tasks bootstrap payload script, body=%s", body)
	}
	if !strings.Contains(body, `id="initial-recent-events-data" type="application/json"`) {
		t.Fatalf("expected initial recent-events bootstrap payload script, body=%s", body)
	}
	if !strings.Contains(body, `const pendingTasks = Array.isArray(snapshot && snapshot.pending_tasks)`) {
		t.Fatalf("expected status polling to extract pending tasks from /status payload, body=%s", body)
	}
	if !strings.Contains(body, `const recentEvents = Array.isArray(snapshot && snapshot.recent_events)`) {
		t.Fatalf("expected status polling to extract recent events from /status payload, body=%s", body)
	}
	if !strings.Contains(body, `renderActivityFeed(pendingTasks, recentEvents);`) {
		t.Fatalf("expected status polling to rerender consolidated activity feed, body=%s", body)
	}
	if !strings.Contains(body, `const mergeActivityFeed = (tasks, events) => {`) {
		t.Fatalf("expected activity feed merge helper, body=%s", body)
	}
	if !strings.Contains(body, `const activityFeedLaneKey = (item) => {`) {
		t.Fatalf("expected activity feed lane grouping key helper, body=%s", body)
	}
	if !strings.Contains(body, `const groupActivityFeedLanes = (feed) => {`) {
		t.Fatalf("expected activity feed lane grouping helper, body=%s", body)
	}
	if !strings.Contains(body, `return leftSortAt - rightSortAt;`) {
		t.Fatalf("expected grouped activity cards to keep oldest events on the left, body=%s", body)
	}
	if !strings.Contains(body, `laneTrack.className = "activity-feed-lane-track";`) {
		t.Fatalf("expected grouped activity renderer to build horizontal lane tracks, body=%s", body)
	}
	if !strings.Contains(body, `laneOrder.textContent = "Oldest -> newest";`) {
		t.Fatalf("expected grouped activity renderer to annotate left-to-right ordering, body=%s", body)
	}
	if !strings.Contains(body, `let activityFeedExpandedKeys = new Set();`) {
		t.Fatalf("expected activity feed expanded-state cache, body=%s", body)
	}
	if !strings.Contains(body, `const activityFeedItemKey = (item) => {`) {
		t.Fatalf("expected activity feed stable-key helper, body=%s", body)
	}
	if !strings.Contains(body, `const expanded = activityFeedExpandedKeys.has(activityKey);`) {
		t.Fatalf("expected activity feed refresh to reuse expanded cards, body=%s", body)
	}
	if !strings.Contains(body, `card.dataset.runtimeEventLaneKey = laneKey;`) {
		t.Fatalf("expected grouped activity cards to record lane identity, body=%s", body)
	}
	if !strings.Contains(body, `if (trimmedString(sibling.dataset.runtimeEventLaneKey) !== laneKey) {`) {
		t.Fatalf("expected runtime event expansion to stay scoped to same lane, body=%s", body)
	}
	if !strings.Contains(body, `setRuntimeEventCardExpanded(sibling, true)`) {
		t.Fatalf("expected opening one activity card to reveal sibling cards in same lane, body=%s", body)
	}
	if !strings.Contains(body, `activityFeedExpandedKeys = nextExpandedKeys;`) {
		t.Fatalf("expected activity feed refresh to persist open-card state, body=%s", body)
	}
	if !strings.Contains(body, `runtimeTargetAgentLabel(task)`) {
		t.Fatalf("expected consolidated feed to resolve pending-task target-agent label, body=%s", body)
	}
	if !strings.Contains(body, `task && task.original_skill_name`) {
		t.Fatalf("expected consolidated feed to include pending task skill label, body=%s", body)
	}
	if !strings.Contains(body, `event && event.original_skill_name`) {
		t.Fatalf("expected consolidated feed to include recent event skill label, body=%s", body)
	}
}

func TestHandleIndexRendersBottomDockAndSettingsDialogForBoundSession(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusConnected,
				Transport: app.ConnectionTransportHTTP,
			},
			Session: app.Session{
				AgentToken:      "agent-token",
				Handle:          "dispatch-agent",
				HandleFinalized: true,
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}
	if !strings.Contains(body, `class="page-bottom-dock"`) {
		t.Fatalf("expected bottom dock container, body=%s", body)
	}
	if !strings.Contains(body, `id="moltenbot-hub-link"`) {
		t.Fatalf("expected molten hub dock link, body=%s", body)
	}
	if !strings.Contains(body, `id="moltenbot-hub-link"`) || !strings.Contains(body, `<img src="/static/logo.svg" alt="" aria-hidden="true">`) {
		t.Fatalf("expected molten hub dock link to use bundled logo asset, body=%s", body)
	}
	if !strings.Contains(body, `<span class="prompt-mode-mobile-label" aria-hidden="true">Hub</span>`) {
		t.Fatalf("expected molten hub dock link to expose mobile footer label, body=%s", body)
	}
	if !strings.Contains(body, `id="agent-settings-dock-button"`) {
		t.Fatalf("expected settings dock button, body=%s", body)
	}
	if !strings.Contains(body, `class="prompt-mode-link prompt-mode-link-logo hub-profile-button"`) {
		t.Fatalf("expected settings dock button to render without an active class, body=%s", body)
	}
	if !strings.Contains(body, `<span class="prompt-mode-mobile-label" aria-hidden="true">Settings</span>`) {
		t.Fatalf("expected settings dock button to expose mobile footer label, body=%s", body)
	}
	if !strings.Contains(body, `id="theme-toggle"`) {
		t.Fatalf("expected theme toggle dock button, body=%s", body)
	}
	if !strings.Contains(body, `<span class="theme-toggle-icon" id="theme-toggle-icon" aria-hidden="true"></span>`) {
		t.Fatalf("expected theme toggle icon slot, body=%s", body)
	}
	if !strings.Contains(body, `<span class="prompt-mode-mobile-label" aria-hidden="true">Theme</span>`) {
		t.Fatalf("expected theme toggle to expose mobile footer label, body=%s", body)
	}
	if !strings.Contains(body, `<span id="theme-toggle-label">Dark</span>`) {
		t.Fatalf("expected dark as the initial theme label, body=%s", body)
	}
	if !strings.Contains(body, `<div class="site-bg" aria-hidden="true">`) {
		t.Fatalf("expected themed background shell, body=%s", body)
	}
	if strings.Contains(body, `id="snowfall-canvas"`) {
		t.Fatalf("did not expect animated snowfall canvas, body=%s", body)
	}
	if !strings.Contains(body, `id="onboarding-modal-backdrop"`) || !strings.Contains(body, `aria-hidden="true"`) {
		t.Fatalf("expected shared profile dialog markup, body=%s", body)
	}
	if !strings.Contains(body, `id="agent-profile-modal-close"`) {
		t.Fatalf("expected profile dialog close control, body=%s", body)
	}
	if !strings.Contains(body, `aria-label="Close agent profile"`) || !strings.Contains(body, `data-lucide="x"`) {
		t.Fatalf("expected profile dialog close to be an icon-only X button, body=%s", body)
	}
	if !strings.Contains(body, `Update how this agent appears in Molten Hub.`) {
		t.Fatalf("expected updated agent settings summary copy, body=%s", body)
	}
	if strings.Contains(body, `Update how this dispatcher appears in Molten Hub.`) {
		t.Fatalf("did not expect outdated dispatcher settings summary copy, body=%s", body)
	}
	if strings.Contains(body, `Refresh Connected Agents`) {
		t.Fatalf("did not expect text label inside icon-only refresh button, body=%s", body)
	}
	if !strings.Contains(body, `id="bind-form"`) || !strings.Contains(body, `data-bound="true"`) || !strings.Contains(body, `action="/profile"`) {
		t.Fatalf("expected settings to use the shared bound profile form, body=%s", body)
	}
	if !strings.Contains(body, `id="agent-disconnect-submit" formaction="/disconnect"`) {
		t.Fatalf("expected disconnect button in shared profile form, body=%s", body)
	}
	if !strings.Contains(body, `aria-label="Disconnect agent"`) || !strings.Contains(body, `data-lucide="unlink"`) {
		t.Fatalf("expected disconnect to be an unlink icon button, body=%s", body)
	}
	disconnectIndex := strings.Index(body, `id="agent-disconnect-submit"`)
	closeIndex := strings.Index(body, `id="agent-profile-modal-close"`)
	saveIndex := strings.Index(body, `id="bind-submit" disabled>Save</button>`)
	if disconnectIndex < 0 || closeIndex < 0 || saveIndex < 0 || !(disconnectIndex < closeIndex && closeIndex < saveIndex) {
		t.Fatalf("expected close action between disconnect and save, body=%s", body)
	}
	if !strings.Contains(body, `id="bind-submit" disabled>Save</button>`) {
		t.Fatalf("expected save button to start disabled in profile form, body=%s", body)
	}
	if !strings.Contains(body, `const syncAgentProfileSaveState = () => {`) || !strings.Contains(body, `bindSubmit.disabled = !agentProfileChanged();`) {
		t.Fatalf("expected profile save dirty-state tracking, body=%s", body)
	}
	if !strings.Contains(body, `const agentSettingsDockButton = document.getElementById("agent-settings-dock-button");`) {
		t.Fatalf("expected settings dock JS hook, body=%s", body)
	}
	if !strings.Contains(body, `const themeToggleButton = document.getElementById("theme-toggle");`) {
		t.Fatalf("expected theme toggle JS hook, body=%s", body)
	}
	if !strings.Contains(body, `const THEME_MODES = ["light", "dark", "night", "pink"];`) || !strings.Contains(body, `const DEFAULT_THEME_MODE = "dark";`) {
		t.Fatalf("expected theme cycle constants, body=%s", body)
	}
	if !strings.Contains(body, `const THEME_ICONS = {`) || !strings.Contains(body, `pink: "heart",`) {
		t.Fatalf("expected theme icon map for toggle button, body=%s", body)
	}
	if !strings.Contains(body, `const initAppearanceControls = () => {`) || !strings.Contains(body, `applyThemeMode(loadThemeMode(), false);`) {
		t.Fatalf("expected theme controls initialization, body=%s", body)
	}
	if !strings.Contains(body, `themeToggleButton.addEventListener("click", () => {`) || !strings.Contains(body, `const nextTheme = nextThemeMode(currentThemeMode());`) || !strings.Contains(body, `applyThemeMode(nextTheme, true);`) {
		t.Fatalf("expected theme toggle click cycle handler, body=%s", body)
	}
	if strings.Contains(body, `const initSnowfallBackground = () => {`) || strings.Contains(body, `initSnowfallBackground();`) {
		t.Fatalf("did not expect snowfall background animation initialization, body=%s", body)
	}
	if !strings.Contains(body, `const setAgentSettingsModalOpen = (open, returnFocus = false) => {`) {
		t.Fatalf("expected settings dialog open/close handler, body=%s", body)
	}
}

func TestHandleIndexKeepsRecentEventsClosedByDefault(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Session: app.Session{
				AgentToken: "agent-token",
			},
			RecentEvents: []app.RuntimeEvent{
				{
					Title:                  "Task dispatched",
					Level:                  "info",
					Detail:                 "Queued code_for_me for moltenbot/jef/codex-beast",
					TaskID:                 "task-123",
					LogPath:                ".moltenhub/logs/task-123.log",
					OriginalSkillName:      "run_task",
					TargetAgentDisplayName: "Worker A",
					TargetAgentEmoji:       "🛠",
					At:                     time.Unix(1, 0).UTC(),
				},
				{
					Title:   "Dispatch failed",
					Level:   "error",
					Detail:  "worker panic: boom",
					TaskID:  "task-456",
					LogPath: ".moltenhub/logs/task-456.log",
					At:      time.Unix(2, 0).UTC(),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}
	if !strings.Contains(body, `class="card runtime-event-card" data-runtime-event-card`) {
		t.Fatalf("expected recent event cards to use collapsible card class, body=%s", body)
	}
	if !strings.Contains(body, `data-runtime-event-toggle`) || !strings.Contains(body, `data-lucide="maximize-2"`) {
		t.Fatalf("expected info event toggle icon to render in collapsed open state by default, body=%s", body)
	}
	if !strings.Contains(body, `class="runtime-event-card-body" data-runtime-event-body hidden`) {
		t.Fatalf("expected info event body to render hidden by default, body=%s", body)
	}
	if !strings.Contains(body, "🛠 Worker A") {
		t.Fatalf("expected recent event card to render target-agent emoji and display name, body=%s", body)
	}
	if !strings.Contains(body, "run_task • Task dispatched") {
		t.Fatalf("expected recent event card to render skill + title subtitle in consolidated feed, body=%s", body)
	}
	if !strings.Contains(body, `class="runtime-event-detail-row"><i data-lucide="clock-3" class="runtime-event-detail-icon"`) {
		t.Fatalf("expected recent event details to render labeled icons, body=%s", body)
	}
	if !strings.Contains(body, `data-lucide="sparkles" class="runtime-event-detail-icon"`) || !strings.Contains(body, `data-lucide="bot" class="runtime-event-detail-icon"`) {
		t.Fatalf("expected recent event details to icon skill and target agent rows, body=%s", body)
	}
	if strings.Contains(body, `data-runtime-event-toggle aria-expanded="true"`) {
		t.Fatalf("expected all runtime event toggles to render collapsed by default, body=%s", body)
	}
	if !strings.Contains(body, `const runtimeEventCards = Array.from(document.querySelectorAll("[data-runtime-event-card]"));`) {
		t.Fatalf("expected runtime event JS collection hook, body=%s", body)
	}
	if !strings.Contains(body, `const initRuntimeEventCards = () => {`) {
		t.Fatalf("expected runtime event initialization helper, body=%s", body)
	}
	if !strings.Contains(body, `const syncRuntimeEventToggleButton = (toggle, expanded) => {`) {
		t.Fatalf("expected runtime event toggle icon sync helper, body=%s", body)
	}
	if !strings.Contains(body, `nextExpanded ? "minimize-2" : "maximize-2"`) {
		t.Fatalf("expected runtime event toggle to switch between maximize/minimize icons, body=%s", body)
	}
	if !strings.Contains(body, `syncRuntimeEventToggleButton(toggle, nextExpanded);`) {
		t.Fatalf("expected runtime event toggle visuals to update inside expansion handler, body=%s", body)
	}
	if !strings.Contains(body, `const runtimeDetailIconName = (label, value = "") => {`) {
		t.Fatalf("expected activity refresh path to choose detail-row icons, body=%s", body)
	}
	if !strings.Contains(body, `initRuntimeEventCards();`) {
		t.Fatalf("expected runtime event toggle initialization on page load, body=%s", body)
	}
}

func TestHandleIndexKeepsPendingTasksClosedByDefault(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Session: app.Session{
				AgentToken: "agent-token",
			},
			PendingTasks: []app.PendingTask{
				{
					ID:                     "task-1",
					Status:                 app.PendingTaskStatusSending,
					OriginalSkillName:      "run_task",
					ChildRequestID:         "child-1",
					TargetAgentDisplayName: "Worker A",
					TargetAgentEmoji:       "🛠",
					TargetAgentUUID:        "worker-uuid",
					Repo:                   "git@github.com:Molten-Bot/moltenhub-code.git",
					LogPath:                ".moltenhub/logs/task-1.log",
					ExpiresAt:              time.Now().Add(time.Minute),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}
	if !strings.Contains(body, ">Recent Activity<") {
		t.Fatalf("expected consolidated activity panel, body=%s", body)
	}
	if !strings.Contains(body, `class="card runtime-event-card" data-runtime-event-card`) {
		t.Fatalf("expected pending task cards to use collapsible card class in consolidated feed, body=%s", body)
	}
	if !strings.Contains(body, `data-runtime-event-toggle`) || !strings.Contains(body, `data-lucide="maximize-2"`) {
		t.Fatalf("expected pending task toggle icon to render collapsed open state by default in consolidated feed, body=%s", body)
	}
	if !strings.Contains(body, `class="runtime-event-card-body" data-runtime-event-body hidden`) {
		t.Fatalf("expected pending task body to render hidden by default in consolidated feed, body=%s", body)
	}
	if strings.Contains(body, `data-runtime-event-toggle aria-expanded="true"`) {
		t.Fatalf("expected pending task toggles to render collapsed by default, body=%s", body)
	}
	if !strings.Contains(body, "Sending") {
		t.Fatalf("expected pending task status label to render sending state, body=%s", body)
	}
	if !strings.Contains(body, `data-lucide="send" class="runtime-event-detail-icon"`) || !strings.Contains(body, `data-lucide="git-fork" class="runtime-event-detail-icon"`) {
		t.Fatalf("expected pending task details to render status and repo icons, body=%s", body)
	}
}

func TestHandleIndexMergesPendingTasksAndRecentEventsByTime(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Session: app.Session{
				AgentToken: "agent-token",
			},
			PendingTasks: []app.PendingTask{
				{
					ID:                     "task-newer",
					Status:                 app.PendingTaskStatusInQueue,
					OriginalSkillName:      "run_task",
					TargetAgentDisplayName: "Worker A",
					TargetAgentEmoji:       "🛠",
					CreatedAt:              time.Unix(20, 0).UTC(),
				},
			},
			RecentEvents: []app.RuntimeEvent{
				{
					Title:                  "Task dispatched",
					Level:                  "info",
					Detail:                 "older event",
					OriginalSkillName:      "review",
					TargetAgentDisplayName: "Worker B",
					TargetAgentEmoji:       "🔎",
					At:                     time.Unix(10, 0).UTC(),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}
	newerIndex := strings.Index(body, "🛠 Worker A")
	olderIndex := strings.Index(body, "🔎 Worker B")
	if newerIndex == -1 || olderIndex == -1 {
		t.Fatalf("expected merged activity feed to render both pending task and event, body=%s", body)
	}
	if newerIndex > olderIndex {
		t.Fatalf("expected consolidated activity feed to sort newer pending task before older event, body=%s", body)
	}
}

func TestHandleStylesEnsuresHiddenModalBackdropsStayHidden(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/styles.css", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}
	if !strings.Contains(body, `.settings-modal-backdrop[hidden],`) {
		t.Fatalf("expected settings modal hidden override rule, body=%s", body)
	}
	if !strings.Contains(body, `.onboarding-modal-backdrop[hidden]`) {
		t.Fatalf("expected onboarding modal hidden override rule, body=%s", body)
	}
	if !strings.Contains(body, `.manual-dispatch-grid {`) {
		t.Fatalf("expected manual dispatch full-width grid rule, body=%s", body)
	}
	if !strings.Contains(body, `grid-template-columns: minmax(0, 1fr);`) {
		t.Fatalf("expected manual dispatch section to force a single full-width column, body=%s", body)
	}
	if !strings.Contains(body, `.manual-dispatch-targets-grid {`) {
		t.Fatalf("expected horizontal manual dispatch target layout rule, body=%s", body)
	}
	if !strings.Contains(body, `flex-wrap: wrap;`) || !strings.Contains(body, `gap: 8px;`) {
		t.Fatalf("expected selectable agent cards to wrap with compact spacing, body=%s", body)
	}
	if !strings.Contains(body, `.manual-dispatch-targets-grid .connected-agent-card-button {`) || !strings.Contains(body, `width: auto;`) {
		t.Fatalf("expected selectable agent cards to size to content, body=%s", body)
	}
	if !strings.Contains(body, `.manual-dispatch-actions {`) || !strings.Contains(body, `justify-content: flex-end;`) {
		t.Fatalf("expected manual dispatch submit actions to right-align, body=%s", body)
	}
	if !strings.Contains(body, `.onboarding-message.is-error {`) || !strings.Contains(body, `color: var(--bad);`) {
		t.Fatalf("expected onboarding errors to render in the danger color, body=%s", body)
	}
	if !strings.Contains(body, `.onboarding-form-actions {`) || !strings.Contains(body, `align-items: center;`) || !strings.Contains(body, `min-height: 44px;`) {
		t.Fatalf("expected onboarding status and submit button to share a compact action row, body=%s", body)
	}
	if !strings.Contains(body, ".onboarding-modal-close {\n  width: 44px;") || !strings.Contains(body, ".onboarding-modal .onboarding-modal-close {\n  width: 44px;") {
		t.Fatalf("expected profile close button to align with footer icon actions, body=%s", body)
	}
	if !strings.Contains(body, `#bind-submit:disabled {`) || !strings.Contains(body, `cursor: not-allowed;`) {
		t.Fatalf("expected disabled save button styling, body=%s", body)
	}
	if !strings.Contains(body, `.secondary-button svg,`) || !strings.Contains(body, `width: 44px;`) {
		t.Fatalf("expected secondary icon button styling for disconnect, body=%s", body)
	}
	if !strings.Contains(body, `.manual-dispatch-skill-select-wrap {`) {
		t.Fatalf("expected manual dispatch skill-select wrapper styles, body=%s", body)
	}
	if !strings.Contains(body, `.manual-dispatch-skill-select-wrap[data-has-multiple-skills="true"] .manual-dispatch-skill-select-icon {`) {
		t.Fatalf("expected manual dispatch skill-select icon visibility toggle styles, body=%s", body)
	}
	if !strings.Contains(body, `.dispatch-workbench-panel::before {`) || !strings.Contains(body, `display: none;`) {
		t.Fatalf("expected dispatch workbench panel to disable extra chrome so it matches the studio shell, body=%s", body)
	}
	if !strings.Contains(body, `#dispatch-submit-status:empty {`) {
		t.Fatalf("expected empty dispatch submit status override for flush footer alignment, body=%s", body)
	}
	if !strings.Contains(body, `visibility: hidden;`) {
		t.Fatalf("expected empty dispatch submit status to preserve row height while hiding copy, body=%s", body)
	}
	if !strings.Contains(body, `gap: 10px;`) {
		t.Fatalf("expected spacing between clear and dispatch actions, body=%s", body)
	}
	if !strings.Contains(body, `.shell button.dispatch-task-button {`) {
		t.Fatalf("expected dispatch task button style override for action button look, body=%s", body)
	}
	if !strings.Contains(body, `.runtime-event-card-header {`) {
		t.Fatalf("expected runtime event card header layout styles, body=%s", body)
	}
	if !strings.Contains(body, `.activity-feed-lane-track {`) {
		t.Fatalf("expected activity feed lane track styles, body=%s", body)
	}
	if !strings.Contains(body, `grid-auto-flow: column;`) {
		t.Fatalf("expected activity feed lanes to stack cards horizontally, body=%s", body)
	}
	if !strings.Contains(body, `align-items: start;`) {
		t.Fatalf("expected horizontal activity lanes to avoid stretching collapsed cards, body=%s", body)
	}
	if !strings.Contains(body, `overflow-x: auto;`) {
		t.Fatalf("expected horizontal activity lanes to support scrolling, body=%s", body)
	}
	if !strings.Contains(body, `.runtime-event-card-toggle {`) {
		t.Fatalf("expected runtime event toggle button styles, body=%s", body)
	}
	if !strings.Contains(body, `.runtime-event-card-body[hidden] {`) {
		t.Fatalf("expected runtime event hidden state styles, body=%s", body)
	}
	if !strings.Contains(body, `.runtime-event-detail-row {`) || !strings.Contains(body, `.runtime-event-detail-icon {`) {
		t.Fatalf("expected runtime event detail icon row styles, body=%s", body)
	}
	if !strings.Contains(body, `.brand-login-card-shell {`) {
		t.Fatalf("expected user-portal glass card shell styles, body=%s", body)
	}
	if !strings.Contains(body, `.brand-btn-primary-main {`) {
		t.Fatalf("expected user-portal primary button styles, body=%s", body)
	}
	if strings.Contains(body, `.dispatch-overview`) {
		t.Fatalf("did not expect removed dispatch overview styles, body=%s", body)
	}
	if strings.Contains(body, `.snowfall-canvas`) || strings.Contains(body, `moltenFloat`) {
		t.Fatalf("did not expect animated background styles, body=%s", body)
	}
	if !strings.Contains(body, `display: none !important;`) {
		t.Fatalf("expected explicit hidden display override, body=%s", body)
	}
}

func TestHandleStaticServesEmbeddedAssets(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/static/styles.css", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("expected css content type header, got %q", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(body, ".shell {") {
		t.Fatalf("expected stylesheet body from embedded static assets, body=%s", body)
	}
}

func TestHandleStylesUsesNeutralDefaultForSettingsDockButton(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/styles.css", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}
	if !strings.Contains(body, ".hub-profile-button {\n  opacity: 1;\n  pointer-events: auto;") {
		t.Fatalf("expected settings dock button to use neutral default color token, body=%s", body)
	}
	if !strings.Contains(body, "color: var(--muted-foreground);") {
		t.Fatalf("expected settings dock button to use neutral default color token, body=%s", body)
	}
	if !strings.Contains(body, ".hub-profile-button {\n  opacity: 1;\n  pointer-events: auto;\n  min-height: 40px;\n  padding: 0;\n  border: 0;\n  background: transparent;\n  box-shadow: none;") {
		t.Fatalf("expected settings dock button to clear shared pill button chrome, body=%s", body)
	}
	if !strings.Contains(body, "--hub-content-bottom-padding: calc(var(--hub-floating-bottom) + var(--hub-floating-stack-height) + var(--hub-studio-dock-gap) + 28px);") {
		t.Fatalf("expected shared dock spacing token from moltenhub-code stylesheet, body=%s", body)
	}
	if !strings.Contains(body, ".page-bottom-dock {\n    inset-inline: 0;\n    bottom: 0;\n    left: 0;\n    width: 100%;") {
		t.Fatalf("expected mobile bottom dock to snap flush to the viewport bottom, body=%s", body)
	}
	if !strings.Contains(body, ".prompt-mode-tabs-dock {\n    width: 100%;\n    max-width: none;\n    justify-content: space-around;") {
		t.Fatalf("expected mobile bottom dock nav to stretch across the viewport, body=%s", body)
	}
	if !strings.Contains(body, ".prompt-mode-mobile-label {\n    position: relative;\n    z-index: 1;\n    display: block;") {
		t.Fatalf("expected mobile bottom dock labels to render under icons, body=%s", body)
	}
	if !strings.Contains(body, `/* Selectable pink theme. */`) || !strings.Contains(body, `html.pink {`) {
		t.Fatalf("expected pink palette to be available as its own selectable theme, body=%s", body)
	}
	if !strings.Contains(body, `--good: #2bb673;`) ||
		!strings.Contains(body, `--primary: #ec4899;`) ||
		!strings.Contains(body, `--accent: #db2777;`) ||
		!strings.Contains(body, `--glass-icon-bg: rgba(255, 255, 255, 0.7);`) {
		t.Fatalf("expected pink theme to define connected-agent accent colors, body=%s", body)
	}
	if !strings.Contains(body, `html.dark {`) || !strings.Contains(body, `--body-linear: linear-gradient(180deg, #0d1424, #0a1120 58%, #09101d);`) {
		t.Fatalf("expected existing dark theme to remain available separately from pink theme, body=%s", body)
	}
	if !strings.Contains(body, ".badge.completed {\n  background: var(--good);\n}") {
		t.Fatalf("expected shared completed badge compatibility selector, body=%s", body)
	}
	if !strings.Contains(body, ".task-result.completed {\n  color: var(--surface-success);\n  background: rgba(43, 182, 115, 0.1);\n}") {
		t.Fatalf("expected shared completed task result compatibility selector, body=%s", body)
	}
}

func TestHandleStylesKeepsMoltenHubLogoUnfiltered(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/styles.css", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}
	if !strings.Contains(body, "#moltenbot-hub-link img {\n  filter: none;") {
		t.Fatalf("expected bundled molten hub logo to avoid theme filters, body=%s", body)
	}
}

func TestBundledLogoUsesOriginalFill(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("static/logo.svg")
	if err != nil {
		t.Fatalf("read logo.svg: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, `fill="#0091b0"`) || !strings.Contains(content, `data-originalfillcolor="#7b61ff"`) {
		t.Fatalf("expected bundled logo to keep original colors, content=%s", content)
	}
}

func TestHandleIndexHidesSubActionsUntilBoundAndConnected(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusDisconnected,
				Transport: app.ConnectionTransportOffline,
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `id="onboarding-modal-backdrop"`) {
		t.Fatalf("expected onboarding modal while runtime is unbound, body=%s", body)
	}
	if strings.Contains(body, `<h2 data-sub-actions-title>`) || strings.Contains(body, `id="sub-actions-notice"`) || strings.Contains(body, `<div id="sub-actions"`) {
		t.Fatalf("did not expect sub-actions surfaces before onboarding completes, body=%s", body)
	}
	if strings.Contains(body, ">3. Connected Agents<") {
		t.Fatalf("did not expect removed connected agents section, body=%s", body)
	}
	if strings.Contains(body, ">Dispatch<") || strings.Contains(body, `id="manual-dispatch-form"`) {
		t.Fatalf("did not expect manual dispatch markup before onboarding completes, body=%s", body)
	}
	if strings.Contains(body, "Save Global Settings") {
		t.Fatalf("did not expect save button for global settings, body=%s", body)
	}
}

func TestHandleIndexShowsOnboardingErrorWhenTokenExistsButHubStatusIsOffline(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusDisconnected,
				Transport: app.ConnectionTransportOffline,
				Error:     "publish failed",
			},
			Session: app.Session{
				AgentToken: "agent-token",
			},
			ConnectedAgents: []app.ConnectedAgent{
				testConnectedAgent("worker-a", "Worker A", "worker-uuid", "molten://agent/worker-a", app.Skill{Name: "run_task"}),
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `id="onboarding-modal-backdrop"`) || !strings.Contains(body, `aria-hidden="false"`) {
		t.Fatalf("expected forced onboarding modal while hub is offline, body=%s", body)
	}
	if !strings.Contains(body, `inert aria-hidden="true"`) {
		t.Fatalf("expected app shell to be inert while hub is offline, body=%s", body)
	}
	if !strings.Contains(body, `class="onboarding-message is-error"`) || !strings.Contains(body, `publish failed`) {
		t.Fatalf("expected hub connection error in onboarding status, body=%s", body)
	}
	if !strings.Contains(body, `action="/profile"`) || !strings.Contains(body, `id="agent-disconnect-submit"`) {
		t.Fatalf("expected bound profile form with disconnect option while token is present, body=%s", body)
	}
	if strings.Contains(body, `id="agent-profile-modal-close"`) {
		t.Fatalf("did not expect close control while forced onboarding is blocking the app, body=%s", body)
	}
}

func TestHandleIndexShowsFlashErrorInsideOnboardingWhenStartupValidationFails(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusDisconnected,
				Transport: app.ConnectionTransportOffline,
			},
			Flash: app.FlashMessage{
				Level:   "error",
				Message: "automatic hub binding from MOLTEN_HUB_TOKEN failed: missing or invalid bearer token",
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `class="onboarding-message is-error"`) {
		t.Fatalf("expected onboarding error styling, body=%s", body)
	}
	if !strings.Contains(body, `automatic hub binding from MOLTEN_HUB_TOKEN failed: missing or invalid bearer token`) {
		t.Fatalf("expected startup validation failure in onboarding message, body=%s", body)
	}
}

func TestHandleIndexTreatsHTTPPollingAsUsableConnection(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusDisconnected,
				Transport: app.ConnectionTransportHTTP,
				BaseURL:   "https://na.hub.molten.bot/v1",
				Domain:    "na.hub.molten.bot",
			},
			Session: app.Session{
				AgentToken:      "agent-token",
				Handle:          "dispatch-agent",
				HandleFinalized: true,
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, `inert aria-hidden="true"`) {
		t.Fatalf("did not expect app shell to be blocked for HTTP polling, body=%s", body)
	}
	if !strings.Contains(body, `id="onboarding-modal-backdrop"`) || !strings.Contains(body, `aria-hidden="true"`) || !strings.Contains(body, `hidden`) {
		t.Fatalf("expected onboarding/profile modal to stay hidden while HTTP polling is usable, body=%s", body)
	}
	if !strings.Contains(body, `data-transport="http"`) || !strings.Contains(body, `class="dot http"`) {
		t.Fatalf("expected HTTP polling to render as non-red connection state, body=%s", body)
	}
	if !strings.Contains(body, `Connect agents in Molten Bot Hub`) {
		t.Fatalf("expected connected-agent guidance instead of connection issue, body=%s", body)
	}
	if strings.Contains(body, `class="onboarding-message is-error"`) {
		t.Fatalf("did not expect connection issue copy while HTTP polling is usable, body=%s", body)
	}
}

func TestHandleIndexUsesBioPlaceholderWithoutPrefilledDefaultText(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusDisconnected,
				Transport: app.ConnectionTransportOffline,
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `placeholder="Please write the bio of your agent..."`) {
		t.Fatalf("expected bio placeholder hint text, body=%s", body)
	}
	if strings.Contains(body, "Dispatches skill requests to connected agents and proxies results back to you.") {
		t.Fatalf("did not expect default bio sentence to be prefilled, body=%s", body)
	}
}

func TestHandleIndexRendersInteractiveEmojiPicker(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `data-hub-emoji-picker`) {
		t.Fatalf("expected interactive emoji picker root, body=%s", body)
	}
	if !strings.Contains(body, `data-hub-emoji-toggle`) {
		t.Fatalf("expected interactive emoji picker toggle, body=%s", body)
	}
	if !strings.Contains(body, `aria-label="Choose emoji"`) {
		t.Fatalf("expected emoji picker toggle to be an icon button, body=%s", body)
	}
	if strings.Contains(body, `hub-emoji-picker-toggle-text`) || strings.Contains(body, `hub-emoji-picker-toggle-caret`) || strings.Contains(body, `data-hub-emoji-selected-text`) {
		t.Fatalf("did not expect text or caret inside emoji picker toggle, body=%s", body)
	}
	if !strings.Contains(body, `data-hub-emoji-panel`) {
		t.Fatalf("expected interactive emoji picker panel, body=%s", body)
	}
	if !strings.Contains(body, `data-hub-emoji-mart-root`) {
		t.Fatalf("expected emoji mart mount root, body=%s", body)
	}
	if strings.Contains(body, `type="radio" name="emoji"`) {
		t.Fatalf("did not expect legacy radio emoji options, body=%s", body)
	}
	if !strings.Contains(body, `const initHubEmojiPicker = (root) => {`) {
		t.Fatalf("expected emoji picker client module to be embedded, body=%s", body)
	}
	if !strings.Contains(body, `document.body.appendChild(panel);`) {
		t.Fatalf("expected emoji picker panel to portal to document body, body=%s", body)
	}
	if !strings.Contains(body, `const PROFILE_EMOJI_GROUPS = [`) || !strings.Contains(body, `className = "hub-emoji-picker-category"`) || !strings.Contains(body, `className = "hub-emoji-picker-grid"`) {
		t.Fatalf("expected built-in categorized emoji picker grid, body=%s", body)
	}
	if strings.Contains(body, `https://esm.sh/@emoji-mart`) || strings.Contains(body, `Loading emoji picker...`) {
		t.Fatalf("did not expect remote emoji picker loader, body=%s", body)
	}
}

func TestDefaultProfileFormChoosesDefaultEmoji(t *testing.T) {
	t.Parallel()

	form := defaultProfileForm(app.AppState{Settings: app.DefaultSettings()}, agentProfileForm{})
	if form.Emoji == "" {
		t.Fatalf("expected default emoji")
	}
	found := false
	for _, emoji := range defaultProfileEmojis {
		if form.Emoji == emoji {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("default emoji %q is not in default set %#v", form.Emoji, defaultProfileEmojis)
	}
}

func TestEmojiPickerPanelStacksAboveModalBackdrop(t *testing.T) {
	t.Parallel()

	styles, err := os.ReadFile("static/styles.css")
	if err != nil {
		t.Fatalf("read styles.css: %v", err)
	}

	content := string(styles)
	if !strings.Contains(content, ".hub-emoji-picker-panel {\n  position: fixed;\n  z-index: 130;") {
		t.Fatalf("expected emoji picker panel to sit above modal backdrops")
	}
	if !strings.Contains(content, ".settings-modal-backdrop,\n.onboarding-modal-backdrop {\n  position: fixed;\n  inset: 0;\n  z-index: 121;") {
		t.Fatalf("expected modal backdrop z-index baseline")
	}
}

func TestOnboardingStylesForceHiddenProfileFields(t *testing.T) {
	t.Parallel()

	styles, err := os.ReadFile("static/styles.css")
	if err != nil {
		t.Fatalf("read styles.css: %v", err)
	}

	content := string(styles)
	if !strings.Contains(content, "#onboarding-profile-fields[hidden] {\n  display: none !important;\n}") {
		t.Fatalf("expected onboarding profile fields hidden override to defeat .stack display grid")
	}
}

func TestHandleIndexRendersInteractiveOnboardingFlowForUnboundSession(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusDisconnected,
				Transport: app.ConnectionTransportOffline,
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `id="bind-form"`) {
		t.Fatalf("expected bind form id for onboarding API flow, body=%s", body)
	}
	if !strings.Contains(body, `id="onboarding-modal-backdrop"`) {
		t.Fatalf("expected onboarding modal for unbound session, body=%s", body)
	}
	if !strings.Contains(body, `data-bound="false"`) || !strings.Contains(body, `inert aria-hidden="true"`) {
		t.Fatalf("expected unbound app shell to be inert behind onboarding modal, body=%s", body)
	}
	if strings.Contains(body, `id="onboarding-existing-agent-toggle"`) || strings.Contains(body, `id="onboarding-new-agent-toggle"`) {
		t.Fatalf("expected onboarding modal to remove existing/new agent mode toggles, body=%s", body)
	}
	if !strings.Contains(body, `name="hub_region"`) {
		t.Fatalf("expected runtime region selector in onboarding modal, body=%s", body)
	}
	if !strings.Contains(body, `<legend>Region</legend>`) {
		t.Fatalf("expected onboarding region legend to use concise label, body=%s", body)
	}
	if !strings.Contains(body, `<strong>North America</strong>`) || !strings.Contains(body, `<strong>Europe</strong>`) {
		t.Fatalf("expected region selector to use full region names, body=%s", body)
	}
	if strings.Contains(body, `<strong>NA</strong>`) || strings.Contains(body, `<strong>EU</strong>`) {
		t.Fatalf("did not expect region selector shorthand labels, body=%s", body)
	}
	if !strings.Contains(body, `id="onboarding-profile-fields" class="stack onboarding-profile-fields" hidden`) {
		t.Fatalf("expected onboarding profile fields to stay hidden in existing-agent mode, body=%s", body)
	}
	if strings.Contains(body, `1. Onboarding`) || strings.Contains(body, `Connect this dispatcher to Molten Hub`) {
		t.Fatalf("did not expect duplicate main-page onboarding description behind modal, body=%s", body)
	}
	if strings.Contains(body, `onboarding-quick-start`) || strings.Contains(body, `1. Open Hub`) || strings.Contains(body, `2. Paste token`) || strings.Contains(body, `3. Connect`) {
		t.Fatalf("did not expect quick-start pills in onboarding modal, body=%s", body)
	}
	if strings.Contains(body, `id="sub-actions-notice"`) || strings.Contains(body, `id="sub-actions"`) || strings.Contains(body, `id="manual-dispatch-form"`) {
		t.Fatalf("did not expect dispatch surfaces while unbound onboarding modal is active, body=%s", body)
	}
	if strings.Contains(body, `class="page-bottom-dock"`) {
		t.Fatalf("did not expect bottom dock while unbound onboarding modal is active, body=%s", body)
	}
	if strings.Contains(body, `id="onboarding-steps"`) || strings.Contains(body, "Connection Check") {
		t.Fatalf("did not expect visible onboarding connection-check steps, body=%s", body)
	}
	if !strings.Contains(body, `id="onboarding-message"`) {
		t.Fatalf("expected onboarding message container, body=%s", body)
	}
	if !strings.Contains(body, `<div class="onboarding-form-actions">`+"\n"+`            <p class="onboarding-message" id="onboarding-message" aria-live="polite">`) {
		t.Fatalf("expected onboarding status message to sit beside the submit button, body=%s", body)
	}
	if !strings.Contains(body, `const formatOnboardingMessage = (message) => {`) || !strings.Contains(body, `console.error("Onboarding failed:", rawMessage);`) {
		t.Fatalf("expected onboarding errors to be formatted for users and logged raw, body=%s", body)
	}
	if strings.Contains(body, `onboarding-step onboarding-step-current" data-step-id="bind"`) || strings.Contains(body, "Check the agent token.") || strings.Contains(body, "Register this runtime with Molten Hub.") {
		t.Fatalf("did not expect onboarding step details in modal, body=%s", body)
	}
	if !strings.Contains(body, `id="onboarding-mode-field" type="hidden" name="agent_mode" value="existing"`) {
		t.Fatalf("expected onboarding mode field in onboarding modal, body=%s", body)
	}
	if !strings.Contains(body, `id="onboarding-token-label"`) {
		t.Fatalf("expected redesigned onboarding form fields, body=%s", body)
	}
	if !strings.Contains(body, `id="onboarding-token-input" type="password"`) {
		t.Fatalf("expected onboarding token input to render as secret field, body=%s", body)
	}
	if !strings.Contains(body, `const onboardingModeFromToken = (token) => String(token || "").trim().toLowerCase().startsWith("b_") ? "new" : "existing";`) {
		t.Fatalf("expected onboarding token prefix to drive profile form visibility, body=%s", body)
	}
	if !strings.Contains(body, "Molten Hub Dispatch") || !strings.Contains(body, "Connect to Hub") {
		t.Fatalf("expected onboarding modal to match app title language, body=%s", body)
	}
	if !strings.Contains(body, "Get your agent token from Molten Hub.") {
		t.Fatalf("expected onboarding summary to describe existing-agent token flow, body=%s", body)
	}
	if !strings.Contains(body, `<span>Agent Token</span>`) || !strings.Contains(body, `data-lucide="external-link"`) {
		t.Fatalf("expected onboarding Hub link to point users to their agent token, body=%s", body)
	}
	if strings.Contains(body, "Open Molten Hub sign-in") || strings.Contains(body, "Use an existing agent token to put this runtime online.") || strings.Contains(body, "Connect this dispatcher") {
		t.Fatalf("did not expect old onboarding modal copy, body=%s", body)
	}
	if !strings.Contains(body, "Use the agent token from Molten Hub.") {
		t.Fatalf("expected onboarding token hint for existing-agent mode, body=%s", body)
	}
}

func TestHandleIndexRendersCompletedOnboardingFlowForBoundSession(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusConnected,
				Transport: app.ConnectionTransportHTTP,
			},
			Session: app.Session{
				AgentToken:      "agent-token",
				Handle:          "dispatch-agent",
				HandleFinalized: true,
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `id="onboarding-modal-backdrop"`) || !strings.Contains(body, `aria-hidden="true"`) {
		t.Fatalf("expected hidden shared profile modal once already bound, body=%s", body)
	}
	if strings.Contains(body, `id="onboarding-steps"`) {
		t.Fatalf("did not expect onboarding steps section once already bound, body=%s", body)
	}
	if strings.Contains(body, `name="hub_region"`) {
		t.Fatalf("did not expect runtime selector once already bound, body=%s", body)
	}
	if strings.Contains(body, `name="bind_token"`) {
		t.Fatalf("did not expect bind token field once already bound, body=%s", body)
	}
	if !strings.Contains(body, "Agent Profile") {
		t.Fatalf("expected profile editor once already bound, body=%s", body)
	}
	if !strings.Contains(body, `id="bind-form"`) || !strings.Contains(body, `data-bound="true"`) || !strings.Contains(body, `id="agent-disconnect-submit" formaction="/disconnect"`) {
		t.Fatalf("expected bound profile editor to use shared form with disconnect, body=%s", body)
	}
	if strings.Contains(body, "The bind token is removed only after the agent is successfully bound. The finalized handle stays visible but immutable here.") {
		t.Fatalf("did not expect removed finalized-handle hint, body=%s", body)
	}
}

func TestHandleIndexAllowsFinalizingTemporaryHandle(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Session: app.Session{
				AgentToken:  "agent-token",
				Handle:      "tmp-agent-123",
				DisplayName: "Dispatch Agent",
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `placeholder="happy-molten-bot"`) {
		t.Fatalf("expected friendly default handle example, body=%s", body)
	}
	if strings.Contains(body, `placeholder="codex-beast"`) {
		t.Fatalf("did not expect old handle placeholder, body=%s", body)
	}
	if strings.Contains(body, `id="agent-settings-handle" name="handle" value="tmp-agent-123" placeholder="happy-molten-bot" readonly`) {
		t.Fatalf("expected temporary handle to remain editable, body=%s", body)
	}
	if strings.Contains(body, "This bind used a temporary handle") || strings.Contains(body, "temporary handle") {
		t.Fatalf("did not expect temporary-handle helper copy, body=%s", body)
	}
}

func TestHandleBindShowsEditProfileAfterSessionBecomesBound(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusDisconnected,
				Transport: app.ConnectionTransportOffline,
			},
		},
		bindErr:          errors.New("agent bound, but profile registration failed"),
		bindStateOnError: true,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/bind", strings.NewReader("bind_token=bind-123&handle=codex-beast&display_name=Dispatch+Agent&emoji=%F0%9F%92%AF&profile_markdown=What+this+runtime+is+for"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect response, got %d", rec.Code)
	}

	location := rec.Header().Get("Location")
	if location != "/" {
		t.Fatalf("expected root redirect location, got %q", location)
	}

	followReq := httptest.NewRequest(http.MethodGet, location, nil)
	followRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(followRec, followReq)
	body := followRec.Body.String()

	if followRec.Code != http.StatusOK {
		t.Fatalf("expected successful GET after redirect, got %d", followRec.Code)
	}
	if strings.Contains(body, `name="bind_token"`) {
		t.Fatalf("did not expect bind token field after session became bound, body=%s", body)
	}
	if !strings.Contains(body, "Connect to Hub") || !strings.Contains(body, `class="onboarding-message is-error"`) {
		t.Fatalf("expected forced connection error after bound session is still offline, body=%s", body)
	}
	if !strings.Contains(body, `id="bind-form"`) || !strings.Contains(body, `data-bound="true"`) || !strings.Contains(body, `action="/profile"`) {
		t.Fatalf("expected bound profile form after session became bound, body=%s", body)
	}
	if !strings.Contains(body, "agent bound, but profile registration failed") {
		t.Fatalf("expected surfaced bind error, body=%s", body)
	}
}

func TestHandleProfileRedirectsOnFailure(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Session: app.Session{
				AgentToken:      "agent-token",
				Handle:          "dispatch-agent",
				HandleFinalized: true,
			},
		},
		updateProfileErr: errors.New("profile update failed"),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/profile", strings.NewReader("handle=dispatch-agent&display_name=Dispatch+Agent"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect response, got %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Fatalf("expected root redirect location, got %q", got)
	}
}

func TestHandleDisconnectReturnsToTokenOnboarding(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Session: app.Session{
				AgentToken:      "agent-token",
				Handle:          "dispatch-agent",
				HandleFinalized: true,
				DisplayName:     "Dispatch Agent",
			},
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusConnected,
				Transport: app.ConnectionTransportHTTP,
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/disconnect", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect response, got %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Fatalf("expected root redirect location, got %q", got)
	}

	followReq := httptest.NewRequest(http.MethodGet, "/", nil)
	followRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(followRec, followReq)
	body := followRec.Body.String()

	if !strings.Contains(body, `data-bound="false"`) || !strings.Contains(body, `inert aria-hidden="true"`) {
		t.Fatalf("expected disconnected app shell to return to forced onboarding, body=%s", body)
	}
	if !strings.Contains(body, `name="hub_region"`) || !strings.Contains(body, `id="onboarding-token-input" type="password"`) {
		t.Fatalf("expected region and token fields after disconnect, body=%s", body)
	}
	if strings.Contains(body, `id="agent-disconnect-submit"`) || strings.Contains(body, `action="/profile"`) {
		t.Fatalf("did not expect bound profile actions after disconnect, body=%s", body)
	}
}

func TestHandleStatusReturnsConnectionView(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusConnected,
				Transport: app.ConnectionTransportHTTP,
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}

	var view struct {
		Status       string `json:"status"`
		Transport    string `json:"transport"`
		Label        string `json:"label"`
		HubConnected bool   `json:"hub_connected"`
		HubTransport string `json:"hub_transport"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if view.Status != app.ConnectionStatusConnected {
		t.Fatalf("unexpected status: %#v", view)
	}
	if view.Transport != app.ConnectionTransportHTTP {
		t.Fatalf("unexpected transport: %#v", view)
	}
	if view.Label != "HTTP Connected" {
		t.Fatalf("unexpected label: %#v", view)
	}
	if !view.HubConnected {
		t.Fatalf("expected hub_connected=true, got %#v", view)
	}
	if view.HubTransport != app.ConnectionTransportHTTP {
		t.Fatalf("unexpected hub transport: %#v", view)
	}
}

func TestHandleStatusIncludesPendingTasksAndRecentEvents(t *testing.T) {
	t.Parallel()

	eventTime := time.Unix(1735689600, 0).UTC()
	server, err := New(&stubService{
		state: app.AppState{
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusConnected,
				Transport: app.ConnectionTransportHTTP,
			},
			PendingTasks: []app.PendingTask{
				{
					ID:                "task-1",
					ChildRequestID:    "child-1",
					OriginalSkillName: "code_for_me",
				},
			},
			RecentEvents: []app.RuntimeEvent{
				{
					At:     eventTime,
					Level:  "info",
					Title:  "Task dispatched",
					Detail: "Queued code_for_me for worker-1",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}

	var view struct {
		Status       string             `json:"status"`
		PendingTasks []app.PendingTask  `json:"pending_tasks"`
		RecentEvents []app.RuntimeEvent `json:"recent_events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if view.Status != app.ConnectionStatusConnected {
		t.Fatalf("unexpected status payload: %#v", view)
	}
	if len(view.PendingTasks) != 1 || view.PendingTasks[0].ID != "task-1" {
		t.Fatalf("unexpected pending_tasks payload: %#v", view.PendingTasks)
	}
	if len(view.RecentEvents) != 1 || view.RecentEvents[0].Title != "Task dispatched" {
		t.Fatalf("unexpected recent_events payload: %#v", view.RecentEvents)
	}
}

func TestHandleStatusReturnsErrorConnectionView(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusDisconnected,
				Transport: app.ConnectionTransportOffline,
				Error:     "status request failed with 503",
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}

	var view struct {
		Status      string `json:"status"`
		Transport   string `json:"transport"`
		Label       string `json:"label"`
		Description string `json:"description"`
		HubDetail   string `json:"hub_detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if view.Status != app.ConnectionStatusDisconnected {
		t.Fatalf("unexpected status: %#v", view)
	}
	if view.Transport != app.ConnectionTransportOffline {
		t.Fatalf("unexpected transport: %#v", view)
	}
	if view.Label != "Error" {
		t.Fatalf("unexpected label: %#v", view)
	}
	if view.Description != "status request failed with 503" {
		t.Fatalf("unexpected description: %#v", view)
	}
	if view.HubDetail != "status request failed with 503" {
		t.Fatalf("unexpected hub detail: %#v", view)
	}
}

func TestHandleStatusReturnsRetryingConnectionView(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.Settings{HubURL: "https://na.hub.molten.bot"},
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusDisconnected,
				Transport: app.ConnectionTransportRetrying,
				Detail:    "Hub endpoint ping failed; retrying every 12s until live. Error: GET https://na.hub.molten.bot/ping returned status=503",
				BaseURL:   "https://na.hub.molten.bot/v1",
				Domain:    "na.hub.molten.bot",
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}

	var view struct {
		Label       string `json:"label"`
		Description string `json:"description"`
		HubDetail   string `json:"hub_detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if view.Label != "Retrying" {
		t.Fatalf("unexpected label: %#v", view)
	}
	if !strings.Contains(view.Description, "retrying every 12s") {
		t.Fatalf("unexpected description: %#v", view)
	}
	if view.HubDetail == "" {
		t.Fatalf("expected retry detail in status payload: %#v", view)
	}
}

func TestHandleConnectedAgentsReturnsSnapshot(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			ConnectedAgents: []app.ConnectedAgent{
				testConnectedAgent("dispatcher", "Dispatcher", "agent-1", "molten://agent/dispatcher"),
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/connected-agents", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}

	var body struct {
		OK              bool                 `json:"ok"`
		ConnectedAgents []app.ConnectedAgent `json:"connected_agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.OK {
		t.Fatalf("expected ok=true response, got %#v", body)
	}
	if len(body.ConnectedAgents) != 1 {
		t.Fatalf("expected one connected agent, got %#v", body.ConnectedAgents)
	}
	if body.ConnectedAgents[0].Metadata == nil || body.ConnectedAgents[0].Metadata.DisplayName != "Dispatcher" {
		t.Fatalf("unexpected connected agent payload: %#v", body.ConnectedAgents[0])
	}
}

func TestHandleConnectedAgentsReturnsStructuredRefreshError(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			ConnectedAgents: []app.ConnectedAgent{{AgentID: "stale-agent", Handle: "stale-agent"}},
		},
		refreshAgentsErr: errors.New("refresh connected agents from /v1/agents/me/capabilities: hub API 401 unauthorized: missing or invalid bearer token"),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/connected-agents", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 response, got %d", rec.Code)
	}

	var body struct {
		OK              bool                 `json:"ok"`
		Error           string               `json:"error"`
		Detail          string               `json:"detail"`
		ConnectedAgents []app.ConnectedAgent `json:"connected_agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.OK {
		t.Fatalf("expected ok=false response, got %#v", body)
	}
	if body.Error != "connected agents refresh failed" {
		t.Fatalf("unexpected error body: %#v", body)
	}
	if !strings.Contains(body.Detail, "missing or invalid bearer token") {
		t.Fatalf("expected detailed refresh error, got %#v", body)
	}
	if len(body.ConnectedAgents) != 1 || body.ConnectedAgents[0].AgentID != "stale-agent" {
		t.Fatalf("expected stale connected agents to be returned for context, got %#v", body.ConnectedAgents)
	}
}

func TestHandleProfileAPIReturnsSnapshot(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Session: app.Session{
				AgentToken:  "agent-token",
				Handle:      "dispatch-agent",
				DisplayName: "Dispatch Agent",
				Emoji:       "🤖",
				ProfileBio:  "Dispatches skill requests.",
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/profile", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}

	var body struct {
		OK      bool `json:"ok"`
		Profile struct {
			Handle          string `json:"handle"`
			DisplayName     string `json:"display_name"`
			Emoji           string `json:"emoji"`
			ProfileMarkdown string `json:"profile_markdown"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.OK {
		t.Fatalf("expected ok=true response, got %#v", body)
	}
	if body.Profile.Handle != "dispatch-agent" || body.Profile.DisplayName != "Dispatch Agent" {
		t.Fatalf("unexpected profile payload: %#v", body.Profile)
	}
}

func TestHandleProfileAPIReturnsStructuredRefreshError(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Session: app.Session{
				AgentToken:  "agent-token",
				Handle:      "stale-agent",
				DisplayName: "Stale Agent",
			},
		},
		refreshProfileErr: errors.New("refresh agent profile from /v1/agents/me/capabilities: hub API 401 unauthorized: missing or invalid bearer token"),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/profile", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 response, got %d", rec.Code)
	}

	var body struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Detail  string `json:"detail"`
		Profile struct {
			Handle string `json:"handle"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "agent profile refresh failed" {
		t.Fatalf("unexpected error payload: %#v", body)
	}
	if body.Profile.Handle != "stale-agent" {
		t.Fatalf("expected stale profile to be returned, got %#v", body.Profile)
	}
}

func TestHandleIndexRendersConnectedAgentsRefreshPanel(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Session: app.Session{
				AgentToken: "agent-token",
			},
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusConnected,
				Transport: app.ConnectionTransportHTTP,
			},
			ConnectedAgents: []app.ConnectedAgent{
				testConnectedAgent(
					"dispatcher",
					"Dispatcher",
					"dispatcher-uuid",
					"molten://agent/dispatcher",
					app.Skill{Name: "dispatch_skill_request", Description: "Dispatch a task."},
					app.Skill{Name: "run_task", Description: "Run a task."},
				),
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, `id="connected-agents-list"`) {
		t.Fatalf("did not expect the removed connected agents directory panel on the main page, body=%s", body)
	}
	if strings.Contains(body, "Connected Agent Directory") {
		t.Fatalf("did not expect deprecated connected agent directory heading, body=%s", body)
	}
	if strings.Contains(body, `id="connected-agents-refresh"`) {
		t.Fatalf("did not expect main-page connected agents refresh control, body=%s", body)
	}
	if !strings.Contains(body, `class="connected-agent-card connected-agent-card-button is-online"`) {
		t.Fatalf("expected connected agent card to render online styling state, body=%s", body)
	}
	if !strings.Contains(body, ">Dispatcher<") {
		t.Fatalf("expected connected agent display name on connected agent cards, body=%s", body)
	}
	if !strings.Contains(body, `class="connected-agent-presence is-online"`) {
		t.Fatalf("expected online presence indicator on connected agent cards, body=%s", body)
	}
	if !strings.Contains(body, `title="Online"`) {
		t.Fatalf("expected online presence tooltip on connected agent cards, body=%s", body)
	}
	if strings.Contains(body, `class="connected-agent-secondary"`) {
		t.Fatalf("did not expect connected-agent secondary handle/id label on cards, body=%s", body)
	}
	if strings.Contains(body, `class="connected-agent-presence is-offline"`) {
		t.Fatalf("did not expect offline presence indicator on connected agent cards, body=%s", body)
	}
	if strings.Contains(body, `data-lucide="wifi-off"`) {
		t.Fatalf("did not expect Lucide offline connectivity icon on connected agent cards, body=%s", body)
	}
	if strings.Contains(body, `title="Offline"`) {
		t.Fatalf("did not expect offline presence tooltip on connected agent cards, body=%s", body)
	}
	if strings.Contains(body, ">Offline<") {
		t.Fatalf("did not expect textual offline badge label on connected agent cards, body=%s", body)
	}
	if strings.Contains(body, `class="connected-agent-skills"`) {
		t.Fatalf("did not expect connected agent skill list on cards, body=%s", body)
	}
	if strings.Contains(body, `class="connected-agent-skill"`) {
		t.Fatalf("did not expect connected agent skill chips in markup, body=%s", body)
	}
	if !strings.Contains(body, `id="target-agent-ref-input"`) {
		t.Fatalf("expected hidden target agent ref input for card selection UI, body=%s", body)
	}
	if !strings.Contains(body, `id="manual-dispatch-targets"`) {
		t.Fatalf("expected manual dispatch connected agent target list, body=%s", body)
	}
	if strings.Contains(body, "Pick the worker that should receive this task. Dispatch keeps the selection synced when the connected-agent list refreshes.") {
		t.Fatalf("did not expect removed manual dispatch helper copy, body=%s", body)
	}
	if strings.Contains(body, "Choose one of the connected agents below.") {
		t.Fatalf("did not expect removed connected-agent hint copy, body=%s", body)
	}
	if !strings.Contains(body, `class="list connected-agents-list connected-agents-list-selectable manual-dispatch-targets-grid"`) {
		t.Fatalf("expected manual dispatch target grid class for horizontal fill layout, body=%s", body)
	}
	if !strings.Contains(body, `id="skill-name-select" name="skill_name"`) {
		t.Fatalf("expected skill-name dropdown in manual dispatch form, body=%s", body)
	}
	if !strings.Contains(body, `id="skill-name-select-wrap"`) {
		t.Fatalf("expected skill-name dropdown wrapper for conditional chevron icon, body=%s", body)
	}
	if !strings.Contains(body, `class="manual-dispatch-skill-select-icon"`) {
		t.Fatalf("expected skill-name dropdown chevron icon markup, body=%s", body)
	}
	if strings.Contains(body, "Select a target first. Skills load from the chosen agent's Hub capabilities.") {
		t.Fatalf("did not expect removed dispatch helper copy, body=%s", body)
	}
	if strings.Contains(body, `name="skill_name" placeholder="Optional when the target agent has a default skill"`) {
		t.Fatalf("did not expect deprecated freeform skill input, body=%s", body)
	}
	if !strings.Contains(body, `id="skill-name-hint"`) {
		t.Fatalf("expected skill-name hint copy, body=%s", body)
	}
	if !strings.Contains(body, `data-connected-agent-target-ref="dispatcher"`) {
		t.Fatalf("expected connected agent target ref on selectable card, body=%s", body)
	}
	if !strings.Contains(body, `data-connected-agent-refs="dispatcher`) {
		t.Fatalf("expected connected agent reference aliases on selectable card, body=%s", body)
	}
	if !strings.Contains(body, `data-connected-agent-display="Dispatcher"`) {
		t.Fatalf("expected connected agent display name in selectable card, body=%s", body)
	}
	if strings.Contains(body, "PERSONAL · YOU") {
		t.Fatalf("did not expect invalid personal-context badge in connected agent card, body=%s", body)
	}
	if !strings.Contains(body, `const CONNECTED_AGENTS_REFRESH_INTERVAL_MS = 30000;`) {
		t.Fatalf("expected 30s connected agents refresh interval constant, body=%s", body)
	}
	if !strings.Contains(body, `const shouldPollConnectedAgents = () => {`) {
		t.Fatalf("expected connected agents polling guard helper, body=%s", body)
	}
	if !strings.Contains(body, `return bound && connectedAgentsCount === 0;`) {
		t.Fatalf("expected polling guard to stop once agents exist, body=%s", body)
	}
	if !strings.Contains(body, `const scheduleConnectedAgentsAutoRefresh = (delayMs = CONNECTED_AGENTS_REFRESH_INTERVAL_MS) => {`) {
		t.Fatalf("expected connected agents auto-refresh scheduler, body=%s", body)
	}
	if !strings.Contains(body, `Auto-refresh paused while agents are connected.`) {
		t.Fatalf("expected paused auto-refresh copy once agents exist, body=%s", body)
	}
	if !strings.Contains(body, `const syncConnectedAgentSelection = (options = {}) => {`) {
		t.Fatalf("expected connected agent card selection sync helper, body=%s", body)
	}
	if !strings.Contains(body, `const connectedAgentSkillEntries = (agent) => {`) {
		t.Fatalf("expected connected-agent skill extraction helper, body=%s", body)
	}
	if !strings.Contains(body, `const inferSkillNameForAgent = (agent) => {`) {
		t.Fatalf("expected client-side skill inference helper for target agents, body=%s", body)
	}
	if !strings.Contains(body, `const visibleAgentLabel = (value) => {`) {
		t.Fatalf("expected client-side visible label helper for connected agents, body=%s", body)
	}
	if !strings.Contains(body, `const looksLikeUUID = (value) => {`) {
		t.Fatalf("expected UUID suppression helper in connected agent client rendering, body=%s", body)
	}
	if !strings.Contains(body, `const connectedAgentRefs = (agent) => {`) {
		t.Fatalf("expected connected-agent alias helper, body=%s", body)
	}
	if !strings.Contains(body, `const agentMatchesTargetRef = (agent, targetRef) => {`) {
		t.Fatalf("expected connected-agent target matching helper, body=%s", body)
	}
	if !strings.Contains(body, `const resolveSelectedDispatchState = () => {`) {
		t.Fatalf("expected resolved dispatch state helper, body=%s", body)
	}
	if !strings.Contains(body, `const updateSkillNameOptions = (options = {}) => {`) {
		t.Fatalf("expected skill dropdown sync helper, body=%s", body)
	}
	if !strings.Contains(body, `const initialConnectedAgentsData = document.getElementById("initial-connected-agents-data");`) {
		t.Fatalf("expected initial connected-agents bootstrap payload, body=%s", body)
	}
	if !strings.Contains(body, `id="initial-connected-agents-data" type="application/json"`) {
		t.Fatalf("expected serialized connected-agents bootstrap payload script, body=%s", body)
	}
	if !strings.Contains(body, `const selectConnectedAgentTarget = (targetRef, options = {}) => {`) {
		t.Fatalf("expected connected agent selector click handler, body=%s", body)
	}
	if strings.Contains(body, `const buildConnectedAgentSkills = (agent) => {`) {
		t.Fatalf("did not expect removed connected agent skill list renderer in client script, body=%s", body)
	}
	if !strings.Contains(body, `formData.set("target_agent_ref", dispatchState.targetRef);`) {
		t.Fatalf("expected dispatch submit flow to explicitly serialize target agent selection, body=%s", body)
	}
	if !strings.Contains(body, `formData.set("skill_name", dispatchState.skillName);`) {
		t.Fatalf("expected dispatch submit flow to explicitly serialize the resolved skill name, body=%s", body)
	}
	if !strings.Contains(body, `const connectedAgentsRefreshButtons = Array.from(document.querySelectorAll("[data-connected-agents-refresh-button]"));`) {
		t.Fatalf("expected shared manual refresh button hooks, body=%s", body)
	}
	if !strings.Contains(body, `const detail = trimmedString(body && body.detail)`) {
		t.Fatalf("expected connected-agents refresh errors to surface backend detail, body=%s", body)
	}
	if !strings.Contains(body, `renderConnectedAgents(staleAgents);`) {
		t.Fatalf("expected connected-agents refresh failures to preserve stale agent context, body=%s", body)
	}
	if strings.Contains(body, `const connectedAgentsRefreshNextNodes = Array.from(document.querySelectorAll("[data-connected-agents-refresh-next]"));`) {
		t.Fatalf("did not expect removed refresh countdown node hooks, body=%s", body)
	}
	if strings.Contains(body, `connectedAgentsRefreshNextNodes.forEach((node) => {`) {
		t.Fatalf("did not expect stale refresh countdown node usage, body=%s", body)
	}
	if strings.Contains(body, `const setConnectedAgentsRefreshCountdown = (remainingMs, busy) => {`) {
		t.Fatalf("did not expect removed refresh countdown helper, body=%s", body)
	}
	if strings.Contains(body, `setConnectedAgentsRefreshCountdown(`) {
		t.Fatalf("did not expect removed refresh countdown calls, body=%s", body)
	}
	if strings.Contains(body, `connectedAgentsRefreshNextNodes`) {
		t.Fatalf("did not expect removed refresh countdown node references, body=%s", body)
	}
	if strings.Contains(body, `setConnectedAgentsRefreshCountdown(CONNECTED_AGENTS_REFRESH_INTERVAL_MS, false);`) {
		t.Fatalf("did not expect stale refresh countdown helper usage, body=%s", body)
	}
	if !strings.Contains(body, `setConnectedAgentsRefreshState(false, "");`) {
		t.Fatalf("expected refresh completion to clear the status text, body=%s", body)
	}
	if !strings.Contains(body, "void refreshConnectedAgents(\"initial\");\n        if (shouldPollConnectedAgents()) {\n          startConnectedAgentsRefreshTicker();") {
		t.Fatalf("expected bound-session bootstrapping to perform an initial API refresh before deciding whether to continue polling, body=%s", body)
	}
	if strings.Contains(body, "toLocaleTimeString") {
		t.Fatalf("did not expect connected agents refresh timestamp formatting, body=%s", body)
	}
}

func TestHandleIndexHidesUUIDsAndShowsHubAgentMetadata(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Session: app.Session{
				AgentToken: "agent-token",
			},
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusConnected,
				Transport: app.ConnectionTransportHTTP,
			},
			ConnectedAgents: []app.ConnectedAgent{
				{
					AgentID:   "8d9add87-10b1-4ee4-a138-acde48001122",
					AgentUUID: "8d9add87-10b1-4ee4-a138-acde48001122",
					URI:       "molten://agent/hub-worker",
					Metadata: &hub.AgentMetadata{
						DisplayName: "Hub Worker",
						Emoji:       "🧪",
						AdvertisedSkills: []map[string]any{
							{"name": "review_openapi", "description": "Review Hub API integration behavior."},
							{"name": "run_task", "description": "Run a task."},
						},
						Presence: &hub.AgentPresence{Status: "online"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, ">Hub Worker<") {
		t.Fatalf("expected hub display name to render on the agent card, body=%s", body)
	}
	if !strings.Contains(body, "🧪") {
		t.Fatalf("expected hub emoji to render on the agent card, body=%s", body)
	}
	if !strings.Contains(body, `class="connected-agent-presence is-online"`) {
		t.Fatalf("expected hub presence indicator to render online status, body=%s", body)
	}
	if !strings.Contains(body, `title="Online"`) {
		t.Fatalf("expected hub presence indicator tooltip to render online status, body=%s", body)
	}
	if strings.Contains(body, ">Online<") {
		t.Fatalf("did not expect textual online badge label on hub agent card, body=%s", body)
	}
	if strings.Contains(body, ">review_openapi<") || strings.Contains(body, ">run_task<") {
		t.Fatalf("did not expect hub advertised skills to render on the agent card, body=%s", body)
	}
	if strings.Contains(body, ">8d9add87-10b1-4ee4-a138-acde48001122<") {
		t.Fatalf("did not expect UUID values to be rendered as visible card labels, body=%s", body)
	}
}

func TestHandleIndexRendersHubAgentRootPropertiesFromConnectedAgents(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Session: app.Session{
				AgentToken: "agent-token",
			},
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusConnected,
				Transport: app.ConnectionTransportHTTP,
			},
			ConnectedAgents: []app.ConnectedAgent{
				{
					AgentID:     "hub-worker",
					AgentUUID:   "8d9add87-10b1-4ee4-a138-acde48001122",
					URI:         "molten://agent/hub-worker",
					DisplayName: "Hub Worker",
					Emoji:       "🧪",
					Skills: []map[string]any{
						{"name": "review_openapi", "description": "Review Hub API integration behavior."},
						{"name": "review_failure_logs", "description": "Review failing logs."},
					},
					Presence: &hub.AgentPresence{Status: "online"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, ">Hub Worker<") {
		t.Fatalf("expected root hub display name to render on the agent card, body=%s", body)
	}
	if !strings.Contains(body, "🧪") {
		t.Fatalf("expected root hub emoji to render on the agent card, body=%s", body)
	}
	if !strings.Contains(body, `class="connected-agent-presence is-online"`) {
		t.Fatalf("expected root hub presence indicator to render online status, body=%s", body)
	}
	if !strings.Contains(body, `title="Online"`) {
		t.Fatalf("expected root hub presence indicator tooltip to render online status, body=%s", body)
	}
	if strings.Contains(body, ">Online<") {
		t.Fatalf("did not expect textual online badge label on root hub agent card, body=%s", body)
	}
	if strings.Contains(body, ">review_openapi<") || strings.Contains(body, ">review_failure_logs<") {
		t.Fatalf("did not expect root hub skills to render on the agent card, body=%s", body)
	}
	if !strings.Contains(body, `agent && agent.display_name,`) {
		t.Fatalf("expected client-side display-name helper to read root display_name, body=%s", body)
	}
	if !strings.Contains(body, `trimmedString(agent && agent.emoji)`) {
		t.Fatalf("expected client-side emoji helper to read root emoji, body=%s", body)
	}
	if !strings.Contains(body, `connectedAgentPresenceStatusFromPresence(agent && agent.presence)`) {
		t.Fatalf("expected client-side presence helper to read root presence.status, body=%s", body)
	}
}

func TestHandleIndexOmitsConnectedAgentSecondaryLabel(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Session: app.Session{
				AgentToken: "agent-token",
			},
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusConnected,
				Transport: app.ConnectionTransportHTTP,
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, `connectedAgentSecondaryLabel`) {
		t.Fatalf("did not expect secondary-label helper in rendered template, body=%s", body)
	}
	if strings.Contains(body, `class="connected-agent-secondary"`) {
		t.Fatalf("did not expect connected-agent-secondary element in rendered template, body=%s", body)
	}
}

func TestHandleIndexDefinesTrimmedStringBeforeDispatchPlaceholderSetup(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Session: app.Session{
				AgentToken: "agent-token",
			},
			Connection: app.ConnectionState{
				Status:    app.ConnectionStatusConnected,
				Transport: app.ConnectionTransportHTTP,
			},
			ConnectedAgents: []app.ConnectedAgent{
				testConnectedAgent("dispatcher", "Dispatcher", "dispatcher-uuid", "molten://agent/dispatcher", app.Skill{Name: "dispatch_skill_request"}),
			},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	helperIndex := strings.Index(body, `const trimmedString = (value) => String(value || "").trim();`)
	if helperIndex == -1 {
		t.Fatalf("expected trimmedString helper in rendered script, body=%s", body)
	}
	placeholderIndex := strings.Index(body, `const defaultSkillPayloadPlaceholder = skillPayloadInput instanceof HTMLTextAreaElement`)
	if placeholderIndex == -1 {
		t.Fatalf("expected default skill payload placeholder setup in rendered script, body=%s", body)
	}
	if helperIndex > placeholderIndex {
		t.Fatalf("expected trimmedString helper to be defined before placeholder setup to avoid script initialization failures, body=%s", body)
	}
}

func TestHandleIndexRejectsPostMethod(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{state: app.AppState{Settings: app.DefaultSettings()}})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 response, got %d", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet+", "+http.MethodHead {
		t.Fatalf("unexpected Allow header: %q", got)
	}
}

func TestHandleStatusRejectsPostMethod(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{state: app.AppState{Settings: app.DefaultSettings()}})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/status", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 response, got %d", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet+", "+http.MethodHead {
		t.Fatalf("unexpected Allow header: %q", got)
	}
}

func TestHandleConnectedAgentsRejectsPostMethod(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{state: app.AppState{Settings: app.DefaultSettings()}})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/connected-agents", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 response, got %d", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet+", "+http.MethodHead {
		t.Fatalf("unexpected Allow header: %q", got)
	}
}

func TestRenderIndexReturnsSingleInternalServerErrorOnTemplateFailure(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{state: app.AppState{Settings: app.DefaultSettings()}})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	server.templates = template.Must(template.New("index.html").Funcs(template.FuncMap{
		"explode": func() (string, error) {
			return "", errors.New("template exploded")
		},
	}).Parse(`prefix {{explode}}`))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := &headerTrackingResponseWriter{header: make(http.Header)}

	server.renderIndex(rec, req, "", false, agentProfileForm{}, nil)

	if rec.status != http.StatusInternalServerError {
		t.Fatalf("expected 500 response, got %d", rec.status)
	}
	if rec.writeHeaderCalls != 1 {
		t.Fatalf("expected one WriteHeader call, got %d", rec.writeHeaderCalls)
	}
	if strings.Contains(rec.body.String(), "prefix") {
		t.Fatalf("did not expect partial template output, body=%q", rec.body.String())
	}
	if !strings.Contains(rec.body.String(), "template exploded") {
		t.Fatalf("expected template error body, got %q", rec.body.String())
	}
}

type headerTrackingResponseWriter struct {
	header           http.Header
	body             strings.Builder
	status           int
	writeHeaderCalls int
}

func (w *headerTrackingResponseWriter) Header() http.Header {
	return w.header
}

func (w *headerTrackingResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(data)
}

func (w *headerTrackingResponseWriter) WriteHeader(statusCode int) {
	w.writeHeaderCalls++
	if w.status == 0 {
		w.status = statusCode
	}
}
