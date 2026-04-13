package hub_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got := body["bind_token"]; got != "bind-token" {
			t.Fatalf("unexpected bind token payload: %#v", body)
		}
		if got := body["handle"]; got != "dispatch" {
			t.Fatalf("unexpected bind handle payload: %#v", body)
		}
		for _, forbidden := range []string{"hub_url", "hubUrl", "agent_id", "bindToken", "token"} {
			if _, exists := body[forbidden]; exists {
				t.Fatalf("unexpected legacy key %q in payload: %#v", forbidden, body)
			}
		}
		if len(body) != 2 {
			t.Fatalf("expected canonical bind payload with 2 fields, got %#v", body)
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

func TestBindAgentParsesNestedAgentAccessToken(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents/bind" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"agent": map[string]any{
					"access_token": "  agent-token  ",
					"agent_uuid":   "agent-uuid",
					"agent_uri":    "molten://agent/dispatch",
					"handle":       "dispatch",
				},
				"api_base": server.URL + "/v1",
				"endpoints": map[string]any{
					"capabilities": server.URL + "/runtime/capabilities",
					"metadata":     server.URL + "/runtime/profile",
				},
			},
		})
	}))
	defer server.Close()

	client := hub.NewClient(server.URL)
	response, err := client.BindAgent(context.Background(), hub.BindRequest{
		BindToken: "bind-token",
		Handle:    "dispatch",
	})
	if err != nil {
		t.Fatalf("bind agent: %v", err)
	}

	if got, want := response.AgentToken, "agent-token"; got != want {
		t.Fatalf("agent token = %q, want %q", got, want)
	}
	if got, want := response.AgentUUID, "agent-uuid"; got != want {
		t.Fatalf("agent uuid = %q, want %q", got, want)
	}
	if got, want := response.AgentURI, "molten://agent/dispatch"; got != want {
		t.Fatalf("agent uri = %q, want %q", got, want)
	}
	if got, want := response.Endpoints.Capabilities, server.URL+"/runtime/capabilities"; got != want {
		t.Fatalf("capabilities endpoint = %q, want %q", got, want)
	}
	if got, want := response.Endpoints.Metadata, server.URL+"/runtime/profile"; got != want {
		t.Fatalf("metadata endpoint = %q, want %q", got, want)
	}
}

func TestBindAgentParsesTopLevelPayloadWithoutRuntimeEnvelope(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents/bind" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"agent_token": "agent-token",
			"agent_uuid":  "agent-uuid",
			"agent_uri":   "molten://agent/dispatch",
			"handle":      "dispatch",
			"api_base":    server.URL + "/v1",
		})
	}))
	defer server.Close()

	client := hub.NewClient(server.URL)
	response, err := client.BindAgent(context.Background(), hub.BindRequest{
		BindToken: "bind-token",
	})
	if err != nil {
		t.Fatalf("bind agent: %v", err)
	}

	if got, want := response.AgentToken, "agent-token"; got != want {
		t.Fatalf("agent token = %q, want %q", got, want)
	}
	if got, want := response.APIBase, server.URL+"/v1"; got != want {
		t.Fatalf("api_base = %q, want %q", got, want)
	}
}

func TestBindAgentParsesBaseURLAndBindTokenAliases(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents/bind" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"bind_token": "agent-token",
				"base_url":   server.URL + "/v1",
				"agent_uuid": "agent-uuid",
				"agent_uri":  "molten://agent/dispatch",
				"handle":     "dispatch",
			},
		})
	}))
	defer server.Close()

	client := hub.NewClient(server.URL)
	response, err := client.BindAgent(context.Background(), hub.BindRequest{
		BindToken: "bind-token",
	})
	if err != nil {
		t.Fatalf("bind agent: %v", err)
	}

	if got, want := response.AgentToken, "agent-token"; got != want {
		t.Fatalf("agent token = %q, want %q", got, want)
	}
	if got, want := response.APIBase, server.URL+"/v1"; got != want {
		t.Fatalf("api_base = %q, want %q", got, want)
	}
}

func TestBindAgentDoesNotFallbackToBindTokensRouteOnInvalidBindPayload(t *testing.T) {
	t.Parallel()

	var bindRouteCalls int
	var bindTokensRouteCalls int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/bind":
			bindRouteCalls++
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":   "invalid_request",
				"message": "invalid JSON request",
			})
		case "/v1/agents/bind-tokens":
			bindTokensRouteCalls++
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":   "unauthorized",
				"message": "missing or invalid human auth",
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := hub.NewClient(server.URL)
	_, err := client.BindAgent(context.Background(), hub.BindRequest{
		BindToken: "bind-token",
	})
	if err == nil {
		t.Fatal("expected bind error")
	}
	if bindRouteCalls != 1 {
		t.Fatalf("expected one /bind attempt, got %d", bindRouteCalls)
	}
	if bindTokensRouteCalls != 0 {
		t.Fatalf("expected no /bind-tokens fallback on invalid bind payload, got %d", bindTokensRouteCalls)
	}
	if !strings.Contains(err.Error(), "/v1/agents/bind: hub API 400 invalid_request: invalid JSON request") {
		t.Fatalf("expected bind error details, got %v", err)
	}
}

func TestBindAgentDoesNotFallbackToBindTokensRouteWhenBindRouteIsMissing(t *testing.T) {
	t.Parallel()

	var bindRouteCalls int
	var bindTokensRouteCalls int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/bind":
			bindRouteCalls++
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":   "not_found",
				"message": "route not found",
			})
		case "/v1/agents/bind-tokens":
			bindTokensRouteCalls++
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":   "unauthorized",
				"message": "missing or invalid human auth",
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := hub.NewClient(server.URL)
	_, err := client.BindAgent(context.Background(), hub.BindRequest{
		BindToken: "bind-token",
		Handle:    "dispatch",
	})
	if err == nil {
		t.Fatal("expected bind error")
	}
	if bindRouteCalls != 1 {
		t.Fatalf("expected one /bind attempt, got %d", bindRouteCalls)
	}
	if bindTokensRouteCalls != 0 {
		t.Fatalf("expected no /bind-tokens fallback when /bind is missing, got %d", bindTokensRouteCalls)
	}
	if !strings.Contains(err.Error(), "/v1/agents/bind: hub API 404 not_found: route not found") {
		t.Fatalf("expected canonical bind-route error details, got %v", err)
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

func TestGetCapabilitiesTrimsAuthorizationBearerToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer agent-token"; got != want {
			t.Fatalf("authorization header = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"advertised_skills": []any{}},
		})
	}))
	defer server.Close()

	client := hub.NewClient(server.URL)
	if _, err := client.GetCapabilities(context.Background(), "  agent-token  "); err != nil {
		t.Fatalf("get capabilities: %v", err)
	}
}

func TestListAgentsUsesMeAgentsEndpointAndParsesEnvelope(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me/agents" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer human-token"; got != want {
			t.Fatalf("authorization header = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"agents": []map[string]any{
					{
						"agent_uuid": "agent-uuid",
						"agent_id":   "dispatch-agent",
						"uri":        "molten://agent/dispatch-agent",
						"status":     "online",
						"metadata": map[string]any{
							"display_name": "Dispatch Agent",
							"emoji":        "🤖",
							"presence": map[string]any{
								"status": "online",
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := hub.NewClient(server.URL)
	agents, err := client.ListAgents(context.Background(), " human-token ")
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected one agent, got %#v", agents)
	}
	if got, want := agents[0].AgentID, "dispatch-agent"; got != want {
		t.Fatalf("agent id = %q, want %q", got, want)
	}
	if agents[0].Metadata == nil || agents[0].Metadata.DisplayName != "Dispatch Agent" {
		t.Fatalf("unexpected metadata payload: %#v", agents[0])
	}
	if agents[0].Metadata.Presence == nil || agents[0].Metadata.Presence.Status != "online" {
		t.Fatalf("unexpected presence payload: %#v", agents[0].Metadata)
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

func TestUpdateMetadataFallsBackWhenCanonicalMetadataEndpointIsMissing(t *testing.T) {
	t.Parallel()

	var runtimeMetadataCalls int
	var canonicalMetadataCalls int
	var aliasCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/runtime/profile":
			runtimeMetadataCalls++
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":   "not_found",
				"message": "route not found",
			})
		case "/v1/agents/me/metadata":
			canonicalMetadataCalls++
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":   "not_found",
				"message": "route not found",
			})
		case "/v1/agents/me":
			aliasCalls++
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
	client.SetRuntimeEndpoints(hub.RuntimeEndpoints{
		MetadataURL: server.URL + "/runtime/profile",
	})

	_, err := client.UpdateMetadata(context.Background(), "agent-token", hub.UpdateMetadataRequest{
		Handle:   "dispatch-agent",
		Metadata: map[string]any{"agent_type": "dispatch"},
	})
	if err != nil {
		t.Fatalf("update metadata: %v", err)
	}
	if runtimeMetadataCalls != 1 {
		t.Fatalf("expected one runtime metadata call, got %d", runtimeMetadataCalls)
	}
	if canonicalMetadataCalls != 1 {
		t.Fatalf("expected one canonical metadata fallback call, got %d", canonicalMetadataCalls)
	}
	if aliasCalls != 1 {
		t.Fatalf("expected one alias fallback call, got %d", aliasCalls)
	}
}

func TestUpdateMetadataFallsBackWhenRuntimeMetadataEndpointReturnsUnauthorized(t *testing.T) {
	t.Parallel()

	var runtimeMetadataCalls int
	var canonicalMetadataCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/runtime/profile":
			runtimeMetadataCalls++
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":   "unauthorized",
				"message": "missing or invalid bearer token",
			})
		case "/v1/agents/me/metadata":
			canonicalMetadataCalls++
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
	client.SetRuntimeEndpoints(hub.RuntimeEndpoints{
		MetadataURL: server.URL + "/runtime/profile",
	})

	_, err := client.UpdateMetadata(context.Background(), "agent-token", hub.UpdateMetadataRequest{
		Metadata: map[string]any{"agent_type": "dispatch"},
	})
	if err != nil {
		t.Fatalf("update metadata: %v", err)
	}
	if runtimeMetadataCalls != 1 {
		t.Fatalf("expected one runtime metadata call, got %d", runtimeMetadataCalls)
	}
	if canonicalMetadataCalls != 1 {
		t.Fatalf("expected one canonical metadata fallback call, got %d", canonicalMetadataCalls)
	}
}

func TestOpenClawHTTPMethodsMatchRuntimeContract(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		called = map[string]bool{}
	)

	markCalled := func(name string) {
		mu.Lock()
		called[name] = true
		mu.Unlock()
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/runtime/openclaw/publish":
			markCalled("publish")
			if r.Method != http.MethodPost {
				t.Fatalf("publish method = %s, want %s", r.Method, http.MethodPost)
			}
			var payload hub.PublishRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode publish payload: %v", err)
			}
			if got := payload.Message.Type; got != "skill_request" {
				t.Fatalf("publish type = %q, want skill_request", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"message_id":  "message-1",
					"delivery":    "queued",
					"idempotency": "idem-1",
				},
			})
		case "/runtime/openclaw/pull":
			markCalled("pull")
			if r.Method != http.MethodGet {
				t.Fatalf("pull method = %s, want %s", r.Method, http.MethodGet)
			}
			if got := r.URL.Query().Get("timeout_ms"); got != "25000" {
				t.Fatalf("pull timeout_ms = %q, want 25000", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"delivery_id":     "delivery-1",
					"message_id":      "message-1",
					"from_agent_uuid": "source-agent",
					"openclaw_message": map[string]any{
						"type":       "skill_request",
						"request_id": "request-1",
					},
				},
			})
		case "/v1/openclaw/messages/ack":
			markCalled("ack")
			if r.Method != http.MethodPost {
				t.Fatalf("ack method = %s, want %s", r.Method, http.MethodPost)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode ack payload: %v", err)
			}
			if got := body["delivery_id"]; got != "delivery-ack" {
				t.Fatalf("ack delivery_id = %q, want delivery-ack", got)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/v1/openclaw/messages/nack":
			markCalled("nack")
			if r.Method != http.MethodPost {
				t.Fatalf("nack method = %s, want %s", r.Method, http.MethodPost)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode nack payload: %v", err)
			}
			if got := body["delivery_id"]; got != "delivery-nack" {
				t.Fatalf("nack delivery_id = %q, want delivery-nack", got)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/runtime/openclaw/offline":
			markCalled("offline")
			if r.Method != http.MethodPost {
				t.Fatalf("offline method = %s, want %s", r.Method, http.MethodPost)
			}
			var body hub.OfflineRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode offline payload: %v", err)
			}
			if body.SessionKey != "session-1" {
				t.Fatalf("offline session_key = %q, want session-1", body.SessionKey)
			}
			if body.Reason != "task failure id=task-1" {
				t.Fatalf("offline reason = %q, want task failure id=task-1", body.Reason)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := hub.NewClient(server.URL)
	client.SetRuntimeEndpoints(hub.RuntimeEndpoints{
		OpenClawPushURL:    server.URL + "/runtime/openclaw/publish",
		OpenClawPullURL:    server.URL + "/runtime/openclaw/pull",
		OpenClawOfflineURL: server.URL + "/runtime/openclaw/offline",
	})

	_, err := client.PublishOpenClaw(context.Background(), "agent-token", hub.PublishRequest{
		ToAgentUUID: "worker-1",
		ClientMsgID: "client-msg-1",
		Message: hub.OpenClawMessage{
			Type:      "skill_request",
			RequestID: "request-1",
			SkillName: "dispatch_skill_request",
			Payload:   map[string]any{"repo": "/tmp/repo"},
		},
	})
	if err != nil {
		t.Fatalf("publish openclaw: %v", err)
	}

	pull, ok, err := client.PullOpenClaw(context.Background(), "agent-token", 25*time.Second)
	if err != nil {
		t.Fatalf("pull openclaw: %v", err)
	}
	if !ok {
		t.Fatal("expected pull message")
	}
	if pull.DeliveryID != "delivery-1" {
		t.Fatalf("pull delivery_id = %q, want delivery-1", pull.DeliveryID)
	}

	if err := client.AckOpenClaw(context.Background(), "agent-token", "delivery-ack"); err != nil {
		t.Fatalf("ack openclaw: %v", err)
	}
	if err := client.NackOpenClaw(context.Background(), "agent-token", "delivery-nack"); err != nil {
		t.Fatalf("nack openclaw: %v", err)
	}
	if err := client.MarkOffline(context.Background(), "agent-token", hub.OfflineRequest{
		SessionKey: "session-1",
		Reason:     "task failure id=task-1",
	}); err != nil {
		t.Fatalf("mark offline: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, key := range []string{"publish", "pull", "ack", "nack", "offline"} {
		if !called[key] {
			t.Fatalf("expected %s call", key)
		}
	}
}

func TestCheckPingUsesRootPingPathFromVersionedBaseURL(t *testing.T) {
	t.Parallel()

	var requestPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := hub.NewClient(server.URL + "/v1")
	detail, err := client.CheckPing(context.Background())
	if err != nil {
		t.Fatalf("check ping: %v", err)
	}
	if requestPath != "/ping" {
		t.Fatalf("unexpected ping path: %q", requestPath)
	}
	if detail != server.URL+"/ping status=204" {
		t.Fatalf("unexpected ping detail: %q", detail)
	}
}

func TestCheckPingReturnsErrorWhenPingStatusIsNotSuccess(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := hub.NewClient(server.URL + "/v1")
	_, err := client.CheckPing(context.Background())
	if err == nil {
		t.Fatal("expected ping status error")
	}
	if !strings.Contains(err.Error(), server.URL+"/ping returned status=503") {
		t.Fatalf("unexpected ping error: %v", err)
	}
}

func TestCheckPingRejectsUnsupportedBaseURLScheme(t *testing.T) {
	t.Parallel()

	client := hub.NewClient("ftp://na.hub.molten.bot/v1")
	_, err := client.CheckPing(context.Background())
	if err == nil {
		t.Fatal("expected scheme validation error")
	}
	if !strings.Contains(err.Error(), "base URL must use http or https") {
		t.Fatalf("unexpected ping scheme error: %v", err)
	}
}
