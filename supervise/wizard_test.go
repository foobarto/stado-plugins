package main

import (
	"errors"
	"reflect"
	"testing"
)

type scriptedWizard struct {
	selected map[string]string
	inputs   map[string]string
	calls    []uiChoiceRequest
	cancelAt int
}

func (s *scriptedWizard) choose(request uiChoiceRequest) (uiChoiceResponse, error) {
	s.calls = append(s.calls, request)
	if s.cancelAt > 0 && len(s.calls) == s.cancelAt {
		return uiChoiceResponse{Cancelled: true}, nil
	}
	if len(request.Options) == 0 {
		return uiChoiceResponse{}, errors.New("test received empty choice")
	}
	if len(request.Options) == 1 && request.Options[0].Input != nil {
		id := request.Options[0].ID
		value := request.Options[0].Input.Default
		if configured, ok := s.inputs[id]; ok {
			value = configured
		}
		return uiChoiceResponse{Selected: []string{id}, InputValue: value}, nil
	}
	selected := request.Default[0]
	if configured, ok := s.selected[request.Options[0].ID]; ok {
		selected = configured
	}
	return uiChoiceResponse{Selected: []string{selected}}, nil
}

func TestSetupWizardCollectsNormalBasicFlowWithoutJSONBlob(t *testing.T) {
	script := &scriptedWizard{selected: map[string]string{}, inputs: map[string]string{"objective": "implement resumable imports"}}
	request, err := runSetupWizard("seed objective", script.choose)
	if err != nil {
		t.Fatal(err)
	}
	if request.Objective != "implement resumable imports" || request.Config.Mode != modeEvent || request.Config.PivotApproval != "user" || request.Config.AssuranceProfile != "standard" || request.WorkerConflict != "replace_operator_loop" {
		t.Fatalf("basic wizard result=%+v", request)
	}
	if len(script.calls) != 6 {
		t.Fatalf("basic wizard calls=%d want 6", len(script.calls))
	}
	if option := script.calls[0].Options[0]; option.ID != "objective" || option.Input == nil || option.Input.Validator == nil || option.Input.Validator.Kind != "length" {
		t.Fatalf("objective was not collected as a normal bounded field: %+v", option)
	}
	for _, call := range script.calls {
		if len(call.Options) == 1 && call.Options[0].Input != nil && call.Options[0].Input.Validator.Kind == "multiline" {
			t.Fatal("basic wizard collapsed setup into a multiline JSON prompt")
		}
	}
}

func TestSetupWizardAdvancedFlowCollectsTypedFields(t *testing.T) {
	script := &scriptedWizard{
		selected: map[string]string{
			"event": "live", "user": "watchdog", "standard": "custom", "replace_operator_loop": "reject",
			"defaults": "customize", "off": "on", "required": "advisory",
		},
		inputs: map[string]string{
			"objective": "bounded migration", "review_every_turns": "5", "paths": "internal\ndocs\ninternal",
			"watchdog_provider": "openai", "watchdog_model": "watchdog-model", "verifier_provider": "anthropic", "verifier_model": "verifier-model", "watchdog_tokens": "12000",
			"verifier_tokens": "18000", "event_review_retries": "4", "failed_event_limit": "12", "correction_limit": "5",
			"live_retry_base_ms": "750", "live_retry_max_ms": "12000", "review_timeout": "900", "hold_ttl": "300",
			"acceptance_hints": "implementation works\ncompatibility preserved", "definition_hints": "docs agree",
			"verification_hints": "go test ./...\ngo vet ./...",
		},
	}
	request, err := runSetupWizard("", script.choose)
	if err != nil {
		t.Fatal(err)
	}
	if request.Config.Mode != modeLive || !request.Config.StrictLiveBarrier || request.Config.PivotApproval != "watchdog" || request.Config.AssuranceProfile != "custom" || request.Config.ReviewEveryTurns != 5 || request.Config.VerifierProfile != "advisory" {
		t.Fatalf("advanced policy fields=%+v", request.Config)
	}
	if request.Config.WatchdogProvider != "openai" || request.Config.WatchdogModel != "watchdog-model" || request.Config.VerifierProvider != "anthropic" || request.Config.VerifierModel != "verifier-model" || request.Config.WatchdogTokenBudget != 12000 || request.Config.VerifierTokenBudget != 18000 || request.Config.EventReviewRetries != 4 || request.Config.FailedEventLimit != 12 || request.Config.CorrectionLimit != 5 || request.Config.LiveRetryBaseMillis != 750 || request.Config.LiveRetryMaxMillis != 12000 || request.Config.WatchdogTimeoutSecond != 900 || request.Config.HoldTTLSeconds != 300 {
		t.Fatalf("advanced reviewer fields=%+v", request.Config)
	}
	if request.WorkerConflict != "reject" || !reflect.DeepEqual(request.Config.AllowedPathPrefixes, []string{"internal", "docs"}) || len(request.AcceptanceHints) != 2 || len(request.DefinitionDoneHints) != 1 || len(request.VerificationHints) != 2 {
		t.Fatalf("advanced setup result=%+v", request)
	}
}

func TestSpawnProfileWizardCollectsExactGenericOverrides(t *testing.T) {
	responses := []uiChoiceResponse{
		{Selected: []string{"watchdog_provider"}, InputValue: "openai"},
		{Selected: []string{"watchdog_model"}, InputValue: "gpt-5.6"},
		{Selected: []string{"on"}},
		{Selected: []string{"watchdog_thinking_tokens"}, InputValue: "7000"},
		{Selected: []string{"xhigh"}},
	}
	index := 0
	choose := func(request uiChoiceRequest) (uiChoiceResponse, error) {
		if index >= len(responses) {
			t.Fatalf("unexpected prompt: %+v", request)
		}
		response := responses[index]
		index++
		return response, nil
	}
	var provider, model, thinking, effort string
	var budget int
	if err := runSpawnProfileWizard(choose, "watchdog", &provider, &model, &thinking, &budget, &effort, 8000); err != nil {
		t.Fatal(err)
	}
	if provider != "openai" || model != "gpt-5.6" || thinking != "on" || budget != 7000 || effort != "xhigh" {
		t.Fatalf("profile=(%q,%q,%q,%d,%q)", provider, model, thinking, budget, effort)
	}
}

func TestSetupWizardCancellationIsNotConfirmation(t *testing.T) {
	script := &scriptedWizard{selected: map[string]string{}, inputs: map[string]string{}, cancelAt: 3}
	if _, err := runSetupWizard("objective", script.choose); !errors.Is(err, errWizardCancelled) {
		t.Fatalf("wizard cancellation error=%v", err)
	}
}

func TestWizardRejectsUnknownHostChoice(t *testing.T) {
	_, err := wizardOne(func(uiChoiceRequest) (uiChoiceResponse, error) {
		return uiChoiceResponse{Selected: []string{"forged"}}, nil
	}, "pick", []uiChoiceOption{{ID: "safe", Label: "Safe"}}, "safe")
	if err == nil {
		t.Fatal("unknown host choice was accepted")
	}
}
