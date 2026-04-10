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
}

func (s *stubService) Snapshot() app.AppState {
	return s.state
}

func (s *stubService) BindAndRegister(_ context.Context, profile app.BindProfile) error {
	if s.bindErr != nil {
		return s.bindErr
	}
	s.state.Session.AgentToken = "agent-token"
	s.state.Session.Handle = profile.Handle
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
}

func TestHandleIndexShowsBoundProfileState(t *testing.T) {
	t.Parallel()

	server, err := New(&stubService{
		state: app.AppState{
			Settings: app.DefaultSettings(),
			Session: app.Session{
				AgentToken:  "agent-token",
				Handle:      "codex-beast",
				DisplayName: "Jef's Codex",
				Emoji:       "💯",
				ProfileBio:  "What this runtime is for",
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

	server.renderIndex(rec, req, "", false, agentProfileForm{})

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
