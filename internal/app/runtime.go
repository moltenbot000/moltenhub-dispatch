package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	HubRegionNA    = "na"
	HubRegionEU    = "eu"
	HubRegionLocal = "local"
	hubBaseDomain  = "hub.molten.bot"
	hubCatalogURL  = "https://molten.bot/hubs.json"
)

type HubRuntime struct {
	ID          string
	Label       string
	Description string
	HubURL      string
}

var fallbackHubRuntimes = []HubRuntime{
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

var (
	hubRuntimeCatalogClient = &http.Client{Timeout: 2 * time.Second}

	hubRuntimeCatalogMu     sync.RWMutex
	hubRuntimeCatalogLoaded bool
	hubRuntimeCatalog       = cloneHubRuntimes(fallbackHubRuntimes)
)

func SupportedHubRuntimes() []HubRuntime {
	return cloneHubRuntimes(currentHubRuntimes())
}

func DefaultHubRuntime() HubRuntime {
	runtimes := currentHubRuntimes()
	if len(runtimes) == 0 {
		return HubRuntime{}
	}
	return runtimes[0]
}

func ResolveHubRuntime(region, hubURL string) (HubRuntime, error) {
	if strings.EqualFold(strings.TrimSpace(region), HubRegionLocal) {
		runtimeURL := strings.TrimSpace(hubURL)
		if runtimeURL == "" {
			runtimeURL = defaultLocalHubURL
		}
		runtimeURL = normalizeLocalHubRuntimeURL(runtimeURL)
		if runtimeURL == "" {
			return HubRuntime{}, fmt.Errorf("unsupported local hub runtime URL %q", strings.TrimSpace(hubURL))
		}
		return HubRuntime{
			ID:          HubRegionLocal,
			Label:       "Local",
			Description: "Local Hub",
			HubURL:      runtimeURL,
		}, nil
	}
	if localHubModeFromEnv() {
		if runtimeURL := normalizeLocalHubRuntimeURL(hubURL); runtimeURL != "" {
			return HubRuntime{
				ID:          HubRegionLocal,
				Label:       "Local",
				Description: "Local Hub",
				HubURL:      runtimeURL,
			}, nil
		}
	}
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
	for _, runtime := range currentHubRuntimes() {
		if runtime.ID == region {
			return runtime, true
		}
	}
	return HubRuntime{}, false
}

func hubRuntimeByURL(hubURL string) (HubRuntime, bool) {
	hubURL = normalizeHubRuntimeURL(hubURL)
	for _, runtime := range currentHubRuntimes() {
		if normalizeHubRuntimeURL(runtime.HubURL) == hubURL {
			return runtime, true
		}
	}
	return HubRuntime{}, false
}

func normalizeHubRuntimeURL(raw string) string {
	if localHubModeFromEnv() {
		if normalized := normalizeLocalHubRuntimeURL(raw); normalized != "" {
			return normalized
		}
	}
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

func NormalizeLocalHubEndpointURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(parsed.Scheme)) {
	case "http", "https":
	default:
		return ""
	}
	if parsed.User != nil {
		return ""
	}
	parsed.Scheme = strings.ToLower(strings.TrimSpace(parsed.Scheme))
	parsed.User = nil
	return strings.TrimRight(parsed.String(), "/")
}

func normalizeConfiguredHubEndpointURL(raw string) string {
	if localHubModeFromEnv() {
		return NormalizeLocalHubEndpointURL(raw)
	}
	return NormalizeHubEndpointURL(raw)
}

func normalizeLocalHubRuntimeURL(raw string) string {
	normalized := NormalizeLocalHubEndpointURL(raw)
	if normalized == "" {
		return ""
	}
	parsed, err := url.Parse(normalized)
	if err != nil || parsed.Host == "" {
		return ""
	}
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
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

	for _, runtime := range currentHubRuntimes() {
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

func currentHubRuntimes() []HubRuntime {
	hubRuntimeCatalogMu.RLock()
	if hubRuntimeCatalogLoaded {
		runtimes := cloneHubRuntimes(hubRuntimeCatalog)
		hubRuntimeCatalogMu.RUnlock()
		return runtimes
	}
	hubRuntimeCatalogMu.RUnlock()

	hubRuntimeCatalogMu.Lock()
	defer hubRuntimeCatalogMu.Unlock()

	if !hubRuntimeCatalogLoaded {
		runtimes, err := fetchHubRuntimeCatalog(hubCatalogURL, hubRuntimeCatalogClient)
		if err == nil && len(runtimes) > 0 {
			hubRuntimeCatalog = runtimes
		} else {
			hubRuntimeCatalog = cloneHubRuntimes(fallbackHubRuntimes)
		}
		hubRuntimeCatalogLoaded = true
	}

	return cloneHubRuntimes(hubRuntimeCatalog)
}

func fetchHubRuntimeCatalog(rawURL string, client *http.Client) ([]HubRuntime, error) {
	if client == nil {
		client = hubRuntimeCatalogClient
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimSpace(rawURL), nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hub catalog returned %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var payload []struct {
		Display string `json:"display"`
		Key     string `json:"key"`
		Domain  string `json:"domain"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	runtimes := make([]HubRuntime, 0, len(payload))
	seen := make(map[string]struct{}, len(payload))
	for _, item := range payload {
		hubURL := catalogHubURL(item.Domain)
		runtime := HubRuntime{
			ID:          strings.TrimSpace(strings.ToLower(item.Key)),
			Label:       strings.ToUpper(strings.TrimSpace(item.Key)),
			Description: strings.TrimSpace(item.Display),
			HubURL:      hubURL,
		}
		if runtime.ID == "" || runtime.Label == "" || runtime.Description == "" || runtime.HubURL == "" {
			continue
		}
		if _, ok := seen[runtime.ID]; ok {
			continue
		}
		seen[runtime.ID] = struct{}{}
		runtimes = append(runtimes, runtime)
	}
	if len(runtimes) == 0 {
		return nil, fmt.Errorf("hub catalog %q did not contain any supported runtimes", rawURL)
	}
	return runtimes, nil
}

func cloneHubRuntimes(runtimes []HubRuntime) []HubRuntime {
	cloned := make([]HubRuntime, len(runtimes))
	copy(cloned, runtimes)
	return cloned
}

func catalogHubURL(domain string) string {
	domain = strings.TrimSpace(strings.ToLower(domain))
	if domain == "" {
		return ""
	}

	rawURL := "https://" + domain
	parsed, err := url.Parse(rawURL)
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
	if host != domain || !strings.HasSuffix(host, "."+hubBaseDomain) {
		return ""
	}
	parsed.Scheme = "https"
	parsed.Host = host
	parsed.User = nil
	return strings.TrimRight(parsed.String(), "/")
}
