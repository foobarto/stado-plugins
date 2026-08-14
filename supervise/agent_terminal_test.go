package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const testFactDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func terminalVerdictMessage(t *testing.T, result reviewerResult) agentMessage {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return agentMessage{Role: "assistant", Content: string(raw)}
}

func observePendingTerminal(t *testing.T, state *runState, terminal agentTerminalMetadata, childStatus string, mutate func(*agentDownFacts, *[]string)) {
	t.Helper()
	if state.PendingReview == nil {
		t.Fatal("test requires a pending review")
	}
	if state.PendingReview.AgentID == "" {
		state.PendingReview.AgentID = "review-child"
	}
	if state.PendingReview.TokenBudget == 0 {
		state.PendingReview.TokenBudget = reviewTokenBudget(state.Config, state.PendingReview)
	}
	ownership := reviewAgentOwnership(state.RunID, state.PendingReview)
	usageComplete := terminal.UsageComplete
	facts := agentDownFacts{
		Schema: agentDownFactsSchema,
		Child: &agentDownChild{
			SessionID: state.PendingReview.AgentID, Status: childStatus,
			Role: "explorer", Mode: "read_only", Execution: "wait",
		},
		Budget: &agentDownBudget{
			TokenLimit: uint64(state.PendingReview.TokenBudget), TurnLimit: 4,
			TimeoutSeconds: uint64(state.Config.WatchdogTimeoutSecond),
		},
		Terminal: &agentDownTerminalMetadata{Usage: &terminal.Usage, UsageComplete: &usageComplete, Cleanup: terminal.Cleanup},
		Scope:    &agentDownScope{Ownership: ownership},
	}
	var evidenceRefs []string
	if mutate != nil {
		mutate(&facts, &evidenceRefs)
	}
	matched, err := state.observeAgentDown(authenticatedAgentParent{
		SessionID: "application-session", SessionGeneration: 7, CanonicalRepoID: "repo-1",
	}, state.AgentDownSequence+1, evidenceRefs, facts)
	if err != nil || !matched {
		t.Fatalf("observe agent.down: matched=%v err=%v facts=%+v", matched, err, facts)
	}
}

func diagnosticsContain(state runState, value string) bool {
	for _, diagnostic := range state.Diagnostics {
		if strings.Contains(diagnostic, value) {
			return true
		}
	}
	return false
}

func TestAgentDownStrictDecoderRejectsShapeDrift(t *testing.T) {
	valid := `{"schema":"stado.dev/agent-down-facts/v1","child":{"session_id":"child","status":"completed","role":"explorer","mode":"read_only"},"budget":{"token_limit":2048,"turn_limit":4,"timeout_seconds":60},"terminal":{"usage":{"input_tokens":10,"output_tokens":2},"usage_complete":true},"scope":{"ownership":"supervise"}}`
	if _, err := decodeAgentDownFacts([]byte(valid)); err != nil {
		t.Fatalf("valid v1 facts rejected: %v", err)
	}
	for name, raw := range map[string]string{
		"unknown schema": strings.Replace(valid, agentDownFactsSchema, "stado.dev/agent-down-facts/v2", 1),
		"unknown field":  strings.Replace(valid, `"scope":{`, `"surprise":true,"scope":{`, 1),
		"trailing JSON":  valid + ` {}`,
		"missing usage":  strings.Replace(valid, `"usage":{"input_tokens":10,"output_tokens":2},`, "", 1),
		"negative token": strings.Replace(valid, `"input_tokens":10`, `"input_tokens":-1`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeAgentDownFacts([]byte(raw)); err == nil {
				t.Fatal("invalid agent.down facts accepted")
			}
		})
	}
}

func TestAgentDownBindsExactParentChildScopeChangesAndRefs(t *testing.T) {
	state, _ := activeToolState(t)
	state.PendingReview = &reviewRequest{ID: "review-1", Anchor: state.CurrentAnchor, Purpose: reviewPurposeWatchdog, AgentID: "review-child", TokenBudget: 2048}
	complete := true
	facts := agentDownFacts{
		Schema: agentDownFactsSchema,
		Child:  &agentDownChild{SessionID: "review-child", Status: "completed", Role: "explorer", Mode: "read_only", Execution: "wait"},
		Budget: &agentDownBudget{TokenLimit: 2048, TurnLimit: 4, TimeoutSeconds: 120},
		Terminal: &agentDownTerminalMetadata{
			Usage: &agentTokenUsage{InputTokens: 13, OutputTokens: 5}, UsageComplete: &complete,
		},
		Scope:   &agentDownScope{Ownership: reviewAgentOwnership(state.RunID, state.PendingReview), WritePaths: []string{"internal"}, WritePathsDigest: testFactDigest},
		Changes: &agentDownChanges{ForkTreeDigest: testFactDigest},
	}
	refs := []string{
		"git:refs/sessions/review-child/tree@0123456789abcdef0123456789abcdef01234567",
		"git:refs/sessions/review-child/trace@89abcdef0123456789abcdef0123456789abcdef",
	}
	parent := authenticatedAgentParent{SessionID: "application-session", SessionGeneration: 9, CanonicalRepoID: "repo-1"}
	matched, err := state.observeAgentDown(parent, 41, refs, facts)
	if err != nil || !matched || state.PendingReview.Terminal == nil {
		t.Fatalf("exact terminal reduction: matched=%v state=%+v err=%v", matched, state, err)
	}
	observed := state.PendingReview.Terminal
	if observed.Parent != parent || observed.Child.SessionID != "review-child" || observed.Scope.WritePathsDigest != testFactDigest || observed.Changes.ForkTreeDigest != testFactDigest || len(observed.EvidenceRefs) != 2 {
		t.Fatalf("terminal facts were not durably reduced: %+v", observed)
	}
	if state.WatchdogTokenUsage != facts.terminalMetadata().Usage || len(state.AgentTerminals) != 1 || state.AgentDownSequence != 41 {
		t.Fatalf("terminal accounting/history=%+v", state)
	}
	if observed.InvalidReason != "read-only review child reported a write scope" {
		t.Fatalf("write-scope mismatch was not derived as plugin policy: %+v", observed)
	}
	if _, err := state.observeAgentDown(authenticatedAgentParent{}, 42, refs, facts); err == nil {
		t.Fatal("agent.down without exact authenticated parent was accepted")
	}
	facts.Child.SessionID = "sibling-child"
	if _, err := state.observeAgentDown(parent, 42, refs, facts); err == nil {
		t.Fatal("child-mismatched evidence references were accepted")
	}
}

func TestAgentDownAdoptsExactPendingReviewIntentAfterLostSpawnReply(t *testing.T) {
	state, _ := activeToolState(t)
	state.PendingReview = &reviewRequest{
		ID: "review-lost-reply", Anchor: state.CurrentAnchor, Purpose: reviewPurposeWatchdog,
		Signals: []signal{{Type: "periodic_turn", Severity: "info"}},
	}
	complete := true
	facts := agentDownFacts{
		Schema: agentDownFactsSchema,
		Child:  &agentDownChild{SessionID: "recovered-child", Status: "completed", Role: "explorer", Mode: "read_only", Execution: "wait"},
		Budget: &agentDownBudget{
			TokenLimit: uint64(state.Config.WatchdogTokenBudget), TurnLimit: 4,
			TimeoutSeconds: uint64(state.Config.WatchdogTimeoutSecond),
		},
		Terminal: &agentDownTerminalMetadata{Usage: &agentTokenUsage{InputTokens: 9, OutputTokens: 4}, UsageComplete: &complete},
		Scope:    &agentDownScope{Ownership: reviewAgentOwnership(state.RunID, state.PendingReview)},
	}
	matched, err := state.observeAgentDown(authenticatedAgentParent{SessionID: "application-session", SessionGeneration: 7}, 17, nil, facts)
	if err != nil || !matched || state.PendingReview.AgentID != "recovered-child" || state.PendingReview.TokenBudget != state.Config.WatchdogTokenBudget || state.PendingReview.Terminal == nil || state.PendingReview.Terminal.InvalidReason != "" {
		t.Fatalf("lost review spawn reply was not recovered from exact terminal intent: matched=%v state=%+v err=%v", matched, state, err)
	}
	if state.WatchdogTokenUsage != facts.terminalMetadata().Usage {
		t.Fatalf("recovered terminal usage was not accounted exactly once: %+v", state.WatchdogTokenUsage)
	}
}

func TestAgentDownBindsExactOperatorInputReviewerAndLostSpawnReply(t *testing.T) {
	state, _ := activeToolState(t)
	route, _, err := state.beginOperatorInputReview(testApplicationCanonical, inputFact(state.RunID, "input-1", 1, "Please verify this follow-up."))
	if err != nil {
		t.Fatal(err)
	}
	claim := inputRecord(*route, "Please verify this follow-up.", operatorInputPhaseReviewing, 2)
	if err := state.acceptOperatorInputClaim(lifecycleIdentity{SessionID: "session-1", SessionGeneration: 2}, route, claim); err != nil {
		t.Fatal(err)
	}
	complete := true
	facts := agentDownFacts{
		Schema:   agentDownFactsSchema,
		Child:    &agentDownChild{SessionID: "input-review-child", Status: "completed", Role: "explorer", Mode: "read_only", Execution: "wait"},
		Budget:   &agentDownBudget{TokenLimit: uint64(state.Config.WatchdogTokenBudget), TurnLimit: 4, TimeoutSeconds: uint64(state.Config.WatchdogTimeoutSecond)},
		Terminal: &agentDownTerminalMetadata{Usage: &agentTokenUsage{InputTokens: 8, OutputTokens: 3}, UsageComplete: &complete},
		Scope:    &agentDownScope{Ownership: operatorInputReviewOwnership(state.RunID, route)},
	}
	matched, err := state.observeAgentDown(authenticatedAgentParent{SessionID: "application-session", SessionGeneration: 7}, 17, nil, facts)
	if err != nil || !matched || route.AgentID != "input-review-child" || route.Terminal == nil || route.Terminal.Purpose != reviewPurposeOperatorInput || route.Terminal.InvalidReason != "" {
		t.Fatalf("operator-input terminal was not exact-bound: matched=%v route=%+v err=%v", matched, route, err)
	}
	if state.WatchdogTokenUsage != facts.terminalMetadata().Usage {
		t.Fatalf("operator-input reviewer usage not accounted once: %+v", state.WatchdogTokenUsage)
	}
	if matched, err := state.observeAgentDown(authenticatedAgentParent{SessionID: "application-session", SessionGeneration: 7}, 17, nil, facts); err != nil || !matched || state.WatchdogTokenUsage != facts.terminalMetadata().Usage {
		t.Fatalf("agent.down redelivery was not idempotent: matched=%v usage=%+v err=%v", matched, state.WatchdogTokenUsage, err)
	}
}

func TestSignedReviewShapeMismatchInvalidatesTerminalObservation(t *testing.T) {
	for name, mutate := range map[string]func(*agentDownFacts){
		"role":      func(f *agentDownFacts) { f.Child.Role = "worker" },
		"mode":      func(f *agentDownFacts) { f.Child.Mode = "workspace_write" },
		"execution": func(f *agentDownFacts) { f.Child.Execution = "retained" },
		"ownership": func(f *agentDownFacts) { f.Scope.Ownership = "other" },
		"tokens":    func(f *agentDownFacts) { f.Budget.TokenLimit++ },
		"turns":     func(f *agentDownFacts) { f.Budget.TurnLimit++ },
		"timeout":   func(f *agentDownFacts) { f.Budget.TimeoutSeconds++ },
		"write scope": func(f *agentDownFacts) {
			f.Scope.WritePaths = []string{"internal"}
			f.Scope.WritePathsDigest = testFactDigest
		},
	} {
		t.Run(name, func(t *testing.T) {
			state, _ := activeToolState(t)
			state.PendingReview = &reviewRequest{ID: "review-1", Anchor: state.CurrentAnchor, Purpose: reviewPurposeWatchdog}
			terminal := agentTerminalMetadata{UsageComplete: true}
			observePendingTerminal(t, &state, terminal, "completed", func(facts *agentDownFacts, _ *[]string) { mutate(facts) })
			if state.PendingReview.Terminal == nil || state.PendingReview.Terminal.InvalidReason == "" {
				t.Fatalf("signed request mismatch was accepted: %+v", state.PendingReview)
			}
		})
	}
}

func TestTerminalCleanupCannotEraseCurrentWatchdogVerdict(t *testing.T) {
	state, _ := activeToolState(t)
	state.PendingReview = &reviewRequest{
		ID: "review-1", Anchor: state.CurrentAnchor, Purpose: reviewPurposeWatchdog,
		Signals: []signal{{Type: "periodic_turn", Severity: "info"}},
	}
	terminal := agentTerminalMetadata{
		Usage:         agentTokenUsage{InputTokens: 21, OutputTokens: 8, CacheReadTokens: 5, CacheWriteTokens: 3},
		UsageComplete: true,
		Cleanup:       &agentCleanupDiagnostic{Kind: "provider_close", Fingerprint: testFactDigest},
	}
	observePendingTerminal(t, &state, terminal, "completed", nil)
	messages := agentMessages{
		Status: "completed", Offset: 1,
		Messages: []agentMessage{terminalVerdictMessage(t, reviewerResult{Verdict: verdict{
			Decision: verdictContinue, Anchor: state.CurrentAnchor, Rationale: "current work remains aligned",
		}})},
		Terminal: &terminal,
	}
	change, consumed, err := state.consumeReviewerMessages(messages)
	if err != nil || !consumed || len(change.Actions) != 0 || state.PendingReview != nil || state.LastVerdict == nil || state.LastVerdict.Decision != verdictContinue {
		t.Fatalf("valid terminal verdict was not applied: change=%+v consumed=%v state=%+v err=%v", change, consumed, state, err)
	}
	if state.WatchdogTokenUsage != terminal.Usage {
		t.Fatalf("watchdog token facts=%+v want=%+v", state.WatchdogTokenUsage, terminal.Usage)
	}
	if !diagnosticsContain(state, "provider_close cleanup "+testFactDigest) {
		t.Fatalf("bounded cleanup fingerprint was not journalled separately: %v", state.Diagnostics)
	}
}

func TestTerminalVerifierUsageIsSeparateAndIncompleteUsageIsDiagnostic(t *testing.T) {
	state, _ := completionReviewState(t)
	anchor := state.CurrentAnchor
	if _, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{
		Decision: verdictApprove, Anchor: anchor, Rationale: "send exact candidate to fresh verifier", EvidenceRefs: []string{"git:criterion-0"},
	}}, ""); err != nil {
		t.Fatal(err)
	}
	terminal := agentTerminalMetadata{Usage: agentTokenUsage{InputTokens: 34, OutputTokens: 13}, UsageComplete: false}
	observePendingTerminal(t, &state, terminal, "completed", nil)
	messages := agentMessages{
		Status: "completed",
		Messages: []agentMessage{terminalVerdictMessage(t, reviewerResult{Verdict: verdict{
			Decision: verdictApprove, Anchor: anchor, Rationale: "all bounded evidence proves the contract", EvidenceRefs: []string{"git:criterion-0"},
		}})},
		Terminal: &terminal,
	}
	_, consumed, err := state.consumeReviewerMessages(messages)
	if err != nil || !consumed || !state.Completed || state.VerifierTokenUsage != terminal.Usage || state.WatchdogTokenUsage != (agentTokenUsage{}) {
		t.Fatalf("verifier terminal facts were not applied independently: consumed=%v state=%+v err=%v", consumed, state, err)
	}
	if !diagnosticsContain(state, "token usage is incomplete") {
		t.Fatalf("incomplete usage was not diagnostic: %v", state.Diagnostics)
	}
}

func TestTerminalFailureStillRecordsHostTokenFacts(t *testing.T) {
	state, _ := activeToolState(t)
	state.PendingReview = &reviewRequest{ID: "review-1", Anchor: state.CurrentAnchor, Purpose: reviewPurposeWatchdog}
	terminal := agentTerminalMetadata{Usage: agentTokenUsage{InputTokens: 100, OutputTokens: 28}, UsageComplete: true}
	observePendingTerminal(t, &state, terminal, "error", nil)
	messages := agentMessages{Status: "budget_exhausted", Terminal: &terminal}
	change, consumed, err := state.consumeReviewerMessages(messages)
	if err != nil || !consumed || state.PendingReview == nil || state.PendingReview.Attempt != 2 || len(change.Actions) != 1 || change.Actions[0].Kind != actionScheduleReviewRetry || state.WatchdogTokenUsage != terminal.Usage {
		t.Fatalf("terminal failure accounting: consumed=%v state=%+v err=%v", consumed, state, err)
	}
}

func TestReviewerTokenFactsAccumulateOnceFromAgentDown(t *testing.T) {
	state, _ := activeToolState(t)
	for index, usage := range []agentTokenUsage{
		{InputTokens: 10, OutputTokens: 4, CacheReadTokens: 3},
		{InputTokens: 7, OutputTokens: 2, CacheWriteTokens: 1},
	} {
		state.PendingReview = &reviewRequest{ID: "review", Anchor: state.CurrentAnchor, Purpose: reviewPurposeWatchdog}
		terminal := agentTerminalMetadata{Usage: usage, UsageComplete: true}
		observePendingTerminal(t, &state, terminal, "completed", nil)
		messages := agentMessages{
			Status: "completed",
			Messages: []agentMessage{terminalVerdictMessage(t, reviewerResult{Verdict: verdict{
				Decision: verdictContinue, Anchor: state.CurrentAnchor, Rationale: "bounded review remains current",
			}})},
			Terminal: &terminal,
		}
		if _, consumed, err := state.consumeReviewerMessages(messages); err != nil || !consumed {
			t.Fatalf("review %d accounting: consumed=%v err=%v", index, consumed, err)
		}
	}
	want := agentTokenUsage{InputTokens: 17, OutputTokens: 6, CacheReadTokens: 3, CacheWriteTokens: 1}
	if state.WatchdogTokenUsage != want {
		t.Fatalf("cumulative watchdog token facts=%+v want=%+v", state.WatchdogTokenUsage, want)
	}
}

func TestTerminalVerdictWaitsForAuthenticatedAgentDown(t *testing.T) {
	state, _ := activeToolState(t)
	state.PendingReview = &reviewRequest{ID: "review-1", Anchor: state.CurrentAnchor, Purpose: reviewPurposeWatchdog, AgentID: "review-child"}
	terminal := agentTerminalMetadata{UsageComplete: true}
	messages := agentMessages{
		Status: "completed", Terminal: &terminal,
		Messages: []agentMessage{terminalVerdictMessage(t, reviewerResult{Verdict: verdict{
			Decision: verdictContinue, Anchor: state.CurrentAnchor, Rationale: "provisional response",
		}})},
	}
	change, consumed, err := state.consumeReviewerMessages(messages)
	if err != nil || consumed || len(change.Actions) != 0 || state.PendingReview == nil || state.LastVerdict != nil || state.WatchdogTokenUsage != (agentTokenUsage{}) {
		t.Fatalf("verdict was applied before agent.down: change=%+v consumed=%v state=%+v err=%v", change, consumed, state, err)
	}
}

func TestReadOnlyScopeOrChangeFactsInvalidateReviewerResult(t *testing.T) {
	for _, name := range []string{"scope", "change"} {
		t.Run(name, func(t *testing.T) {
			state, _ := activeToolState(t)
			state.PendingReview = &reviewRequest{ID: "review-1", Anchor: state.CurrentAnchor, Purpose: reviewPurposeWatchdog}
			terminal := agentTerminalMetadata{Usage: agentTokenUsage{InputTokens: 4, OutputTokens: 2}, UsageComplete: true}
			observePendingTerminal(t, &state, terminal, "completed", func(facts *agentDownFacts, refs *[]string) {
				if name == "scope" {
					facts.Scope.Violations = []string{"write denied"}
					facts.Scope.ViolationsDigest = testFactDigest
					return
				}
				facts.Changes = &agentDownChanges{ForkTreeDigest: testFactDigest, ChangedPaths: []string{"changed.go"}, ChangedPathsDigest: testFactDigest}
				*refs = []string{"git:refs/sessions/review-child/tree@0123456789abcdef0123456789abcdef01234567"}
			})
			messages := agentMessages{
				Status: "completed", Terminal: &terminal,
				Messages: []agentMessage{terminalVerdictMessage(t, reviewerResult{Verdict: verdict{
					Decision: verdictContinue, Anchor: state.CurrentAnchor, Rationale: "ignore the scope fact",
				}})},
			}
			change, consumed, err := state.consumeReviewerMessages(messages)
			if err != nil || !consumed || state.LastVerdict != nil || state.PendingReview == nil || state.PendingReview.Attempt != 2 || len(change.Actions) != 1 || change.Actions[0].Kind != actionScheduleReviewRetry || !diagnosticsContain(state, "review facts rejected") {
				t.Fatalf("invalid read-only facts were allowed to authorize verdict: state=%+v consumed=%v err=%v", state, consumed, err)
			}
		})
	}
}

func TestMalformedCleanupMetadataCannotBecomeApplicationInput(t *testing.T) {
	complete := true
	facts := agentDownFacts{
		Schema:   agentDownFactsSchema,
		Child:    &agentDownChild{SessionID: "child", Status: "completed"},
		Budget:   &agentDownBudget{},
		Terminal: &agentDownTerminalMetadata{Usage: &agentTokenUsage{}, UsageComplete: &complete, Cleanup: &agentCleanupDiagnostic{Kind: "provider_close", Fingerprint: strings.Repeat("x", 71)}},
		Scope:    &agentDownScope{},
	}
	if err := facts.validate(); err == nil {
		t.Fatal("malformed cleanup metadata was accepted")
	}
}
