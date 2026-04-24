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

func NormalizeOnboardingMode(mode, bindToken, agentToken string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case OnboardingModeNew:
		return OnboardingModeNew
	case OnboardingModeExisting:
		return OnboardingModeExisting
	}
	if strings.TrimSpace(bindToken) != "" && strings.TrimSpace(agentToken) == "" {
		return OnboardingModeNew
	}
	return OnboardingModeExisting
}

func OnboardingModeFromToken(token string) string {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(strings.ToLower(token), "b_") {
		return OnboardingModeNew
	}
	return OnboardingModeExisting
}

func onboardingTokenHasModePrefix(token string) bool {
	token = strings.ToLower(strings.TrimSpace(token))
	return strings.HasPrefix(token, "b_") || strings.HasPrefix(token, "t_")
}

func NormalizeOnboardingTokens(mode, bindToken, agentToken string) (string, string, string) {
	bindToken = strings.TrimSpace(bindToken)
	agentToken = strings.TrimSpace(agentToken)
	mode = strings.ToLower(strings.TrimSpace(mode))

	submittedToken := bindToken
	if submittedToken == "" {
		submittedToken = agentToken
	}
	if submittedToken != "" && onboardingTokenHasModePrefix(submittedToken) {
		resolvedMode := OnboardingModeFromToken(submittedToken)
		if resolvedMode == OnboardingModeNew {
			return OnboardingModeNew, submittedToken, ""
		}
		return OnboardingModeExisting, "", submittedToken
	}
	switch mode {
	case OnboardingModeNew:
		if submittedToken != "" {
			return OnboardingModeNew, submittedToken, ""
		}
		return OnboardingModeNew, bindToken, ""
	case OnboardingModeExisting:
		if submittedToken != "" {
			return OnboardingModeExisting, "", submittedToken
		}
		return OnboardingModeExisting, "", agentToken
	}

	if submittedToken != "" {
		resolvedMode := OnboardingModeFromToken(submittedToken)
		if resolvedMode == OnboardingModeNew {
			return resolvedMode, submittedToken, ""
		}
		return resolvedMode, "", submittedToken
	}

	resolvedMode := NormalizeOnboardingMode(mode, bindToken, agentToken)
	if resolvedMode == OnboardingModeNew {
		return resolvedMode, bindToken, ""
	}
	return resolvedMode, "", agentToken
}

func DefaultOnboardingSteps() []OnboardingStep {
	return DefaultOnboardingStepsForMode(OnboardingModeNew)
}

func DefaultOnboardingStepsForMode(mode string) []OnboardingStep {
	steps := []OnboardingStep{
		{
			ID:     OnboardingStepBind,
			Label:  "Redeem Token",
			Status: "pending",
			Detail: "Create this dispatcher's agent credential.",
		},
		{
			ID:     OnboardingStepWorkBind,
			Label:  "Verify",
			Status: "pending",
			Detail: "Check the Molten Hub credential.",
		},
		{
			ID:     OnboardingStepProfileSet,
			Label:  "Register",
			Status: "pending",
			Detail: "Register this runtime with Molten Hub.",
		},
		{
			ID:     OnboardingStepWorkActivate,
			Label:  "Activate",
			Status: "pending",
			Detail: "Apply the region settings.",
		},
	}
	if strings.EqualFold(strings.TrimSpace(mode), OnboardingModeExisting) {
		steps[0].Label = "Agent Token"
		steps[0].Detail = "Check the agent token."
	}
	return steps
}
