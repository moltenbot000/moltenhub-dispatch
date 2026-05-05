package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
	"github.com/moltenbot000/moltenhub-dispatch/internal/support"
)

type failureReport struct {
	Message    string
	Error      string
	Detail     any
	Retryable  bool
	NextAction string
}

func (s *Service) handleExecutionFailure(ctx context.Context, state AppState, pending PendingTask, report failureReport) error {
	return s.finalizeTaskFailure(ctx, state, pending, report)
}

func (s *Service) finalizeTaskFailure(ctx context.Context, state AppState, pending PendingTask, report failureReport) error {
	failureErr := s.handleTaskFailure(ctx, state, pending, report)
	removeErr := s.removePendingTask(pending.ChildRequestID)
	return errors.Join(failureErr, removeErr)
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
		Protocol:      runtimeEnvelopeProtocol,
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
	_, err := s.hub.PublishRuntimeMessage(ctx, state.Session.AgentToken, hub.PublishRequest{
		ToAgentUUID: pending.CallerAgentUUID,
		ToAgentURI:  pending.CallerAgentURI,
		ClientMsgID: NewID("result"),
		Message:     message,
	})
	s.noteHubInteraction(err, ConnectionTransportHTTP)
	return err
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
	if err := s.logTaskEvent("error", "Task failed", formatFailureSummary(report), pending); err != nil {
		combinedErr = errors.Join(combinedErr, fmt.Errorf("append failure event: %w", err))
	}
	if hasCallerTarget(pending) {
		if err := s.publishFailureToCaller(ctx, state, pending, report); err != nil {
			combinedErr = errors.Join(combinedErr, fmt.Errorf("publish failure response: %w", err))
		}
	}
	s.tryMarkTaskFailureOffline(ctx, pending, report)
	return combinedErr
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

func completionSummary(task PendingTask) string {
	skillName := strings.TrimSpace(task.OriginalSkillName)
	if skillName == "" {
		return "Downstream agent reported success."
	}
	return fmt.Sprintf("%s completed successfully.", skillName)
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
	summary := callerFailureError(report)
	if failureDetailIsEmpty(detail) {
		detail = report.Error
	}
	if failureDetailIsEmpty(detail) {
		detail = summary
	}
	payload := failureFields(report, summary, detail)
	payload["ok"] = false
	payload["failure"] = true
	payload["error_details"] = detail
	payload["log_paths"] = logPaths
	payload["Failure"] = summary
	payload["Failure:"] = summary
	payload["Error details"] = detail
	payload["Error details:"] = detail
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

func failureLogPaths(pending PendingTask) []string {
	paths := support.StringSliceFromAny(pending.DispatchPayload["log_paths"])
	paths = append(paths, pending.LogPath)
	return support.CompactStrings(paths)
}
