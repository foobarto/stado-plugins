package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func testSetupRequest() setupRequest {
	request := defaultSetupRequest("implement resumable imports")
	request.AcceptanceHints = []string{"restart after partial progress"}
	request.DefinitionDoneHints = []string{"documentation reconciled"}
	request.VerificationHints = []string{"go test ./..."}
	return request
}

func testBaselineProposal() baselineProposal {
	return baselineProposal{Schema: baselineProposalSchema, Baseline: baseline{
		Objective:   "Implement resumable imports without breaking existing callers",
		Constraints: []string{"preserve transactional restart semantics"}, NonGoals: []string{"redesign unrelated import formats"},
		AcceptanceCriteria: []string{"partial imports resume", "existing callers remain compatible"},
		Plan: []baselineStep{
			{ID: "define-semantics", Title: "Define restart semantics", DoneWhen: "failure boundaries are explicit"},
			{ID: "implement", Title: "Implement resume path", DoneWhen: "state resumes without duplication"},
			{ID: "verify", Title: "Verify behavior", DoneWhen: "focused and full tests pass"},
		},
		DefinitionOfDone: []string{"implementation, tests, and docs agree"},
		Verification:     []string{"go test ./..."}, Risks: []string{"duplicate rows after restart"},
	}}
}

func testSetupState(t *testing.T) setupState {
	t.Helper()
	setup, err := newSetupStateWithNonce(testSetupRequest(), authenticatedAgentParent{
		SessionID: "parent-session", SessionGeneration: 7, CanonicalRepoID: "repo-1",
	}, strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	return setup
}

func TestSetupRequestAndRunIdentityUseFreshRestartSafeEntropy(t *testing.T) {
	request := testSetupRequest()
	parent := authenticatedAgentParent{SessionID: "parent-session", SessionGeneration: 7, CanonicalRepoID: "repo-1"}
	first, err := newSetupStateWithNonce(request, parent, strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	second, err := newSetupStateWithNonce(request, parent, strings.Repeat("a", 32))
	if err != nil || first.RunID != second.RunID || first.Phase != setupPhaseStarting || first.Parent != parent {
		t.Fatalf("durable setup identity mismatch: first=%+v second=%+v err=%v", first, second, err)
	}
	changed, err := newSetupStateWithNonce(request, parent, strings.Repeat("b", 32))
	if err != nil || changed.RunID == first.RunID {
		t.Fatalf("distinct entropy did not create a distinct run: changed=%+v err=%v", changed, err)
	}
	// A command sequence may restart at the same value after rebind. Fresh WASI
	// entropy, not callback-local sequencing, keeps the broker run identity
	// distinct from a prior terminal recurrence.
	rebound, err := newSetupState(request, parent)
	if err != nil || rebound.RunID == first.RunID || rebound.RunID == changed.RunID {
		t.Fatalf("rebound setup reused a durable run identity: rebound=%+v err=%v", rebound, err)
	}
	again, err := newSetupState(request, parent)
	if err != nil || again.RunID == rebound.RunID {
		t.Fatalf("identical restarted command reused run id: first=%s second=%s err=%v", rebound.RunID, again.RunID, err)
	}
	corrupted := again
	corrupted.RunID = "callback-sequence-1"
	if err := corrupted.validate(); err == nil {
		t.Fatal("durable setup accepted a non-random legacy run identity")
	}
	request.Config.AssuranceProfile = "high_assurance"
	request.Config.VerifierProfile = "advisory"
	if err := request.validate(); err == nil {
		t.Fatal("high-assurance advisory verifier was accepted")
	}
}

func TestBaselineSpawnIdentitySurvivesRebindAndSeparatesAttempts(t *testing.T) {
	setup := testSetupState(t)
	rebound := setup
	first := baselineAgentSpawnKey(setup)
	if first == "" || baselineAgentSpawnKey(rebound) != first {
		t.Fatalf("identical durable setup did not reproduce spawn identity: %q", first)
	}
	if err := setup.beginBaseline("baseline-child-1"); err != nil {
		t.Fatal(err)
	}
	if err := setup.markBaselineFailed("retry"); err != nil {
		t.Fatal(err)
	}
	if second := baselineAgentSpawnKey(setup); second == first {
		t.Fatalf("new bounded baseline attempt reused spawn identity: %q", second)
	}
	request, err := buildBaselineSpawnRequest(setup, setup.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	reboundRequest, err := buildBaselineSpawnRequest(setup, setup.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	left, _ := json.Marshal(request)
	right, _ := json.Marshal(reboundRequest)
	if string(left) != string(right) || request.IdempotencyKey != baselineAgentSpawnKeyForAttempt(setup, setup.Attempt) || request.Ownership != currentBaselineAgentOwnership(setup) {
		t.Fatalf("rebound baseline request changed logical admission: first=%s second=%s", left, right)
	}
}

func TestSetupCancellationRemainsFencedUntilExactChildCleanupIsDurable(t *testing.T) {
	setup := testSetupState(t)
	if err := setup.beginBaseline("baseline-child"); err != nil {
		t.Fatal(err)
	}
	if err := setup.cancel("operator cancelled setup"); err != nil {
		t.Fatal(err)
	}
	if setup.Phase != setupPhaseCancelled || setup.CancellationAgentID != "baseline-child" || setup.CancellationCleaned {
		t.Fatalf("setup cancellation erased its exact cleanup obligation: %+v", setup)
	}
	if err := setup.validate(); err != nil {
		t.Fatalf("durable pending cleanup was rejected: %v", err)
	}
	encoded, _ := json.Marshal(setup)
	var rebound setupState
	if err := decodeStrictBytes(encoded, &rebound); err != nil || rebound.CancellationAgentID != "baseline-child" || rebound.CancellationCleaned {
		t.Fatalf("setup cleanup fence did not survive rebind: %+v err=%v", rebound, err)
	}
	rebound.CancellationAgentID = ""
	rebound.CancellationCleaned = true
	if err := rebound.validate(); err != nil {
		t.Fatalf("cleaned setup cancellation was rejected: %v", err)
	}
	broken := rebound
	broken.CancellationCleaned = false
	if err := broken.validate(); err == nil {
		t.Fatal("cancelled setup with an unaccounted empty cleanup target was accepted")
	}

	withoutChild := testSetupState(t)
	if err := withoutChild.cancel("cancel before spawn"); err != nil || !withoutChild.CancellationCleaned || withoutChild.CancellationAgentID != "" {
		t.Fatalf("childless setup cancellation did not finish immediately: %+v err=%v", withoutChild, err)
	}
}

func TestBaselineTerminalAdoptsExactDurableSpawnIntentAfterLostReply(t *testing.T) {
	setup := testSetupState(t)
	complete := true
	facts := agentDownFacts{
		Schema: agentDownFactsSchema,
		Child:  &agentDownChild{SessionID: "baseline-child", Status: "completed", Role: "explorer", Mode: "read_only", Execution: "wait"},
		Budget: &agentDownBudget{
			TokenLimit: uint64(setup.Request.Config.WatchdogTokenBudget), TurnLimit: 4,
			TimeoutSeconds: uint64(setup.Request.Config.WatchdogTimeoutSecond),
		},
		Terminal: &agentDownTerminalMetadata{Usage: &agentTokenUsage{InputTokens: 7, OutputTokens: 3}, UsageComplete: &complete},
		Scope:    &agentDownScope{Ownership: baselineAgentOwnership(setup)},
	}
	matched, err := setup.observeAgentDown(setup.Parent, 11, nil, facts)
	if err != nil || !matched || setup.Phase != setupPhaseRunning || setup.Attempt != 1 || setup.BaselineAgentID != "baseline-child" || setup.Terminal == nil || setup.Terminal.InvalidReason != "" {
		t.Fatalf("lost spawn reply was not recovered from exact terminal intent: matched=%v setup=%+v err=%v", matched, setup, err)
	}

	failed := testSetupState(t)
	if err := failed.beginBaseline("old-child"); err != nil {
		t.Fatal(err)
	}
	if err := failed.markBaselineFailed("retry"); err != nil {
		t.Fatal(err)
	}
	facts.Child.SessionID = "baseline-child-2"
	facts.Scope.Ownership = baselineAgentOwnership(failed)
	matched, err = failed.observeAgentDown(failed.Parent, 12, nil, facts)
	if err != nil || !matched || failed.Attempt != 2 || failed.BaselineAgentID != "baseline-child-2" || failed.Terminal == nil || failed.Terminal.InvalidReason != "" {
		t.Fatalf("retried lost spawn reply was not recovered: matched=%v setup=%+v err=%v", matched, failed, err)
	}
}

func TestSetupAndBaselineRespectGenericWorkerBounds(t *testing.T) {
	request := testSetupRequest()
	request.Objective = strings.Repeat("x", (4<<10)+1)
	if err := request.validate(); err == nil {
		t.Fatal("setup accepted an objective the generic worker broker must reject")
	}
	proposal := testBaselineProposal()
	proposal.Baseline.Objective = strings.Repeat("x", (4<<10)+1)
	raw, _ := json.Marshal(proposal)
	if _, err := decodeBaselineProposal(raw); err == nil {
		t.Fatal("baseline accepted an objective the generic worker broker must reject")
	}
	request = testSetupRequest()
	request.AcceptanceHints = []string{strings.Repeat("a", 2048), strings.Repeat("b", 2048), strings.Repeat("c", 2048), strings.Repeat("d", 2048), strings.Repeat("e", 2048), strings.Repeat("f", 2048)}
	if err := request.validate(); err == nil || !strings.Contains(err.Error(), "12 KiB") {
		t.Fatalf("oversized aggregate setup input was accepted: %v", err)
	}
}

func TestBaselineProposalIsStrictFullShapeAndConvertsToContract(t *testing.T) {
	proposal := testBaselineProposal()
	raw, _ := json.Marshal(proposal)
	decoded, err := decodeBaselineProposal(raw)
	if err != nil {
		t.Fatal(err)
	}
	contract := decoded.Baseline.contract("run-1", defaultConfig())
	contract.ArtifactID, contract.ArtifactVersion = "artifact-1", 1
	if err := contract.validate(); err != nil {
		t.Fatal(err)
	}
	if len(contract.Plan) != 3 || len(contract.Acceptance) != 2 || contract.Plan[0].ID != "define-semantics" || len(contract.NonGoals) != 1 || len(contract.Risks) != 1 {
		t.Fatalf("full baseline shape was lost: %+v", contract)
	}
	state, err := newContractRunState(contract)
	if err != nil || state.PlanTotal != 3 || state.CriteriaTotal != 2 || state.CurrentAnchor.ActiveStep != "define-semantics" {
		t.Fatalf("plan progression and criterion evidence were conflated: state=%+v err=%v", state, err)
	}
	for name, invalid := range map[string]string{
		"unknown field": strings.Replace(string(raw), `"baseline":{`, `"extra":true,"baseline":{`, 1),
		"wrong schema":  strings.Replace(string(raw), baselineProposalSchema, "stado.dev/supervise/baseline-proposal/v2", 1),
		"missing plan":  strings.Replace(string(raw), `"plan":[`, `"plan_removed":[`, 1),
		"trailing":      string(raw) + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeBaselineProposal([]byte(invalid)); err == nil {
				t.Fatal("invalid baseline proposal was accepted")
			}
		})
	}
}

func TestFreshBaselineTerminalCleanupCannotEraseProposal(t *testing.T) {
	setup := testSetupState(t)
	if err := setup.beginBaseline("baseline-child"); err != nil {
		t.Fatal(err)
	}
	complete := true
	terminal := agentTerminalMetadata{
		Usage: agentTokenUsage{InputTokens: 500, OutputTokens: 120, CacheReadTokens: 20}, UsageComplete: true,
		Cleanup: &agentCleanupDiagnostic{Kind: "provider_close", Fingerprint: testFactDigest},
	}
	facts := agentDownFacts{
		Schema:   agentDownFactsSchema,
		Child:    &agentDownChild{SessionID: "baseline-child", Status: "completed", Role: "explorer", Mode: "read_only", Execution: "wait"},
		Budget:   &agentDownBudget{TokenLimit: uint64(setup.Request.Config.WatchdogTokenBudget), TurnLimit: 4, TimeoutSeconds: uint64(setup.Request.Config.WatchdogTimeoutSecond)},
		Terminal: &agentDownTerminalMetadata{Usage: &terminal.Usage, UsageComplete: &complete, Cleanup: terminal.Cleanup},
		Scope:    &agentDownScope{Ownership: currentBaselineAgentOwnership(setup)},
	}
	matched, err := setup.observeAgentDown(setup.Parent, 41, nil, facts)
	if err != nil || !matched || setup.Terminal == nil || setup.BaselineTokenUsage != terminal.Usage {
		t.Fatalf("baseline terminal reduction: matched=%v setup=%+v err=%v", matched, setup, err)
	}
	proposal := testBaselineProposal()
	raw, _ := json.Marshal(proposal)
	messages := agentMessages{
		Status: "completed", Offset: 1, Terminal: &terminal,
		Messages: []agentMessage{{Role: "assistant", Content: string(raw)}},
	}
	consumed, err := setup.consumeBaselineMessages(messages)
	if err != nil || !consumed || setup.Phase != setupPhaseReady || setup.Proposal == nil {
		t.Fatalf("valid proposal was erased by cleanup: consumed=%v setup=%+v err=%v", consumed, setup, err)
	}
	if !strings.Contains(strings.Join(setup.Diagnostics, "\n"), "provider_close cleanup "+testFactDigest) {
		t.Fatalf("cleanup was not retained as a diagnostic: %v", setup.Diagnostics)
	}
}

func TestBaselineSpawnShapeMismatchFailsPolicyButNotHostSchema(t *testing.T) {
	setup := testSetupState(t)
	if err := setup.beginBaseline("baseline-child"); err != nil {
		t.Fatal(err)
	}
	complete := true
	terminal := agentTerminalMetadata{UsageComplete: true}
	facts := agentDownFacts{
		Schema:   agentDownFactsSchema,
		Child:    &agentDownChild{SessionID: "baseline-child", Status: "completed", Role: "worker", Mode: "workspace_write", Execution: "retained"},
		Budget:   &agentDownBudget{TokenLimit: uint64(setup.Request.Config.WatchdogTokenBudget), TurnLimit: 4, TimeoutSeconds: uint64(setup.Request.Config.WatchdogTimeoutSecond)},
		Terminal: &agentDownTerminalMetadata{Usage: &terminal.Usage, UsageComplete: &complete},
		Scope:    &agentDownScope{Ownership: "other"},
	}
	if matched, err := setup.observeAgentDown(setup.Parent, 42, nil, facts); err != nil || !matched || setup.Terminal.InvalidReason == "" {
		t.Fatalf("generic facts did not produce plugin-owned rejection: matched=%v setup=%+v err=%v", matched, setup, err)
	}
	proposal := testBaselineProposal()
	raw, _ := json.Marshal(proposal)
	consumed, err := setup.consumeBaselineMessages(agentMessages{Status: "completed", Terminal: &terminal, Messages: []agentMessage{{Role: "assistant", Content: string(raw)}}})
	if err != nil || !consumed || setup.Phase != setupPhaseFailed || setup.Proposal != nil {
		t.Fatalf("invalid child facts authorized a proposal: consumed=%v setup=%+v err=%v", consumed, setup, err)
	}
}

func TestFailedBaselineChildCannotAuthorizeValidLookingProposal(t *testing.T) {
	setup := testSetupState(t)
	if err := setup.beginBaseline("baseline-child"); err != nil {
		t.Fatal(err)
	}
	complete := true
	facts := agentDownFacts{
		Schema:   agentDownFactsSchema,
		Child:    &agentDownChild{SessionID: "baseline-child", Status: "failed", Role: "explorer", Mode: "read_only", Execution: "wait"},
		Budget:   &agentDownBudget{TokenLimit: uint64(setup.Request.Config.WatchdogTokenBudget), TurnLimit: 4, TimeoutSeconds: uint64(setup.Request.Config.WatchdogTimeoutSecond)},
		Terminal: &agentDownTerminalMetadata{Usage: &agentTokenUsage{}, UsageComplete: &complete},
		Scope:    &agentDownScope{Ownership: currentBaselineAgentOwnership(setup)},
	}
	if matched, err := setup.observeAgentDown(setup.Parent, 44, nil, facts); err != nil || !matched {
		t.Fatalf("terminal observation: matched=%v err=%v", matched, err)
	}
	proposalRaw, _ := json.Marshal(testBaselineProposal())
	consumed, err := setup.consumeBaselineMessages(agentMessages{
		Status: "failed", Terminal: &agentTerminalMetadata{UsageComplete: true},
		Messages: []agentMessage{{Role: "assistant", Content: string(proposalRaw)}},
	})
	if err != nil || !consumed || setup.Phase != setupPhaseFailed || setup.Proposal != nil {
		t.Fatalf("failed child authorized baseline: setup=%+v consumed=%v err=%v", setup, consumed, err)
	}
}

func TestBaselinePromptKeepsApprovalOutsideModel(t *testing.T) {
	prompt, err := baselinePrompt(testSetupRequest())
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{baselineProposalSchema, "operator will make a later quality-workflow confirmation", "no authority", "restart after partial progress"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("baseline prompt omitted %q: %s", required, prompt)
		}
	}
}
