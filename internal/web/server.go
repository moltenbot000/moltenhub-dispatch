package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/app"
	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
	"github.com/moltenbot000/moltenhub-dispatch/internal/support"
)

//go:embed templates/index.html static
var assets embed.FS

var canonicalUUIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var defaultProfileEmojis = []string{"🤖", "🚀", "🧠", "🛠️", "⚡", "🔥", "✨", "🧪", "🛰️", "💡", "🌊", "🎯"}

type service interface {
	Snapshot() app.AppState
	BindAndRegister(ctx context.Context, profile app.BindProfile) error
	UpdateAgentProfile(ctx context.Context, profile app.AgentProfile) error
	RefreshAgentProfile(ctx context.Context) (app.AgentProfile, error)
	DisconnectAgent(ctx context.Context) error
	AddConnectedAgent(agent app.ConnectedAgent) error
	RefreshConnectedAgents(ctx context.Context) ([]app.ConnectedAgent, error)
	DispatchFromUI(ctx context.Context, req app.DispatchRequest) (app.PendingTask, error)
	UpdateSettings(mutator func(*app.Settings) error) error
	SetFlash(level, message string) error
	ConsumeFlash() (app.FlashMessage, error)
}

type Server struct {
	service       service
	templates     *template.Template
	mux           *http.ServeMux
	staticHandler http.Handler
}

func New(service service) (*Server, error) {
	templates, err := template.New("index.html").Funcs(template.FuncMap{
		"formatTime": func(value time.Time) string {
			if value.IsZero() {
				return "-"
			}
			return value.Local().Format(time.RFC822)
		},
		"toJSON": func(value any) string {
			data, err := json.MarshalIndent(value, "", "  ")
			if err != nil {
				return "{}"
			}
			return string(data)
		},
		"connectedAgentDisplayName":    connectedAgentDisplayName,
		"connectedAgentPresenceStatus": connectedAgentPresenceStatus,
		"connectedAgentPresenceLabel":  connectedAgentPresenceLabel,
		"connectedAgentEmoji":          connectedAgentEmoji,
		"connectedAgentSkills":         connectedAgentSkills,
	}).ParseFS(assets, "templates/index.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	staticAssets, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, fmt.Errorf("prepare static assets: %w", err)
	}

	server := &Server{
		service:       service,
		templates:     templates,
		mux:           http.NewServeMux(),
		staticHandler: http.StripPrefix("/static/", http.FileServer(http.FS(staticAssets))),
	}
	server.routes()
	return server, nil
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/status", s.handleStatus)
	s.mux.HandleFunc("/api/onboarding", s.handleOnboarding)
	s.mux.HandleFunc("/api/profile", s.handleProfileAPI)
	s.mux.HandleFunc("/api/connected-agents", s.handleConnectedAgents)
	s.mux.HandleFunc("/api/dispatch", s.handleDispatchAPI)
	s.mux.HandleFunc("/bind", s.handleBind)
	s.mux.HandleFunc("/profile", s.handleProfile)
	s.mux.HandleFunc("/disconnect", s.handleDisconnect)
	s.mux.HandleFunc("/agents", s.handleAgents)
	s.mux.HandleFunc("/dispatch", s.handleDispatch)
	s.mux.HandleFunc("/settings", s.handleSettings)
	s.mux.HandleFunc("/styles.css", s.handleStyles)
	s.mux.Handle("/static/", s.staticHandler)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.renderIndex(w, r, "", false, agentProfileForm{}, nil)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state := s.service.Snapshot()
	view := statusView{
		connectionView: connectionStatusView(state),
		PendingTasks:   state.PendingTasks,
		RecentEvents:   state.RecentEvents,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(view)
}

func (s *Server) renderIndex(w http.ResponseWriter, r *http.Request, flash string, isError bool, form agentProfileForm, onboarding *onboardingView) {
	if strings.TrimSpace(flash) == "" {
		if pendingFlash, err := s.service.ConsumeFlash(); err == nil {
			flash = pendingFlash.Message
			isError = strings.EqualFold(pendingFlash.Level, "error")
		}
	}
	state := s.service.Snapshot()
	selectedRuntime, err := app.ResolveHubRuntime(state.Settings.HubRegion, state.Settings.HubURL)
	if err != nil {
		selectedRuntime = app.DefaultHubRuntime()
	}
	currentOnboarding := defaultOnboardingView(state)
	if onboarding != nil {
		currentOnboarding = *onboarding
	}
	view := pageData{
		State:                        state,
		ActivityFeed:                 mergedActivityFeed(state.PendingTasks, state.RecentEvents),
		Flash:                        flash,
		IsError:                      isError,
		RuntimeOptions:               app.SupportedHubRuntimes(),
		SelectedRuntime:              selectedRuntime,
		ProfileForm:                  defaultProfileForm(state, form),
		Connection:                   connectionStatusView(state),
		SubActions:                   subActionState(state),
		Onboarding:                   currentOnboarding,
		GoogleAnalyticsMeasurementID: strings.TrimSpace(state.Settings.GoogleAnalyticsMeasurementID),
	}
	var rendered bytes.Buffer
	if err := s.templates.ExecuteTemplate(&rendered, "index.html", view); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = rendered.WriteTo(w)
}

func (s *Server) handleBind(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.redirectWithMessage(w, r, "error", err.Error())
		return
	}
	form := profileFormFromRequest(r)
	if err := s.applyRuntimeSelection(r.FormValue("hub_region")); err != nil {
		s.redirectWithMessage(w, r, "error", err.Error())
		return
	}
	mode, bindToken, agentToken := app.NormalizeOnboardingTokens(
		r.FormValue("agent_mode"),
		r.FormValue("bind_token"),
		r.FormValue("agent_token"),
	)
	if err := s.service.BindAndRegister(r.Context(), app.BindProfile{
		AgentMode:       mode,
		AgentToken:      agentToken,
		BindToken:       bindToken,
		Handle:          form.Handle,
		DisplayName:     form.DisplayName,
		Emoji:           form.Emoji,
		ProfileMarkdown: form.ProfileMarkdown,
	}); err != nil {
		s.redirectWithMessage(w, r, "error", err.Error())
		return
	}
	s.redirectWithMessage(w, r, "info", "Agent bound and profile registered.")
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.redirectWithMessage(w, r, "error", err.Error())
		return
	}
	form := profileFormFromRequest(r)
	if err := s.service.UpdateAgentProfile(r.Context(), app.AgentProfile{
		Handle:          form.Handle,
		DisplayName:     form.DisplayName,
		Emoji:           form.Emoji,
		ProfileMarkdown: form.ProfileMarkdown,
	}); err != nil {
		s.redirectWithMessage(w, r, "error", err.Error())
		return
	}
	s.redirectWithMessage(w, r, "info", "Agent profile updated.")
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if err := s.service.DisconnectAgent(r.Context()); err != nil {
		s.redirectWithMessage(w, r, "error", err.Error())
		return
	}
	s.redirectWithMessage(w, r, "info", "Agent disconnected.")
}

func (s *Server) handleOnboarding(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"onboarding": defaultOnboardingView(s.service.Snapshot()),
		})
		return
	case http.MethodPost:
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		AgentMode       string `json:"agent_mode"`
		HubRegion       string `json:"hub_region"`
		AgentToken      string `json:"agent_token"`
		BindToken       string `json:"bind_token"`
		Handle          string `json:"handle"`
		DisplayName     string `json:"display_name"`
		Emoji           string `json:"emoji"`
		ProfileMarkdown string `json:"profile_markdown"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		onboarding := onboardingViewFromError(app.OnboardingModeExisting, s.service.Snapshot(), app.WrapOnboardingError(app.OnboardingStepBind, fmt.Errorf("decode onboarding payload: %w", err)))
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":         false,
			"error":      "invalid onboarding request",
			"detail":     err.Error(),
			"onboarding": onboarding,
			"bound":      false,
		})
		return
	}
	mode, bindToken, agentToken := app.NormalizeOnboardingTokens(payload.AgentMode, payload.BindToken, payload.AgentToken)
	if err := s.applyRuntimeSelection(payload.HubRegion); err != nil {
		state := s.service.Snapshot()
		onboarding := onboardingViewFromError(mode, state, app.WrapOnboardingError(app.OnboardingStepBind, err))
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":         false,
			"error":      err.Error(),
			"detail":     err.Error(),
			"onboarding": onboarding,
			"bound":      strings.TrimSpace(state.Session.AgentToken) != "",
		})
		return
	}

	err := s.service.BindAndRegister(r.Context(), app.BindProfile{
		AgentMode:       mode,
		AgentToken:      agentToken,
		BindToken:       bindToken,
		Handle:          strings.TrimSpace(payload.Handle),
		DisplayName:     strings.TrimSpace(payload.DisplayName),
		Emoji:           strings.TrimSpace(payload.Emoji),
		ProfileMarkdown: strings.TrimSpace(payload.ProfileMarkdown),
	})
	state := s.service.Snapshot()
	if err != nil {
		onboarding := onboardingViewFromError(mode, state, err)
		if strings.TrimSpace(state.Session.AgentToken) != "" {
			_ = s.service.SetFlash("error", err.Error())
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":         false,
			"error":      err.Error(),
			"detail":     err.Error(),
			"onboarding": onboarding,
			"bound":      strings.TrimSpace(state.Session.AgentToken) != "",
		})
		return
	}

	successMessage := onboardingSuccessMessage(mode)
	onboarding := completedOnboardingView(mode, state)
	_ = s.service.SetFlash("info", successMessage)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"message":    successMessage,
		"onboarding": onboarding,
		"bound":      true,
	})
}

func (s *Server) handleConnectedAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agents, err := s.service.RefreshConnectedAgents(r.Context())
	if err != nil {
		state := s.service.Snapshot()
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":               false,
			"error":            "connected agents refresh failed",
			"detail":           err.Error(),
			"connected_agents": state.ConnectedAgents,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"connected_agents": agents,
	})
}

func (s *Server) handleProfileAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	profile, err := s.service.RefreshAgentProfile(r.Context())
	if err != nil {
		state := s.service.Snapshot()
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":     false,
			"error":  "agent profile refresh failed",
			"detail": err.Error(),
			"profile": map[string]any{
				"handle":           state.Session.Handle,
				"display_name":     state.Session.DisplayName,
				"emoji":            state.Session.Emoji,
				"profile_markdown": state.Session.ProfileBio,
			},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"profile": map[string]any{
			"handle":           profile.Handle,
			"display_name":     profile.DisplayName,
			"emoji":            profile.Emoji,
			"profile_markdown": profile.ProfileMarkdown,
		},
	})
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.redirectWithMessage(w, r, "error", err.Error())
		return
	}

	agent := app.ConnectedAgent{
		AgentUUID: strings.TrimSpace(r.FormValue("agent_uuid")),
		AgentID:   strings.TrimSpace(r.FormValue("id")),
		URI:       strings.TrimSpace(r.FormValue("agent_uri")),
		Handle:    strings.TrimSpace(r.FormValue("id")),
		Metadata: &hub.AgentMetadata{
			DisplayName: strings.TrimSpace(r.FormValue("name")),
			Skills:      app.SkillsToMetadata(parseSkills(strings.TrimSpace(r.FormValue("skills")))),
		},
	}
	if err := s.service.AddConnectedAgent(agent); err != nil {
		s.redirectWithMessage(w, r, "error", err.Error())
		return
	}
	s.redirectWithMessage(w, r, "info", "Connected agent saved.")
}

func (s *Server) handleDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	task, err := s.dispatchTaskFromRequest(r)
	if err != nil {
		s.redirectWithMessage(w, r, "error", err.Error())
		return
	}
	s.redirectWithMessage(w, r, "info", "Dispatched task "+task.ID)
}

func (s *Server) handleDispatchAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	task, err := s.dispatchTaskFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"task_id": task.ID,
		"message": "Dispatched task " + task.ID,
	})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.redirectWithMessage(w, r, "error", err.Error())
		return
	}
	if err := s.applyRuntimeSelection(r.FormValue("hub_region")); err != nil {
		s.redirectWithMessage(w, r, "error", err.Error())
		return
	}
	s.redirectWithMessage(w, r, "info", "Settings updated.")
}

func (s *Server) handleStyles(w http.ResponseWriter, r *http.Request) {
	data, err := assets.ReadFile("static/styles.css")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) applyRuntimeSelection(runtimeID string) error {
	runtimeID = strings.TrimSpace(runtimeID)
	if runtimeID == "" {
		return nil
	}
	runtime, err := app.ResolveHubRuntime(runtimeID, "")
	if err != nil {
		return err
	}
	return s.service.UpdateSettings(func(settings *app.Settings) error {
		settings.HubRegion = runtime.ID
		settings.HubURL = runtime.HubURL
		return nil
	})
}

func (s *Server) redirectWithMessage(w http.ResponseWriter, r *http.Request, level, message string) {
	if err := s.service.SetFlash(level, message); err != nil {
		http.Error(w, "persist flash message: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func parseFormValues(r *http.Request) (url.Values, error) {
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			return nil, err
		}
		return r.Form, nil
	}
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	return r.Form, nil
}

func (s *Server) dispatchTaskFromRequest(r *http.Request) (app.PendingTask, error) {
	values, err := parseFormValues(r)
	if err != nil {
		return app.PendingTask{}, err
	}
	dispatchReq, err := dispatchRequestFromValues(values)
	if err != nil {
		return app.PendingTask{}, err
	}
	dispatchReq.RequestID = app.NewID("ui")
	return s.service.DispatchFromUI(r.Context(), dispatchReq)
}

func decodeStructuredJSONPayload(raw string) (any, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, false
	}
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return nil, false
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return nil, false
	}
	return decoded, true
}

func dispatchRequestFromValues(values url.Values) (app.DispatchRequest, error) {
	targetAgentRef := support.FirstNonEmptyString(
		strings.TrimSpace(values.Get("target_agent_ref")),
		strings.TrimSpace(values.Get("targetAgentRef")),
		strings.TrimSpace(values.Get("selected_agent_ref")),
		strings.TrimSpace(values.Get("selectedAgentRef")),
		strings.TrimSpace(values.Get("agent_ref")),
		strings.TrimSpace(values.Get("agentRef")),
		strings.TrimSpace(values.Get("target_agent_uuid")),
		strings.TrimSpace(values.Get("targetAgentUUID")),
		strings.TrimSpace(values.Get("selected_agent_uuid")),
		strings.TrimSpace(values.Get("selectedAgentUUID")),
		strings.TrimSpace(values.Get("target_agent_uri")),
		strings.TrimSpace(values.Get("targetAgentURI")),
		strings.TrimSpace(values.Get("selected_agent_uri")),
		strings.TrimSpace(values.Get("selectedAgentURI")),
	)
	skillName := support.FirstNonEmptyString(
		strings.TrimSpace(values.Get("skill_name")),
		strings.TrimSpace(values.Get("skillName")),
		strings.TrimSpace(values.Get("selected_skill")),
		strings.TrimSpace(values.Get("selectedSkill")),
		strings.TrimSpace(values.Get("selected_skill_name")),
		strings.TrimSpace(values.Get("selectedSkillName")),
		strings.TrimSpace(values.Get("selected_task")),
		strings.TrimSpace(values.Get("selectedTask")),
		strings.TrimSpace(values.Get("task_name")),
		strings.TrimSpace(values.Get("taskName")),
		strings.TrimSpace(values.Get("task")),
	)
	if targetAgentRef == "" && skillName == "" {
		return app.DispatchRequest{}, errors.New(app.DispatchSelectionRequiredMessage)
	}

	payloadText := strings.TrimSpace(values.Get("payload"))
	payloadFormat := strings.ToLower(strings.TrimSpace(values.Get("payload_format")))
	var payloadValue any
	switch {
	case payloadText == "":
		// Hub rejects payload_format when payload is omitted.
		payloadFormat = ""
	case payloadFormat == "json":
		var decoded any
		if err := json.Unmarshal([]byte(payloadText), &decoded); err != nil {
			return app.DispatchRequest{}, fmt.Errorf("payload JSON is invalid: %w", err)
		}
		payloadValue = decoded
	case payloadFormat == "", payloadFormat == "text", payloadFormat == "markdown":
		if decoded, ok := decodeStructuredJSONPayload(payloadText); ok {
			payloadFormat = "json"
			payloadValue = decoded
		} else {
			payloadFormat = "markdown"
			payloadValue = payloadText
		}
	default:
		return app.DispatchRequest{}, errors.New("payload_format must be one of markdown or json")
	}

	timeout := 0 * time.Second
	if raw := strings.TrimSpace(values.Get("timeout_seconds")); raw != "" {
		seconds, err := time.ParseDuration(raw + "s")
		if err != nil {
			return app.DispatchRequest{}, errors.New("timeout_seconds must be numeric")
		}
		timeout = seconds
	}

	return app.DispatchRequest{
		TargetAgentRef: targetAgentRef,
		SkillName:      skillName,
		Repo:           strings.TrimSpace(values.Get("repo")),
		LogPaths:       support.SplitLines(values.Get("log_paths")),
		Payload:        payloadValue,
		PayloadFormat:  payloadFormat,
		Timeout:        timeout,
	}, nil
}

func parseSkills(raw string) []app.Skill {
	parts := strings.Split(raw, ",")
	skills := make([]app.Skill, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, description, found := strings.Cut(part, ":")
		if !found {
			skills = append(skills, app.Skill{Name: strings.TrimSpace(name)})
			continue
		}
		skills = append(skills, app.Skill{
			Name:        strings.TrimSpace(name),
			Description: strings.TrimSpace(description),
		})
	}
	return skills
}

func connectedAgentDisplayName(agent app.ConnectedAgent) string {
	for _, candidate := range app.ConnectedAgentLabelCandidates(agent) {
		if label := visibleAgentLabel(candidate); label != "" {
			return label
		}
	}
	return "Connected agent"
}

func connectedAgentPresenceStatus(agent app.ConnectedAgent) string {
	return app.ConnectedAgentPresenceStatus(agent)
}

func connectedAgentPresenceLabel(agent app.ConnectedAgent) string {
	if connectedAgentPresenceStatus(agent) == "online" {
		return "Online"
	}
	return "Offline"
}

func connectedAgentEmoji(agent app.ConnectedAgent) string {
	if emoji := strings.TrimSpace(app.ConnectedAgentEmoji(agent)); emoji != "" {
		return emoji
	}
	return "🤖"
}

func connectedAgentSkills(agent app.ConnectedAgent) []app.Skill {
	return dedupeSkills(app.ConnectedAgentSkills(agent))
}

func dedupeSkills(skills []app.Skill) []app.Skill {
	if len(skills) == 0 {
		return nil
	}
	out := make([]app.Skill, 0, len(skills))
	seen := make(map[string]struct{}, len(skills))
	for _, skill := range skills {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, app.Skill{
			Name:        name,
			Description: strings.TrimSpace(skill.Description),
		})
	}
	return out
}

func visibleAgentLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || looksLikeUUID(value) {
		return ""
	}
	return value
}

func looksLikeUUID(value string) bool {
	return canonicalUUIDPattern.MatchString(strings.TrimSpace(value))
}

type pageData struct {
	State                        app.AppState
	ActivityFeed                 []activityFeedItem
	Flash                        string
	IsError                      bool
	RuntimeOptions               []app.HubRuntime
	SelectedRuntime              app.HubRuntime
	ProfileForm                  agentProfileForm
	Connection                   connectionView
	SubActions                   subActionView
	Onboarding                   onboardingView
	GoogleAnalyticsMeasurementID string
}

type connectionView struct {
	Status       string `json:"status"`
	Transport    string `json:"transport"`
	Label        string `json:"label"`
	Description  string `json:"description"`
	Error        string `json:"error,omitempty"`
	HubConnected bool   `json:"hub_connected"`
	HubTransport string `json:"hub_transport,omitempty"`
	HubBaseURL   string `json:"hub_base_url,omitempty"`
	HubDomain    string `json:"hub_domain,omitempty"`
	HubDetail    string `json:"hub_detail,omitempty"`
}

type statusView struct {
	connectionView
	PendingTasks []app.PendingTask  `json:"pending_tasks"`
	RecentEvents []app.RuntimeEvent `json:"recent_events"`
}

type activityFeedItem struct {
	SortAt         time.Time
	Kind           string
	Title          string
	Subtitle       string
	Status         string
	StatusClass    string
	StatusIcon     string
	WhenLabel      string
	Detail         string
	Level          string
	Skill          string
	TargetAgent    string
	TaskID         string
	ChildRequestID string
	LogPath        string
	Repo           string
	ExpiresAtLabel string
	IsPendingTask  bool
	IsRecentEvent  bool
}

type subActionView struct {
	Visible                 bool
	Reason                  string
	RequiresAgentConnection bool
	AgentConnectURL         string
}

type onboardingView struct {
	Steps   []onboardingStepView `json:"steps"`
	Stage   string               `json:"stage,omitempty"`
	Active  bool                 `json:"active,omitempty"`
	Message string               `json:"message,omitempty"`
	Error   bool                 `json:"error,omitempty"`
}

type onboardingStepView struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}
type agentProfileForm struct {
	BindToken       string
	Handle          string
	DisplayName     string
	Emoji           string
	ProfileMarkdown string
}

func profileFormFromRequest(r *http.Request) agentProfileForm {
	return agentProfileForm{
		BindToken:       strings.TrimSpace(r.FormValue("bind_token")),
		Handle:          strings.TrimSpace(r.FormValue("handle")),
		DisplayName:     strings.TrimSpace(r.FormValue("display_name")),
		Emoji:           strings.TrimSpace(r.FormValue("emoji")),
		ProfileMarkdown: strings.TrimSpace(r.FormValue("profile_markdown")),
	}
}

func defaultProfileForm(state app.AppState, form agentProfileForm) agentProfileForm {
	if state.Session.AgentToken != "" {
		if form.Handle == "" {
			form.Handle = state.Session.Handle
		}
		if form.DisplayName == "" {
			form.DisplayName = state.Session.DisplayName
		}
		if form.Emoji == "" {
			form.Emoji = state.Session.Emoji
		}
		if form.Emoji == "" {
			form.Emoji = randomDefaultProfileEmoji()
		}
		if form.ProfileMarkdown == "" {
			form.ProfileMarkdown = state.Session.ProfileBio
		}
		return form
	}

	if form.Emoji == "" {
		form.Emoji = randomDefaultProfileEmoji()
	}
	return form
}

func randomDefaultProfileEmoji() string {
	if len(defaultProfileEmojis) == 0 {
		return "🤖"
	}
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(defaultProfileEmojis))))
	if err != nil {
		return defaultProfileEmojis[int(time.Now().UnixNano()%int64(len(defaultProfileEmojis)))]
	}
	return defaultProfileEmojis[index.Int64()]
}

func mergedActivityFeed(tasks []app.PendingTask, events []app.RuntimeEvent) []activityFeedItem {
	items := make([]activityFeedItem, 0, len(tasks)+len(events))
	for _, task := range tasks {
		target := runtimeTargetAgentLabel(task.TargetAgentDisplayName, task.TargetAgentEmoji, task.TargetAgentUUID, task.TargetAgentURI)
		skill := strings.TrimSpace(task.OriginalSkillName)
		if skill == "" {
			skill = strings.TrimSpace(task.ID)
		}
		status := pendingTaskStatusLabel(task.Status)
		items = append(items, activityFeedItem{
			SortAt:         task.CreatedAt,
			Kind:           "pending_task",
			Title:          support.FirstNonEmptyString(target, "Unknown agent"),
			Subtitle:       skill,
			Status:         status,
			StatusClass:    pendingTaskStatusClass(task.Status),
			StatusIcon:     pendingTaskStatusIcon(task.Status),
			WhenLabel:      formatTimestamp(task.CreatedAt),
			Skill:          skill,
			TargetAgent:    target,
			TaskID:         strings.TrimSpace(task.ID),
			ChildRequestID: strings.TrimSpace(task.ChildRequestID),
			LogPath:        strings.TrimSpace(task.LogPath),
			Repo:           strings.TrimSpace(task.Repo),
			ExpiresAtLabel: formatTimestamp(task.ExpiresAt),
			IsPendingTask:  true,
		})
	}
	for _, event := range events {
		target := runtimeTargetAgentLabel(event.TargetAgentDisplayName, event.TargetAgentEmoji, event.TargetAgentUUID, event.TargetAgentURI)
		skill := strings.TrimSpace(event.OriginalSkillName)
		title := support.FirstNonEmptyString(target, strings.TrimSpace(event.Title), "Runtime event")
		subtitle := ""
		if target != "" {
			subtitle = joinNonEmpty(" • ", skill, strings.TrimSpace(event.Title))
		}
		items = append(items, activityFeedItem{
			SortAt:        event.At,
			Kind:          "recent_event",
			Title:         title,
			Subtitle:      subtitle,
			WhenLabel:     formatTimestamp(event.At),
			Detail:        strings.TrimSpace(event.Detail),
			Level:         strings.TrimSpace(event.Level),
			Skill:         skill,
			TargetAgent:   target,
			TaskID:        strings.TrimSpace(event.TaskID),
			LogPath:       strings.TrimSpace(event.LogPath),
			IsRecentEvent: true,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		left := items[i].SortAt
		right := items[j].SortAt
		switch {
		case left.IsZero() && right.IsZero():
			return items[i].Kind < items[j].Kind
		case left.IsZero():
			return false
		case right.IsZero():
			return true
		case left.Equal(right):
			return items[i].Kind < items[j].Kind
		default:
			return left.After(right)
		}
	})
	return items
}

func runtimeTargetAgentLabel(displayName, emoji, uuid, uri string) string {
	displayName = strings.TrimSpace(displayName)
	if displayName != "" {
		return strings.TrimSpace(support.FirstNonEmptyString(strings.TrimSpace(emoji), "🤖") + " " + displayName)
	}
	return support.FirstNonEmptyString(strings.TrimSpace(uuid), strings.TrimSpace(uri))
}

func pendingTaskStatusLabel(status string) string {
	if strings.TrimSpace(status) == app.PendingTaskStatusSending {
		return "Sending"
	}
	return "In Queue"
}

func pendingTaskStatusClass(status string) string {
	if strings.TrimSpace(status) == app.PendingTaskStatusSending {
		return "runtime-event-card-status runtime-event-card-status-sending"
	}
	return "runtime-event-card-status runtime-event-card-status-queued"
}

func pendingTaskStatusIcon(status string) string {
	if strings.TrimSpace(status) == app.PendingTaskStatusSending {
		return "send"
	}
	return "clock-3"
}

func formatTimestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Local().Format(time.RFC822)
}

func joinNonEmpty(sep string, values ...string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, sep)
}

func connectionStatusView(appState app.AppState) connectionView {
	state := appState.Connection
	status := strings.TrimSpace(state.Status)
	if status == "" {
		status = app.ConnectionStatusDisconnected
	}
	transport := strings.TrimSpace(state.Transport)
	if transport == "" {
		transport = app.ConnectionTransportOffline
	}
	baseURL := strings.TrimSpace(state.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(appState.Session.APIBase)
	}
	if baseURL == "" {
		baseURL = strings.TrimSpace(appState.Settings.HubURL)
	}
	domain := strings.TrimSpace(state.Domain)
	if domain == "" {
		if parsed, err := url.Parse(baseURL); err == nil {
			domain = strings.TrimSpace(parsed.Host)
		}
	}
	detail := strings.TrimSpace(state.Detail)
	if detail == "" {
		detail = strings.TrimSpace(state.Error)
	}
	target := domain
	if target == "" {
		target = baseURL
	}

	view := connectionView{
		Status:       status,
		Transport:    transport,
		Error:        strings.TrimSpace(state.Error),
		HubConnected: connectionUsable(status, transport),
		HubTransport: transport,
		HubBaseURL:   baseURL,
		HubDomain:    domain,
		HubDetail:    detail,
	}
	switch {
	case transport == app.ConnectionTransportWebSocket:
		view.Label = "WS Connected"
		view.Description = fmt.Sprintf("Connected via WebSocket to %s", fallbackTarget(target))
	case transport == app.ConnectionTransportHTTPLong || transport == app.ConnectionTransportHTTP:
		view.Label = "HTTP Connected"
		view.Description = fmt.Sprintf("Connected via HTTP long polling to %s", fallbackTarget(target))
	case status == app.ConnectionStatusConnected:
		view.Label = "Connected"
		view.Description = fmt.Sprintf("Connected to %s (transport pending)", fallbackTarget(target))
	case transport == app.ConnectionTransportReachable:
		view.Label = "Connecting"
		view.Description = support.FirstNonEmptyString(detail, fmt.Sprintf("Hub endpoint is live at %s. Connecting...", fallbackTarget(target)))
	case transport == app.ConnectionTransportRetrying:
		view.Label = "Retrying"
		view.Description = support.FirstNonEmptyString(detail, fmt.Sprintf("Hub endpoint is waking up at %s. Retrying ping every 12s.", fallbackTarget(target)))
	case target != "":
		view.Label = "Disconnected"
		view.Description = support.FirstNonEmptyString(detail, fmt.Sprintf("Disconnected from %s", target))
	case view.Error != "" || detail != "":
		view.Label = "Error"
		view.Description = support.FirstNonEmptyString(detail, view.Error)
	case strings.TrimSpace(appState.Session.AgentToken) != "":
		view.Label = "Disconnected"
		view.Description = "Configured locally. Restart runtime to connect."
	default:
		view.Label = "Offline"
		view.Description = "Connect to Molten Hub"
	}
	return view
}

func fallbackTarget(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return "hub"
	}
	return target
}

func connectionUsable(status, transport string) bool {
	status = strings.TrimSpace(status)
	transport = strings.TrimSpace(transport)
	return status == app.ConnectionStatusConnected ||
		transport == app.ConnectionTransportWebSocket ||
		transport == app.ConnectionTransportHTTPLong ||
		transport == app.ConnectionTransportHTTP ||
		transport == app.ConnectionTransportConnected
}

func subActionState(state app.AppState) subActionView {
	if strings.TrimSpace(state.Session.AgentToken) == "" {
		return subActionView{
			Visible: false,
			Reason:  "Sub-actions stay hidden until this runtime is bound to Molten Hub and connectivity is working.",
		}
	}
	if len(state.ConnectedAgents) > 0 {
		return subActionView{Visible: true}
	}
	if !connectionUsable(state.Connection.Status, state.Connection.Transport) {
		return subActionView{
			Visible: false,
			Reason:  "Sub-actions stay hidden while the Hub connection is offline or unavailable.",
		}
	}
	return subActionView{
		Visible:                 false,
		Reason:                  "No talkable peer agents are available yet. Bound agents are listed in Molten Bot Hub, and dispatch targets appear here after Hub trust/connectivity makes them reachable.",
		RequiresAgentConnection: true,
		AgentConnectURL:         "https://app.molten.bot/hub",
	}
}

func defaultOnboardingView(state app.AppState) onboardingView {
	mode := onboardingModeForState(state)
	steps := defaultOnboardingSteps(mode)
	if strings.TrimSpace(state.Session.AgentToken) != "" {
		for i := range steps {
			steps[i].Status = "completed"
		}
		if !connectionUsable(state.Connection.Status, state.Connection.Transport) {
			connection := connectionStatusView(state)
			message := strings.TrimSpace(connection.Description)
			if message == "" {
				message = "Molten Hub is not connected."
			}
			return onboardingView{
				Steps:   steps,
				Stage:   app.OnboardingStepWorkActivate,
				Active:  false,
				Message: message,
				Error:   true,
			}
		}
		return onboardingView{
			Steps:   steps,
			Stage:   app.OnboardingStepWorkActivate,
			Active:  false,
			Message: onboardingSuccessMessage(mode),
		}
	}
	setOnboardingProgress(steps, app.OnboardingStepBind, "current", "")
	return onboardingView{
		Steps:  steps,
		Stage:  app.OnboardingStepBind,
		Active: false,
	}
}

func completedOnboardingView(mode string, _ app.AppState) onboardingView {
	steps := defaultOnboardingSteps(mode)
	for i := range steps {
		steps[i].Status = "completed"
	}
	return onboardingView{
		Steps:   steps,
		Stage:   app.OnboardingStepWorkActivate,
		Active:  false,
		Message: onboardingSuccessMessage(mode),
	}
}

func onboardingViewFromError(mode string, _ app.AppState, err error) onboardingView {
	stage := app.OnboardingStageFromError(err)
	steps := defaultOnboardingSteps(mode)
	setOnboardingProgress(steps, stage, "error", err.Error())
	return onboardingView{
		Steps:   steps,
		Stage:   stage,
		Active:  false,
		Message: err.Error(),
		Error:   true,
	}
}

func defaultOnboardingSteps(mode string) []onboardingStepView {
	base := app.DefaultOnboardingStepsForMode(mode)
	steps := make([]onboardingStepView, 0, len(base))
	for _, step := range base {
		steps = append(steps, onboardingStepView{
			ID:     step.ID,
			Label:  step.Label,
			Status: step.Status,
			Detail: step.Detail,
		})
	}
	return steps
}

func onboardingModeForState(state app.AppState) string {
	return app.OnboardingModeExisting
}

func onboardingSuccessMessage(mode string) string {
	if app.NormalizeOnboardingMode(mode, "", "") == app.OnboardingModeExisting {
		return "Existing agent connected and profile registered."
	}
	return "Agent bound and profile registered."
}

func setOnboardingProgress(steps []onboardingStepView, stage, status, detail string) {
	stage = strings.TrimSpace(stage)
	status = strings.TrimSpace(status)
	if status == "" {
		status = "pending"
	}

	stageIndex := 0
	for i := range steps {
		if steps[i].ID == stage {
			stageIndex = i
			break
		}
	}

	for i := range steps {
		switch {
		case i < stageIndex:
			steps[i].Status = "completed"
		case i == stageIndex:
			steps[i].Status = status
			if strings.TrimSpace(detail) != "" {
				steps[i].Detail = strings.TrimSpace(detail)
			}
		default:
			steps[i].Status = "pending"
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
