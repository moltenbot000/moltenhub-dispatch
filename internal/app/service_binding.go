package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

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
			APIBase:             strings.TrimSpace(result.APIBase),
			BaseURL:             strings.TrimSpace(result.APIBase),
			ManifestURL:         runtimeEndpoints.ManifestURL,
			MetadataURL:         runtimeEndpoints.MetadataURL,
			Capabilities:        runtimeEndpoints.CapabilitiesURL,
			RuntimePullURL:      runtimeEndpoints.RuntimePullURL,
			RuntimePushURL:      runtimeEndpoints.RuntimePushURL,
			RuntimeAckURL:       runtimeEndpoints.RuntimeAckURL,
			RuntimeNackURL:      runtimeEndpoints.RuntimeNackURL,
			RuntimeStatusURL:    runtimeEndpoints.RuntimeStatusURL,
			RuntimeWebSocketURL: runtimeEndpoints.RuntimeWebSocketURL,
			RuntimeOfflineURL:   runtimeEndpoints.RuntimeOfflineURL,
			OfflineURL:          runtimeEndpoints.RuntimeOfflineURL,
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
		BoundAt:             time.Now().UTC(),
		APIBase:             result.APIBase,
		AgentToken:          result.AgentToken,
		BaseURL:             result.APIBase,
		BindToken:           result.AgentToken,
		AgentUUID:           result.AgentUUID,
		AgentURI:            result.AgentURI,
		Handle:              agentProfile.Handle,
		HandleFinalized:     handleRequestedDuringBind,
		DisplayName:         agentProfile.DisplayName,
		Emoji:               agentProfile.Emoji,
		ProfileBio:          agentProfile.ProfileMarkdown,
		ManifestURL:         runtimeEndpoints.ManifestURL,
		MetadataURL:         runtimeEndpoints.MetadataURL,
		Capabilities:        runtimeEndpoints.CapabilitiesURL,
		RuntimePullURL:      runtimeEndpoints.RuntimePullURL,
		RuntimePushURL:      runtimeEndpoints.RuntimePushURL,
		RuntimeAckURL:       runtimeEndpoints.RuntimeAckURL,
		RuntimeNackURL:      runtimeEndpoints.RuntimeNackURL,
		RuntimeStatusURL:    runtimeEndpoints.RuntimeStatusURL,
		RuntimeWebSocketURL: runtimeEndpoints.RuntimeWebSocketURL,
		RuntimeOfflineURL:   runtimeEndpoints.RuntimeOfflineURL,
		OfflineURL:          runtimeEndpoints.RuntimeOfflineURL,
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

func (s *Service) RefreshAgentProfile(ctx context.Context) (AgentProfile, error) {
	state := s.store.Snapshot()
	if strings.TrimSpace(state.Session.AgentToken) == "" {
		return AgentProfile{}, errors.New("agent is not bound yet")
	}
	s.syncHubClient(state)

	capabilities, err := s.hub.GetCapabilities(ctx, state.Session.AgentToken)
	if err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		return AgentProfile{}, fmt.Errorf("refresh agent profile from /v1/agents/me/capabilities: %w", err)
	}
	s.noteHubInteraction(nil, ConnectionTransportHTTP)

	identity := existingAgentIdentityFromCapabilities(capabilities)
	profile := normalizeAgentProfile(AgentProfile{
		Handle:          coalesceTrimmed(identity.Handle, state.Session.Handle),
		DisplayName:     coalesceTrimmed(identity.DisplayName, state.Session.DisplayName),
		Emoji:           coalesceTrimmed(identity.Emoji, state.Session.Emoji),
		ProfileMarkdown: coalesceTrimmed(identity.ProfileMarkdown, state.Session.ProfileBio),
	})
	if err := s.store.Update(func(current *AppState) error {
		current.Session.Handle = profile.Handle
		current.Session.DisplayName = profile.DisplayName
		current.Session.Emoji = profile.Emoji
		current.Session.ProfileBio = profile.ProfileMarkdown
		return nil
	}); err != nil {
		return AgentProfile{}, err
	}
	return profile, nil
}

func (s *Service) DisconnectAgent(ctx context.Context) error {
	state := s.store.Snapshot()
	if strings.TrimSpace(state.Session.AgentToken) != "" {
		_ = s.MarkOffline(ctx, "manual disconnect")
	}
	s.presenceSynced = false
	s.presenceTransport = ""
	return s.store.Update(func(current *AppState) error {
		current.Session = Session{}
		current.Connection = ConnectionState{
			Status:        ConnectionStatusDisconnected,
			Transport:     ConnectionTransportOffline,
			LastChangedAt: time.Now().UTC(),
		}
		return nil
	})
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
