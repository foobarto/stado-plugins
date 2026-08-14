package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestWatchdogUsesSupportedPinnedResearchSpawnShape(t *testing.T) {
	state, contract := activeToolState(t)
	review := &reviewRequest{ID: "review-1", Anchor: state.CurrentAnchor, Signals: []signal{{Type: "completion_claim", Severity: "high", EvidenceRefs: []string{"git:evidence"}}}, Attempt: 1}
	request, err := buildWatchdogSpawnRequest(contract, state.Config, review, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if !request.Async || !request.Ephemeral || request.Role != "explorer" || request.Mode != "read_only" || request.ToolProfile != "research" || request.Ownership != reviewAgentOwnership(contract.RunID, review) {
		t.Fatalf("unsupported reviewer admission shape: %+v", request)
	}
	if !reflect.DeepEqual(request.NarrowTools, reviewerResearchTools) {
		t.Fatalf("reviewer tools=%v want=%v", request.NarrowTools, reviewerResearchTools)
	}
	if request.MaxTurns != 4 || request.TimeoutSeconds != state.Config.WatchdogTimeoutSecond || request.TokenBudget != 2048 {
		t.Fatalf("review bounds lost: %+v", request)
	}
	if request.IdempotencyKey == "" || request.IdempotencyKey != agentSpawnKey(contract.RunID, string(review.Purpose), review.ID, "1") {
		t.Fatalf("review spawn lacks stable application request identity: %+v", request)
	}
	if request.Source == nil || request.Source.At != review.Anchor.TurnRef {
		t.Fatalf("review source=%+v want exact event anchor %q", request.Source, review.Anchor.TurnRef)
	}
	if !strings.Contains(request.Prompt, "supervision watchdog") || !strings.Contains(request.Prompt, state.CurrentAnchor.TreeDigest) {
		t.Fatalf("watchdog policy/anchor missing from prompt: %s", request.Prompt)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"allowed_tools"`) || strings.Contains(string(raw), `"role":"watchdog"`) || strings.Contains(string(raw), `"mode":"run"`) {
		t.Fatalf("legacy unsupported reviewer fields remain: %s", raw)
	}
	if strings.Contains(string(raw), `"session_id"`) || !strings.Contains(string(raw), `"source":{"at":"`+review.Anchor.TurnRef+`"}`) {
		t.Fatalf("review source must contain only the exact authenticated turn_ref: %s", raw)
	}
}

func TestCompletionVerifierUsesFreshPinnedResearchRequest(t *testing.T) {
	state, contract := completionReviewState(t)
	if _, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictApprove, Anchor: state.CurrentAnchor, Rationale: "send to verifier", EvidenceRefs: []string{"git:c0"}}}, ""); err != nil {
		t.Fatal(err)
	}
	request, err := buildCompletionVerifierSpawnRequest(contract, state.Config, state.PendingReview, state.PendingCompletion, state.Config.VerifierTokenBudget)
	if err != nil {
		t.Fatal(err)
	}
	if !request.Async || !request.Ephemeral || request.Role != "explorer" || request.Mode != "read_only" || request.ToolProfile != "research" || request.Ownership != reviewAgentOwnership(contract.RunID, state.PendingReview) {
		t.Fatalf("unsupported final verifier admission: %+v", request)
	}
	if !reflect.DeepEqual(request.NarrowTools, reviewerResearchTools) || request.TokenBudget != state.Config.VerifierTokenBudget {
		t.Fatalf("verifier tools/budget=%+v", request)
	}
	if request.Source == nil || request.Source.At != state.PendingReview.Anchor.TurnRef {
		t.Fatalf("verifier source=%+v want exact candidate anchor %q", request.Source, state.PendingReview.Anchor.TurnRef)
	}
	if request.IdempotencyKey == "" || request.IdempotencyKey != agentSpawnKey(contract.RunID, string(state.PendingReview.Purpose), state.PendingReview.ID, "1") {
		t.Fatalf("verifier spawn lacks stable application request identity: %+v", request)
	}
	for _, want := range []string{"independent stado completion verifier", state.CurrentAnchor.TreeDigest, "git:criterion-0", "definition_of_done", "host_verification_facts", "never executable command authority"} {
		if !strings.Contains(request.Prompt, want) {
			t.Fatalf("verifier prompt omits %q: %s", want, request.Prompt)
		}
	}
}

func TestDelayedReviewKeepsAuthenticatedEventSourceAfterWorkerAdvances(t *testing.T) {
	state, contract := activeToolState(t)
	delayed := &reviewRequest{
		ID: "review-delayed", Anchor: state.CurrentAnchor,
		Signals: []signal{{Type: "periodic_turn", Severity: "info", EvidenceRefs: []string{"event:turn-7"}}},
		Attempt: 1,
	}
	state.CurrentAnchor = anchor{
		SessionSequence: 8, PlanVersion: 1, ActiveStep: "plan-implement", TreeDigest: "tree-new",
		TurnRef: "git:refs/sessions/worker-1/tree@89abcdef0123456789abcdef0123456789abcdef#turn-8-iteration-1",
	}

	request, err := buildWatchdogSpawnRequest(contract, state.Config, delayed, state.Config.WatchdogTokenBudget)
	if err != nil {
		t.Fatal(err)
	}
	if request.Source == nil || request.Source.At != delayed.Anchor.TurnRef || request.Source.At == state.CurrentAnchor.TurnRef {
		t.Fatalf("delayed review moved to mutable worker tip: source=%+v event=%q current=%q", request.Source, delayed.Anchor.TurnRef, state.CurrentAnchor.TurnRef)
	}
}

func TestReviewerProfilesMapByteExactlyToGenericSpawnABI(t *testing.T) {
	state, contract := activeToolState(t)
	state.Config.WatchdogProvider, state.Config.WatchdogModel = "openai", "gpt-5.6"
	state.Config.WatchdogThinking, state.Config.WatchdogThinkingBudgetTokens, state.Config.WatchdogReasoningEffort = "on", 2048, "xhigh"
	review := &reviewRequest{ID: "configured", Anchor: state.CurrentAnchor, Purpose: reviewPurposeWatchdog, Signals: []signal{{Type: "periodic_turn"}}, Attempt: 1}
	request, err := buildWatchdogSpawnRequest(contract, state.Config, review, 4096)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(request)
	for _, exact := range []string{`"provider":"openai"`, `"model":"gpt-5.6"`, `"thinking":"on"`, `"thinking_budget_tokens":2048`, `"reasoning_effort":"xhigh"`, `"token_budget":4096`} {
		if !strings.Contains(string(raw), exact) {
			t.Fatalf("spawn JSON omits %s: %s", exact, raw)
		}
	}

	state.Config.VerifierProvider, state.Config.VerifierModel = "anthropic", "claude-sonnet-4-6"
	state.Config.VerifierThinking, state.Config.VerifierThinkingBudgetTokens, state.Config.VerifierReasoningEffort = "auto", 1024, "high"
	state, contract = completionReviewStateWithConfig(t, state.Config)
	if _, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictApprove, Anchor: state.CurrentAnchor, Rationale: "verify", EvidenceRefs: []string{"git:c0"}}}, ""); err != nil {
		t.Fatal(err)
	}
	verifier, err := buildCompletionVerifierSpawnRequest(contract, state.Config, state.PendingReview, state.PendingCompletion, state.Config.VerifierTokenBudget)
	if err != nil {
		t.Fatal(err)
	}
	if verifier.Provider != "anthropic" || verifier.Model != "claude-sonnet-4-6" || verifier.Thinking != "auto" || verifier.ThinkingBudgetTokens != 1024 || verifier.ReasoningEffort != "high" {
		t.Fatalf("verifier profile lost: %+v", verifier)
	}
}

func TestBaselineUsesTheSameExactWatchdogProfile(t *testing.T) {
	setup := testSetupState(t)
	setup.Request.Config.WatchdogProvider, setup.Request.Config.WatchdogModel = "openai", "gpt-5.6"
	setup.Request.Config.WatchdogThinking = "on"
	setup.Request.Config.WatchdogThinkingBudgetTokens = 4096
	setup.Request.Config.WatchdogReasoningEffort = "high"
	request, err := buildBaselineSpawnRequest(setup, 1)
	if err != nil {
		t.Fatal(err)
	}
	if request.Provider != "openai" || request.Model != "gpt-5.6" || request.Thinking != "on" || request.ThinkingBudgetTokens != 4096 || request.ReasoningEffort != "high" {
		t.Fatalf("baseline profile lost: %+v", request)
	}
}

func completionReviewStateWithConfig(t *testing.T, cfg config) (runState, supervisionContract) {
	state, contract := completionReviewState(t)
	state.Config, contract.Config = cfg, cfg
	return state, contract
}

func TestReviewerProfileValidationMatchesGenericHostBounds(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*config)
	}{
		{"provider requires model", func(cfg *config) { cfg.WatchdogProvider = "openai" }},
		{"thinking enum", func(cfg *config) { cfg.WatchdogThinking = "forced" }},
		{"thinking budget token cap", func(cfg *config) { cfg.WatchdogThinkingBudgetTokens = cfg.WatchdogTokenBudget + 1 }},
		{"effort enum", func(cfg *config) { cfg.VerifierReasoningEffort = "ultra" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := defaultConfig()
			test.mutate(&cfg)
			if err := cfg.validate(); err == nil {
				t.Fatalf("accepted invalid profile: %+v", cfg)
			}
		})
	}
}

func TestReviewSpawnRequestIsByteStableAcrossModuleRebind(t *testing.T) {
	state, contract := activeToolState(t)
	review := &reviewRequest{
		ID: "review-rebind", Anchor: state.CurrentAnchor, Purpose: reviewPurposeWatchdog,
		Signals: []signal{{Type: "periodic_turn", Severity: "info", EvidenceRefs: []string{"event:turn-7"}}},
		Attempt: 1,
	}
	first, err := buildWatchdogSpawnRequest(contract, state.Config, review, state.Config.WatchdogTokenBudget)
	if err != nil {
		t.Fatal(err)
	}
	rebound, err := buildWatchdogSpawnRequest(contract, state.Config, cloneReview(review), state.Config.WatchdogTokenBudget)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(first)
	reboundJSON, _ := json.Marshal(rebound)
	if string(firstJSON) != string(reboundJSON) || first.IdempotencyKey == "" || first.Ownership != reviewAgentOwnership(contract.RunID, review) {
		t.Fatalf("module rebind changed logical review admission: first=%s rebound=%s", firstJSON, reboundJSON)
	}
}

func TestReviewSourceRejectsMutableAndMalformedSelectors(t *testing.T) {
	for _, value := range []string{
		"", "last_committed_turn", "turns/7",
		"git:refs/sessions/worker-1/tree@0123456789abcdef#turn-7-iteration-1",
		"git:refs/sessions/worker-1/tree@0123456789abcdef0123456789abcdef01234567#turn-0-iteration-1",
		"git:refs/sessions/worker-1/tree@0123456789ABCDEF0123456789ABCDEF01234567#turn-7-iteration-1",
	} {
		if source, err := exactTurnSource(value); err == nil || source != nil {
			t.Fatalf("accepted non-exact source %q as %+v", value, source)
		}
	}
	for _, value := range []string{
		"git:refs/sessions/worker-1/tree@0123456789abcdef0123456789abcdef01234567#turn-7-iteration-1",
		"git:refs/sessions/worker-1/tree@empty#turn-7-iteration-1",
	} {
		source, err := exactTurnSource(value)
		if err != nil || source == nil || source.At != value {
			t.Fatalf("rejected exact source %q: source=%+v err=%v", value, source, err)
		}
	}
}
