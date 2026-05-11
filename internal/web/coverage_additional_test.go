package web

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/app"
)

func newAdditionalTestServer(t *testing.T, stub *stubService) *Server {
	t.Helper()
	server, err := New(stub)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return server
}

func serveAdditional(t *testing.T, server *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	return rec
}

func TestAdditionalServerRoutesAndMethodBranches(t *testing.T) {
	stub := &stubService{state: app.AppState{
		Settings: app.DefaultSettings(),
		Session:  app.Session{AgentToken: "token", Handle: "dispatch", DisplayName: "Dispatch"},
		Connection: app.ConnectionState{
			Status:    app.ConnectionStatusConnected,
			Transport: app.ConnectionTransportWebSocket,
			BaseURL:   "https://na.hub.molten.bot/v1",
		},
		ConnectedAgents: []app.ConnectedAgent{
			testConnectedAgent("agent-1", "Agent One", "uuid-1", "molten://agent/1", app.Skill{Name: "review"}),
		},
		ScheduledMessages: []app.ScheduledMessage{{ID: "schedule-1", Status: app.ScheduledMessageStatusActive}},
	}}
	server := newAdditionalTestServer(t, stub)

	methodCases := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodPost, "/", http.StatusMethodNotAllowed},
		{http.MethodPost, "/status", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/api/onboarding", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/connected-agents", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/profile", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/dispatch", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/schedules/delete", http.StatusMethodNotAllowed},
		{http.MethodGet, "/bind", http.StatusNotFound},
		{http.MethodGet, "/profile", http.StatusNotFound},
		{http.MethodGet, "/disconnect", http.StatusNotFound},
		{http.MethodGet, "/agents", http.StatusNotFound},
		{http.MethodGet, "/dispatch", http.StatusNotFound},
		{http.MethodGet, "/schedules/delete", http.StatusNotFound},
		{http.MethodGet, "/settings", http.StatusNotFound},
	}
	for _, tc := range methodCases {
		rec := serveAdditional(t, server, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.want {
			t.Fatalf("%s %s = %d, want %d body=%s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}

	if rec := serveAdditional(t, server, httptest.NewRequest(http.MethodGet, "/styles.css", nil)); rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("styles response = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := serveAdditional(t, server, httptest.NewRequest(http.MethodHead, "/status", nil)); rec.Code != http.StatusOK {
		t.Fatalf("HEAD /status = %d", rec.Code)
	}
	if rec := serveAdditional(t, server, httptest.NewRequest(http.MethodGet, "/api/onboarding", nil)); rec.Code != http.StatusOK {
		t.Fatalf("GET onboarding = %d", rec.Code)
	}

	stub.refreshAgentsErr = errors.New("hub unavailable")
	rec := serveAdditional(t, server, httptest.NewRequest(http.MethodGet, "/api/connected-agents", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("connected agents error = %d", rec.Code)
	}
	stub.refreshAgentsErr = nil
	stub.refreshProfileErr = errors.New("profile unavailable")
	rec = serveAdditional(t, server, httptest.NewRequest(http.MethodGet, "/api/profile", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("profile error = %d", rec.Code)
	}
}

func TestAdditionalFormHandlers(t *testing.T) {
	stub := &stubService{
		state: app.AppState{Settings: app.DefaultSettings()},
		dispatchTask: app.PendingTask{
			ID:     "task-1",
			Status: app.PendingTaskStatusInQueue,
		},
	}
	server := newAdditionalTestServer(t, stub)

	form := url.Values{
		"agent_uuid": []string{"uuid-1"},
		"id":         []string{"agent-1"},
		"name":       []string{"Agent One"},
		"skills":     []string{"review:Review code"},
	}
	req := httptest.NewRequest(http.MethodPost, "/agents", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := serveAdditional(t, server, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /agents = %d body=%s", rec.Code, rec.Body.String())
	}
	if stub.lastFlashMessage != "Connected agent saved." {
		t.Fatalf("agent flash = %q", stub.lastFlashMessage)
	}

	dispatchForm := url.Values{
		"target_agent_ref": []string{"agent-1"},
		"skill_name":       []string{"review"},
		"payload":          []string{"inspect"},
	}
	req = httptest.NewRequest(http.MethodPost, "/dispatch", strings.NewReader(dispatchForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = serveAdditional(t, server, req)
	if rec.Code != http.StatusSeeOther || stub.lastDispatchReq.SkillName != "review" {
		t.Fatalf("POST /dispatch = %d req=%#v", rec.Code, stub.lastDispatchReq)
	}
	if got := dispatchSuccessMessage(app.PendingTask{ID: "schedule-1", Status: app.ScheduledMessageStatusActive}); got != "Scheduled message schedule-1" {
		t.Fatalf("scheduled success message = %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(url.Values{"hub_region": []string{"eu"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = serveAdditional(t, server, req)
	if rec.Code != http.StatusSeeOther || stub.state.Settings.HubRegion != app.HubRegionEU {
		t.Fatalf("POST /settings = %d region=%q", rec.Code, stub.state.Settings.HubRegion)
	}
	stub.updateSettingsErr = errors.New("cannot save")
	req = httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(url.Values{"hub_region": []string{"na"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = serveAdditional(t, server, req)
	if rec.Code != http.StatusSeeOther || stub.lastFlashLevel != "error" {
		t.Fatalf("settings error = %d flash=%s/%s", rec.Code, stub.lastFlashLevel, stub.lastFlashMessage)
	}
	stub.updateSettingsErr = nil

	rec = serveAdditional(t, server, httptest.NewRequest(http.MethodPost, "/api/schedules/delete", strings.NewReader(`{"id":"schedule-1"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete schedule missing content type = %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/schedules/delete", strings.NewReader(`{"id":"schedule-1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = serveAdditional(t, server, req)
	if rec.Code != http.StatusOK || stub.deletedScheduleID != "schedule-1" {
		t.Fatalf("delete schedule JSON = %d id=%q body=%s", rec.Code, stub.deletedScheduleID, rec.Body.String())
	}
	stub.deleteScheduleErr = errors.New("missing")
	req = httptest.NewRequest(http.MethodPost, "/schedules/delete", strings.NewReader(url.Values{"id": []string{"missing"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = serveAdditional(t, server, req)
	if rec.Code != http.StatusSeeOther || stub.lastFlashLevel != "error" {
		t.Fatalf("delete schedule form error = %d flash=%s", rec.Code, stub.lastFlashLevel)
	}
}

func TestAdditionalOnboardingAndProfileHandlers(t *testing.T) {
	stub := &stubService{state: app.AppState{Settings: app.DefaultSettings()}}
	server := newAdditionalTestServer(t, stub)

	req := httptest.NewRequest(http.MethodPost, "/api/onboarding", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	rec := serveAdditional(t, server, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad onboarding JSON = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/onboarding", strings.NewReader(`{"agent_mode":"existing","hub_region":"bad","agent_token":"t_1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = serveAdditional(t, server, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad onboarding runtime = %d", rec.Code)
	}

	stub.bindStateOnError = true
	stub.bindErr = errors.New("profile failed")
	req = httptest.NewRequest(http.MethodPost, "/api/onboarding", strings.NewReader(`{"agent_mode":"existing","hub_region":"na","agent_token":"t_1","handle":"dispatch"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = serveAdditional(t, server, req)
	if rec.Code != http.StatusBadRequest || stub.lastFlashLevel != "error" {
		t.Fatalf("partial onboarding failure = %d flash=%s/%s", rec.Code, stub.lastFlashLevel, stub.lastFlashMessage)
	}

	stub.bindErr = nil
	req = httptest.NewRequest(http.MethodPost, "/api/onboarding", strings.NewReader(`{"agent_mode":"bind","hub_region":"na","bind_token":"b_1","handle":"dispatch"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = serveAdditional(t, server, req)
	if rec.Code != http.StatusOK || stub.lastFlashLevel != "info" {
		t.Fatalf("onboarding success = %d flash=%s", rec.Code, stub.lastFlashLevel)
	}

	stub.updateProfileErr = errors.New("profile update failed")
	profileForm := url.Values{"display_name": []string{"Dispatch"}, "emoji": []string{"D"}}
	req = httptest.NewRequest(http.MethodPost, "/profile", strings.NewReader(profileForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = serveAdditional(t, server, req)
	if rec.Code != http.StatusSeeOther || stub.lastFlashLevel != "error" {
		t.Fatalf("profile update error = %d flash=%s", rec.Code, stub.lastFlashLevel)
	}
}

func TestAdditionalA2ARoutesAndJSONRPCBranches(t *testing.T) {
	stub := &stubService{state: app.AppState{
		Settings: app.DefaultSettings(),
		ConnectedAgents: []app.ConnectedAgent{
			testConnectedAgent("agent-1", "Agent One", "uuid-1", "molten://agent/1", app.Skill{Name: "review"}),
		},
		PendingTasks: []app.PendingTask{{ID: "task-1", ChildRequestID: "child-1", ParentRequestID: "ctx", Status: app.PendingTaskStatusInQueue}},
	}}
	stub.dispatchTask = app.PendingTask{ID: "task-new", Status: app.PendingTaskStatusInQueue}
	server := newAdditionalTestServer(t, stub)

	routeCases := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodPost, "/.well-known/agent-card.json", http.StatusBadRequest},
		{http.MethodDelete, "/v1/a2a", http.StatusBadRequest},
		{http.MethodGet, "/v1/a2a/agents/", http.StatusNotFound},
		{http.MethodPost, "/v1/a2a/agents/agent-1/agent-card", http.StatusBadRequest},
		{http.MethodGet, "/v1/a2a/agents/agent-1/message:stream", http.StatusBadRequest},
		{http.MethodPost, "/v1/a2a/agents/agent-1/tasks/task-1:cancel", http.StatusBadRequest},
		{http.MethodGet, "/v1/a2a/agents/agent-1/tasks/task-1:cancel", http.StatusBadRequest},
		{http.MethodPost, "/v1/a2a/agents/agent-1/tasks/task-1:subscribe", http.StatusBadRequest},
		{http.MethodGet, "/v1/a2a/agents/agent-1/tasks/task-1/pushNotificationConfigs", http.StatusBadRequest},
		{http.MethodPost, "/v1/a2a/agents/agent-1/tasks/task-1", http.StatusBadRequest},
		{http.MethodGet, "/v1/a2a/unknown", http.StatusNotFound},
		{http.MethodGet, "/v1/a2a/agents/agent-1/extendedAgentCard", http.StatusOK},
		{http.MethodGet, "/v1/a2a/agents/agent-1/tasks", http.StatusOK},
		{http.MethodGet, "/v1/a2a/agents/agent-1/tasks/task-1", http.StatusOK},
	}
	for _, tc := range routeCases {
		rec := serveAdditional(t, server, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.want {
			t.Fatalf("%s %s = %d, want %d body=%s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}

	jsonRPCCases := []string{
		`{"jsonrpc":"1.0","id":1,"method":"ListTasks"}`,
		`{"jsonrpc":"2.0","id":1,"method":""}`,
		`{"jsonrpc":"2.0","id":1,"method":"CancelTask","params":{"id":"task-1"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"SendStreamingMessage"}`,
		`{"jsonrpc":"2.0","id":1,"method":"GetTaskPushNotificationConfig"}`,
		`{"jsonrpc":"2.0","id":1,"method":"NoSuchMethod"}`,
		`{"jsonrpc":"2.0","id":1,"method":"GetExtendedAgentCard"}`,
		`{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"messageId":"msg","parts":[{"text":"inspect"}],"metadata":{"skill_name":"review"}}}}`,
	}
	for _, body := range jsonRPCCases {
		req := httptest.NewRequest(http.MethodPost, "/v1/a2a", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := serveAdditional(t, server, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("JSON-RPC body %s = %d %s", body, rec.Code, rec.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/a2a", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	if rec := serveAdditional(t, server, req); rec.Code != http.StatusOK {
		t.Fatalf("JSON-RPC parse error status = %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/a2a", strings.NewReader(`{}`))
	if rec := serveAdditional(t, server, req); rec.Code != http.StatusOK {
		t.Fatalf("JSON-RPC content type status = %d", rec.Code)
	}

	restReq := httptest.NewRequest(http.MethodPost, "/v1/a2a/agents/agent-1/message:send", strings.NewReader("{bad"))
	restReq.Header.Set("Content-Type", "application/json")
	if rec := serveAdditional(t, server, restReq); rec.Code != http.StatusBadRequest {
		t.Fatalf("REST send parse status = %d", rec.Code)
	}
	restReq = httptest.NewRequest(http.MethodPost, "/v1/a2a/agents/agent-1/message:send", strings.NewReader(`{"message":{"messageId":"msg","parts":[{"text":"inspect"}],"metadata":{"skill_name":"review"}}}`))
	restReq.Header.Set("Content-Type", "application/json")
	if rec := serveAdditional(t, server, restReq); rec.Code != http.StatusOK {
		t.Fatalf("REST send success status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdditionalA2AHelperBranches(t *testing.T) {
	if target, route, err := a2aRouteTargetAndPath("/v1/a2a/agents/agent%2Fone/tasks/task-1"); err != nil || target != "agent/one" || route != "tasks/task-1" {
		t.Fatalf("route target/path = %q %q %#v", target, route, err)
	}
	if _, _, err := a2aRouteTargetAndPath("/v1/a2a/agents/%zz"); err == nil {
		t.Fatal("invalid target route expected error")
	}

	if _, _, _, protocolErr := a2aPayloadFromParts(nil); protocolErr != nil {
		t.Fatalf("nil parts protocol error: %#v", protocolErr)
	}
	if payload, format, _, protocolErr := a2aPayloadFromParts([]a2aPart{{Data: []any{"a"}}}); protocolErr != nil || format != "json" || payload == nil {
		t.Fatalf("single data payload = %#v %q %#v", payload, format, protocolErr)
	}
	if payload, format, _, protocolErr := a2aPayloadFromParts([]a2aPart{{URL: "https://example.test/file"}}); protocolErr != nil || format != "json" || payload.(map[string]any)["url"] == "" {
		t.Fatalf("single URL payload = %#v %q %#v", payload, format, protocolErr)
	}
	mergeA2AMetadata(nil, map[string]any{"x": "y"})

	if err := decodeA2AParams(nil, &a2aGetTaskRequest{}); err != nil {
		t.Fatalf("decode nil params: %v", err)
	}
	if err := decodeA2AParams(json.RawMessage(`null`), &a2aGetTaskRequest{}); err != nil {
		t.Fatalf("decode null params: %v", err)
	}

	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	stub := &stubService{state: app.AppState{
		PendingTasks: []app.PendingTask{
			{ID: "task-1", ParentRequestID: "ctx-1", Status: app.PendingTaskStatusSending, CreatedAt: now, DownstreamTaskState: "TASK_STATE_WORKING", DownstreamMessage: "working", DispatchPayload: map[string]any{"input": "one"}},
			{ID: "task-2", ParentRequestID: "ctx-2", Status: app.PendingTaskStatusInQueue, CreatedAt: now, DispatchPayload: map[string]any{"input": "two"}, DispatchPayloadFormat: "markdown"},
		},
		RecentEvents: []app.RuntimeEvent{
			{TaskID: "event-error", Level: "error", Title: "Task failed", Detail: "bad", At: now},
			{TaskID: "event-ignore", Title: "Heartbeat", At: now},
		},
	}}
	server := newAdditionalTestServer(t, stub)
	history := 1
	list := server.a2aListTasksForRequest(a2aListTasksRequest{ContextID: "ctx-2", PageSize: 1, HistoryLength: &history})
	if list["totalSize"].(int) != 1 {
		t.Fatalf("filtered list = %#v", list)
	}
	list = server.a2aListTasksForRequest(a2aListTasksRequest{PageSize: 1, PageToken: "1"})
	if list["pageSize"].(int) != 1 {
		t.Fatalf("paged list = %#v", list)
	}
	if _, err := server.a2aTaskByID("missing", nil); err == nil {
		t.Fatal("missing task expected A2A error")
	}
	task := a2aTaskFromPendingTask(stub.state.PendingTasks[0])
	status := task["status"].(map[string]any)
	if status["message"] == nil {
		t.Fatalf("pending task status missing message: %#v", task)
	}
	if text, ok := a2aTextFromPayload(map[string]any{"prompt": " inspect "}); !ok || text != "inspect" {
		t.Fatalf("text from payload = %q %v", text, ok)
	}
	if text, ok := a2aTextFromPayload(map[string]any{"other": "x"}); ok || text != "" {
		t.Fatalf("text from unknown map = %q %v", text, ok)
	}
	if got := stringFromAny(123); got != "" {
		t.Fatalf("stringFromAny non-string = %q", got)
	}
	if got := a2aTaskMetadata(map[string]any{"": "skip", "nil": nil, "blank": " ", "keep": "x"}); got["moltenhub_dispatch"].(map[string]any)["keep"] != "x" {
		t.Fatalf("task metadata = %#v", got)
	}
	zero := 0
	applied := a2aApplyHistoryLength(map[string]any{"history": []map[string]any{{"id": "1"}}}, &zero)
	if len(applied["history"].([]map[string]any)) != 0 {
		t.Fatalf("history zero = %#v", applied)
	}
	if got := a2aTimeString(time.Time{}); got != "" {
		t.Fatalf("zero time string = %q", got)
	}
	if err := a2aDispatchError(errors.New("internal boom")); err.httpStatus != http.StatusInternalServerError {
		t.Fatalf("internal dispatch error = %#v", err)
	}
}

func TestAdditionalViewHelperBranches(t *testing.T) {
	agents := []app.ConnectedAgent{
		{AgentID: "offline", Status: "offline"},
		testConnectedAgent("online", "Online", "uuid-online", "", app.Skill{Name: "review"}),
	}
	ordered := orderConnectedAgents(agents)
	if ordered[0].AgentID != "online" {
		t.Fatalf("ordered agents = %#v", ordered)
	}
	if got := connectedAgentDisplayName(app.ConnectedAgent{AgentUUID: "12345678-1234-1234-1234-123456789abc"}); got != "Connected agent" {
		t.Fatalf("display name UUID fallback = %q", got)
	}
	if got := connectedAgentEmoji(app.ConnectedAgent{}); got == "" {
		t.Fatal("connected agent emoji fallback empty")
	}
	if got := visibleAgentLabel("12345678-1234-1234-1234-123456789abc"); got != "" {
		t.Fatalf("visible UUID label = %q", got)
	}

	connectionCases := []app.AppState{
		{Connection: app.ConnectionState{Status: app.ConnectionStatusConnected, Transport: app.ConnectionTransportHTTP, BaseURL: "https://hub.test"}},
		{Connection: app.ConnectionState{Status: app.ConnectionStatusConnected, Transport: app.ConnectionTransportReachable, Detail: "live"}},
		{Connection: app.ConnectionState{Transport: app.ConnectionTransportRetrying}},
		{Connection: app.ConnectionState{Error: "boom"}},
		{Session: app.Session{AgentToken: "token"}},
		{},
	}
	for _, state := range connectionCases {
		if view := connectionStatusView(state); strings.TrimSpace(view.Label) == "" || strings.TrimSpace(view.Description) == "" {
			t.Fatalf("connection view empty for %#v: %#v", state, view)
		}
	}
	if got := fallbackTarget(""); got != "hub" {
		t.Fatalf("fallback target = %q", got)
	}

	subCases := []app.AppState{
		{},
		{Session: app.Session{AgentToken: "token"}, Connection: app.ConnectionState{Status: app.ConnectionStatusDisconnected}},
		{Session: app.Session{AgentToken: "token"}, Connection: app.ConnectionState{Status: app.ConnectionStatusConnected, Transport: app.ConnectionTransportHTTP}},
	}
	for _, state := range subCases {
		_ = subActionState(state)
	}

	oldEmojis := defaultProfileEmojis
	defaultProfileEmojis = nil
	if got := randomDefaultProfileEmoji(); got == "" {
		t.Fatal("random default emoji empty")
	}
	defaultProfileEmojis = oldEmojis
	oldReader := rand.Reader
	rand.Reader = strings.NewReader("")
	if got := randomDefaultProfileEmoji(); got == "" {
		t.Fatal("fallback random default emoji empty")
	}
	rand.Reader = oldReader

	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	feed := mergedActivityFeed(
		[]app.PendingTask{{ID: "pending", Status: app.PendingTaskStatusSending, CreatedAt: now, TargetAgentDisplayName: "Agent", OriginalSkillName: "review"}},
		[]app.RuntimeEvent{{TaskID: "event", Title: "Task completed", Detail: "done", At: now.Add(time.Minute), TargetAgentDisplayName: "Agent"}},
	)
	if len(feed) != 2 || !feed[0].IsRecentEvent {
		t.Fatalf("activity feed = %#v", feed)
	}
	if got := runtimeTargetAgentLabel("", "", "uuid", "uri"); got != "uuid" {
		t.Fatalf("runtime target fallback = %q", got)
	}
	if got := joinNonEmpty(" / ", " a ", "", "b"); got != "a / b" {
		t.Fatalf("joinNonEmpty = %q", got)
	}

	view := defaultOnboardingView(app.AppState{
		Session:    app.Session{AgentToken: "token"},
		Connection: app.ConnectionState{Status: app.ConnectionStatusDisconnected, Transport: app.ConnectionTransportOffline},
	})
	if !view.Error {
		t.Fatalf("offline onboarding view = %#v", view)
	}
	steps := defaultOnboardingSteps(app.OnboardingModeExisting)
	setOnboardingProgress(steps, "missing", "", "detail")
	if steps[0].Status != "pending" {
		t.Fatalf("missing stage progress = %#v", steps)
	}
}
