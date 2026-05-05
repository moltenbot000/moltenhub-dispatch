package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

func TestDispatchRequestFromPayloadCoversAliasesAndInlinePayload(t *testing.T) {
	scheduledAt := time.Date(2026, 5, 5, 10, 30, 0, 0, time.UTC)
	req, err := DispatchRequestFromPayload(map[string]any{
		"selectedAgentUuid": "agent-uuid",
		"selectedTask":      "review",
		"repo":              "owner/repo",
		"logPaths":          []any{"/tmp/a.log", 42, "/tmp/b.log"},
		"scheduledAt":       scheduledAt.Format(time.RFC3339),
		"every":             "2h",
		"prompt":            "inspect logs",
	})
	if err != nil {
		t.Fatalf("DispatchRequestFromPayload: %v", err)
	}
	if req.TargetAgentRef != "agent-uuid" || req.SkillName != "review" || req.Repo != "owner/repo" {
		t.Fatalf("unexpected dispatch request: %#v", req)
	}
	if req.ScheduledAt != scheduledAt || req.Frequency != 2*time.Hour {
		t.Fatalf("unexpected schedule: at=%v frequency=%v", req.ScheduledAt, req.Frequency)
	}
	payload := req.Payload.(map[string]any)
	if payload["prompt"] != "inspect logs" {
		t.Fatalf("inline payload = %#v, want prompt", payload)
	}
}

func TestDispatchPayloadParsingEdgeCases(t *testing.T) {
	var payload dispatchPayload
	if err := payload.FromAny(nil); err != nil {
		t.Fatalf("FromAny nil: %v", err)
	}
	if err := payload.FromAny(" "); err != nil {
		t.Fatalf("FromAny empty string: %v", err)
	}
	if err := payload.FromAny([]byte(`{"targetAgentUri":"molten://agent/1","skillName":"build","payload":"{\"ok\":true}","payloadFormat":"json"}`)); err != nil {
		t.Fatalf("FromAny bytes: %v", err)
	}
	taskPayload, err := payload.TaskPayload()
	if err != nil {
		t.Fatalf("TaskPayload JSON: %v", err)
	}
	if taskPayload.(map[string]any)["ok"] != true {
		t.Fatalf("decoded task payload = %#v", taskPayload)
	}
	if payload.TargetAgentRef() != "molten://agent/1" || payload.RequestedSkillName() != "build" {
		t.Fatalf("unexpected target/skill: %#v", payload)
	}
	if err := payload.FromAny(`{"payload":`); err == nil {
		t.Fatal("FromAny invalid JSON expected error")
	}
}

func TestScheduleTimeDurationAndPayloadNormalizationBranches(t *testing.T) {
	now := time.Now().UTC()
	if got := timeFromAny(now); !got.Equal(now) {
		t.Fatalf("timeFromAny time = %v, want %v", got, now)
	}
	for _, value := range []any{json.Number("1700000000"), float64(1700000001), int64(1700000002), int(1700000003), "15m"} {
		if got := timeFromAny(value); got.IsZero() {
			t.Fatalf("timeFromAny(%#v) returned zero", value)
		}
	}
	if got := timeFromAny("not-a-time"); !got.IsZero() {
		t.Fatalf("timeFromAny invalid = %v, want zero", got)
	}
	for _, value := range []any{time.Minute, "90", json.Number("2.5"), float64(3), int64(4), int(5)} {
		if got := durationFromAny(value); got <= 0 {
			t.Fatalf("durationFromAny(%#v) = %v, want positive", value, got)
		}
	}
	if got := durationFromAny("bad"); got != 0 {
		t.Fatalf("durationFromAny invalid = %v, want zero", got)
	}

	at, frequency := scheduleFromAny(map[string]any{"after": "1s", "interval": "30s"})
	if at.IsZero() || frequency != 30*time.Second {
		t.Fatalf("scheduleFromAny delay = %v/%v, want nonzero/30s", at, frequency)
	}
	at, frequency = scheduleFromAny("2026-05-05T10:00:00Z")
	if at.IsZero() || frequency != 0 {
		t.Fatalf("scheduleFromAny scalar = %v/%v, want time/0", at, frequency)
	}

	payload := normalizePayload("text", "owner/repo", []string{"/a", "/a", "/b"})
	if payload["input"] != "text" || payload["repo"] != "owner/repo" {
		t.Fatalf("normalizePayload scalar = %#v", payload)
	}
	if got := normalizePayloadFormat("text/plain", "hello"); got != "markdown" {
		t.Fatalf("normalizePayloadFormat text/plain = %q, want markdown", got)
	}
	if got := normalizePayloadFormat("", map[string]any{}); got != "json" {
		t.Fatalf("normalizePayloadFormat map = %q, want json", got)
	}
	if got := normalizePayloadFormat("weird", "hello"); got != "markdown" {
		t.Fatalf("normalizePayloadFormat unknown string = %q, want markdown", got)
	}
}

func TestStatusUpdateHelpersCoverA2AAndFallbacks(t *testing.T) {
	message := hub.OpenClawMessage{
		StatusUpdate: map[string]any{
			"status": map[string]any{
				"state": "done",
				"message": map[string]any{
					"parts": []any{map[string]any{"text": "finished"}},
				},
			},
		},
	}
	if got := statusUpdateTaskState(message); got != "TASK_STATE_COMPLETED" {
		t.Fatalf("statusUpdateTaskState = %q, want completed", got)
	}
	if got := statusUpdateMessageText(message); got != "finished" {
		t.Fatalf("statusUpdateMessageText = %q, want finished", got)
	}
	for status, want := range map[string]string{
		"queued":    "TASK_STATE_SUBMITTED",
		"waiting":   "TASK_STATE_WORKING",
		"timeout":   "TASK_STATE_FAILED",
		"aborted":   "TASK_STATE_CANCELED",
		"duplicate": "TASK_STATE_REJECTED",
		"unknown":   "",
	} {
		if got := taskStateFromStatus(status); got != want {
			t.Fatalf("taskStateFromStatus(%q) = %q, want %q", status, got, want)
		}
	}
	for state, wantMessage := range map[string]string{
		"TASK_STATE_COMPLETED": "Task completed.",
		"TASK_STATE_FAILED":    "Task failed.",
		"TASK_STATE_CANCELED":  "Task canceled.",
		"TASK_STATE_REJECTED":  "Task rejected.",
		"TASK_STATE_SUBMITTED": "Task queued.",
		"":                     "Task status updated.",
	} {
		if got := statusUpdateDefaultMessage("", state); got != wantMessage {
			t.Fatalf("statusUpdateDefaultMessage(%q) = %q, want %q", state, got, wantMessage)
		}
	}
	if got := statusUpdateDefaultMessage("custom", ""); got != "Task status updated: custom." {
		t.Fatalf("statusUpdateDefaultMessage status = %q", got)
	}
	level, title := taskStatusUpdateEventLevelTitle("failed")
	if level != "error" || !strings.Contains(title, "failed") {
		t.Fatalf("event level/title = %q/%q, want error failed", level, title)
	}
}
