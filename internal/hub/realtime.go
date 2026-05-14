package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

var (
	websocketHeartbeatInterval = 15 * time.Second
	websocketDialTimeout       = 8 * time.Second
	websocketHandshakeTimeout  = 8 * time.Second
)

type RealtimeSession interface {
	Receive(ctx context.Context) (PullResponse, error)
	Ack(ctx context.Context, deliveryID string) error
	Nack(ctx context.Context, deliveryID string) error
	Close() error
}

type websocketSession struct {
	conn       *websocket.Conn
	writeMu    sync.Mutex
	pendingMu  sync.Mutex
	pending    map[string]chan realtimeEnvelope
	deliveries chan PullResponse
	readErr    chan error
	closeOnce  sync.Once
	closed     chan struct{}
}

type realtimeEnvelope struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	OK        bool            `json:"ok"`
	Result    json.RawMessage `json:"result"`
	Error     struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) ConnectRuntimeMessages(ctx context.Context, token, sessionKey string) (RealtimeSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	endpoints := c.runtimeWebsocketEndpointCandidates()
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("runtime websocket endpoint is not configured")
	}

	var lastErr error
	for i, endpoint := range endpoints {
		wsURL, err := websocketURL(c.baseURL, endpoint, sessionKey)
		if err != nil {
			return nil, err
		}

		config, err := websocket.NewConfig(wsURL, httpOriginFor(wsURL))
		if err != nil {
			return nil, fmt.Errorf("create websocket config: %w", err)
		}
		if timeout := boundedTimeout(ctx, websocketDialTimeout); timeout > 0 {
			config.Dialer = &net.Dialer{Timeout: timeout}
		}
		config.Header = http.Header{
			"Authorization": []string{"Bearer " + token},
			"User-Agent":    []string{c.userAgent},
		}

		conn, err := websocket.DialConfig(config)
		if err != nil {
			lastErr = fmt.Errorf("open websocket session: %w", err)
			if i+1 < len(endpoints) && shouldRetryRuntimeWebsocketEndpoint(lastErr) {
				continue
			}
			return nil, lastErr
		}

		session := &websocketSession{
			conn:       conn,
			pending:    make(map[string]chan realtimeEnvelope),
			deliveries: make(chan PullResponse, 32),
			readErr:    make(chan error, 1),
			closed:     make(chan struct{}),
		}
		handshakeCtx, cancelHandshake := context.WithTimeout(ctx, boundedTimeout(ctx, websocketHandshakeTimeout))
		first, err := session.readEnvelope(handshakeCtx)
		cancelHandshake()
		if err != nil {
			_ = session.Close()
			return nil, err
		}
		if !strings.EqualFold(first.Type, "session_ready") {
			_ = session.Close()
			return nil, fmt.Errorf("unexpected websocket handshake message type %q", first.Type)
		}
		_ = session.conn.SetReadDeadline(time.Time{})
		session.bindContext(ctx)
		session.startReadLoop()
		session.startHeartbeat(ctx)
		return session, nil
	}
	return nil, lastErr
}

func (c *Client) ConnectOpenClaw(ctx context.Context, token, sessionKey string) (RealtimeSession, error) {
	return c.ConnectRuntimeMessages(ctx, token, sessionKey)
}

func boundedTimeout(ctx context.Context, fallback time.Duration) time.Duration {
	if fallback <= 0 {
		return 0
	}
	if ctx == nil {
		return fallback
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		switch {
		case remaining <= 0:
			return time.Millisecond
		case remaining < fallback:
			return remaining
		}
	}
	return fallback
}

func (c *Client) runtimeWebsocketEndpointCandidates() []string {
	return compactEndpoints(
		c.endpoints.RuntimeWebSocketURL,
		websocketEndpointFromPull(c.endpoints.RuntimePullURL),
		"/v1/runtime/messages/ws",
	)
}

func (s *websocketSession) Receive(ctx context.Context) (PullResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		select {
		case <-ctx.Done():
			return PullResponse{}, ctx.Err()
		case message := <-s.deliveries:
			return message, nil
		case err := <-s.readErr:
			return PullResponse{}, err
		case <-s.closed:
			return PullResponse{}, fmt.Errorf("hub websocket session closed")
		}
	}
}

func (s *websocketSession) Ack(ctx context.Context, deliveryID string) error {
	return s.respond(ctx, "ack", deliveryID)
}

func (s *websocketSession) Nack(ctx context.Context, deliveryID string) error {
	return s.respond(ctx, "nack", deliveryID)
}

func (s *websocketSession) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		if s.closed != nil {
			close(s.closed)
		}
		closeErr = s.conn.Close()
	})
	return closeErr
}

func (s *websocketSession) respond(ctx context.Context, action, deliveryID string) error {
	if strings.TrimSpace(deliveryID) == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	requestID := action + ":" + deliveryID
	responseCh := s.registerPending(requestID)
	defer s.unregisterPending(requestID)

	if err := s.writeEnvelope(ctx, map[string]any{
		"type":        action,
		"request_id":  requestID,
		"delivery_id": deliveryID,
	}); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case envelope := <-responseCh:
			if envelope.OK {
				return nil
			}
			code := strings.TrimSpace(envelope.Error.Code)
			message := strings.TrimSpace(envelope.Error.Message)
			if message == "" {
				message = "unknown websocket response error"
			}
			if code == "" {
				return fmt.Errorf("hub websocket %s failed: %s", action, message)
			}
			return fmt.Errorf("hub websocket %s failed (%s): %s", action, code, message)
		case err := <-s.readErr:
			return err
		case <-s.closed:
			return fmt.Errorf("hub websocket session closed")
		}
	}
}

func (s *websocketSession) writeEnvelope(ctx context.Context, payload any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.applyWriteDeadline(ctx); err != nil {
		return err
	}
	if err := websocket.JSON.Send(s.conn, payload); err != nil {
		return fmt.Errorf("send websocket payload: %w", err)
	}
	return nil
}

func (s *websocketSession) readEnvelope(ctx context.Context) (realtimeEnvelope, error) {
	if err := s.applyReadDeadline(ctx); err != nil {
		return realtimeEnvelope{}, err
	}
	var envelope realtimeEnvelope
	if err := websocket.JSON.Receive(s.conn, &envelope); err != nil {
		return realtimeEnvelope{}, fmt.Errorf("receive websocket payload: %w", err)
	}
	return envelope, nil
}

func (s *websocketSession) applyReadDeadline(ctx context.Context) error {
	if deadline, ok := ctx.Deadline(); ok {
		return s.conn.SetReadDeadline(deadline)
	}
	return s.conn.SetReadDeadline(time.Time{})
}

func (s *websocketSession) applyWriteDeadline(ctx context.Context) error {
	if deadline, ok := ctx.Deadline(); ok {
		return s.conn.SetWriteDeadline(deadline)
	}
	return s.conn.SetWriteDeadline(time.Time{})
}

func (s *websocketSession) startReadLoop() {
	if s == nil {
		return
	}
	go func() {
		for {
			envelope, err := s.readEnvelope(context.Background())
			if err != nil {
				s.failRead(fmt.Errorf("receive websocket payload: %w", err))
				return
			}
			switch {
			case strings.EqualFold(envelope.Type, "delivery"):
				message, decodeErr := decodePullResponsePayload(envelope.Result, "realtime delivery")
				if decodeErr != nil {
					s.failRead(decodeErr)
					return
				}
				select {
				case s.deliveries <- message:
				case <-s.closed:
					return
				}
			case strings.EqualFold(envelope.Type, "__close__"):
				s.failRead(fmt.Errorf("hub websocket session closed"))
				return
			case strings.EqualFold(envelope.Type, "__error__"):
				s.failRead(fmt.Errorf("hub websocket error: %s", envelope.Error.Message))
				return
			case strings.EqualFold(envelope.Type, "response"):
				s.deliverResponse(envelope)
			}
		}
	}()
}

func (s *websocketSession) registerPending(requestID string) chan realtimeEnvelope {
	ch := make(chan realtimeEnvelope, 1)
	s.pendingMu.Lock()
	s.pending[requestID] = ch
	s.pendingMu.Unlock()
	return ch
}

func (s *websocketSession) unregisterPending(requestID string) {
	s.pendingMu.Lock()
	delete(s.pending, requestID)
	s.pendingMu.Unlock()
}

func (s *websocketSession) deliverResponse(envelope realtimeEnvelope) {
	requestID := strings.TrimSpace(envelope.RequestID)
	if requestID == "" {
		return
	}
	s.pendingMu.Lock()
	ch := s.pending[requestID]
	s.pendingMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- envelope:
	default:
	}
}

func (s *websocketSession) failRead(err error) {
	if err != nil {
		select {
		case s.readErr <- err:
		default:
		}
	}
	_ = s.Close()
}

func (s *websocketSession) bindContext(ctx context.Context) {
	if s == nil || ctx == nil || ctx.Done() == nil {
		return
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.closed:
		}
	}()
}

func (s *websocketSession) startHeartbeat(ctx context.Context) {
	interval := websocketHeartbeatInterval
	if s == nil || interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.closed:
				return
			case <-ticker.C:
				if err := s.writePing(ctx, []byte("hb")); err != nil {
					_ = s.Close()
					return
				}
			}
		}
	}()
}

func (s *websocketSession) writePing(ctx context.Context, payload []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.applyWriteDeadline(ctx); err != nil {
		return err
	}

	previousType := s.conn.PayloadType
	s.conn.PayloadType = websocket.PingFrame
	defer func() {
		s.conn.PayloadType = previousType
	}()

	if _, err := s.conn.Write(payload); err != nil {
		return fmt.Errorf("send websocket ping: %w", err)
	}
	return nil
}

func websocketURL(baseURL, endpoint, sessionKey string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}

	endpointURL, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "", fmt.Errorf("parse websocket endpoint: %w", err)
	}

	u := base
	if strings.TrimSpace(endpointURL.Scheme) != "" {
		u = endpointURL
	} else {
		u.Path = joinURLPath(base.Path, endpointURL.Path)
		u.RawPath = ""
		u.RawQuery = endpointURL.RawQuery
		u.Fragment = endpointURL.Fragment
	}

	switch strings.ToLower(strings.TrimSpace(u.Scheme)) {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "wss", "ws":
	default:
		return "", fmt.Errorf("unsupported websocket base URL scheme %q", u.Scheme)
	}
	if strings.TrimSpace(u.Host) == "" {
		return "", fmt.Errorf("websocket URL host is required")
	}

	query := u.Query()
	if strings.TrimSpace(sessionKey) != "" {
		query.Set("session_key", strings.TrimSpace(sessionKey))
		query.Set("sessionKey", strings.TrimSpace(sessionKey))
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func websocketEndpointFromPull(pullURL string) string {
	return runtimeEndpointFromPull(pullURL, "/messages/ws", "/messages_ws", "/ws")
}

func shouldRetryRuntimeWebsocketEndpoint(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "404") ||
		strings.Contains(message, "405") ||
		strings.Contains(message, "bad status") ||
		strings.Contains(message, "not found") ||
		strings.Contains(message, "method not allowed")
}

func httpOriginFor(wsURL string) string {
	u, err := url.Parse(wsURL)
	if err != nil {
		return "http://localhost"
	}
	if u.Scheme == "wss" {
		u.Scheme = "https"
	} else {
		u.Scheme = "http"
	}
	u.Path = "/"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
