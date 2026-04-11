package hub

import (
	"net/url"
	"testing"
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
