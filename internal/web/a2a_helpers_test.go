package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/app"
	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

func TestA2APayloadFromPartsCoversTextDataRawAndFileParts(t *testing.T) {
	text := " hello "
	payload, format, metadata, protocolErr := a2aPayloadFromParts([]a2aPart{{
		Text:     &text,
		Metadata: map[string]any{"target_agent_ref": "agent-1"},
	}})
	if protocolErr != nil {
		t.Fatalf("a2aPayloadFromParts text error: %v", protocolErr)
	}
	if payload != "hello" || format != "markdown" || metadata["target_agent_ref"] != "agent-1" {
		t.Fatalf("text payload = %#v/%q/%#v", payload, format, metadata)
	}

	payload, format, metadata, protocolErr = a2aPayloadFromParts([]a2aPart{{
		Data: map[string]any{"skill_name": "review", "prompt": "inspect"},
	}})
	if protocolErr != nil {
		t.Fatalf("a2aPayloadFromParts data error: %v", protocolErr)
	}
	if payload != nil || format != "" || metadata["skill_name"] != "review" {
		t.Fatalf("control data payload = %#v/%q/%#v", payload, format, metadata)
	}

	payload, format, _, protocolErr = a2aPayloadFromParts([]a2aPart{
		{Raw: "raw text"},
		{URL: " https://example.test/file.txt ", Filename: " file.txt ", MediaType: "text/plain"},
	})
	if protocolErr != nil {
		t.Fatalf("a2aPayloadFromParts multi error: %v", protocolErr)
	}
	if format != "json" {
		t.Fatalf("multi format = %q, want json", format)
	}
	parts := payload.(map[string]any)["parts"].([]map[string]any)
	if parts[1]["url"] != "https://example.test/file.txt" || parts[1]["filename"] != "file.txt" {
		t.Fatalf("file part payload = %#v", parts[1])
	}
}

func TestA2ATaskLookupAndListCoverPendingScheduledAndRuntimeEvents(t *testing.T) {
	history := 0
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	stub := &stubService{state: app.AppState{
		PendingTasks: []app.PendingTask{{
			ID:                     "task-1",
			Status:                 "sending",
			OriginalSkillName:      "review",
			TargetAgentDisplayName: "Agent One",
			TargetAgentUUID:        "agent-uuid",
			CreatedAt:              now,
			DispatchPayload:        map[string]any{"prompt": "inspect"},
		}},
		ScheduledMessages: []app.ScheduledMessage{{
			ID:                "schedule-1",
			Status:            app.ScheduledMessageStatusActive,
			OriginalSkillName: "build",
			TargetAgentRef:    "agent-2",
			NextRunAt:         now.Add(time.Hour),
			DispatchPayload:   map[string]any{"prompt": "build"},
		}},
		RecentEvents: []app.RuntimeEvent{{
			TaskID:                 "event-task",
			Title:                  "Task completed",
			Detail:                 "done",
			TargetAgentDisplayName: "Agent Event",
			At:                     now,
		}},
	}}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	task, protocolErr := server.a2aTaskByID("schedule-1", &history)
	if protocolErr != nil {
		t.Fatalf("a2aTaskByID schedule: %#v", protocolErr)
	}
	if task["id"] != "schedule-1" || task["status"].(map[string]any)["state"] != "TASK_STATE_SUBMITTED" {
		t.Fatalf("scheduled task = %#v", task)
	}
	task, protocolErr = server.a2aTaskByID("event-task", nil)
	if protocolErr != nil {
		t.Fatalf("a2aTaskByID event: %#v", protocolErr)
	}
	if task["id"] != "event-task" {
		t.Fatalf("event task = %#v", task)
	}
	if _, protocolErr = server.a2aGetTask(json.RawMessage(`{"id":""}`)); protocolErr == nil {
		t.Fatal("a2aGetTask missing id expected error")
	}
	list, protocolErr := server.a2aListTasks(json.RawMessage(`{"historyLength":0}`))
	if protocolErr != nil {
		t.Fatalf("a2aListTasks: %#v", protocolErr)
	}
	if len(list["tasks"].([]map[string]any)) == 0 {
		t.Fatalf("expected listed tasks, got %#v", list)
	}
}

func TestA2ARequestAndErrorHelpers(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/a2a", nil)
	req.Host = "internal.local"
	req.Header.Set("X-Forwarded-Proto", "https, http")
	req.Header.Set("X-Forwarded-Host", "hub.example.test")
	req.Header.Set("Content-Type", "application/a2a+json; charset=utf-8")
	if got := a2aRequestBaseURL(req); got != "https://hub.example.test" {
		t.Fatalf("a2aRequestBaseURL = %q", got)
	}
	if got := a2aEndpointURL(req, "agent/one"); got != "https://hub.example.test/v1/a2a/agents/agent%2Fone" {
		t.Fatalf("a2aEndpointURL = %q", got)
	}
	if !a2aRequestHasJSONContent(req) {
		t.Fatal("expected JSON content")
	}
	if got := a2aHistoryLengthFromQuery(url.Values{"historyLength": []string{"2"}}); got == nil || *got != 2 {
		t.Fatalf("a2aHistoryLengthFromQuery = %#v, want 2", got)
	}
	if got := a2aHistoryLengthFromQuery(url.Values{"historyLength": []string{"bad"}}); got != nil {
		t.Fatalf("a2aHistoryLengthFromQuery invalid = %#v, want nil", got)
	}

	for _, protocolErr := range []*a2aProtocolError{
		a2aParseError("parse", "", nil),
		a2aInvalidRequest("invalid", "bad request", nil),
		a2aMethodNotFound("missing", "missing method", nil),
		a2aTaskNotCancelable("locked", "locked", nil),
		a2aPushUnsupported("push", "push unsupported", nil),
		a2aUnsupported("unsupported", "unsupported", nil),
		a2aContentType("content", "bad content", nil),
		a2aDispatchError(errors.New("no connected agent matched")),
	} {
		if protocolErr.message == "" || len(a2aErrorDetails(protocolErr)) == 0 {
			t.Fatalf("bad protocol error: %#v", protocolErr)
		}
	}
	rec := httptest.NewRecorder()
	writeA2ARESTError(rec, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("writeA2ARESTError status = %d, want 500", rec.Code)
	}
}

func TestA2ASkillsAndServerAgentHelpers(t *testing.T) {
	agent := app.ConnectedAgent{
		AgentID:   "agent-1",
		AgentUUID: "uuid-1",
		Metadata: &hub.AgentMetadata{
			Skills: []map[string]any{{"name": "review", "description": "Review code"}, {"name": " "}},
		},
	}
	skills := a2aSkillsFromConnectedAgent(agent)
	if len(skills) != 1 || skills[0]["id"] != "review" {
		t.Fatalf("a2aSkillsFromConnectedAgent = %#v", skills)
	}
	if got := a2aSkillID("agent/one", "Code Review"); got != "agent_one_code_review" {
		t.Fatalf("a2aSkillID = %q", got)
	}

	stub := &stubService{state: app.AppState{ConnectedAgents: []app.ConnectedAgent{agent}}}
	server, err := New(stub)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if target := server.a2aTargetAgent("uuid-1"); target == nil || target.AgentUUID != "uuid-1" {
		t.Fatalf("a2aTargetAgent = %#v", target)
	}
	if target := server.a2aTargetAgent(" "); target != nil {
		t.Fatalf("a2aTargetAgent blank = %#v, want nil", target)
	}
	event := a2aCompatEvent("status", map[string]any{"id": "task-1"})
	if event["kind"] != "status" || event["id"] != "task-1" {
		t.Fatalf("a2aCompatEvent = %#v", event)
	}
}

func TestServerFormParsingAndSkillHelpers(t *testing.T) {
	values := url.Values{
		"target_agent_ref": []string{"agent-1"},
		"skill_name":       []string{"review"},
		"payload":          []string{`{"prompt":"inspect"}`},
		"payload_format":   []string{"json"},
		"schedule_at":      []string{"2026-05-05T12:00:00Z"},
		"frequency":        []string{"15m"},
		"log_paths":        []string{"/a\n/b"},
	}
	req := httptest.NewRequest(http.MethodPost, "/dispatch", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	form, err := parseFormValues(req)
	if err != nil {
		t.Fatalf("parseFormValues: %v", err)
	}
	dispatchReq, err := dispatchRequestFromValues(form)
	if err != nil {
		t.Fatalf("dispatchRequestFromValues: %v", err)
	}
	if dispatchReq.TargetAgentRef != "agent-1" || dispatchReq.SkillName != "review" || dispatchReq.Frequency != 15*time.Minute {
		t.Fatalf("dispatch request = %#v", dispatchReq)
	}
	if got, _, err := dispatchPayloadValue(" ", "markdown"); err != nil || got != nil {
		t.Fatalf("dispatchPayloadValue blank = %#v, want nil", got)
	}
	if got, err := parseScheduleTime(""); err != nil || !got.IsZero() {
		t.Fatalf("parseScheduleTime blank = %v/%v, want zero nil", got, err)
	}
	if _, err := parseScheduleTime("bad"); err == nil {
		t.Fatal("parseScheduleTime invalid expected error")
	}
	if _, err := parseScheduleDuration("-1s"); err == nil {
		t.Fatal("parseScheduleDuration negative expected error")
	}
	if _, err := parseScheduleDuration("bad"); err == nil {
		t.Fatal("parseScheduleDuration invalid expected error")
	}

	skills := parseSkills(" review : Review code, build, ,review:duplicate ")
	if len(skills) != 3 || skills[0].Name != "review" || skills[0].Description != "Review code" {
		t.Fatalf("parseSkills = %#v", skills)
	}
	deduped := dedupeSkills(skills)
	if len(deduped) != 2 {
		t.Fatalf("dedupeSkills = %#v, want 2 skills", deduped)
	}
	agent := app.ConnectedAgent{Metadata: &hub.AgentMetadata{Skills: []map[string]any{{"name": "review"}, {"name": "review"}}}}
	if got := connectedAgentSkills(agent); len(got) != 1 || got[0].Name != "review" {
		t.Fatalf("connectedAgentSkills = %#v", got)
	}
}
