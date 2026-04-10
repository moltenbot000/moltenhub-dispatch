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
	HubURL    string `json:"hub_url,omitempty"`
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
	ManifestURL       string
	CapabilitiesURL   string
	MetadataURL       string
	OpenClawPullURL   string
	OpenClawPushURL   string
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
	var out BindResponse
	err := c.doJSON(ctx, http.MethodPost, "/v1/agents/bind", "", req, &out)
	return out, err
}

func (c *Client) UpdateMetadata(ctx context.Context, token string, req UpdateMetadataRequest) (map[string]any, error) {
	var out map[string]any
	endpoint := c.runtimeEndpoint(c.endpoints.MetadataURL, "/v1/agents/me/metadata")
	err := c.doJSON(ctx, http.MethodPatch, endpoint, token, req, &out)
	if err == nil {
		return out, nil
	}
	if c.endpoints.MetadataURL != "" || !isRouteNotFound(err) {
		return out, err
	}

	out = nil
	err = c.doJSON(ctx, http.MethodPatch, "/v1/agents/me", token, req, &out)
	return out, err
}

func (c *Client) GetCapabilities(ctx context.Context, token string) (map[string]any, error) {
	var out map[string]any
	err := c.doJSON(ctx, http.MethodGet, c.runtimeEndpoint(c.endpoints.CapabilitiesURL, "/v1/agents/me/capabilities"), token, nil, &out)
	return out, err
}

func (c *Client) PublishOpenClaw(ctx context.Context, token string, req PublishRequest) (PublishResponse, error) {
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
		OK     bool         `json:"ok"`
		Result PullResponse `json:"result"`
	}{}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return PullResponse{}, false, fmt.Errorf("decode pull response: %w", err)
	}
	return envelope.Result, true, nil
}

func (c *Client) AckOpenClaw(ctx context.Context, token, deliveryID string) error {
	return c.doJSON(ctx, http.MethodPost, "/v1/openclaw/messages/ack", token, map[string]string{
		"delivery_id": deliveryID,
	}, nil)
}

func (c *Client) NackOpenClaw(ctx context.Context, token, deliveryID string) error {
	return c.doJSON(ctx, http.MethodPost, "/v1/openclaw/messages/nack", token, map[string]string{
		"delivery_id": deliveryID,
	}, nil)
}

func (c *Client) MarkOffline(ctx context.Context, token string, req OfflineRequest) error {
	return c.doJSON(ctx, http.MethodPost, c.runtimeEndpoint(c.endpoints.OpenClawOfflineURL, "/v1/openclaw/messages/offline"), token, req, nil)
}

func (c *Client) runtimeEndpoint(override, fallback string) string {
	override = strings.TrimSpace(override)
	if override != "" {
		return override
	}
	return fallback
}

func isRouteNotFound(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == http.StatusNotFound && strings.EqualFold(strings.TrimSpace(apiErr.Code), "not_found")
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
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}

	envelope := struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}{}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode hub response: %w", err)
	}
	if out != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return fmt.Errorf("decode hub result payload: %w", err)
		}
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
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
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
