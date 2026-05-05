package hub

import (
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
	Envelope        json.RawMessage `json:"envelope"`
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

	message, ok, err := decodeOpenClawMessage(delivery.Envelope)
	if err != nil {
		return PullResponse{}, fmt.Errorf("decode %s envelope: %w", source, err)
	}
	if !ok {
		message, ok, err = decodeOpenClawMessage(delivery.OpenClawMessage)
		if err != nil {
			return PullResponse{}, fmt.Errorf("decode %s openclaw_message: %w", source, err)
		}
		if !ok {
			message, ok, err = decodeOpenClawMessage(delivery.Message)
			if err != nil {
				return PullResponse{}, fmt.Errorf("decode %s message: %w", source, err)
			}
			if !ok {
				message = OpenClawMessage{}
			}
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

func decodeOpenClawMessage(raw json.RawMessage) (OpenClawMessage, bool, error) {
	if len(strings.TrimSpace(string(raw))) == 0 || string(raw) == "null" {
		return OpenClawMessage{}, false, nil
	}

	if message, ok, err := openClawMessageFromA2A(raw); err != nil || ok {
		return message, ok, err
	}

	var message OpenClawMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return OpenClawMessage{}, false, err
	}
	if !looksLikeOpenClawMessage(message) {
		return OpenClawMessage{}, false, nil
	}
	return message, true, nil
}

func openClawMessageFromA2A(raw json.RawMessage) (OpenClawMessage, bool, error) {
	var envelope struct {
		Protocol string          `json:"protocol"`
		Message  json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return OpenClawMessage{}, false, err
	}
	if strings.TrimSpace(envelope.Protocol) == a2aProtocolAdapter && len(strings.TrimSpace(string(envelope.Message))) > 0 {
		message, ok, err := decodeA2AMessagePayload(envelope.Message)
		if err != nil || !ok {
			return OpenClawMessage{}, false, err
		}
		return openClawMessageFromA2APayload(message)
	}

	message, ok, err := decodeA2AMessagePayload(raw)
	if err != nil || !ok {
		return OpenClawMessage{}, false, err
	}
	if len(message.Parts) == 0 {
		return OpenClawMessage{}, false, nil
	}
	return openClawMessageFromA2APayload(message)
}

type a2aMessagePayload struct {
	MessageID string    `json:"messageId"`
	Role      string    `json:"role"`
	ContextID string    `json:"contextId"`
	TaskID    string    `json:"taskId"`
	Parts     []a2aPart `json:"parts"`
}

type a2aPart struct {
	Data json.RawMessage `json:"data"`
	Text *string         `json:"text"`
}

func decodeA2AMessagePayload(raw json.RawMessage) (a2aMessagePayload, bool, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return a2aMessagePayload{}, false, nil
	}
	if strings.HasPrefix(trimmed, "\"") {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return a2aMessagePayload{}, false, err
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return a2aMessagePayload{}, false, nil
		}
		return a2aMessagePayload{
			Parts: []a2aPart{{Text: &text}},
		}, true, nil
	}
	if !strings.HasPrefix(trimmed, "{") {
		return a2aMessagePayload{}, false, nil
	}
	var message a2aMessagePayload
	if err := json.Unmarshal(raw, &message); err != nil {
		return a2aMessagePayload{}, false, err
	}
	if len(message.Parts) == 0 {
		return a2aMessagePayload{}, false, nil
	}
	return message, true, nil
}

func openClawMessageFromA2APayload(message a2aMessagePayload) (OpenClawMessage, bool, error) {
	for _, part := range message.Parts {
		if len(strings.TrimSpace(string(part.Data))) > 0 && string(part.Data) != "null" {
			openClawMessage, ok, err := decodeOpenClawMessageFromJSON(part.Data)
			if err != nil || ok {
				return openClawMessage, ok, err
			}
		}
		if part.Text != nil {
			text := strings.TrimSpace(*part.Text)
			if strings.HasPrefix(text, "{") {
				openClawMessage, ok, err := decodeOpenClawMessageFromJSON(json.RawMessage(text))
				if err != nil || ok {
					return openClawMessage, ok, err
				}
			}
		}
	}

	if text := a2aTextMessagePayload(message.Parts); text != "" {
		requestID := strings.TrimSpace(message.MessageID)
		if requestID == "" {
			requestID = strings.TrimSpace(message.TaskID)
		}
		return OpenClawMessage{
			Protocol:  a2aProtocolAdapter,
			Kind:      "text_message",
			Type:      "text_message",
			RequestID: requestID,
			ReplyTo:   strings.TrimSpace(message.ContextID),
			Payload:   text,
		}, true, nil
	}
	return OpenClawMessage{}, false, nil
}

func a2aTextMessagePayload(parts []a2aPart) string {
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Text != nil {
			if text := strings.TrimSpace(*part.Text); text != "" {
				values = append(values, text)
			}
			continue
		}
		if len(strings.TrimSpace(string(part.Data))) > 0 && string(part.Data) != "null" {
			values = append(values, strings.TrimSpace(string(part.Data)))
		}
	}
	return strings.Join(values, "\n")
}

func decodeOpenClawMessageFromJSON(raw json.RawMessage) (OpenClawMessage, bool, error) {
	var wrapped struct {
		Envelope        json.RawMessage `json:"envelope"`
		OpenClawMessage json.RawMessage `json:"openclaw_message"`
		Message         json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return OpenClawMessage{}, false, err
	}
	for _, candidate := range []json.RawMessage{wrapped.Envelope, wrapped.OpenClawMessage, wrapped.Message} {
		message, ok, err := decodeWrappedOpenClawMessage(candidate)
		if err != nil || ok {
			return message, ok, err
		}
	}

	var message OpenClawMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return OpenClawMessage{}, false, err
	}
	if !looksLikeOpenClawMessage(message) {
		return OpenClawMessage{}, false, nil
	}
	return message, true, nil
}

func decodeWrappedOpenClawMessage(raw json.RawMessage) (OpenClawMessage, bool, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || !strings.HasPrefix(trimmed, "{") {
		return OpenClawMessage{}, false, nil
	}
	var message OpenClawMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return OpenClawMessage{}, false, err
	}
	if !looksLikeOpenClawMessage(message) {
		return OpenClawMessage{}, false, nil
	}
	return message, true, nil
}

func looksLikeOpenClawMessage(message OpenClawMessage) bool {
	return strings.TrimSpace(message.Protocol) == runtimeEnvelopeProtocol ||
		strings.TrimSpace(message.Protocol) == openClawProtocol ||
		strings.TrimSpace(message.Kind) != "" ||
		strings.TrimSpace(message.Type) != "" ||
		strings.TrimSpace(message.SkillName) != "" ||
		strings.TrimSpace(message.RequestID) != "" ||
		strings.TrimSpace(message.ReplyTo) != "" ||
		strings.TrimSpace(message.Status) != "" ||
		strings.TrimSpace(message.A2AState) != "" ||
		strings.TrimSpace(message.TaskState) != "" ||
		strings.TrimSpace(message.Error) != "" ||
		message.StatusUpdate != nil ||
		message.Payload != nil ||
		message.Input != nil
}
