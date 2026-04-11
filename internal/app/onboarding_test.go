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
