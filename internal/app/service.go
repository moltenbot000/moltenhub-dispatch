package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

const (
	dispatchSkillName      = "dispatch_skill_request"
	failureReviewSkillName = "review_failure_logs"
	dispatcherHarness      = "moltenhub-dispatch"
	followUpRepoURL        = "git@github.com:Molten-Bot/moltenhub-code.git"
	followUpPrompt         = "Review the failing log paths first, identify every root cause behind the failed task, fix the underlying issues in this repository, validate locally where possible, and summarize the verified results."
)

var advertisedSkills = []map[string]string{
	{
		"name":        dispatchSkillName,
		"description": "Dispatch a skill request to a connected agent and proxy the result back to the original caller.",
	},
	{
		"name":        failureReviewSkillName,
		"description": "Review failing log paths, find root causes, fix the repository, and report verified results.",
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

type Service struct {
	store    *Store
	hub      HubClient
	settings Settings
	mu       sync.Mutex
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
	if setter, ok := hubClient.(baseURLSetter); ok {
		baseURL := strings.TrimSpace(snapshot.Session.APIBase)
		if baseURL == "" {
			baseURL = strings.TrimSpace(snapshot.Settings.HubURL)
		}
		if baseURL != "" {
			setter.SetBaseURL(baseURL)
		}
	}
	if setter, ok := hubClient.(runtimeEndpointSetter); ok {
		setter.SetRuntimeEndpoints(runtimeEndpointsFromSession(snapshot.Session))
	}
	return &Service{
		store:    store,
		hub:      hubClient,
		settings: snapshot.Settings,
	}
}

func (s *Service) Snapshot() AppState {
	return s.store.Snapshot()
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
	resolvedHandle, err := resolveBindHandle(profile.Email, agentProfile.Handle)
	if err != nil {
		return WrapOnboardingError(OnboardingStepBind, err)
	}
	agentProfile.Handle = resolvedHandle
	handleRequestedDuringBind := agentProfile.Handle != ""
	if setter, ok := s.hub.(baseURLSetter); ok {
		setter.SetBaseURL(runtime.HubURL)
	}
	result, err := s.hub.BindAgent(ctx, hub.BindRequest{
		HubURL:    runtime.HubURL,
		BindToken: profile.BindToken,
		Handle:    agentProfile.Handle,
	})
	if err != nil {
		return WrapOnboardingError(OnboardingStepBind, err)
	}
	if setter, ok := s.hub.(baseURLSetter); ok && strings.TrimSpace(result.APIBase) != "" {
		setter.SetBaseURL(result.APIBase)
	}
	if setter, ok := s.hub.(runtimeEndpointSetter); ok {
		setter.SetRuntimeEndpoints(runtimeEndpointsFromBind(result))
	}
	if strings.TrimSpace(result.Handle) != "" {
		agentProfile.Handle = strings.TrimSpace(result.Handle)
	}

	if err := s.store.Update(func(state *AppState) error {
		state.Settings.HubRegion = runtime.ID
		state.Settings.HubURL = runtime.HubURL
		state.Session = Session{
			BoundAt:         time.Now().UTC(),
			HubURL:          runtime.HubURL,
			APIBase:         result.APIBase,
			AgentToken:      result.AgentToken,
			AgentUUID:       result.AgentUUID,
			AgentURI:        result.AgentURI,
			Handle:          agentProfile.Handle,
			HandleFinalized: handleRequestedDuringBind,
			DisplayName:     agentProfile.DisplayName,
			Emoji:           agentProfile.Emoji,
			ProfileBio:      agentProfile.ProfileMarkdown,
			ManifestURL:     result.Endpoints.Manifest,
			MetadataURL:     result.Endpoints.Metadata,
			Capabilities:    result.Endpoints.Capabilities,
			OpenClawPullURL: result.Endpoints.OpenClawPull,
			OpenClawPushURL: result.Endpoints.OpenClawPush,
			OfflineURL:      result.Endpoints.Offline,
			OfflineMarked:   false,
		}
		state.Connection = ConnectionState{
			Status:        ConnectionStatusConnected,
			Transport:     ConnectionTransportHTTP,
			LastChangedAt: time.Now().UTC(),
		}
		return nil
	}); err != nil {
		return WrapOnboardingError(OnboardingStepBind, err)
	}
	s.settings = s.store.Snapshot().Settings

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
	return nil
}

func (s *Service) UpdateAgentProfile(ctx context.Context, profile AgentProfile) error {
	state := s.store.Snapshot()
	if strings.TrimSpace(state.Session.AgentToken) == "" {
		return errors.New("agent is not bound yet")
	}

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
	agent.ID = strings.TrimSpace(agent.ID)
	if agent.ID == "" {
		agent.ID = NewID("agent")
	}
	agent.Name = strings.TrimSpace(agent.Name)
	agent.AgentUUID = strings.TrimSpace(agent.AgentUUID)
	agent.AgentURI = strings.TrimSpace(agent.AgentURI)
	agent.DefaultSkill = strings.TrimSpace(agent.DefaultSkill)
	agent.Repo = strings.TrimSpace(agent.Repo)
	agent.CreatedAt = time.Now().UTC()
	return s.store.Update(func(state *AppState) error {
		state.ConnectedAgents = AddOrReplaceConnectedAgent(state.ConnectedAgents, agent)
		return nil
	})
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
	if setter, ok := s.hub.(baseURLSetter); ok {
		snapshot := s.store.Snapshot()
		baseURL := strings.TrimSpace(snapshot.Session.APIBase)
		if baseURL == "" {
			baseURL = strings.TrimSpace(snapshot.Settings.HubURL)
		}
		if baseURL != "" {
			setter.SetBaseURL(baseURL)
		}
	}
	if setter, ok := s.hub.(runtimeEndpointSetter); ok {
		setter.SetRuntimeEndpoints(runtimeEndpointsFromSession(s.store.Snapshot().Session))
	}
	return nil
}

func (s *Service) DispatchFromUI(ctx context.Context, req DispatchRequest) (PendingTask, error) {
	state := s.store.Snapshot()
	if state.Session.AgentToken == "" {
		return PendingTask{}, errors.New("agent is not bound yet")
	}

	target, err := s.resolveDispatchTarget(state, req)
	if err != nil {
		return PendingTask{}, err
	}

	task, publishReq := s.buildPendingTask(state, target, req, "", "")
	if err := s.writeTaskLog(task.LogPath, map[string]any{
		"phase":   "queued",
		"task_id": task.ID,
		"target":  target,
		"request": req,
	}); err != nil {
		return PendingTask{}, err
	}

	if _, err := s.hub.PublishOpenClaw(ctx, state.Session.AgentToken, publishReq); err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		return PendingTask{}, s.failUIRequest(ctx, state, task, err)
	}
	s.noteHubInteraction(nil, ConnectionTransportHTTP)

	if err := s.store.Update(func(current *AppState) error {
		current.PendingTasks = append(current.PendingTasks, task)
		return nil
	}); err != nil {
		return PendingTask{}, err
	}
	_ = s.logEvent("info", "Task dispatched", fmt.Sprintf("Queued %s for %s", req.SkillName, target.NameOrRef()), task.ID, task.LogPath)
	return task, nil
}

func (s *Service) PollOnce(ctx context.Context) error {
	state := s.store.Snapshot()
	if state.Session.AgentToken == "" {
		return nil
	}

	message, ok, err := s.hub.PullOpenClaw(ctx, state.Session.AgentToken, 25*time.Second)
	if err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		return err
	}
	s.noteHubInteraction(nil, ConnectionTransportHTTP)
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
	ticker := time.NewTicker(s.pollInterval())
	defer ticker.Stop()

	for {
		if ctx.Err() != nil {
			return
		}

		state := s.store.Snapshot()
		if strings.TrimSpace(state.Session.AgentToken) == "" {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			continue
		}

		if realtime, ok := s.hub.(realtimeHubClient); ok {
			session, err := realtime.ConnectOpenClaw(ctx, state.Session.AgentToken, state.Settings.SessionKey)
			if err == nil {
				s.noteHubInteraction(nil, ConnectionTransportWebSocket)
				if err := s.consumeRealtimeSession(ctx, session); err != nil && ctx.Err() == nil {
					s.noteHubInteraction(err, ConnectionTransportWebSocket)
				}
				continue
			}
		}

		pollCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
		_ = s.PollOnce(pollCtx)
		cancel()

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) MarkOffline(ctx context.Context, reason string) error {
	state := s.store.Snapshot()
	if state.Session.AgentToken == "" || state.Session.OfflineMarked {
		return nil
	}
	if err := s.hub.MarkOffline(ctx, state.Session.AgentToken, hub.OfflineRequest{
		SessionKey: state.Settings.SessionKey,
		Reason:     reason,
	}); err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		return err
	}
	return s.store.Update(func(current *AppState) error {
		current.Session.OfflineMarked = true
		current.Connection = ConnectionState{
			Status:        ConnectionStatusDisconnected,
			Transport:     ConnectionTransportOffline,
			LastChangedAt: time.Now().UTC(),
			Error:         strings.TrimSpace(reason),
		}
		return nil
	})
}

func (s *Service) handleInboundMessage(ctx context.Context, message hub.PullResponse) error {
	messageType := strings.TrimSpace(message.OpenClawMessage.Type)
	if messageType == "" {
		messageType = strings.TrimSpace(message.OpenClawMessage.Kind)
	}
	switch messageType {
	case "skill_result":
		return s.handleSkillResult(ctx, message)
	case "skill_request":
		return s.handleSkillRequest(ctx, message)
	default:
		return s.logEvent("info", "Ignored message", "Received unsupported message type "+messageType, "", "")
	}
}

func (s *Service) handleSkillRequest(ctx context.Context, message hub.PullResponse) error {
	state := s.store.Snapshot()
	var payload dispatchPayload
	if err := payload.FromAny(message.OpenClawMessage.Payload); err != nil {
		pending := PendingTask{
			ID:              NewID("task"),
			ParentRequestID: message.OpenClawMessage.RequestID,
			CallerAgentUUID: message.FromAgentUUID,
			CallerAgentURI:  message.FromAgentURI,
			CallerRequestID: message.OpenClawMessage.RequestID,
			LogPath:         filepath.Join(s.settings.DataDir, "logs", NewID("task")+".log"),
		}
		return s.failDispatchedTask(ctx, state, pending, failureFromError("Failed to decode the dispatch request payload.", fmt.Errorf("decode dispatch payload: %w", err)))
	}

	req := DispatchRequest{
		RequestID:      message.OpenClawMessage.RequestID,
		TargetAgentRef: payload.TargetAgentRef(),
		SkillName:      payload.SkillName,
		Repo:           payload.Repo,
		LogPaths:       payload.LogPaths,
		Payload:        payload.Payload,
		PayloadFormat:  payload.PayloadFormat,
	}
	target, err := s.resolveDispatchTarget(state, req)
	if err != nil {
		pending := PendingTask{
			ID:                NewID("task"),
			ParentRequestID:   message.OpenClawMessage.RequestID,
			CallerAgentUUID:   message.FromAgentUUID,
			CallerAgentURI:    message.FromAgentURI,
			CallerRequestID:   message.OpenClawMessage.RequestID,
			OriginalSkillName: req.SkillName,
			Repo:              req.Repo,
			LogPath:           filepath.Join(s.settings.DataDir, "logs", NewID("task")+".log"),
			DispatchPayload:   normalizePayload(req.Payload, req.Repo, req.LogPaths),
		}
		return s.failDispatchedTask(ctx, state, pending, failureFromError("Task dispatch failed before it reached a connected agent.", err))
	}

	task, publishReq := s.buildPendingTask(state, target, req, message.FromAgentUUID, message.FromAgentURI)
	if err := s.writeTaskLog(task.LogPath, map[string]any{
		"phase":          "forwarding",
		"received_from":  message.FromAgentUUID,
		"received_skill": message.OpenClawMessage.SkillName,
		"task_id":        task.ID,
		"request":        req,
	}); err != nil {
		return err
	}

	if _, err := s.hub.PublishOpenClaw(ctx, state.Session.AgentToken, publishReq); err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		return s.failDispatchedTask(ctx, state, task, failureFromError("Task dispatch failed before it reached a connected agent.", err))
	}
	s.noteHubInteraction(nil, ConnectionTransportHTTP)

	if err := s.store.Update(func(current *AppState) error {
		current.PendingTasks = append(current.PendingTasks, task)
		return nil
	}); err != nil {
		return err
	}
	return s.logEvent("info", "Forwarded request", fmt.Sprintf("Forwarded %s to %s", req.SkillName, target.NameOrRef()), task.ID, task.LogPath)
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

	isFailure := !messageSucceeded(message.OpenClawMessage)
	report := failureReport{}
	if isFailure {
		report = failureFromMessage(message.OpenClawMessage)
	}

	if hasCallerTarget(pending) {
		if isFailure {
			if err := s.publishFailureToCaller(ctx, state, pending, report); err != nil {
				_ = s.logEvent("error", "Caller failure publish failed", err.Error(), pending.ID, pending.LogPath)
			}
		} else {
			if err := s.publishResultToCaller(ctx, state, pending, message.OpenClawMessage); err != nil {
				return err
			}
		}
	}

	if isFailure {
		if _, err := s.queueFollowUp(ctx, state, pending, report); err != nil {
			return err
		}
		s.tryMarkTaskFailureOffline(ctx, pending, report)
	}

	if err := s.store.Update(func(current *AppState) error {
		current.PendingTasks = RemovePendingTask(current.PendingTasks, pending.ChildRequestID)
		return nil
	}); err != nil {
		return err
	}
	return nil
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
		if hasCallerTarget(pending) {
			if publishErr := s.publishFailureToCaller(ctx, state, pending, report); publishErr != nil {
				_ = s.logEvent("error", "Caller timeout publish failed", publishErr.Error(), pending.ID, pending.LogPath)
			}
		}
		if _, queueErr := s.queueFollowUp(ctx, state, pending, report); queueErr != nil {
			return queueErr
		}
		s.tryMarkTaskFailureOffline(ctx, pending, report)
		if updateErr := s.store.Update(func(current *AppState) error {
			current.PendingTasks = RemovePendingTask(current.PendingTasks, pending.ChildRequestID)
			return nil
		}); updateErr != nil {
			return updateErr
		}
	}
	return nil
}

func (s *Service) failDispatchedTask(ctx context.Context, state AppState, pending PendingTask, report failureReport) error {
	if hasCallerTarget(pending) {
		if err := s.publishFailureToCaller(ctx, state, pending, report); err != nil {
			_ = s.logEvent("error", "Caller failure publish failed", err.Error(), pending.ID, pending.LogPath)
		}
	}
	_, err := s.queueFollowUp(ctx, state, pending, report)
	if err != nil {
		return err
	}
	s.tryMarkTaskFailureOffline(ctx, pending, report)
	return nil
}

func (s *Service) queueFollowUp(ctx context.Context, state AppState, pending PendingTask, report failureReport) (FollowUpTask, error) {
	logPaths := followUpLogPaths(pending)
	originalRequest := cloneMap(pending.DispatchPayload)
	task := FollowUpTask{
		ID:              NewID("followup"),
		CreatedAt:       time.Now().UTC(),
		Status:          "queued",
		Reason:          "task_failed",
		FailedTaskID:    pending.ID,
		FailedSkillName: pending.OriginalSkillName,
		FailedRepo:      fallbackRepo(pending.Repo),
		LogPaths:        logPaths,
		RunConfig: FollowUpRunConfig{
			Repos:        []string{followUpRepoURL},
			BaseBranch:   "main",
			TargetSubdir: ".",
			Prompt:       followUpPrompt,
		},
		OriginalError:    formatFailureSummary(report),
		OriginalRequest:  originalRequest,
		RequestedByAgent: pending.CallerAgentUUID,
	}

	reviewer, ok := SelectFailureReviewer(state)
	if ok {
		task.TargetAgentUUID = reviewer.AgentUUID
		task.TargetAgentURI = reviewer.AgentURI
		payload := map[string]any{
			"failed_task_id": pending.ID,
			"log_paths":      task.LogPaths,
			"run_config":     task.RunConfig,
			"failure": map[string]any{
				"status":       "failed",
				"message":      report.Message,
				"error":        report.Error,
				"retryable":    report.Retryable,
				"next_action":  report.NextAction,
				"error_detail": report.Detail,
			},
			"original_request": map[string]any{
				"skill_name":     pending.OriginalSkillName,
				"repo":           fallbackRepo(pending.Repo),
				"payload_format": normalizePayloadFormat("", pending.DispatchPayload),
				"payload":        originalRequest,
			},
		}
		if _, err := s.hub.PublishOpenClaw(ctx, state.Session.AgentToken, hub.PublishRequest{
			ToAgentUUID: reviewer.AgentUUID,
			ToAgentURI:  reviewer.AgentURI,
			ClientMsgID: task.ID,
			Message: hub.OpenClawMessage{
				Protocol:      "openclaw.http.v1",
				Type:          "skill_request",
				Timestamp:     time.Now().UTC().Format(time.RFC3339),
				SkillName:     failureReviewSkillName,
				Payload:       payload,
				PayloadFormat: "json",
				RequestID:     task.ID,
			},
		}); err != nil {
			s.noteHubInteraction(err, ConnectionTransportHTTP)
			task.Status = "queued_local_only"
			task.LastDispatchErr = err.Error()
		} else {
			s.noteHubInteraction(nil, ConnectionTransportHTTP)
		}
	} else {
		task.Status = "pending_reviewer"
		task.LastDispatchErr = "no failure reviewer configured"
	}

	if err := s.store.Update(func(current *AppState) error {
		current.FollowUpTasks = UpsertFollowUpTask(current.FollowUpTasks, task)
		return nil
	}); err != nil {
		return FollowUpTask{}, err
	}

	if err := s.logEvent("error", "Follow-up queued", task.OriginalError, pending.ID, pending.LogPath); err != nil {
		return FollowUpTask{}, err
	}
	return task, nil
}

func (s *Service) publishFailureToCaller(ctx context.Context, state AppState, pending PendingTask, report failureReport) error {
	if pending.LogPath == "" {
		pending.LogPath = filepath.Join(s.settings.DataDir, "logs", pending.ID+".log")
	}
	logPaths := followUpLogPaths(pending)
	if err := s.writeTaskLog(pending.LogPath, map[string]any{
		"phase":  "failed",
		"error":  report.Error,
		"detail": report.Detail,
	}); err != nil {
		return err
	}

	failurePayload := map[string]any{
		"ok":            false,
		"failure":       true,
		"status":        "failed",
		"message":       explicitFailureMessage(report.Message),
		"error":         report.Error,
		"retryable":     report.Retryable,
		"next_action":   report.NextAction,
		"error_detail":  detail,
		"error_details": detail,
		"log_paths":     logPaths,
	}

	message := hub.OpenClawMessage{
		Protocol:      "openclaw.http.v1",
		Type:          "skill_result",
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		SkillName:     pending.OriginalSkillName,
		RequestID:     pending.ParentRequestID,
		ReplyTo:       pending.CallerRequestID,
		PayloadFormat: "json",
		Payload:       failurePayload,
		Error:         report.Error,
		ErrorDetail:   failurePayload,
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
	task := PendingTask{
		ID:                taskID,
		ParentRequestID:   req.RequestID,
		ChildRequestID:    childRequestID,
		OriginalSkillName: req.SkillName,
		TargetAgentUUID:   target.AgentUUID,
		TargetAgentURI:    target.AgentURI,
		CallerAgentUUID:   callerAgentUUID,
		CallerAgentURI:    callerAgentURI,
		CallerRequestID:   req.RequestID,
		Repo:              req.Repo,
		LogPath:           logPath,
		CreatedAt:         now,
		ExpiresAt:         now.Add(timeout),
		DispatchPayload:   payload,
	}

	message := hub.OpenClawMessage{
		Protocol:      "openclaw.http.v1",
		Type:          "skill_request",
		Timestamp:     now.Format(time.RFC3339),
		SkillName:     req.SkillName,
		Payload:       payload,
		PayloadFormat: normalizePayloadFormat(req.PayloadFormat, req.Payload),
		RequestID:     childRequestID,
		ReplyTo:       req.RequestID,
	}

	return task, hub.PublishRequest{
		ToAgentUUID: target.AgentUUID,
		ToAgentURI:  target.AgentURI,
		ClientMsgID: childRequestID,
		Message:     message,
	}
}

func boolPtr(value bool) *bool {
	return &value
}

func (s *Service) updateAgentProfile(ctx context.Context, token string, profile AgentProfile) error {
	if _, err := s.hub.UpdateMetadata(ctx, token, hub.UpdateMetadataRequest{
		Handle:   profile.Handle,
		Metadata: buildAgentMetadata(profile, s.settings.SessionKey),
	}); err != nil {
		return err
	}
	return nil
}

func (s *Service) resolveDispatchTarget(state AppState, req DispatchRequest) (ConnectedAgent, error) {
	if req.TargetAgentRef != "" {
		if agent, ok := FindConnectedAgent(state.ConnectedAgents, req.TargetAgentRef); ok {
			return agent, nil
		}
		for _, agent := range state.ConnectedAgents {
			if agent.AgentUUID == req.TargetAgentRef || agent.AgentURI == req.TargetAgentRef {
				return agent, nil
			}
		}
		if strings.HasPrefix(req.TargetAgentRef, "molten://") {
			return ConnectedAgent{Name: req.TargetAgentRef, AgentURI: req.TargetAgentRef}, nil
		}
		return ConnectedAgent{}, fmt.Errorf("no connected agent matched %q", req.TargetAgentRef)
	}

	for _, agent := range state.ConnectedAgents {
		if agent.DefaultSkill == req.SkillName {
			return agent, nil
		}
		for _, skill := range agent.AdvertisedSkills {
			if skill.Name == req.SkillName {
				return agent, nil
			}
		}
	}
	return ConnectedAgent{}, fmt.Errorf("no connected agent advertises skill %q", req.SkillName)
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
	if err := s.failDispatchedTask(ctx, state, task, report); err != nil {
		return fmt.Errorf("%w; failure handling failed: %v", cause, err)
	}
	return cause
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

type dispatchPayload struct {
	TargetAgentUUID string   `json:"target_agent_uuid"`
	TargetAgentURI  string   `json:"target_agent_uri"`
	SkillName       string   `json:"skill_name"`
	Repo            string   `json:"repo"`
	LogPaths        []string `json:"log_paths"`
	Payload         any      `json:"payload"`
	PayloadFormat   string   `json:"payload_format"`
}

func (p *dispatchPayload) FromAny(value any) error {
	if value == nil {
		return errors.New("missing payload")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, p)
}

func (p dispatchPayload) TargetAgentRef() string {
	if p.TargetAgentUUID != "" {
		return p.TargetAgentUUID
	}
	return p.TargetAgentURI
}

func normalizePayload(payload any, repo string, logPaths []string) map[string]any {
	switch typed := payload.(type) {
	case map[string]any:
		if repo != "" {
			typed["repo"] = repo
		}
		if len(logPaths) > 0 {
			typed["log_paths"] = compactPaths(logPaths)
		}
		return typed
	default:
		result := map[string]any{"input": typed}
		if repo != "" {
			result["repo"] = repo
		}
		if len(logPaths) > 0 {
			result["log_paths"] = compactPaths(logPaths)
		}
		return result
	}
}

func normalizePayloadFormat(format string, payload any) string {
	if format != "" {
		return format
	}
	if _, ok := payload.(string); ok {
		return "markdown"
	}
	return "json"
}

func messageSucceeded(message hub.OpenClawMessage) bool {
	if message.OK != nil {
		return *message.OK
	}
	if strings.EqualFold(message.Status, "failed") || message.Error != "" {
		return false
	}
	payloadMap, ok := message.Payload.(map[string]any)
	if !ok {
		return true
	}
	if status, ok := payloadMap["status"].(string); ok && strings.EqualFold(status, "failed") {
		return false
	}
	if okValue, ok := payloadMap["ok"].(bool); ok {
		return okValue
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
		report.Error = stringFromMap(payloadMap, "error")
	}
	if detail, ok := payloadMap["error_detail"]; ok && detail != nil {
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

func stringFromMap(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
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

func compactPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, entry := range paths {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		out = append(out, entry)
	}
	return out
}

func followUpLogPaths(pending PendingTask) []string {
	paths := make([]string, 0, 1)
	if logPaths, ok := pending.DispatchPayload["log_paths"].([]string); ok {
		paths = append(paths, logPaths...)
	} else if logPaths, ok := pending.DispatchPayload["log_paths"].([]any); ok {
		for _, entry := range logPaths {
			if path, ok := entry.(string); ok {
				paths = append(paths, path)
			}
		}
	}
	paths = append(paths, pending.LogPath)
	return compactPaths(paths)
}

func cloneMap(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		result := make(map[string]any, len(value))
		for key, item := range value {
			result[key] = item
		}
		return result
	}
	var cloned map[string]any
	if err := json.Unmarshal(data, &cloned); err != nil {
		result := make(map[string]any, len(value))
		for key, item := range value {
			result[key] = item
		}
		return result
	}
	return cloned
}

func (a ConnectedAgent) NameOrRef() string {
	if a.Name != "" {
		return a.Name
	}
	if a.AgentUUID != "" {
		return a.AgentUUID
	}
	return a.AgentURI
}

func hasCallerTarget(task PendingTask) bool {
	return task.CallerAgentUUID != "" || task.CallerAgentURI != ""
}

func normalizeAgentProfile(profile AgentProfile) AgentProfile {
	profile.Handle = strings.TrimSpace(profile.Handle)
	profile.DisplayName = strings.TrimSpace(profile.DisplayName)
	profile.Emoji = strings.TrimSpace(profile.Emoji)
	profile.ProfileMarkdown = strings.TrimSpace(profile.ProfileMarkdown)
	return profile
}

func resolveBindHandle(email, handle string) (string, error) {
	handle = strings.TrimSpace(handle)
	if handle != "" {
		return handle, nil
	}

	derived, ok := handleFromEmailLocalPart(email)
	if ok {
		return derived, nil
	}

	return "", errors.New("handle is required unless a usable email local-part is provided")
}

func handleFromEmailLocalPart(email string) (string, bool) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return "", false
	}

	localPart, _, found := strings.Cut(email, "@")
	if !found {
		return "", false
	}
	localPart = strings.TrimSpace(localPart)
	if localPart == "" {
		return "", false
	}
	for _, ch := range localPart {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '.' || ch == '_' || ch == '-' {
			continue
		}
		return "", false
	}
	return localPart, true
}

func buildAgentMetadata(profile AgentProfile, sessionKey string) map[string]any {
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
			"transport":   "http",
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

func runtimeEndpointsFromBind(result hub.BindResponse) hub.RuntimeEndpoints {
	return hub.RuntimeEndpoints{
		ManifestURL:        strings.TrimSpace(result.Endpoints.Manifest),
		CapabilitiesURL:    strings.TrimSpace(result.Endpoints.Capabilities),
		MetadataURL:        strings.TrimSpace(result.Endpoints.Metadata),
		OpenClawPullURL:    strings.TrimSpace(result.Endpoints.OpenClawPull),
		OpenClawPushURL:    strings.TrimSpace(result.Endpoints.OpenClawPush),
		OpenClawOfflineURL: strings.TrimSpace(result.Endpoints.Offline),
	}
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

func (s *Service) noteHubInteraction(err error, transport string) {
	if transport == "" {
		transport = ConnectionTransportHTTP
	}
	now := time.Now().UTC()
	if !hubReachable(err) {
		_ = s.store.Update(func(state *AppState) error {
			state.Connection = ConnectionState{
				Status:        ConnectionStatusDisconnected,
				Transport:     ConnectionTransportOffline,
				LastChangedAt: now,
				Error:         strings.TrimSpace(err.Error()),
			}
			return nil
		})
		return
	}

	_ = s.store.Update(func(state *AppState) error {
		state.Connection = ConnectionState{
			Status:        ConnectionStatusConnected,
			Transport:     transport,
			LastChangedAt: now,
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

func hubReachable(err error) bool {
	if err == nil {
		return true
	}
	var apiErr *hub.APIError
	return errors.As(err, &apiErr)
}
