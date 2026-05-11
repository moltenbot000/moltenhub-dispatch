package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestAdditionalPrimitiveHelpers(t *testing.T) {
	boolCases := map[any]struct {
		want bool
		ok   bool
	}{
		true:    {true, true},
		" yes ": {true, true},
		"0":     {false, true},
		"maybe": {false, false},
		1:       {false, false},
	}
	for raw, want := range boolCases {
		got, ok := boolFromAny(raw)
		if got != want.want || ok != want.ok {
			t.Fatalf("boolFromAny(%#v) = %v, %v; want %v, %v", raw, got, ok, want.want, want.ok)
		}
	}

	intCases := map[any]int64{
		int(1):           1,
		int8(2):          2,
		int16(3):         3,
		int32(4):         4,
		int64(5):         5,
		float32(6.9):     6,
		float64(7.9):     7,
		json.Number("8"): 8,
		" 9 ":            9,
	}
	for raw, want := range intCases {
		got, ok := intFromAny(raw)
		if !ok || got != want {
			t.Fatalf("intFromAny(%#v) = %d, %v; want %d, true", raw, got, ok, want)
		}
	}
	for _, raw := range []any{json.Number("bad"), "bad", true} {
		if got, ok := intFromAny(raw); ok || got != 0 {
			t.Fatalf("intFromAny(%#v) = %d, %v; want 0, false", raw, got, ok)
		}
	}

	if got := errorString(nil); got != "" {
		t.Fatalf("errorString(nil) = %q, want empty", got)
	}
	if got := errorString(errors.New("boom")); got != "boom" {
		t.Fatalf("errorString = %q, want boom", got)
	}
	if got := fallbackRepo(" "); got != "." {
		t.Fatalf("fallbackRepo blank = %q, want .", got)
	}
	if got := fallbackRepo(" /workspace "); got != "/workspace" {
		t.Fatalf("fallbackRepo = %q, want /workspace", got)
	}
}

func TestAdditionalNormalizeOnboardingTokens(t *testing.T) {
	cases := []struct {
		name           string
		mode           string
		bindToken      string
		agentToken     string
		wantMode       string
		wantBindToken  string
		wantAgentToken string
	}{
		{"new blank", OnboardingModeNew, "", "", OnboardingModeNew, "", ""},
		{"existing blank", OnboardingModeExisting, "", "", OnboardingModeExisting, "", ""},
		{"unknown submitted bind", "", "opaque", "", OnboardingModeExisting, "", "opaque"},
		{"unknown submitted agent", "", "", "opaque", OnboardingModeExisting, "", "opaque"},
		{"default blank", "", "", "", OnboardingModeExisting, "", ""},
	}
	for _, tc := range cases {
		gotMode, gotBind, gotAgent := NormalizeOnboardingTokens(tc.mode, tc.bindToken, tc.agentToken)
		if gotMode != tc.wantMode || gotBind != tc.wantBindToken || gotAgent != tc.wantAgentToken {
			t.Fatalf("%s = %q %q %q, want %q %q %q", tc.name, gotMode, gotBind, gotAgent, tc.wantMode, tc.wantBindToken, tc.wantAgentToken)
		}
	}
}

func TestAdditionalConnectedAgentHelpers(t *testing.T) {
	service, _ := newTestService(t)
	if err := service.AddConnectedAgent(ConnectedAgent{}); err == nil {
		t.Fatal("AddConnectedAgent blank identity expected error")
	}
	if err := service.AddConnectedAgent(ConnectedAgent{AgentID: " agent-1 ", DisplayName: " Agent One "}); err != nil {
		t.Fatalf("AddConnectedAgent: %v", err)
	}
	if got := service.store.Snapshot().ConnectedAgents[0].DisplayName; got != "Agent One" {
		t.Fatalf("stored display name = %q, want trimmed", got)
	}

	ready := true
	notReady := false
	agent := ConnectedAgent{
		Status: "ignored",
		Presence: &hub.AgentPresence{
			Ready: &notReady,
		},
		Metadata: &hub.AgentMetadata{
			Presence: &hub.AgentPresence{Ready: &ready},
			AdvertisedSkills: []map[string]any{
				{"name": "review", "description": "Review code"},
			},
		},
	}
	if got := ConnectedAgentPresenceStatus(agent); got != "online" {
		t.Fatalf("metadata ready status = %q, want online", got)
	}
	if !connectedAgentSupportsSkill(agent, " REVIEW ") {
		t.Fatal("expected skill match")
	}
	if connectedAgentSupportsSkill(agent, " ") {
		t.Fatal("blank skill should not match")
	}

	agent.Metadata = nil
	if got := ConnectedAgentPresenceStatus(agent); got != "offline" {
		t.Fatalf("agent ready=false status = %q, want offline", got)
	}
	agent.Presence = nil
	agent.Status = "available"
	if got := ConnectedAgentPresenceStatus(agent); got != "online" {
		t.Fatalf("available status = %q, want online", got)
	}
	agent.Status = ""
	if got := ConnectedAgentPresenceStatus(agent); got != "offline" {
		t.Fatalf("empty status = %q, want offline", got)
	}

	agent.Skills = []map[string]any{{"name": "direct"}}
	if got := ConnectedAgentSkills(agent); len(got) != 1 || got[0].Name != "direct" {
		t.Fatalf("direct skills = %#v, want direct", got)
	}
	agent.Skills = nil
	agent.AdvertisedSkills = []map[string]any{{"name": "advertised"}}
	if got := ConnectedAgentSkills(agent); len(got) != 1 || got[0].Name != "advertised" {
		t.Fatalf("advertised skills = %#v, want advertised", got)
	}

	refs := connectedAgentRefs(ConnectedAgent{
		AgentID:   "Agent",
		Handle:    "agent",
		AgentUUID: "uuid",
		URI:       "molten://agent/1",
	})
	if len(refs) != 3 {
		t.Fatalf("refs = %#v, want 3 unique values", refs)
	}
}

func TestAdditionalCapabilityHelpers(t *testing.T) {
	public := true
	hireMe := true
	ready := true
	merged := mergeConnectedAgentEntries(
		ConnectedAgent{
			AgentID: "agent-1",
			Metadata: &hub.AgentMetadata{
				Public: &public,
			},
		},
		ConnectedAgent{
			AgentUUID: "uuid-1",
			Status:    "online",
			Owner:     &hub.AgentOwner{HumanID: "human-1"},
			Metadata: &hub.AgentMetadata{
				DisplayName: "Agent One",
				HireMe:      &hireMe,
				Presence:    &hub.AgentPresence{Ready: &ready, Transport: "ws"},
			},
		},
	)
	if merged.AgentUUID != "uuid-1" || merged.Owner == nil || merged.Metadata.DisplayName != "Agent One" {
		t.Fatalf("merged agent = %#v", merged)
	}
	if merged.Metadata.Presence == nil || merged.Metadata.Presence.Ready == nil || !*merged.Metadata.Presence.Ready {
		t.Fatalf("merged presence = %#v, want ready", merged.Metadata.Presence)
	}

	if got := mergeConnectedAgentMetadata(nil, nil); got != nil {
		t.Fatalf("nil metadata merge = %#v, want nil", got)
	}
	if got := mergeConnectedAgentMetadata(nil, &hub.AgentMetadata{DisplayName: "Only Secondary"}); got == nil || got.DisplayName != "Only Secondary" {
		t.Fatalf("secondary-only metadata merge = %#v", got)
	}
	if got := mergeConnectedAgentMetadata(&hub.AgentMetadata{DisplayName: "Only Primary"}, nil); got == nil || got.DisplayName != "Only Primary" {
		t.Fatalf("primary-only metadata merge = %#v", got)
	}
	if got := mergeConnectedAgentPresence(&hub.AgentPresence{}, &hub.AgentPresence{}); got != nil {
		t.Fatalf("empty presence merge = %#v, want nil", got)
	}
	if got := mergeConnectedAgentPresence(nil, &hub.AgentPresence{Status: "online"}); got == nil || got.Status != "online" {
		t.Fatalf("secondary-only presence merge = %#v", got)
	}
	if got := mergeConnectedAgentPresence(&hub.AgentPresence{Status: "offline"}, nil); got == nil || got.Status != "offline" {
		t.Fatalf("primary-only presence merge = %#v", got)
	}

	mapsCases := []any{
		[]map[string]any{{}, {"agent_id": "a"}},
		[]any{"skip", map[string]any{"agent_id": "b"}},
		map[string]any{"agent": map[string]any{"handle": "nested"}},
		map[string]any{"first": map[string]any{"agent_id": "c"}},
	}
	for _, raw := range mapsCases {
		if got := mapsFromAny(raw); len(got) != 1 {
			t.Fatalf("mapsFromAny(%#v) len = %d, want 1", raw, len(got))
		}
	}
	if got := mapsFromAny("unsupported"); got != nil {
		t.Fatalf("mapsFromAny unsupported = %#v, want nil", got)
	}

	for _, entry := range []map[string]any{
		{"agent_uuid": "uuid"},
		{"peer_agent": map[string]any{"handle": "agent"}},
	} {
		if !looksLikeCapabilityPeerEntry(entry) {
			t.Fatalf("entry should look like peer: %#v", entry)
		}
	}
	if looksLikeCapabilityPeerEntry(map[string]any{"name": "not enough"}) {
		t.Fatal("entry without identity should not look like peer")
	}

	root := map[string]any{
		"controlPlane": map[string]any{
			"talkablePeers": []any{map[string]any{"agent_id": "peer-1"}},
		},
	}
	if got := capabilityTalkablePeerEntries(root, "controlPlane"); len(got) != 1 {
		t.Fatalf("talkable peers = %#v, want one", got)
	}
	if got := capabilityTalkablePeerEntries(nil, "controlPlane"); got != nil {
		t.Fatalf("nil talkable peers = %#v, want nil", got)
	}

	sources := capabilityStringSources(map[string]any{
		"avatar": map[string]any{"native": ":)"},
		"metadata": map[string]any{
			"displayName": "Agent",
		},
	})
	if got := capabilityEmoji(sources); got != ":)" {
		t.Fatalf("capabilityEmoji = %q, want :)", got)
	}
	if got := firstCapabilityString([]map[string]any{nil, {"name": "agent"}}, "name"); got != "agent" {
		t.Fatalf("firstCapabilityString = %q, want agent", got)
	}
	identity := existingAgentIdentityFromCapabilities(map[string]any{
		"result": map[string]any{
			"agent": map[string]any{
				"uuid":        "self-uuid",
				"uri":         "molten://self",
				"displayName": "Self",
				"description": "Profile",
			},
		},
	})
	if identity.AgentUUID != "self-uuid" || identity.ProfileMarkdown != "Profile" {
		t.Fatalf("identity = %#v", identity)
	}

	capabilities := map[string]any{
		"peer_skill_catalog": []any{
			map[string]any{"agent_id": "self"},
			map[string]any{"agent_id": "peer", "metadata": map[string]any{"skills": []any{"one"}}},
			map[string]any{"handle": "peer", "display_name": "Peer Updated"},
			map[string]any{"name": "no identity"},
		},
	}
	agents := connectedAgentsFromCapabilities(capabilities, AppState{Session: Session{Handle: "self"}})
	if len(agents) != 1 || ConnectedAgentDisplayName(agents[0]) != "Peer Updated" {
		t.Fatalf("connected agents = %#v, want merged peer only", agents)
	}

	notReady := false
	if presence := connectedAgentPresenceFromCapabilitySources([]map[string]any{{"presence": map[string]any{"ready": ready}}}); presence == nil || presence.Status != "online" {
		t.Fatalf("ready presence = %#v", presence)
	}
	if presence := connectedAgentPresenceFromCapabilitySources([]map[string]any{{"presence": map[string]any{"ready": notReady}}}); presence == nil || presence.Status != "offline" {
		t.Fatalf("not-ready presence = %#v", presence)
	}
	if presence := connectedAgentPresenceFromCapabilitySources([]map[string]any{{"presence": map[string]any{}}, {"status": "available"}}); presence == nil || presence.Status != "online" {
		t.Fatalf("fallback status presence = %#v", presence)
	}
	if status := connectedAgentStatusFromCapabilitySources([]map[string]any{{"status": "mystery"}}, nil); status != "mystery" {
		t.Fatalf("custom status = %q", status)
	}
	if got := SkillsToMetadata([]Skill{{}, {Name: " review ", Description: " Review code "}}); len(got) != 1 || got[0]["description"] != "Review code" {
		t.Fatalf("skills metadata = %#v", got)
	}
	if !metadataEmpty(nil) || metadataEmpty(&hub.AgentMetadata{Activities: []any{"busy"}}) {
		t.Fatal("metadataEmpty mismatch")
	}
	for _, raw := range []any{
		[]any{map[string]any{"name": "map"}, map[string]any{"name": " "}},
		[]map[string]any{{"name": "mapped"}},
		[]Skill{{Name: " skill "}, {}},
	} {
		if got := skillsFromAny(raw); len(got) == 0 {
			t.Fatalf("skillsFromAny(%#v) empty", raw)
		}
	}
}

func TestAdditionalDispatchPayloadHelpers(t *testing.T) {
	req, err := DispatchRequestFromPayload(nil)
	if err != nil {
		t.Fatalf("nil payload: %v", err)
	}
	if req.TargetAgentRef != "" || req.SkillName != "" || req.Payload != nil || len(req.LogPaths) != 0 || !req.ScheduledAt.IsZero() || req.Frequency != 0 {
		t.Fatalf("nil payload request = %#v, want zero", req)
	}
	if _, err := DispatchRequestFromPayload(map[string]any{"payload": make(chan int)}); err == nil {
		t.Fatal("expected marshal error")
	}
	if _, err := DispatchRequestFromPayload(`{"payload":"{bad","payload_format":"json"}`); err == nil {
		t.Fatal("expected JSON payload error")
	}

	var payload dispatchPayload
	if err := payload.FromAny([]byte(" ")); err != nil {
		t.Fatalf("blank bytes payload: %v", err)
	}
	if err := payload.FromAny(json.RawMessage(`{"agent":"a","skill_name":"s","message":"hi"}`)); err != nil {
		t.Fatalf("raw message payload: %v", err)
	}

	scheduledAt, frequency := scheduleFromAny(map[string]any{"after": "1s", "every": json.Number("2")})
	if scheduledAt.IsZero() || frequency != 2*time.Second {
		t.Fatalf("scheduleFromAny map = %v, %v", scheduledAt, frequency)
	}
	if got, _ := scheduleFromAny("2000-01-02T03:04:05Z"); got.IsZero() {
		t.Fatal("scheduleFromAny string should parse time")
	}

	for _, raw := range []any{
		time.Date(2000, 1, 2, 3, 4, 5, 0, time.FixedZone("test", 3600)),
		"2000-01-02 03:04:05",
		"2000-01-02 03:04",
		json.Number("946684800"),
		float64(946684800),
		int64(946684800),
		int(946684800),
	} {
		if got := timeFromAny(raw); got.IsZero() {
			t.Fatalf("timeFromAny(%#v) returned zero", raw)
		}
	}
	for _, raw := range []any{" ", "not-time", json.Number("0"), float64(0), int64(0), int(0), true} {
		if got := timeFromAny(raw); !got.IsZero() {
			t.Fatalf("timeFromAny(%#v) = %v, want zero", raw, got)
		}
	}

	durationCases := map[any]time.Duration{
		time.Minute:      time.Minute,
		"2":              2 * time.Second,
		json.Number("3"): 3 * time.Second,
		float64(4):       4 * time.Second,
		int64(5):         5 * time.Second,
		int(6):           6 * time.Second,
	}
	for raw, want := range durationCases {
		if got := durationFromAny(raw); got != want {
			t.Fatalf("durationFromAny(%#v) = %v, want %v", raw, got, want)
		}
	}
	for _, raw := range []any{" ", "bad", true} {
		if got := durationFromAny(raw); got != 0 {
			t.Fatalf("durationFromAny(%#v) = %v, want 0", raw, got)
		}
	}

	if got, err := normalizeDispatchTaskPayload(" ", "json"); err != nil || got != nil {
		t.Fatalf("blank JSON string = %#v, %v; want nil, nil", got, err)
	}
	if got, err := normalizeDispatchTaskPayload([]byte(" "), "json"); err != nil || got != nil {
		t.Fatalf("blank JSON bytes = %#v, %v; want nil, nil", got, err)
	}
	if _, err := normalizeDispatchTaskPayload(json.RawMessage(`{bad`), "json"); err == nil {
		t.Fatal("invalid JSON raw message expected error")
	}

	task := PendingTask{ID: "task", ChildRequestID: "child", HubTaskID: "hub", ParentRequestID: "parent", CallerRequestID: "caller"}
	for _, id := range []string{"task", "child", "hub", "parent", "caller"} {
		if !pendingTaskMatchesID(task, id) {
			t.Fatalf("task should match %q", id)
		}
	}
	if pendingTaskMatchesID(task, " ") || pendingTaskMatchesID(task, "missing") {
		t.Fatal("task should not match blank or missing id")
	}
}

func TestAdditionalStatusAndFailureHelpers(t *testing.T) {
	statusMessage := hub.OpenClawMessage{
		StatusUpdate: map[string]any{
			"status": map[string]any{
				"state":   "completed",
				"message": map[string]any{"parts": []any{map[string]any{"text": "done"}}},
			},
		},
	}
	if got := statusUpdateTaskState(statusMessage); got != "TASK_STATE_COMPLETED" {
		t.Fatalf("statusUpdateTaskState = %q, want completed", got)
	}
	if got := statusUpdateMessageText(statusMessage); got != "done" {
		t.Fatalf("statusUpdateMessageText = %q, want done", got)
	}
	statusMessage.StatusUpdate = map[string]any{"status": map[string]any{"message": "plain"}}
	if got := statusUpdateMessageText(statusMessage); got != "plain" {
		t.Fatalf("plain status message = %q", got)
	}
	if got := statusUpdateMessageText(hub.OpenClawMessage{}); got != "" {
		t.Fatalf("empty status message = %q", got)
	}
	if got := a2aMessageText(nil); got != "" {
		t.Fatalf("nil a2a message text = %q", got)
	}
	if got := a2aMessageText(map[string]any{"text": " direct "}); got != "direct" {
		t.Fatalf("direct a2a message text = %q", got)
	}
	if got := a2aMessageText(map[string]any{"parts": []any{map[string]any{"text": " part "}}}); got != "part" {
		t.Fatalf("part a2a message text = %q", got)
	}
	if got := a2aMessageText(map[string]any{"parts": []any{map[string]any{"data": "none"}}}); got != "" {
		t.Fatalf("missing a2a text = %q", got)
	}
	if got := statusUpdateTaskState(hub.OpenClawMessage{A2AState: "in-progress"}); got != "TASK_STATE_WORKING" {
		t.Fatalf("a2a state = %q, want working", got)
	}
	if got := statusUpdateTaskState(hub.OpenClawMessage{Status: "mystery"}); got != "TASK_STATE_WORKING" {
		t.Fatalf("default task state = %q, want working", got)
	}
	if got := normalizeA2ATaskState("TASK_STATE_REJECTED"); got != "TASK_STATE_REJECTED" {
		t.Fatalf("prefixed state = %q", got)
	}
	for _, raw := range []string{"queued", "running", "done", "timeout", "aborted", "duplicate"} {
		if got := normalizeA2ATaskState(raw); got == "" {
			t.Fatalf("normalizeA2ATaskState(%q) returned empty", raw)
		}
	}
	for _, status := range []string{"queued", "waiting", "success", "timeout", "cancelled", "duplicate"} {
		if got := taskStateFromStatus(status); got == "" {
			t.Fatalf("taskStateFromStatus(%q) returned empty", status)
		}
	}
	if level, title := taskStatusUpdateEventLevelTitle("failed"); level != "error" || title != "Task failed" {
		t.Fatalf("failed event = %q, %q", level, title)
	}
	if level, title := taskStatusUpdateEventLevelTitle("completed"); level != "info" || title != "Task completed" {
		t.Fatalf("completed event = %q, %q", level, title)
	}

	ok := true
	notOK := false
	successMessages := []hub.OpenClawMessage{
		{OK: &ok},
		{Status: "completed"},
		{Payload: map[string]any{"ok": true}},
	}
	for _, message := range successMessages {
		if !messageSucceeded(message) {
			t.Fatalf("message should succeed: %#v", message)
		}
	}
	failedMessages := []hub.OpenClawMessage{
		{OK: &notOK},
		{Status: "failed"},
		{Error: "boom"},
		{Payload: map[string]any{"error": "boom"}},
		{Payload: "panic: boom"},
	}
	for _, message := range failedMessages {
		if messageSucceeded(message) {
			t.Fatalf("message should fail: %#v", message)
		}
	}

	apiErr := &hub.APIError{StatusCode: 503, Code: "down", Message: "unavailable", Retryable: true, NextAction: "retry", Detail: map[string]any{"id": "1"}}
	report := failureFromError("", apiErr)
	if !report.Retryable || report.NextAction != "retry" || report.Message == "" {
		t.Fatalf("failureFromError = %#v", report)
	}
	report = failureFromMessage(hub.OpenClawMessage{
		Error:       "",
		ErrorDetail: "detail",
		Payload: map[string]any{
			"message":      "bad result",
			"error_detail": map[string]any{"stderr": "stack"},
			"retryable":    true,
			"next_action":  "inspect",
		},
	})
	if report.Message != "bad result" || report.Error != "stack" || !report.Retryable || report.NextAction != "inspect" {
		t.Fatalf("failureFromMessage = %#v", report)
	}

	callerCases := []failureReport{
		{},
		{Message: "cannot run"},
		{Error: "boom"},
		{Message: "Task failed.", Error: "Task failed."},
		{Message: "Bad!", Error: "boom"},
	}
	for _, report := range callerCases {
		if got := callerFailureError(report); strings.TrimSpace(got) == "" {
			t.Fatalf("callerFailureError(%#v) empty", report)
		}
	}

	failurePayloads := []map[string]any{
		{"status": "failed"},
		{"ok": false},
		{"failure": true},
		{"stderr": "boom"},
		{"exit_code": json.Number("1")},
		{"error_details": "boom"},
		{"failure": map[string]any{"error": "nested"}},
		{"output": "check your internet connection: githubstatus.com"},
	}
	for _, payload := range failurePayloads {
		if !payloadMapIndicatesFailure(payload) {
			t.Fatalf("payload should indicate failure: %#v", payload)
		}
	}
	if !payloadMapIndicatesSuccess(map[string]any{"status": "ok"}) || !payloadMapIndicatesSuccess(map[string]any{"success": "yes"}) {
		t.Fatal("success payloads should indicate success")
	}
	if payloadStringIndicatesFailure(" ") {
		t.Fatal("blank text should not indicate failure")
	}

	if got := normalizePendingTaskStatus(PendingTaskStatusSending); got != PendingTaskStatusSending {
		t.Fatalf("sending status = %q", got)
	}
	if got := normalizePendingTaskStatus(""); got != PendingTaskStatusInQueue {
		t.Fatalf("blank status = %q", got)
	}
	if got := normalizePendingTaskStatus("unknown"); got != PendingTaskStatusInQueue {
		t.Fatalf("unknown status = %q", got)
	}
}

func TestAdditionalSchedulerHelpers(t *testing.T) {
	service, fake := newTestService(t)
	if err := service.DeleteScheduledMessage(" "); err == nil {
		t.Fatal("blank schedule id expected error")
	}
	if err := service.DeleteScheduledMessage("missing"); err == nil {
		t.Fatal("missing schedule id expected error")
	}

	now := time.Now().UTC()
	if _, err := service.scheduleDispatch(AppState{}, ConnectedAgent{AgentID: "a"}, DispatchRequest{Frequency: -time.Second}, "", ""); err == nil {
		t.Fatal("negative frequency expected error")
	}
	if _, err := service.scheduleDispatch(AppState{}, ConnectedAgent{AgentID: "a"}, DispatchRequest{Frequency: 1500 * time.Millisecond}, "", ""); err == nil {
		t.Fatal("sub-second frequency expected error")
	}

	scheduled, err := service.scheduleDispatch(AppState{}, ConnectedAgent{AgentID: "a", DisplayName: "Agent"}, DispatchRequest{
		RequestID:      "req",
		TargetAgentRef: "a",
		SkillName:      "review",
		ScheduledAt:    now.Add(time.Hour),
		Frequency:      time.Minute,
		Payload:        "hi",
		PayloadFormat:  "text",
		PreferA2A:      true,
	}, "caller-uuid", "caller-uri")
	if err != nil {
		t.Fatalf("scheduleDispatch: %v", err)
	}
	if err := service.DeleteScheduledMessage(scheduled.ID); err != nil {
		t.Fatalf("DeleteScheduledMessage: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service.RunSchedulerLoop(ctx)

	target, err := scheduledDispatchTarget(AppState{ConnectedAgents: []ConnectedAgent{{AgentID: "a"}}}, ScheduledMessage{TargetAgentRef: "a"})
	if err != nil || target.AgentID != "a" {
		t.Fatalf("scheduledDispatchTarget existing = %#v, %v", target, err)
	}
	target, err = scheduledDispatchTarget(AppState{}, ScheduledMessage{TargetAgentRef: "molten://agent/1"})
	if err != nil || target.URI != "molten://agent/1" {
		t.Fatalf("scheduledDispatchTarget URI = %#v, %v", target, err)
	}
	if _, err := scheduledDispatchTarget(AppState{}, ScheduledMessage{TargetAgentRef: "missing"}); err == nil {
		t.Fatal("missing scheduled target expected error")
	}

	pending := pendingFromScheduledMessage(ScheduledMessage{
		ID:                     "schedule",
		ParentRequestID:        "parent",
		OriginalSkillName:      "review",
		TargetAgentDisplayName: "Agent",
		TargetAgentUUID:        "uuid",
		TargetAgentURI:         "uri",
		CallerRequestID:        "caller",
		DispatchPayload:        map[string]any{"input": "hi"},
		PreferA2A:              true,
	}, AppState{Settings: Settings{DataDir: t.TempDir()}})
	if pending.ID == "" || pending.LogPath == "" || !pending.PreferA2A {
		t.Fatalf("pendingFromScheduledMessage = %#v", pending)
	}

	if err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "token"
		state.ConnectedAgents = []ConnectedAgent{{AgentID: "a"}}
		state.ScheduledMessages = []ScheduledMessage{
			{ID: "due-missing-skill", TargetAgentRef: "a", NextRunAt: now.Add(-time.Second)},
			{ID: "future", TargetAgentRef: "a", OriginalSkillName: "review", NextRunAt: now.Add(time.Hour)},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed schedules: %v", err)
	}
	if err := service.processDueScheduledMessages(context.Background()); err == nil {
		t.Fatal("due schedule missing skill expected error")
	}
	if len(fake.publishCalls) != 0 {
		t.Fatalf("publish calls = %d, want 0", len(fake.publishCalls))
	}

	delayService, _ := newTestService(t)
	if got := delayService.nextScheduleDelay(); got < time.Second {
		t.Fatalf("empty schedule delay = %v, want at least one second", got)
	}
	if err := delayService.store.Update(func(state *AppState) error {
		state.ScheduledMessages = []ScheduledMessage{{ID: "zero"}}
		return nil
	}); err != nil {
		t.Fatalf("seed zero schedule: %v", err)
	}
	if got := delayService.nextScheduleDelay(); got != 0 {
		t.Fatalf("zero next run delay = %v, want 0", got)
	}

	cronCases := map[time.Duration]string{
		time.Second:             "*/1 * * * * *",
		time.Minute:             "*/1 * * * *",
		time.Hour:               "0 */1 * * *",
		24 * time.Hour:          "0 0 */1 * *",
		32 * 24 * time.Hour:     "",
		24 * time.Hour * 60:     "",
		1500 * time.Millisecond: "",
	}
	for duration, want := range cronCases {
		if got := cronFromDuration(duration); got != want {
			t.Fatalf("cronFromDuration(%v) = %q, want %q", duration, got, want)
		}
	}
	durationCronCases := map[string]time.Duration{
		"*/5 * * * * *": 5 * time.Second,
		"*/6 * * * *":   6 * time.Minute,
		"0 */7 * * *":   7 * time.Hour,
		"0 0 */8 * *":   8 * 24 * time.Hour,
		"bad":           0,
		"*/x * * * *":   0,
	}
	for raw, want := range durationCronCases {
		if got := durationFromCron(raw); got != want {
			t.Fatalf("durationFromCron(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestAdditionalRuntimeCatalogHelpers(t *testing.T) {
	if got := hubHostForRegion(" "); got != "" {
		t.Fatalf("blank region host = %q, want empty", got)
	}
	if got := hubURLForRegion(" "); got != "" {
		t.Fatalf("blank region URL = %q, want empty", got)
	}
	if got := normalizeHubRuntimeURL("https://evil.example"); got != "" {
		t.Fatalf("non-hub runtime URL = %q, want empty", got)
	}
	if got := catalogHubURL("na.hub.molten.bot:443"); got != "" {
		t.Fatalf("catalog URL with port = %q, want empty", got)
	}

	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body: io.NopCloser(strings.NewReader(`[
				{"key":"na","display":"North America","domain":"na.hub.molten.bot"},
				{"key":"na","display":"Duplicate","domain":"na.hub.molten.bot"},
				{"key":"","display":"Invalid","domain":"na.hub.molten.bot"},
				{"key":"bad","display":"Invalid","domain":"example.com"}
			]`)),
		}, nil
	})}
	runtimes, err := fetchHubRuntimeCatalog("https://catalog.test/hubs.json", client)
	if err != nil {
		t.Fatalf("fetchHubRuntimeCatalog: %v", err)
	}
	if len(runtimes) != 1 || runtimes[0].ID != "na" {
		t.Fatalf("runtimes = %#v, want unique na runtime", runtimes)
	}

	errorClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	})}
	if _, err := fetchHubRuntimeCatalog("https://catalog.test/hubs.json", errorClient); err == nil {
		t.Fatal("network error expected")
	}
	statusClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway", Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	if _, err := fetchHubRuntimeCatalog("https://catalog.test/hubs.json", statusClient); err == nil {
		t.Fatal("non-200 status expected error")
	}
	badJSONClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader("{bad"))}, nil
	})}
	if _, err := fetchHubRuntimeCatalog("https://catalog.test/hubs.json", badJSONClient); err == nil {
		t.Fatal("bad JSON expected error")
	}
	emptyClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`[{"key":"x","display":"","domain":"example.com"}]`))}, nil
	})}
	if _, err := fetchHubRuntimeCatalog("https://catalog.test/hubs.json", emptyClient); err == nil {
		t.Fatal("empty runtime catalog expected error")
	}
	if _, err := fetchHubRuntimeCatalog("://bad", client); err == nil {
		t.Fatal("bad catalog URL expected error")
	}
}

func TestAdditionalServiceURLHelpers(t *testing.T) {
	bind := hub.BindResponse{APIBase: "https://na.hub.molten.bot/v1"}
	bind.Endpoints.Manifest = "https://na.hub.molten.bot/v1/agents/me/manifest"
	bind.Endpoints.Capabilities = "https://na.hub.molten.bot/v1/agents/me/capabilities"
	bind.Endpoints.Metadata = "https://na.hub.molten.bot/v1/agents/me/metadata"
	bind.Endpoints.RuntimePull = "https://na.hub.molten.bot/v1/runtime/messages/pull"
	if got := runtimeAPIBaseFromBind(bind); got != "https://na.hub.molten.bot/v1" {
		t.Fatalf("runtimeAPIBaseFromBind = %q", got)
	}

	if got := defaultAPIBaseForHub(" "); got != "" {
		t.Fatalf("defaultAPIBaseForHub blank = %q", got)
	}
	endpointCases := map[string]string{
		"https://na.hub.molten.bot/v1/runtime/messages/{message_id}": "https://na.hub.molten.bot",
		"https://na.hub.molten.bot/runtime/profile":                  "https://na.hub.molten.bot",
		"https://na.hub.molten.bot/other/path?x=1#frag":              "https://na.hub.molten.bot",
		"%": "",
	}
	for raw, want := range endpointCases {
		if got := runtimeAPIBaseFromEndpoint(raw); got != want {
			t.Fatalf("runtimeAPIBaseFromEndpoint(%q) = %q, want %q", raw, got, want)
		}
	}

	invalid := invalidRuntimeEndpoints(hub.RuntimeEndpoints{
		ManifestURL: "https://example.com/manifest",
	})
	if len(invalid) != 1 || !strings.Contains(invalid[0], "manifest") {
		t.Fatalf("invalid endpoints = %#v", invalid)
	}
	if base, domain := hubConnectionTarget("://bad", "https://na.hub.molten.bot/v1"); base != "https://na.hub.molten.bot/v1" || domain != "na.hub.molten.bot" {
		t.Fatalf("fallback connection target = %q %q", base, domain)
	}
	if base, domain := hubConnectionTarget("://bad", "://bad"); base != "" || domain != "" {
		t.Fatalf("invalid connection target = %q %q", base, domain)
	}
}

func TestAdditionalServiceStateHelpers(t *testing.T) {
	service, fake := newTestService(t)
	if flash, err := service.ConsumeFlash(); err != nil || flash.Message != "" {
		t.Fatalf("empty ConsumeFlash = %#v, %v", flash, err)
	}
	if err := service.SetFlash("error", " boom "); err != nil {
		t.Fatalf("SetFlash: %v", err)
	}
	if flash, err := service.ConsumeFlash(); err != nil || flash.Level != "error" || flash.Message != "boom" {
		t.Fatalf("ConsumeFlash = %#v, %v", flash, err)
	}
	if err := service.SetFlash("info", " "); err != nil {
		t.Fatalf("clear flash: %v", err)
	}
	if got := service.store.Snapshot().Flash.Message; got != "" {
		t.Fatalf("cleared flash = %q", got)
	}

	if err := service.UpdateSettings(func(*Settings) error { return errors.New("bad settings") }); err == nil {
		t.Fatal("UpdateSettings mutator error expected")
	}
	if err := service.UpdateSettings(func(settings *Settings) error {
		settings.HubRegion = "bad"
		settings.HubURL = ""
		return nil
	}); err == nil {
		t.Fatal("UpdateSettings invalid runtime expected")
	}
	if len(fake.baseURLCalls) == 0 {
		t.Fatal("expected configureHubClient base URL calls")
	}
	service.setHubBaseURL(" ")

	connectedAt := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	if err := service.storeConnectedSession(DefaultHubRuntime(), Session{BoundAt: connectedAt, AgentToken: "token", APIBase: "https://na.hub.molten.bot/v1"}); err != nil {
		t.Fatalf("storeConnectedSession: %v", err)
	}
	if got := service.store.Snapshot().Session.BoundAt; !got.Equal(connectedAt) {
		t.Fatalf("BoundAt = %v, want %v", got, connectedAt)
	}

	if err := service.DisconnectAgent(context.Background()); err != nil {
		t.Fatalf("DisconnectAgent: %v", err)
	}
	if got := service.store.Snapshot().Session.AgentToken; got != "" {
		t.Fatalf("disconnect token = %q", got)
	}
}

func TestAdditionalHubLoopHelpers(t *testing.T) {
	service, fake := newTestService(t)
	if err := service.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce unbound: %v", err)
	}
	if err := service.pollOnceWithTimeout(contextCanceled()); !errors.Is(err, context.Canceled) {
		t.Fatalf("pollOnceWithTimeout canceled = %v", err)
	}

	if err := service.store.Update(func(state *AppState) error {
		state.Session.AgentToken = "token"
		state.Settings.PollInterval = 0
		return nil
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if got := service.pollInterval(); got != 2*time.Second {
		t.Fatalf("default poll interval = %v", got)
	}
	if err := service.MarkOnline(context.Background(), ConnectionTransportWebSocket); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}
	if !service.presenceSynced || service.presenceTransport != ConnectionTransportWebSocket {
		t.Fatalf("presence sync = %v/%q", service.presenceSynced, service.presenceTransport)
	}
	if err := service.syncPresenceTransport(context.Background(), ConnectionTransportWebSocket); err != nil {
		t.Fatalf("syncPresenceTransport already synced: %v", err)
	}
	if err := service.MarkOffline(context.Background(), "manual"); err != nil {
		t.Fatalf("MarkOffline: %v", err)
	}
	if len(fake.offlineCalls) == 0 {
		t.Fatal("expected offline call")
	}
	if err := service.MarkOffline(context.Background(), "again"); err != nil {
		t.Fatalf("MarkOffline already offline: %v", err)
	}

	service.noteRealtimeFallback(errors.New("ws closed"))
	if got := service.store.Snapshot().Connection.Transport; got != ConnectionTransportReachable {
		t.Fatalf("fallback transport = %q", got)
	}
	service.noteHubPingRetrying(errors.New("no route"), 0)
	if got := service.store.Snapshot().Connection.Transport; got != ConnectionTransportRetrying {
		t.Fatalf("retrying transport = %q", got)
	}
	service.noteHubPingReachable("ok")
	if got := service.store.Snapshot().Connection.Transport; got != ConnectionTransportReachable {
		t.Fatalf("reachable transport = %q", got)
	}

	if !canDeferHubDisconnect(ConnectionState{Transport: ConnectionTransportHTTP}) {
		t.Fatal("HTTP transport should defer disconnect")
	}
	if canDeferHubDisconnect(ConnectionState{Transport: ConnectionTransportOffline}) {
		t.Fatal("offline transport should not defer disconnect")
	}
	if shouldFallbackToLongPoll(nil) {
		t.Fatal("nil error should not fallback")
	}
	if !shouldFallbackToLongPoll(errors.New("broken pipe")) {
		t.Fatal("broken pipe should fallback")
	}
	if isUnauthorizedHubError(nil) {
		t.Fatal("nil unauthorized error")
	}
	if !isUnauthorizedHubError(&hub.APIError{StatusCode: http.StatusForbidden}) || !isUnauthorizedHubError(errors.New("status=401")) {
		t.Fatal("unauthorized errors not detected")
	}
	if !hubReachable(&hub.APIError{StatusCode: http.StatusInternalServerError}) || hubReachable(errors.New("network down")) {
		t.Fatal("hubReachable mismatch")
	}
	if !sleepWithContext(context.Background(), time.Nanosecond) {
		t.Fatal("sleep should complete")
	}
	if got := hubPingFailureDetail(nil, 0); !strings.Contains(got, hubPingRetryInterval.String()) {
		t.Fatalf("hub ping detail = %q", got)
	}
}

func TestAdditionalStoreBranches(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, []byte(`{"settings":{"hub_region":"na"},"session":{"agent_token":"token"}}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if got, err := ResolveStorePath(dir); err != nil || got != config {
		t.Fatalf("ResolveStorePath config = %q, %v", got, err)
	}

	legacyDir := t.TempDir()
	legacy := filepath.Join(legacyDir, "state.json")
	if err := os.WriteFile(legacy, []byte(`{"session":{"bind_token":"legacy-token"}}`), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	if got, err := ResolveStorePath(legacyDir); err != nil || got != filepath.Join(legacyDir, "config.json") {
		t.Fatalf("ResolveStorePath legacy = %q, %v", got, err)
	}

	filePath := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := ResolveStorePath(filePath); err == nil {
		t.Fatal("ResolveStorePath file path expected error")
	}

	emptyPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(emptyPath, nil, 0o644); err != nil {
		t.Fatalf("write empty config: %v", err)
	}
	store, err := NewStore(emptyPath, DefaultSettings())
	if err != nil {
		t.Fatalf("NewStore empty: %v", err)
	}
	if err := store.Update(func(*AppState) error { return errors.New("stop") }); err == nil {
		t.Fatal("Store.Update callback error expected")
	}
	if err := store.AppendEvent(RuntimeEvent{Title: "one"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	badPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(badPath, []byte("{bad"), 0o644); err != nil {
		t.Fatalf("write bad config: %v", err)
	}
	if _, err := NewStore(badPath, DefaultSettings()); err == nil {
		t.Fatal("NewStore bad JSON expected error")
	}
	if _, err := NewStore(filepath.Join(filePath, "config.json"), DefaultSettings()); err == nil {
		t.Fatal("NewStore mkdir error expected")
	}

	agents := AddOrReplaceConnectedAgent(nil, ConnectedAgent{})
	if len(agents) != 0 {
		t.Fatalf("blank add agent = %#v", agents)
	}
	agents = AddOrReplaceConnectedAgent(nil, ConnectedAgent{AgentID: "a", DisplayName: "one"})
	agents = AddOrReplaceConnectedAgent(agents, ConnectedAgent{AgentID: "a", DisplayName: "two"})
	if len(agents) != 1 || agents[0].DisplayName != "two" {
		t.Fatalf("replace agent = %#v", agents)
	}
	if got := RemovePendingTask([]PendingTask{{ChildRequestID: "a"}, {ChildRequestID: "b"}}, "missing"); len(got) != 2 {
		t.Fatalf("RemovePendingTask missing = %#v", got)
	}
}

func contextCanceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
