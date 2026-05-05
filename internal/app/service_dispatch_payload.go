package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
	"github.com/moltenbot000/moltenhub-dispatch/internal/support"
)

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
	ScheduledAt     time.Time
	Frequency       time.Duration
	raw             map[string]any
}

// DispatchRequestFromPayload converts a dispatch skill payload into the
// internal request shape shared by OpenClaw and HTTP JSON dispatch entrypoints.

func DispatchRequestFromPayload(value any) (DispatchRequest, error) {
	var payload dispatchPayload
	if err := payload.FromAny(value); err != nil {
		return DispatchRequest{}, err
	}
	taskPayload, err := payload.TaskPayload()
	if err != nil {
		return DispatchRequest{}, err
	}
	return DispatchRequest{
		TargetAgentRef: payload.TargetAgentRef(),
		SkillName:      payload.RequestedSkillName(),
		Repo:           payload.Repo,
		LogPaths:       payload.LogPaths,
		Payload:        taskPayload,
		PayloadFormat:  payload.PayloadFormat,
		ScheduledAt:    payload.ScheduledAt,
		Frequency:      payload.Frequency,
	}, nil
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
	if err := support.UnmarshalJSONPayload(data, &raw); err != nil {
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
			"agent",
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
		Payload:       firstMapValue(raw, "payload", "message", "messages"),
		PayloadFormat: stringFromMap(raw, "payload_format", "payloadFormat"),
		ScheduledAt:   timeFromAny(firstMapValue(raw, "scheduled_at", "scheduledAt", "schedule_at", "scheduleAt", "run_at", "runAt", "start_at", "startAt")),
		Frequency:     durationFromAny(firstMapValue(raw, "frequency", "recurring_frequency", "recurringFrequency", "interval", "every")),
		raw:           raw,
	}
	scheduledAt, frequency := scheduleFromAny(firstMapValue(raw, "schedule", "delivery_schedule", "deliverySchedule"))
	if p.ScheduledAt.IsZero() {
		p.ScheduledAt = scheduledAt
	}
	if p.Frequency == 0 {
		p.Frequency = frequency
	}
}

func (p dispatchPayload) TargetAgentRef() string {
	return support.FirstNonEmptyString(p.AgentRef, p.TargetAgentUUID, p.TargetAgentURI)
}

func (p dispatchPayload) RequestedSkillName() string {
	return support.FirstNonEmptyString(p.SkillName, p.SelectedTask)
}

func (p dispatchPayload) TaskPayload() (any, error) {
	var taskPayload any
	if p.Payload != nil {
		taskPayload = p.Payload
	} else if len(p.raw) > 0 {
		inline := make(map[string]any)
		for key, value := range p.raw {
			if dispatchPayloadControlField(key) {
				continue
			}
			inline[key] = value
		}
		if len(inline) > 0 {
			taskPayload = inline
		}
	}

	return normalizeDispatchTaskPayload(taskPayload, p.PayloadFormat)
}

func dispatchPayloadControlField(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "target_agent_ref", "targetagentref",
		"agent",
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
		"message", "messages",
		"payload_format", "payloadformat",
		"schedule",
		"delivery_schedule", "deliveryschedule":
		return true
	case "scheduled_at", "scheduledat",
		"schedule_at", "scheduleat",
		"run_at", "runat",
		"start_at", "startat",
		"frequency",
		"recurring_frequency", "recurringfrequency",
		"interval", "every":
		return true
	default:
		return false
	}
}

func scheduleFromAny(value any) (time.Time, time.Duration) {
	switch typed := value.(type) {
	case nil:
		return time.Time{}, 0
	case map[string]any:
		scheduledAt := timeFromAny(firstMapValue(
			typed,
			"scheduled_at", "scheduledAt",
			"schedule_at", "scheduleAt",
			"run_at", "runAt",
			"start_at", "startAt",
			"at", "time", "when",
		))
		if scheduledAt.IsZero() {
			if delay := durationFromAny(firstMapValue(typed, "after", "delay", "in")); delay > 0 {
				scheduledAt = time.Now().UTC().Add(delay)
			}
		}
		return scheduledAt, durationFromAny(firstMapValue(
			typed,
			"frequency",
			"recurring_frequency", "recurringFrequency",
			"interval",
			"every",
		))
	default:
		return timeFromAny(value), 0
	}
}

func timeFromAny(value any) time.Time {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC()
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return time.Time{}
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04"} {
			if parsed, err := time.Parse(layout, trimmed); err == nil {
				return parsed.UTC()
			}
		}
		if duration := durationFromAny(trimmed); duration > 0 {
			return time.Now().UTC().Add(duration)
		}
	case json.Number:
		if seconds, err := typed.Int64(); err == nil && seconds > 0 {
			return time.Unix(seconds, 0).UTC()
		}
	case float64:
		if typed > 0 {
			return time.Unix(int64(typed), 0).UTC()
		}
	case int64:
		if typed > 0 {
			return time.Unix(typed, 0).UTC()
		}
	case int:
		if typed > 0 {
			return time.Unix(int64(typed), 0).UTC()
		}
	}
	return time.Time{}
}

func durationFromAny(value any) time.Duration {
	switch typed := value.(type) {
	case time.Duration:
		return typed
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0
		}
		if duration, err := support.ParseDuration(trimmed); err == nil {
			return duration
		}
		seconds, err := strconv.ParseFloat(trimmed, 64)
		if err == nil {
			return time.Duration(seconds * float64(time.Second))
		}
	case json.Number:
		seconds, err := typed.Float64()
		if err == nil {
			return time.Duration(seconds * float64(time.Second))
		}
	case float64:
		return time.Duration(typed * float64(time.Second))
	case int64:
		return time.Duration(typed) * time.Second
	case int:
		return time.Duration(typed) * time.Second
	}
	return 0
}

func normalizeDispatchTaskPayload(payload any, payloadFormat string) (any, error) {
	if !strings.EqualFold(strings.TrimSpace(payloadFormat), "json") {
		return payload, nil
	}
	switch typed := payload.(type) {
	case string:
		return decodeJSONPayloadString(typed)
	case []byte:
		return decodeJSONPayloadBytes(typed)
	case json.RawMessage:
		return decodeJSONPayloadBytes(typed)
	default:
		return payload, nil
	}
}

func decodeJSONPayloadString(raw string) (any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	return decodeJSONPayloadBytes([]byte(raw))
}

func decodeJSONPayloadBytes(raw []byte) (any, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, nil
	}
	var decoded any
	if err := support.UnmarshalJSONPayload(raw, &decoded); err != nil {
		return nil, fmt.Errorf("payload_format json requires valid JSON payload: %w", err)
	}
	return decoded, nil
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

func findPendingTaskForStatusUpdate(tasks []PendingTask, message hub.PullResponse) (PendingTask, bool) {
	candidates := taskStatusUpdateIDs(message)
	for _, task := range tasks {
		for _, candidate := range candidates {
			if pendingTaskMatchesID(task, candidate) {
				return task, true
			}
		}
	}
	return PendingTask{}, false
}

func taskStatusUpdateIDs(message hub.PullResponse) []string {
	statusUpdate := mapFromAny(message.OpenClawMessage.StatusUpdate)
	ids := []string{
		message.OpenClawMessage.RequestID,
		message.OpenClawMessage.ReplyTo,
		message.MessageID,
		stringFromMap(statusUpdate, "taskId", "task_id"),
		stringFromMap(statusUpdate, "contextId", "context_id"),
	}
	if status := mapFromAny(statusUpdate["status"]); len(status) > 0 {
		if statusMessage := mapFromAny(status["message"]); len(statusMessage) > 0 {
			ids = append(ids,
				stringFromMap(statusMessage, "messageId", "message_id"),
				stringFromMap(statusMessage, "taskId", "task_id"),
				stringFromMap(statusMessage, "contextId", "context_id"),
			)
		}
	}
	return support.CompactStrings(ids)
}

func pendingTaskMatchesID(task PendingTask, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, candidate := range []string{
		task.ID,
		task.ChildRequestID,
		task.HubTaskID,
		task.ParentRequestID,
		task.CallerRequestID,
	} {
		if strings.EqualFold(strings.TrimSpace(candidate), value) {
			return true
		}
	}
	return false
}

func statusUpdateTaskState(message hub.OpenClawMessage) string {
	if state := normalizeA2ATaskState(coalesceTrimmed(message.A2AState, message.TaskState)); state != "" {
		return state
	}
	statusUpdate := mapFromAny(message.StatusUpdate)
	if status := mapFromAny(statusUpdate["status"]); len(status) > 0 {
		if state := normalizeA2ATaskState(stringFromMap(status, "state")); state != "" {
			return state
		}
	}
	if state := taskStateFromStatus(message.Status); state != "" {
		return state
	}
	return "TASK_STATE_WORKING"
}

func normalizeA2ATaskState(state string) string {
	normalized := strings.ToUpper(strings.TrimSpace(state))
	if normalized == "" {
		return ""
	}
	normalized = strings.ReplaceAll(normalized, "-", "_")
	normalized = strings.ReplaceAll(normalized, " ", "_")
	if strings.HasPrefix(normalized, "TASK_STATE_") {
		return normalized
	}
	switch normalized {
	case "SUBMITTED", "QUEUED", "PENDING", "IN_QUEUE":
		return "TASK_STATE_SUBMITTED"
	case "WORKING", "RUNNING", "IN_PROGRESS", "PROGRESS":
		return "TASK_STATE_WORKING"
	case "COMPLETED", "COMPLETE", "DONE", "SUCCEEDED", "SUCCESS", "NO_CHANGES":
		return "TASK_STATE_COMPLETED"
	case "FAILED", "FAILURE", "ERROR", "ERRORED", "TIMED_OUT", "TIMEOUT":
		return "TASK_STATE_FAILED"
	case "CANCELED", "CANCELLED", "ABORTED":
		return "TASK_STATE_CANCELED"
	case "REJECTED", "DUPLICATE":
		return "TASK_STATE_REJECTED"
	default:
		return ""
	}
}

func taskStateFromStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "submitted", "pending", "in_queue":
		return "TASK_STATE_SUBMITTED"
	case "working", "running", "in_progress", "progress", "waiting":
		return "TASK_STATE_WORKING"
	case "completed", "complete", "done", "succeeded", "success", "no_changes":
		return "TASK_STATE_COMPLETED"
	case "failed", "failure", "error", "errored", "timed_out", "timeout":
		return "TASK_STATE_FAILED"
	case "canceled", "cancelled", "aborted":
		return "TASK_STATE_CANCELED"
	case "rejected", "duplicate":
		return "TASK_STATE_REJECTED"
	default:
		return ""
	}
}

func statusUpdateMessageText(message hub.OpenClawMessage) string {
	if text := strings.TrimSpace(message.Message); text != "" {
		return text
	}
	statusUpdate := mapFromAny(message.StatusUpdate)
	status := mapFromAny(statusUpdate["status"])
	statusMessage := mapFromAny(status["message"])
	if text := a2aMessageText(statusMessage); text != "" {
		return text
	}
	if text := stringFromMap(status, "message"); text != "" {
		return text
	}
	return ""
}

func a2aMessageText(message map[string]any) string {
	if len(message) == 0 {
		return ""
	}
	if text := stringFromMap(message, "text"); text != "" {
		return text
	}
	parts, _ := message["parts"].([]any)
	for _, rawPart := range parts {
		part := mapFromAny(rawPart)
		if text := stringFromMap(part, "text"); text != "" {
			return text
		}
	}
	return ""
}

func statusUpdateDefaultMessage(status, taskState string) string {
	status = strings.TrimSpace(status)
	if status != "" {
		return "Task status updated: " + status + "."
	}
	switch taskState {
	case "TASK_STATE_COMPLETED":
		return "Task completed."
	case "TASK_STATE_FAILED":
		return "Task failed."
	case "TASK_STATE_CANCELED":
		return "Task canceled."
	case "TASK_STATE_REJECTED":
		return "Task rejected."
	case "TASK_STATE_SUBMITTED":
		return "Task queued."
	default:
		return "Task status updated."
	}
}

func taskStatusUpdateEventLevelTitle(taskState string) (string, string) {
	switch normalizeA2ATaskState(taskState) {
	case "TASK_STATE_COMPLETED":
		return "info", "Task completed"
	case "TASK_STATE_FAILED", "TASK_STATE_CANCELED", "TASK_STATE_REJECTED":
		return "error", "Task failed"
	default:
		return "info", "Task progress"
	}
}
