package hub

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
)

func (c *Client) publishOpenClawA2A(ctx context.Context, token string, req PublishRequest) (PublishResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpointURL, err := c.a2aEndpointURL()
	if err != nil {
		return PublishResponse{}, err
	}

	client, err := a2aclient.NewFromEndpoints(
		ctx,
		[]*a2a.AgentInterface{
			a2a.NewAgentInterface(endpointURL, a2a.TransportProtocolJSONRPC),
			a2a.NewAgentInterface(endpointURL, a2a.TransportProtocolHTTPJSON),
		},
		a2aclient.WithDefaultsDisabled(),
		a2aclient.WithConfig(a2aclient.Config{
			PreferredTransports: []a2a.TransportProtocol{
				a2a.TransportProtocolJSONRPC,
				a2a.TransportProtocolHTTPJSON,
			},
		}),
		a2aclient.WithJSONRPCTransport(c.httpClient),
		a2aclient.WithRESTTransport(c.httpClient),
	)
	if err != nil {
		return PublishResponse{}, err
	}
	defer client.Destroy()

	if bearer := strings.TrimSpace(token); bearer != "" {
		ctx = a2aclient.AttachServiceParams(ctx, a2aclient.ServiceParams{
			"Authorization": []string{"Bearer " + bearer},
		})
	}

	result, err := client.SendMessage(ctx, a2aSendMessageRequestFromOpenClaw(req))
	if err != nil {
		return PublishResponse{}, err
	}
	return publishResponseFromA2AResult(result)
}

func (c *Client) a2aEndpointURL() (string, error) {
	u, err := url.Parse(strings.TrimSpace(c.baseURL))
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("base URL must use http or https")
	}
	if strings.TrimSpace(u.Host) == "" {
		return "", fmt.Errorf("base URL host is required")
	}

	trimmedPath := strings.TrimRight(u.Path, "/")
	switch {
	case strings.HasSuffix(trimmedPath, "/a2a"):
		u.Path = trimmedPath
	case strings.HasSuffix(trimmedPath, "/v1"):
		u.Path = joinURLPath(trimmedPath, "a2a")
	default:
		u.Path = joinURLPath(trimmedPath, "v1/a2a")
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func a2aSendMessageRequestFromOpenClaw(req PublishRequest) *a2a.SendMessageRequest {
	part := a2a.NewDataPart(req.Message)
	part.MediaType = "application/json"

	message := a2a.NewMessage(a2aRoleFromOpenClaw(req.Message), part)
	if id := firstTrimmed(req.ClientMsgID, req.Message.RequestID); id != "" {
		message.ID = id
	}
	message.ContextID = firstTrimmed(req.Message.ReplyTo, req.Message.RequestID)
	message.Metadata = openClawA2AMetadata(req.Message)

	return &a2a.SendMessageRequest{
		Config: &a2a.SendMessageConfig{
			AcceptedOutputModes: []string{"application/json", "text/plain"},
			ReturnImmediately:   true,
		},
		Message:  message,
		Metadata: a2aRoutingMetadata(req),
	}
}

func a2aRoleFromOpenClaw(message OpenClawMessage) a2a.MessageRole {
	messageType := strings.ToLower(firstTrimmed(message.Type, message.Kind))
	if messageType == "skill_result" || message.OK != nil || strings.TrimSpace(message.Status) != "" {
		return a2a.MessageRoleAgent
	}
	return a2a.MessageRoleUser
}

func a2aRoutingMetadata(req PublishRequest) map[string]any {
	metadata := map[string]any{}
	if value := strings.TrimSpace(req.ToAgentUUID); value != "" {
		metadata["to_agent_uuid"] = value
	}
	if value := strings.TrimSpace(req.ToAgentURI); value != "" {
		metadata["to_agent_uri"] = value
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func openClawA2AMetadata(message OpenClawMessage) map[string]any {
	openClaw := map[string]any{}
	for key, value := range map[string]string{
		"protocol":            message.Protocol,
		"kind":                message.Kind,
		"type":                message.Type,
		"request_id":          message.RequestID,
		"reply_to_request_id": message.ReplyTo,
		"reply_target":        message.ReplyTarget,
		"skill_name":          message.SkillName,
		"payload_format":      message.PayloadFormat,
		"status":              message.Status,
	} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			openClaw[key] = trimmed
		}
	}
	if len(openClaw) == 0 {
		return nil
	}
	return map[string]any{"openclaw": openClaw}
}

func publishResponseFromA2AResult(result a2a.SendMessageResult) (PublishResponse, error) {
	switch typed := result.(type) {
	case *a2a.Task:
		messageID := firstTrimmed(a2aMetadataString(typed.Metadata, "moltenhub", "message_id"), string(typed.ID))
		if messageID == "" {
			return PublishResponse{}, fmt.Errorf("a2a publish response missing task id")
		}
		return PublishResponse{
			MessageID:   messageID,
			Delivery:    firstTrimmed(a2aMetadataString(typed.Metadata, "moltenhub", "status"), string(typed.Status.State)),
			Idempotency: a2aMetadataString(typed.Metadata, "moltenhub", "idempotency"),
		}, nil
	case *a2a.Message:
		messageID := strings.TrimSpace(typed.ID)
		if messageID == "" {
			return PublishResponse{}, fmt.Errorf("a2a publish response missing message id")
		}
		return PublishResponse{MessageID: messageID, Delivery: "message"}, nil
	default:
		return PublishResponse{}, fmt.Errorf("a2a publish returned unsupported result %T", result)
	}
}

func a2aMetadataString(metadata map[string]any, path ...string) string {
	var value any = metadata
	for _, key := range path {
		mapped, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		value = mapped[key]
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func firstTrimmed(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
