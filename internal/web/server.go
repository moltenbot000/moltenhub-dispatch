package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/app"
)

//go:embed templates/index.html static/styles.css
var assets embed.FS

type service interface {
	Snapshot() app.AppState
	BindAndRegister(ctx context.Context, profile app.BindProfile) error
	AddConnectedAgent(agent app.ConnectedAgent) error
	DispatchFromUI(ctx context.Context, req app.DispatchRequest) (app.PendingTask, error)
	UpdateSettings(mutator func(*app.Settings) error) error
}

type Server struct {
	service   service
	templates *template.Template
	mux       *http.ServeMux
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
	}).ParseFS(assets, "templates/index.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	server := &Server{
		service:   service,
		templates: templates,
		mux:       http.NewServeMux(),
	}
	server.routes()
	return server, nil
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/bind", s.handleBind)
	s.mux.HandleFunc("/agents", s.handleAgents)
	s.mux.HandleFunc("/dispatch", s.handleDispatch)
	s.mux.HandleFunc("/settings", s.handleSettings)
	s.mux.HandleFunc("/styles.css", s.handleStyles)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	state := s.service.Snapshot()
	selectedRuntime, err := app.ResolveHubRuntime(state.Settings.HubRegion, state.Settings.HubURL)
	if err != nil {
		selectedRuntime = app.DefaultHubRuntime()
	}
	view := pageData{
		State:            state,
		Flash:            r.URL.Query().Get("message"),
		IsError:          r.URL.Query().Get("level") == "error",
		RuntimeOptions:   app.SupportedHubRuntimes(),
		SelectedRuntime:  selectedRuntime,
	}
	if err := s.templates.ExecuteTemplate(w, "index.html", view); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
	if err := s.service.BindAndRegister(r.Context(), app.BindProfile{
		HubRegion:       strings.TrimSpace(r.FormValue("hub_region")),
		HubURL:          strings.TrimSpace(r.FormValue("hub_url")),
		BindToken:       strings.TrimSpace(r.FormValue("bind_token")),
		Handle:          strings.TrimSpace(r.FormValue("handle")),
		ProfileMarkdown: r.FormValue("profile_markdown"),
	}); err != nil {
		s.redirectWithMessage(w, r, "error", err.Error())
		return
	}
	s.redirectWithMessage(w, r, "info", "Agent bound and profile registered.")
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
		ID:              strings.TrimSpace(r.FormValue("id")),
		Name:            strings.TrimSpace(r.FormValue("name")),
		AgentUUID:       strings.TrimSpace(r.FormValue("agent_uuid")),
		AgentURI:        strings.TrimSpace(r.FormValue("agent_uri")),
		DefaultSkill:    strings.TrimSpace(r.FormValue("default_skill")),
		FailureReviewer: r.FormValue("failure_reviewer") == "on",
		Repo:            strings.TrimSpace(r.FormValue("repo")),
		Notes:           strings.TrimSpace(r.FormValue("notes")),
		AdvertisedSkills: parseSkills(strings.TrimSpace(
			r.FormValue("skills"),
		)),
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
	if err := r.ParseForm(); err != nil {
		s.redirectWithMessage(w, r, "error", err.Error())
		return
	}

	payloadText := strings.TrimSpace(r.FormValue("payload"))
	payloadValue := any(payloadText)
	payloadFormat := "markdown"
	if strings.EqualFold(strings.TrimSpace(r.FormValue("payload_format")), "json") {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(payloadText), &decoded); err != nil {
			s.redirectWithMessage(w, r, "error", "payload JSON is invalid: "+err.Error())
			return
		}
		payloadValue = decoded
		payloadFormat = "json"
	}

	timeout := 0 * time.Second
	if raw := strings.TrimSpace(r.FormValue("timeout_seconds")); raw != "" {
		seconds, err := time.ParseDuration(raw + "s")
		if err != nil {
			s.redirectWithMessage(w, r, "error", "timeout_seconds must be numeric")
			return
		}
		timeout = seconds
	}

	task, err := s.service.DispatchFromUI(r.Context(), app.DispatchRequest{
		RequestID:      app.NewID("ui"),
		TargetAgentRef: strings.TrimSpace(r.FormValue("target_agent_ref")),
		SkillName:      strings.TrimSpace(r.FormValue("skill_name")),
		Repo:           strings.TrimSpace(r.FormValue("repo")),
		LogPaths:       splitLines(r.FormValue("log_paths")),
		Payload:        payloadValue,
		PayloadFormat:  payloadFormat,
		Timeout:        timeout,
	})
	if err != nil {
		s.redirectWithMessage(w, r, "error", err.Error())
		return
	}
	s.redirectWithMessage(w, r, "info", "Dispatched task "+task.ID)
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
	err := s.service.UpdateSettings(func(settings *app.Settings) error {
		if ref := strings.TrimSpace(r.FormValue("follow_up_agent_ref")); ref != "" {
			settings.FollowUpAgentRef = ref
		} else {
			settings.FollowUpAgentRef = ""
		}
		return nil
	})
	if err != nil {
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

func (s *Server) redirectWithMessage(w http.ResponseWriter, r *http.Request, level, message string) {
	target := "/?level=" + url.QueryEscape(level) + "&message=" + url.QueryEscape(message)
	http.Redirect(w, r, target, http.StatusSeeOther)
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

func splitLines(raw string) []string {
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

type pageData struct {
	State           app.AppState
	Flash           string
	IsError         bool
	RuntimeOptions  []app.HubRuntime
	SelectedRuntime app.HubRuntime
}
