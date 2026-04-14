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
	updateSettingsErr error
	setFlashErr       error
	refreshAgentsErr  error
	dispatchErr       error
	dispatchTask      app.PendingTask
	lastDispatchReq   app.DispatchRequest
	bindStateOnError  bool
	lastBindProfile   app.BindProfile
	lastFlashLevel    string
	lastFlashMessage  string
}

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
	agent := app.ConnectedAgent{
		AgentID:   agentID,
		Handle:    agentID,
		AgentUUID: agentUUID,
		URI:       uri,
		Metadata: &hub.AgentMetadata{
			DisplayName: displayName,
			Skills:      skillMetadata(skills...),
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

	server, err := New(&stubService{state: app.AppState{Settings: app.DefaultSettings()}})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/onboarding", strings.NewReader(`{"bind_token":"bind-123","handle":"dispatch-agent"}`))
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
	if body.Message != "Agent bound and profile registered." {
		t.Fatalf("unexpected message: %#v", body)
	}
	if len(body.Onboarding.Steps) != 4 {
		t.Fatalf("expected four onboarding steps, got %#v", body.Onboarding.Steps)
	}
	if got, want := body.Onboarding.Steps[0].Detail, "Exchange the bind token for an agent credential."; got != want {
		t.Fatalf("bind step detail = %q, want %q", got, want)
	}
}

func TestHandleOnboardingAPISupportsExistingAgentFlow(t *testing.T) {
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
	if stub.lastFlashMessage != "Existing agent connected and profile registered." {
		t.Fatalf("unexpected success flash: %q", stub.lastFlashMessage)
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
	if !strings.Contains(body, "Edit Agent Profile") {
		t.Fatalf("expected bound profile panel, body=%s", body)
	}
	if !strings.Contains(body, `class="panel dispatch-overview brand-login-card-shell"`) {
		t.Fatalf("expected bound dispatch overview panel, body=%s", body)
	}
	if !strings.Contains(body, `id="dispatch-overview"`) {
		t.Fatalf("expected dispatch overview id for dismiss-once behavior, body=%s", body)
	}
	if !strings.Contains(body, `data-auto-dismiss-seconds="30"`) {
		t.Fatalf("expected dispatch overview to advertise 30s auto-dismiss, body=%s", body)
	}
	if !strings.Contains(body, "Queue the right task with fewer clicks.") {
		t.Fatalf("expected practical dispatch overview heading, body=%s", body)
	}
	if !strings.Contains(body, `id="dispatch-overview-close"`) {
		t.Fatalf("expected dismiss button for dispatch overview, body=%s", body)
	}
	if !strings.Contains(body, ">Connected Agents<") || !strings.Contains(body, ">Queued Follow-Ups<") || !strings.Contains(body, ">Recent Events<") {
		t.Fatalf("expected overview stat labels for dispatch state, body=%s", body)
	}
	if strings.Contains(body, `name="bind_token"`) {
		t.Fatalf("did not expect bind token field after bind, body=%s", body)
	}
	if !strings.Contains(body, `name="handle" value="codex-beast" readonly`) {
		t.Fatalf("expected readonly handle field, body=%s", body)
	}
	if !strings.Contains(body, `name="display_name" value="Dispatch Agent"`) {
		t.Fatalf("expected display name field, body=%s", body)
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
	if strings.Contains(body, `id="bind-form"`) {
		t.Fatalf("did not expect onboarding bind form once bound, body=%s", body)
	}
	if strings.Contains(body, `name="hub_region"`) {
		t.Fatalf("did not expect runtime selector once bound, body=%s", body)
	}
	if strings.Contains(body, `id="onboarding-modal-backdrop"`) {
		t.Fatalf("did not expect onboarding modal once bound, body=%s", body)
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
	if strings.Contains(body, ">Manual Dispatch<") {
		t.Fatalf("did not expect previous manual dispatch heading, body=%s", body)
	}
	if !strings.Contains(body, "<legend>Agents</legend>") {
		t.Fatalf("expected connected agent legend copy, body=%s", body)
	}
	if !strings.Contains(body, "Skills") {
		t.Fatalf("expected skills field label, body=%s", body)
	}
	if !strings.Contains(body, "<span>Payload</span>") {
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
	if !strings.Contains(body, `class="manual-dispatch-actions"`) {
		t.Fatalf("expected manual dispatch submit action wrapper, body=%s", body)
	}
	if !strings.Contains(body, `id="dispatch-task-clear"`) {
		t.Fatalf("expected manual dispatch clear button beside submit, body=%s", body)
	}
	if strings.Contains(body, `name="repo"`) {
		t.Fatalf("did not expect manual dispatch repo field, body=%s", body)
	}
	if strings.Contains(body, `name="log_paths"`) {
		t.Fatalf("did not expect manual dispatch log paths field, body=%s", body)
	}
	if !strings.Contains(body, `id="skill-payload-field" hidden`) {
		t.Fatalf("expected hidden manual dispatch payload field, body=%s", body)
	}
	if !strings.Contains(body, `id="skill-payload-input" name="payload"`) {
		t.Fatalf("expected manual dispatch payload textarea, body=%s", body)
	}
	if !strings.Contains(body, `placeholder="Issue an offline to moltenbot hub -&gt; review na.hub.molten.bot.openapi.yaml for integration behaviours."`) {
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
	if !strings.Contains(body, `>Dispatch</button>`) {
		t.Fatalf("expected dispatch submit button label to be Dispatch, body=%s", body)
	}
	if !strings.Contains(body, `const dispatchTaskClear = document.getElementById("dispatch-task-clear");`) {
		t.Fatalf("expected clear button hook in client script, body=%s", body)
	}
	if !strings.Contains(body, `const dispatchOverview = document.getElementById("dispatch-overview");`) {
		t.Fatalf("expected dispatch overview client hook, body=%s", body)
	}
	if !strings.Contains(body, `const DISPATCH_OVERVIEW_STORAGE_KEY = "moltenhub.dispatchOverview.dismissed";`) {
		t.Fatalf("expected dispatch overview dismissal storage key, body=%s", body)
	}
	if !strings.Contains(body, `dismissDispatchOverview(true);`) {
		t.Fatalf("expected dispatch overview dismissal to persist, body=%s", body)
	}
	if !strings.Contains(body, `if (readDispatchOverviewDismissed() && hubConnected) {`) {
		t.Fatalf("expected dispatch overview to stay suppressible only while connected to hub, body=%s", body)
	}
	if !strings.Contains(body, `dispatchTaskClear.disabled = busy;`) {
		t.Fatalf("expected dispatch busy state to disable the clear action, body=%s", body)
	}
	if !strings.Contains(body, `dispatchTaskSubmit.textContent = busy ? "Dispatching..." : "Dispatch";`) {
		t.Fatalf("expected dispatch submit reset label to stay aligned with the rendered button copy, body=%s", body)
	}
	if !strings.Contains(body, `id="dispatch-submit-status" class="connected-agents-refresh-status"`) {
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

	form := "selectedAgentRef=worker-a&selectedSkill=code_for_me&payload_format=text&payload=Issue+an+offline+to+moltenbot+hub"
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
	if got := stub.lastDispatchReq.Payload; got != "Issue an offline to moltenbot hub" {
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
		"payload":          "{\"repos\":[\"git@github.com:Molten-Bot/moltenhub-dispatch.git\"],\"prompt\":\"Issue an offline to moltenbot hub -> review na.hub.molten.bot.openapi.yaml for integration behaviours.\"}",
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
	if got := payload["prompt"]; got != "Issue an offline to moltenbot hub -> review na.hub.molten.bot.openapi.yaml for integration behaviours." {
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

	form := "target_agent_ref=worker-a&skill_name=run_task&payload_format=text&payload=Issue+an+offline+to+moltenbot+hub"
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
	if got := stub.lastDispatchReq.Payload; got != "Issue an offline to moltenbot hub" {
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
	if !strings.Contains(body, `id="agent-settings-refresh-connected-agents"`) {
		t.Fatalf("expected settings modal refresh control for connected agents, body=%s", body)
	}
}

func TestHandleIndexOmitsPendingTasksPanelFromMainUI(t *testing.T) {
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
					ID:                "task-1",
					OriginalSkillName: "run_task",
					TargetAgentUUID:   "worker-uuid",
					LogPath:           "/tmp/logs/task-1.log",
					ExpiresAt:         time.Now().Add(time.Minute),
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
	if strings.Contains(body, ">Pending Tasks<") {
		t.Fatalf("did not expect pending tasks panel in main UI, body=%s", body)
	}
	if strings.Contains(body, "No tasks are waiting on downstream results.") {
		t.Fatalf("did not expect pending tasks empty state in main UI, body=%s", body)
	}
	if !strings.Contains(body, ">Queued Follow-Ups<") {
		t.Fatalf("expected follow-up panel to remain visible, body=%s", body)
	}
}

func TestHandleIndexRendersBottomDockAndSettingsDialogForBoundSession(t *testing.T) {
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
	if !strings.Contains(body, `id="agent-settings-dock-button"`) {
		t.Fatalf("expected settings dock button, body=%s", body)
	}
	if !strings.Contains(body, `class="prompt-mode-link prompt-mode-link-logo hub-profile-button"`) {
		t.Fatalf("expected settings dock button to render without an active class, body=%s", body)
	}
	if !strings.Contains(body, `id="theme-toggle"`) {
		t.Fatalf("expected theme toggle dock button, body=%s", body)
	}
	if !strings.Contains(body, `<span class="theme-toggle-icon" id="theme-toggle-icon" aria-hidden="true"></span>`) {
		t.Fatalf("expected theme toggle icon slot, body=%s", body)
	}
	if !strings.Contains(body, `<span id="theme-toggle-label">Dark</span>`) {
		t.Fatalf("expected dark as the initial theme label, body=%s", body)
	}
	if !strings.Contains(body, `<div class="site-bg" aria-hidden="true">`) || !strings.Contains(body, `id="snowfall-canvas"`) {
		t.Fatalf("expected themed background shell with snowfall canvas, body=%s", body)
	}
	if !strings.Contains(body, `id="agent-settings-modal-backdrop"`) {
		t.Fatalf("expected agent settings dialog markup, body=%s", body)
	}
	if !strings.Contains(body, `id="agent-settings-modal-close"`) {
		t.Fatalf("expected settings dialog close control, body=%s", body)
	}
	if !strings.Contains(body, `Update how this agent appears in Molten Hub.`) {
		t.Fatalf("expected updated agent settings summary copy, body=%s", body)
	}
	if strings.Contains(body, `Update how this dispatcher appears in Molten Hub.`) {
		t.Fatalf("did not expect outdated dispatcher settings summary copy, body=%s", body)
	}
	if !strings.Contains(body, `id="agent-settings-refresh-connected-agents"`) {
		t.Fatalf("expected connected-agents refresh button inside settings dialog, body=%s", body)
	}
	if !strings.Contains(body, `id="agent-settings-refresh-connected-agents-progress"`) {
		t.Fatalf("expected connected-agents refresh progress inside settings dialog, body=%s", body)
	}
	if strings.Contains(body, `id="agent-settings-refresh-connected-agents-next"`) {
		t.Fatalf("did not expect removed connected-agents refresh countdown inside settings dialog, body=%s", body)
	}
	if !strings.Contains(body, `id="agent-settings-refresh-connected-agents-status"`) {
		t.Fatalf("expected connected-agents refresh status inside settings dialog, body=%s", body)
	}
	if !strings.Contains(body, `class="connected-agents-refresh connected-agents-refresh-icon-button"`) {
		t.Fatalf("expected icon-only refresh button styling inside settings dialog, body=%s", body)
	}
	if strings.Contains(body, `Refresh Connected Agents`) {
		t.Fatalf("did not expect text label inside icon-only refresh button, body=%s", body)
	}
	if !strings.Contains(body, `class="profile-save-button"`) || !strings.Contains(body, `>Save</button>`) {
		t.Fatalf("expected compact save button in profile footer, body=%s", body)
	}
	if !strings.Contains(body, `const agentSettingsDockButton = document.getElementById("agent-settings-dock-button");`) {
		t.Fatalf("expected settings dock JS hook, body=%s", body)
	}
	if !strings.Contains(body, `const themeToggleButton = document.getElementById("theme-toggle");`) {
		t.Fatalf("expected theme toggle JS hook, body=%s", body)
	}
	if !strings.Contains(body, `const THEME_MODES = ["light", "dark", "night"];`) || !strings.Contains(body, `const DEFAULT_THEME_MODE = "dark";`) {
		t.Fatalf("expected theme cycle constants, body=%s", body)
	}
	if !strings.Contains(body, `const THEME_ICONS = {`) {
		t.Fatalf("expected theme icon map for toggle button, body=%s", body)
	}
	if !strings.Contains(body, `const initAppearanceControls = () => {`) || !strings.Contains(body, `applyThemeMode(loadThemeMode(), false);`) {
		t.Fatalf("expected theme controls initialization, body=%s", body)
	}
	if !strings.Contains(body, `themeToggleButton.addEventListener("click", () => {`) || !strings.Contains(body, `applyThemeMode(nextThemeMode(currentThemeMode()), true);`) {
		t.Fatalf("expected theme toggle click cycle handler, body=%s", body)
	}
	if !strings.Contains(body, `const initSnowfallBackground = () => {`) || !strings.Contains(body, `initSnowfallBackground();`) {
		t.Fatalf("expected snowfall background animation initialization, body=%s", body)
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
			RecentEvents: []app.RuntimeEvent{
				{
					Title:   "Task dispatched",
					Level:   "info",
					Detail:  "Queued code_for_me for moltenbot/jef/codex-beast",
					TaskID:  "task-123",
					LogPath: ".moltenhub/logs/task-123.log",
					At:      time.Unix(1, 0).UTC(),
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
	if !strings.Contains(body, `data-runtime-event-toggle aria-expanded="false">Open</button>`) {
		t.Fatalf("expected info event toggle to render closed by default, body=%s", body)
	}
	if !strings.Contains(body, `class="runtime-event-card-body" data-runtime-event-body hidden`) {
		t.Fatalf("expected info event body to render hidden by default, body=%s", body)
	}
	if strings.Contains(body, `data-runtime-event-toggle aria-expanded="true">Close</button>`) {
		t.Fatalf("expected all runtime event toggles to render closed by default, body=%s", body)
	}
	if !strings.Contains(body, `const runtimeEventCards = Array.from(document.querySelectorAll("[data-runtime-event-card]"));`) {
		t.Fatalf("expected runtime event JS collection hook, body=%s", body)
	}
	if !strings.Contains(body, `const initRuntimeEventCards = () => {`) {
		t.Fatalf("expected runtime event initialization helper, body=%s", body)
	}
	if !strings.Contains(body, `toggle.textContent = nextExpanded ? "Close" : "Open";`) {
		t.Fatalf("expected runtime event toggle button copy to swap between open and close, body=%s", body)
	}
	if !strings.Contains(body, `initRuntimeEventCards();`) {
		t.Fatalf("expected runtime event toggle initialization on page load, body=%s", body)
	}
}

func TestHandleIndexKeepsQueuedFollowUpsClosedByDefault(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			FollowUpTasks: []app.FollowUpTask{
				{
					ID:           "followup-1",
					Status:       "queued",
					FailedTaskID: "task-1",
					FailedRepo:   "git@github.com:Molten-Bot/moltenhub-code.git",
					LogPaths:     []string{".moltenhub/logs/task-1.log"},
					RunConfig: app.FollowUpRunConfig{
						Repos:        []string{"git@github.com:Molten-Bot/moltenhub-code.git"},
						BaseBranch:   "main",
						TargetSubdir: ".",
						Prompt:       "Review the failing log paths first.",
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
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}
	if !strings.Contains(body, ">Queued Follow-Ups<") {
		t.Fatalf("expected queued follow-up panel, body=%s", body)
	}
	if !strings.Contains(body, `class="card runtime-event-card" data-runtime-event-card`) {
		t.Fatalf("expected follow-up cards to use collapsible card class, body=%s", body)
	}
	if !strings.Contains(body, `data-runtime-event-toggle aria-expanded="false">Open</button>`) {
		t.Fatalf("expected follow-up toggle to render closed by default, body=%s", body)
	}
	if !strings.Contains(body, `class="runtime-event-card-body" data-runtime-event-body hidden`) {
		t.Fatalf("expected follow-up body to render hidden by default, body=%s", body)
	}
	if strings.Contains(body, `data-runtime-event-toggle aria-expanded="true">Close</button>`) {
		t.Fatalf("expected follow-up toggles to render closed by default, body=%s", body)
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
		t.Fatalf("expected horizontal manual dispatch target grid rule, body=%s", body)
	}
	if !strings.Contains(body, `grid-template-columns: repeat(auto-fit, minmax(min(100%, 18rem), 1fr));`) {
		t.Fatalf("expected selectable agent cards to auto-fill the available width, body=%s", body)
	}
	if !strings.Contains(body, `grid-auto-rows: 1fr;`) {
		t.Fatalf("expected selectable agent grid rows to stretch evenly, body=%s", body)
	}
	if !strings.Contains(body, `.manual-dispatch-actions {`) || !strings.Contains(body, `justify-content: flex-end;`) {
		t.Fatalf("expected manual dispatch submit actions to right-align, body=%s", body)
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
	if !strings.Contains(body, `.runtime-event-card-toggle {`) {
		t.Fatalf("expected runtime event toggle button styles, body=%s", body)
	}
	if !strings.Contains(body, `.runtime-event-card-body[hidden] {`) {
		t.Fatalf("expected runtime event hidden state styles, body=%s", body)
	}
	if !strings.Contains(body, `.brand-login-card-shell {`) {
		t.Fatalf("expected user-portal glass card shell styles, body=%s", body)
	}
	if !strings.Contains(body, `.brand-btn-primary-main {`) {
		t.Fatalf("expected user-portal primary button styles, body=%s", body)
	}
	if !strings.Contains(body, `.dispatch-overview {`) {
		t.Fatalf("expected dispatch overview layout styles, body=%s", body)
	}
	if !strings.Contains(body, `.dispatch-overview-close {`) {
		t.Fatalf("expected dispatch overview dismiss button styles, body=%s", body)
	}
	if !strings.Contains(body, `.dispatch-overview-fading {`) {
		t.Fatalf("expected dispatch overview fade-out styles, body=%s", body)
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
	if !strings.Contains(body, ".badge.completed {\n  background: var(--good);\n}") {
		t.Fatalf("expected shared completed badge compatibility selector, body=%s", body)
	}
	if !strings.Contains(body, ".task-result.completed {\n  color: var(--surface-success);\n  background: rgba(43, 182, 115, 0.1);\n}") {
		t.Fatalf("expected shared completed task result compatibility selector, body=%s", body)
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
	if !strings.Contains(body, "Sub-Actions Hidden") {
		t.Fatalf("expected hidden sub-actions notice, body=%s", body)
	}
	if !strings.Contains(body, `id="sub-actions" hidden`) {
		t.Fatalf("expected sub-actions container to be hidden, body=%s", body)
	}
	if strings.Contains(body, ">3. Connected Agents<") {
		t.Fatalf("did not expect removed connected agents section, body=%s", body)
	}
	if !strings.Contains(body, ">Dispatch<") {
		t.Fatalf("expected manual dispatch markup to remain available for client-side reveal, body=%s", body)
	}
	if !strings.Contains(body, "until this runtime is bound to Molten Hub and connectivity is working") {
		t.Fatalf("expected unbound gating reason, body=%s", body)
	}
	if strings.Contains(body, "Save Global Settings") {
		t.Fatalf("did not expect save button for global settings, body=%s", body)
	}
}

func TestHandleIndexKeepsDispatchVisibleWhenAgentsExistButHubStatusIsOffline(t *testing.T) {
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
	if strings.Contains(body, `id="sub-actions" hidden`) {
		t.Fatalf("expected dispatch surface to remain visible with known connected agents, body=%s", body)
	}
	if !strings.Contains(body, `id="sub-actions-notice" class="panel dispatch-gate-panel" hidden`) {
		t.Fatalf("expected hidden sub-actions notice when connected agents exist, body=%s", body)
	}
	if !strings.Contains(body, `const dispatchEnabled = bound && connectedAgentsCount > 0;`) {
		t.Fatalf("expected client-side sub-action gate to keep dispatch visible when agents exist, body=%s", body)
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
	if strings.Contains(body, "Dispatches skill requests to connected agents and reports failures with follow-up remediation tasks.") {
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
	if !strings.Contains(body, `const REACT_VERSION = "18.2.0";`) {
		t.Fatalf("expected shared React version constant for emoji picker modules, body=%s", body)
	}
	if !strings.Contains(body, `https://esm.sh/@emoji-mart/react@1.1.1?deps=react@${REACT_VERSION}`) {
		t.Fatalf("expected @emoji-mart/react module usage, body=%s", body)
	}
	if !strings.Contains(body, `https://esm.sh/@emoji-mart/data@1.2.1`) {
		t.Fatalf("expected @emoji-mart/data module usage, body=%s", body)
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
	if !strings.Contains(body, `id="onboarding-existing-agent-toggle"`) || !strings.Contains(body, `id="onboarding-new-agent-toggle"`) {
		t.Fatalf("expected existing/new agent mode toggles in onboarding modal, body=%s", body)
	}
	if !strings.Contains(body, `name="hub_region"`) {
		t.Fatalf("expected runtime region selector in onboarding modal, body=%s", body)
	}
	if !strings.Contains(body, `id="onboarding-steps"`) {
		t.Fatalf("expected onboarding steps container, body=%s", body)
	}
	if !strings.Contains(body, `id="onboarding-message"`) {
		t.Fatalf("expected onboarding message container, body=%s", body)
	}
	if !strings.Contains(body, `onboarding-step onboarding-step-current" data-step-id="bind"`) {
		t.Fatalf("expected bind step to render as current in unbound state, body=%s", body)
	}
	if !strings.Contains(body, "Verify the existing Molten Hub agent credential.") {
		t.Fatalf("expected existing-agent onboarding bind detail to match hub flow, body=%s", body)
	}
	if !strings.Contains(body, "Persist the agent profile in Molten Hub.") {
		t.Fatalf("expected unbound onboarding profile detail to match hub flow, body=%s", body)
	}
	if !strings.Contains(body, `id="onboarding-mode-field"`) || !strings.Contains(body, `id="onboarding-token-label"`) {
		t.Fatalf("expected redesigned onboarding form fields, body=%s", body)
	}
}

func TestHandleIndexRendersCompletedOnboardingFlowForBoundSession(t *testing.T) {
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
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, `id="onboarding-modal-backdrop"`) {
		t.Fatalf("did not expect onboarding modal once already bound, body=%s", body)
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
	if !strings.Contains(body, "Edit Agent Profile") {
		t.Fatalf("expected profile editor once already bound, body=%s", body)
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
	if strings.Contains(body, `name="handle" value="tmp-agent-123" readonly`) {
		t.Fatalf("expected temporary handle to remain editable, body=%s", body)
	}
	if !strings.Contains(body, "temporary handle") {
		t.Fatalf("expected temporary-handle onboarding hint, body=%s", body)
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
	if !strings.Contains(body, "Edit Agent Profile") {
		t.Fatalf("expected edit profile panel after bound session, body=%s", body)
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
					app.Skill{Name: "review_failure_logs", Description: "Review logs."},
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
	if !strings.Contains(body, `id="agent-settings-refresh-connected-agents"`) {
		t.Fatalf("expected connected agents refresh control in settings modal, body=%s", body)
	}
	if !strings.Contains(body, `class="connected-agent-card connected-agent-card-button"`) {
		t.Fatalf("expected connected agent card layout, body=%s", body)
	}
	if !strings.Contains(body, ">dispatcher<") {
		t.Fatalf("expected agent id secondary label on connected agent cards, body=%s", body)
	}
	if !strings.Contains(body, "Offline") {
		t.Fatalf("expected presence badge on connected agent cards, body=%s", body)
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
	if strings.Contains(body, "Choose one of the connected agents below.") {
		t.Fatalf("did not expect removed connected-agent hint copy, body=%s", body)
	}
	if !strings.Contains(body, `class="list connected-agents-list connected-agents-list-selectable manual-dispatch-targets-grid"`) {
		t.Fatalf("expected manual dispatch target grid class for horizontal fill layout, body=%s", body)
	}
	if !strings.Contains(body, `id="skill-name-select" name="skill_name"`) {
		t.Fatalf("expected skill-name dropdown in manual dispatch form, body=%s", body)
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
							{"name": "review_failure_logs", "description": "Review failing logs."},
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
	if !strings.Contains(body, ">Online<") {
		t.Fatalf("expected hub presence badge to render online status, body=%s", body)
	}
	if strings.Contains(body, ">review_openapi<") || strings.Contains(body, ">review_failure_logs<") {
		t.Fatalf("did not expect hub advertised skills to render on the agent card, body=%s", body)
	}
	if strings.Contains(body, ">8d9add87-10b1-4ee4-a138-acde48001122<") {
		t.Fatalf("did not expect UUID values to be rendered as visible card labels, body=%s", body)
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
