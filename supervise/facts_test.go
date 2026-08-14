package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestCrossRepoSessionTurnFactsFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/session-turn-facts-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	facts, err := decodeTurnCommittedFacts(raw)
	if err != nil {
		t.Fatalf("host fixture rejected by plugin decoder: %v", err)
	}
	state := testState(t, defaultConfig())
	events, err := state.deriveWorkerEvents(facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != eventTurnCompleted {
		t.Fatalf("host fixture policy events=%+v", events)
	}
	if events[0].TokenUsage != 150 || events[0].TokenBudget != 2000 || events[0].Anchor.TreeDigest != facts.Anchor.TreeDigest {
		t.Fatalf("host fixture reduced incorrectly: %+v", events[0])
	}
}

func TestMandatoryTurnCallbackRebindIsAtomicAndExactlyReplayable(t *testing.T) {
	state := testState(t, defaultConfig())
	facts := sampleTurnFacts(1)
	facts.Tools = []toolOutcomeFact{{
		ID: "failed-1", Tool: "release.publish", Class: "Exec", CallDigest: "call-1", ArgsDigest: "args-1",
		ResultDigest: "result-1", Outcome: "error", ErrorFingerprint: "denied", EvidenceRefs: []string{"evidence:tool"},
	}}
	raw, _ := json.Marshal(facts)
	digest := "sha256:" + digestString(string(raw))
	change, evidence, replay, err := state.applyTurnCommittedFacts(41, facts, digest)
	if err != nil || replay || state.TurnsSeen != 1 || state.LastTurnEventSequence != 41 || state.LastTurnFactsDigest != digest || len(change.Actions) != 1 || len(evidence) == 0 {
		t.Fatalf("first atomic turn fold: change=%+v evidence=%v replay=%v state=%+v err=%v", change, evidence, replay, state, err)
	}
	afterFirst, _ := json.Marshal(state)

	// Simulate callback timeout after worker.turn_observed committed but before
	// the event cursor ACK. The exact redelivery must be a no-op policy fold so
	// the caller can resume the already-durable review effect and ACK it.
	var rebound runState
	if err := decodeStrictBytes(afterFirst, &rebound); err != nil {
		t.Fatal(err)
	}
	change, evidence, replay, err = rebound.applyTurnCommittedFacts(41, facts, digest)
	if err != nil || !replay || len(change.Actions) != 0 || len(evidence) != 0 {
		t.Fatalf("exact turn redelivery: change=%+v evidence=%v replay=%v err=%v", change, evidence, replay, err)
	}
	afterReplay, _ := json.Marshal(rebound)
	if string(afterReplay) != string(afterFirst) || rebound.TurnsSeen != 1 {
		t.Fatalf("redelivery double-applied policy\nfirst=%s\nreplay=%s", afterFirst, afterReplay)
	}
	if _, _, _, err := rebound.applyTurnCommittedFacts(41, facts, "sha256:"+strings.Repeat("f", 64)); err == nil {
		t.Fatal("same broker sequence with different authenticated facts was accepted")
	}
	if _, _, _, err := rebound.applyTurnCommittedFacts(40, facts, digest); err == nil {
		t.Fatal("turn broker sequence rollback was accepted")
	}

	// If the journal append did not commit, refolding the pre-turn snapshot and
	// applying the same facts yields the same durable state as the first try.
	prior := testState(t, defaultConfig())
	if _, _, replay, err := prior.applyTurnCommittedFacts(41, facts, digest); err != nil || replay {
		t.Fatalf("pre-commit callback retry failed: replay=%v err=%v", replay, err)
	}
	priorRaw, _ := json.Marshal(prior)
	if string(priorRaw) != string(afterFirst) {
		t.Fatalf("pre-commit retry diverged\nfirst=%s\nretry=%s", afterFirst, priorRaw)
	}
}

func sampleTurnFacts(sequence uint64) turnCommittedFacts {
	tree := "tree-" + string(rune('a'+sequence))
	return turnCommittedFacts{
		Schema:    turnFactsSchema,
		Anchor:    hostTurnAnchor{SessionSequence: sequence, TurnRef: "turn-" + string(rune('a'+sequence)), TreeDigest: tree},
		Provider:  &providerTokenFacts{InputTokens: 100, OutputTokens: 25, RunBudgetTokens: 1000},
		Assistant: &assistantMessageFact{MessageRef: "message-" + string(rune('a'+sequence)), Digest: "assistant-digest", Excerpt: "work continues"},
	}
}

func eventKinds(events []workerEvent) string {
	values := make([]string, 0, len(events))
	for _, event := range events {
		values = append(values, string(event.Kind))
	}
	return strings.Join(values, ",")
}

func TestTurnFactsStrictVersionedDecoderRejectsPolicyFields(t *testing.T) {
	facts := sampleTurnFacts(1)
	raw, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeTurnCommittedFacts(raw); err != nil {
		t.Fatalf("valid facts rejected: %v", err)
	}
	withNativePolicy := strings.TrimSuffix(string(raw), "}") + `,"completed_steps":1}`
	if _, err := decodeTurnCommittedFacts([]byte(withNativePolicy)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("policy-derived native field was accepted: %v", err)
	}
	withCompletionPolicy := strings.TrimSuffix(string(raw), "}") + `,"completion_candidate":{"candidate_ref":"assistant:1"}}`
	if _, err := decodeTurnCommittedFacts([]byte(withCompletionPolicy)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("host-derived completion policy was accepted: %v", err)
	}
	facts.Schema = "stado.dev/session-turn-facts/v2"
	raw, _ = json.Marshal(facts)
	if _, err := decodeTurnCommittedFacts(raw); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unknown facts version was accepted: %v", err)
	}
	facts = sampleTurnFacts(1)
	facts.Provider = nil
	raw, _ = json.Marshal(facts)
	if _, err := decodeTurnCommittedFacts(raw); err == nil || !strings.Contains(err.Error(), "provider token facts") {
		t.Fatalf("missing required provider facts were accepted: %v", err)
	}
}

func TestGenericFactsBecomePluginPolicyEvents(t *testing.T) {
	cfg := defaultConfig()
	cfg.AllowedPathPrefixes = []string{"src", "go.mod"}
	state := testState(t, cfg)
	facts := sampleTurnFacts(1)
	facts.Tools = []toolOutcomeFact{{
		ID: "tool-1", Tool: "release.publish", Class: "Exec", CallDigest: "call-1",
		ArgsDigest: "args-1", ResultDigest: "result-1", Outcome: "error",
		ErrorFingerprint: "permission-denied", EvidenceRefs: []string{"e-tool", "e-shared"},
	}}
	facts.Verifications = []verificationFact{{ID: "verify-1", CommandDigest: "cmd-1", ResultDigest: "verify-result", Outcome: "fail", EvidenceRefs: []string{"e-verify", "e-shared"}}}
	facts.Tree = &treeDiffFact{BeforeDigest: "tree-old", AfterDigest: facts.Anchor.TreeDigest, DiffRef: "diff-1", DiffDigest: "diff-digest", ChangedPaths: []string{"src/main.go", "docs/drift.md"}, Bytes: 900, EvidenceRefs: []string{"e-diff"}}
	facts.Assistant.Excerpt = "I need to pivot and revise the plan."

	events, err := state.deriveWorkerEvents(facts)
	if err != nil {
		t.Fatal(err)
	}
	got := eventKinds(events)
	for _, want := range []string{"tool_outcome", "risk_boundary", "verification", "tree_changed", "pivot_requested", "turn_completed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("derived events %q omit %q", got, want)
		}
	}
	var tree, turn *workerEvent
	for i := range events {
		switch events[i].Kind {
		case eventTreeChanged:
			tree = &events[i]
		case eventTurnCompleted:
			turn = &events[i]
		}
	}
	if tree == nil || len(tree.OutOfScopePaths) != 1 || tree.OutOfScopePaths[0] != "docs/drift.md" {
		t.Fatalf("plugin did not derive contract scope policy: %#v", tree)
	}
	if turn == nil || turn.TokenUsage != 125 || turn.TokenBudget != 1000 || turn.EvidenceCount != 4 {
		t.Fatalf("generic token/evidence facts were not reduced correctly: %#v", turn)
	}
	if turn.Anchor.PlanVersion != 1 || turn.Anchor.TurnRef != facts.Anchor.TurnRef {
		t.Fatalf("plugin/host anchor composition lost identity: %#v", turn.Anchor)
	}
}

func TestProseFinalityCannotCreateCompletionCandidate(t *testing.T) {
	state := testState(t, defaultConfig())
	facts := sampleTurnFacts(1)
	facts.Assistant.Excerpt = "Everything is complete and ready to ship."
	events, err := state.deriveWorkerEvents(facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != eventTurnCompleted {
		t.Fatalf("prose finality became completion policy: %+v", events)
	}
	if state.PendingCompletion != nil {
		t.Fatalf("generic turn facts created durable completion state: %+v", state.PendingCompletion)
	}
}

func TestFourTurnStallIgnoresGenericActivityAndEvidence(t *testing.T) {
	state := testState(t, defaultConfig())
	var final transition
	for sequence := uint64(1); sequence <= 4; sequence++ {
		facts := sampleTurnFacts(sequence)
		facts.Tools = []toolOutcomeFact{{ID: "tool-" + facts.Anchor.TurnRef, Tool: "fs.read", Class: "NonMutating", CallDigest: "call-" + facts.Anchor.TurnRef, ArgsDigest: "args-" + facts.Anchor.TurnRef, ResultDigest: "result-" + facts.Anchor.TurnRef, Outcome: "success", EvidenceRefs: []string{"e-" + facts.Anchor.TurnRef}}}
		facts.Tree = &treeDiffFact{BeforeDigest: "before", AfterDigest: facts.Anchor.TreeDigest, DiffRef: "diff-" + facts.Anchor.TurnRef, DiffDigest: "digest-" + facts.Anchor.TurnRef, ChangedPaths: []string{"src/file.go"}, Bytes: int64(sequence * 10), EvidenceRefs: []string{"tree-e-" + facts.Anchor.TurnRef}}
		events, err := state.deriveWorkerEvents(facts)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			final, err = state.observe(event)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if state.PendingReview == nil || !strings.Contains(signalTypes(state.PendingReview.Signals), "no_criteria_progress") || len(final.Actions) == 0 {
		t.Fatalf("activity/evidence suppressed four-turn policy review: state=%#v transition=%#v", state.PendingReview, final)
	}
}

func TestGenericToolFactsDriveRetryPolicy(t *testing.T) {
	state := testState(t, defaultConfig())
	for sequence := uint64(1); sequence <= 2; sequence++ {
		facts := sampleTurnFacts(sequence)
		facts.Tools = []toolOutcomeFact{{
			ID: "failed-" + facts.Anchor.TurnRef, Tool: "fs.read", Class: "NonMutating",
			CallDigest: "call-" + facts.Anchor.TurnRef, ArgsDigest: "same-args",
			ResultDigest: "result-" + facts.Anchor.TurnRef, Outcome: "error",
			ErrorFingerprint: "not-found", EvidenceRefs: []string{"e-" + facts.Anchor.TurnRef},
		}}
		events, err := state.deriveWorkerEvents(facts)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if _, err := state.observe(event); err != nil {
				t.Fatal(err)
			}
		}
	}
	if state.PendingReview == nil || !strings.Contains(signalTypes(state.PendingReview.Signals), "repeated_failure") {
		t.Fatalf("generic failure facts did not drive plugin retry policy: %#v", state.PendingReview)
	}
	if got := state.PendingReview.Signals[0].EvidenceRefs; len(got) == 0 || got[0] != "e-turn-b" {
		t.Fatalf("review signal did not retain host evidence refs: %#v", got)
	}
}

func TestPluginOwnedCriteriaProgressBreaksStallWindow(t *testing.T) {
	state := testState(t, defaultConfig())
	for sequence := uint64(1); sequence <= 4; sequence++ {
		if sequence == 4 {
			state.CompletedSteps++
			state.CurrentAnchor.ActiveStep = "next-step"
		}
		events, err := state.deriveWorkerEvents(sampleTurnFacts(sequence))
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if _, err := state.observe(event); err != nil {
				t.Fatal(err)
			}
		}
	}
	if state.PendingReview != nil {
		t.Fatalf("plugin-owned criteria progress did not break stall window: %#v", state.PendingReview)
	}
}

func TestTurnFactsBoundsAndTokenSaturation(t *testing.T) {
	facts := sampleTurnFacts(1)
	facts.Provider.InputTokens = ^uint64(0)
	facts.Provider.OutputTokens = 1
	state := testState(t, defaultConfig())
	events, err := state.deriveWorkerEvents(facts)
	if err != nil {
		t.Fatal(err)
	}
	if got := events[len(events)-1].TokenUsage; got != ^uint64(0) {
		t.Fatalf("token sum wrapped: %d", got)
	}
	facts.Assistant.Excerpt = strings.Repeat("x", 4097)
	if _, err := state.deriveWorkerEvents(facts); err == nil {
		t.Fatal("oversized assistant excerpt was accepted")
	}
}

func TestWorkerTokenBudgetUsesDurablePerTurnAccumulation(t *testing.T) {
	state := testState(t, defaultConfig())
	for sequence := uint64(1); sequence <= 2; sequence++ {
		facts := sampleTurnFacts(sequence)
		facts.Provider.InputTokens = 300
		facts.Provider.OutputTokens = 100
		facts.Provider.RunBudgetTokens = 1000
		events, err := state.deriveWorkerEvents(facts)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if _, err := state.observe(event); err != nil {
				t.Fatal(err)
			}
		}
	}
	if state.CumulativeWorkerTokens != 800 {
		t.Fatalf("worker token ledger = %d, want 800", state.CumulativeWorkerTokens)
	}
	if state.PendingReview == nil || !strings.Contains(signalTypes(state.PendingReview.Signals), "budget_burn") {
		t.Fatalf("cumulative 80%% burn did not trigger review: %#v", state.PendingReview)
	}
}

func TestCurrentHostProviderShapeUsesPerTurnTokensAndRunBudget(t *testing.T) {
	raw := []byte(`{"schema":"stado.dev/session-turn-facts/v1","anchor":{"session_sequence":4294967297,"turn_ref":"git:refs/stado/trees/test@0123456789abcdef#turn-1-iteration-1","tree_digest":"0123456789abcdef"},"provider_tokens":{"input_tokens":300,"output_tokens":100,"cached_tokens":25,"budget_tokens":1000},"assistant":{"message_ref":"message-1","digest":"sha256:assistant"}}`)
	facts, err := decodeTurnCommittedFacts(raw)
	if err != nil {
		t.Fatalf("current host fact shape rejected: %v", err)
	}
	if facts.Provider == nil || facts.Provider.InputTokens != 300 || facts.Provider.OutputTokens != 100 || facts.Provider.CachedTokens != 25 || facts.Provider.RunBudgetTokens != 1000 {
		t.Fatalf("provider facts decoded incorrectly: %#v", facts.Provider)
	}
	state := testState(t, defaultConfig())
	events, err := state.deriveWorkerEvents(facts)
	if err != nil {
		t.Fatal(err)
	}
	turn := events[len(events)-1]
	if turn.TokenUsage != 400 || turn.TokenBudget != 1000 {
		t.Fatalf("per-turn facts produced usage=%d budget=%d", turn.TokenUsage, turn.TokenBudget)
	}
}
