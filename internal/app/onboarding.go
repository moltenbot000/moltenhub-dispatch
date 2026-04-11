package app

import (
	"errors"
	"strings"
)

const (
	OnboardingStepBind         = "bind"
	OnboardingStepWorkBind     = "work_bind"
	OnboardingStepProfileSet   = "profile_set"
	OnboardingStepWorkActivate = "work_activate"

	OnboardingModeNew      = "new"
	OnboardingModeExisting = "existing"
)

type OnboardingStep struct {
	ID     string
	Label  string
	Status string
	Detail string
}

type OnboardingError struct {
	Stage string
	Err   error
}

func (e *OnboardingError) Error() string {
	if e == nil || e.Err == nil {
		return "onboarding failed"
	}
	return e.Err.Error()
}

func (e *OnboardingError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func WrapOnboardingError(stage string, err error) error {
	if err == nil {
		return nil
	}
	if stage == "" {
		stage = OnboardingStepBind
	}
	return &OnboardingError{Stage: stage, Err: err}
}

func OnboardingStageFromError(err error) string {
	if err == nil {
		return ""
	}
	var onboardingErr *OnboardingError
	if errors.As(err, &onboardingErr) {
		stage := strings.TrimSpace(onboardingErr.Stage)
		if stage != "" {
			return stage
		}
	}
	return OnboardingStepBind
}

func DefaultOnboardingSteps() []OnboardingStep {
	return DefaultOnboardingStepsForMode(OnboardingModeNew)
}

func DefaultOnboardingStepsForMode(mode string) []OnboardingStep {
	steps := []OnboardingStep{
		{
			ID:     OnboardingStepBind,
			Label:  "Bind",
			Status: "pending",
			Detail: "Exchange the bind token for an agent credential.",
		},
		{
			ID:     OnboardingStepWorkBind,
			Label:  "Work",
			Status: "pending",
			Detail: "Resolve and verify Molten Hub credentials.",
		},
		{
			ID:     OnboardingStepProfileSet,
			Label:  "Profile Set",
			Status: "pending",
			Detail: "Persist the agent profile in Molten Hub.",
		},
		{
			ID:     OnboardingStepWorkActivate,
			Label:  "Work",
			Status: "pending",
			Detail: "Apply the runtime transport and confirm activation.",
		},
	}
	if strings.EqualFold(strings.TrimSpace(mode), OnboardingModeExisting) {
		steps[0].Detail = "Verify the existing Molten Hub agent credential."
	}
	return steps
}
