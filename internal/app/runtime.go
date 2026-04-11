package app

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	HubRegionNA   = "na"
	HubRegionEU   = "eu"
	hubBaseDomain = "hub.molten.bot"
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
		HubURL:      hubURLForRegion(HubRegionNA),
	},
	{
		ID:          HubRegionEU,
		Label:       "EU",
		Description: "Europe",
		HubURL:      hubURLForRegion(HubRegionEU),
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
	normalized := NormalizeHubEndpointURL(raw)
	if normalized == "" {
		return ""
	}

	parsed, err := url.Parse(normalized)
	if err != nil || parsed.Host == "" {
		return ""
	}
	runtime, ok := runtimeFromHost(parsed.Hostname())
	if !ok {
		return ""
	}
	return runtime.HubURL
}

func NormalizeHubEndpointURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return ""
	}
	if parsed.User != nil || strings.TrimSpace(parsed.Port()) != "" {
		return ""
	}

	host := strings.TrimSpace(strings.ToLower(parsed.Hostname()))
	if !isAllowedHubHost(host) {
		return ""
	}

	parsed.Scheme = "https"
	parsed.Host = host
	parsed.User = nil
	return strings.TrimRight(parsed.String(), "/")
}

func isAllowedHubHost(host string) bool {
	_, ok := runtimeFromHost(host)
	return ok
}

func runtimeFromHost(host string) (HubRuntime, bool) {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return HubRuntime{}, false
	}

	for _, runtime := range supportedHubRuntimes {
		rootHost := hubHostForRegion(runtime.ID)
		if host == rootHost || strings.HasSuffix(host, "."+rootHost) {
			return runtime, true
		}
	}
	return HubRuntime{}, false
}

func hubHostForRegion(region string) string {
	region = strings.TrimSpace(strings.ToLower(region))
	if region == "" {
		return ""
	}
	return region + "." + hubBaseDomain
}

func hubURLForRegion(region string) string {
	host := hubHostForRegion(region)
	if host == "" {
		return ""
	}
	return "https://" + host
}
