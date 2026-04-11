package app

import (
	"errors"
	"testing"
)

func TestDefaultOnboardingSteps(t *testing.T) {
	t.Parallel()

	steps := DefaultOnboardingSteps()
	if len(steps) != 3 {
		t.Fatalf("expected 3 onboarding steps, got %d", len(steps))
	}
	if steps[0].ID != OnboardingStepBind || steps[1].ID != OnboardingStepProfileSet || steps[2].ID != OnboardingStepWorkActivate {
		t.Fatalf("unexpected step order: %#v", steps)
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
