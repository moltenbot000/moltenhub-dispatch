package app

import (
	"context"
	"strings"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

const (
	dispatchSkillName     = "dispatch_skill_request"
	dispatcherHarness     = "moltenhub-dispatch"
	openClawHTTPProtocol  = "openclaw.http.v1"
	openClawSkillRequest  = "skill_request"
	openClawSkillResult   = "skill_result"
	openClawTaskStatus    = "task_status_update"
	openClawTextMessage   = "text_message"
	hubPingRetryInterval  = 12 * time.Second
	hubPingRequestTimeout = 6 * time.Second
	wsFallbackWindow      = 30 * time.Second
	wsUpgradeRetryWindow  = 5 * time.Second
	hubDisconnectGrace    = 30 * time.Second
)

var advertisedSkills = []Skill{
	{
		Name:        dispatchSkillName,
		Description: "Dispatch a skill request to a connected agent and proxy the result back to the original caller.",
	},
}

type HubClient interface {
	BindAgent(ctx context.Context, req hub.BindRequest) (hub.BindResponse, error)
	UpdateMetadata(ctx context.Context, token string, req hub.UpdateMetadataRequest) (map[string]any, error)
	GetCapabilities(ctx context.Context, token string) (map[string]any, error)
	PublishOpenClaw(ctx context.Context, token string, req hub.PublishRequest) (hub.PublishResponse, error)
	PullOpenClaw(ctx context.Context, token string, timeout time.Duration) (hub.PullResponse, bool, error)
	AckOpenClaw(ctx context.Context, token, deliveryID string) error
	NackOpenClaw(ctx context.Context, token, deliveryID string) error
	MarkOffline(ctx context.Context, token string, req hub.OfflineRequest) error
}

type realtimeHubClient interface {
	ConnectOpenClaw(ctx context.Context, token, sessionKey string) (hub.RealtimeSession, error)
}

type hubPingClient interface {
	CheckPing(ctx context.Context) (string, error)
}

type Service struct {
	store               *Store
	hub                 HubClient
	settings            Settings
	hubPingRetryDelay   time.Duration
	hubPingCheckTimeout time.Duration
	wsFallbackWindow    time.Duration
	wsUpgradeRetryDelay time.Duration
	presenceSynced      bool
	presenceTransport   string
	hubFailureStartedAt time.Time
}

type baseURLSetter interface {
	SetBaseURL(baseURL string)
}

type runtimeEndpointSetter interface {
	SetRuntimeEndpoints(endpoints hub.RuntimeEndpoints)
}

func NewService(store *Store, hubClient HubClient) *Service {
	snapshot := store.Snapshot()
	service := &Service{
		store:               store,
		hub:                 hubClient,
		settings:            snapshot.Settings,
		hubPingRetryDelay:   hubPingRetryInterval,
		hubPingCheckTimeout: hubPingRequestTimeout,
		wsFallbackWindow:    wsFallbackWindow,
		wsUpgradeRetryDelay: wsUpgradeRetryWindow,
	}
	service.configureHubClient(snapshot)
	return service
}

func (s *Service) Snapshot() AppState {
	return s.store.Snapshot()
}

func (s *Service) SetFlash(level, message string) error {
	level = normalizedFlashLevel(level)
	message = strings.TrimSpace(message)
	return s.store.Update(func(state *AppState) error {
		if message == "" {
			state.Flash = FlashMessage{}
			return nil
		}
		state.Flash = FlashMessage{
			Level:   level,
			Message: message,
		}
		return nil
	})
}

func (s *Service) ConsumeFlash() (FlashMessage, error) {
	snapshot := s.store.Snapshot()
	if strings.TrimSpace(snapshot.Flash.Message) == "" {
		return FlashMessage{}, nil
	}
	var consumed FlashMessage
	err := s.store.Update(func(state *AppState) error {
		consumed = state.Flash
		state.Flash = FlashMessage{}
		return nil
	})
	return consumed, err
}

func (s *Service) configureHubClient(state AppState) {
	baseURL := runtimeAPIBaseFromSession(state.Session)
	if baseURL == "" {
		baseURL = strings.TrimSpace(state.Settings.HubURL)
	}
	s.setHubBaseURL(baseURL)
	s.setRuntimeEndpoints(runtimeEndpointsFromSession(state.Session))
}

func (s *Service) syncHubClient(state AppState) {
	s.configureHubClient(state)
}

func (s *Service) refreshConfiguredState() AppState {
	state := s.store.Snapshot()
	s.settings = state.Settings
	s.syncHubClient(state)
	return state
}

func (s *Service) storeConnectedSession(runtime HubRuntime, session Session) error {
	now := time.Now().UTC()
	if session.BoundAt.IsZero() {
		session.BoundAt = now
	}
	session.HubURL = runtime.HubURL
	session.APIBase = NormalizeHubEndpointURL(coalesceTrimmed(session.APIBase, session.BaseURL))
	session.BaseURL = session.APIBase
	session.OfflineMarked = false

	return s.store.Update(func(state *AppState) error {
		state.Settings.HubRegion = runtime.ID
		state.Settings.HubURL = runtime.HubURL
		state.Session = session
		connectionBaseURL, connectionDomain := hubConnectionTarget(session.APIBase, runtime.HubURL)
		state.Connection = ConnectionState{
			Status:        ConnectionStatusConnected,
			Transport:     ConnectionTransportConnected,
			LastChangedAt: now,
			BaseURL:       connectionBaseURL,
			Domain:        connectionDomain,
		}
		return nil
	})
}

func (s *Service) setHubBaseURL(baseURL string) {
	baseURL = NormalizeHubEndpointURL(baseURL)
	if baseURL == "" {
		return
	}
	if setter, ok := s.hub.(baseURLSetter); ok {
		setter.SetBaseURL(baseURL)
	}
}

func (s *Service) setRuntimeEndpoints(endpoints hub.RuntimeEndpoints) {
	endpoints = sanitizeRuntimeEndpoints(endpoints)
	if setter, ok := s.hub.(runtimeEndpointSetter); ok {
		setter.SetRuntimeEndpoints(endpoints)
	}
}

func (s *Service) UpdateSettings(mutator func(*Settings) error) error {
	if err := s.store.Update(func(state *AppState) error {
		if err := mutator(&state.Settings); err != nil {
			return err
		}
		runtime, err := ResolveHubRuntime(state.Settings.HubRegion, state.Settings.HubURL)
		if err != nil {
			return err
		}
		state.Settings.HubRegion = runtime.ID
		state.Settings.HubURL = runtime.HubURL
		s.settings = state.Settings
		return nil
	}); err != nil {
		return err
	}
	s.configureHubClient(s.store.Snapshot())
	return nil
}

func normalizedFlashLevel(level string) string {
	if strings.EqualFold(strings.TrimSpace(level), "error") {
		return "error"
	}
	return "info"
}
