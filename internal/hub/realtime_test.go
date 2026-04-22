package hub

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

func TestWebsocketURLSupportsAbsoluteEndpointOverride(t *testing.T) {
	t.Parallel()

	raw, err := websocketURL(
		"https://na.hub.molten.bot/v1",
		"https://runtime.na.hub.molten.bot/runtime/openclaw/ws?channel=dispatch",
		"main",
	)
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
	if parsed.Host != "runtime.na.hub.molten.bot" {
		t.Fatalf("host = %q, want runtime.na.hub.molten.bot", parsed.Host)
	}
	if parsed.Path != "/runtime/openclaw/ws" {
		t.Fatalf("path = %q, want /runtime/openclaw/ws", parsed.Path)
	}

	query := parsed.Query()
	if got := query.Get("channel"); got != "dispatch" {
		t.Fatalf("channel = %q, want dispatch", got)
	}
	if got := query.Get("session_key"); got != "main" {
		t.Fatalf("session_key = %q, want main", got)
	}
	if got := query.Get("sessionKey"); got != "main" {
		t.Fatalf("sessionKey = %q, want main", got)
	}
}

func TestWebsocketEndpointFromPull(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		pull string
		want string
	}{
		{
			name: "canonical messages route",
			pull: "https://na.hub.molten.bot/v1/openclaw/messages/pull",
			want: "https://na.hub.molten.bot/v1/openclaw/messages/ws",
		},
		{
			name: "runtime pull route",
			pull: "https://runtime.na.hub.molten.bot/runtime/openclaw/pull",
			want: "https://runtime.na.hub.molten.bot/runtime/openclaw/ws",
		},
		{
			name: "legacy messages alias route",
			pull: "https://runtime.na.hub.molten.bot/v1/openclaw/messages_pull",
			want: "https://runtime.na.hub.molten.bot/v1/openclaw/messages_ws",
		},
		{
			name: "unknown route",
			pull: "https://runtime.na.hub.molten.bot/v1/openclaw",
			want: "",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := websocketEndpointFromPull(tc.pull); got != tc.want {
				t.Fatalf("websocketEndpointFromPull(%q) = %q, want %q", tc.pull, got, tc.want)
			}
		})
	}
}

func TestConnectOpenClawUsesRuntimeEndpointDerivedFromPullURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runtime/openclaw/ws" {
			t.Fatalf("path = %q, want /runtime/openclaw/ws", r.URL.Path)
		}
		if got := r.URL.Query().Get("session_key"); got != "main" {
			t.Fatalf("session_key = %q, want main", got)
		}
		if got := r.URL.Query().Get("sessionKey"); got != "main" {
			t.Fatalf("sessionKey = %q, want main", got)
		}
		websocket.Handler(func(conn *websocket.Conn) {
			defer conn.Close()
			if err := websocket.JSON.Send(conn, map[string]any{"type": "session_ready"}); err != nil {
				t.Fatalf("send session_ready: %v", err)
			}
		}).ServeHTTP(w, r)
	}))
	defer server.Close()

	client := NewClient(server.URL + "/v1")
	client.SetRuntimeEndpoints(RuntimeEndpoints{
		OpenClawPullURL: server.URL + "/runtime/openclaw/pull",
	})

	session, err := client.ConnectOpenClaw(context.Background(), "agent-token", "main")
	if err != nil {
		t.Fatalf("ConnectOpenClaw() error = %v", err)
	}
	defer session.Close()
}

func TestConnectOpenClawUsesVersionedFallbackEndpointWhenRuntimePullEndpointMissing(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/openclaw/messages/ws" {
			t.Fatalf("path = %q, want /v1/openclaw/messages/ws", r.URL.Path)
		}
		if got := r.URL.Query().Get("session_key"); got != "main" {
			t.Fatalf("session_key = %q, want main", got)
		}
		if got := r.URL.Query().Get("sessionKey"); got != "main" {
			t.Fatalf("sessionKey = %q, want main", got)
		}
		websocket.Handler(func(conn *websocket.Conn) {
			defer conn.Close()
			if err := websocket.JSON.Send(conn, map[string]any{"type": "session_ready"}); err != nil {
				t.Fatalf("send session_ready: %v", err)
			}
		}).ServeHTTP(w, r)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	session, err := client.ConnectOpenClaw(context.Background(), "agent-token", "main")
	if err != nil {
		t.Fatalf("ConnectOpenClaw() error = %v", err)
	}
	defer session.Close()
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

func TestConnectOpenClawStartsHeartbeatPing(t *testing.T) {
	previousInterval := websocketHeartbeatInterval
	websocketHeartbeatInterval = 20 * time.Millisecond
	defer func() {
		websocketHeartbeatInterval = previousInterval
	}()

	pingSeen := make(chan byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("response writer does not support hijacking")
		}
		conn, rw, err := hijacker.Hijack()
		if err != nil {
			t.Fatalf("Hijack() error = %v", err)
		}
		defer conn.Close()

		key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
		if key == "" {
			t.Fatal("missing Sec-WebSocket-Key")
		}

		fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\n")
		fmt.Fprintf(rw, "Upgrade: websocket\r\n")
		fmt.Fprintf(rw, "Connection: Upgrade\r\n")
		fmt.Fprintf(rw, "Sec-WebSocket-Accept: %s\r\n\r\n", websocketAcceptKey(key))
		if err := rw.Flush(); err != nil {
			t.Fatalf("flush handshake: %v", err)
		}
		if err := writeWebsocketTextFrame(rw, `{"type":"session_ready"}`); err != nil {
			t.Fatalf("write session_ready: %v", err)
		}
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("SetReadDeadline() error = %v", err)
		}
		opcode, err := readClientFrameOpcode(rw.Reader)
		if err != nil {
			t.Fatalf("read client frame opcode: %v", err)
		}
		pingSeen <- opcode
	}))
	defer server.Close()

	client := NewClient(server.URL)
	session, err := client.ConnectOpenClaw(context.Background(), "agent-token", "main")
	if err != nil {
		t.Fatalf("ConnectOpenClaw() error = %v", err)
	}
	defer session.Close()

	select {
	case opcode := <-pingSeen:
		if opcode != websocket.PingFrame {
			t.Fatalf("client opcode = %d, want %d (ping)", opcode, websocket.PingFrame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat ping not observed")
	}
}

func websocketAcceptKey(key string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(key) + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func writeWebsocketTextFrame(w *bufio.ReadWriter, payload string) error {
	frame := []byte{0x81}
	data := []byte(payload)
	switch length := len(data); {
	case length <= 125:
		frame = append(frame, byte(length))
	case length < 65536:
		frame = append(frame, 126, byte(length>>8), byte(length))
	default:
		return fmt.Errorf("websocket test frame too large: %d", length)
	}
	frame = append(frame, data...)
	if _, err := w.Write(frame); err != nil {
		return err
	}
	return w.Flush()
}

func readClientFrameOpcode(r *bufio.Reader) (byte, error) {
	first, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	second, err := r.ReadByte()
	if err != nil {
		return 0, err
	}

	payloadLen := int(second & 0x7f)
	switch payloadLen {
	case 126:
		if _, err := ioReadFullDiscard(r, 2); err != nil {
			return 0, err
		}
	case 127:
		if _, err := ioReadFullDiscard(r, 8); err != nil {
			return 0, err
		}
	}

	if second&0x80 == 0 {
		return 0, fmt.Errorf("client frame missing mask bit")
	}
	if _, err := ioReadFullDiscard(r, 4); err != nil {
		return 0, err
	}
	if _, err := ioReadFullDiscard(r, payloadLen); err != nil {
		return 0, err
	}
	return first & 0x0f, nil
}

func ioReadFullDiscard(r *bufio.Reader, n int) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	return buf, err
}
