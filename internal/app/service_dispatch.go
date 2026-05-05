package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

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
	if isScheduledDispatch(req) {
		scheduled, err := s.scheduleDispatch(state, target, req, "", "")
		if err != nil {
			return PendingTask{}, err
		}
		_ = s.logEvent("info", "Message scheduled", scheduledMessageSummary(scheduled), scheduled.ID, "")
		return PendingTask{
			ID:                     scheduled.ID,
			Status:                 ScheduledMessageStatusActive,
			ParentRequestID:        scheduled.ParentRequestID,
			OriginalSkillName:      scheduled.OriginalSkillName,
			TargetAgentDisplayName: scheduled.TargetAgentDisplayName,
			TargetAgentEmoji:       scheduled.TargetAgentEmoji,
			TargetAgentUUID:        scheduled.TargetAgentUUID,
			TargetAgentURI:         scheduled.TargetAgentURI,
			CallerAgentUUID:        scheduled.CallerAgentUUID,
			CallerAgentURI:         scheduled.CallerAgentURI,
			CallerRequestID:        scheduled.CallerRequestID,
			Repo:                   scheduled.Repo,
			CreatedAt:              scheduled.CreatedAt,
			ExpiresAt:              scheduled.NextRunAt,
			DispatchPayload:        scheduled.DispatchPayload,
			DispatchPayloadFormat:  scheduled.DispatchPayloadFormat,
		}, nil
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

	publishResp, err := s.hub.PublishOpenClaw(ctx, state.Session.AgentToken, publishReq)
	if err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		failureErr := s.failUIRequest(ctx, state, task, err)
		removeErr := s.removePendingTask(task.ChildRequestID)
		return PendingTask{}, errors.Join(failureErr, removeErr)
	}
	s.noteHubInteraction(nil, ConnectionTransportHTTP)
	task.HubTaskID = strings.TrimSpace(publishResp.MessageID)
	task.Status = PendingTaskStatusInQueue
	if err := s.setPendingTaskHubState(task.ChildRequestID, task.Status, task.HubTaskID); err != nil {
		return PendingTask{}, err
	}
	_ = s.logTaskEvent("info", "Task dispatched", fmt.Sprintf("Queued %s for %s", req.SkillName, connectedAgentNameOrRef(target)), task)
	return task, nil
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
	case openClawTaskStatus:
		return s.handleTaskStatusUpdate(message)
	case openClawTextMessage:
		return s.handleTextMessage(message)
	default:
		if isAcknowledgementMessage(message.OpenClawMessage, messageType) {
			return nil
		}
		return s.logEvent("info", "Ignored message", "Received unsupported message type "+messageType, "", "")
	}
}

func (s *Service) handleTextMessage(message hub.PullResponse) error {
	return s.logEvent("info", "Message received", textMessageDetail(message.OpenClawMessage), "", "")
}

func isAcknowledgementMessage(message hub.OpenClawMessage, messageType string) bool {
	for _, candidate := range []string{messageType, message.Kind, message.Type, message.Status} {
		switch strings.ToLower(strings.TrimSpace(candidate)) {
		case "ack", "nack", "acknowledged", "acknowledgement", "acknowledgment", "delivery_ack", "delivery_nack":
			return true
		}
	}
	return false
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
		PayloadFormat:  payload.PayloadFormat,
		ScheduledAt:    payload.ScheduledAt,
		Frequency:      payload.Frequency,
	}
	taskPayload, err := payload.TaskPayload()
	if err != nil {
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
	req.Payload = taskPayload
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
	if isScheduledDispatch(req) {
		scheduled, err := s.scheduleDispatch(state, target, req, callerAgentUUID, callerAgentURI)
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
			return s.handleTaskFailure(ctx, state, pending, failureFromError("Task schedule failed before it reached a connected agent.", err))
		}
		if hasCallerTarget(PendingTask{CallerAgentUUID: callerAgentUUID, CallerAgentURI: callerAgentURI}) {
			if err := s.publishScheduleAckToCaller(ctx, state, scheduled); err != nil {
				return err
			}
		}
		return s.logEvent("info", "Message scheduled", scheduledMessageSummary(scheduled), scheduled.ID, "")
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

	publishResp, err := s.hub.PublishOpenClaw(ctx, state.Session.AgentToken, publishReq)
	if err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		failureErr := s.handleTaskFailure(ctx, state, task, failureFromError("Task dispatch failed before it reached a connected agent.", err))
		removeErr := s.removePendingTask(task.ChildRequestID)
		return errors.Join(failureErr, removeErr)
	}
	s.noteHubInteraction(nil, ConnectionTransportHTTP)
	task.HubTaskID = strings.TrimSpace(publishResp.MessageID)
	if err := s.setPendingTaskHubState(task.ChildRequestID, PendingTaskStatusInQueue, task.HubTaskID); err != nil {
		return err
	}
	return s.logTaskEvent("info", "Forwarded request", fmt.Sprintf("Forwarded %s to %s", req.SkillName, connectedAgentNameOrRef(target)), task)
}

func (s *Service) handleSkillResult(ctx context.Context, message hub.PullResponse) error {
	state := s.store.Snapshot()
	pending, ok := FindPendingTask(state.PendingTasks, message.OpenClawMessage.RequestID)
	if !ok {
		requestID := strings.TrimSpace(message.OpenClawMessage.RequestID)
		return s.logTaskAliasEvent("info", "Unmatched skill result", "No pending task matched "+requestID, "", "", requestID, "")
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
	if err := s.logTaskEvent("info", "Task completed", completionSummary(pending), pending); err != nil {
		return err
	}
	return s.removePendingTask(pending.ChildRequestID)
}

func (s *Service) handleTaskStatusUpdate(message hub.PullResponse) error {
	state := s.store.Snapshot()
	pending, ok := findPendingTaskForStatusUpdate(state.PendingTasks, message)
	if !ok {
		requestID := strings.TrimSpace(message.OpenClawMessage.RequestID)
		if requestID == "" {
			requestID = strings.TrimSpace(message.MessageID)
		}
		return s.logTaskAliasEvent("info", "Unmatched task status", "No pending task matched "+requestID, requestID, "", requestID, "")
	}

	status := strings.TrimSpace(message.OpenClawMessage.Status)
	taskState := statusUpdateTaskState(message.OpenClawMessage)
	statusMessage := statusUpdateMessageText(message.OpenClawMessage)
	if statusMessage == "" {
		statusMessage = statusUpdateDefaultMessage(status, taskState)
	}
	now := time.Now().UTC()

	if err := s.writeTaskLog(pending.LogPath, map[string]any{
		"phase":       openClawTaskStatus,
		"task_id":     pending.ID,
		"request_id":  message.OpenClawMessage.RequestID,
		"status":      status,
		"task_state":  taskState,
		"message":     statusMessage,
		"status_body": message.OpenClawMessage,
	}); err != nil {
		return err
	}

	updated := pending
	updated.DownstreamStatus = status
	updated.DownstreamTaskState = taskState
	updated.DownstreamMessage = statusMessage
	updated.DownstreamUpdatedAt = now
	if err := s.updatePendingTaskStatusUpdate(updated); err != nil {
		return err
	}

	level, title := taskStatusUpdateEventLevelTitle(taskState)
	return s.logTaskEvent(level, title, statusMessage, updated)
}

func (s *Service) updatePendingTaskStatusUpdate(updated PendingTask) error {
	return s.store.Update(func(current *AppState) error {
		for i := range current.PendingTasks {
			if current.PendingTasks[i].ChildRequestID == updated.ChildRequestID {
				current.PendingTasks[i].DownstreamStatus = updated.DownstreamStatus
				current.PendingTasks[i].DownstreamTaskState = updated.DownstreamTaskState
				current.PendingTasks[i].DownstreamMessage = updated.DownstreamMessage
				current.PendingTasks[i].DownstreamUpdatedAt = updated.DownstreamUpdatedAt
				return nil
			}
		}
		return nil
	})
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
	return s.setPendingTaskHubState(childRequestID, status, "")
}

func (s *Service) setPendingTaskHubState(childRequestID, status, hubTaskID string) error {
	childRequestID = strings.TrimSpace(childRequestID)
	status = normalizePendingTaskStatus(status)
	hubTaskID = strings.TrimSpace(hubTaskID)
	if childRequestID == "" || (status == "" && hubTaskID == "") {
		return nil
	}
	return s.store.Update(func(current *AppState) error {
		for i := range current.PendingTasks {
			if current.PendingTasks[i].ChildRequestID == childRequestID {
				if status != "" {
					current.PendingTasks[i].Status = status
				}
				if hubTaskID != "" {
					current.PendingTasks[i].HubTaskID = hubTaskID
				}
				return nil
			}
		}
		return nil
	})
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

func (s *Service) publishScheduleAckToCaller(ctx context.Context, state AppState, scheduled ScheduledMessage) error {
	s.syncHubClient(state)
	payload := map[string]any{
		"ok":                true,
		"scheduled":         true,
		"schedule_id":       scheduled.ID,
		"next_run_at":       scheduled.NextRunAt.UTC().Format(time.RFC3339),
		"frequency":         scheduled.Frequency.String(),
		"target_agent":      scheduled.TargetAgentDisplayName,
		"target_agent_uuid": scheduled.TargetAgentUUID,
		"target_agent_uri":  scheduled.TargetAgentURI,
	}
	_, err := s.hub.PublishOpenClaw(ctx, state.Session.AgentToken, hub.PublishRequest{
		ToAgentUUID: scheduled.CallerAgentUUID,
		ToAgentURI:  scheduled.CallerAgentURI,
		ClientMsgID: NewID("result"),
		Message: hub.OpenClawMessage{
			Protocol:      runtimeEnvelopeProtocol,
			Type:          openClawSkillResult,
			Timestamp:     time.Now().UTC().Format(time.RFC3339),
			SkillName:     scheduled.OriginalSkillName,
			RequestID:     scheduled.ParentRequestID,
			ReplyTo:       scheduled.CallerRequestID,
			PayloadFormat: "json",
			Payload:       payload,
			OK:            boolPtr(true),
			Status:        "scheduled",
		},
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
		TargetAgentDisplayName: ConnectedAgentDisplayName(target),
		TargetAgentEmoji:       coalesceTrimmed(ConnectedAgentEmoji(target), "🙂"),
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
		PreferA2A:              req.PreferA2A,
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
		PreferA2A:   req.PreferA2A,
	}
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
	for _, skill := range ConnectedAgentSkills(target) {
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

func textMessageDetail(message hub.OpenClawMessage) string {
	for _, candidate := range []any{message.Payload, message.Input} {
		if detail := textMessageValue(candidate); detail != "" {
			return detail
		}
	}
	return "Received text message."
}

func textMessageValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		for _, key := range []string{"text", "message", "content"} {
			if detail := textMessageValue(typed[key]); detail != "" {
				return detail
			}
		}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return strings.TrimSpace(fmt.Sprint(value))
	}
	return strings.TrimSpace(string(data))
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

func newSkillRequestMessage(timestamp time.Time, skillName string, payload any, payloadFormat, requestID, replyTo string) hub.OpenClawMessage {
	message := hub.OpenClawMessage{
		Protocol:      runtimeEnvelopeProtocol,
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
