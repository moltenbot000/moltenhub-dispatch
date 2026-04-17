package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
	"github.com/moltenbot000/moltenhub-dispatch/internal/support"
)

const (
	dispatchSkillName     = "dispatch_skill_request"
	dispatcherHarness     = "moltenhub-dispatch"
	openClawHTTPProtocol  = "openclaw.http.v1"
	openClawSkillRequest  = "skill_request"
	openClawSkillResult   = "skill_result"
	hubPingRetryInterval  = 12 * time.Second
	hubPingRequestTimeout = 6 * time.Second
	wsFallbackWindow      = 30 * time.Second
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
	presenceSynced      bool
	presenceTransport   string
}

type failureReport struct {
	Message    string
	Error      string
	Detail     any
	Retryable  bool
	NextAction string
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

func (s *Service) BindAndRegister(ctx context.Context, profile BindProfile) error {
	state := s.store.Snapshot()
	runtime, err := ResolveHubRuntime(state.Settings.HubRegion, state.Settings.HubURL)
	if err != nil {
		return WrapOnboardingError(OnboardingStepBind, err)
	}
	agentProfile := normalizeAgentProfile(AgentProfile{
		Handle:          profile.Handle,
		DisplayName:     profile.DisplayName,
		Emoji:           profile.Emoji,
		ProfileMarkdown: profile.ProfileMarkdown,
	})
	agentProfile.Handle = strings.TrimSpace(agentProfile.Handle)
	mode := NormalizeOnboardingMode(profile.AgentMode, profile.BindToken, profile.AgentToken)
	if mode == OnboardingModeExisting {
		return s.connectExistingAgent(ctx, runtime, strings.TrimSpace(profile.AgentToken), agentProfile)
	}
	handleRequestedDuringBind := agentProfile.Handle != ""
	s.setHubBaseURL(runtime.HubURL)
	result, err := s.hub.BindAgent(ctx, hub.BindRequest{
		HubURL:    runtime.HubURL,
		BindToken: profile.BindToken,
		Handle:    agentProfile.Handle,
	})
	if err != nil {
		return WrapOnboardingError(OnboardingStepBind, err)
	}
	result.AgentToken = strings.TrimSpace(result.AgentToken)
	if result.AgentToken == "" {
		return WrapOnboardingError(OnboardingStepBind, errors.New("bind response missing agent token"))
	}
	if rawAPIBase := strings.TrimSpace(result.APIBase); rawAPIBase != "" && NormalizeHubEndpointURL(rawAPIBase) == "" {
		return WrapOnboardingError(OnboardingStepBind, fmt.Errorf("bind response returned unsupported api_base %q", rawAPIBase))
	}
	runtimeEndpoints := runtimeEndpointsFromBind(result)
	if invalid := invalidRuntimeEndpoints(runtimeEndpoints); len(invalid) > 0 {
		return WrapOnboardingError(OnboardingStepBind, fmt.Errorf("bind response returned unsupported runtime endpoint(s): %s", strings.Join(invalid, ", ")))
	}
	runtimeEndpoints = sanitizeRuntimeEndpoints(runtimeEndpoints)
	result.APIBase = coalesceTrimmed(
		NormalizeHubEndpointURL(strings.TrimSpace(result.APIBase)),
		NormalizeHubEndpointURL(runtimeAPIBaseFromSession(Session{
			APIBase:         strings.TrimSpace(result.APIBase),
			BaseURL:         strings.TrimSpace(result.APIBase),
			ManifestURL:     runtimeEndpoints.ManifestURL,
			MetadataURL:     runtimeEndpoints.MetadataURL,
			Capabilities:    runtimeEndpoints.CapabilitiesURL,
			OpenClawPullURL: runtimeEndpoints.OpenClawPullURL,
			OpenClawPushURL: runtimeEndpoints.OpenClawPushURL,
			OfflineURL:      runtimeEndpoints.OpenClawOfflineURL,
		})),
		NormalizeHubEndpointURL(defaultAPIBaseForHub(runtime.HubURL)),
	)
	if result.APIBase == "" {
		return WrapOnboardingError(OnboardingStepBind, errors.New("bind response missing supported api_base"))
	}
	result.Handle = strings.TrimSpace(result.Handle)
	s.setHubBaseURL(result.APIBase)
	s.setRuntimeEndpoints(runtimeEndpoints)
	if strings.TrimSpace(result.Handle) != "" {
		agentProfile.Handle = strings.TrimSpace(result.Handle)
	}

	if err := s.storeConnectedSession(runtime, Session{
		BoundAt:         time.Now().UTC(),
		APIBase:         result.APIBase,
		AgentToken:      result.AgentToken,
		BaseURL:         result.APIBase,
		BindToken:       result.AgentToken,
		AgentUUID:       result.AgentUUID,
		AgentURI:        result.AgentURI,
		Handle:          agentProfile.Handle,
		HandleFinalized: handleRequestedDuringBind,
		DisplayName:     agentProfile.DisplayName,
		Emoji:           agentProfile.Emoji,
		ProfileBio:      agentProfile.ProfileMarkdown,
		ManifestURL:     runtimeEndpoints.ManifestURL,
		MetadataURL:     runtimeEndpoints.MetadataURL,
		Capabilities:    runtimeEndpoints.CapabilitiesURL,
		OpenClawPullURL: runtimeEndpoints.OpenClawPullURL,
		OpenClawPushURL: runtimeEndpoints.OpenClawPushURL,
		OfflineURL:      runtimeEndpoints.OpenClawOfflineURL,
	}); err != nil {
		return WrapOnboardingError(OnboardingStepBind, err)
	}
	s.refreshConfiguredState()
	if _, err := s.hub.GetCapabilities(ctx, result.AgentToken); err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		return WrapOnboardingError(OnboardingStepWorkBind, fmt.Errorf("agent bound, but credential verification failed: %w", err))
	}
	s.noteHubInteraction(nil, ConnectionTransportHTTP)

	registrationProfile := agentProfile
	if !handleRequestedDuringBind {
		registrationProfile.Handle = ""
	}
	if err := s.updateAgentProfile(ctx, result.AgentToken, registrationProfile); err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		return WrapOnboardingError(OnboardingStepProfileSet, fmt.Errorf("agent bound, but profile registration failed: %w", err))
	}
	if _, err := s.hub.GetCapabilities(ctx, result.AgentToken); err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		return WrapOnboardingError(OnboardingStepWorkActivate, fmt.Errorf("agent bound and profile registered, but activation check failed: %w", err))
	}
	s.noteHubInteraction(nil, ConnectionTransportHTTP)

	if err := s.logEvent("info", "Agent bound", fmt.Sprintf("Bound handle %q against %s", result.Handle, result.APIBase), "", ""); err != nil {
		return WrapOnboardingError(OnboardingStepWorkActivate, err)
	}
	s.presenceSynced = true
	s.presenceTransport = ConnectionTransportHTTP
	return nil
}

func (s *Service) connectExistingAgent(ctx context.Context, runtime HubRuntime, agentToken string, profile AgentProfile) error {
	agentToken = strings.TrimSpace(agentToken)
	if agentToken == "" {
		return WrapOnboardingError(OnboardingStepBind, errors.New("agent token is required"))
	}

	apiBase := NormalizeHubEndpointURL(defaultAPIBaseForHub(runtime.HubURL))
	if apiBase == "" {
		return WrapOnboardingError(OnboardingStepBind, fmt.Errorf("runtime config missing supported api_base for %q", runtime.HubURL))
	}

	s.setHubBaseURL(apiBase)
	s.setRuntimeEndpoints(hub.RuntimeEndpoints{})

	capabilities, err := s.hub.GetCapabilities(ctx, agentToken)
	if err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		return WrapOnboardingError(OnboardingStepWorkBind, fmt.Errorf("existing agent credential verification failed: %w", err))
	}
	s.noteHubInteraction(nil, ConnectionTransportHTTP)

	identity := existingAgentIdentityFromCapabilities(capabilities)
	if err := s.storeConnectedSession(runtime, Session{
		BoundAt:         time.Now().UTC(),
		APIBase:         apiBase,
		AgentToken:      agentToken,
		BaseURL:         apiBase,
		BindToken:       agentToken,
		AgentUUID:       identity.AgentUUID,
		AgentURI:        identity.AgentURI,
		Handle:          identity.Handle,
		HandleFinalized: identity.Handle != "",
		DisplayName:     coalesceTrimmed(profile.DisplayName, identity.DisplayName),
		Emoji:           coalesceTrimmed(profile.Emoji, identity.Emoji),
		ProfileBio:      coalesceTrimmed(profile.ProfileMarkdown, identity.ProfileMarkdown),
	}); err != nil {
		return WrapOnboardingError(OnboardingStepBind, err)
	}

	updatedState := s.refreshConfiguredState()

	if err := s.updateAgentProfile(ctx, agentToken, profile); err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		return WrapOnboardingError(OnboardingStepProfileSet, fmt.Errorf("existing agent verified, but profile registration failed: %w", err))
	}
	if _, err := s.hub.GetCapabilities(ctx, agentToken); err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		return WrapOnboardingError(OnboardingStepWorkActivate, fmt.Errorf("existing agent connected and profile registered, but activation check failed: %w", err))
	}
	s.noteHubInteraction(nil, ConnectionTransportHTTP)

	if err := s.logEvent("info", "Existing agent connected", fmt.Sprintf("Connected handle %q against %s", updatedState.Session.Handle, apiBase), "", ""); err != nil {
		return WrapOnboardingError(OnboardingStepWorkActivate, err)
	}
	s.presenceSynced = true
	s.presenceTransport = ConnectionTransportHTTP
	return nil
}

func (s *Service) UpdateAgentProfile(ctx context.Context, profile AgentProfile) error {
	state := s.store.Snapshot()
	if strings.TrimSpace(state.Session.AgentToken) == "" {
		return errors.New("agent is not bound yet")
	}
	s.syncHubClient(state)

	normalized := normalizeAgentProfile(profile)
	if normalized.Handle == "" {
		normalized.Handle = strings.TrimSpace(state.Session.Handle)
	}
	current := strings.TrimSpace(state.Session.Handle)
	if state.Session.HandleFinalized && current != "" && normalized.Handle != current {
		return fmt.Errorf("bound handle is immutable in this console: %s", current)
	}
	finalizingHandle := !state.Session.HandleFinalized && normalized.Handle != "" && normalized.Handle != current
	if !state.Session.HandleFinalized && !finalizingHandle && normalized.Handle == current {
		normalized.Handle = ""
	}

	if err := s.updateAgentProfile(ctx, state.Session.AgentToken, normalized); err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		return err
	}
	s.noteHubInteraction(nil, ConnectionTransportHTTP)
	return s.store.Update(func(current *AppState) error {
		if finalizingHandle {
			current.Session.Handle = normalized.Handle
			current.Session.HandleFinalized = true
		}
		current.Session.DisplayName = normalized.DisplayName
		current.Session.Emoji = normalized.Emoji
		current.Session.ProfileBio = normalized.ProfileMarkdown
		return nil
	})
}

func (s *Service) AddConnectedAgent(agent ConnectedAgent) error {
	agent = normalizeConnectedAgent(agent)
	if connectedAgentIdentityKey(agent) == "" {
		return errors.New("connected agent requires agent_id, handle, uri, or agent_uuid")
	}
	return s.store.Update(func(state *AppState) error {
		state.ConnectedAgents = AddOrReplaceConnectedAgent(state.ConnectedAgents, agent)
		return nil
	})
}

func (s *Service) RefreshConnectedAgents(ctx context.Context) ([]ConnectedAgent, error) {
	state := s.store.Snapshot()
	if strings.TrimSpace(state.Session.AgentToken) == "" {
		return state.ConnectedAgents, nil
	}
	s.syncHubClient(state)

	capabilities, err := s.hub.GetCapabilities(ctx, state.Session.AgentToken)
	if err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		return nil, fmt.Errorf("refresh connected agents from /v1/agents/me/capabilities: %w", err)
	}
	s.noteHubInteraction(nil, ConnectionTransportHTTP)

	agents := connectedAgentsFromCapabilities(capabilities, state)
	if err := s.store.Update(func(current *AppState) error {
		current.ConnectedAgents = agents
		return nil
	}); err != nil {
		return nil, err
	}
	return agents, nil
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

func (s *Service) DispatchFromUI(ctx context.Context, req DispatchRequest) (PendingTask, error) {
	state := s.store.Snapshot()
	if state.Session.AgentToken == "" {
		return PendingTask{}, errors.New("agent is not bound yet")
	}
	s.syncHubClient(state)

	target, req, err := s.prepareDispatchRequest(state, req)
	if err != nil {
		return PendingTask{}, err
	}

	task, publishReq := s.buildPendingTask(state, target, req, "", "")
	if err := s.writeTaskLog(task.LogPath, map[string]any{
		"phase":   PendingTaskStatusSending,
		"task_id": task.ID,
		"target":  target,
		"request": req,
	}); err != nil {
		return PendingTask{}, err
	}
	if err := s.store.Update(func(current *AppState) error {
		current.PendingTasks = append(current.PendingTasks, task)
		return nil
	}); err != nil {
		return PendingTask{}, err
	}

	if _, err := s.hub.PublishOpenClaw(ctx, state.Session.AgentToken, publishReq); err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		failureErr := s.failUIRequest(ctx, state, task, err)
		removeErr := s.removePendingTask(task.ChildRequestID)
		return PendingTask{}, errors.Join(failureErr, removeErr)
	}
	s.noteHubInteraction(nil, ConnectionTransportHTTP)
	task.Status = PendingTaskStatusInQueue
	if err := s.setPendingTaskStatus(task.ChildRequestID, task.Status); err != nil {
		return PendingTask{}, err
	}
	_ = s.logTaskEvent("info", "Task dispatched", fmt.Sprintf("Queued %s for %s", req.SkillName, connectedAgentNameOrRef(target)), task)
	return task, nil
}

func (s *Service) PollOnce(ctx context.Context) error {
	state := s.store.Snapshot()
	if state.Session.AgentToken == "" {
		return nil
	}
	s.syncHubClient(state)

	message, ok, err := s.hub.PullOpenClaw(ctx, state.Session.AgentToken, 25*time.Second)
	if err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTPLong)
		return err
	}
	s.noteHubInteraction(nil, ConnectionTransportHTTPLong)
	if !ok {
		return s.expirePendingTasks(ctx)
	}

	handleErr := s.handleInboundMessage(ctx, message)
	if handleErr != nil {
		_ = s.hub.NackOpenClaw(ctx, state.Session.AgentToken, message.DeliveryID)
		return handleErr
	}
	if err := s.hub.AckOpenClaw(ctx, state.Session.AgentToken, message.DeliveryID); err != nil {
		return err
	}
	return s.expirePendingTasks(ctx)
}

func (s *Service) RunHubLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		state := s.store.Snapshot()
		if strings.TrimSpace(state.Session.AgentToken) == "" {
			if !sleepWithContext(ctx, s.pollInterval()) {
				return
			}
			continue
		}
		s.syncHubClient(state)

		if err := s.waitForHubReachable(ctx); err != nil {
			return
		}

		state = s.store.Snapshot()
		if strings.TrimSpace(state.Session.AgentToken) == "" {
			continue
		}
		if realtime, ok := s.hub.(realtimeHubClient); ok {
			session, err := realtime.ConnectOpenClaw(ctx, state.Session.AgentToken, state.Settings.SessionKey)
			if err == nil {
				if err := s.ensurePresenceOnline(ctx, ConnectionTransportWebSocket); err != nil {
					_ = session.Close()
					if ctx.Err() != nil {
						return
					}
					if isUnauthorizedHubError(err) {
						return
					}
					if !sleepWithContext(ctx, s.pollInterval()) {
						return
					}
					continue
				}
				s.noteHubInteraction(nil, ConnectionTransportWebSocket)
				err = s.consumeRealtimeSession(ctx, session)
				if err == nil || ctx.Err() != nil {
					continue
				}
				if !shouldFallbackToLongPoll(err) {
					if !sleepWithContext(ctx, s.pollInterval()) {
						return
					}
					continue
				}
			}
			s.noteRealtimeFallback(err)
			if err := s.ensurePresenceOnline(ctx, ConnectionTransportHTTPLong); err != nil {
				if ctx.Err() != nil {
					return
				}
				if isUnauthorizedHubError(err) {
					return
				}
				if !sleepWithContext(ctx, s.pollInterval()) {
					return
				}
				continue
			}
			if err := s.runHTTPFallbackWindow(ctx); err != nil {
				return
			}
			continue
		}

		if err := s.ensurePresenceOnline(ctx, ConnectionTransportHTTPLong); err != nil {
			if ctx.Err() != nil {
				return
			}
			if isUnauthorizedHubError(err) {
				return
			}
			if !sleepWithContext(ctx, s.pollInterval()) {
				return
			}
			continue
		}
		if err := s.pollOnceWithTimeout(ctx); err != nil {
			if ctx.Err() != nil || isUnauthorizedHubError(err) {
				return
			}
			if !sleepWithContext(ctx, s.pollInterval()) {
				return
			}
			continue
		}
		if !sleepWithContext(ctx, s.pollInterval()) {
			return
		}
	}
}

func (s *Service) pollOnceWithTimeout(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	pollCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	if err := s.PollOnce(pollCtx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return ctx.Err()
}

func (s *Service) runHTTPFallbackWindow(ctx context.Context) error {
	window := s.wsFallbackWindow
	if window <= 0 {
		window = wsFallbackWindow
	}
	deadline := time.Now().Add(window)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.pollOnceWithTimeout(ctx); err != nil {
			if isUnauthorizedHubError(err) {
				return err
			}
			if !sleepWithContext(ctx, s.pollInterval()) {
				return ctx.Err()
			}
			continue
		}
		if time.Now().After(deadline) {
			return nil
		}
		if !sleepWithContext(ctx, s.pollInterval()) {
			return ctx.Err()
		}
	}
}

func (s *Service) waitForHubReachable(ctx context.Context) error {
	pinger, ok := s.hub.(hubPingClient)
	if !ok {
		return nil
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		timeout := s.hubPingCheckTimeout
		if timeout <= 0 {
			timeout = hubPingRequestTimeout
		}
		pingCtx, cancel := context.WithTimeout(ctx, timeout)
		detail, err := pinger.CheckPing(pingCtx)
		cancel()
		if err == nil {
			snapshot := s.store.Snapshot()
			if strings.TrimSpace(snapshot.Connection.Status) != ConnectionStatusConnected {
				s.noteHubPingReachable(detail)
			}
			return nil
		}

		retryDelay := s.hubPingRetryDelay
		if retryDelay <= 0 {
			retryDelay = hubPingRetryInterval
		}
		s.noteHubPingRetrying(err, retryDelay)
		if !sleepWithContext(ctx, retryDelay) {
			return ctx.Err()
		}
	}
}

func (s *Service) noteHubPingRetrying(err error, retryDelay time.Duration) {
	now := time.Now().UTC()
	_ = s.store.Update(func(state *AppState) error {
		baseURL, domain := hubConnectionTarget(state.Session.APIBase, state.Settings.HubURL)
		state.Connection = ConnectionState{
			Status:        ConnectionStatusDisconnected,
			Transport:     ConnectionTransportRetrying,
			LastChangedAt: now,
			Error:         strings.TrimSpace(err.Error()),
			Detail:        hubPingFailureDetail(err, retryDelay),
			BaseURL:       baseURL,
			Domain:        domain,
		}
		return nil
	})
}

func (s *Service) noteHubPingReachable(detail string) {
	now := time.Now().UTC()
	_ = s.store.Update(func(state *AppState) error {
		baseURL, domain := hubConnectionTarget(state.Session.APIBase, state.Settings.HubURL)
		state.Connection = ConnectionState{
			Status:        ConnectionStatusDisconnected,
			Transport:     ConnectionTransportReachable,
			LastChangedAt: now,
			Detail:        strings.TrimSpace(detail),
			BaseURL:       baseURL,
			Domain:        domain,
		}
		state.Session.OfflineMarked = false
		return nil
	})
}

func (s *Service) noteRealtimeFallback(err error) {
	now := time.Now().UTC()
	_ = s.store.Update(func(state *AppState) error {
		baseURL, domain := hubConnectionTarget(state.Session.APIBase, state.Settings.HubURL)
		detail := "WebSocket unavailable; falling back to HTTP long polling."
		if err != nil {
			detail = fmt.Sprintf("%s Error: %s", detail, strings.TrimSpace(err.Error()))
		}
		state.Connection = ConnectionState{
			Status:        ConnectionStatusDisconnected,
			Transport:     ConnectionTransportReachable,
			LastChangedAt: now,
			Error:         strings.TrimSpace(errorString(err)),
			Detail:        detail,
			BaseURL:       baseURL,
			Domain:        domain,
		}
		state.Session.OfflineMarked = false
		return nil
	})
}

func (s *Service) syncPresenceTransport(ctx context.Context, transport string) error {
	transport = normalizePresenceTransport(transport)
	if s.presenceSynced && s.presenceTransport == transport {
		return nil
	}
	return s.MarkOnline(ctx, transport)
}

func (s *Service) MarkOffline(ctx context.Context, reason string) error {
	state := s.store.Snapshot()
	if state.Session.AgentToken == "" || state.Session.OfflineMarked {
		return nil
	}
	s.syncHubClient(state)
	if err := s.hub.MarkOffline(ctx, state.Session.AgentToken, hub.OfflineRequest{
		SessionKey: state.Settings.SessionKey,
		Reason:     reason,
	}); err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		return err
	}
	s.presenceSynced = false
	s.presenceTransport = ""
	return s.store.Update(func(current *AppState) error {
		baseURL, domain := hubConnectionTarget(current.Session.APIBase, current.Settings.HubURL)
		current.Session.OfflineMarked = true
		current.Connection = ConnectionState{
			Status:        ConnectionStatusDisconnected,
			Transport:     ConnectionTransportOffline,
			LastChangedAt: time.Now().UTC(),
			Error:         strings.TrimSpace(reason),
			Detail:        strings.TrimSpace(reason),
			BaseURL:       baseURL,
			Domain:        domain,
		}
		return nil
	})
}

func (s *Service) MarkOnline(ctx context.Context, transport string) error {
	state := s.store.Snapshot()
	if strings.TrimSpace(state.Session.AgentToken) == "" {
		return nil
	}
	normalizedTransport := normalizePresenceTransport(transport)
	s.syncHubClient(state)
	profile := AgentProfile{
		DisplayName:     state.Session.DisplayName,
		Emoji:           state.Session.Emoji,
		ProfileMarkdown: state.Session.ProfileBio,
	}
	_, err := s.hub.UpdateMetadata(ctx, state.Session.AgentToken, hub.UpdateMetadataRequest{
		Metadata: buildAgentMetadata(profile, state.Settings.SessionKey, normalizedTransport),
	})
	if err != nil {
		s.noteHubInteraction(err, normalizedTransport)
		return err
	}
	s.noteHubInteraction(nil, normalizedTransport)
	s.presenceSynced = true
	s.presenceTransport = normalizedTransport
	return nil
}

func (s *Service) ensurePresenceOnline(ctx context.Context, transport string) error {
	normalizedTransport := normalizePresenceTransport(transport)
	if s.presenceSynced && s.presenceTransport == normalizedTransport {
		return nil
	}
	return s.MarkOnline(ctx, normalizedTransport)
}

func (s *Service) handleInboundMessage(ctx context.Context, message hub.PullResponse) error {
	messageType := strings.TrimSpace(message.OpenClawMessage.Type)
	if messageType == "" {
		messageType = strings.TrimSpace(message.OpenClawMessage.Kind)
	}
	switch messageType {
	case openClawSkillResult:
		return s.handleSkillResult(ctx, message)
	case openClawSkillRequest:
		return s.handleSkillRequest(ctx, message)
	default:
		return s.logEvent("info", "Ignored message", "Received unsupported message type "+messageType, "", "")
	}
}

func (s *Service) handleSkillRequest(ctx context.Context, message hub.PullResponse) error {
	state := s.store.Snapshot()
	callerAgentUUID, callerAgentURI := callerTargetFromMessage(message)
	var payload dispatchPayload
	rawDispatchPayload := message.OpenClawMessage.Payload
	if rawDispatchPayload == nil {
		rawDispatchPayload = message.OpenClawMessage.Input
	}
	if err := payload.FromAny(rawDispatchPayload); err != nil {
		pending := PendingTask{
			ID:              NewID("task"),
			ParentRequestID: message.OpenClawMessage.RequestID,
			CallerAgentUUID: callerAgentUUID,
			CallerAgentURI:  callerAgentURI,
			CallerRequestID: message.OpenClawMessage.RequestID,
			LogPath:         filepath.Join(s.settings.DataDir, "logs", NewID("task")+".log"),
		}
		return s.handleTaskFailure(ctx, state, pending, failureFromError("Failed to decode the dispatch request payload.", fmt.Errorf("decode dispatch payload: %w", err)))
	}

	req := DispatchRequest{
		RequestID:      message.OpenClawMessage.RequestID,
		TargetAgentRef: payload.TargetAgentRef(),
		SkillName:      payload.RequestedSkillName(),
		Repo:           payload.Repo,
		LogPaths:       payload.LogPaths,
		Payload:        payload.TaskPayload(),
		PayloadFormat:  payload.PayloadFormat,
	}
	target, req, err := s.prepareDispatchRequest(state, req)
	if err != nil {
		pending := PendingTask{
			ID:                NewID("task"),
			ParentRequestID:   message.OpenClawMessage.RequestID,
			CallerAgentUUID:   callerAgentUUID,
			CallerAgentURI:    callerAgentURI,
			CallerRequestID:   message.OpenClawMessage.RequestID,
			OriginalSkillName: req.SkillName,
			Repo:              req.Repo,
			LogPath:           filepath.Join(s.settings.DataDir, "logs", NewID("task")+".log"),
			DispatchPayload:   normalizePayload(req.Payload, req.Repo, req.LogPaths),
		}
		return s.handleTaskFailure(ctx, state, pending, failureFromError("Task dispatch failed before it reached a connected agent.", err))
	}

	task, publishReq := s.buildPendingTask(state, target, req, callerAgentUUID, callerAgentURI)
	if err := s.writeTaskLog(task.LogPath, map[string]any{
		"phase":          PendingTaskStatusSending,
		"received_from":  message.FromAgentUUID,
		"received_skill": message.OpenClawMessage.SkillName,
		"task_id":        task.ID,
		"request":        req,
	}); err != nil {
		return err
	}
	if err := s.store.Update(func(current *AppState) error {
		current.PendingTasks = append(current.PendingTasks, task)
		return nil
	}); err != nil {
		return err
	}

	if _, err := s.hub.PublishOpenClaw(ctx, state.Session.AgentToken, publishReq); err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		failureErr := s.handleTaskFailure(ctx, state, task, failureFromError("Task dispatch failed before it reached a connected agent.", err))
		removeErr := s.removePendingTask(task.ChildRequestID)
		return errors.Join(failureErr, removeErr)
	}
	s.noteHubInteraction(nil, ConnectionTransportHTTP)
	if err := s.setPendingTaskStatus(task.ChildRequestID, PendingTaskStatusInQueue); err != nil {
		return err
	}
	return s.logTaskEvent("info", "Forwarded request", fmt.Sprintf("Forwarded %s to %s", req.SkillName, connectedAgentNameOrRef(target)), task)
}

func (s *Service) handleSkillResult(ctx context.Context, message hub.PullResponse) error {
	state := s.store.Snapshot()
	pending, ok := FindPendingTask(state.PendingTasks, message.OpenClawMessage.RequestID)
	if !ok {
		return s.logEvent("info", "Unmatched skill result", "No pending task matched "+message.OpenClawMessage.RequestID, "", "")
	}

	if err := s.writeTaskLog(pending.LogPath, map[string]any{
		"phase":   "completed",
		"message": message,
	}); err != nil {
		return err
	}

	if !messageSucceeded(message.OpenClawMessage) {
		return s.handleExecutionFailure(ctx, state, pending, failureFromMessage(message.OpenClawMessage))
	}

	if hasCallerTarget(pending) {
		if err := s.publishResultToCaller(ctx, state, pending, message.OpenClawMessage); err != nil {
			return err
		}
	}
	return s.removePendingTask(pending.ChildRequestID)
}

func (s *Service) expirePendingTasks(ctx context.Context) error {
	state := s.store.Snapshot()
	if len(state.PendingTasks) == 0 {
		return nil
	}

	now := time.Now()
	for _, pending := range state.PendingTasks {
		if pending.ExpiresAt.After(now) {
			continue
		}
		err := fmt.Errorf("task timed out waiting for %s", pending.OriginalSkillName)
		report := failureFromError("Task failed because the downstream agent did not reply before the timeout.", err)
		report.Detail = map[string]any{"timeout": true}
		if err := s.handleExecutionFailure(ctx, state, pending, report); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) handleExecutionFailure(ctx context.Context, state AppState, pending PendingTask, report failureReport) error {
	return s.finalizeTaskFailure(ctx, state, pending, report)
}

func (s *Service) finalizeTaskFailure(ctx context.Context, state AppState, pending PendingTask, report failureReport) error {
	failureErr := s.handleTaskFailure(ctx, state, pending, report)
	removeErr := s.removePendingTask(pending.ChildRequestID)
	return errors.Join(failureErr, removeErr)
}

func (s *Service) removePendingTask(childRequestID string) error {
	childRequestID = strings.TrimSpace(childRequestID)
	if childRequestID == "" {
		return nil
	}
	return s.store.Update(func(current *AppState) error {
		current.PendingTasks = RemovePendingTask(current.PendingTasks, childRequestID)
		return nil
	})
}

func (s *Service) setPendingTaskStatus(childRequestID, status string) error {
	childRequestID = strings.TrimSpace(childRequestID)
	status = normalizePendingTaskStatus(status)
	if childRequestID == "" || status == "" {
		return nil
	}
	return s.store.Update(func(current *AppState) error {
		for i := range current.PendingTasks {
			if current.PendingTasks[i].ChildRequestID == childRequestID {
				current.PendingTasks[i].Status = status
				return nil
			}
		}
		return nil
	})
}

func (s *Service) publishFailureToCaller(ctx context.Context, state AppState, pending PendingTask, report failureReport) error {
	s.syncHubClient(state)
	if pending.LogPath == "" {
		pending.LogPath = filepath.Join(s.settings.DataDir, "logs", pending.ID+".log")
	}
	logPaths := failureLogPaths(pending)
	if err := s.writeTaskLog(pending.LogPath, map[string]any{
		"phase":  "failed",
		"error":  report.Error,
		"detail": report.Detail,
	}); err != nil {
		_ = s.logEvent("error", "Task failure log write failed", err.Error(), pending.ID, pending.LogPath)
	}

	failurePayload := callerFailurePayload(report, logPaths)
	errorDetail := failurePayload["error_detail"]

	message := hub.OpenClawMessage{
		Protocol:      openClawHTTPProtocol,
		Type:          openClawSkillResult,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		SkillName:     pending.OriginalSkillName,
		RequestID:     pending.ParentRequestID,
		ReplyTo:       pending.CallerRequestID,
		PayloadFormat: "json",
		Payload:       failurePayload,
		Error:         callerFailureError(report),
		ErrorDetail:   errorDetail,
		OK:            boolPtr(false),
		Status:        "failed",
	}
	_, err := s.hub.PublishOpenClaw(ctx, state.Session.AgentToken, hub.PublishRequest{
		ToAgentUUID: pending.CallerAgentUUID,
		ToAgentURI:  pending.CallerAgentURI,
		ClientMsgID: NewID("result"),
		Message:     message,
	})
	s.noteHubInteraction(err, ConnectionTransportHTTP)
	return err
}

func (s *Service) publishResultToCaller(ctx context.Context, state AppState, pending PendingTask, result hub.OpenClawMessage) error {
	s.syncHubClient(state)
	forwarded := result
	forwarded.ReplyTo = pending.CallerRequestID
	forwarded.RequestID = pending.ParentRequestID
	_, err := s.hub.PublishOpenClaw(ctx, state.Session.AgentToken, hub.PublishRequest{
		ToAgentUUID: pending.CallerAgentUUID,
		ToAgentURI:  pending.CallerAgentURI,
		ClientMsgID: NewID("result"),
		Message:     forwarded,
	})
	s.noteHubInteraction(err, ConnectionTransportHTTP)
	return err
}

func (s *Service) buildPendingTask(state AppState, target ConnectedAgent, req DispatchRequest, callerAgentUUID, callerAgentURI string) (PendingTask, hub.PublishRequest) {
	now := time.Now().UTC()
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = state.Settings.TaskTimeout
	}
	childRequestID := NewID("dispatch")
	taskID := NewID("task")
	logPath := filepath.Join(state.Settings.DataDir, "logs", taskID+".log")

	payload := normalizePayload(req.Payload, req.Repo, req.LogPaths)
	var outboundPayload any
	if payload != nil {
		outboundPayload = payload
	}
	payloadFormat := normalizePayloadFormat(req.PayloadFormat, outboundPayload)
	task := PendingTask{
		ID:                     taskID,
		Status:                 PendingTaskStatusSending,
		ParentRequestID:        req.RequestID,
		ChildRequestID:         childRequestID,
		OriginalSkillName:      req.SkillName,
		TargetAgentDisplayName: connectedAgentDisplayName(target),
		TargetAgentEmoji:       coalesceTrimmed(connectedAgentEmoji(target), "🙂"),
		TargetAgentUUID:        target.AgentUUID,
		TargetAgentURI:         target.URI,
		CallerAgentUUID:        callerAgentUUID,
		CallerAgentURI:         callerAgentURI,
		CallerRequestID:        req.RequestID,
		Repo:                   req.Repo,
		LogPath:                logPath,
		CreatedAt:              now,
		ExpiresAt:              now.Add(timeout),
		DispatchPayload:        payload,
		DispatchPayloadFormat:  payloadFormat,
	}

	message := newSkillRequestMessage(
		now,
		req.SkillName,
		outboundPayload,
		payloadFormat,
		childRequestID,
		req.RequestID,
	)

	return task, hub.PublishRequest{
		ToAgentUUID: target.AgentUUID,
		ToAgentURI:  target.URI,
		ClientMsgID: childRequestID,
		Message:     message,
	}
}

func boolPtr(value bool) *bool {
	return &value
}

func normalizedFlashLevel(level string) string {
	if strings.EqualFold(strings.TrimSpace(level), "error") {
		return "error"
	}
	return "info"
}

func (s *Service) updateAgentProfile(ctx context.Context, token string, profile AgentProfile) error {
	if _, err := s.hub.UpdateMetadata(ctx, token, hub.UpdateMetadataRequest{
		Handle:   profile.Handle,
		Metadata: buildAgentMetadata(profile, s.settings.SessionKey, ConnectionTransportHTTP),
	}); err != nil {
		return err
	}
	return nil
}

func (s *Service) resolveDispatchTarget(state AppState, req DispatchRequest) (ConnectedAgent, error) {
	targetRef := strings.TrimSpace(req.TargetAgentRef)
	if targetRef != "" {
		if agent, ok := FindConnectedAgent(state.ConnectedAgents, targetRef); ok {
			return agent, nil
		}
		for _, agent := range state.ConnectedAgents {
			if strings.EqualFold(agent.AgentUUID, targetRef) || strings.EqualFold(agent.URI, targetRef) {
				return agent, nil
			}
		}
		if strings.HasPrefix(targetRef, "molten://") {
			return ConnectedAgent{URI: targetRef}, nil
		}
		return ConnectedAgent{}, fmt.Errorf("no connected agent matched %q", targetRef)
	}

	skillName := strings.TrimSpace(req.SkillName)
	if skillName == "" {
		return ConnectedAgent{}, errors.New(DispatchSelectionRequiredMessage)
	}

	for _, agent := range state.ConnectedAgents {
		if connectedAgentSupportsSkill(agent, skillName) {
			return agent, nil
		}
	}
	return ConnectedAgent{}, fmt.Errorf("no connected agent advertises skill %q", skillName)
}

func (s *Service) prepareDispatchRequest(state AppState, req DispatchRequest) (ConnectedAgent, DispatchRequest, error) {
	target, err := s.resolveDispatchTarget(state, req)
	if err != nil {
		return ConnectedAgent{}, req, err
	}

	skillName, err := resolveDispatchSkillName(target, req.SkillName)
	if err != nil {
		return ConnectedAgent{}, req, err
	}
	req.SkillName = skillName
	return target, req, nil
}

func resolveDispatchSkillName(target ConnectedAgent, skillName string) (string, error) {
	skillName = strings.TrimSpace(skillName)
	if skillName != "" {
		return skillName, nil
	}

	var inferred string
	for _, skill := range connectedAgentSkills(target) {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			continue
		}
		if inferred == "" {
			inferred = name
			continue
		}
		if inferred != name {
			return "", fmt.Errorf("skill_name is required for %q because Molten Hub does not expose a default skill", connectedAgentNameOrRef(target))
		}
	}
	if inferred != "" {
		return inferred, nil
	}

	return "", fmt.Errorf("skill_name is required for %q because Molten Hub does not expose a default skill", connectedAgentNameOrRef(target))
}

func (s *Service) failUIRequest(ctx context.Context, state AppState, task PendingTask, cause error) error {
	report := failureFromError("Task failed before it reached the connected agent.", cause)
	if err := s.writeTaskLog(task.LogPath, map[string]any{
		"phase":  "dispatch_failed",
		"error":  report.Error,
		"detail": report.Detail,
	}); err != nil {
		return fmt.Errorf("%w; task log write failed: %v", cause, err)
	}
	if err := s.handleTaskFailure(ctx, state, task, report); err != nil {
		return fmt.Errorf("%w; failure handling failed: %v", cause, err)
	}
	return cause
}

func (s *Service) handleTaskFailure(ctx context.Context, state AppState, pending PendingTask, report failureReport) error {
	var combinedErr error
	if pending.CallerAgentUUID != "" || pending.CallerAgentURI != "" {
		if err := s.publishFailureToCaller(ctx, state, pending, report); err != nil {
			combinedErr = errors.Join(combinedErr, fmt.Errorf("publish failure response: %w", err))
		}
	}
	s.tryMarkTaskFailureOffline(ctx, pending, report)
	return combinedErr
}

func (s *Service) writeTaskLog(path string, payload any) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create task log directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open task log: %w", err)
	}
	defer file.Close()

	entry := map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"payload":   payload,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode task log entry: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write task log entry: %w", err)
	}
	return nil
}

func (s *Service) logEvent(level, title, detail, taskID, logPath string) error {
	return s.store.AppendEvent(RuntimeEvent{
		At:      time.Now().UTC(),
		Level:   level,
		Title:   title,
		Detail:  detail,
		TaskID:  taskID,
		LogPath: logPath,
	})
}

func (s *Service) logTaskEvent(level, title, detail string, task PendingTask) error {
	return s.store.AppendEvent(RuntimeEvent{
		At:                     time.Now().UTC(),
		Level:                  level,
		Title:                  title,
		Detail:                 detail,
		TaskID:                 task.ID,
		LogPath:                task.LogPath,
		OriginalSkillName:      task.OriginalSkillName,
		TargetAgentDisplayName: task.TargetAgentDisplayName,
		TargetAgentEmoji:       coalesceTrimmed(task.TargetAgentEmoji, "🤖"),
		TargetAgentUUID:        task.TargetAgentUUID,
		TargetAgentURI:         task.TargetAgentURI,
	})
}

type dispatchPayload struct {
	AgentRef        string   `json:"target_agent_ref"`
	TargetAgentUUID string   `json:"target_agent_uuid"`
	TargetAgentURI  string   `json:"target_agent_uri"`
	SkillName       string   `json:"skill_name"`
	SelectedTask    string   `json:"selected_task"`
	Repo            string   `json:"repo"`
	LogPaths        []string `json:"log_paths"`
	Payload         any      `json:"payload"`
	PayloadFormat   string   `json:"payload_format"`
	raw             map[string]any
}

func (p *dispatchPayload) FromAny(value any) error {
	if value == nil {
		*p = dispatchPayload{}
		return nil
	}
	switch typed := value.(type) {
	case string:
		return p.fromJSONString(typed)
	case []byte:
		return p.fromJSONString(string(typed))
	case json.RawMessage:
		return p.fromJSONBytes(typed)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return p.fromJSONBytes(data)
}

func (p *dispatchPayload) fromJSONString(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		*p = dispatchPayload{}
		return nil
	}
	return p.fromJSONBytes([]byte(raw))
}

func (p *dispatchPayload) fromJSONBytes(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("dispatch payload must be a JSON object: %w", err)
	}
	p.fromMap(raw)
	return nil
}

func (p *dispatchPayload) fromMap(raw map[string]any) {
	*p = dispatchPayload{
		AgentRef: stringFromMap(
			raw,
			"target_agent_ref", "targetAgentRef",
			"selected_agent_ref", "selectedAgentRef",
			"agent_ref", "agentRef",
		),
		TargetAgentUUID: stringFromMap(
			raw,
			"target_agent_uuid", "targetAgentUUID", "targetAgentUuid",
			"selected_agent_uuid", "selectedAgentUUID", "selectedAgentUuid",
		),
		TargetAgentURI: stringFromMap(
			raw,
			"target_agent_uri", "targetAgentURI", "targetAgentUri",
			"selected_agent_uri", "selectedAgentURI", "selectedAgentUri",
		),
		SkillName: stringFromMap(
			raw,
			"skill_name", "skillName",
			"selected_skill", "selectedSkill",
			"selected_skill_name", "selectedSkillName",
		),
		SelectedTask: stringFromMap(
			raw,
			"selected_task", "selectedTask",
			"task_name", "taskName",
			"task",
		),
		Repo:          stringFromMap(raw, "repo"),
		LogPaths:      support.StringSliceFromAny(firstMapValue(raw, "log_paths", "logPaths")),
		Payload:       firstMapValue(raw, "payload"),
		PayloadFormat: stringFromMap(raw, "payload_format", "payloadFormat"),
		raw:           raw,
	}
}

func firstMapValue(values map[string]any, keys ...string) any {
	if values == nil {
		return nil
	}
	for _, key := range keys {
		value, ok := values[key]
		if ok {
			return value
		}
	}
	return nil
}

func (p dispatchPayload) TargetAgentRef() string {
	return support.FirstNonEmptyString(p.AgentRef, p.TargetAgentUUID, p.TargetAgentURI)
}

func (p dispatchPayload) RequestedSkillName() string {
	return support.FirstNonEmptyString(p.SkillName, p.SelectedTask)
}

func (p dispatchPayload) TaskPayload() any {
	if p.Payload != nil {
		return p.Payload
	}
	if len(p.raw) == 0 {
		return nil
	}

	inline := make(map[string]any)
	for key, value := range p.raw {
		if dispatchPayloadControlField(key) {
			continue
		}
		inline[key] = value
	}
	if len(inline) == 0 {
		return nil
	}
	return inline
}

func dispatchPayloadControlField(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "target_agent_ref", "targetagentref",
		"selected_agent_ref", "selectedagentref",
		"agent_ref", "agentref",
		"target_agent_uuid", "targetagentuuid",
		"selected_agent_uuid", "selectedagentuuid",
		"target_agent_uri", "targetagenturi",
		"selected_agent_uri", "selectedagenturi",
		"skill_name", "skillname",
		"selected_skill", "selectedskill",
		"selected_skill_name", "selectedskillname",
		"selected_task", "selectedtask",
		"task_name", "taskname",
		"task",
		"repo",
		"log_paths", "logpaths",
		"payload",
		"payload_format", "payloadformat":
		return true
	default:
		return false
	}
}

func normalizePayload(payload any, repo string, logPaths []string) map[string]any {
	if payload == nil && repo == "" && len(logPaths) == 0 {
		return nil
	}
	switch typed := payload.(type) {
	case map[string]any:
		if repo != "" {
			typed["repo"] = repo
		}
		if len(logPaths) > 0 {
			typed["log_paths"] = support.CompactStrings(logPaths)
		}
		return typed
	default:
		result := map[string]any{"input": typed}
		if repo != "" {
			result["repo"] = repo
		}
		if len(logPaths) > 0 {
			result["log_paths"] = support.CompactStrings(logPaths)
		}
		return result
	}
}

func normalizePayloadFormat(format string, payload any) string {
	if payload == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		return "json"
	case "markdown", "md", "text", "text/plain", "plaintext":
		return "markdown"
	case "":
		if _, ok := payload.(string); ok {
			return "markdown"
		}
		return "json"
	default:
		if _, ok := payload.(string); ok {
			return "markdown"
		}
		return "json"
	}
}

func messageSucceeded(message hub.OpenClawMessage) bool {
	if message.OK != nil {
		return *message.OK
	}

	if failed, known := statusIndicatesFailure(message.Status); known {
		return !failed
	}
	if strings.TrimSpace(message.Error) != "" {
		return false
	}

	payloadMap, ok := message.Payload.(map[string]any)
	if ok {
		if payloadMapIndicatesFailure(payloadMap) {
			return false
		}
		if payloadMapIndicatesSuccess(payloadMap) {
			return true
		}
	} else if payloadText, ok := message.Payload.(string); ok && payloadStringIndicatesFailure(payloadText) {
		return false
	}

	return true
}

func failureFromError(message string, err error) failureReport {
	report := failureReport{
		Message: strings.TrimSpace(message),
		Error:   "task failed",
	}
	if err != nil {
		report.Error = err.Error()
	}
	if report.Message == "" {
		report.Message = "Task failed while dispatching to a connected agent."
	}
	report.Detail = errorDetail(err)
	var apiErr *hub.APIError
	if errors.As(err, &apiErr) {
		report.Retryable = apiErr.Retryable
		report.NextAction = strings.TrimSpace(apiErr.NextAction)
	}
	return report
}

func failureFromMessage(message hub.OpenClawMessage) failureReport {
	payloadMap, _ := message.Payload.(map[string]any)
	report := failureReport{
		Message: "Task failed while dispatching to a connected agent.",
		Error:   strings.TrimSpace(message.Error),
		Detail:  message.ErrorDetail,
	}
	if payloadMessage := stringFromMap(payloadMap, "message"); payloadMessage != "" {
		report.Message = payloadMessage
	}
	if report.Error == "" {
		report.Error = payloadFailureError(payloadMap, message.Payload)
	}
	if detail := firstMapValue(payloadMap, "error_detail", "error_details"); detail != nil {
		report.Detail = detail
	}
	if report.Error == "" {
		report.Error = "downstream agent reported failure"
	}
	if retryable, ok := payloadMap["retryable"].(bool); ok {
		report.Retryable = retryable
	}
	if nextAction := stringFromMap(payloadMap, "next_action"); nextAction != "" {
		report.NextAction = nextAction
	}
	if report.Detail == nil {
		report.Detail = message.Payload
	}
	if report.Detail == nil {
		report.Detail = report.Error
	}
	return report
}

func formatFailureSummary(report failureReport) string {
	if failureDetailIsEmpty(report.Detail) {
		return report.Error
	}
	return fmt.Sprintf("%s | detail=%v", report.Error, report.Detail)
}

func failureFields(report failureReport, message string, detail any) map[string]any {
	return map[string]any{
		"status":       "failed",
		"message":      message,
		"error":        report.Error,
		"detail":       detail,
		"retryable":    report.Retryable,
		"next_action":  report.NextAction,
		"error_detail": detail,
	}
}

func callerFailurePayload(report failureReport, logPaths []string) map[string]any {
	detail := report.Detail
	if failureDetailIsEmpty(detail) {
		detail = report.Error
	}
	payload := failureFields(report, callerFailureError(report), detail)
	payload["ok"] = false
	payload["failure"] = true
	payload["error_details"] = detail
	payload["log_paths"] = logPaths
	return payload
}

func callerFailureError(report failureReport) string {
	summary := explicitFailureMessage(report.Message)
	errText := strings.TrimSpace(report.Error)
	switch {
	case summary == "" && errText == "":
		return "Task failed."
	case summary == "":
		return explicitFailureMessage(errText)
	case errText == "":
		return summary
	case strings.EqualFold(summary, errText):
		return summary
	default:
		separator := ". Error: "
		if strings.HasSuffix(summary, ".") || strings.HasSuffix(summary, "!") || strings.HasSuffix(summary, "?") {
			separator = " Error: "
		}
		return summary + separator + errText
	}
}

func explicitFailureMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "Task failed."
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "failed") {
		return message
	}
	return "Task failed: " + message
}

func payloadFailureError(payloadMap map[string]any, payload any) string {
	if value := stringFromMap(payloadMap, "error", "error_message", "stderr", "detail", "output"); value != "" {
		return value
	}
	if value := failureErrorString(firstMapValue(payloadMap, "error_detail", "error_details")); value != "" {
		return value
	}
	if value, ok := payload.(string); ok {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return ""
		}
		firstLine, _, _ := strings.Cut(trimmed, "\n")
		return strings.TrimSpace(firstLine)
	}
	return ""
}

func failureErrorString(detail any) string {
	switch typed := detail.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		return stringFromMap(typed, "error", "message", "stderr", "detail", "output")
	default:
		return ""
	}
}

func failureDetailIsEmpty(detail any) bool {
	if detail == nil {
		return true
	}
	value, ok := detail.(string)
	return ok && strings.TrimSpace(value) == ""
}

func errorDetail(err error) any {
	if err == nil {
		return nil
	}
	var apiErr *hub.APIError
	if errors.As(err, &apiErr) {
		detail := map[string]any{
			"status_code": apiErr.StatusCode,
			"error":       apiErr.Code,
			"message":     apiErr.Message,
			"retryable":   apiErr.Retryable,
			"next_action": apiErr.NextAction,
		}
		if apiErr.Detail != nil {
			detail["error_detail"] = apiErr.Detail
		}
		return detail
	}
	return err.Error()
}

func statusIndicatesFailure(status string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "":
		return false, false
	case "ok", "success", "succeeded", "completed", "complete", "done", "passed":
		return false, true
	case "failed", "failure", "error", "errored", "cancelled", "canceled", "timeout", "timed_out", "aborted", "crashed":
		return true, true
	default:
		return false, false
	}
}

func payloadMapIndicatesFailure(payload map[string]any) bool {
	if failed, known := statusIndicatesFailure(stringFromMap(payload, "status", "state", "result")); known {
		return failed
	}

	for _, key := range []string{"ok", "success", "succeeded", "completed"} {
		if value, ok := boolFromAny(payload[key]); ok && !value {
			return true
		}
	}
	for _, key := range []string{"failed", "failure", "error"} {
		if value, ok := boolFromAny(payload[key]); ok && value {
			return true
		}
	}
	for _, key := range []string{"error", "error_message", "stderr"} {
		if strings.TrimSpace(stringFromMap(payload, key)) != "" {
			return true
		}
	}
	for _, key := range []string{"exit_code", "exit_status"} {
		if code, ok := intFromAny(payload[key]); ok && code != 0 {
			return true
		}
	}
	if detail := firstMapValue(payload, "error_detail", "error_details"); !failureDetailIsEmpty(detail) {
		return true
	}
	if nested, ok := payload["failure"].(map[string]any); ok {
		if payloadMapIndicatesFailure(nested) {
			return true
		}
	}
	if output, ok := payload["output"].(string); ok && payloadStringIndicatesFailure(output) {
		return true
	}
	return false
}

func payloadMapIndicatesSuccess(payload map[string]any) bool {
	if failed, known := statusIndicatesFailure(stringFromMap(payload, "status", "state", "result")); known {
		return !failed
	}
	for _, key := range []string{"ok", "success", "succeeded", "completed"} {
		if value, ok := boolFromAny(payload[key]); ok {
			return value
		}
	}
	return false
}

func payloadStringIndicatesFailure(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return false
	}
	if strings.HasPrefix(normalized, "error") || strings.HasPrefix(normalized, "fatal") || strings.HasPrefix(normalized, "panic:") {
		return true
	}
	if strings.Contains(normalized, "check your internet connection") && strings.Contains(normalized, "githubstatus.com") {
		return true
	}
	return false
}

func normalizePendingTaskStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case PendingTaskStatusSending:
		return PendingTaskStatusSending
	case "", PendingTaskStatusInQueue:
		return PendingTaskStatusInQueue
	default:
		return PendingTaskStatusInQueue
	}
}

func boolFromAny(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "y":
			return true, true
		case "false", "0", "no", "n":
			return false, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

func intFromAny(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int8:
		return int64(typed), true
	case int16:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case float32:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func stringFromMap(values map[string]any, keys ...string) string {
	return support.StringFromMap(values, keys...)
}

func (s *Service) tryMarkTaskFailureOffline(ctx context.Context, pending PendingTask, report failureReport) {
	if err := s.MarkOffline(ctx, failureOfflineReason(pending, report)); err != nil {
		_ = s.logEvent("error", "Offline mark failed", err.Error(), pending.ID, pending.LogPath)
	}
}

func failureOfflineReason(pending PendingTask, report failureReport) string {
	parts := []string{"task failure"}
	if pending.ID != "" {
		parts = append(parts, "id="+pending.ID)
	}
	if pending.OriginalSkillName != "" {
		parts = append(parts, "skill="+pending.OriginalSkillName)
	}
	if report.Error != "" {
		parts = append(parts, "error="+report.Error)
	}
	return strings.Join(parts, " ")
}

func fallbackRepo(repo string) string {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "."
	}
	return repo
}

func failureLogPaths(pending PendingTask) []string {
	paths := support.StringSliceFromAny(pending.DispatchPayload["log_paths"])
	paths = append(paths, pending.LogPath)
	return support.CompactStrings(paths)
}

func connectedAgentNameOrRef(agent ConnectedAgent) string {
	return coalesceTrimmed(
		connectedAgentDisplayName(agent),
		connectedAgentSecondaryRef(agent),
	)
}

func connectedAgentDisplayName(agent ConnectedAgent) string {
	metadata := connectedAgentMetadata(agent)
	if metadata != nil && strings.TrimSpace(metadata.DisplayName) != "" {
		return strings.TrimSpace(metadata.DisplayName)
	}
	return coalesceTrimmed(agent.DisplayName, agent.Handle, agent.AgentID, agent.URI, agent.AgentUUID, "Unknown agent")
}

func connectedAgentSecondaryRef(agent ConnectedAgent) string {
	return coalesceTrimmed(agent.AgentID, agent.URI, agent.Handle, agent.AgentUUID)
}

func connectedAgentEmoji(agent ConnectedAgent) string {
	metadata := connectedAgentMetadata(agent)
	if metadata != nil && strings.TrimSpace(metadata.Emoji) != "" {
		return strings.TrimSpace(metadata.Emoji)
	}
	return strings.TrimSpace(agent.Emoji)
}

func connectedAgentPresenceStatus(agent ConnectedAgent) string {
	metadata := connectedAgentMetadata(agent)
	if metadata != nil && metadata.Presence != nil && strings.EqualFold(strings.TrimSpace(metadata.Presence.Status), "online") {
		return "online"
	}
	if agent.Presence != nil && strings.EqualFold(strings.TrimSpace(agent.Presence.Status), "online") {
		return "online"
	}
	if strings.EqualFold(strings.TrimSpace(agent.Status), "online") {
		return "online"
	}
	return "offline"
}

func connectedAgentSupportsSkill(agent ConnectedAgent, skillName string) bool {
	skillName = strings.TrimSpace(skillName)
	if skillName == "" {
		return false
	}
	for _, skill := range connectedAgentSkills(agent) {
		if strings.EqualFold(skill.Name, skillName) {
			return true
		}
	}
	return false
}

func connectedAgentSkills(agent ConnectedAgent) []Skill {
	metadata := connectedAgentMetadata(agent)
	if metadata != nil {
		for _, raw := range []any{metadata.Skills, metadata.AdvertisedSkills} {
			if skills := skillsFromAny(raw); len(skills) > 0 {
				return skills
			}
		}
	}
	for _, raw := range []any{agent.Skills, agent.AdvertisedSkills} {
		if skills := skillsFromAny(raw); len(skills) > 0 {
			return skills
		}
	}
	return nil
}

func connectedAgentRefs(agent ConnectedAgent) []string {
	refs := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for _, value := range []string{agent.AgentID, agent.Handle, agent.AgentUUID, agent.URI} {
		ref := strings.TrimSpace(value)
		if ref == "" {
			continue
		}
		key := strings.ToLower(ref)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}

func connectedAgentIdentityKey(agent ConnectedAgent) string {
	return strings.ToLower(coalesceTrimmed(agent.AgentUUID, agent.URI, agent.AgentID, agent.Handle))
}

func connectedAgentMetadata(agent ConnectedAgent) *hub.AgentMetadata {
	return agent.Metadata
}

func normalizeConnectedAgent(agent ConnectedAgent) ConnectedAgent {
	agent.AgentUUID = strings.TrimSpace(agent.AgentUUID)
	agent.AgentID = strings.TrimSpace(agent.AgentID)
	agent.URI = strings.TrimSpace(agent.URI)
	agent.Handle = strings.TrimSpace(agent.Handle)
	agent.Status = strings.TrimSpace(agent.Status)
	agent.DisplayName = strings.TrimSpace(agent.DisplayName)
	agent.Emoji = strings.TrimSpace(agent.Emoji)
	if agent.Presence != nil {
		presence := *agent.Presence
		presence.Status = strings.TrimSpace(presence.Status)
		presence.Transport = strings.TrimSpace(presence.Transport)
		presence.SessionKey = strings.TrimSpace(presence.SessionKey)
		presence.UpdatedAt = strings.TrimSpace(presence.UpdatedAt)
		agent.Presence = &presence
	}
	if agent.Metadata != nil {
		metadata := *agent.Metadata
		metadata.AgentType = strings.TrimSpace(metadata.AgentType)
		metadata.DisplayName = strings.TrimSpace(metadata.DisplayName)
		metadata.Emoji = strings.TrimSpace(metadata.Emoji)
		metadata.ProfileMarkdown = strings.TrimSpace(metadata.ProfileMarkdown)
		metadata.LLM = strings.TrimSpace(metadata.LLM)
		metadata.Harness = strings.TrimSpace(metadata.Harness)
		if metadata.Presence != nil {
			presence := *metadata.Presence
			presence.Status = strings.TrimSpace(presence.Status)
			presence.Transport = strings.TrimSpace(presence.Transport)
			presence.SessionKey = strings.TrimSpace(presence.SessionKey)
			presence.UpdatedAt = strings.TrimSpace(presence.UpdatedAt)
			metadata.Presence = &presence
		}
		if agent.Metadata.Skills == nil {
			metadata.Skills = nil
		}
		if agent.Metadata.AdvertisedSkills == nil {
			metadata.AdvertisedSkills = nil
		}
		if agent.Metadata.Activities == nil {
			metadata.Activities = nil
		}
		agent.Metadata = &metadata
	}
	if agent.Skills == nil {
		agent.Skills = nil
	}
	if agent.AdvertisedSkills == nil {
		agent.AdvertisedSkills = nil
	}
	return agent
}

func hasCallerTarget(task PendingTask) bool {
	return task.CallerAgentUUID != "" || task.CallerAgentURI != ""
}

func callerTargetFromMessage(message hub.PullResponse) (string, string) {
	callerAgentUUID := strings.TrimSpace(message.FromAgentUUID)
	callerAgentURI := strings.TrimSpace(message.FromAgentURI)
	if callerAgentUUID != "" || callerAgentURI != "" {
		return callerAgentUUID, callerAgentURI
	}

	replyTarget := strings.TrimSpace(message.OpenClawMessage.ReplyTarget)
	if replyTarget == "" {
		return "", ""
	}
	if strings.Contains(replyTarget, "://") {
		return "", replyTarget
	}
	return replyTarget, ""
}

func normalizeAgentProfile(profile AgentProfile) AgentProfile {
	profile.Handle = strings.TrimSpace(profile.Handle)
	profile.DisplayName = strings.TrimSpace(profile.DisplayName)
	profile.Emoji = strings.TrimSpace(profile.Emoji)
	profile.ProfileMarkdown = strings.TrimSpace(profile.ProfileMarkdown)
	return profile
}

func buildAgentMetadata(profile AgentProfile, sessionKey, transport string) map[string]any {
	metadata := map[string]any{
		"agent_type":       "dispatch",
		"display_name":     profile.DisplayName,
		"emoji":            profile.Emoji,
		"profile_markdown": profile.ProfileMarkdown,
		"harness":          dispatcherHarness,
		"skills":           advertisedSkills,
		"presence": map[string]any{
			"status":      "online",
			"ready":       true,
			"transport":   normalizePresenceTransport(transport),
			"session_key": sessionKey,
			"updated_at":  time.Now().UTC().Format(time.RFC3339),
		},
	}
	if profile.DisplayName == "" {
		delete(metadata, "display_name")
	}
	if profile.Emoji == "" {
		delete(metadata, "emoji")
	}
	if profile.ProfileMarkdown == "" {
		delete(metadata, "profile_markdown")
	}
	return metadata
}

func normalizePresenceTransport(transport string) string {
	switch strings.TrimSpace(transport) {
	case ConnectionTransportWebSocket:
		return ConnectionTransportWebSocket
	case ConnectionTransportHTTPLong:
		return ConnectionTransportHTTPLong
	default:
		return ConnectionTransportHTTP
	}
}

func runtimeEndpointsFromBind(result hub.BindResponse) hub.RuntimeEndpoints {
	return runtimeEndpointsFromSession(Session{
		ManifestURL:     result.Endpoints.Manifest,
		Capabilities:    result.Endpoints.Capabilities,
		MetadataURL:     result.Endpoints.Metadata,
		OpenClawPullURL: result.Endpoints.OpenClawPull,
		OpenClawPushURL: result.Endpoints.OpenClawPush,
		OfflineURL:      result.Endpoints.Offline,
	})
}

func connectedAgentsFromCapabilities(capabilities map[string]any, state AppState) []ConnectedAgent {
	entries := capabilityPeerCatalogEntries(capabilities)
	connected := make([]ConnectedAgent, 0, len(entries))
	seen := make(map[string]int, len(entries))
	for _, entry := range entries {
		agent := connectedAgentFromCapabilityEntry(entry)
		if sameAgentRef(state.Session, agent.AgentUUID, agent.URI, agent.AgentID, agent.Handle) {
			continue
		}
		key := connectedAgentIdentityKey(agent)
		if key == "" {
			continue
		}
		if index, ok := seen[key]; ok {
			connected[index] = mergeConnectedAgentEntries(connected[index], agent)
			continue
		}
		seen[key] = len(connected)
		connected = append(connected, agent)
	}
	return connected
}

func mergeConnectedAgentEntries(primary, secondary ConnectedAgent) ConnectedAgent {
	merged := primary
	merged.AgentUUID = coalesceTrimmed(merged.AgentUUID, secondary.AgentUUID)
	merged.AgentID = coalesceTrimmed(merged.AgentID, secondary.AgentID)
	merged.URI = coalesceTrimmed(merged.URI, secondary.URI)
	merged.Handle = coalesceTrimmed(merged.Handle, secondary.Handle)
	merged.Status = coalesceTrimmed(merged.Status, secondary.Status)
	merged.DisplayName = coalesceTrimmed(merged.DisplayName, secondary.DisplayName)
	merged.Emoji = coalesceTrimmed(merged.Emoji, secondary.Emoji)
	merged.Presence = mergeConnectedAgentPresence(merged.Presence, secondary.Presence)
	if len(merged.AdvertisedSkills) == 0 {
		merged.AdvertisedSkills = secondary.AdvertisedSkills
	}
	if len(merged.Skills) == 0 {
		merged.Skills = secondary.Skills
	}
	merged.Metadata = mergeConnectedAgentMetadata(merged.Metadata, secondary.Metadata)
	if merged.Owner == nil && secondary.Owner != nil {
		owner := *secondary.Owner
		merged.Owner = &owner
	}
	return normalizeConnectedAgent(merged)
}

func mergeConnectedAgentMetadata(primary, secondary *hub.AgentMetadata) *hub.AgentMetadata {
	switch {
	case primary == nil && secondary == nil:
		return nil
	case primary == nil:
		metadata := *secondary
		return &metadata
	case secondary == nil:
		metadata := *primary
		return &metadata
	}

	metadata := *primary
	metadata.AgentType = coalesceTrimmed(metadata.AgentType, secondary.AgentType)
	metadata.DisplayName = coalesceTrimmed(metadata.DisplayName, secondary.DisplayName)
	metadata.Emoji = coalesceTrimmed(metadata.Emoji, secondary.Emoji)
	metadata.ProfileMarkdown = coalesceTrimmed(metadata.ProfileMarkdown, secondary.ProfileMarkdown)
	metadata.LLM = coalesceTrimmed(metadata.LLM, secondary.LLM)
	metadata.Harness = coalesceTrimmed(metadata.Harness, secondary.Harness)
	if metadata.Public == nil && secondary.Public != nil {
		value := *secondary.Public
		metadata.Public = &value
	}
	if len(metadata.Activities) == 0 {
		metadata.Activities = secondary.Activities
	}
	if len(metadata.AdvertisedSkills) == 0 {
		metadata.AdvertisedSkills = secondary.AdvertisedSkills
	}
	if len(metadata.Skills) == 0 {
		metadata.Skills = secondary.Skills
	}
	if metadata.HireMe == nil && secondary.HireMe != nil {
		value := *secondary.HireMe
		metadata.HireMe = &value
	}
	metadata.Presence = mergeConnectedAgentPresence(metadata.Presence, secondary.Presence)
	if metadataEmpty(&metadata) {
		return nil
	}
	return &metadata
}

func mergeConnectedAgentPresence(primary, secondary *hub.AgentPresence) *hub.AgentPresence {
	switch {
	case primary == nil && secondary == nil:
		return nil
	case primary == nil:
		presence := *secondary
		return &presence
	case secondary == nil:
		presence := *primary
		return &presence
	}

	presence := *primary
	presence.Status = coalesceTrimmed(presence.Status, secondary.Status)
	presence.Transport = coalesceTrimmed(presence.Transport, secondary.Transport)
	presence.SessionKey = coalesceTrimmed(presence.SessionKey, secondary.SessionKey)
	presence.UpdatedAt = coalesceTrimmed(presence.UpdatedAt, secondary.UpdatedAt)
	if presence.Ready == nil && secondary.Ready != nil {
		ready := *secondary.Ready
		presence.Ready = &ready
	}
	if presenceEmpty(&presence) {
		return nil
	}
	return &presence
}

type agentIdentity struct {
	AgentUUID       string
	AgentURI        string
	Handle          string
	DisplayName     string
	Emoji           string
	ProfileMarkdown string
}

func existingAgentIdentityFromCapabilities(capabilities map[string]any) agentIdentity {
	roots := []map[string]any{
		capabilities,
		nestedMap(capabilities, "result"),
		nestedMap(capabilities, "self"),
		nestedMap(capabilities, "me"),
		nestedMap(capabilities, "agent"),
	}
	sources := make([]map[string]any, 0, 24)
	seen := make(map[uintptr]struct{}, len(roots))
	for _, root := range roots {
		if len(root) == 0 {
			continue
		}
		ref := reflect.ValueOf(root).Pointer()
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		sources = append(sources, capabilityStringSources(root)...)
	}
	return agentIdentity{
		AgentUUID:       firstCapabilityString(sources, "agent_uuid", "uuid"),
		AgentURI:        firstCapabilityString(sources, "agent_uri", "uri"),
		Handle:          firstCapabilityString(sources, "handle", "agent_id", "id"),
		DisplayName:     firstCapabilityString(sources, "display_name", "displayName", "name"),
		Emoji:           capabilityEmoji(sources),
		ProfileMarkdown: firstCapabilityString(sources, "profile_markdown", "profile", "bio", "description"),
	}
}

func capabilityStringSources(entry map[string]any) []map[string]any {
	sources := make([]map[string]any, 0, 16)
	seen := make(map[uintptr]struct{}, 16)
	appendSource := func(source map[string]any) {
		if len(source) == 0 {
			return
		}
		ref := reflect.ValueOf(source).Pointer()
		if _, ok := seen[ref]; ok {
			return
		}
		seen[ref] = struct{}{}
		sources = append(sources, source)
	}

	entryKeys := []string{
		"metadata",
		"agent",
		"profile",
		"public_profile",
		"directory_profile",
		"directory",
		"public_directory",
		"identity",
		"peer",
		"peer_agent",
		"agent_profile",
	}

	appendSource(entry)
	queue := []map[string]any{entry}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, key := range entryKeys {
			nested := nestedMap(current, key)
			if len(nested) == 0 {
				continue
			}
			ref := reflect.ValueOf(nested).Pointer()
			if _, ok := seen[ref]; ok {
				continue
			}
			appendSource(nested)
			queue = append(queue, nested)
		}
	}

	for i := 0; i < len(sources); i++ {
		if nested := nestedMap(sources[i], "metadata"); len(nested) > 0 {
			appendSource(nested)
		}
	}

	return sources
}

func nestedMap(entry map[string]any, key string) map[string]any {
	value, ok := entry[key]
	if !ok {
		return nil
	}
	mapped, _ := value.(map[string]any)
	return mapped
}

func firstCapabilityString(sources []map[string]any, keys ...string) string {
	for _, key := range keys {
		for _, source := range sources {
			if source == nil {
				continue
			}
			if value := stringFromMap(source, key); value != "" {
				return value
			}
		}
	}
	return ""
}

func capabilityEmoji(sources []map[string]any) string {
	if emoji := firstCapabilityString(sources,
		"emoji",
		"avatar_emoji",
		"display_emoji",
		"profile_emoji",
		"icon_emoji",
		"emoji_native",
		"avatarEmoji",
		"displayEmoji",
		"emojiNative",
		"avatar",
		"icon",
	); emoji != "" {
		return emoji
	}

	for _, source := range sources {
		for _, key := range []string{"avatar", "icon"} {
			nested := nestedMap(source, key)
			if len(nested) == 0 {
				continue
			}
			if emoji := stringFromMap(nested, "emoji", "native", "emoji_native", "emojiNative"); emoji != "" {
				return emoji
			}
		}
	}

	return ""
}

func capabilitySkills(primary map[string]any, metadata map[string]any, agent map[string]any) []Skill {
	for _, source := range []map[string]any{primary, metadata, agent} {
		for _, current := range []map[string]any{source, nestedMap(source, "metadata")} {
			if current == nil {
				continue
			}
			for _, key := range []string{"advertised_skills", "skills"} {
				if skills := skillsFromAny(current[key]); len(skills) > 0 {
					return skills
				}
			}
		}
	}
	return nil
}

func mapsFromAny(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if len(item) > 0 {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			mapped, ok := item.(map[string]any)
			if ok && len(mapped) > 0 {
				out = append(out, mapped)
			}
		}
		return out
	case map[string]any:
		if looksLikeCapabilityPeerEntry(typed) {
			return []map[string]any{typed}
		}
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			mapped, ok := item.(map[string]any)
			if ok && len(mapped) > 0 {
				out = append(out, mapped)
			}
		}
		return out
	default:
		return nil
	}
}

func looksLikeCapabilityPeerEntry(entry map[string]any) bool {
	if len(entry) == 0 {
		return false
	}
	for _, key := range []string{"agent_uuid", "agent_id", "agent_uri", "uri", "handle"} {
		if strings.TrimSpace(stringFromMap(entry, key)) != "" {
			return true
		}
	}
	for _, key := range []string{"agent", "peer", "peer_agent"} {
		if nested := nestedMap(entry, key); len(nested) > 0 {
			return true
		}
	}
	return false
}

func capabilityPeerCatalogEntries(capabilities map[string]any) []map[string]any {
	roots := []map[string]any{
		capabilities,
		nestedMap(capabilities, "result"),
		nestedMap(capabilities, "self"),
		nestedMap(capabilities, "me"),
		nestedMap(capabilities, "agent"),
	}
	entries := make([]map[string]any, 0)
	seen := make(map[uintptr]struct{}, len(roots))
	for _, root := range roots {
		if len(root) == 0 {
			continue
		}
		ref := reflect.ValueOf(root).Pointer()
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		for _, key := range []string{"peer_skill_catalog", "peerSkillCatalog"} {
			entries = append(entries, mapsFromAny(root[key])...)
		}
		peers := capabilityTalkablePeerEntries(root, "control_plane", "controlPlane")
		if len(peers) == 0 {
			peers = capabilityTalkablePeerEntries(root, "communication")
		}
		if len(peers) > 0 {
			entries = append(entries, peers...)
		}
	}
	return entries
}

func capabilityTalkablePeerEntries(root map[string]any, containerKeys ...string) []map[string]any {
	if len(root) == 0 {
		return nil
	}
	for _, containerKey := range containerKeys {
		container := nestedMap(root, containerKey)
		if len(container) == 0 {
			continue
		}
		for _, peersKey := range []string{"talkable_peers", "talkablePeers"} {
			if peers := mapsFromAny(container[peersKey]); len(peers) > 0 {
				return peers
			}
		}
	}
	return nil
}

func connectedAgentFromCapabilityEntry(entry map[string]any) ConnectedAgent {
	sources := capabilityStringSources(entry)
	metadata := connectedAgentMetadataFromCapabilityEntry(entry, sources)
	agentID := firstCapabilityString(sources, "agent_id")
	handle := firstCapabilityString(sources, "handle")
	if agentID == "" {
		agentID = firstCapabilityString(sources, "id")
	}
	if handle == "" {
		handle = agentID
	}
	agent := ConnectedAgent{
		AgentUUID: firstCapabilityString(sources, "agent_uuid", "uuid"),
		AgentID:   agentID,
		URI:       firstCapabilityString(sources, "agent_uri", "uri"),
		Handle:    handle,
		Status:    connectedAgentStatusFromCapabilitySources(sources, metadata),
		Metadata:  metadata,
	}
	return normalizeConnectedAgent(agent)
}

func connectedAgentMetadataFromCapabilityEntry(entry map[string]any, sources []map[string]any) *hub.AgentMetadata {
	metadata := &hub.AgentMetadata{
		AgentType:       firstCapabilityString(sources, "agent_type"),
		DisplayName:     firstCapabilityString(sources, "display_name", "displayName", "name"),
		Emoji:           capabilityEmoji(sources),
		ProfileMarkdown: firstCapabilityString(sources, "profile_markdown", "profile", "bio"),
		LLM:             firstCapabilityString(sources, "llm"),
		Harness:         firstCapabilityString(sources, "harness"),
		Presence:        connectedAgentPresenceFromCapabilitySources(sources),
	}
	if skills := capabilitySkills(entry, nestedMap(entry, "metadata"), nestedMap(entry, "agent")); len(skills) > 0 {
		metadata.AdvertisedSkills = skillMetadataFromSkills(skills)
	}
	if metadataEmpty(metadata) {
		return nil
	}
	return metadata
}

func connectedAgentPresenceFromCapabilitySources(sources []map[string]any) *hub.AgentPresence {
	for _, source := range sources {
		presence := nestedMap(source, "presence")
		if len(presence) == 0 {
			continue
		}
		status := normalizeCapabilityPresenceStatus(stringFromMap(presence, "status"))
		ready, readyOK := boolFromAny(firstMapValue(presence, "ready"))
		if status == "" && readyOK {
			if ready {
				status = "online"
			} else {
				status = "offline"
			}
		}
		out := &hub.AgentPresence{
			Status:     status,
			Transport:  stringFromMap(presence, "transport"),
			SessionKey: stringFromMap(presence, "session_key", "sessionKey"),
			UpdatedAt:  stringFromMap(presence, "updated_at", "updatedAt"),
		}
		if readyOK {
			out.Ready = &ready
		}
		if !presenceEmpty(out) {
			return out
		}
	}

	status := normalizeCapabilityPresenceStatus(firstCapabilityString(sources, "status"))
	if status == "" {
		status = "online"
	}
	return &hub.AgentPresence{Status: status}
}

func connectedAgentStatusFromCapabilitySources(sources []map[string]any, metadata *hub.AgentMetadata) string {
	if metadata != nil && metadata.Presence != nil && strings.TrimSpace(metadata.Presence.Status) != "" {
		return strings.TrimSpace(metadata.Presence.Status)
	}
	status := normalizeCapabilityPresenceStatus(firstCapabilityString(sources, "status"))
	if status != "" {
		return status
	}
	return "online"
}

func normalizeCapabilityPresenceStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "online", "connected", "ready", "available":
		return "online"
	case "offline", "disconnected", "unavailable":
		return "offline"
	default:
		return strings.TrimSpace(status)
	}
}

func skillMetadataFromSkills(skills []Skill) []map[string]any {
	if len(skills) == 0 {
		return nil
	}
	metadata := make([]map[string]any, 0, len(skills))
	for _, skill := range skills {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			continue
		}
		entry := map[string]any{"name": name}
		if description := strings.TrimSpace(skill.Description); description != "" {
			entry["description"] = description
		}
		metadata = append(metadata, entry)
	}
	return metadata
}

func presenceEmpty(presence *hub.AgentPresence) bool {
	if presence == nil {
		return true
	}
	return strings.TrimSpace(presence.Status) == "" &&
		presence.Ready == nil &&
		strings.TrimSpace(presence.Transport) == "" &&
		strings.TrimSpace(presence.SessionKey) == "" &&
		strings.TrimSpace(presence.UpdatedAt) == ""
}

func metadataEmpty(metadata *hub.AgentMetadata) bool {
	if metadata == nil {
		return true
	}
	return strings.TrimSpace(metadata.AgentType) == "" &&
		strings.TrimSpace(metadata.DisplayName) == "" &&
		strings.TrimSpace(metadata.Emoji) == "" &&
		strings.TrimSpace(metadata.ProfileMarkdown) == "" &&
		strings.TrimSpace(metadata.LLM) == "" &&
		strings.TrimSpace(metadata.Harness) == "" &&
		len(metadata.Activities) == 0 &&
		len(metadata.AdvertisedSkills) == 0 &&
		len(metadata.Skills) == 0 &&
		presenceEmpty(metadata.Presence)
}

func skillsFromAny(value any) []Skill {
	skills := make([]Skill, 0)
	appendSkill := func(item any) {
		switch typed := item.(type) {
		case map[string]any:
			name := strings.TrimSpace(stringFromMap(typed, "name"))
			description := strings.TrimSpace(stringFromMap(typed, "description"))
			if name == "" {
				return
			}
			skills = append(skills, Skill{Name: name, Description: description})
		case Skill:
			name := strings.TrimSpace(typed.Name)
			if name == "" {
				return
			}
			skills = append(skills, Skill{Name: name, Description: strings.TrimSpace(typed.Description)})
		case string:
			name := strings.TrimSpace(typed)
			if name == "" {
				return
			}
			skills = append(skills, Skill{Name: name})
		}
	}

	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			appendSkill(item)
		}
	case []map[string]any:
		for _, item := range typed {
			appendSkill(item)
		}
	case []Skill:
		for _, item := range typed {
			appendSkill(item)
		}
	}

	return skills
}

func sameAgentRef(session Session, refs ...string) bool {
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if strings.EqualFold(ref, session.AgentUUID) || strings.EqualFold(ref, session.AgentURI) || strings.EqualFold(ref, session.Handle) {
			return true
		}
	}
	return false
}

func runtimeAPIBaseFromBind(result hub.BindResponse) string {
	return runtimeAPIBaseFromSession(Session{
		APIBase:         result.APIBase,
		ManifestURL:     result.Endpoints.Manifest,
		Capabilities:    result.Endpoints.Capabilities,
		MetadataURL:     result.Endpoints.Metadata,
		OpenClawPullURL: result.Endpoints.OpenClawPull,
		OpenClawPushURL: result.Endpoints.OpenClawPush,
		OfflineURL:      result.Endpoints.Offline,
	})
}

func runtimeAPIBaseFromSession(session Session) string {
	if apiBase := coalesceTrimmed(session.APIBase, session.BaseURL); apiBase != "" {
		return apiBase
	}
	for _, endpoint := range []string{
		session.MetadataURL,
		session.Capabilities,
		session.OpenClawPullURL,
		session.OpenClawPushURL,
		session.OfflineURL,
		session.ManifestURL,
	} {
		if apiBase := runtimeAPIBaseFromEndpoint(endpoint); apiBase != "" {
			return apiBase
		}
	}
	return ""
}

func coalesceTrimmed(values ...string) string {
	return support.FirstNonEmptyString(values...)
}

func defaultAPIBaseForHub(hubURL string) string {
	hubURL = strings.TrimRight(strings.TrimSpace(hubURL), "/")
	if hubURL == "" {
		return ""
	}
	return hubURL + "/v1"
}

func runtimeAPIBaseFromEndpoint(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}

	trimmedPath := strings.TrimRight(parsed.Path, "/")
	for _, suffix := range []string{
		"/v1/agents/me/metadata",
		"/v1/agents/me/capabilities",
		"/v1/agents/me/manifest",
		"/v1/agents/me",
		"/v1/openclaw/messages/pull",
		"/v1/openclaw/messages/publish",
		"/v1/openclaw/messages/offline",
		"/runtime/profile",
		"/runtime/capabilities",
		"/runtime/manifest",
	} {
		if strings.HasSuffix(trimmedPath, suffix) {
			parsed.Path = strings.TrimSuffix(trimmedPath, suffix)
			parsed.RawPath = ""
			return strings.TrimRight(parsed.String(), "/")
		}
	}

	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

func runtimeEndpointsFromSession(session Session) hub.RuntimeEndpoints {
	return hub.RuntimeEndpoints{
		ManifestURL:        strings.TrimSpace(session.ManifestURL),
		CapabilitiesURL:    strings.TrimSpace(session.Capabilities),
		MetadataURL:        strings.TrimSpace(session.MetadataURL),
		OpenClawPullURL:    strings.TrimSpace(session.OpenClawPullURL),
		OpenClawPushURL:    strings.TrimSpace(session.OpenClawPushURL),
		OpenClawOfflineURL: strings.TrimSpace(session.OfflineURL),
	}
}

func sanitizeRuntimeEndpoints(endpoints hub.RuntimeEndpoints) hub.RuntimeEndpoints {
	return hub.RuntimeEndpoints{
		ManifestURL:        NormalizeHubEndpointURL(endpoints.ManifestURL),
		CapabilitiesURL:    NormalizeHubEndpointURL(endpoints.CapabilitiesURL),
		MetadataURL:        NormalizeHubEndpointURL(endpoints.MetadataURL),
		OpenClawPullURL:    NormalizeHubEndpointURL(endpoints.OpenClawPullURL),
		OpenClawPushURL:    NormalizeHubEndpointURL(endpoints.OpenClawPushURL),
		OpenClawOfflineURL: NormalizeHubEndpointURL(endpoints.OpenClawOfflineURL),
	}
}

func invalidRuntimeEndpoints(endpoints hub.RuntimeEndpoints) []string {
	type endpoint struct {
		name  string
		value string
	}
	fields := []endpoint{
		{name: "manifest", value: endpoints.ManifestURL},
		{name: "capabilities", value: endpoints.CapabilitiesURL},
		{name: "metadata", value: endpoints.MetadataURL},
		{name: "openclaw_pull", value: endpoints.OpenClawPullURL},
		{name: "openclaw_push", value: endpoints.OpenClawPushURL},
		{name: "openclaw_offline", value: endpoints.OpenClawOfflineURL},
	}

	invalid := make([]string, 0, len(fields))
	for _, field := range fields {
		value := strings.TrimSpace(field.value)
		if value == "" {
			continue
		}
		if NormalizeHubEndpointURL(value) == "" {
			invalid = append(invalid, fmt.Sprintf("%s=%q", field.name, value))
		}
	}
	return invalid
}

func newSkillRequestMessage(timestamp time.Time, skillName string, payload any, payloadFormat, requestID, replyTo string) hub.OpenClawMessage {
	message := hub.OpenClawMessage{
		Protocol:      openClawHTTPProtocol,
		Type:          openClawSkillRequest,
		Timestamp:     timestamp.UTC().Format(time.RFC3339),
		SkillName:     skillName,
		Payload:       payload,
		PayloadFormat: payloadFormat,
		RequestID:     requestID,
	}
	if strings.TrimSpace(replyTo) != "" {
		message.ReplyTo = replyTo
	}
	return message
}

func (s *Service) noteHubInteraction(err error, transport string) {
	transport = normalizePresenceTransport(transport)
	now := time.Now().UTC()
	if !hubReachable(err) {
		_ = s.store.Update(func(state *AppState) error {
			baseURL, domain := hubConnectionTarget(state.Session.APIBase, state.Settings.HubURL)
			state.Connection = ConnectionState{
				Status:        ConnectionStatusDisconnected,
				Transport:     ConnectionTransportOffline,
				LastChangedAt: now,
				Error:         strings.TrimSpace(err.Error()),
				Detail:        strings.TrimSpace(err.Error()),
				BaseURL:       baseURL,
				Domain:        domain,
			}
			return nil
		})
		return
	}

	_ = s.store.Update(func(state *AppState) error {
		baseURL, domain := hubConnectionTarget(state.Session.APIBase, state.Settings.HubURL)
		currentTransport := normalizePresenceTransport(state.Connection.Transport)
		if transport == ConnectionTransportHTTP &&
			state.Connection.Status == ConnectionStatusConnected &&
			currentTransport == ConnectionTransportWebSocket {
			transport = ConnectionTransportWebSocket
		}
		state.Connection = ConnectionState{
			Status:        ConnectionStatusConnected,
			Transport:     transport,
			LastChangedAt: now,
			BaseURL:       baseURL,
			Domain:        domain,
		}
		state.Session.OfflineMarked = false
		return nil
	})
}

func (s *Service) consumeRealtimeSession(ctx context.Context, session hub.RealtimeSession) error {
	defer session.Close()

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		message, err := session.Receive(ctx)
		if err != nil {
			return err
		}

		handleErr := s.handleInboundMessage(ctx, message)
		if handleErr != nil {
			_ = session.Nack(ctx, message.DeliveryID)
			return handleErr
		}
		if err := session.Ack(ctx, message.DeliveryID); err != nil {
			return err
		}
		s.noteHubInteraction(nil, ConnectionTransportWebSocket)
		if err := s.expirePendingTasks(ctx); err != nil {
			return err
		}
	}
}

func (s *Service) pollInterval() time.Duration {
	interval := s.store.Snapshot().Settings.PollInterval
	if interval <= 0 {
		return 2 * time.Second
	}
	return interval
}

func shouldFallbackToLongPoll(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	for _, marker := range []string{
		"use of closed network connection",
		"websocket session closed",
		"connection reset by peer",
		"broken pipe",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return true
}

func isUnauthorizedHubError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *hub.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "status=401") ||
		strings.Contains(text, "status 401") ||
		strings.Contains(text, "status=403") ||
		strings.Contains(text, "status 403") ||
		strings.Contains(text, "unauthorized") ||
		strings.Contains(text, "forbidden")
}

func hubReachable(err error) bool {
	if err == nil {
		return true
	}
	var apiErr *hub.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode != http.StatusUnauthorized && apiErr.StatusCode != http.StatusForbidden
}

func sleepWithContext(ctx context.Context, wait time.Duration) bool {
	if wait <= 0 {
		wait = time.Second
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func hubConnectionTarget(apiBase, fallback string) (string, string) {
	baseURL := NormalizeHubEndpointURL(apiBase)
	if baseURL == "" {
		baseURL = NormalizeHubEndpointURL(fallback)
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return "", ""
	}

	parsed, err := url.Parse(baseURL)
	if err != nil || strings.TrimSpace(parsed.Host) == "" {
		return baseURL, ""
	}
	return baseURL, strings.TrimSpace(parsed.Host)
}

func hubPingFailureDetail(pingErr error, retryDelay time.Duration) string {
	if retryDelay <= 0 {
		retryDelay = hubPingRetryInterval
	}
	message := fmt.Sprintf("Hub endpoint ping failed; retrying every %s until live.", retryDelay)
	if pingErr == nil {
		return message
	}
	return fmt.Sprintf("%s Error: %s", message, strings.TrimSpace(pingErr.Error()))
}
