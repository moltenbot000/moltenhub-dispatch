package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
)

const (
	a2aProtocolAdapter = "a2a.v1"
	a2aMIMEJSON        = "application/json"
	a2aMIMEText        = "text/plain"
	openClawProtocol   = "openclaw.http.v1"
)

func (c *Client) canPublishOpenClawViaA2A(req PublishRequest) bool {
	if c.a2aEndpointBaseURL() == "" {
		return false
	}
	return isUUIDLike(req.ToAgentUUID) || strings.TrimSpace(req.ToAgentURI) != ""
}

func (c *Client) publishOpenClawA2A(ctx context.Context, token string, req PublishRequest) (PublishResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := c.a2aEndpointBaseURL()
	if endpoint == "" {
		return PublishResponse{}, errors.New("a2a endpoint is not configured")
	}
	message, err := a2aSendMessageRequestFromOpenClaw(req)
	if err != nil {
		return PublishResponse{}, err
	}

	client, err := a2aclient.NewFromEndpoints(
		ctx,
		[]*a2a.AgentInterface{
			a2a.NewAgentInterface(endpoint, a2a.TransportProtocolJSONRPC),
			a2a.NewAgentInterface(endpoint, a2a.TransportProtocolHTTPJSON),
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
	defer func() { _ = client.Destroy() }()

	result, err := client.SendMessage(a2aContextWithBearer(ctx, token), message)
	if err != nil {
		return PublishResponse{}, err
	}
	return publishResponseFromA2AResult(result), nil
}

func (c *Client) publishOpenClawViaA2A(ctx context.Context, token string, req PublishRequest) (PublishResponse, error) {
	endpoint := c.a2aPublishEndpoint(req)
	if endpoint == "" {
		return PublishResponse{}, errors.New("a2a endpoint is not configured")
	}
	message, err := a2aSendMessageRequestFromOpenClaw(req)
	if err != nil {
		return PublishResponse{}, err
	}

	client, err := a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{
		a2a.NewAgentInterface(endpoint, a2a.TransportProtocolHTTPJSON),
	}, a2aclient.WithRESTTransport(c.httpClient))
	if err != nil {
		return PublishResponse{}, err
	}
	defer func() { _ = client.Destroy() }()

	result, err := client.SendMessage(a2aContextWithBearer(ctx, token), message)
	if err != nil {
		return PublishResponse{}, err
	}
	return publishResponseFromA2AResult(result), nil
}

func a2aSendMessageRequestFromOpenClaw(req PublishRequest) (*a2a.SendMessageRequest, error) {
	message := normalizeOpenClawMessageForA2A(req.Message)
	payload, err := openClawMessagePayload(message)
	if err != nil {
		return nil, err
	}

	part := a2a.NewDataPart(payload)
	part.MediaType = a2aMIMEJSON
	out := a2a.NewMessage(a2a.MessageRoleUser, part)
	if clientMsgID := strings.TrimSpace(req.ClientMsgID); clientMsgID != "" {
		out.ID = clientMsgID
	}
	if contextID := strings.TrimSpace(message.ReplyTo); contextID != "" {
		out.ContextID = contextID
	} else if contextID := strings.TrimSpace(message.RequestID); contextID != "" {
		out.ContextID = contextID
	}
	out.Metadata = a2aRoutingMetadata(req)

	return &a2a.SendMessageRequest{
		Config: &a2a.SendMessageConfig{
			AcceptedOutputModes: []string{a2aMIMEJSON, a2aMIMEText},
			ReturnImmediately:   true,
		},
		Message:  out,
		Metadata: a2aRoutingMetadata(req),
	}, nil
}

func normalizeOpenClawMessageForA2A(message OpenClawMessage) OpenClawMessage {
	if strings.TrimSpace(message.Protocol) == "" {
		message.Protocol = openClawProtocol
	}
	if strings.TrimSpace(message.Kind) == "" && strings.TrimSpace(message.Type) == "" {
		message.Kind = "agent_message"
	}
	if strings.TrimSpace(message.Timestamp) == "" {
		message.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return message
}

func openClawMessagePayload(message OpenClawMessage) (map[string]any, error) {
	raw, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("encode openclaw message for a2a: %w", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decode openclaw message for a2a: %w", err)
	}
	return payload, nil
}

func a2aRoutingMetadata(req PublishRequest) map[string]any {
	metadata := map[string]any{}
	if targetUUID := strings.TrimSpace(req.ToAgentUUID); targetUUID != "" && (req.PreferA2A || isUUIDLike(targetUUID)) {
		metadata["to_agent_uuid"] = targetUUID
	}
	if targetURI := strings.TrimSpace(req.ToAgentURI); targetURI != "" {
		metadata["to_agent_uri"] = targetURI
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func a2aContextWithBearer(ctx context.Context, token string) context.Context {
	token = strings.TrimSpace(token)
	if token == "" {
		return ctx
	}
	return a2aclient.AttachServiceParams(ctx, a2aclient.ServiceParams{
		"Authorization": []string{"Bearer " + token},
	})
}

func publishResponseFromA2AResult(result a2a.SendMessageResult) PublishResponse {
	switch typed := result.(type) {
	case *a2a.Task:
		return PublishResponse{
			MessageID:   strings.TrimSpace(string(typed.ID)),
			Delivery:    a2aDeliveryFromTaskState(typed.Status.State),
			Idempotency: a2aMetadataString(typed.Metadata, "moltenhub", "idempotency"),
		}
	case *a2a.Message:
		messageID := strings.TrimSpace(string(typed.TaskID))
		if messageID == "" {
			messageID = strings.TrimSpace(typed.ID)
		}
		return PublishResponse{
			MessageID:   messageID,
			Delivery:    "delivered",
			Idempotency: a2aMetadataString(typed.Metadata, "moltenhub", "idempotency"),
		}
	default:
		return PublishResponse{}
	}
}

func a2aDeliveryFromTaskState(state a2a.TaskState) string {
	switch state {
	case a2a.TaskStateSubmitted:
		return "queued"
	case a2a.TaskStateWorking:
		return "working"
	case a2a.TaskStateCompleted:
		return "delivered"
	case a2a.TaskStateFailed:
		return "failed"
	case a2a.TaskStateCanceled:
		return "canceled"
	case a2a.TaskStateRejected:
		return "rejected"
	default:
		delivery := strings.ToLower(strings.TrimPrefix(state.String(), "TASK_STATE_"))
		if delivery == "" || delivery == "unspecified" {
			return "queued"
		}
		return delivery
	}
}

func a2aMetadataString(metadata map[string]any, path ...string) string {
	var current any = metadata
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = mapped[key]
	}
	value, _ := current.(string)
	return strings.TrimSpace(value)
}

func (c *Client) a2aPublishEndpoint(req PublishRequest) string {
	base := c.a2aEndpointBaseURL()
	if base == "" {
		return ""
	}
	if targetUUID := strings.TrimSpace(req.ToAgentUUID); isUUIDLike(targetUUID) {
		return strings.TrimRight(base, "/") + "/agents/" + url.PathEscape(targetUUID)
	}
	return base
}

func (c *Client) a2aEndpointBaseURL() string {
	for _, endpoint := range []string{
		c.endpoints.ManifestURL,
		c.endpoints.CapabilitiesURL,
		c.endpoints.MetadataURL,
		c.endpoints.OpenClawPushURL,
		c.endpoints.OpenClawPullURL,
		c.endpoints.OpenClawOfflineURL,
	} {
		if apiBase := apiBaseFromRuntimeEndpoint(endpoint); apiBase != "" {
			return strings.TrimRight(apiBase, "/") + "/a2a"
		}
	}

	rawBase := strings.TrimSpace(c.baseURL)
	if rawBase == "" {
		return ""
	}
	u, err := url.Parse(rawBase)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || strings.TrimSpace(u.Host) == "" {
		return ""
	}
	u.RawQuery = ""
	u.Fragment = ""
	trimmedPath := strings.TrimRight(u.Path, "/")
	if trimmedPath == "" {
		u.Path = "/v1"
	} else {
		u.Path = trimmedPath
	}
	return strings.TrimRight(u.String(), "/") + "/a2a"
}

func apiBaseFromRuntimeEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || strings.TrimSpace(u.Host) == "" {
		return ""
	}
	trimmedPath := strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{
		"/agents/me/manifest",
		"/agents/me/capabilities",
		"/agents/me/metadata",
		"/agents/me",
		"/openclaw/messages/publish",
		"/openclaw/messages/pull",
		"/openclaw/messages/offline",
		"/messages/publish",
		"/messages/pull",
		"/messages/ack",
		"/messages/nack",
	} {
		if strings.HasSuffix(trimmedPath, suffix) {
			u.Path = strings.TrimSuffix(trimmedPath, suffix)
			u.RawPath = ""
			u.RawQuery = ""
			u.Fragment = ""
			return strings.TrimRight(u.String(), "/")
		}
	}
	return ""
}

func shouldFallbackOpenClawPublish(err error) bool {
	if err == nil {
		return false
	}
	for _, fallbackErr := range []error{
		a2a.ErrMethodNotFound,
		a2a.ErrUnsupportedOperation,
		a2a.ErrServerError,
	} {
		if errors.Is(err, fallbackErr) {
			return true
		}
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"404",
		"405",
		"not found",
		"method not found",
		"unsupported",
		"failed to send http request",
		"connection refused",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func isUUIDLike(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				return false
			}
		}
	}
	return true
}
