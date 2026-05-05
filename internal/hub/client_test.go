package hub_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

func newLoopbackServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on 127.0.0.1:0: %v", err)
	}

	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return server
}

func TestBindAgentParsesRuntimeEnvelope(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
					"manifest":     server.URL + "/v1/agents/me/manifest",
					"capabilities": server.URL + "/v1/agents/me/capabilities",
					"metadata":     server.URL + "/v1/agents/me/metadata",
				},
				"protocol_adapters": map[string]any{
					"runtime_v1": map[string]any{
						"protocol": "runtime.envelope.v1",
						"endpoints": map[string]any{
							"publish":   server.URL + "/v1/runtime/messages/publish",
							"pull":      server.URL + "/v1/runtime/messages/pull",
							"ack":       server.URL + "/v1/runtime/messages/ack",
							"nack":      server.URL + "/v1/runtime/messages/nack",
							"websocket": server.URL + "/v1/runtime/messages/ws",
							"offline":   server.URL + "/v1/runtime/messages/offline",
						},
					},
				},
			},
		})
	}))

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
	if got, want := response.Endpoints.RuntimePush, server.URL+"/v1/runtime/messages/publish"; got != want {
		t.Fatalf("runtime publish endpoint = %q, want %q", got, want)
	}
	if got := response.Endpoints.OpenClawPush; got != "" {
		t.Fatalf("openclaw publish endpoint = %q, want empty", got)
	}
}

func TestBindAgentParsesNestedAgentAccessToken(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	server = newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	server = newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	server = newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	server = newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"status": "ok"},
		})
	}))

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

	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents/me/capabilities" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer agent-token"; got != want {
			t.Fatalf("authorization header = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"advertised_skills": []any{}},
		})
	}))

	client := hub.NewClient(server.URL)
	if _, err := client.GetCapabilities(context.Background(), "  agent-token  "); err != nil {
		t.Fatalf("get capabilities: %v", err)
	}
}

func TestGetCapabilitiesParsesDataEnvelope(t *testing.T) {
	t.Parallel()

	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents/me/capabilities" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"data": map[string]any{
				"peer_skill_catalog": []map[string]any{
					{
						"agent_uuid": "agent-uuid",
						"agent_id":   "dispatch-agent",
						"uri":        "molten://agent/dispatch-agent",
						"metadata": map[string]any{
							"display_name": "Dispatch Agent",
							"emoji":        "🤖",
							"skills": []map[string]any{
								{
									"name":        "review_openapi",
									"description": "Review Hub API integration behavior.",
								},
							},
						},
					},
				},
			},
		})
	}))

	client := hub.NewClient(server.URL)
	capabilities, err := client.GetCapabilities(context.Background(), " human-token ")
	if err != nil {
		t.Fatalf("get capabilities: %v", err)
	}
	catalog, ok := capabilities["peer_skill_catalog"].([]any)
	if !ok || len(catalog) != 1 {
		t.Fatalf("expected peer skill catalog in data envelope, got %#v", capabilities)
	}
	entry, ok := catalog[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first catalog entry to be an object, got %#v", catalog[0])
	}
	if got, want := entry["agent_id"], "dispatch-agent"; got != want {
		t.Fatalf("agent id = %#v, want %q", got, want)
	}
	metadata, ok := entry["metadata"].(map[string]any)
	if !ok || metadata["display_name"] != "Dispatch Agent" {
		t.Fatalf("unexpected metadata payload: %#v", entry["metadata"])
	}
	skills, ok := metadata["skills"].([]any)
	if !ok || len(skills) != 1 {
		t.Fatalf("expected skills payload, got %#v", metadata["skills"])
	}
	skill, ok := skills[0].(map[string]any)
	if !ok || skill["name"] != "review_openapi" {
		t.Fatalf("unexpected skill payload: %#v", skills[0])
	}
}
func TestUpdateMetadataFallsBackToAgentAliasWhenMetadataRouteIsMissing(t *testing.T) {
	t.Parallel()

	var metadataCalls int
	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestPublishRuntimeMessagePreferA2AUsesStandardSendMessage(t *testing.T) {
	t.Parallel()

	var sawA2A bool
	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/a2a" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		sawA2A = true
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer agent-token" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}

		var rpc struct {
			JSONRPC string         `json:"jsonrpc"`
			Method  string         `json:"method"`
			Params  map[string]any `json:"params"`
			ID      any            `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			t.Fatalf("decode rpc request: %v", err)
		}
		if rpc.JSONRPC != "2.0" || rpc.Method != "SendMessage" {
			t.Fatalf("unexpected rpc request: %#v", rpc)
		}
		metadata, _ := rpc.Params["metadata"].(map[string]any)
		if got := metadata["to_agent_uuid"]; got != "worker-1" {
			t.Fatalf("target metadata = %#v, want worker-1", metadata)
		}
		message, _ := rpc.Params["message"].(map[string]any)
		if got := message["messageId"]; got != "client-msg-1" {
			t.Fatalf("message id = %v, want client-msg-1", got)
		}
		parts, _ := message["parts"].([]any)
		if len(parts) != 1 {
			t.Fatalf("parts = %#v, want one data part", parts)
		}
		part, _ := parts[0].(map[string]any)
		data, _ := part["data"].(map[string]any)
		if got := data["protocol"]; got != "runtime.envelope.v1" {
			t.Fatalf("data protocol = %v, want runtime.envelope.v1", got)
		}
		if got := data["type"]; got != "skill_request" {
			t.Fatalf("data type = %v, want skill_request", got)
		}
		if got := data["skill_name"]; got != "dispatch_skill_request" {
			t.Fatalf("skill_name = %v, want dispatch_skill_request", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      rpc.ID,
			"result": map[string]any{
				"task": map[string]any{
					"id":        "message-1",
					"contextId": "request-1",
					"status": map[string]any{
						"state": "TASK_STATE_SUBMITTED",
					},
					"metadata": map[string]any{
						"moltenhub": map[string]any{
							"message_id": "message-1",
							"status":     "queued",
						},
					},
				},
			},
		})
	}))

	client := hub.NewClient(server.URL + "/v1")
	response, err := client.PublishRuntimeMessage(context.Background(), "agent-token", hub.PublishRequest{
		ToAgentUUID: "worker-1",
		ClientMsgID: "client-msg-1",
		PreferA2A:   true,
		Message: hub.OpenClawMessage{
			Protocol:  "runtime.envelope.v1",
			Type:      "skill_request",
			RequestID: "request-1",
			SkillName: "dispatch_skill_request",
			Payload:   map[string]any{"repo": "/tmp/repo"},
		},
	})
	if err != nil {
		t.Fatalf("publish runtime over a2a: %v", err)
	}
	if !sawA2A {
		t.Fatal("expected a2a call")
	}
	if response.MessageID != "message-1" || response.Delivery != "queued" {
		t.Fatalf("unexpected publish response: %#v", response)
	}
}

func TestPublishRuntimeMessageDoesNotFallbackToOpenClawCompatibility(t *testing.T) {
	t.Parallel()

	var sawA2A, sawRuntime, sawOpenClaw bool
	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/a2a":
			sawA2A = true
			http.NotFound(w, r)
		case "/v1/runtime/messages/publish":
			sawRuntime = true
			http.NotFound(w, r)
		case "/v1/openclaw/messages/publish":
			sawOpenClaw = true
			t.Fatalf("unexpected retired openclaw publish call")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))

	client := hub.NewClient(server.URL + "/v1")
	_, err := client.PublishRuntimeMessage(context.Background(), "agent-token", hub.PublishRequest{
		ToAgentUUID: "worker-1",
		ClientMsgID: "client-msg-1",
		PreferA2A:   true,
		Message: hub.OpenClawMessage{
			Type:      "skill_request",
			RequestID: "request-1",
		},
	})
	if err == nil {
		t.Fatal("expected runtime publish error")
	}
	if !sawA2A || !sawRuntime || sawOpenClaw {
		t.Fatalf("expected a2a and runtime only, sawA2A=%v sawRuntime=%v sawOpenClaw=%v", sawA2A, sawRuntime, sawOpenClaw)
	}
}

func TestPullRuntimeMessageDecodesA2AWrappedRuntimeDataPart(t *testing.T) {
	t.Parallel()

	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runtime/messages/pull" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"delivery_id":     "delivery-1",
				"message_id":      "message-1",
				"from_agent_uuid": "source-agent",
				"envelope": map[string]any{
					"protocol": "a2a.v1",
					"kind":     "agent_message",
					"message": map[string]any{
						"messageId": "client-msg-1",
						"role":      "ROLE_USER",
						"parts": []any{
							map[string]any{
								"data": map[string]any{
									"protocol":       "runtime.envelope.v1",
									"type":           "skill_request",
									"request_id":     "request-1",
									"skill_name":     "dispatch_skill_request",
									"payload_format": "json",
									"payload": map[string]any{
										"repo": "/tmp/repo",
									},
								},
								"mediaType": "application/json",
							},
						},
					},
				},
			},
		})
	}))

	client := hub.NewClient(server.URL + "/v1")
	pull, ok, err := client.PullRuntimeMessage(context.Background(), "agent-token", time.Second)
	if err != nil {
		t.Fatalf("pull runtime: %v", err)
	}
	if !ok {
		t.Fatal("expected pull message")
	}
	if got := pull.OpenClawMessage.Type; got != "skill_request" {
		t.Fatalf("message type = %q, want skill_request", got)
	}
	if got := pull.OpenClawMessage.RequestID; got != "request-1" {
		t.Fatalf("request id = %q, want request-1", got)
	}
	if got := pull.OpenClawMessage.SkillName; got != "dispatch_skill_request" {
		t.Fatalf("skill name = %q, want dispatch_skill_request", got)
	}
	payload, ok := pull.OpenClawMessage.Payload.(map[string]any)
	if !ok || payload["repo"] != "/tmp/repo" {
		t.Fatalf("payload = %#v, want repo", pull.OpenClawMessage.Payload)
	}
}

func TestPullRuntimeMessageDecodesA2ATextPartAsTextMessage(t *testing.T) {
	t.Parallel()

	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runtime/messages/pull" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"delivery_id":     "delivery-1",
				"message_id":      "message-1",
				"from_agent_uuid": "source-agent",
				"envelope": map[string]any{
					"protocol": "a2a.v1",
					"message": map[string]any{
						"messageId": "client-msg-1",
						"role":      "ROLE_USER",
						"parts": []any{
							map[string]any{"text": "hello from a2a"},
						},
					},
				},
			},
		})
	}))

	client := hub.NewClient(server.URL + "/v1")
	pull, ok, err := client.PullRuntimeMessage(context.Background(), "agent-token", time.Second)
	if err != nil {
		t.Fatalf("pull runtime: %v", err)
	}
	if !ok {
		t.Fatal("expected pull message")
	}
	if got := pull.OpenClawMessage.Type; got != "text_message" {
		t.Fatalf("message type = %q, want text_message", got)
	}
	if got := pull.OpenClawMessage.RequestID; got != "client-msg-1" {
		t.Fatalf("request id = %q, want client-msg-1", got)
	}
	if got := pull.OpenClawMessage.Payload; got != "hello from a2a" {
		t.Fatalf("payload = %#v, want text", got)
	}
}

func TestRuntimeHTTPMethodsMatchRuntimeContract(t *testing.T) {
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

	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/runtime/messages/publish":
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
			if got := payload.Message.Protocol; got != "runtime.envelope.v1" {
				t.Fatalf("publish protocol = %q, want runtime.envelope.v1", got)
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
		case "/runtime/messages/pull":
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
					"envelope": map[string]any{
						"protocol":   "runtime.envelope.v1",
						"type":       "skill_request",
						"request_id": "request-1",
					},
				},
			})
		case "/runtime/messages/ack":
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
		case "/runtime/messages/nack":
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
		case "/runtime/messages/offline":
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

	client := hub.NewClient(server.URL)
	client.SetRuntimeEndpoints(hub.RuntimeEndpoints{
		RuntimePushURL:    server.URL + "/runtime/messages/publish",
		RuntimePullURL:    server.URL + "/runtime/messages/pull",
		RuntimeOfflineURL: server.URL + "/runtime/messages/offline",
	})

	_, err := client.PublishRuntimeMessage(context.Background(), "agent-token", hub.PublishRequest{
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
		t.Fatalf("publish runtime: %v", err)
	}

	pull, ok, err := client.PullRuntimeMessage(context.Background(), "agent-token", 25*time.Second)
	if err != nil {
		t.Fatalf("pull runtime: %v", err)
	}
	if !ok {
		t.Fatal("expected pull message")
	}
	if pull.DeliveryID != "delivery-1" {
		t.Fatalf("pull delivery_id = %q, want delivery-1", pull.DeliveryID)
	}

	if err := client.AckRuntimeMessage(context.Background(), "agent-token", "delivery-ack"); err != nil {
		t.Fatalf("ack runtime: %v", err)
	}
	if err := client.NackRuntimeMessage(context.Background(), "agent-token", "delivery-nack"); err != nil {
		t.Fatalf("nack runtime: %v", err)
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

func TestPublishRuntimeMessageUsesA2ASendMessageWhenTargetUUIDIsStandard(t *testing.T) {
	t.Parallel()

	targetUUID := "11111111-1111-4111-8111-111111111111"
	var a2aCalled bool
	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/a2a/agents/" + targetUUID + "/message:send":
			a2aCalled = true
			if r.Method != http.MethodPost {
				t.Fatalf("a2a method = %s, want %s", r.Method, http.MethodPost)
			}
			if got, want := r.Header.Get("Authorization"), "Bearer agent-token"; got != want {
				t.Fatalf("authorization header = %q, want %q", got, want)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode a2a payload: %v", err)
			}
			message, _ := body["message"].(map[string]any)
			if got, want := message["messageId"], "client-msg-1"; got != want {
				t.Fatalf("messageId = %#v, want %q", got, want)
			}
			metadata, _ := message["metadata"].(map[string]any)
			if got, want := metadata["to_agent_uuid"], targetUUID; got != want {
				t.Fatalf("message metadata to_agent_uuid = %#v, want %q", got, want)
			}
			parts, _ := message["parts"].([]any)
			if len(parts) != 1 {
				t.Fatalf("parts length = %d, want 1", len(parts))
			}
			part, _ := parts[0].(map[string]any)
			if got, want := part["mediaType"], "application/json"; got != want {
				t.Fatalf("part mediaType = %#v, want %q", got, want)
			}
			data, _ := part["data"].(map[string]any)
			if got, want := data["protocol"], "runtime.envelope.v1"; got != want {
				t.Fatalf("runtime protocol = %#v, want %q", got, want)
			}
			if got, want := data["type"], "skill_request"; got != want {
				t.Fatalf("runtime type = %#v, want %q", got, want)
			}
			if got, want := data["skill_name"], "dispatch_skill_request"; got != want {
				t.Fatalf("runtime skill_name = %#v, want %q", got, want)
			}
			payload, _ := data["payload"].(map[string]any)
			if got, want := payload["repo"], "/tmp/repo"; got != want {
				t.Fatalf("payload repo = %#v, want %q", got, want)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"task": map[string]any{
					"id":        "message-a2a",
					"contextId": "request-1",
					"status": map[string]any{
						"state": "TASK_STATE_SUBMITTED",
					},
				},
			})
		case "/v1/runtime/messages/publish", "/v1/openclaw/messages/publish":
			t.Fatal("unexpected runtime publish fallback")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))

	client := hub.NewClient(server.URL + "/v1")
	response, err := client.PublishRuntimeMessage(context.Background(), "agent-token", hub.PublishRequest{
		ToAgentUUID: targetUUID,
		ClientMsgID: "client-msg-1",
		Message: hub.OpenClawMessage{
			Type:      "skill_request",
			RequestID: "request-1",
			SkillName: "dispatch_skill_request",
			Payload:   map[string]any{"repo": "/tmp/repo"},
		},
	})
	if err != nil {
		t.Fatalf("publish runtime via a2a: %v", err)
	}
	if !a2aCalled {
		t.Fatal("expected A2A send call")
	}
	if got, want := response.MessageID, "message-a2a"; got != want {
		t.Fatalf("message_id = %q, want %q", got, want)
	}
	if got, want := response.Delivery, "queued"; got != want {
		t.Fatalf("delivery = %q, want %q", got, want)
	}
}

func TestPublishRuntimeMessageFallsBackWhenA2AEndpointIsUnavailable(t *testing.T) {
	t.Parallel()

	targetUUID := "22222222-2222-4222-8222-222222222222"
	var a2aCalls int
	var runtimeCalls int
	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/a2a/agents/" + targetUUID + "/message:send":
			a2aCalls++
			w.WriteHeader(http.StatusNotFound)
		case "/v1/runtime/messages/publish":
			runtimeCalls++
			var payload hub.PublishRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode fallback payload: %v", err)
			}
			if got, want := payload.ToAgentUUID, targetUUID; got != want {
				t.Fatalf("fallback target uuid = %q, want %q", got, want)
			}
			if got, want := payload.Message.Type, "skill_request"; got != want {
				t.Fatalf("fallback message type = %q, want %q", got, want)
			}
			if got, want := payload.Message.Protocol, "runtime.envelope.v1"; got != want {
				t.Fatalf("fallback protocol = %q, want %q", got, want)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"message_id":  "message-runtime",
					"delivery":    "queued",
					"idempotency": "idem-runtime",
				},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))

	client := hub.NewClient(server.URL + "/v1")
	response, err := client.PublishRuntimeMessage(context.Background(), "agent-token", hub.PublishRequest{
		ToAgentUUID: targetUUID,
		ClientMsgID: "client-msg-1",
		Message: hub.OpenClawMessage{
			Type:      "skill_request",
			RequestID: "request-1",
			SkillName: "dispatch_skill_request",
			Payload:   map[string]any{"repo": "/tmp/repo"},
		},
	})
	if err != nil {
		t.Fatalf("publish runtime fallback: %v", err)
	}
	if a2aCalls != 1 {
		t.Fatalf("a2a calls = %d, want 1", a2aCalls)
	}
	if runtimeCalls != 1 {
		t.Fatalf("runtime calls = %d, want 1", runtimeCalls)
	}
	if got, want := response.MessageID, "message-runtime"; got != want {
		t.Fatalf("message_id = %q, want %q", got, want)
	}
}

func TestRuntimeAckNackDoNotFallbackToOpenClawCompatibilityWhenRuntimeRouteMissing(t *testing.T) {
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

	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/runtime/messages/ack":
			markCalled("runtime_ack")
			http.NotFound(w, r)
		case "/v1/runtime/messages/nack":
			markCalled("runtime_nack")
			http.NotFound(w, r)
		case "/v1/openclaw/messages/ack":
			markCalled("ack")
			t.Fatalf("unexpected retired openclaw ack call")
		case "/v1/openclaw/messages/nack":
			markCalled("nack")
			t.Fatalf("unexpected retired openclaw nack call")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))

	client := hub.NewClient(server.URL)

	if err := client.AckRuntimeMessage(context.Background(), "agent-token", "delivery-ack"); err == nil {
		t.Fatal("expected ack runtime error")
	}
	if err := client.NackRuntimeMessage(context.Background(), "agent-token", "delivery-nack"); err == nil {
		t.Fatal("expected nack runtime error")
	}

	mu.Lock()
	defer mu.Unlock()
	if !called["runtime_ack"] {
		t.Fatal("expected runtime ack attempt")
	}
	if !called["runtime_nack"] {
		t.Fatal("expected runtime nack attempt")
	}
	if called["ack"] {
		t.Fatal("did not expect retired openclaw ack call")
	}
	if called["nack"] {
		t.Fatal("did not expect retired openclaw nack call")
	}
}

func TestPullRuntimeMessageDoesNotFallbackToOpenClawCompatibilityWhenRuntimeRouteMissing(t *testing.T) {
	t.Parallel()

	var (
		sawRuntime  bool
		sawOpenClaw bool
	)
	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/runtime/messages/pull":
			sawRuntime = true
			http.NotFound(w, r)
		case "/v1/openclaw/messages/pull":
			sawOpenClaw = true
			t.Fatalf("unexpected retired openclaw pull call")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))

	client := hub.NewClient(server.URL)
	_, ok, err := client.PullRuntimeMessage(context.Background(), "agent-token", 5*time.Second)
	if err == nil {
		t.Fatal("expected pull runtime error")
	}
	if ok {
		t.Fatal("did not expect pull message")
	}
	if !sawRuntime || sawOpenClaw {
		t.Fatalf("expected runtime only, sawRuntime=%v sawOpenClaw=%v", sawRuntime, sawOpenClaw)
	}
}

func TestPullRuntimeMessageDecodesMessageAliasesFromRuntimeContract(t *testing.T) {
	t.Parallel()

	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runtime/messages/pull" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"delivery": map[string]any{
					"delivery_id": "delivery-2",
				},
				"message_id":      "message-2",
				"from_agent_uuid": "source-agent",
				"message": map[string]any{
					"kind":       "skill_result",
					"request_id": "request-2",
				},
			},
		})
	}))

	client := hub.NewClient(server.URL)
	client.SetRuntimeEndpoints(hub.RuntimeEndpoints{
		RuntimePullURL: server.URL + "/runtime/messages/pull",
	})

	pull, ok, err := client.PullRuntimeMessage(context.Background(), "agent-token", 5*time.Second)
	if err != nil {
		t.Fatalf("pull runtime: %v", err)
	}
	if !ok {
		t.Fatal("expected pull message")
	}
	if got, want := pull.DeliveryID, "delivery-2"; got != want {
		t.Fatalf("delivery_id = %q, want %q", got, want)
	}
	if got, want := pull.OpenClawMessage.Kind, "skill_result"; got != want {
		t.Fatalf("message kind = %q, want %q", got, want)
	}
	if got, want := pull.OpenClawMessage.RequestID, "request-2"; got != want {
		t.Fatalf("request_id = %q, want %q", got, want)
	}
}

func TestPullRuntimeMessageUsesEnvelopeOnly(t *testing.T) {
	t.Parallel()

	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runtime/messages/pull" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"delivery_id":     "delivery-3",
				"message_id":      "message-3",
				"from_agent_uuid": "source-agent",
				"envelope": map[string]any{
					"protocol":   "runtime.envelope.v1",
					"type":       "skill_result",
					"request_id": "request-runtime",
				},
			},
		})
	}))

	client := hub.NewClient(server.URL)
	client.SetRuntimeEndpoints(hub.RuntimeEndpoints{
		RuntimePullURL: server.URL + "/runtime/messages/pull",
	})

	pull, ok, err := client.PullRuntimeMessage(context.Background(), "agent-token", 5*time.Second)
	if err != nil {
		t.Fatalf("pull runtime: %v", err)
	}
	if !ok {
		t.Fatal("expected pull message")
	}
	if got, want := pull.OpenClawMessage.Protocol, "runtime.envelope.v1"; got != want {
		t.Fatalf("protocol = %q, want %q", got, want)
	}
	if got, want := pull.OpenClawMessage.RequestID, "request-runtime"; got != want {
		t.Fatalf("request_id = %q, want %q", got, want)
	}
}

func TestPullRuntimeMessageDecodesA2ADataPartRuntimeEnvelope(t *testing.T) {
	t.Parallel()

	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runtime/messages/pull" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"delivery_id":     "delivery-a2a",
				"message_id":      "message-a2a",
				"from_agent_uuid": "source-agent",
				"envelope": map[string]any{
					"protocol": "a2a.v1",
					"message": map[string]any{
						"messageId": "client-msg-1",
						"parts": []map[string]any{
							{
								"mediaType": "application/json",
								"data": map[string]any{
									"protocol":       "runtime.envelope.v1",
									"type":           "skill_request",
									"request_id":     "request-a2a",
									"skill_name":     "dispatch_skill_request",
									"payload_format": "json",
									"payload": map[string]any{
										"repo": "/tmp/repo",
									},
								},
							},
						},
						"role": "ROLE_USER",
					},
				},
			},
		})
	}))

	client := hub.NewClient(server.URL)
	client.SetRuntimeEndpoints(hub.RuntimeEndpoints{
		RuntimePullURL: server.URL + "/runtime/messages/pull",
	})

	pull, ok, err := client.PullRuntimeMessage(context.Background(), "agent-token", 5*time.Second)
	if err != nil {
		t.Fatalf("pull runtime: %v", err)
	}
	if !ok {
		t.Fatal("expected pull message")
	}
	if got, want := pull.OpenClawMessage.Type, "skill_request"; got != want {
		t.Fatalf("message type = %q, want %q", got, want)
	}
	if got, want := pull.OpenClawMessage.RequestID, "request-a2a"; got != want {
		t.Fatalf("request_id = %q, want %q", got, want)
	}
	if got, want := pull.OpenClawMessage.SkillName, "dispatch_skill_request"; got != want {
		t.Fatalf("skill_name = %q, want %q", got, want)
	}
	payload, ok := pull.OpenClawMessage.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", pull.OpenClawMessage.Payload)
	}
	if got, want := payload["repo"], "/tmp/repo"; got != want {
		t.Fatalf("payload repo = %#v, want %q", got, want)
	}
}

func TestCheckPingUsesRootPingPathFromVersionedBaseURL(t *testing.T) {
	t.Parallel()

	var requestPath string
	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))

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

	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

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
