package web

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/app"
)

type stubService struct {
	state            app.AppState
	bindErr          error
	updateProfileErr error
	bindStateOnError bool
	lastBindProfile  app.BindProfile
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

func (s *stubService) DispatchFromUI(context.Context, app.DispatchRequest) (app.PendingTask, error) {
	return app.PendingTask{}, nil
}

func (s *stubService) UpdateSettings(func(*app.Settings) error) error {
	return nil
}

func TestHandleBindRendersSubmittedTokenOnFailure(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{bindErr: errors.New("bind failed")})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/bind", strings.NewReader("bind_token=bind-123&email=jef%40site.com&handle=codex-beast&display_name=Jef%27s+Codex&emoji=%F0%9F%92%AF&profile_markdown=What+this+runtime+is+for"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}
	if !strings.Contains(body, `name="bind_token" value="bind-123"`) {
		t.Fatalf("expected bind token to remain in form, body=%s", body)
	}
	if !strings.Contains(body, `name="email" type="email" value="jef@site.com"`) {
		t.Fatalf("expected email to remain in form, body=%s", body)
	}
	if !strings.Contains(body, "bind failed") {
		t.Fatalf("expected bind error in page, body=%s", body)
	}
	if !strings.Contains(body, `id="bind-submit">Bind</button>`) {
		t.Fatalf("expected bind CTA label to be Bind, body=%s", body)
	}
	if strings.Contains(body, "Bind And Register") {
		t.Fatalf("did not expect legacy bind CTA label, body=%s", body)
	}
}

func TestHandleBindPassesSubmittedEmailToService(t *testing.T) {
	t.Parallel()

	stub := &stubService{}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/bind", strings.NewReader("bind_token=bind-123&email=jef%40site.com&display_name=Jef%27s+Codex"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect response, got %d", rec.Code)
	}
	if stub.lastBindProfile.Email != "jef@site.com" {
		t.Fatalf("expected submitted email to reach service, got %#v", stub.lastBindProfile)
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
		OK      bool   `json:"ok"`
		Message string `json:"message"`
		Bound   bool   `json:"bound"`
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
				DisplayName:     "Jef's Codex",
				Emoji:           "💯",
				ProfileBio:      "What this runtime is for",
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
	if strings.Contains(body, `name="bind_token"`) {
		t.Fatalf("did not expect bind token field after bind, body=%s", body)
	}
	if !strings.Contains(body, `name="handle" value="codex-beast" readonly`) {
		t.Fatalf("expected readonly handle field, body=%s", body)
	}
	if !strings.Contains(body, `name="display_name" value="Jef&#39;s Codex"`) {
		t.Fatalf("expected display name field, body=%s", body)
	}
	if !strings.Contains(body, `id="connection-indicator"`) {
		t.Fatalf("expected connection indicator in page, body=%s", body)
	}
	if !strings.Contains(body, "Bound Session") {
		t.Fatalf("expected bound session summary, body=%s", body)
	}
	if !strings.Contains(body, "The one-time bind token is no longer needed here.") {
		t.Fatalf("expected bound-state explanation, body=%s", body)
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
	if !strings.Contains(body, `id="global-settings-form"`) {
		t.Fatalf("expected global settings form id for auto-save, body=%s", body)
	}
	if !strings.Contains(body, `data-auto-save-setting`) {
		t.Fatalf("expected runtime inputs to opt into auto-save, body=%s", body)
	}
	if !strings.Contains(body, ">4. Manual Dispatch<") {
		t.Fatalf("expected sub-actions when bound and connected, body=%s", body)
	}
	if !strings.Contains(body, `id="sub-actions-notice" class="panel" hidden`) {
		t.Fatalf("expected sub-action notice to be hidden when bound and connected, body=%s", body)
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
	if !strings.Contains(body, ">3. Connected Agents<") {
		t.Fatalf("expected connected agents markup to remain available for client-side reveal, body=%s", body)
	}
	if !strings.Contains(body, ">4. Manual Dispatch<") {
		t.Fatalf("expected manual dispatch markup to remain available for client-side reveal, body=%s", body)
	}
	if !strings.Contains(body, "until this runtime is bound to Molten Hub and connectivity is working") {
		t.Fatalf("expected unbound gating reason, body=%s", body)
	}
	if strings.Contains(body, "Save Global Settings") {
		t.Fatalf("did not expect save button for global settings, body=%s", body)
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

	req := httptest.NewRequest(http.MethodPost, "/bind", strings.NewReader("bind_token=bind-123&email=jef%40site.com&handle=codex-beast&display_name=Jef%27s+Codex&emoji=%F0%9F%92%AF&profile_markdown=What+this+runtime+is+for"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d", rec.Code)
	}
	if strings.Contains(body, `name="bind_token"`) {
		t.Fatalf("did not expect bind token field after session became bound, body=%s", body)
	}
	if !strings.Contains(body, "Edit Agent Profile") {
		t.Fatalf("expected edit profile panel after bound session, body=%s", body)
	}
	if !strings.Contains(body, "live connection is currently offline") {
		t.Fatalf("expected offline bound-state summary, body=%s", body)
	}
	if !strings.Contains(body, "agent bound, but profile registration failed") {
		t.Fatalf("expected surfaced bind error, body=%s", body)
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
		Status    string `json:"status"`
		Transport string `json:"transport"`
		Label     string `json:"label"`
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
