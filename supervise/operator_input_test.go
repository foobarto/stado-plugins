package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

const testApplicationCanonical = "github.com/foobarto/stado-plugins/supervise@v0.1.1"

func inputFact(runID, inputID string, ordinal uint64, text string) operatorInputQueuedFact {
	return operatorInputQueuedFact{Schema: operatorInputSchema, InputID: inputID, RunID: runID, Version: 1, Ordinal: ordinal, Text: text, Digest: digestOperatorInput(text)}
}

func inputRecord(route operatorInputRouteState, text, status string, version uint64) operatorInputRecord {
	now := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	record := operatorInputRecord{
		ID: route.InputID, SessionID: "session-1", Generation: 2, PluginID: route.PluginID,
		RunID: route.RunID, Ordinal: route.Ordinal, Version: version, WALSequence: 70 + route.Ordinal,
		Text: text, Digest: route.Digest, Status: status, ReviewID: route.ReviewID,
		CreatedAt: now, UpdatedAt: now.Add(time.Second),
	}
	if status == "deferred" {
		record.TaskID = "task-" + route.InputID
	}
	return record
}

func beginClaimedInput(t *testing.T, state *runState, inputID string, ordinal uint64, text string) (*operatorInputRouteState, operatorInputRecord) {
	t.Helper()
	route, created, err := state.beginOperatorInputReview(testApplicationCanonical, inputFact(state.RunID, inputID, ordinal, text))
	if err != nil || !created || route.Phase != operatorInputPhaseClaiming {
		t.Fatalf("begin review=%+v created=%v err=%v", route, created, err)
	}
	claim := inputRecord(*route, text, operatorInputPhaseReviewing, 2)
	if err := state.acceptOperatorInputClaim(lifecycleIdentity{SessionID: "session-1", SessionGeneration: 2}, route, claim); err != nil {
		t.Fatal(err)
	}
	return route, claim
}

func TestOperatorInputFactIsStrictVersionedAndImmutable(t *testing.T) {
	fact := inputFact("run-test", "input-1", 1, "Please verify the implementation criterion.")
	raw, _ := json.Marshal(fact)
	if got, err := decodeOperatorInputQueued(raw); err != nil || got != fact {
		t.Fatalf("valid fact=%+v err=%v", got, err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"unknown field": func(value map[string]any) { value["policy_conclusion"] = "related" },
		"wrong version": func(value map[string]any) { value["version"] = float64(2) },
		"wrong digest":  func(value map[string]any) { value["digest"] = "sha256:" + strings.Repeat("0", 64) },
		"control byte": func(value map[string]any) {
			value["text"] = "bad\u0000text"
			value["digest"] = digestOperatorInput("bad\x00text")
		},
	} {
		t.Run(name, func(t *testing.T) {
			var value map[string]any
			_ = json.Unmarshal(raw, &value)
			mutate(value)
			changed, _ := json.Marshal(value)
			if _, err := decodeOperatorInputQueued(changed); err == nil {
				t.Fatalf("accepted %s: %s", name, changed)
			}
		})
	}
}

func TestOperatorInputClaimIntentIsDurableAndHasNoLexicalConclusion(t *testing.T) {
	state, _ := activeToolState(t)
	fact := inputFact(state.RunID, "input-1", 1, "After this, also verify the current implementation.")
	route, created, err := state.beginOperatorInputReview(testApplicationCanonical, fact)
	if err != nil || !created {
		t.Fatal(err)
	}
	if route.Disposition != "" || route.Label != "" || route.Rationale != "" || route.Phase != operatorInputPhaseClaiming {
		t.Fatalf("input was classified before a fresh review: %+v", route)
	}
	wantClaim := operatorInputClaimRequest(*route)
	raw, _ := json.Marshal(state)
	var rebound runState
	if err := decodeStrictBytes(raw, &rebound); err != nil {
		t.Fatal(err)
	}
	recovered := rebound.operatorInputRoute(fact.InputID)
	if recovered == nil || !mapsEqualJSON(operatorInputClaimRequest(*recovered), wantClaim) || recovered.ReviewAnchor != state.CurrentAnchor {
		t.Fatalf("rebind changed stable fresh-review intent: %+v", recovered)
	}
	if replay, created, err := rebound.beginOperatorInputReview(testApplicationCanonical, fact); err != nil || created || replay.ReviewID != route.ReviewID {
		t.Fatalf("redelivery was not idempotent: route=%+v created=%v err=%v", replay, created, err)
	}
	changed := fact
	changed.Digest = digestOperatorInput("changed")
	if _, _, err := rebound.beginOperatorInputReview(testApplicationCanonical, changed); err == nil {
		t.Fatal("redelivery changed immutable input identity")
	}
	source, err := os.ReadFile("operator_input.go")
	if err != nil || strings.Contains(string(source), "classifyOperatorInput") || strings.Contains(string(source), "qualityTerms") {
		t.Fatal("runtime retained a lexical operator-input classifier")
	}
}

func mapsEqualJSON(first, second map[string]any) bool {
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	return string(a) == string(b)
}

func TestClaimAndRouteStrictlyPreserveOriginalReviewAndScope(t *testing.T) {
	state, _ := activeToolState(t)
	route, claim := beginClaimedInput(t, &state, "input-1", 1, "Please verify implementation.")
	if route.ExpectedVersion != 2 || route.Phase != operatorInputPhaseReviewing || claim.ReviewID != route.ReviewID {
		t.Fatalf("claim not folded exactly: %+v %+v", route, claim)
	}
	for _, mutate := range []func(*operatorInputRecord){
		func(value *operatorInputRecord) { value.Text = "replacement" },
		func(value *operatorInputRecord) { value.RunID = "foreign" },
		func(value *operatorInputRecord) { value.ReviewID = "other-review" },
		func(value *operatorInputRecord) { value.Version++ },
	} {
		candidate := claim
		mutate(&candidate)
		copyState := state
		copyState.OperatorInputRoutes = cloneOperatorInputRoutes(state.OperatorInputRoutes)
		copyRoute := copyState.operatorInputRoute(route.InputID)
		copyRoute.Phase, copyRoute.ExpectedVersion = operatorInputPhaseClaiming, 1
		if err := copyState.acceptOperatorInputClaim(lifecycleIdentity{SessionID: "session-1", SessionGeneration: 2}, copyRoute, candidate); err == nil {
			t.Fatalf("accepted changed claim: %+v", candidate)
		}
	}
	if err := state.applyOperatorInputClassification(route, operatorInputDispositionGive, "related", "fresh reviewer found direct active-step relevance"); err != nil {
		t.Fatal(err)
	}
	ready := inputRecord(*route, claim.Text, "ready", 3)
	ready.Label, ready.Rationale = route.Label, route.Rationale
	if err := state.acceptOperatorInputRoute(lifecycleIdentity{SessionID: "session-1", SessionGeneration: 2}, route, ready); err != nil {
		t.Fatal(err)
	}
	if route.Phase != operatorInputPhaseRouted || route.RoutedVersion != 3 || len(state.DeferredInputs) != 0 {
		t.Fatalf("ready route not folded exactly: %+v", route)
	}
}

func TestFreshReviewerResultIsStrictAndUncertaintyDefers(t *testing.T) {
	state, _ := activeToolState(t)
	route, _ := beginClaimedInput(t, &state, "input-1", 1, "Please check whether this belongs here.")
	valid := operatorInputReviewerResult{Classification: operatorInputClassification{
		ReviewID: route.ReviewID, InputID: route.InputID, Anchor: route.ReviewAnchor,
		Disposition: operatorInputDispositionGive, Label: "related follow-up", Rationale: "directly tests the active step",
	}}
	raw, _ := json.Marshal(valid)
	if _, err := decodeOperatorInputReviewerResult(raw, route); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	value["host_authority"] = true
	unknown, _ := json.Marshal(value)
	if _, err := decodeOperatorInputReviewerResult(unknown, route); err == nil {
		t.Fatal("accepted reviewer authority-shaped field")
	}
	if err := state.conservativelyDeferOperatorInput(route, "reviewer failed or remained uncertain"); err != nil || route.Disposition != operatorInputDispositionLater || !strings.Contains(route.Rationale, "uncertain") {
		t.Fatalf("uncertainty did not defer: route=%+v err=%v", route, err)
	}
}

func TestValidOperatorInputClassificationSurvivesCleanupDiagnostic(t *testing.T) {
	state, _ := activeToolState(t)
	route, _ := beginClaimedInput(t, &state, "input-1", 1, "Please verify this exact step.")
	cleanup := &agentCleanupDiagnostic{Kind: "provider_close", Fingerprint: testFactDigest}
	terminal := agentTerminalMetadata{Usage: agentTokenUsage{InputTokens: 5, OutputTokens: 2}, UsageComplete: true, Cleanup: cleanup}
	route.Terminal = &reviewTerminalObservation{
		ReviewID: route.ReviewID, Purpose: reviewPurposeOperatorInput,
		Child: agentDownChild{AgentID: "child", SessionID: "child", Status: "completed"}, Terminal: terminal,
	}
	result := operatorInputReviewerResult{Classification: operatorInputClassification{
		ReviewID: route.ReviewID, InputID: route.InputID, Anchor: route.ReviewAnchor,
		Disposition: operatorInputDispositionGive, Label: "current follow-up", Rationale: "directly concerns the active step",
	}}
	raw, _ := json.Marshal(result)
	consumed, err := state.consumeOperatorInputReviewerMessages(route, agentMessages{
		Status: "completed", Terminal: &terminal, Messages: []agentMessage{{Role: "assistant", Content: string(raw)}},
	})
	if err != nil || !consumed || route.Phase != operatorInputPhaseRouting || route.Disposition != operatorInputDispositionGive {
		t.Fatalf("cleanup erased semantic classification: consumed=%v route=%+v err=%v", consumed, route, err)
	}
}

func TestOperatorInputReviewerSpawnIsFreshPinnedAndIdempotent(t *testing.T) {
	state, contract := activeToolState(t)
	route, claim := beginClaimedInput(t, &state, "input-1", 1, "Please verify this exact active step.")
	request, err := buildOperatorInputReviewSpawnRequest(contract, state.Config, route, claim.Text)
	if err != nil {
		t.Fatal(err)
	}
	if !request.Async || !request.Ephemeral || request.Role != "explorer" || request.Mode != "read_only" || request.ToolProfile != "research" || request.Source == nil || request.Source.At != route.ReviewAnchor.TurnRef || request.IdempotencyKey != route.SpawnKey || request.Ownership != operatorInputReviewOwnership(state.RunID, route) {
		t.Fatalf("fresh review request shape=%+v", request)
	}
	if strings.Contains(request.Prompt, "lexical") || !strings.Contains(request.Prompt, claim.Text) || !strings.Contains(request.Prompt, "Use defer") {
		t.Fatalf("fresh review prompt=%s", request.Prompt)
	}
	raw, _ := json.Marshal(state)
	var rebound runState
	_ = decodeStrictBytes(raw, &rebound)
	replayed, err := buildOperatorInputReviewSpawnRequest(contract, rebound.Config, rebound.operatorInputRoute(route.InputID), claim.Text)
	if err != nil || replayed.IdempotencyKey != request.IdempotencyKey || replayed.Prompt != request.Prompt {
		t.Fatalf("rebind changed spawn request: replay=%+v err=%v", replayed, err)
	}
}

func TestOperatorInputReviewSpawnRebindReplacesDeadProcessLocalChild(t *testing.T) {
	state, _ := activeToolState(t)
	route, _ := beginClaimedInput(t, &state, "input-1", 1, "Please verify this input.")
	if changed, err := state.acceptOperatorInputReviewSpawn(route, "old-process-child"); err != nil || !changed {
		t.Fatalf("initial child: changed=%v err=%v", changed, err)
	}
	route.Terminal = &reviewTerminalObservation{BrokerSequence: 7, ReviewID: route.ReviewID, Child: agentDownChild{AgentID: "old-process-child", SessionID: "old-process-child"}}
	route.AgentOffset = 9
	route.RetryAt = time.Now().Add(-time.Second)
	if changed, err := state.acceptOperatorInputReviewSpawn(route, "replacement-child"); err != nil || !changed {
		t.Fatalf("replacement child: changed=%v err=%v", changed, err)
	}
	if route.AgentID != "replacement-child" || route.AgentOffset != 0 || route.Terminal != nil || !route.RetryAt.IsZero() || route.TokenBudget != state.Config.WatchdogTokenBudget {
		t.Fatalf("replacement did not clear old process-local state: %+v", route)
	}
	if changed, err := state.acceptOperatorInputReviewSpawn(route, "replacement-child"); err != nil || changed {
		t.Fatalf("same-process replay was not idempotent: changed=%v err=%v", changed, err)
	}
}

func TestOperatorInputReviewTransportFailuresBackOffThenDefer(t *testing.T) {
	state, _ := activeToolState(t)
	state.Config.EventReviewRetries = 3
	route, _ := beginClaimedInput(t, &state, "input-1", 1, "Please review this input.")
	now := route.ReviewDeadline.Add(-time.Minute)
	for attempt := 1; attempt <= 3; attempt++ {
		exhausted, err := state.recordOperatorInputReviewFailure(route, "agent transport unavailable", now)
		if err != nil {
			t.Fatal(err)
		}
		if attempt < 3 && (exhausted || route.Phase != operatorInputPhaseReviewing || !route.RetryAt.After(now)) {
			t.Fatalf("attempt %d did not stage durable retry: %+v", attempt, route)
		}
		if attempt == 3 && (!exhausted || route.Phase != operatorInputPhaseRouting || route.Disposition != operatorInputDispositionLater) {
			t.Fatalf("exhausted review did not conservatively defer: %+v", route)
		}
		raw, _ := json.Marshal(state)
		var rebound runState
		if err := decodeStrictBytes(raw, &rebound); err != nil {
			t.Fatal(err)
		}
		state = rebound
		route = state.operatorInputRoute("input-1")
		now = now.Add(time.Second)
	}
}

func TestFirstSpawnFailureSchedulesRetryWithoutChildID(t *testing.T) {
	state, _ := activeToolState(t)
	route, _ := beginClaimedInput(t, &state, "input-1", 1, "Please review this input.")
	now := route.ReviewDeadline.Add(-time.Minute)
	exhausted, err := state.recordOperatorInputReviewFailure(route, "spawn callback unavailable", now)
	if err != nil || exhausted || route.AgentID != "" || !route.RetryAt.After(now) {
		t.Fatalf("first spawn failure did not stage retry: route=%+v exhausted=%v err=%v", route, exhausted, err)
	}
	due, err := operatorInputPollDue(route, now)
	if err != nil || !due.Equal(route.RetryAt) {
		t.Fatalf("agent-less retry has no durable timer due: due=%s retry=%s err=%v", due, route.RetryAt, err)
	}
	if changed, err := state.acceptOperatorInputReviewSpawn(route, "recovered-child"); err != nil || !changed || route.AgentID != "recovered-child" || !route.RetryAt.IsZero() {
		t.Fatalf("spawn retry did not recover: changed=%v route=%+v err=%v", changed, route, err)
	}
}

func TestMultipleInputsPreserveOrdinalDeferredContinuationSet(t *testing.T) {
	state, _ := activeToolState(t)
	for ordinal := uint64(1); ordinal <= 8; ordinal++ {
		text := fmt.Sprintf("Separate follow-up %d", ordinal)
		route, claim := beginClaimedInput(t, &state, fmt.Sprintf("input-%02d", ordinal), ordinal, text)
		if err := state.applyOperatorInputClassification(route, operatorInputDispositionLater, "later", "fresh reviewer deferred unrelated work"); err != nil {
			t.Fatal(err)
		}
		deferred := inputRecord(*route, claim.Text, "deferred", 3)
		deferred.Label, deferred.Rationale = route.Label, route.Rationale
		if err := state.acceptOperatorInputRoute(lifecycleIdentity{SessionID: "session-1", SessionGeneration: 2}, route, deferred); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := state.continuationInputIDs()
	if err != nil || len(ids) != 8 || ids[0] != "input-01" || ids[7] != "input-08" {
		t.Fatalf("continuation=%v err=%v", ids, err)
	}
	state.DeferredInputs[0], state.DeferredInputs[1] = state.DeferredInputs[1], state.DeferredInputs[0]
	if reordered, err := state.continuationInputIDs(); err != nil || !slices.Equal(reordered, ids) {
		t.Fatalf("application order changed after journal reorder: %v %v", reordered, err)
	}
	state.DeferredInputs[1].InputID = state.DeferredInputs[0].InputID
	if _, err := state.continuationInputIDs(); err == nil {
		t.Fatal("duplicate continuation input accepted")
	}
}

func TestOperatorInputReviewLedgerBackpressuresWithoutDroppingOriginal(t *testing.T) {
	state, _ := activeToolState(t)
	for ordinal := uint64(1); ordinal <= maxOperatorInputRoutes; ordinal++ {
		route, created, err := state.beginOperatorInputReview(testApplicationCanonical, inputFact(state.RunID, fmt.Sprintf("input-%02d", ordinal), ordinal, "Pending review"))
		if err != nil || !created || route.Phase != operatorInputPhaseClaiming {
			t.Fatalf("review %d: route=%+v created=%v err=%v", ordinal, route, created, err)
		}
	}
	if _, _, err := state.beginOperatorInputReview(testApplicationCanonical, inputFact(state.RunID, "input-17", 17, "Must remain broker queued")); err == nil || !strings.Contains(err.Error(), "backpressured") {
		t.Fatalf("active review bound silently dropped input: %v", err)
	}
	if state.OperatorInputOrdinal != 16 || len(state.OperatorInputRoutes) != 16 {
		t.Fatalf("backpressure mutated durable order: ordinal=%d routes=%d", state.OperatorInputOrdinal, len(state.OperatorInputRoutes))
	}
}

func TestDeferredInputOverflowPausesForTerminalRecoveryWithoutReclassification(t *testing.T) {
	state, _ := activeToolState(t)
	for ordinal := uint64(1); ordinal <= maxDeferredInputs; ordinal++ {
		state.DeferredInputs = append(state.DeferredInputs, deferredInputState{
			InputID: fmt.Sprintf("existing-%02d", ordinal), RunID: state.RunID,
			Ordinal: ordinal, Digest: digestOperatorInput("existing"), TaskID: fmt.Sprintf("task-%02d", ordinal),
		})
	}
	state.OperatorInputOrdinal = maxDeferredInputs
	text := "Preserve this unrelated request after the continuation set is full."
	route, created, err := state.beginOperatorInputReview(testApplicationCanonical, inputFact(state.RunID, "overflow-input", maxDeferredInputs+1, text))
	if err != nil || !created {
		t.Fatal(err)
	}
	claim := inputRecord(*route, text, operatorInputPhaseReviewing, 2)
	if err := state.acceptOperatorInputClaim(lifecycleIdentity{SessionID: "session-1", SessionGeneration: 2}, route, claim); err != nil {
		t.Fatal(err)
	}
	if err := state.conservativelyDeferOperatorInput(route, "fresh reviewer classified this as unrelated"); err != nil {
		t.Fatal(err)
	}
	if route.Phase != operatorInputPhaseOverflow || route.Disposition != operatorInputDispositionLater || route.Digest != digestOperatorInput(text) || len(state.DeferredInputs) != maxDeferredInputs {
		t.Fatalf("overflow dropped or reclassified the original: route=%+v deferred=%d", route, len(state.DeferredInputs))
	}
	if ids, err := state.continuationInputIDs(); err == nil || ids != nil || !strings.Contains(err.Error(), "still pending") {
		t.Fatalf("overflow was silently omitted from completion: ids=%v err=%v", ids, err)
	}
	raw, _ := json.Marshal(state)
	var rebound runState
	if err := decodeStrictBytes(raw, &rebound); err != nil || rebound.operatorInputRoute(route.InputID).Phase != operatorInputPhaseOverflow {
		t.Fatalf("overflow pause posture was not durable: %+v err=%v", rebound, err)
	}
	rebound.WorkerRunStatus = workerRunInterrupted
	if err := rebound.recoverOperatorInputAfterTerminal(rebound.operatorInputRoute(route.InputID)); err != nil || rebound.operatorInputRoute(route.InputID).Phase != operatorInputPhaseRecovered {
		t.Fatalf("terminal recovery did not preserve overflow original: route=%+v err=%v", rebound.operatorInputRoute(route.InputID), err)
	}
	if ids, err := rebound.continuationInputIDs(); err != nil || len(ids) != maxDeferredInputs {
		t.Fatalf("overflow polluted or lost the existing continuation set: ids=%d err=%v", len(ids), err)
	}
}

func TestQueuedInputDurablyInvalidatesCompletionBeforeCleanup(t *testing.T) {
	state, _ := activeToolState(t)
	state.PendingCompletion = &completionCandidateState{Anchor: state.CurrentAnchor, Stage: reviewPurposeVerifier}
	state.PendingReview = &reviewRequest{ID: "verifier", Purpose: reviewPurposeVerifier, AgentID: "child-verifier", Anchor: state.CurrentAnchor, Attempt: 1}
	state.Hold = &holdState{ID: "hold-1", Version: 2, Reason: "completion", LeaseUntil: time.Now().Add(time.Minute)}
	route, created, err := state.beginOperatorInputReview(testApplicationCanonical, inputFact(state.RunID, "input-1", 1, "One more completion concern."))
	if err != nil || !created {
		t.Fatal(err)
	}
	if state.PendingCompletion != nil || state.PendingReview != nil || state.Completed || route.ObsoleteAgentID != "child-verifier" || !route.ReleaseHold || state.Hold == nil {
		t.Fatalf("completion was not invalidated before effects: state=%+v route=%+v", state, route)
	}
	raw, _ := json.Marshal(state)
	var rebound runState
	if err := decodeStrictBytes(raw, &rebound); err != nil || rebound.operatorInputRoute("input-1").ObsoleteAgentID != "child-verifier" || !rebound.operatorInputRoute("input-1").ReleaseHold {
		t.Fatalf("invalidation cleanup intent was not durable: %+v err=%v", rebound, err)
	}
	state.CompletionHandedOff = true
	state.Completed = true
	if _, _, err := state.beginOperatorInputReview(testApplicationCanonical, inputFact(state.RunID, "input-2", 2, "impossible race")); err == nil {
		t.Fatal("broker-acknowledged terminal completion was reversed")
	}
}

func TestReviewingInputBlocksCompletionHandoff(t *testing.T) {
	state, _ := activeToolState(t)
	beginClaimedInput(t, &state, "input-1", 1, "One more check.")
	state.Completed = true
	state.CompletedAnchor = &state.CurrentAnchor
	state.CompletionVerdict = &verdict{Decision: verdictApprove, Anchor: state.CurrentAnchor, Rationale: "stale approval"}
	if _, err := prepareCompletionHandoff(state); err == nil || !strings.Contains(err.Error(), "still pending") {
		t.Fatalf("reviewing input did not block stale completion: %v", err)
	}
}

func TestReviewingProjectionRecoveryAndTerminalRecoveryAreExact(t *testing.T) {
	state, _ := activeToolState(t)
	route, claim := beginClaimedInput(t, &state, "input-1", 1, "Please verify.")
	projection, _ := json.Marshal(map[string]any{"reviewing_inputs": []operatorInputRecord{claim}, "future": true})
	items, err := decodeReviewingOperatorInputs(projection)
	if err != nil || len(items) != 1 || items[0].ReviewID != route.ReviewID {
		t.Fatalf("reviewing projection=%+v err=%v", items, err)
	}
	changed := claim
	changed.ReviewID = "foreign-review"
	bad, _ := json.Marshal(map[string]any{"reviewing_inputs": []operatorInputRecord{changed}})
	decoded, err := decodeReviewingOperatorInputs(bad)
	if err != nil || validateOperatorInputIdentity(lifecycleIdentity{SessionID: "session-1", SessionGeneration: 2}, route, decoded[0]) == nil {
		t.Fatal("changed projection review identity was accepted")
	}
	state.WorkerRunStatus = workerRunInterrupted
	if err := state.recoverOperatorInputAfterTerminal(route); err != nil || route.Phase != operatorInputPhaseRecovered {
		t.Fatalf("terminal recovery=%+v err=%v", route, err)
	}
	if ids, err := state.continuationInputIDs(); err != nil || len(ids) != 0 {
		t.Fatalf("terminal-recovered original polluted deferred continuation: %v %v", ids, err)
	}
}

func TestTerminalRunAcknowledgesLateQueuedFactWithoutReview(t *testing.T) {
	state, _ := activeToolState(t)
	state.Cancelled = true
	state.WorkerRunStatus = workerRunCancelled
	route, created, err := state.beginOperatorInputReview(testApplicationCanonical, inputFact(state.RunID, "input-1", 1, "Captured before cancellation."))
	if err != nil || !created || route.Phase != operatorInputPhaseRecovered || route.ReviewID == "" || route.AgentID != "" || route.Disposition != "" {
		t.Fatalf("terminal queued recovery created a reviewer or failed: route=%+v created=%v err=%v", route, created, err)
	}
	if _, err := state.continuationInputIDs(); err != nil {
		t.Fatalf("terminal native recovery left application input pending: %v", err)
	}
}
