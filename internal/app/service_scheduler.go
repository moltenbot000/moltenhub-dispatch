package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/support"
)

func (s *Service) DeleteScheduledMessage(scheduleID string) error {
	scheduleID = strings.TrimSpace(scheduleID)
	if scheduleID == "" {
		return errors.New("schedule_id is required")
	}

	var deleted ScheduledMessage
	if err := s.store.Update(func(current *AppState) error {
		filtered := current.ScheduledMessages[:0]
		for _, scheduled := range current.ScheduledMessages {
			if scheduled.ID == scheduleID {
				deleted = scheduled
				continue
			}
			filtered = append(filtered, scheduled)
		}
		if deleted.ID == "" {
			return fmt.Errorf("scheduled message %q not found", scheduleID)
		}
		current.ScheduledMessages = filtered
		return nil
	}); err != nil {
		return err
	}

	_ = s.logEvent("info", "Scheduled message deleted", scheduledMessageSummary(deleted), deleted.ID, "")
	return nil
}

func (s *Service) RunSchedulerLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := s.processDueScheduledMessages(ctx); err != nil && ctx.Err() == nil {
			_ = s.logEvent("error", "Scheduled message failed", err.Error(), "", "")
		}
		delay := s.nextScheduleDelay()
		if !sleepWithContext(ctx, delay) {
			return
		}
	}
}

func (s *Service) scheduleDispatch(state AppState, target ConnectedAgent, req DispatchRequest, callerAgentUUID, callerAgentURI string) (ScheduledMessage, error) {
	now := time.Now().UTC()
	nextRunAt := req.ScheduledAt.UTC()
	if nextRunAt.IsZero() {
		nextRunAt = now
	}
	if req.Frequency < 0 {
		return ScheduledMessage{}, errors.New("schedule frequency must be positive")
	}
	cron := cronFromDuration(req.Frequency)
	if req.Frequency > 0 && cron == "" {
		return ScheduledMessage{}, errors.New("schedule frequency must be cron-compatible seconds, minutes, hours, or days")
	}
	payload := normalizePayload(req.Payload, req.Repo, req.LogPaths)
	payloadFormat := normalizePayloadFormat(req.PayloadFormat, payload)
	scheduled := ScheduledMessage{
		ID:                     NewID("schedule"),
		Status:                 ScheduledMessageStatusActive,
		ParentRequestID:        req.RequestID,
		OriginalSkillName:      req.SkillName,
		TargetAgentRef:         req.TargetAgentRef,
		TargetAgentDisplayName: ConnectedAgentDisplayName(target),
		TargetAgentEmoji:       coalesceTrimmed(ConnectedAgentEmoji(target), "🙂"),
		TargetAgentUUID:        target.AgentUUID,
		TargetAgentURI:         target.URI,
		CallerAgentUUID:        callerAgentUUID,
		CallerAgentURI:         callerAgentURI,
		CallerRequestID:        req.RequestID,
		Repo:                   req.Repo,
		LogPaths:               support.CompactStrings(req.LogPaths),
		CreatedAt:              now,
		NextRunAt:              nextRunAt,
		Frequency:              req.Frequency,
		Cron:                   cron,
		DispatchPayload:        payload,
		DispatchPayloadFormat:  payloadFormat,
		Timeout:                req.Timeout,
		PreferA2A:              req.PreferA2A,
	}
	if err := s.store.Update(func(current *AppState) error {
		current.ScheduledMessages = append(current.ScheduledMessages, scheduled)
		return nil
	}); err != nil {
		return ScheduledMessage{}, err
	}
	return scheduled, nil
}

func (s *Service) processDueScheduledMessages(ctx context.Context) error {
	state := s.store.Snapshot()
	if strings.TrimSpace(state.Session.AgentToken) == "" || len(state.ScheduledMessages) == 0 {
		return nil
	}
	now := time.Now().UTC()
	var combinedErr error
	for _, scheduled := range state.ScheduledMessages {
		if scheduled.NextRunAt.IsZero() || scheduled.NextRunAt.After(now) {
			continue
		}
		if err := s.dispatchScheduledMessage(ctx, state, scheduled, now); err != nil {
			combinedErr = errors.Join(combinedErr, err)
		}
	}
	return combinedErr
}

func (s *Service) dispatchScheduledMessage(ctx context.Context, state AppState, scheduled ScheduledMessage, now time.Time) error {
	req := DispatchRequest{
		RequestID:      scheduled.ParentRequestID,
		TargetAgentRef: support.FirstNonEmptyString(scheduled.TargetAgentUUID, scheduled.TargetAgentURI, scheduled.TargetAgentRef),
		SkillName:      scheduled.OriginalSkillName,
		Repo:           scheduled.Repo,
		LogPaths:       scheduled.LogPaths,
		Payload:        scheduled.DispatchPayload,
		PayloadFormat:  scheduled.DispatchPayloadFormat,
		Timeout:        scheduled.Timeout,
		PreferA2A:      scheduled.PreferA2A,
	}
	target, err := scheduledDispatchTarget(state, scheduled)
	if err != nil {
		pending := pendingFromScheduledMessage(scheduled, state)
		if failureErr := s.handleTaskFailure(ctx, state, pending, failureFromError("Scheduled message dispatch failed before it reached a connected agent.", err)); failureErr != nil {
			err = errors.Join(err, failureErr)
		}
		return errors.Join(err, s.advanceScheduledMessage(scheduled.ID, now))
	}
	if strings.TrimSpace(req.SkillName) == "" {
		err := errors.New("scheduled message missing skill_name")
		pending := pendingFromScheduledMessage(scheduled, state)
		if failureErr := s.handleTaskFailure(ctx, state, pending, failureFromError("Scheduled message dispatch failed before it reached a connected agent.", err)); failureErr != nil {
			err = errors.Join(err, failureErr)
		}
		return errors.Join(err, s.advanceScheduledMessage(scheduled.ID, now))
	}
	task, publishReq := s.buildPendingTask(state, target, req, scheduled.CallerAgentUUID, scheduled.CallerAgentURI)
	if err := s.writeTaskLog(task.LogPath, map[string]any{
		"phase":       PendingTaskStatusSending,
		"schedule_id": scheduled.ID,
		"task_id":     task.ID,
		"request":     req,
	}); err != nil {
		return err
	}
	if err := s.store.Update(func(current *AppState) error {
		current.PendingTasks = append(current.PendingTasks, task)
		return nil
	}); err != nil {
		return err
	}
	if _, err := s.hub.PublishRuntimeMessage(ctx, state.Session.AgentToken, publishReq); err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		failureErr := s.handleTaskFailure(ctx, state, task, failureFromError("Scheduled message dispatch failed before it reached a connected agent.", err))
		removeErr := s.removePendingTask(task.ChildRequestID)
		return errors.Join(failureErr, removeErr, s.advanceScheduledMessage(scheduled.ID, now), err)
	}
	s.noteHubInteraction(nil, ConnectionTransportHTTP)
	if err := s.setPendingTaskStatus(task.ChildRequestID, PendingTaskStatusInQueue); err != nil {
		return err
	}
	if err := s.advanceScheduledMessage(scheduled.ID, now); err != nil {
		return err
	}
	return s.logTaskEvent("info", "Scheduled message dispatched", fmt.Sprintf("Queued %s for %s", req.SkillName, connectedAgentNameOrRef(target)), task)
}

func scheduledDispatchTarget(state AppState, scheduled ScheduledMessage) (ConnectedAgent, error) {
	for _, ref := range []string{scheduled.TargetAgentUUID, scheduled.TargetAgentURI, scheduled.TargetAgentRef} {
		if agent, ok := FindConnectedAgent(state.ConnectedAgents, ref); ok {
			return agent, nil
		}
	}

	target := ConnectedAgent{
		AgentUUID:   strings.TrimSpace(scheduled.TargetAgentUUID),
		URI:         strings.TrimSpace(scheduled.TargetAgentURI),
		DisplayName: strings.TrimSpace(scheduled.TargetAgentDisplayName),
		Emoji:       strings.TrimSpace(scheduled.TargetAgentEmoji),
	}
	if target.AgentUUID == "" && target.URI == "" {
		targetRef := strings.TrimSpace(scheduled.TargetAgentRef)
		if strings.HasPrefix(targetRef, "molten://") {
			target.URI = targetRef
		}
	}
	if connectedAgentIdentityKey(target) == "" {
		return ConnectedAgent{}, fmt.Errorf("no connected agent matched scheduled target %q", strings.TrimSpace(scheduled.TargetAgentRef))
	}
	return target, nil
}

func pendingFromScheduledMessage(scheduled ScheduledMessage, state AppState) PendingTask {
	taskID := NewID("task")
	return PendingTask{
		ID:                     taskID,
		Status:                 PendingTaskStatusSending,
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
		LogPath:                filepath.Join(state.Settings.DataDir, "logs", taskID+".log"),
		CreatedAt:              time.Now().UTC(),
		DispatchPayload:        scheduled.DispatchPayload,
		DispatchPayloadFormat:  scheduled.DispatchPayloadFormat,
		PreferA2A:              scheduled.PreferA2A,
	}
}

func (s *Service) advanceScheduledMessage(scheduleID string, now time.Time) error {
	return s.store.Update(func(current *AppState) error {
		filtered := current.ScheduledMessages[:0]
		for _, scheduled := range current.ScheduledMessages {
			if scheduled.ID != scheduleID {
				filtered = append(filtered, scheduled)
				continue
			}
			if scheduled.Frequency <= 0 {
				continue
			}
			scheduled.LastRunAt = now.UTC()
			nextRunAt := scheduled.NextRunAt
			if nextRunAt.IsZero() {
				nextRunAt = now.UTC()
			}
			for !nextRunAt.After(now) {
				nextRunAt = nextRunAt.Add(scheduled.Frequency)
			}
			scheduled.NextRunAt = nextRunAt
			filtered = append(filtered, scheduled)
		}
		current.ScheduledMessages = filtered
		return nil
	})
}

func (s *Service) nextScheduleDelay() time.Duration {
	state := s.store.Snapshot()
	delay := s.pollInterval()
	now := time.Now().UTC()
	for _, scheduled := range state.ScheduledMessages {
		if scheduled.NextRunAt.IsZero() {
			return 0
		}
		until := time.Until(scheduled.NextRunAt)
		if scheduled.NextRunAt.Before(now) {
			return 0
		}
		if until < delay {
			delay = until
		}
	}
	if delay < time.Second {
		return time.Second
	}
	return delay
}

func cronFromDuration(duration time.Duration) string {
	if duration <= 0 {
		return ""
	}
	if duration%time.Second != 0 {
		return ""
	}
	seconds := int(duration / time.Second)
	if seconds <= 0 {
		return ""
	}
	if seconds < 60 {
		return fmt.Sprintf("*/%d * * * * *", seconds)
	}
	if duration%time.Minute != 0 {
		return ""
	}
	minutes := seconds / 60
	if minutes <= 0 {
		return ""
	}
	if minutes < 60 {
		return fmt.Sprintf("*/%d * * * *", minutes)
	}
	if minutes%(60*24) == 0 {
		days := minutes / (60 * 24)
		if days <= 31 {
			return fmt.Sprintf("0 0 */%d * *", days)
		}
		return ""
	}
	if minutes%60 == 0 {
		hours := minutes / 60
		if hours <= 23 {
			return fmt.Sprintf("0 */%d * * *", hours)
		}
	}
	return ""
}

func durationFromCron(raw string) time.Duration {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) == 6 {
		if strings.HasPrefix(fields[0], "*/") && fields[1] == "*" && fields[2] == "*" && fields[3] == "*" && fields[4] == "*" && fields[5] == "*" {
			return cronStepDuration(fields[0], time.Second)
		}
		return 0
	}
	if len(fields) == 5 {
		if strings.HasPrefix(fields[0], "*/") && fields[1] == "*" && fields[2] == "*" && fields[3] == "*" && fields[4] == "*" {
			return cronStepDuration(fields[0], time.Minute)
		}
		if fields[0] == "0" && strings.HasPrefix(fields[1], "*/") && fields[2] == "*" && fields[3] == "*" && fields[4] == "*" {
			return cronStepDuration(fields[1], time.Hour)
		}
		if fields[0] == "0" && fields[1] == "0" && strings.HasPrefix(fields[2], "*/") && fields[3] == "*" && fields[4] == "*" {
			return cronStepDuration(fields[2], 24*time.Hour)
		}
	}
	return 0
}

func cronStepDuration(field string, unit time.Duration) time.Duration {
	step, err := strconv.Atoi(strings.TrimPrefix(field, "*/"))
	if err != nil || step <= 0 {
		return 0
	}
	return time.Duration(step) * unit
}

func isScheduledDispatch(req DispatchRequest) bool {
	return !req.ScheduledAt.IsZero() || req.Frequency > 0
}

func scheduledMessageSummary(scheduled ScheduledMessage) string {
	summary := fmt.Sprintf("Scheduled %s for %s at %s", scheduled.OriginalSkillName, scheduled.TargetAgentDisplayName, scheduled.NextRunAt.UTC().Format(time.RFC3339))
	if scheduled.Frequency > 0 {
		summary += " every " + scheduled.Frequency.String()
	}
	return summary
}
