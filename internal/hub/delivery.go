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

	message, ok, err := decodeOpenClawMessage(delivery.OpenClawMessage)
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

	if message, ok, err := openClawMessageFromA2AEnvelope(raw); err != nil || ok {
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

func openClawMessageFromA2AEnvelope(raw json.RawMessage) (OpenClawMessage, bool, error) {
	var envelope struct {
		Protocol string `json:"protocol"`
		Message  struct {
			Parts []struct {
				Data json.RawMessage `json:"data"`
				Text *string         `json:"text"`
			} `json:"parts"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return OpenClawMessage{}, false, err
	}
	if strings.TrimSpace(envelope.Protocol) != a2aProtocolAdapter || len(envelope.Message.Parts) == 0 {
		return OpenClawMessage{}, false, nil
	}

	for _, part := range envelope.Message.Parts {
		if len(strings.TrimSpace(string(part.Data))) > 0 && string(part.Data) != "null" {
			message, ok, err := decodeOpenClawMessageFromJSON(part.Data)
			if err != nil || ok {
				return message, ok, err
			}
		}
		if part.Text != nil {
			text := strings.TrimSpace(*part.Text)
			if strings.HasPrefix(text, "{") {
				message, ok, err := decodeOpenClawMessageFromJSON(json.RawMessage(text))
				if err != nil || ok {
					return message, ok, err
				}
			}
		}
	}
	return OpenClawMessage{}, false, nil
}

func decodeOpenClawMessageFromJSON(raw json.RawMessage) (OpenClawMessage, bool, error) {
	var wrapped struct {
		OpenClawMessage *OpenClawMessage `json:"openclaw_message"`
		Message         *OpenClawMessage `json:"message"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return OpenClawMessage{}, false, err
	}
	for _, candidate := range []*OpenClawMessage{wrapped.OpenClawMessage, wrapped.Message} {
		if candidate != nil && looksLikeOpenClawMessage(*candidate) {
			return *candidate, true, nil
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

func looksLikeOpenClawMessage(message OpenClawMessage) bool {
	return strings.TrimSpace(message.Protocol) == openClawProtocol ||
		strings.TrimSpace(message.Kind) != "" ||
		strings.TrimSpace(message.Type) != "" ||
		strings.TrimSpace(message.SkillName) != "" ||
		strings.TrimSpace(message.RequestID) != "" ||
		strings.TrimSpace(message.ReplyTo) != "" ||
		strings.TrimSpace(message.Status) != "" ||
		strings.TrimSpace(message.Error) != "" ||
		message.Payload != nil ||
		message.Input != nil
}
