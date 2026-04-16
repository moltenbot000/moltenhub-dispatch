package app

import (
	"errors"
	"testing"
)

func TestDefaultOnboardingSteps(t *testing.T) {
	t.Parallel()

	steps := DefaultOnboardingSteps()
	if len(steps) != 4 {
		t.Fatalf("expected 4 onboarding steps, got %d", len(steps))
	}
	if steps[0].ID != OnboardingStepBind ||
		steps[1].ID != OnboardingStepWorkBind ||
		steps[2].ID != OnboardingStepProfileSet ||
		steps[3].ID != OnboardingStepWorkActivate {
		t.Fatalf("unexpected step order: %#v", steps)
	}
	if got, want := steps[0].Detail, "Exchange the bind token for an agent credential."; got != want {
		t.Fatalf("bind detail = %q, want %q", got, want)
	}
	if got, want := steps[2].Detail, "Persist the agent profile in Molten Hub."; got != want {
		t.Fatalf("profile detail = %q, want %q", got, want)
	}
}

func TestDefaultOnboardingStepsForModeExisting(t *testing.T) {
	t.Parallel()

	steps := DefaultOnboardingStepsForMode(OnboardingModeExisting)
	if len(steps) != 4 {
		t.Fatalf("expected 4 onboarding steps, got %d", len(steps))
	}
	if got, want := steps[0].Detail, "Verify the existing Molten Hub agent credential."; got != want {
		t.Fatalf("bind detail = %q, want %q", got, want)
	}
	if got, want := steps[2].Detail, "Persist the agent profile in Molten Hub."; got != want {
		t.Fatalf("profile detail = %q, want %q", got, want)
	}
}

func TestOnboardingStageFromError(t *testing.T) {
	t.Parallel()

	err := WrapOnboardingError(OnboardingStepProfileSet, errors.New("boom"))
	if got := OnboardingStageFromError(err); got != OnboardingStepProfileSet {
		t.Fatalf("stage = %q, want %q", got, OnboardingStepProfileSet)
	}
	if got := OnboardingStageFromError(errors.New("generic")); got != OnboardingStepBind {
		t.Fatalf("stage = %q, want %q", got, OnboardingStepBind)
	}
	if got := OnboardingStageFromError(nil); got != "" {
		t.Fatalf("stage = %q, want empty", got)
	}
}

func TestNormalizeOnboardingMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mode       string
		bindToken  string
		agentToken string
		want       string
	}{
		{
			name: "explicit new mode wins",
			mode: "new",
			want: OnboardingModeNew,
		},
		{
			name: "explicit existing mode wins",
			mode: "existing",
			want: OnboardingModeExisting,
		},
		{
			name:      "bind token without agent token infers new",
			bindToken: "bind-123",
			want:      OnboardingModeNew,
		},
		{
			name:       "agent token defaults to existing",
			agentToken: "agent-123",
			want:       OnboardingModeExisting,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeOnboardingMode(test.mode, test.bindToken, test.agentToken); got != test.want {
				t.Fatalf("NormalizeOnboardingMode(%q, %q, %q) = %q, want %q", test.mode, test.bindToken, test.agentToken, got, test.want)
			}
		})
	}
}

func TestOnboardingModeFromToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		token string
		want  string
	}{
		{
			name:  "bind prefix infers new mode",
			token: "b_token",
			want:  OnboardingModeNew,
		},
		{
			name:  "bind prefix is case insensitive",
			token: "B_token",
			want:  OnboardingModeNew,
		},
		{
			name:  "target prefix infers existing mode",
			token: "t_token",
			want:  OnboardingModeExisting,
		},
		{
			name:  "legacy token defaults to existing mode",
			token: "legacy-token",
			want:  OnboardingModeExisting,
		},
		{
			name:  "empty token defaults to existing mode",
			token: "",
			want:  OnboardingModeExisting,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := OnboardingModeFromToken(test.token); got != test.want {
				t.Fatalf("OnboardingModeFromToken(%q) = %q, want %q", test.token, got, test.want)
			}
		})
	}
}

func TestNormalizeOnboardingTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		mode           string
		bindToken      string
		agentToken     string
		wantMode       string
		wantBindToken  string
		wantAgentToken string
	}{
		{
			name:           "bind token prefix b_ routes to new flow",
			bindToken:      "b_123",
			wantMode:       OnboardingModeNew,
			wantBindToken:  "b_123",
			wantAgentToken: "",
		},
		{
			name:           "explicit new mode keeps non-prefixed token in bind flow",
			mode:           "new",
			bindToken:      "agent-legacy",
			wantMode:       OnboardingModeNew,
			wantBindToken:  "agent-legacy",
			wantAgentToken: "",
		},
		{
			name:           "bind token prefix t_ routes to existing flow",
			bindToken:      "t_123",
			wantMode:       OnboardingModeExisting,
			wantBindToken:  "",
			wantAgentToken: "t_123",
		},
		{
			name:           "explicit existing mode keeps prefixed token in existing flow",
			mode:           "existing",
			bindToken:      "b_123",
			wantMode:       OnboardingModeExisting,
			wantBindToken:  "",
			wantAgentToken: "b_123",
		},
		{
			name:           "legacy bind token routes to existing flow",
			bindToken:      "legacy-token",
			wantMode:       OnboardingModeExisting,
			wantBindToken:  "",
			wantAgentToken: "legacy-token",
		},
		{
			name:           "agent token can drive bind-new flow",
			agentToken:     "b_123",
			wantMode:       OnboardingModeNew,
			wantBindToken:  "b_123",
			wantAgentToken: "",
		},
		{
			name:           "empty tokens fallback to explicit mode",
			mode:           "new",
			wantMode:       OnboardingModeNew,
			wantBindToken:  "",
			wantAgentToken: "",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			gotMode, gotBindToken, gotAgentToken := NormalizeOnboardingTokens(test.mode, test.bindToken, test.agentToken)
			if gotMode != test.wantMode || gotBindToken != test.wantBindToken || gotAgentToken != test.wantAgentToken {
				t.Fatalf(
					"NormalizeOnboardingTokens(%q, %q, %q) = (%q, %q, %q), want (%q, %q, %q)",
					test.mode,
					test.bindToken,
					test.agentToken,
					gotMode,
					gotBindToken,
					gotAgentToken,
					test.wantMode,
					test.wantBindToken,
					test.wantAgentToken,
				)
			}
		})
	}
}
