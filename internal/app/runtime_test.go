package app

import "testing"

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
			name:    "empty selection defaults to na",
			wantID:  HubRegionNA,
			wantURL: "https://na.hub.molten.bot",
		},
		{
			name:    "unknown runtime is rejected",
			hubURL:  "https://apac.hub.molten.bot",
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
