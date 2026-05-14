package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

type hubRoundTripFunc func(*http.Request) (*http.Response, error)

func (f hubRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (errReadCloser) Close() error             { return nil }

func TestAdditionalA2AHelpers(t *testing.T) {
	client := NewClient("")
	if client.canPublishRuntimeViaA2A(PublishRequest{ToAgentUUID: "12345678-1234-1234-1234-123456789abc"}) {
		t.Fatal("A2A publish should be unavailable without endpoint")
	}
	if _, err := client.publishRuntimeA2A(nil, "", PublishRequest{}); err == nil {
		t.Fatal("publishRuntimeA2A without endpoint expected error")
	}

	client = NewClient("https://hub.test/root?x=1#frag")
	if got := client.a2aEndpointBaseURL(); got != "https://hub.test/root/a2a" {
		t.Fatalf("a2aEndpointBaseURL = %q", got)
	}
	client.SetRuntimeEndpoints(RuntimeEndpoints{RuntimePushURL: "https://hub.test/v1/runtime/messages/publish"})
	if got := client.a2aPublishEndpoint(PublishRequest{ToAgentUUID: "12345678-1234-1234-1234-123456789abc"}); got != "https://hub.test/v1/a2a/agents/12345678-1234-1234-1234-123456789abc" {
		t.Fatalf("a2a publish endpoint = %q", got)
	}
	if !client.canPublishRuntimeViaA2A(PublishRequest{ToAgentURI: "molten://agent/peer"}) {
		t.Fatal("A2A publish should accept agent URI")
	}

	req, err := a2aSendMessageRequestFromRuntime(PublishRequest{
		ToAgentUUID: "12345678-1234-1234-1234-123456789abc",
		ClientMsgID: "client-msg",
		Message: OpenClawMessage{
			Protocol:  "openclaw.http.v1",
			RequestID: "request-id",
			ReplyTo:   "context-id",
			Payload:   map[string]any{"input": "hi"},
		},
	})
	if err != nil {
		t.Fatalf("a2aSendMessageRequestFromRuntime: %v", err)
	}
	if req.Message.ID != "client-msg" || req.Message.ContextID != "context-id" {
		t.Fatalf("message IDs = %q, %q", req.Message.ID, req.Message.ContextID)
	}
	if req.Metadata["to_agent_uuid"] != "12345678-1234-1234-1234-123456789abc" {
		t.Fatalf("metadata = %#v", req.Metadata)
	}
	if _, err := a2aSendMessageRequestFromRuntime(PublishRequest{Message: OpenClawMessage{Payload: make(chan int)}}); err == nil {
		t.Fatal("unmarshalable runtime payload expected error")
	}
	if got := a2aRoutingMetadata(PublishRequest{ToAgentUUID: "not-a-uuid"}); got != nil {
		t.Fatalf("metadata without routable target = %#v", got)
	}
	ctx := a2aContextWithBearer(context.Background(), " token ")
	if ctx == nil {
		t.Fatal("bearer context should not be nil")
	}

	stateCases := map[a2a.TaskState]string{
		a2a.TaskStateSubmitted:                     "queued",
		a2a.TaskStateWorking:                       "working",
		a2a.TaskStateCompleted:                     "delivered",
		a2a.TaskStateFailed:                        "failed",
		a2a.TaskStateCanceled:                      "canceled",
		a2a.TaskStateRejected:                      "rejected",
		a2a.TaskState("TASK_STATE_INPUT_REQUIRED"): "input_required",
		a2a.TaskStateUnspecified:                   "queued",
	}
	for state, want := range stateCases {
		if got := a2aDeliveryFromTaskState(state); got != want {
			t.Fatalf("delivery for %q = %q, want %q", state, got, want)
		}
	}

	taskResponse := publishResponseFromA2AResult(&a2a.Task{
		ID:       "task-id",
		Status:   a2a.TaskStatus{State: a2a.TaskStateCompleted},
		Metadata: map[string]any{"moltenhub": map[string]any{"idempotency": "same"}},
	})
	if taskResponse.MessageID != "task-id" || taskResponse.Delivery != "delivered" || taskResponse.Idempotency != "same" {
		t.Fatalf("task publish response = %#v", taskResponse)
	}
	messageResponse := publishResponseFromA2AResult(&a2a.Message{
		ID:       "message-id",
		TaskID:   "task-id",
		Metadata: map[string]any{"moltenhub": map[string]any{"idempotency": "same"}},
	})
	if messageResponse.MessageID != "task-id" || messageResponse.Delivery != "delivered" {
		t.Fatalf("message publish response = %#v", messageResponse)
	}
	if got := publishResponseFromA2AResult(nil); got != (PublishResponse{}) {
		t.Fatalf("nil publish response = %#v", got)
	}
}

func TestAdditionalClientHelpers(t *testing.T) {
	if got := (*APIError)(nil).Error(); got != "<nil>" {
		t.Fatalf("nil APIError = %q", got)
	}
	if got := (&APIError{StatusCode: 418}).Error(); got != "hub API 418: I'm a teapot" {
		t.Fatalf("status APIError = %q", got)
	}
	if got := (&APIError{StatusCode: 400, Code: "bad", Message: "wrong"}).Error(); got != "hub API 400 bad: wrong" {
		t.Fatalf("coded APIError = %q", got)
	}

	client := NewClient("https://hub.test/")
	client.SetBaseURL("https://other.test/")
	if client.baseURL != "https://other.test" {
		t.Fatalf("baseURL = %q", client.baseURL)
	}
	originalHTTPClient := client.httpClient
	client.SetHTTPClient(nil)
	if client.httpClient != originalHTTPClient {
		t.Fatal("nil HTTP client should be ignored")
	}
	customHTTPClient := &http.Client{}
	client.SetHTTPClient(customHTTPClient)
	if client.httpClient != customHTTPClient {
		t.Fatal("custom HTTP client not applied")
	}
	if _, err := client.BindAgent(context.Background(), BindRequest{}); err == nil {
		t.Fatal("blank bind token expected error")
	}

	if got := client.runtimeEndpoint(" explicit ", "fallback"); got != "explicit" {
		t.Fatalf("runtimeEndpoint override = %q", got)
	}
	if got := client.runtimeEndpoint(" ", "fallback"); got != "fallback" {
		t.Fatalf("runtimeEndpoint fallback = %q", got)
	}
	if got := client.runtimeDeliveryEndpointCandidates("delete"); got != nil {
		t.Fatalf("invalid delivery candidates = %#v", got)
	}
	client.SetRuntimeEndpoints(RuntimeEndpoints{
		RuntimePullURL: "https://hub.test/v1/runtime/messages/pull?x=1#frag",
		RuntimeAckURL:  "https://hub.test/v1/runtime/messages/ack",
	})
	if got := client.runtimeDeliveryEndpointCandidates("ack"); len(got) != 2 || got[0] != "https://hub.test/v1/runtime/messages/ack" {
		t.Fatalf("ack candidates = %#v", got)
	}
	for _, pull := range []string{"%", "https://hub.test/v1/openclaw/messages/pull", "https://hub.test/v1/runtime/messages_pull", "https://hub.test/v1/runtime/pull"} {
		_ = runtimeEndpointFromPull(pull, "/messages/ack", "/messages_ack", "/ack")
	}

	if !shouldRetryRuntimeEndpoint(&APIError{StatusCode: http.StatusNotFound}) {
		t.Fatal("404 should retry runtime endpoint")
	}
	if shouldRetryRuntimeEndpoint(errors.New("no api")) {
		t.Fatal("non API error should not retry runtime endpoint")
	}
	if !shouldRetryMetadataEndpoint(&APIError{StatusCode: http.StatusUnauthorized, Code: "unauthorized"}) {
		t.Fatal("unauthorized code should retry metadata endpoint")
	}
	if !shouldRetryMetadataEndpoint(&APIError{StatusCode: http.StatusUnauthorized, Message: "missing or invalid bearer token"}) {
		t.Fatal("bearer message should retry metadata endpoint")
	}
	if shouldRetryMetadataEndpoint(&APIError{StatusCode: http.StatusForbidden}) {
		t.Fatal("forbidden should not retry metadata endpoint")
	}

	if got := endpointPath("https://hub.test/v1/runtime/messages/pull?timeout=1"); got != "/v1/runtime/messages/pull" {
		t.Fatalf("endpointPath absolute = %q", got)
	}
	if got := endpointPath("/v1/runtime/messages/pull?timeout=1"); got != "/v1/runtime/messages/pull" {
		t.Fatalf("endpointPath relative = %q", got)
	}
	if got := isRuntimeMessagesEndpoint("/v1/runtime/messages/pull"); !got {
		t.Fatal("runtime messages endpoint expected true")
	}

	if _, err := client.newRequest(context.Background(), http.MethodPost, "/v1/test", "", make(chan int)); err == nil {
		t.Fatal("unmarshalable request body expected error")
	}
	badBase := NewClient("%")
	if _, err := badBase.newRequest(context.Background(), http.MethodGet, "/v1/test", "", nil); err == nil {
		t.Fatal("bad base URL expected error")
	}
	if _, err := client.newRequest(context.Background(), http.MethodGet, "http://%", "", nil); err == nil {
		t.Fatal("bad endpoint URL expected error")
	}
	request, err := client.newRequest(context.Background(), http.MethodPost, "/v1/test?x=1", " token ", map[string]string{"ok": "yes"})
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}
	if request.Header.Get("Authorization") != "Bearer token" || request.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %#v", request.Header)
	}
}

func TestAdditionalHTTPClientBehavior(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/runtime/messages/publish":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"message_id": "msg-1", "delivery": "queued"}})
		case "/v1/runtime/messages/pull":
			w.WriteHeader(http.StatusNoContent)
		case "/v1/runtime/messages/ack", "/v1/runtime/messages/nack":
			w.WriteHeader(http.StatusNoContent)
		case "/v1/runtime/messages/offline":
			w.WriteHeader(http.StatusNoContent)
		case "/ping":
			_, _ = w.Write([]byte(strings.Repeat("x", 140)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL)
	resp, err := client.PublishOpenClaw(context.Background(), "token", PublishRequest{Message: OpenClawMessage{Payload: "hi"}})
	if err != nil {
		t.Fatalf("PublishOpenClaw: %v", err)
	}
	if resp.MessageID != "msg-1" || resp.Delivery != "queued" {
		t.Fatalf("publish response = %#v", resp)
	}
	if _, ok, err := client.PullOpenClaw(context.Background(), "token", time.Millisecond); err != nil || ok {
		t.Fatalf("PullOpenClaw no content = ok %v err %v", ok, err)
	}
	if err := client.AckOpenClaw(context.Background(), "token", "delivery"); err != nil {
		t.Fatalf("AckOpenClaw: %v", err)
	}
	if err := client.NackOpenClaw(context.Background(), "token", "delivery"); err != nil {
		t.Fatalf("NackOpenClaw: %v", err)
	}
	if err := client.MarkOffline(context.Background(), "token", OfflineRequest{Reason: "test"}); err != nil {
		t.Fatalf("MarkOffline: %v", err)
	}
	if detail, err := client.CheckPing(context.Background()); err != nil || !strings.Contains(detail, "...") {
		t.Fatalf("CheckPing detail = %q err %v", detail, err)
	}

	errorClient := NewClient(server.URL)
	errorClient.SetHTTPClient(&http.Client{Transport: hubRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	})})
	if err := errorClient.doJSON(context.Background(), http.MethodGet, "/v1/test", "", nil, nil); err == nil {
		t.Fatal("doJSON network error expected")
	}
	if _, _, err := errorClient.PullRuntimeMessage(context.Background(), "token", 0); err == nil {
		t.Fatal("pull network error expected")
	}
	if _, err := errorClient.CheckPing(context.Background()); err == nil {
		t.Fatal("ping network error expected")
	}

	badJSONServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{bad"))
	}))
	defer badJSONServer.Close()
	badJSONClient := NewClient(badJSONServer.URL)
	var out map[string]any
	if err := badJSONClient.doJSON(context.Background(), http.MethodGet, "/v1/test", "", nil, &out); err == nil {
		t.Fatal("bad JSON response expected error")
	}
}

func TestAdditionalBindAndErrorDecodingHelpers(t *testing.T) {
	if _, err := parseBindResponsePayload(nil); err == nil {
		t.Fatal("empty bind payload expected error")
	}
	if _, err := parseBindResponsePayload(json.RawMessage(`{bad`)); err == nil {
		t.Fatal("invalid bind payload expected error")
	}
	if _, err := parseBindResponsePayload(json.RawMessage(`{"agent_uuid":"uuid"}`)); err == nil {
		t.Fatal("missing token expected error")
	}

	var out BindResponse
	applyBindEndpoints(nil, map[string]any{"manifest": "x"})
	applyBindEndpoints(&out, nil)
	applyBindProtocolAdapters(nil, map[string]any{"runtime_v1": map[string]any{}})
	applyBindProtocolAdapters(&out, nil)
	applyBindProtocolAdapters(&out, map[string]any{"runtime_v1": map[string]any{"endpoints": map[string]any{"status": "/status"}}})
	if out.Endpoints.RuntimeStatus != "/status" {
		t.Fatalf("adapter status endpoint = %q", out.Endpoints.RuntimeStatus)
	}
	if got := protocolAdapterEndpoints(map[string]any{"runtime_v1": "bad"}, "runtime_v1"); got != nil {
		t.Fatalf("bad adapter endpoints = %#v", got)
	}

	for _, raw := range []string{"://bad", "ftp://hub.test", "https://"} {
		if _, err := hubPingURL(raw); err == nil {
			t.Fatalf("hubPingURL(%q) expected error", raw)
		}
	}
	if got := joinURLPath("/v1", ""); got != "/v1" {
		t.Fatalf("joinURLPath empty endpoint = %q", got)
	}
	if got := joinURLPath("", ""); got != "/" {
		t.Fatalf("joinURLPath empty = %q", got)
	}

	readErr := decodeAPIError(&http.Response{StatusCode: 500, Body: errReadCloser{}})
	if !strings.Contains(readErr.Error(), "read error body") {
		t.Fatalf("read error API = %v", readErr)
	}
	emptyErr := decodeAPIError(&http.Response{StatusCode: 404, Body: io.NopCloser(bytes.NewReader(nil))})
	if !strings.Contains(emptyErr.Error(), "Not Found") {
		t.Fatalf("empty API error = %v", emptyErr)
	}
	textErr := decodeAPIError(&http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader("plain error"))})
	if !strings.Contains(textErr.Error(), "plain error") {
		t.Fatalf("text API error = %v", textErr)
	}
}

func TestAdditionalDeliveryDecodingHelpers(t *testing.T) {
	if _, err := decodePullResponsePayload(json.RawMessage(`{bad`), ""); err == nil {
		t.Fatal("invalid pull response expected error")
	}
	if _, err := decodePullResponsePayload(json.RawMessage(`{"envelope":{bad}}`), "pull"); err == nil {
		t.Fatal("invalid envelope expected error")
	}

	raw := json.RawMessage(`{
		"delivery":{"delivery_id":"nested-delivery"},
		"message_id":"message-1",
		"message":{"type":"skill_result","request_id":"request-1","payload":{"ok":true}}
	}`)
	response, err := decodePullResponsePayload(raw, "pull")
	if err != nil {
		t.Fatalf("decode pull response: %v", err)
	}
	if response.DeliveryID != "nested-delivery" || response.OpenClawMessage.Type != "skill_result" {
		t.Fatalf("pull response = %#v", response)
	}

	for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`{"unknown":true}`)} {
		if message, ok, err := decodeOpenClawMessage(raw); err != nil || ok || message != (OpenClawMessage{}) {
			t.Fatalf("decodeOpenClawMessage(%s) = %#v %v %v", raw, message, ok, err)
		}
	}
	if _, _, err := decodeOpenClawMessage(json.RawMessage(`{bad`)); err == nil {
		t.Fatal("invalid OpenClaw JSON expected error")
	}

	if _, ok, err := openClawMessageFromA2A(json.RawMessage(`{"protocol":"a2a.v1","message":{}}`)); err != nil || ok {
		t.Fatalf("empty A2A message = ok %v err %v", ok, err)
	}
	if _, _, err := openClawMessageFromA2A(json.RawMessage(`{bad`)); err == nil {
		t.Fatal("invalid A2A envelope expected error")
	}

	if _, ok, err := decodeA2AMessagePayload(json.RawMessage(`42`)); err != nil || ok {
		t.Fatalf("numeric A2A message = ok %v err %v", ok, err)
	}
	if _, _, err := decodeA2AMessagePayload(json.RawMessage(`"`)); err == nil {
		t.Fatal("invalid A2A string expected error")
	}
	if _, ok, err := decodeA2AMessagePayload(json.RawMessage(`" "`)); err != nil || ok {
		t.Fatalf("blank A2A string = ok %v err %v", ok, err)
	}
	if _, _, err := decodeA2AMessagePayload(json.RawMessage(`{"parts":`)); err == nil {
		t.Fatal("invalid A2A object expected error")
	}

	text := `{"type":"skill_result","request_id":"r","payload":{"ok":true}}`
	message, ok, err := openClawMessageFromA2APayload(a2aMessagePayload{Parts: []a2aPart{{Text: &text}}})
	if err != nil || !ok || message.Type != "skill_result" {
		t.Fatalf("A2A text JSON = %#v ok %v err %v", message, ok, err)
	}
	message, ok, err = openClawMessageFromA2APayload(a2aMessagePayload{
		MessageID: "message-id",
		ContextID: "context-id",
		Parts: []a2aPart{
			{Text: stringPtr("part text")},
		},
	})
	if err != nil || !ok || message.Payload != "part text" || message.RequestID != "message-id" || message.ReplyTo != "context-id" {
		t.Fatalf("A2A text payload = %#v ok %v err %v", message, ok, err)
	}
	if got := a2aTextMessagePayload([]a2aPart{{Data: json.RawMessage(`"data text"`)}}); got != `"data text"` {
		t.Fatalf("a2aTextMessagePayload data = %q", got)
	}
	if _, _, err := openClawMessageFromA2APayload(a2aMessagePayload{Parts: []a2aPart{{Data: json.RawMessage(`{bad`)}}}); err == nil {
		t.Fatal("invalid A2A data JSON expected error")
	}

	wrapped := json.RawMessage(`{"envelope":{"type":"skill_result","request_id":"r"}}`)
	if message, ok, err := decodeOpenClawMessageFromJSON(wrapped); err != nil || !ok || message.Type != "skill_result" {
		t.Fatalf("wrapped OpenClaw = %#v ok %v err %v", message, ok, err)
	}
	if _, _, err := decodeOpenClawMessageFromJSON(json.RawMessage(`{bad`)); err == nil {
		t.Fatal("invalid wrapped OpenClaw expected error")
	}
	if _, _, err := decodeWrappedOpenClawMessage(json.RawMessage(`{bad`)); err == nil {
		t.Fatal("invalid decoded wrapped OpenClaw expected error")
	}
}

func TestAdditionalRealtimeHelpers(t *testing.T) {
	client := NewClient("")
	if _, err := client.ConnectRuntimeMessages(nil, "", ""); err == nil {
		t.Fatal("missing websocket endpoint expected error")
	}
	if _, err := client.ConnectOpenClaw(context.Background(), "", ""); err == nil {
		t.Fatal("ConnectOpenClaw missing endpoint expected error")
	}
	if got := boundedTimeout(context.Background(), 0); got != 0 {
		t.Fatalf("zero fallback timeout = %v", got)
	}
	if got := boundedTimeout(nil, time.Second); got != time.Second {
		t.Fatalf("nil ctx timeout = %v", got)
	}

	session := &websocketSession{
		deliveries: make(chan PullResponse, 1),
		readErr:    make(chan error, 1),
		closed:     make(chan struct{}),
	}
	session.deliveries <- PullResponse{DeliveryID: "queued"}
	if message, err := session.Receive(context.Background()); err != nil || message.DeliveryID != "queued" {
		t.Fatalf("queued receive = %#v err %v", message, err)
	}
	if err := session.Ack(context.Background(), " "); err != nil {
		t.Fatalf("blank ack: %v", err)
	}
	if err := session.Nack(context.Background(), " "); err != nil {
		t.Fatalf("blank nack: %v", err)
	}
	session.bindContext(nil)
	session.startHeartbeat(context.Background())
	oldHeartbeat := websocketHeartbeatInterval
	websocketHeartbeatInterval = 0
	session.startHeartbeat(context.Background())
	websocketHeartbeatInterval = oldHeartbeat

	for _, tc := range []struct {
		base     string
		endpoint string
	}{
		{"://bad", "/ws"},
		{"https://hub.test", "%"},
		{"ftp://hub.test", "/ws"},
		{"https://", "/ws"},
	} {
		if _, err := websocketURL(tc.base, tc.endpoint, ""); err == nil {
			t.Fatalf("websocketURL(%q, %q) expected error", tc.base, tc.endpoint)
		}
	}
	if got := httpOriginFor("://bad"); got != "http://localhost" {
		t.Fatalf("invalid origin = %q", got)
	}
}

func TestAdditionalA2AFallbackAndURLHelpers(t *testing.T) {
	for _, err := range []error{
		a2a.ErrMethodNotFound,
		a2a.ErrUnsupportedOperation,
		a2a.ErrServerError,
		errors.New("404 not found"),
		errors.New("method not found"),
		errors.New("connection refused"),
	} {
		if !shouldFallbackRuntimePublish(err) {
			t.Fatalf("should fallback for %v", err)
		}
	}
	if shouldFallbackRuntimePublish(nil) || shouldFallbackRuntimePublish(errors.New("permission denied")) {
		t.Fatal("unexpected A2A fallback")
	}

	for _, endpoint := range []string{
		"https://hub.test/v1/agents/me/manifest",
		"https://hub.test/v1/agents/me/capabilities",
		"https://hub.test/v1/agents/me/metadata",
		"https://hub.test/v1/agents/me",
		"https://hub.test/v1/runtime/messages/pull",
		"https://hub.test/v1/runtime/messages/{message_id}",
		"https://hub.test/v1/messages/publish",
	} {
		if got := apiBaseFromRuntimeEndpoint(endpoint); got != "https://hub.test/v1" {
			t.Fatalf("apiBaseFromRuntimeEndpoint(%q) = %q", endpoint, got)
		}
	}
	for _, endpoint := range []string{"", "%", "ftp://hub.test/v1/runtime/messages/pull", "https://hub.test/unknown"} {
		if got := apiBaseFromRuntimeEndpoint(endpoint); got != "" {
			t.Fatalf("api base for %q = %q, want empty", endpoint, got)
		}
	}

	for _, value := range []string{
		"",
		"12345678-1234-1234-1234-123456789ab",
		"12345678_1234-1234-1234-123456789abc",
		"12345678-1234-1234-1234-123456789abz",
	} {
		if isUUIDLike(value) {
			t.Fatalf("invalid UUID-like value accepted: %q", value)
		}
	}
}

func stringPtr(value string) *string {
	return &value
}
