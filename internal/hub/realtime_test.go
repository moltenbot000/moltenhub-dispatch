package hub

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestWebsocketURLIncludesSessionKeyAliases(t *testing.T) {
	t.Parallel()

	raw, err := websocketURL("https://na.hub.molten.bot/v1", "/openclaw/messages/ws", "main")
	if err != nil {
		t.Fatalf("websocketURL() error = %v", err)
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse websocketURL(): %v", err)
	}
	if parsed.Scheme != "wss" {
		t.Fatalf("scheme = %q, want wss", parsed.Scheme)
	}
	if parsed.Path != "/v1/openclaw/messages/ws" {
		t.Fatalf("path = %q, want /v1/openclaw/messages/ws", parsed.Path)
	}

	query := parsed.Query()
	if got := query.Get("session_key"); got != "main" {
		t.Fatalf("session_key = %q, want main", got)
	}
	if got := query.Get("sessionKey"); got != "main" {
		t.Fatalf("sessionKey = %q, want main", got)
	}
}

func TestConnectOpenClawClosesSessionWhenContextCanceled(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		if err := websocket.JSON.Send(conn, map[string]any{"type": "session_ready"}); err != nil {
			return
		}
		var payload map[string]any
		_ = websocket.JSON.Receive(conn, &payload)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := NewClient(server.URL)
	session, err := client.ConnectOpenClaw(ctx, "agent-token", "main")
	if err != nil {
		t.Fatalf("ConnectOpenClaw() error = %v", err)
	}
	defer session.Close()

	recvDone := make(chan error, 1)
	go func() {
		_, recvErr := session.Receive(context.Background())
		recvDone <- recvErr
	}()

	cancel()

	select {
	case recvErr := <-recvDone:
		if recvErr == nil {
			t.Fatal("Receive() error = nil, want connection close error")
		}
		if !errors.Is(recvErr, context.Canceled) && recvErr.Error() == "" {
			t.Fatalf("Receive() error = %v, want close signal", recvErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Receive() did not unblock after context cancellation")
	}
}
