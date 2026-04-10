package hub_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

func TestBindAgentParsesRuntimeEnvelope(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents/bind" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}

		var request hub.BindRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.BindToken != "bind-token" {
			t.Fatalf("unexpected bind token: %s", request.BindToken)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"agent_token": "agent-token",
				"agent_uuid":  "agent-uuid",
				"agent_uri":   "molten://agent/dispatch",
				"handle":      "dispatch",
				"api_base":    server.URL,
				"endpoints": map[string]any{
					"manifest":                  server.URL + "/v1/agents/me/manifest",
					"capabilities":              server.URL + "/v1/agents/me/capabilities",
					"metadata":                  server.URL + "/v1/agents/me/metadata",
					"openclaw_messages_pull":    server.URL + "/v1/openclaw/messages/pull",
					"openclaw_messages_publish": server.URL + "/v1/openclaw/messages/publish",
					"openclaw_offline":          server.URL + "/v1/openclaw/messages/offline",
				},
			},
		})
	}))
	defer server.Close()

	client := hub.NewClient(server.URL)
	response, err := client.BindAgent(context.Background(), hub.BindRequest{
		HubURL:    server.URL,
		BindToken: "bind-token",
		Handle:    "dispatch",
	})
	if err != nil {
		t.Fatalf("bind agent: %v", err)
	}

	if response.AgentToken != "agent-token" {
		t.Fatalf("unexpected token: %s", response.AgentToken)
	}
	if response.Endpoints.Offline == "" {
		t.Fatal("expected offline endpoint")
	}
}

func TestBindAgentReturnsCanonicalError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":       "agent_exists",
			"message":     "handle already claimed",
			"retryable":   true,
			"next_action": "retry_with_different_handle",
			"error_detail": map[string]any{
				"handle": "dispatch",
			},
		})
	}))
	defer server.Close()

	client := hub.NewClient(server.URL)
	_, err := client.BindAgent(context.Background(), hub.BindRequest{
		BindToken: "bind-token",
		Handle:    "dispatch",
	})
	if err == nil {
		t.Fatal("expected error")
	}

	apiErr, ok := err.(*hub.APIError)
	if !ok {
		t.Fatalf("expected APIError, got %T", err)
	}
	if apiErr.Code != "agent_exists" {
		t.Fatalf("unexpected code: %s", apiErr.Code)
	}
	if !apiErr.Retryable {
		t.Fatal("expected retryable error")
	}
}

func TestUpdateMetadataUsesAPIBasePathWithoutDoublingVersionPrefix(t *testing.T) {
	t.Parallel()

	var requestPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"status": "ok"},
		})
	}))
	defer server.Close()

	client := hub.NewClient(server.URL + "/v1")
	if _, err := client.UpdateMetadata(context.Background(), "agent-token", hub.UpdateMetadataRequest{
		Metadata: map[string]any{"display_name": "Dispatch Agent"},
	}); err != nil {
		t.Fatalf("update metadata: %v", err)
	}

	if requestPath != "/v1/agents/me/metadata" {
		t.Fatalf("unexpected request path: %s", requestPath)
	}
}

func TestUpdateMetadataFallsBackToAgentAliasWhenMetadataRouteIsMissing(t *testing.T) {
	t.Parallel()

	var metadataCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/me/metadata":
			metadataCalls++
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":   "not_found",
				"message": "route not found",
			})
		case "/v1/agents/me":
			if r.Method != http.MethodPatch {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			var request hub.UpdateMetadataRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if request.Handle != "dispatch-agent" {
				t.Fatalf("unexpected handle: %q", request.Handle)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":     true,
				"result": map[string]any{"status": "ok"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := hub.NewClient(server.URL)
	_, err := client.UpdateMetadata(context.Background(), "agent-token", hub.UpdateMetadataRequest{
		Handle:   "dispatch-agent",
		Metadata: map[string]any{"agent_type": "dispatch"},
	})
	if err != nil {
		t.Fatalf("update metadata: %v", err)
	}
	if metadataCalls != 1 {
		t.Fatalf("expected one metadata route attempt, got %d", metadataCalls)
	}
}

func TestUpdateMetadataUsesCanonicalMetadataEndpointWhenProvided(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runtime/profile" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPatch {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"status": "ok"},
		})
	}))
	defer server.Close()

	client := hub.NewClient(server.URL)
	client.SetRuntimeEndpoints(hub.RuntimeEndpoints{
		MetadataURL: server.URL + "/runtime/profile",
	})

	_, err := client.UpdateMetadata(context.Background(), "agent-token", hub.UpdateMetadataRequest{
		Metadata: map[string]any{"agent_type": "dispatch"},
	})
	if err != nil {
		t.Fatalf("update metadata: %v", err)
	}
}
