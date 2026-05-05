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
