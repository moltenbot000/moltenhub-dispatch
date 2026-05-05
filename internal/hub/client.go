package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/support"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
	userAgent  string
	endpoints  RuntimeEndpoints
}

type APIError struct {
	StatusCode int
	Code       string `json:"error"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable"`
	NextAction string `json:"next_action"`
	Detail     any    `json:"error_detail"`
}

func (e *APIError) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := e.Message
	if message == "" {
		message = http.StatusText(e.StatusCode)
	}
	if e.Code == "" {
		return fmt.Sprintf("hub API %d: %s", e.StatusCode, message)
	}
	return fmt.Sprintf("hub API %d %s: %s", e.StatusCode, e.Code, message)
}

type BindRequest struct {
	HubURL    string `json:"-"`
	BindToken string `json:"bind_token"`
	Handle    string `json:"handle,omitempty"`
}

type BindResponse struct {
	AgentToken string `json:"agent_token"`
	AgentUUID  string `json:"agent_uuid"`
	AgentURI   string `json:"agent_uri"`
	Handle     string `json:"handle"`
	APIBase    string `json:"api_base"`
	Endpoints  struct {
		Manifest     string `json:"manifest"`
		Capabilities string `json:"capabilities"`
		Metadata     string `json:"metadata"`
		MessagesPull string `json:"messages_pull"`
		MessagesPush string `json:"messages_publish"`
		OpenClawPull string `json:"openclaw_messages_pull"`
		OpenClawPush string `json:"openclaw_messages_publish"`
		Offline      string `json:"openclaw_offline"`
	} `json:"endpoints"`
}

type RuntimeEndpoints struct {
	ManifestURL        string
	CapabilitiesURL    string
	MetadataURL        string
	OpenClawPullURL    string
	OpenClawPushURL    string
	OpenClawOfflineURL string
}

type SkillMetadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type UpdateMetadataRequest struct {
	Handle   string         `json:"handle,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type PublishRequest struct {
	ToAgentUUID string          `json:"to_agent_uuid,omitempty"`
	ToAgentURI  string          `json:"to_agent_uri,omitempty"`
	ClientMsgID string          `json:"client_msg_id,omitempty"`
	Message     OpenClawMessage `json:"message"`
	PreferA2A   bool            `json:"-"`
}

type PublishResponse struct {
	MessageID   string `json:"message_id"`
	Delivery    string `json:"delivery"`
	Idempotency string `json:"idempotency"`
}

type OpenClawMessage struct {
	Protocol      string `json:"protocol,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Type          string `json:"type,omitempty"`
	Timestamp     string `json:"timestamp,omitempty"`
	SkillName     string `json:"skill_name,omitempty"`
	Message       string `json:"message,omitempty"`
	Payload       any    `json:"payload,omitempty"`
	PayloadFormat string `json:"payload_format,omitempty"`
	Input         any    `json:"input,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	ReplyTo       string `json:"reply_to_request_id,omitempty"`
	ReplyTarget   string `json:"reply_target,omitempty"`
	OK            *bool  `json:"ok,omitempty"`
	Error         string `json:"error,omitempty"`
	ErrorDetail   any    `json:"error_detail,omitempty"`
	Status        string `json:"status,omitempty"`
	A2AState      string `json:"a2a_state,omitempty"`
	TaskState     string `json:"task_state,omitempty"`
	StatusUpdate  any    `json:"statusUpdate,omitempty"`
	Details       any    `json:"details,omitempty"`
}

type PullResponse struct {
	DeliveryID      string          `json:"delivery_id"`
	MessageID       string          `json:"message_id"`
	FromAgentUUID   string          `json:"from_agent_uuid"`
	FromAgentURI    string          `json:"from_agent_uri"`
	ToAgentUUID     string          `json:"to_agent_uuid"`
	ToAgentURI      string          `json:"to_agent_uri"`
	OpenClawMessage OpenClawMessage `json:"openclaw_message"`
}

type OfflineRequest struct {
	SessionKey string `json:"session_key,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		userAgent: "moltenhub-dispatch/1.0",
	}
}

func (c *Client) SetRuntimeEndpoints(endpoints RuntimeEndpoints) {
	c.endpoints = endpoints
}

func (c *Client) SetBaseURL(baseURL string) {
	c.baseURL = strings.TrimRight(baseURL, "/")
}

func (c *Client) SetHTTPClient(client *http.Client) {
	if client != nil {
		c.httpClient = client
	}
}

func (c *Client) BindAgent(ctx context.Context, req BindRequest) (BindResponse, error) {
	bindToken := strings.TrimSpace(req.BindToken)
	if bindToken == "" {
		return BindResponse{}, errors.New("bind token is required")
	}

	requestBody := struct {
		BindToken string `json:"bind_token"`
		Handle    string `json:"handle,omitempty"`
	}{
		BindToken: bindToken,
		Handle:    strings.TrimSpace(req.Handle),
	}

	out, bindErr := c.bindAgent(ctx, "/v1/agents/bind", requestBody)
	if bindErr == nil {
		return out, nil
	}

	var apiErr *APIError
	if errors.As(bindErr, &apiErr) && apiErr.StatusCode == http.StatusConflict {
		return BindResponse{}, bindErr
	}
	return BindResponse{}, fmt.Errorf("/v1/agents/bind: %w", bindErr)
}

func (c *Client) bindAgent(ctx context.Context, endpoint string, requestBody any) (BindResponse, error) {
	var payload json.RawMessage
	if err := c.doJSON(ctx, http.MethodPost, endpoint, "", requestBody, &payload); err != nil {
		return BindResponse{}, err
	}
	return parseBindResponsePayload(payload)
}

func (c *Client) UpdateMetadata(ctx context.Context, token string, req UpdateMetadataRequest) (map[string]any, error) {
	candidates := []string{
		strings.TrimSpace(c.endpoints.MetadataURL),
		"/v1/agents/me/metadata",
		"/v1/agents/me",
	}
	seen := make(map[string]struct{}, len(candidates))
	var lastErr error
	for _, endpoint := range candidates {
		endpoint = strings.TrimSpace(endpoint)
		if endpoint == "" {
			continue
		}
		if _, ok := seen[endpoint]; ok {
			continue
		}
		seen[endpoint] = struct{}{}

		var out map[string]any
		err := c.doJSON(ctx, http.MethodPatch, endpoint, token, req, &out)
		if err == nil {
			return out, nil
		}
		if !shouldRetryMetadataEndpoint(err) {
			return nil, err
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("metadata endpoint is not configured")
}

func (c *Client) GetCapabilities(ctx context.Context, token string) (map[string]any, error) {
	var out map[string]any
	err := c.doJSON(ctx, http.MethodGet, c.runtimeEndpoint(c.endpoints.CapabilitiesURL, "/v1/agents/me/capabilities"), token, nil, &out)
	return out, err
}

func (c *Client) PublishOpenClaw(ctx context.Context, token string, req PublishRequest) (PublishResponse, error) {
	if req.PreferA2A {
		out, err := c.publishOpenClawA2A(ctx, token, req)
		if err == nil {
			return out, nil
		}
		if !shouldFallbackOpenClawPublish(err) {
			return PublishResponse{}, fmt.Errorf("a2a publish: %w", err)
		}
	}
	if c.canPublishOpenClawViaA2A(req) {
		out, err := c.publishOpenClawViaA2A(ctx, token, req)
		if err == nil {
			return out, nil
		}
		if !shouldFallbackOpenClawPublish(err) {
			return PublishResponse{}, fmt.Errorf("a2a publish: %w", err)
		}
	}
	return c.publishOpenClawHTTP(ctx, token, req)
}

func (c *Client) publishOpenClawHTTP(ctx context.Context, token string, req PublishRequest) (PublishResponse, error) {
	var out PublishResponse
	err := c.doJSON(ctx, http.MethodPost, c.runtimeEndpoint(c.endpoints.OpenClawPushURL, "/v1/openclaw/messages/publish"), token, req, &out)
	return out, err
}

func (c *Client) PullOpenClaw(ctx context.Context, token string, timeout time.Duration) (PullResponse, bool, error) {
	values := url.Values{}
	if timeout > 0 {
		values.Set("timeout_ms", fmt.Sprintf("%d", timeout.Milliseconds()))
	}
	endpoint := c.runtimeEndpoint(c.endpoints.OpenClawPullURL, "/v1/openclaw/messages/pull")
	if len(values) > 0 {
		endpoint += "?" + values.Encode()
	}

	req, err := c.newRequest(ctx, http.MethodGet, endpoint, token, nil)
	if err != nil {
		return PullResponse{}, false, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return PullResponse{}, false, fmt.Errorf("hub pull: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return PullResponse{}, false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return PullResponse{}, false, decodeAPIError(resp)
	}

	envelope := struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}{}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return PullResponse{}, false, fmt.Errorf("decode pull response: %w", err)
	}
	result, err := decodePullResponsePayload(envelope.Result, "pull response")
	if err != nil {
		return PullResponse{}, false, err
	}
	return result, true, nil
}

func (c *Client) AckOpenClaw(ctx context.Context, token, deliveryID string) error {
	return c.doJSON(ctx, http.MethodPost, c.openClawDeliveryEndpoint("ack"), token, map[string]string{
		"delivery_id": deliveryID,
	}, nil)
}

func (c *Client) NackOpenClaw(ctx context.Context, token, deliveryID string) error {
	return c.doJSON(ctx, http.MethodPost, c.openClawDeliveryEndpoint("nack"), token, map[string]string{
		"delivery_id": deliveryID,
	}, nil)
}

func (c *Client) MarkOffline(ctx context.Context, token string, req OfflineRequest) error {
	return c.doJSON(ctx, http.MethodPost, c.runtimeEndpoint(c.endpoints.OpenClawOfflineURL, "/v1/openclaw/messages/offline"), token, req, nil)
}

func (c *Client) CheckPing(ctx context.Context) (string, error) {
	pingURL, err := hubPingURL(c.baseURL)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pingURL, nil)
	if err != nil {
		return "", fmt.Errorf("build ping request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s failed: %w", pingURL, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	detail := fmt.Sprintf("%s status=%d", pingURL, resp.StatusCode)
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		trimmed = strings.Join(strings.Fields(trimmed), " ")
		if len(trimmed) > 120 {
			trimmed = trimmed[:117] + "..."
		}
		detail += fmt.Sprintf(" body=%q", trimmed)
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("GET %s returned status=%d", pingURL, resp.StatusCode)
	}
	return detail, nil
}

func (c *Client) runtimeEndpoint(override, fallback string) string {
	override = strings.TrimSpace(override)
	if override != "" {
		return override
	}
	return fallback
}

func (c *Client) openClawDeliveryEndpoint(action string) string {
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "ack", "nack":
	default:
		return ""
	}

	if endpoint := deliveryEndpointFromPull(c.endpoints.OpenClawPullURL, action); endpoint != "" {
		return endpoint
	}
	return "/v1/openclaw/messages/" + action
}

func deliveryEndpointFromPull(pullURL, action string) string {
	return openClawEndpointFromPull(pullURL, "/messages/"+action, "/messages_"+action, "/"+action)
}

func openClawEndpointFromPull(pullURL, messagesSuffix, legacyMessagesSuffix, pullSuffix string) string {
	pullURL = strings.TrimSpace(pullURL)
	if pullURL == "" {
		return ""
	}
	parsed, err := url.Parse(pullURL)
	if err != nil {
		return ""
	}

	trimmedPath := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(trimmedPath, "/messages/pull"):
		parsed.Path = strings.TrimSuffix(trimmedPath, "/messages/pull") + messagesSuffix
	case strings.HasSuffix(trimmedPath, "/messages_pull"):
		parsed.Path = strings.TrimSuffix(trimmedPath, "/messages_pull") + legacyMessagesSuffix
	case strings.HasSuffix(trimmedPath, "/pull"):
		parsed.Path = strings.TrimSuffix(trimmedPath, "/pull") + pullSuffix
	default:
		return ""
	}

	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func isRouteNotFound(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == http.StatusNotFound && strings.EqualFold(strings.TrimSpace(apiErr.Code), "not_found")
}

func shouldRetryMetadataEndpoint(err error) bool {
	if isRouteNotFound(err) {
		return true
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		return false
	}

	code := strings.ToLower(strings.TrimSpace(apiErr.Code))
	message := strings.ToLower(strings.TrimSpace(apiErr.Message))
	if code == "unauthorized" {
		return true
	}
	if strings.Contains(message, "missing or invalid bearer token") {
		return true
	}
	return false
}

func (c *Client) doJSON(ctx context.Context, method, endpoint, token string, body any, out any) error {
	req, err := c.newRequest(ctx, method, endpoint, token, body)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("hub %s %s: %w", method, endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeAPIError(resp)
	}

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read hub response: %w", err)
	}
	if out == nil {
		return nil
	}

	if len(bytes.TrimSpace(rawBody)) == 0 {
		return nil
	}

	payload := rawBody
	envelope := struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Data   json.RawMessage `json:"data"`
	}{}
	if err := json.Unmarshal(rawBody, &envelope); err == nil {
		switch {
		case len(bytes.TrimSpace(envelope.Result)) > 0:
			payload = envelope.Result
		case len(bytes.TrimSpace(envelope.Data)) > 0:
			payload = envelope.Data
		}
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode hub result payload: %w", err)
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, endpoint, token string, body any) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		reader = bytes.NewReader(payload)
	}

	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		u, err = url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("parse endpoint URL: %w", err)
		}
	} else {
		query := ""
		if idx := strings.Index(endpoint, "?"); idx >= 0 {
			query = endpoint[idx+1:]
			endpoint = endpoint[:idx]
		}
		u.Path = joinURLPath(u.Path, endpoint)
		u.RawQuery = query
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer := strings.TrimSpace(token); bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req, nil
}

func parseBindResponsePayload(payload json.RawMessage) (BindResponse, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return BindResponse{}, errors.New("bind response missing result payload")
	}

	var out BindResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return BindResponse{}, fmt.Errorf("decode bind response: %w", err)
	}

	var parsed any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return BindResponse{}, fmt.Errorf("decode bind response payload: %w", err)
	}
	normalizeBindResponse(&out, parsed)

	if strings.TrimSpace(out.AgentToken) == "" {
		return BindResponse{}, errors.New("bind response missing agent token")
	}
	return out, nil
}

func normalizeBindResponse(out *BindResponse, payload any) {
	if out == nil {
		return
	}

	out.AgentToken = mergePayloadString(out.AgentToken, payload, "agent_token", "access_token", "bearer_token", "bind_token", "bindToken", "token")
	out.AgentUUID = mergePayloadString(out.AgentUUID, payload, "agent_uuid", "agentUUID")
	out.AgentURI = mergePayloadString(out.AgentURI, payload, "agent_uri", "agentURI")
	out.Handle = mergePayloadString(out.Handle, payload, "handle")
	out.APIBase = mergePayloadString(out.APIBase, payload, "api_base", "apiBase", "base_url", "baseUrl")

	if endpoints := support.MapByKey(payload, "endpoints"); len(endpoints) > 0 {
		applyBindEndpoints(out, endpoints)
	}

	out.Endpoints.Manifest = strings.TrimSpace(out.Endpoints.Manifest)
	out.Endpoints.Capabilities = strings.TrimSpace(out.Endpoints.Capabilities)
	out.Endpoints.Metadata = strings.TrimSpace(out.Endpoints.Metadata)
	out.Endpoints.MessagesPull = strings.TrimSpace(out.Endpoints.MessagesPull)
	out.Endpoints.MessagesPush = strings.TrimSpace(out.Endpoints.MessagesPush)
	out.Endpoints.OpenClawPull = strings.TrimSpace(out.Endpoints.OpenClawPull)
	out.Endpoints.OpenClawPush = strings.TrimSpace(out.Endpoints.OpenClawPush)
	out.Endpoints.Offline = strings.TrimSpace(out.Endpoints.Offline)
}

func applyBindEndpoints(out *BindResponse, endpoints map[string]any) {
	if out == nil || len(endpoints) == 0 {
		return
	}

	out.Endpoints.Manifest = mergeEndpointString(out.Endpoints.Manifest, endpoints, "manifest", "manifest_url", "manifestURL")
	out.Endpoints.Capabilities = mergeEndpointString(out.Endpoints.Capabilities, endpoints, "capabilities", "capabilities_url", "capabilitiesURL")
	out.Endpoints.Metadata = mergeEndpointString(out.Endpoints.Metadata, endpoints, "metadata", "metadata_url", "metadataURL", "profile", "profile_url", "profileURL")
	out.Endpoints.MessagesPull = mergeEndpointString(out.Endpoints.MessagesPull, endpoints, "messages_pull", "messagesPull")
	out.Endpoints.MessagesPush = mergeEndpointString(out.Endpoints.MessagesPush, endpoints, "messages_publish", "messagesPush")
	out.Endpoints.OpenClawPull = mergeEndpointString(out.Endpoints.OpenClawPull, endpoints, "openclaw_messages_pull", "openclaw_pull", "openclawPull")
	out.Endpoints.OpenClawPush = mergeEndpointString(out.Endpoints.OpenClawPush, endpoints, "openclaw_messages_publish", "openclaw_publish", "openclawPush")
	out.Endpoints.Offline = mergeEndpointString(out.Endpoints.Offline, endpoints, "openclaw_offline", "offline", "openclawOffline")
}

func mergePayloadString(current string, payload any, keys ...string) string {
	return support.FirstNonEmptyString(strings.TrimSpace(current), support.StringFromAny(payload, keys...))
}

func mergeEndpointString(current string, endpoints map[string]any, keys ...string) string {
	return support.FirstNonEmptyString(strings.TrimSpace(current), support.StringFromMap(endpoints, keys...))
}

func hubPingURL(baseURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("base URL must use http or https")
	}
	if strings.TrimSpace(u.Host) == "" {
		return "", fmt.Errorf("base URL host is required")
	}
	u.Path = "/ping"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func joinURLPath(basePath, endpoint string) string {
	base := strings.Trim(basePath, "/")
	next := strings.Trim(endpoint, "/")

	switch {
	case base == "" && next == "":
		return "/"
	case base == "":
		return "/" + next
	case next == "":
		return "/" + base
	case next == base || strings.HasPrefix(next, base+"/"):
		return "/" + next
	default:
		return "/" + path.Join(base, next)
	}
}

func decodeAPIError(resp *http.Response) error {
	apiErr := &APIError{StatusCode: resp.StatusCode}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		apiErr.Message = fmt.Sprintf("read error body: %v", err)
		return apiErr
	}
	if len(bytes.TrimSpace(body)) == 0 {
		apiErr.Message = http.StatusText(resp.StatusCode)
		return apiErr
	}
	if err := json.Unmarshal(body, apiErr); err == nil && (apiErr.Message != "" || apiErr.Code != "") {
		apiErr.StatusCode = resp.StatusCode
		return apiErr
	}
	apiErr.Message = strings.TrimSpace(string(body))
	return apiErr
}
