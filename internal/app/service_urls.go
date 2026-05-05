package app

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

func runtimeEndpointsFromBind(result hub.BindResponse) hub.RuntimeEndpoints {
	return runtimeEndpointsFromSession(Session{
		ManifestURL:     result.Endpoints.Manifest,
		Capabilities:    result.Endpoints.Capabilities,
		MetadataURL:     result.Endpoints.Metadata,
		OpenClawPullURL: result.Endpoints.OpenClawPull,
		OpenClawPushURL: result.Endpoints.OpenClawPush,
		OfflineURL:      result.Endpoints.Offline,
	})
}

func runtimeAPIBaseFromBind(result hub.BindResponse) string {
	return runtimeAPIBaseFromSession(Session{
		APIBase:         result.APIBase,
		ManifestURL:     result.Endpoints.Manifest,
		Capabilities:    result.Endpoints.Capabilities,
		MetadataURL:     result.Endpoints.Metadata,
		OpenClawPullURL: result.Endpoints.OpenClawPull,
		OpenClawPushURL: result.Endpoints.OpenClawPush,
		OfflineURL:      result.Endpoints.Offline,
	})
}

func runtimeAPIBaseFromSession(session Session) string {
	if apiBase := coalesceTrimmed(session.APIBase, session.BaseURL); apiBase != "" {
		return apiBase
	}
	for _, endpoint := range []string{
		session.MetadataURL,
		session.Capabilities,
		session.OpenClawPullURL,
		session.OpenClawPushURL,
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
		"/v1/openclaw/messages/pull",
		"/v1/openclaw/messages/publish",
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
		ManifestURL:        strings.TrimSpace(session.ManifestURL),
		CapabilitiesURL:    strings.TrimSpace(session.Capabilities),
		MetadataURL:        strings.TrimSpace(session.MetadataURL),
		OpenClawPullURL:    strings.TrimSpace(session.OpenClawPullURL),
		OpenClawPushURL:    strings.TrimSpace(session.OpenClawPushURL),
		OpenClawOfflineURL: strings.TrimSpace(session.OfflineURL),
	}
}

func sanitizeRuntimeEndpoints(endpoints hub.RuntimeEndpoints) hub.RuntimeEndpoints {
	return hub.RuntimeEndpoints{
		ManifestURL:        NormalizeHubEndpointURL(endpoints.ManifestURL),
		CapabilitiesURL:    NormalizeHubEndpointURL(endpoints.CapabilitiesURL),
		MetadataURL:        NormalizeHubEndpointURL(endpoints.MetadataURL),
		OpenClawPullURL:    NormalizeHubEndpointURL(endpoints.OpenClawPullURL),
		OpenClawPushURL:    NormalizeHubEndpointURL(endpoints.OpenClawPushURL),
		OpenClawOfflineURL: NormalizeHubEndpointURL(endpoints.OpenClawOfflineURL),
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
		{name: "openclaw_pull", value: endpoints.OpenClawPullURL},
		{name: "openclaw_push", value: endpoints.OpenClawPushURL},
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
