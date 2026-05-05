package app

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

func runtimeEndpointsFromBind(result hub.BindResponse) hub.RuntimeEndpoints {
	return runtimeEndpointsFromSession(Session{
		ManifestURL:          result.Endpoints.Manifest,
		Capabilities:         result.Endpoints.Capabilities,
		MetadataURL:          result.Endpoints.Metadata,
		RuntimePullURL:       result.Endpoints.RuntimePull,
		RuntimePushURL:       result.Endpoints.RuntimePush,
		RuntimeAckURL:        result.Endpoints.RuntimeAck,
		RuntimeNackURL:       result.Endpoints.RuntimeNack,
		RuntimeStatusURL:     result.Endpoints.RuntimeStatus,
		RuntimeWebSocketURL:  result.Endpoints.RuntimeWebSocket,
		RuntimeOfflineURL:    result.Endpoints.RuntimeOffline,
		OpenClawPullURL:      result.Endpoints.OpenClawPull,
		OpenClawPushURL:      result.Endpoints.OpenClawPush,
		OpenClawAckURL:       result.Endpoints.OpenClawAck,
		OpenClawNackURL:      result.Endpoints.OpenClawNack,
		OpenClawStatusURL:    result.Endpoints.OpenClawStatus,
		OpenClawWebSocketURL: result.Endpoints.OpenClawWebSocket,
		OfflineURL:           result.Endpoints.Offline,
	})
}

func runtimeAPIBaseFromBind(result hub.BindResponse) string {
	return runtimeAPIBaseFromSession(Session{
		APIBase:              result.APIBase,
		ManifestURL:          result.Endpoints.Manifest,
		Capabilities:         result.Endpoints.Capabilities,
		MetadataURL:          result.Endpoints.Metadata,
		RuntimePullURL:       result.Endpoints.RuntimePull,
		RuntimePushURL:       result.Endpoints.RuntimePush,
		RuntimeAckURL:        result.Endpoints.RuntimeAck,
		RuntimeNackURL:       result.Endpoints.RuntimeNack,
		RuntimeStatusURL:     result.Endpoints.RuntimeStatus,
		RuntimeWebSocketURL:  result.Endpoints.RuntimeWebSocket,
		RuntimeOfflineURL:    result.Endpoints.RuntimeOffline,
		OpenClawPullURL:      result.Endpoints.OpenClawPull,
		OpenClawPushURL:      result.Endpoints.OpenClawPush,
		OpenClawAckURL:       result.Endpoints.OpenClawAck,
		OpenClawNackURL:      result.Endpoints.OpenClawNack,
		OpenClawStatusURL:    result.Endpoints.OpenClawStatus,
		OpenClawWebSocketURL: result.Endpoints.OpenClawWebSocket,
		OfflineURL:           result.Endpoints.Offline,
	})
}

func runtimeAPIBaseFromSession(session Session) string {
	if apiBase := coalesceTrimmed(session.APIBase, session.BaseURL); apiBase != "" {
		return apiBase
	}
	for _, endpoint := range []string{
		session.MetadataURL,
		session.Capabilities,
		session.RuntimePullURL,
		session.RuntimePushURL,
		session.RuntimeAckURL,
		session.RuntimeNackURL,
		session.RuntimeStatusURL,
		session.RuntimeWebSocketURL,
		session.RuntimeOfflineURL,
		session.OpenClawPullURL,
		session.OpenClawPushURL,
		session.OpenClawAckURL,
		session.OpenClawNackURL,
		session.OpenClawStatusURL,
		session.OpenClawWebSocketURL,
		session.OfflineURL,
		session.ManifestURL,
	} {
		if apiBase := runtimeAPIBaseFromEndpoint(endpoint); apiBase != "" {
			return apiBase
		}
	}
	return ""
}

func defaultAPIBaseForHub(hubURL string) string {
	hubURL = strings.TrimRight(strings.TrimSpace(hubURL), "/")
	if hubURL == "" {
		return ""
	}
	return hubURL + "/v1"
}

func runtimeAPIBaseFromEndpoint(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}

	trimmedPath := strings.TrimRight(parsed.Path, "/")
	for _, suffix := range []string{
		"/v1/agents/me/metadata",
		"/v1/agents/me/capabilities",
		"/v1/agents/me/manifest",
		"/v1/agents/me",
		"/v1/runtime/messages/pull",
		"/v1/runtime/messages/publish",
		"/v1/runtime/messages/ack",
		"/v1/runtime/messages/nack",
		"/v1/runtime/messages/{message_id}",
		"/v1/runtime/messages/ws",
		"/v1/runtime/messages/offline",
		"/v1/openclaw/messages/pull",
		"/v1/openclaw/messages/publish",
		"/v1/openclaw/messages/ack",
		"/v1/openclaw/messages/nack",
		"/v1/openclaw/messages/{message_id}",
		"/v1/openclaw/messages/ws",
		"/v1/openclaw/messages/offline",
		"/runtime/profile",
		"/runtime/capabilities",
		"/runtime/manifest",
	} {
		if strings.HasSuffix(trimmedPath, suffix) {
			parsed.Path = strings.TrimSuffix(trimmedPath, suffix)
			parsed.RawPath = ""
			return strings.TrimRight(parsed.String(), "/")
		}
	}

	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

func runtimeEndpointsFromSession(session Session) hub.RuntimeEndpoints {
	return hub.RuntimeEndpoints{
		ManifestURL:          strings.TrimSpace(session.ManifestURL),
		CapabilitiesURL:      strings.TrimSpace(session.Capabilities),
		MetadataURL:          strings.TrimSpace(session.MetadataURL),
		RuntimePullURL:       strings.TrimSpace(session.RuntimePullURL),
		RuntimePushURL:       strings.TrimSpace(session.RuntimePushURL),
		RuntimeAckURL:        strings.TrimSpace(session.RuntimeAckURL),
		RuntimeNackURL:       strings.TrimSpace(session.RuntimeNackURL),
		RuntimeStatusURL:     strings.TrimSpace(session.RuntimeStatusURL),
		RuntimeWebSocketURL:  strings.TrimSpace(session.RuntimeWebSocketURL),
		RuntimeOfflineURL:    strings.TrimSpace(session.RuntimeOfflineURL),
		OpenClawPullURL:      strings.TrimSpace(session.OpenClawPullURL),
		OpenClawPushURL:      strings.TrimSpace(session.OpenClawPushURL),
		OpenClawAckURL:       strings.TrimSpace(session.OpenClawAckURL),
		OpenClawNackURL:      strings.TrimSpace(session.OpenClawNackURL),
		OpenClawStatusURL:    strings.TrimSpace(session.OpenClawStatusURL),
		OpenClawWebSocketURL: strings.TrimSpace(session.OpenClawWebSocketURL),
		OpenClawOfflineURL:   strings.TrimSpace(session.OfflineURL),
	}
}

func sanitizeRuntimeEndpoints(endpoints hub.RuntimeEndpoints) hub.RuntimeEndpoints {
	return hub.RuntimeEndpoints{
		ManifestURL:          NormalizeHubEndpointURL(endpoints.ManifestURL),
		CapabilitiesURL:      NormalizeHubEndpointURL(endpoints.CapabilitiesURL),
		MetadataURL:          NormalizeHubEndpointURL(endpoints.MetadataURL),
		RuntimePullURL:       NormalizeHubEndpointURL(endpoints.RuntimePullURL),
		RuntimePushURL:       NormalizeHubEndpointURL(endpoints.RuntimePushURL),
		RuntimeAckURL:        NormalizeHubEndpointURL(endpoints.RuntimeAckURL),
		RuntimeNackURL:       NormalizeHubEndpointURL(endpoints.RuntimeNackURL),
		RuntimeStatusURL:     NormalizeHubEndpointURL(endpoints.RuntimeStatusURL),
		RuntimeWebSocketURL:  NormalizeHubEndpointURL(endpoints.RuntimeWebSocketURL),
		RuntimeOfflineURL:    NormalizeHubEndpointURL(endpoints.RuntimeOfflineURL),
		OpenClawPullURL:      NormalizeHubEndpointURL(endpoints.OpenClawPullURL),
		OpenClawPushURL:      NormalizeHubEndpointURL(endpoints.OpenClawPushURL),
		OpenClawAckURL:       NormalizeHubEndpointURL(endpoints.OpenClawAckURL),
		OpenClawNackURL:      NormalizeHubEndpointURL(endpoints.OpenClawNackURL),
		OpenClawStatusURL:    NormalizeHubEndpointURL(endpoints.OpenClawStatusURL),
		OpenClawWebSocketURL: NormalizeHubEndpointURL(endpoints.OpenClawWebSocketURL),
		OpenClawOfflineURL:   NormalizeHubEndpointURL(endpoints.OpenClawOfflineURL),
	}
}

func invalidRuntimeEndpoints(endpoints hub.RuntimeEndpoints) []string {
	type endpoint struct {
		name  string
		value string
	}
	fields := []endpoint{
		{name: "manifest", value: endpoints.ManifestURL},
		{name: "capabilities", value: endpoints.CapabilitiesURL},
		{name: "metadata", value: endpoints.MetadataURL},
		{name: "runtime_pull", value: endpoints.RuntimePullURL},
		{name: "runtime_push", value: endpoints.RuntimePushURL},
		{name: "runtime_ack", value: endpoints.RuntimeAckURL},
		{name: "runtime_nack", value: endpoints.RuntimeNackURL},
		{name: "runtime_status", value: endpoints.RuntimeStatusURL},
		{name: "runtime_websocket", value: endpoints.RuntimeWebSocketURL},
		{name: "runtime_offline", value: endpoints.RuntimeOfflineURL},
		{name: "openclaw_pull", value: endpoints.OpenClawPullURL},
		{name: "openclaw_push", value: endpoints.OpenClawPushURL},
		{name: "openclaw_ack", value: endpoints.OpenClawAckURL},
		{name: "openclaw_nack", value: endpoints.OpenClawNackURL},
		{name: "openclaw_status", value: endpoints.OpenClawStatusURL},
		{name: "openclaw_websocket", value: endpoints.OpenClawWebSocketURL},
		{name: "openclaw_offline", value: endpoints.OpenClawOfflineURL},
	}

	invalid := make([]string, 0, len(fields))
	for _, field := range fields {
		value := strings.TrimSpace(field.value)
		if value == "" {
			continue
		}
		if NormalizeHubEndpointURL(value) == "" {
			invalid = append(invalid, fmt.Sprintf("%s=%q", field.name, value))
		}
	}
	return invalid
}

func hubConnectionTarget(apiBase, fallback string) (string, string) {
	baseURL := NormalizeHubEndpointURL(apiBase)
	if baseURL == "" {
		baseURL = NormalizeHubEndpointURL(fallback)
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return "", ""
	}

	parsed, err := url.Parse(baseURL)
	if err != nil || strings.TrimSpace(parsed.Host) == "" {
		return baseURL, ""
	}
	return baseURL, strings.TrimSpace(parsed.Host)
}
