package hub

import (
	"encoding/json"
	"testing"
)

func TestDecodeOpenClawMessageFromA2AStatusUpdateTextPart(t *testing.T) {
	t.Parallel()

	statusPayload := map[string]any{
		"protocol":   "a2a.v1",
		"type":       "task_status_update",
		"request_id": "dispatch-1",
		"status":     "working",
		"a2a_state":  "TASK_STATE_WORKING",
		"message":    "Task running.",
		"statusUpdate": map[string]any{
			"taskId":    "hub-task-1",
			"contextId": "a2a-context-1",
			"status": map[string]any{
				"state": "TASK_STATE_WORKING",
				"message": map[string]any{
					"parts": []map[string]any{{"text": "Task running."}},
				},
			},
		},
	}
	encodedStatus, err := json.Marshal(statusPayload)
	if err != nil {
		t.Fatalf("marshal status payload: %v", err)
	}
	a2aPayload := map[string]any{
		"messageId": "dispatch-1-status-working",
		"contextId": "a2a-context-1",
		"taskId":    "hub-task-1",
		"role":      "agent",
		"parts": []map[string]any{{
			"text":      string(encodedStatus),
			"mediaType": "application/json",
		}},
	}
	encodedA2A, err := json.Marshal(a2aPayload)
	if err != nil {
		t.Fatalf("marshal a2a payload: %v", err)
	}

	message, ok, err := decodeOpenClawMessage(encodedA2A)
	if err != nil {
		t.Fatalf("decode A2A status update: %v", err)
	}
	if !ok {
		t.Fatal("expected decoded OpenClaw message")
	}
	if message.Type != "task_status_update" {
		t.Fatalf("type = %q, want task_status_update", message.Type)
	}
	if message.Message != "Task running." {
		t.Fatalf("message = %q, want Task running.", message.Message)
	}
	if message.A2AState != "TASK_STATE_WORKING" {
		t.Fatalf("a2a_state = %q, want TASK_STATE_WORKING", message.A2AState)
	}
	if _, ok := message.StatusUpdate.(map[string]any); !ok {
		t.Fatalf("statusUpdate = %#v, want map", message.StatusUpdate)
	}
}

func TestDecodeOpenClawMessageFromA2AStringMessage(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"protocol": "a2a.v1",
		"message": "Hub still connected."
	}`)

	message, ok, err := decodeOpenClawMessage(raw)
	if err != nil {
		t.Fatalf("decode A2A string message: %v", err)
	}
	if !ok {
		t.Fatal("expected decoded text message")
	}
	if message.Type != "text_message" {
		t.Fatalf("type = %q, want text_message", message.Type)
	}
	if message.Payload != "Hub still connected." {
		t.Fatalf("payload = %#v, want Hub still connected.", message.Payload)
	}
}

func TestDecodePullResponsePayloadWithA2AStringMessage(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"delivery_id": "delivery-1",
		"envelope": {
			"protocol": "a2a.v1",
			"message": "Hub still connected."
		}
	}`)

	response, err := decodePullResponsePayload(raw, "pull response")
	if err != nil {
		t.Fatalf("decode pull response: %v", err)
	}
	if response.DeliveryID != "delivery-1" {
		t.Fatalf("delivery_id = %q, want delivery-1", response.DeliveryID)
	}
	if response.OpenClawMessage.Type != "text_message" {
		t.Fatalf("type = %q, want text_message", response.OpenClawMessage.Type)
	}
	if response.OpenClawMessage.Payload != "Hub still connected." {
		t.Fatalf("payload = %#v, want Hub still connected.", response.OpenClawMessage.Payload)
	}
}

func TestDecodePullResponsePayloadWithQueueMessagePayloadString(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"delivery_id": "delivery-1",
		"message": {
			"id": "queue-message-1",
			"payload": "{\"protocol\":\"runtime.envelope.v1\",\"type\":\"skill_result\",\"request_id\":\"request-1\",\"payload\":{\"ok\":true}}"
		}
	}`)

	response, err := decodePullResponsePayload(raw, "pull response")
	if err != nil {
		t.Fatalf("decode pull response: %v", err)
	}
	if response.OpenClawMessage.Type != "skill_result" {
		t.Fatalf("type = %q, want skill_result", response.OpenClawMessage.Type)
	}
	if response.OpenClawMessage.RequestID != "request-1" {
		t.Fatalf("request_id = %q, want request-1", response.OpenClawMessage.RequestID)
	}
	payload, ok := response.OpenClawMessage.Payload.(map[string]any)
	if !ok || payload["ok"] != true {
		t.Fatalf("payload = %#v, want ok true", response.OpenClawMessage.Payload)
	}
}
