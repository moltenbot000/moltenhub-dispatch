package app

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
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

func TestResolveHubRuntime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		region  string
		hubURL  string
		wantID  string
		wantURL string
		wantErr bool
	}{
		{
			name:    "region wins when selected",
			region:  HubRegionEU,
			hubURL:  "https://na.hub.molten.bot",
			wantID:  HubRegionEU,
			wantURL: "https://eu.hub.molten.bot",
		},
		{
			name:    "known hub url maps to region",
			hubURL:  "https://na.hub.molten.bot/",
			wantID:  HubRegionNA,
			wantURL: "https://na.hub.molten.bot",
		},
		{
			name:    "canonical api base maps to runtime",
			hubURL:  "https://eu.hub.molten.bot/v1",
			wantID:  HubRegionEU,
			wantURL: "https://eu.hub.molten.bot",
		},
		{
			name:    "runtime subdomain maps to runtime region",
			hubURL:  "https://runtime.na.hub.molten.bot/v1/runtime/messages/pull",
			wantID:  HubRegionNA,
			wantURL: "https://na.hub.molten.bot",
		},
		{
			name:    "empty selection defaults to na",
			wantID:  HubRegionNA,
			wantURL: "https://na.hub.molten.bot",
		},
		{
			name:    "unknown runtime is rejected",
			hubURL:  "https://apac.hub.molten.bot",
			wantErr: true,
		},
		{
			name:    "http runtime is rejected",
			hubURL:  "http://na.hub.molten.bot",
			wantErr: true,
		},
		{
			name:    "runtime ports are rejected",
			hubURL:  "https://na.hub.molten.bot:8443",
			wantErr: true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			runtime, err := ResolveHubRuntime(test.region, test.hubURL)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve runtime: %v", err)
			}
			if runtime.ID != test.wantID {
				t.Fatalf("runtime id = %q, want %q", runtime.ID, test.wantID)
			}
			if runtime.HubURL != test.wantURL {
				t.Fatalf("runtime hub url = %q, want %q", runtime.HubURL, test.wantURL)
			}
		})
	}
}

func TestSupportedAndDefaultHubRuntimeBranches(t *testing.T) {
	runtimes := SupportedHubRuntimes()
	if len(runtimes) < 2 {
		t.Fatalf("SupportedHubRuntimes count = %d, want at least 2", len(runtimes))
	}
	runtimes[0].ID = "mutated"
	if SupportedHubRuntimes()[0].ID == "mutated" {
		t.Fatal("SupportedHubRuntimes returned mutable backing slice")
	}
	if got := DefaultHubRuntime(); got.ID != HubRegionNA || got.HubURL == "" {
		t.Fatalf("DefaultHubRuntime = %#v", got)
	}
}

func TestNormalizeHubEndpointURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "normalize runtime endpoint",
			in:   "https://runtime.na.hub.molten.bot/v1/runtime/messages/pull",
			want: "https://runtime.na.hub.molten.bot/v1/runtime/messages/pull",
		},
		{
			name: "normalize canonical runtime root",
			in:   "https://eu.hub.molten.bot/",
			want: "https://eu.hub.molten.bot",
		},
		{
			name: "reject localhost",
			in:   "http://127.0.0.1:37581/v1",
			want: "",
		},
		{
			name: "reject unknown domain",
			in:   "https://example.com/v1",
			want: "",
		},
		{
			name: "reject explicit ports",
			in:   "https://na.hub.molten.bot:443/v1",
			want: "",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeHubEndpointURL(test.in); got != test.want {
				t.Fatalf("NormalizeHubEndpointURL(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestRuntimeURLHelpersCoverUnknownBranches(t *testing.T) {
	if got := normalizeHubRuntimeURL("https://runtime.eu.hub.molten.bot/v1/runtime/messages/ws"); got != "https://eu.hub.molten.bot" {
		t.Fatalf("normalizeHubRuntimeURL runtime endpoint = %q", got)
	}
	if got, ok := runtimeFromHost("runtime.eu.hub.molten.bot"); !ok || got.ID != HubRegionEU {
		t.Fatalf("runtimeFromHost runtime host = %#v", got)
	}
	if got := hubHostForRegion("unknown"); got != "unknown.hub.molten.bot" {
		t.Fatalf("hubHostForRegion unknown = %q, want synthesized hub host", got)
	}
	if got := hubURLForRegion("unknown"); got != "https://unknown.hub.molten.bot" {
		t.Fatalf("hubURLForRegion unknown = %q, want synthesized hub URL", got)
	}
}

func TestRuntimeAPIBaseFromEndpointSupportsMessageStatusRoutes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "runtime status route",
			in:   "https://runtime.na.hub.molten.bot/v1/runtime/messages/{message_id}",
			want: "https://runtime.na.hub.molten.bot",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := runtimeAPIBaseFromEndpoint(test.in); got != test.want {
				t.Fatalf("runtimeAPIBaseFromEndpoint(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestFetchHubRuntimeCatalog(t *testing.T) {
	server := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hubs.json" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"display":"North America","key":"na","domain":"na.hub.molten.bot"},
			{"display":"Europe","key":"eu","domain":"eu.hub.molten.bot"},
			{"display":"Invalid","key":"bad","domain":"example.com"},
			{"display":"Duplicate","key":"eu","domain":"eu.hub.molten.bot"}
		]`))
	}))

	runtimes, err := fetchHubRuntimeCatalog(server.URL+"/hubs.json", server.Client())
	if err != nil {
		t.Fatalf("fetch hub runtime catalog: %v", err)
	}
	if len(runtimes) != 2 {
		t.Fatalf("runtime count = %d, want 2", len(runtimes))
	}
	if runtimes[0].ID != HubRegionNA || runtimes[0].Label != "NA" || runtimes[0].Description != "North America" || runtimes[0].HubURL != "https://na.hub.molten.bot" {
		t.Fatalf("unexpected first runtime: %#v", runtimes[0])
	}
	if runtimes[1].ID != HubRegionEU || runtimes[1].HubURL != "https://eu.hub.molten.bot" {
		t.Fatalf("unexpected second runtime: %#v", runtimes[1])
	}
}

func TestCatalogHubURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		domain string
		want   string
	}{
		{
			name:   "accepts hub domain",
			domain: "na.hub.molten.bot",
			want:   "https://na.hub.molten.bot",
		},
		{
			name:   "rejects external domain",
			domain: "example.com",
			want:   "",
		},
		{
			name:   "rejects empty domain",
			domain: " ",
			want:   "",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := catalogHubURL(test.domain); got != test.want {
				t.Fatalf("catalogHubURL(%q) = %q, want %q", test.domain, got, test.want)
			}
		})
	}
}
