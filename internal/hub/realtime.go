package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

var websocketHeartbeatInterval = 15 * time.Second

type RealtimeSession interface {
	Receive(ctx context.Context) (PullResponse, error)
	Ack(ctx context.Context, deliveryID string) error
	Nack(ctx context.Context, deliveryID string) error
	Close() error
}

type websocketSession struct {
	conn      *websocket.Conn
	mu        sync.Mutex
	queue     []PullResponse
	closeOnce sync.Once
	closed    chan struct{}
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

func (c *Client) ConnectOpenClaw(ctx context.Context, token, sessionKey string) (RealtimeSession, error) {
	wsURL, err := websocketURL(c.baseURL, "/openclaw/messages/ws", sessionKey)
	if err != nil {
		return nil, err
	}

	config, err := websocket.NewConfig(wsURL, httpOriginFor(wsURL))
	if err != nil {
		return nil, fmt.Errorf("create websocket config: %w", err)
	}
	config.Header = http.Header{
		"Authorization": []string{"Bearer " + token},
		"User-Agent":    []string{c.userAgent},
	}

	conn, err := websocket.DialConfig(config)
	if err != nil {
		return nil, fmt.Errorf("open websocket session: %w", err)
	}

	session := &websocketSession{
		conn:   conn,
		closed: make(chan struct{}),
	}
	first, err := session.readEnvelope(ctx)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	if !strings.EqualFold(first.Type, "session_ready") {
		_ = session.Close()
		return nil, fmt.Errorf("unexpected websocket handshake message type %q", first.Type)
	}
	session.bindContext(ctx)
	session.startHeartbeat(ctx)
	return session, nil
}

func (s *websocketSession) Receive(ctx context.Context) (PullResponse, error) {
	s.mu.Lock()
	if len(s.queue) > 0 {
		message := s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()
		return message, nil
	}
	s.mu.Unlock()

	for {
		envelope, err := s.readEnvelope(ctx)
		if err != nil {
			return PullResponse{}, err
		}
		switch {
		case strings.EqualFold(envelope.Type, "delivery"):
			return decodePullResponsePayload(envelope.Result, "realtime delivery")
		case strings.EqualFold(envelope.Type, "__close__"):
			return PullResponse{}, fmt.Errorf("hub websocket session closed")
		case strings.EqualFold(envelope.Type, "__error__"):
			return PullResponse{}, fmt.Errorf("hub websocket error: %s", envelope.Error.Message)
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

	requestID := action + ":" + deliveryID
	if err := s.writeEnvelope(ctx, map[string]any{
		"type":        action,
		"request_id":  requestID,
		"delivery_id": deliveryID,
	}); err != nil {
		return err
	}

	for {
		envelope, err := s.readEnvelope(ctx)
		if err != nil {
			return err
		}
		switch {
		case strings.EqualFold(envelope.Type, "delivery"):
			message, decodeErr := decodePullResponsePayload(envelope.Result, "realtime delivery")
			if decodeErr != nil {
				return decodeErr
			}
			s.mu.Lock()
			s.queue = append(s.queue, message)
			s.mu.Unlock()
		case strings.EqualFold(envelope.Type, "__close__"):
			return fmt.Errorf("hub websocket session closed")
		case strings.EqualFold(envelope.Type, "__error__"):
			return fmt.Errorf("hub websocket error: %s", envelope.Error.Message)
		case strings.EqualFold(envelope.Type, "response") && strings.TrimSpace(envelope.RequestID) == requestID:
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
		}
	}
}

func (s *websocketSession) writeEnvelope(ctx context.Context, payload any) error {
	if err := s.applyDeadline(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := websocket.JSON.Send(s.conn, payload); err != nil {
		return fmt.Errorf("send websocket payload: %w", err)
	}
	return nil
}

func (s *websocketSession) readEnvelope(ctx context.Context) (realtimeEnvelope, error) {
	if err := s.applyDeadline(ctx); err != nil {
		return realtimeEnvelope{}, err
	}
	var envelope realtimeEnvelope
	if err := websocket.JSON.Receive(s.conn, &envelope); err != nil {
		return realtimeEnvelope{}, fmt.Errorf("receive websocket payload: %w", err)
	}
	return envelope, nil
}

func (s *websocketSession) applyDeadline(ctx context.Context) error {
	if deadline, ok := ctx.Deadline(); ok {
		return s.conn.SetDeadline(deadline)
	}
	return s.conn.SetDeadline(time.Time{})
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
	if err := s.applyDeadline(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

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
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}

	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported websocket base URL scheme %q", u.Scheme)
	}

	u.Path = joinURLPath(u.Path, endpoint)
	query := u.Query()
	if strings.TrimSpace(sessionKey) != "" {
		query.Set("session_key", strings.TrimSpace(sessionKey))
		query.Set("sessionKey", strings.TrimSpace(sessionKey))
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
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
