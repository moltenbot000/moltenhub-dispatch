package app

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	HubRegionNA = "na"
	HubRegionEU = "eu"
)

type HubRuntime struct {
	ID          string
	Label       string
	Description string
	HubURL      string
}

var supportedHubRuntimes = []HubRuntime{
	{
		ID:          HubRegionNA,
		Label:       "NA",
		Description: "North America",
		HubURL:      "https://na.hub.molten.bot",
	},
	{
		ID:          HubRegionEU,
		Label:       "EU",
		Description: "Europe",
		HubURL:      "https://eu.hub.molten.bot",
	},
}

func SupportedHubRuntimes() []HubRuntime {
	runtimes := make([]HubRuntime, len(supportedHubRuntimes))
	copy(runtimes, supportedHubRuntimes)
	return runtimes
}

func DefaultHubRuntime() HubRuntime {
	return supportedHubRuntimes[0]
}

func ResolveHubRuntime(region, hubURL string) (HubRuntime, error) {
	if runtime, ok := hubRuntimeByID(region); ok {
		return runtime, nil
	}
	if runtime, ok := hubRuntimeByURL(hubURL); ok {
		return runtime, nil
	}
	if strings.TrimSpace(region) == "" && strings.TrimSpace(hubURL) == "" {
		return DefaultHubRuntime(), nil
	}
	return HubRuntime{}, fmt.Errorf("unsupported hub runtime selection %q (%q)", strings.TrimSpace(region), strings.TrimSpace(hubURL))
}

func hubRuntimeByID(region string) (HubRuntime, bool) {
	region = strings.TrimSpace(strings.ToLower(region))
	for _, runtime := range supportedHubRuntimes {
		if runtime.ID == region {
			return runtime, true
		}
	}
	return HubRuntime{}, false
}

func hubRuntimeByURL(hubURL string) (HubRuntime, bool) {
	hubURL = normalizeHubRuntimeURL(hubURL)
	for _, runtime := range supportedHubRuntimes {
		if normalizeHubRuntimeURL(runtime.HubURL) == hubURL {
			return runtime, true
		}
	}
	return HubRuntime{}, false
}

func normalizeHubRuntimeURL(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return strings.TrimRight(raw, "/")
	}

	return strings.TrimRight(parsed.Scheme+"://"+parsed.Host, "/")
}
