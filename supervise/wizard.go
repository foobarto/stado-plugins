package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var errWizardCancelled = errors.New("supervise setup cancelled")

type uiChoiceInput struct {
	Default   string             `json:"default"`
	Validator *uiChoiceValidator `json:"validator,omitempty"`
}

type uiChoiceValidator struct {
	Kind string `json:"kind"`
	Spec string `json:"spec,omitempty"`
}

type uiChoiceOption struct {
	ID     string         `json:"id"`
	Label  string         `json:"label"`
	Prefix string         `json:"prefix,omitempty"`
	Input  *uiChoiceInput `json:"input,omitempty"`
}

type uiChoiceRequest struct {
	Prompt  string           `json:"prompt"`
	Options []uiChoiceOption `json:"options"`
	Multi   bool             `json:"multi"`
	Default []string         `json:"default"`
}

type uiChoiceResponse struct {
	Selected   []string `json:"selected"`
	InputValue string   `json:"input_value,omitempty"`
	Cancelled  bool     `json:"cancelled"`
}

type setupChoice func(uiChoiceRequest) (uiChoiceResponse, error)

func runSetupWizard(seed string, choose setupChoice) (setupRequest, error) {
	if choose == nil {
		return setupRequest{}, errors.New("interactive setup UI is unavailable")
	}
	request := defaultSetupRequest(seed)
	objective, err := wizardInput(choose, "What should the supervised worker accomplish?", "objective", "Objective", request.Objective, "length", "1,4096")
	if err != nil {
		return setupRequest{}, err
	}
	request.Objective = strings.TrimSpace(objective)
	selected, err := wizardOne(choose, "Choose watchdog cadence.", []uiChoiceOption{
		{ID: "event", Label: "Event-driven (recommended)"}, {ID: "live", Label: "Every worker turn"},
	}, "event")
	if err != nil {
		return setupRequest{}, err
	}
	request.Config.Mode = mode(selected)
	selected, err = wizardOne(choose, "Who may approve plan-level pivots? Objective, criteria, and constraint changes always return to you.", []uiChoiceOption{
		{ID: "user", Label: "Ask me (recommended)"}, {ID: "watchdog", Label: "Watchdog may approve plan pivots"},
	}, "user")
	if err != nil {
		return setupRequest{}, err
	}
	request.Config.PivotApproval = selected
	selected, err = wizardOne(choose, "Choose the supervision assurance profile.", []uiChoiceOption{
		{ID: "standard", Label: "Standard (recommended)"}, {ID: "high_assurance", Label: "High assurance"}, {ID: "custom", Label: "Custom"},
	}, "standard")
	if err != nil {
		return setupRequest{}, err
	}
	request.Config.AssuranceProfile = selected
	if selected == "high_assurance" {
		request.Config.VerifierProfile = "required"
	}
	selected, err = wizardOne(choose, "If an operator /loop already exists, may this confirmed supervise run replace it?", []uiChoiceOption{
		{ID: "replace_operator_loop", Label: "Replace operator loop"}, {ID: "reject", Label: "Fail on conflict"},
	}, "replace_operator_loop")
	if err != nil {
		return setupRequest{}, err
	}
	request.WorkerConflict = selected
	advancedDefault := "defaults"
	if request.Config.AssuranceProfile == "custom" {
		advancedDefault = "customize"
	}
	selected, err = wizardOne(choose, "Use profile defaults or review advanced setup?", []uiChoiceOption{
		{ID: "defaults", Label: "Use profile defaults"}, {ID: "customize", Label: "Review advanced settings"},
	}, advancedDefault)
	if err != nil {
		return setupRequest{}, err
	}
	if selected == "customize" {
		if err := runAdvancedSetupWizard(&request, choose); err != nil {
			return setupRequest{}, err
		}
	}
	if err := request.validate(); err != nil {
		return setupRequest{}, err
	}
	return request, nil
}

func runAdvancedSetupWizard(request *setupRequest, choose setupChoice) error {
	var err error
	request.Config.ReviewEveryTurns, err = wizardInt(choose, "Also review every N worker turns (0 disables periodic cadence).", "review_every_turns", request.Config.ReviewEveryTurns, 0, 10_000)
	if err != nil {
		return err
	}
	if request.Config.Mode == modeLive {
		selected, chooseErr := wizardOne(choose, "Should live mode hold the next worker turn until its review finishes?", []uiChoiceOption{
			{ID: "off", Label: "No strict barrier"}, {ID: "on", Label: "Hold every turn for review"},
		}, map[bool]string{true: "on", false: "off"}[request.Config.StrictLiveBarrier])
		if chooseErr != nil {
			return chooseErr
		}
		request.Config.StrictLiveBarrier = selected == "on"
	}
	paths, err := wizardInput(choose, "Allowed repository path prefixes, one per line (blank means repository-wide quality review).", "paths", "Path prefixes", strings.Join(request.Config.AllowedPathPrefixes, "\n"), "multiline", "")
	if err != nil {
		return err
	}
	request.Config.AllowedPathPrefixes, err = wizardLines(paths, 64)
	if err != nil {
		return err
	}
	request.Config.WatchdogTokenBudget, err = wizardInt(choose, "Watchdog token cap per fresh review.", "watchdog_tokens", request.Config.WatchdogTokenBudget, 1, 1_000_000)
	if err != nil {
		return err
	}
	request.Config.VerifierTokenBudget, err = wizardInt(choose, "Independent final-verifier token cap.", "verifier_tokens", request.Config.VerifierTokenBudget, 1, 1_000_000)
	if err != nil {
		return err
	}
	if err := runSpawnProfileWizard(choose, "watchdog", &request.Config.WatchdogProvider, &request.Config.WatchdogModel, &request.Config.WatchdogThinking, &request.Config.WatchdogThinkingBudgetTokens, &request.Config.WatchdogReasoningEffort, request.Config.WatchdogTokenBudget); err != nil {
		return err
	}
	if err := runSpawnProfileWizard(choose, "verifier", &request.Config.VerifierProvider, &request.Config.VerifierModel, &request.Config.VerifierThinking, &request.Config.VerifierThinkingBudgetTokens, &request.Config.VerifierReasoningEffort, request.Config.VerifierTokenBudget); err != nil {
		return err
	}
	request.Config.EventReviewRetries, err = wizardInt(choose, "Fresh attempts per event review before applying failure posture.", "event_review_retries", request.Config.EventReviewRetries, 1, 10)
	if err != nil {
		return err
	}
	request.Config.FailedEventLimit, err = wizardInt(choose, "Consecutive exhausted event triggers before pausing.", "failed_event_limit", request.Config.FailedEventLimit, 1, 100)
	if err != nil {
		return err
	}
	request.Config.CorrectionLimit, err = wizardInt(choose, "Consecutive correction follow-ups that may still need another correction.", "correction_limit", request.Config.CorrectionLimit, 1, 20)
	if err != nil {
		return err
	}
	request.Config.LiveRetryBaseMillis, err = wizardInt(choose, "Live-review retry base delay in milliseconds.", "live_retry_base_ms", request.Config.LiveRetryBaseMillis, 1, 300_000)
	if err != nil {
		return err
	}
	request.Config.LiveRetryMaxMillis, err = wizardInt(choose, "Live-review retry maximum delay in milliseconds.", "live_retry_max_ms", request.Config.LiveRetryMaxMillis, request.Config.LiveRetryBaseMillis, 300_000)
	if err != nil {
		return err
	}
	if request.Config.AssuranceProfile != "high_assurance" {
		selected, chooseErr := wizardOne(choose, "If the independent completion verifier is unavailable, should completion fail closed?", []uiChoiceOption{
			{ID: "required", Label: "Required (recommended)"}, {ID: "advisory", Label: "Advisory; invalidate candidate and resume"},
		}, normalizedVerifierProfile(request.Config.VerifierProfile))
		if chooseErr != nil {
			return chooseErr
		}
		request.Config.VerifierProfile = selected
	}
	request.Config.WatchdogTimeoutSecond, err = wizardInt(choose, "Maximum seconds for each fresh reviewer or verifier.", "review_timeout", request.Config.WatchdogTimeoutSecond, 1, 3600)
	if err != nil {
		return err
	}
	request.Config.HoldTTLSeconds, err = wizardInt(choose, "Scheduling-hold lease TTL in seconds (renewed halfway through).", "hold_ttl", request.Config.HoldTTLSeconds, 30, 3600)
	if err != nil {
		return err
	}
	acceptance, err := wizardInput(choose, "Optional acceptance-criteria hints, one per line.", "acceptance_hints", "Acceptance hints", strings.Join(request.AcceptanceHints, "\n"), "multiline", "")
	if err != nil {
		return err
	}
	request.AcceptanceHints, err = wizardLines(acceptance, 32)
	if err != nil {
		return err
	}
	done, err := wizardInput(choose, "Optional definition-of-done hints, one per line.", "definition_hints", "Definition-of-done hints", strings.Join(request.DefinitionDoneHints, "\n"), "multiline", "")
	if err != nil {
		return err
	}
	request.DefinitionDoneHints, err = wizardLines(done, 32)
	if err != nil {
		return err
	}
	verification, err := wizardInput(choose, "Optional verification hints, one per line.", "verification_hints", "Verification hints", strings.Join(request.VerificationHints, "\n"), "multiline", "")
	if err != nil {
		return err
	}
	request.VerificationHints, err = wizardLines(verification, 32)
	return err
}

func runSpawnProfileWizard(choose setupChoice, role string, provider, model, thinking *string, thinkingBudget *int, effort *string, tokenBudget int) error {
	var err error
	*provider, err = wizardInput(choose, "Optional "+role+" provider override (blank uses the current provider; credentials remain native).", role+"_provider", role+" provider", *provider, "length", "0,128")
	if err != nil {
		return err
	}
	*model, err = wizardInput(choose, "Optional "+role+" model override (required when provider is set).", role+"_model", role+" model", *model, "length", "0,256")
	if err != nil {
		return err
	}
	thinkingDefault := *thinking
	if thinkingDefault == "" {
		thinkingDefault = "inherit"
	}
	selected, err := wizardOne(choose, "Choose "+role+" thinking mode. Forced values fail closed if the exact model does not support them.", []uiChoiceOption{
		{ID: "inherit", Label: "Inherit session setting"}, {ID: "auto", Label: "Provider auto"}, {ID: "on", Label: "Force on"}, {ID: "off", Label: "Force off"},
	}, thinkingDefault)
	if err != nil {
		return err
	}
	if selected == "inherit" {
		selected = ""
	}
	*thinking = selected
	*thinkingBudget, err = wizardInt(choose, role+" thinking-token budget (0 inherits; never exceeds the role token cap).", role+"_thinking_tokens", *thinkingBudget, 0, min(tokenBudget, 2_000_000))
	if err != nil {
		return err
	}
	effortDefault := *effort
	if effortDefault == "" {
		effortDefault = "inherit"
	}
	selected, err = wizardOne(choose, "Choose "+role+" provider-native reasoning effort. Forced values fail closed when unsupported.", []uiChoiceOption{
		{ID: "inherit", Label: "Inherit session setting"}, {ID: "low", Label: "Low"}, {ID: "medium", Label: "Medium"}, {ID: "high", Label: "High"}, {ID: "xhigh", Label: "Extra high"}, {ID: "max", Label: "Maximum"},
	}, effortDefault)
	if err != nil {
		return err
	}
	if selected == "inherit" {
		selected = ""
	}
	*effort = selected
	return validateSpawnProfile(role, *provider, *model, *thinking, *thinkingBudget, *effort, tokenBudget)
}

func wizardOne(choose setupChoice, prompt string, options []uiChoiceOption, defaultID string) (string, error) {
	response, err := choose(uiChoiceRequest{Prompt: prompt, Options: options, Default: []string{defaultID}})
	if err != nil {
		return "", err
	}
	if response.Cancelled {
		return "", errWizardCancelled
	}
	if len(response.Selected) != 1 {
		return "", errors.New("setup UI returned no exact single choice")
	}
	for _, option := range options {
		if response.Selected[0] == option.ID {
			return option.ID, nil
		}
	}
	return "", errors.New("setup UI returned an unknown choice")
}

func wizardInput(choose setupChoice, prompt, id, label, value, validatorKind, validatorSpec string) (string, error) {
	validator := &uiChoiceValidator{Kind: validatorKind, Spec: validatorSpec}
	response, err := choose(uiChoiceRequest{
		Prompt: prompt, Options: []uiChoiceOption{{ID: id, Label: label, Input: &uiChoiceInput{Default: value, Validator: validator}}},
		Default: []string{id},
	})
	if err != nil {
		return "", err
	}
	if response.Cancelled {
		return "", errWizardCancelled
	}
	if len(response.Selected) != 1 || response.Selected[0] != id {
		return "", errors.New("setup UI returned an invalid input choice")
	}
	return strings.TrimSpace(response.InputValue), nil
}

func wizardInt(choose setupChoice, prompt, id string, value, minimum, maximum int) (int, error) {
	raw, err := wizardInput(choose, prompt, id, id, strconv.Itoa(value), "int", "")
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be in %d..%d", id, minimum, maximum)
	}
	return parsed, nil
}

func wizardLines(raw string, maximum int) ([]string, error) {
	seen := map[string]bool{}
	var result []string
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		if len(line) > 2048 || len(result) == maximum {
			return nil, errors.New("wizard list exceeds bounded line count or size")
		}
		seen[line] = true
		result = append(result, line)
	}
	return result, nil
}

func normalizedVerifierProfile(value string) string {
	if value == "advisory" {
		return value
	}
	return "required"
}
