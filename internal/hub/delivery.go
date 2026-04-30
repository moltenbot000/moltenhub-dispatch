package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type deliveryPayload struct {
	Delivery struct {
		DeliveryID string `json:"delivery_id"`
	} `json:"delivery"`
	DeliveryID      string          `json:"delivery_id"`
	MessageID       string          `json:"message_id"`
	FromAgentUUID   string          `json:"from_agent_uuid"`
	FromAgentURI    string          `json:"from_agent_uri"`
	ToAgentUUID     string          `json:"to_agent_uuid"`
	ToAgentURI      string          `json:"to_agent_uri"`
	Message         json.RawMessage `json:"message"`
	OpenClawMessage json.RawMessage `json:"openclaw_message"`
}

func decodePullResponsePayload(raw json.RawMessage, source string) (PullResponse, error) {
	var delivery deliveryPayload
	if err := json.Unmarshal(raw, &delivery); err != nil {
		if strings.TrimSpace(source) == "" {
			source = "pull response"
		}
		return PullResponse{}, fmt.Errorf("decode %s: %w", source, err)
	}

	message, err := decodeOpenClawMessagePayload(delivery.OpenClawMessage)
	if err != nil {
		return PullResponse{}, err
	}
	if message.Kind == "" && message.Type == "" {
		fallback, fallbackErr := decodeOpenClawMessagePayload(delivery.Message)
		if fallbackErr != nil {
			return PullResponse{}, fallbackErr
		}
		if fallback.Kind != "" || fallback.Type != "" {
			message = fallback
		}
	}

	deliveryID := strings.TrimSpace(delivery.Delivery.DeliveryID)
	if deliveryID == "" {
		deliveryID = strings.TrimSpace(delivery.DeliveryID)
	}

	return PullResponse{
		DeliveryID:      deliveryID,
		MessageID:       strings.TrimSpace(delivery.MessageID),
		FromAgentUUID:   strings.TrimSpace(delivery.FromAgentUUID),
		FromAgentURI:    strings.TrimSpace(delivery.FromAgentURI),
		ToAgentUUID:     strings.TrimSpace(delivery.ToAgentUUID),
		ToAgentURI:      strings.TrimSpace(delivery.ToAgentURI),
		OpenClawMessage: message,
	}, nil
}

func decodeOpenClawMessagePayload(raw json.RawMessage) (OpenClawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return OpenClawMessage{}, nil
	}
	if message, ok, err := openClawMessageFromA2AEnvelope(raw); ok || err != nil {
		return message, err
	}

	var message OpenClawMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return OpenClawMessage{}, fmt.Errorf("decode openclaw message: %w", err)
	}
	return message, nil
}

func openClawMessageFromA2AEnvelope(raw json.RawMessage) (OpenClawMessage, bool, error) {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return OpenClawMessage{}, false, nil
	}
	if strings.TrimSpace(stringFromAny(envelope["protocol"])) != "a2a.v1" {
		return OpenClawMessage{}, false, nil
	}

	message, ok := envelope["message"].(map[string]any)
	if !ok {
		return OpenClawMessage{}, false, nil
	}
	parts, ok := message["parts"].([]any)
	if !ok {
		return OpenClawMessage{}, false, nil
	}
	for _, item := range parts {
		part, ok := item.(map[string]any)
		if !ok {
			continue
		}
		data, ok := part["data"].(map[string]any)
		if !ok || !looksLikeOpenClawMessage(data) {
			continue
		}
		if strings.TrimSpace(stringFromAny(data["protocol"])) == "" {
			data["protocol"] = "openclaw.http.v1"
		}
		payload, err := json.Marshal(data)
		if err != nil {
			return OpenClawMessage{}, true, fmt.Errorf("decode a2a openclaw data part: %w", err)
		}
		var out OpenClawMessage
		if err := json.Unmarshal(payload, &out); err != nil {
			return OpenClawMessage{}, true, fmt.Errorf("decode a2a openclaw data part: %w", err)
		}
		return out, true, nil
	}
	return OpenClawMessage{}, false, nil
}

func looksLikeOpenClawMessage(message map[string]any) bool {
	if len(message) == 0 {
		return false
	}
	if strings.TrimSpace(stringFromAny(message["protocol"])) == "openclaw.http.v1" {
		return true
	}
	for _, key := range []string{
		"kind",
		"type",
		"request_id",
		"reply_to_request_id",
		"reply_target",
		"skill_name",
		"payload_format",
		"status",
	} {
		if strings.TrimSpace(stringFromAny(message[key])) != "" {
			return true
		}
	}
	if _, ok := message["ok"]; ok {
		return true
	}
	if _, ok := message["error"]; ok {
		return true
	}
	return false
}

func stringFromAny(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
