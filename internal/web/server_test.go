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
	state             app.AppState
	bindErr           error
	updateProfileErr  error
	updateSettingsErr error
	bindStateOnError  bool
	lastBindProfile   app.BindProfile
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

func (s *stubService) UpdateSettings(mutator func(*app.Settings) error) error {
	if s.updateSettingsErr != nil {
		return s.updateSettingsErr
	}
	return mutator(&s.state.Settings)
}

func TestHandleBindRendersSubmittedTokenOnFailure(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{bindErr: errors.New("bind failed")})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/bind", strings.NewReader("bind_token=bind-123&handle=codex-beast&display_name=Jef%27s+Codex&emoji=%F0%9F%92%AF&profile_markdown=What+this+runtime+is+for"))
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

func TestHandleBindPassesSubmittedProfileToService(t *testing.T) {
	t.Parallel()

	stub := &stubService{}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/bind", strings.NewReader("bind_token=bind-123&handle=codex-beast&display_name=Jef%27s+Codex"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect response, got %d", rec.Code)
	}
	if stub.lastBindProfile.Handle != "codex-beast" {
		t.Fatalf("expected submitted handle to reach service, got %#v", stub.lastBindProfile)
	}
	if stub.lastBindProfile.DisplayName != "Jef's Codex" {
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
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/?level=info&message=Settings+updated.", nil)
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
	if !strings.Contains(body, `id="hub-conn-item"`) {
		t.Fatalf("expected connection indicator in page, body=%s", body)
	}
	if strings.Contains(body, "Awaiting Bind") {
		t.Fatalf("did not expect removed bind state section, body=%s", body)
	}
	if strings.Contains(body, "one-time bind token") {
		t.Fatalf("did not expect removed bind state copy, body=%s", body)
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
	if !strings.Contains(body, ">4. Manual Dispatch<") {
		t.Fatalf("expected sub-actions when bound and connected, body=%s", body)
	}
	if !strings.Contains(body, `id="sub-actions-notice" class="panel" hidden`) {
		t.Fatalf("expected sub-action notice to be hidden when bound and connected, body=%s", body)
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
	if !strings.Contains(body, `id="agent-settings-dock-button"`) {
		t.Fatalf("expected settings dock button, body=%s", body)
	}
	if !strings.Contains(body, `id="agent-settings-modal-backdrop"`) {
		t.Fatalf("expected agent settings dialog markup, body=%s", body)
	}
	if !strings.Contains(body, `id="agent-settings-modal-close"`) {
		t.Fatalf("expected settings dialog close control, body=%s", body)
	}
	if !strings.Contains(body, `const agentSettingsDockButton = document.getElementById("agent-settings-dock-button");`) {
		t.Fatalf("expected settings dock JS hook, body=%s", body)
	}
	if !strings.Contains(body, `const setAgentSettingsModalOpen = (open, returnFocus = false) => {`) {
		t.Fatalf("expected settings dialog open/close handler, body=%s", body)
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
	if !strings.Contains(body, "Exchange the bind token for an agent credential.") {
		t.Fatalf("expected unbound onboarding bind detail to match hub flow, body=%s", body)
	}
	if !strings.Contains(body, "Persist the agent profile in Molten Hub.") {
		t.Fatalf("expected unbound onboarding profile detail to match hub flow, body=%s", body)
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

	req := httptest.NewRequest(http.MethodPost, "/bind", strings.NewReader("bind_token=bind-123&handle=codex-beast&display_name=Jef%27s+Codex&emoji=%F0%9F%92%AF&profile_markdown=What+this+runtime+is+for"))
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
