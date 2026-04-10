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

type Service struct {
	store    *Store
	hub      HubClient
	settings Settings
	mu       sync.Mutex
}

type failureReport struct {
	Message string
	Error   string
	Detail  any
}

type baseURLSetter interface {
	SetBaseURL(baseURL string)
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
	runtime, err := ResolveHubRuntime(profile.HubRegion, profile.HubURL)
	if err != nil {
		return err
	}
	if setter, ok := s.hub.(baseURLSetter); ok {
		setter.SetBaseURL(runtime.HubURL)
	}
	result, err := s.hub.BindAgent(ctx, hub.BindRequest{
		HubURL:    runtime.HubURL,
		BindToken: profile.BindToken,
		Handle:    profile.Handle,
	})
	if err != nil {
		return err
	}
	if setter, ok := s.hub.(baseURLSetter); ok && strings.TrimSpace(result.APIBase) != "" {
		setter.SetBaseURL(result.APIBase)
	}

	metadata := map[string]any{
		"agent_type":       "dispatch",
		"profile_markdown": strings.TrimSpace(profile.ProfileMarkdown),
		"harness":          dispatcherHarness,
		"skills":           advertisedSkills,
		"presence": map[string]any{
			"status":      "online",
			"ready":       true,
			"transport":   "polling+web",
			"session_key": s.settings.SessionKey,
			"updated_at":  time.Now().UTC().Format(time.RFC3339),
		},
	}

	if _, err := s.hub.UpdateMetadata(ctx, result.AgentToken, hub.UpdateMetadataRequest{
		Handle:   strings.TrimSpace(profile.Handle),
		Metadata: metadata,
	}); err != nil {
		return err
	}

	if err := s.store.Update(func(state *AppState) error {
		state.Settings.HubRegion = runtime.ID
		state.Settings.HubURL = runtime.HubURL
		state.Session = Session{
			BoundAt:       time.Now().UTC(),
			HubURL:        runtime.HubURL,
			APIBase:       result.APIBase,
			AgentToken:    result.AgentToken,
			AgentUUID:     result.AgentUUID,
			AgentURI:      result.AgentURI,
			Handle:        result.Handle,
			ManifestURL:   result.Endpoints.Manifest,
			Capabilities:  result.Endpoints.Capabilities,
			OfflineMarked: false,
		}
		return nil
	}); err != nil {
		return err
	}
	s.settings = s.store.Snapshot().Settings

	return s.logEvent("info", "Agent bound", fmt.Sprintf("Bound handle %q against %s", result.Handle, result.APIBase), "", "")
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
		s.settings = state.Settings
		return nil
	}); err != nil {
		return err
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
		return PendingTask{}, s.failUIRequest(task, err)
	}

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
		return err
	}
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

func (s *Service) MarkOffline(ctx context.Context, reason string) error {
	state := s.store.Snapshot()
	if state.Session.AgentToken == "" || state.Session.OfflineMarked {
		return nil
	}
	if err := s.hub.MarkOffline(ctx, state.Session.AgentToken, hub.OfflineRequest{
		SessionKey: state.Settings.SessionKey,
		Reason:     reason,
	}); err != nil {
		return err
	}
	return s.store.Update(func(current *AppState) error {
		current.Session.OfflineMarked = true
		return nil
	})
}

func (s *Service) handleInboundMessage(ctx context.Context, message hub.PullResponse) error {
	switch message.OpenClawMessage.Type {
	case "skill_result":
		return s.handleSkillResult(ctx, message)
	case "skill_request":
		return s.handleSkillRequest(ctx, message)
	default:
		return s.logEvent("info", "Ignored message", "Received unsupported message type "+message.OpenClawMessage.Type, "", "")
	}
}

func (s *Service) handleSkillRequest(ctx context.Context, message hub.PullResponse) error {
	state := s.store.Snapshot()
	var payload dispatchPayload
	if err := payload.FromAny(message.OpenClawMessage.Payload); err != nil {
		return s.publishFailureToCaller(ctx, state, PendingTask{
			ID:              NewID("task"),
			CallerAgentUUID: message.FromAgentUUID,
			CallerAgentURI:  message.FromAgentURI,
			CallerRequestID: message.OpenClawMessage.RequestID,
			LogPath:         filepath.Join(s.settings.DataDir, "logs", NewID("task")+".log"),
		}, failureFromError("Failed to decode the dispatch request payload.", fmt.Errorf("decode dispatch payload: %w", err)))
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
		return s.publishFailureToCaller(ctx, state, PendingTask{
			ID:                NewID("task"),
			CallerAgentUUID:   message.FromAgentUUID,
			CallerAgentURI:    message.FromAgentURI,
			CallerRequestID:   message.OpenClawMessage.RequestID,
			OriginalSkillName: req.SkillName,
			Repo:              req.Repo,
			LogPath:           filepath.Join(s.settings.DataDir, "logs", NewID("task")+".log"),
		}, failureFromError("Task dispatch failed before it reached a connected agent.", err))
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
		return s.publishFailureToCaller(ctx, state, task, failureFromError("Task dispatch failed before it reached a connected agent.", err))
	}

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
	if pending.CallerAgentUUID != "" || pending.CallerAgentURI != "" {
		if isFailure {
			if err := s.publishFailureToCaller(ctx, state, pending, failureFromMessage(message.OpenClawMessage)); err != nil {
				return err
			}
		} else {
			if err := s.publishResultToCaller(ctx, state, pending, message.OpenClawMessage); err != nil {
				return err
			}
		}
	}

	if isFailure {
		if _, err := s.queueFollowUp(ctx, state, pending, failureFromMessage(message.OpenClawMessage)); err != nil {
			return err
		}
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
		if pending.CallerAgentUUID != "" || pending.CallerAgentURI != "" {
			if publishErr := s.publishFailureToCaller(ctx, state, pending, failureFromError("Task failed because the downstream agent did not reply before the timeout.", err)); publishErr != nil {
				return publishErr
			}
		}
		if _, queueErr := s.queueFollowUp(ctx, state, pending, failureReport{
			Message: "Task failed because the downstream agent did not reply before the timeout.",
			Error:   err.Error(),
			Detail:  map[string]any{"timeout": true},
		}); queueErr != nil {
			return queueErr
		}
		if updateErr := s.store.Update(func(current *AppState) error {
			current.PendingTasks = RemovePendingTask(current.PendingTasks, pending.ChildRequestID)
			return nil
		}); updateErr != nil {
			return updateErr
		}
	}
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
			Repos:        []string{fallbackRepo(pending.Repo)},
			BaseBranch:   "main",
			TargetSubdir: ".",
			Prompt:       "Review the failing log paths first, identify every root cause behind the failed task, fix the underlying issues in this repository, validate locally where possible, and summarize the verified results.",
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
			task.Status = "queued_local_only"
			task.LastDispatchErr = err.Error()
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
		"phase": "failed",
		"error": report.Error,
		"detail": report.Detail,
	}); err != nil {
		return err
	}

	message := hub.OpenClawMessage{
		Protocol:      "openclaw.http.v1",
		Type:          "skill_result",
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		SkillName:     pending.OriginalSkillName,
		RequestID:     pending.ParentRequestID,
		ReplyTo:       pending.CallerRequestID,
		PayloadFormat: "json",
		Payload: map[string]any{
			"status":       "failed",
			"message":      report.Message,
			"error":        report.Error,
			"error_detail": report.Detail,
			"log_paths":    logPaths,
		},
		Error: report.Error,
		ErrorDetail: map[string]any{
			"error_detail": report.Detail,
			"log_paths":    logPaths,
		},
		OK:          boolPtr(false),
		Status:      "failed",
	}
	_, err := s.hub.PublishOpenClaw(ctx, state.Session.AgentToken, hub.PublishRequest{
		ToAgentUUID: pending.CallerAgentUUID,
		ToAgentURI:  pending.CallerAgentURI,
		ClientMsgID: NewID("result"),
		Message:     message,
	})
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

func (s *Service) failUIRequest(task PendingTask, cause error) error {
	if err := s.writeTaskLog(task.LogPath, map[string]any{
		"phase": "dispatch_failed",
		"error": cause.Error(),
	}); err != nil {
		return err
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
	report.Detail = report.Error
	return report
}

func failureFromMessage(message hub.OpenClawMessage) failureReport {
	report := failureReport{
		Message: "Task failed while dispatching to a connected agent.",
		Error:   strings.TrimSpace(message.Error),
		Detail:  message.ErrorDetail,
	}
	if report.Error == "" {
		report.Error = "downstream agent reported failure"
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

func failureDetailIsEmpty(detail any) bool {
	if detail == nil {
		return true
	}
	value, ok := detail.(string)
	return ok && strings.TrimSpace(value) == ""
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
