package hub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDecodePullResponsePayloadCoversEnvelopeFallbacks(t *testing.T) {
	raw := json.RawMessage(`{
		"delivery":{"delivery_id":" nested-delivery "},
		"message_id":" message-1 ",
		"from_agent_uuid":" from ",
		"to_agent_uri":" to-uri ",
		"message":{"kind":"text_message","request_id":" req-1 ","payload":"hello"}
	}`)
	got, err := decodePullResponsePayload(raw, "")
	if err != nil {
		t.Fatalf("decodePullResponsePayload: %v", err)
	}
	if got.DeliveryID != "nested-delivery" || got.MessageID != "message-1" {
		t.Fatalf("unexpected delivery IDs: %#v", got)
	}
	if got.OpenClawMessage.Kind != "text_message" || got.OpenClawMessage.RequestID != " req-1 " {
		t.Fatalf("unexpected message: %#v", got.OpenClawMessage)
	}

	if _, err := decodePullResponsePayload(json.RawMessage(`{`), "custom source"); err == nil || !strings.Contains(err.Error(), "custom source") {
		t.Fatalf("expected source decode error, got %v", err)
	}
	if _, err := decodePullResponsePayload(json.RawMessage(`{"envelope":{`), "pull"); err == nil {
		t.Fatal("expected envelope decode error")
	}
}

func TestDecodeOpenClawMessageFromA2AVariants(t *testing.T) {
	text := "plain text"
	message, ok, err := decodeA2AMessagePayload(json.RawMessage(`"` + text + `"`))
	if err != nil || !ok {
		t.Fatalf("decodeA2AMessagePayload text ok=%v err=%v", ok, err)
	}
	openClaw, ok, err := openClawMessageFromA2APayload(message)
	if err != nil || !ok {
		t.Fatalf("openClawMessageFromA2APayload text ok=%v err=%v", ok, err)
	}
	if openClaw.Payload != text || openClaw.Kind != "text_message" {
		t.Fatalf("unexpected text OpenClaw message: %#v", openClaw)
	}

	wrapped := json.RawMessage(`{"protocol":"a2a.v1","message":{"messageId":"m1","contextId":"c1","parts":[{"text":"hello"}]}}`)
	openClaw, ok, err = openClawMessageFromA2A(wrapped)
	if err != nil || !ok {
		t.Fatalf("openClawMessageFromA2A wrapped ok=%v err=%v", ok, err)
	}
	if openClaw.RequestID != "m1" || openClaw.ReplyTo != "c1" {
		t.Fatalf("unexpected wrapped OpenClaw message: %#v", openClaw)
	}

	dataMessage := json.RawMessage(`{"messageId":"m2","parts":[{"data":{"kind":"skill_request","skill_name":"review"}}]}`)
	openClaw, ok, err = openClawMessageFromA2A(dataMessage)
	if err != nil || !ok {
		t.Fatalf("openClawMessageFromA2A data ok=%v err=%v", ok, err)
	}
	if openClaw.SkillName != "review" {
		t.Fatalf("unexpected data OpenClaw message: %#v", openClaw)
	}

	if _, ok, err := decodeA2AMessagePayload(json.RawMessage(`42`)); err != nil || ok {
		t.Fatalf("decodeA2AMessagePayload number ok=%v err=%v, want false nil", ok, err)
	}
}

func TestRealtimeHelpersCoverRetryAndURLBranches(t *testing.T) {
	for _, err := range []error{
		nil,
		context.Canceled,
		&APIError{StatusCode: 404, Message: "missing"},
		&APIError{StatusCode: 405, Message: "method not allowed"},
	} {
		got := shouldRetryRuntimeWebsocketEndpoint(err)
		want := err != nil && (strings.Contains(strings.ToLower(err.Error()), "404") || strings.Contains(strings.ToLower(err.Error()), "405") || strings.Contains(strings.ToLower(err.Error()), "method not allowed"))
		if got != want {
			t.Fatalf("shouldRetryRuntimeWebsocketEndpoint(%v) = %v, want %v", err, got, want)
		}
	}

	wsURL, err := websocketURL("https://example.test/base", "/v1/runtime/messages/ws?x=1", " session ")
	if err != nil {
		t.Fatalf("websocketURL: %v", err)
	}
	if !strings.HasPrefix(wsURL, "wss://example.test/base/v1/runtime/messages/ws?") || !strings.Contains(wsURL, "sessionKey=session") {
		t.Fatalf("websocketURL = %q", wsURL)
	}
	if _, err := websocketURL("ftp://example.test", "/ws", ""); err == nil {
		t.Fatal("websocketURL unsupported scheme expected error")
	}
	if got := httpOriginFor("wss://example.test/ws?token=1"); got != "https://example.test/" {
		t.Fatalf("httpOriginFor wss = %q", got)
	}
	if got := httpOriginFor(":bad-url"); got != "http://localhost" {
		t.Fatalf("httpOriginFor invalid = %q", got)
	}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(10*time.Millisecond))
	defer cancel()
	if got := boundedTimeout(ctx, time.Second); got <= 0 || got > time.Second {
		t.Fatalf("boundedTimeout deadline = %v", got)
	}
	if got := boundedTimeout(nil, time.Second); got != time.Second {
		t.Fatalf("boundedTimeout nil = %v, want 1s", got)
	}
	if got := boundedTimeout(context.Background(), 0); got != 0 {
		t.Fatalf("boundedTimeout zero = %v, want 0", got)
	}
}
