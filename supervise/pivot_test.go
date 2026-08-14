package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func planPivot(t *testing.T, state *runState, contract supervisionContract, approval string) requestPivotArgs {
	t.Helper()
	state.Config.PivotApproval, contract.Config.PivotApproval = approval, approval
	replacement := baselineFromContract(contract)
	replacement.Plan = []baselineStep{
		{ID: "alternate", Title: "Use the bounded alternate", DoneWhen: "alternate evidence exists"},
		{ID: "verify", Title: "Verify the alternate", DoneWhen: "verification passes"},
	}
	return requestPivotArgs{IdempotencyKey: "pivot-plan", Anchor: state.CurrentAnchor, Rationale: "current evidence invalidated the tactic", Replacement: replacement}
}

func TestPivotClassificationIsComputedFromExactStructuredReplacement(t *testing.T) {
	_, contract := activeToolState(t)
	plan := baselineFromContract(contract)
	plan.Plan[0].Title = "different tactic"
	classification, digest, err := classifyPivotReplacement(contract, plan)
	if err != nil || classification != pivotClassificationPlanOnly || digest == "" {
		t.Fatalf("plan classification=%q digest=%q err=%v", classification, digest, err)
	}
	contractChange := cloneBaseline(plan)
	contractChange.AcceptanceCriteria[0] = "different acceptance contract"
	classification, _, err = classifyPivotReplacement(contract, contractChange)
	if err != nil || classification != pivotClassificationContract {
		t.Fatalf("contract classification=%q err=%v", classification, err)
	}
	if _, _, err := classifyPivotReplacement(contract, baselineFromContract(contract)); err == nil {
		t.Fatal("no-op replacement was accepted")
	}
}

func TestOnlyConfiguredPlanOnlyPivotCanBeWatchdogApproved(t *testing.T) {
	for _, test := range []struct {
		name, approval string
		contractChange bool
		wantStage      string
	}{
		{"watchdog plan", "watchdog", false, pivotStageEditIntent},
		{"user plan", "user", false, pivotStageAwaitingUser},
		{"watchdog contract", "watchdog", true, pivotStageAwaitingUser},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, contract := activeToolState(t)
			args := planPivot(t, &state, contract, test.approval)
			contract.Config = state.Config
			if test.contractChange {
				args.Replacement.AcceptanceCriteria[0] = "new user-facing result"
			}
			change, _, err := state.applyRequestPivot(contract, args)
			if err != nil || len(change.Actions) != 2 || state.Hold == nil {
				t.Fatalf("request change=%+v state=%+v err=%v", change, state, err)
			}
			result := reviewerResult{Verdict: verdict{Decision: verdictApprove, Anchor: args.Anchor, Rationale: "exact replacement is sound", EvidenceRefs: []string{"review:pivot"}}}
			if _, err := state.applyReviewerResult(result, "cleanup:diagnostic"); err != nil {
				t.Fatal(err)
			}
			if state.PendingPivot == nil || state.PendingPivot.Stage != test.wantStage {
				t.Fatalf("pivot=%+v want stage %s", state.PendingPivot, test.wantStage)
			}
			if state.Hold == nil {
				t.Fatal("pivot quality hold was released before exact candidate selection")
			}
		})
	}
}

func TestStalePivotVerdictNeverSelectsReplacement(t *testing.T) {
	state, contract := activeToolState(t)
	args := planPivot(t, &state, contract, "watchdog")
	contract.Config = state.Config
	if _, _, err := state.applyRequestPivot(contract, args); err != nil {
		t.Fatal(err)
	}
	old := args.Anchor
	state.CurrentAnchor.SessionSequence++
	state.CurrentAnchor.TreeDigest = "new-tree"
	change, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictApprove, Anchor: old, Rationale: "old tree looked sound", EvidenceRefs: []string{"review:old"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingPivot == nil || state.PendingPivot.Stage != pivotStageReviewing || state.PendingReview == nil || state.PendingReview.Anchor != state.CurrentAnchor || state.PendingReview.Pivot == nil {
		t.Fatalf("stale pivot was not re-reviewed: state=%+v", state)
	}
	if len(change.Actions) != 1 || change.Actions[0].Kind != actionStartReview {
		t.Fatalf("stale pivot change=%+v", change)
	}
}

func TestApplyingExactPivotReanchorsAndPreservesOnlyStillValidEvidence(t *testing.T) {
	state, previous := activeToolState(t)
	state.Criteria[0].EvidenceRefs = []string{"evidence:still-valid"}
	state.Criteria[1].EvidenceRefs = []string{"evidence:invalidated"}
	state.CompletedSteps = 1
	state.VerificationGates = map[string]verificationGateState{"old": {CommandDigest: "old", Passed: true, Anchor: state.CurrentAnchor}}
	args := planPivot(t, &state, previous, "watchdog")
	args.Replacement.AcceptanceCriteria[1] = "replacement criterion"
	state.PendingPivot = &pivotCandidateState{Rationale: args.Rationale, Anchor: args.Anchor, Replacement: cloneBaseline(args.Replacement), ReplacementDigest: "sha256:" + digestString(mustBaselineJSON(args.Replacement)), Classification: pivotClassificationContract, Stage: pivotStageEditIntent, EditExpectedVersion: previous.ArtifactVersion}
	next := args.Replacement.contract(previous.RunID, state.Config)
	next.ArtifactID, next.ArtifactVersion = previous.ArtifactID, previous.ArtifactVersion+1
	oldWorkerObjective := state.WorkerObjective
	if err := state.applySelectedPivot(previous, next); err != nil {
		t.Fatal(err)
	}
	if state.ContractVersion != next.ArtifactVersion || state.CurrentAnchor.PlanVersion != 2 || state.CurrentAnchor.ActiveStep != next.Plan[0].ID || state.CompletedSteps != 0 {
		t.Fatalf("pivot did not re-anchor: %+v", state)
	}
	if !reflect.DeepEqual(state.Criteria[0].EvidenceRefs, []string{"evidence:still-valid"}) || len(state.Criteria[1].EvidenceRefs) != 0 {
		t.Fatalf("criterion evidence validity=%+v", state.Criteria)
	}
	if state.PendingPivot == nil || state.PendingPivot.Stage != pivotStageReleasePending || state.PendingCompletion != nil || state.VerificationGates != nil || state.WorkerObjective != oldWorkerObjective {
		t.Fatalf("stale policy state survived pivot: %+v", state)
	}
}

func TestRejectedPivotKeepsDurableReleaseMarker(t *testing.T) {
	state, contract := activeToolState(t)
	args := planPivot(t, &state, contract, "user")
	contract.Config = state.Config
	if _, _, err := state.applyRequestPivot(contract, args); err != nil {
		t.Fatal(err)
	}
	change, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictContinue, Anchor: args.Anchor, Rationale: "replacement is not justified"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingPivot == nil || state.PendingPivot.Stage != pivotStageCleanupPending || len(change.Actions) != 1 || change.Actions[0].Kind != actionReleaseHold {
		t.Fatalf("rejected pivot lacks replayable cleanup marker: state=%+v change=%+v", state, change)
	}
}

func TestExhaustedPivotReviewCannotLeaveAnUnresolvableCandidate(t *testing.T) {
	state, contract := activeToolState(t)
	args := planPivot(t, &state, contract, "watchdog")
	contract.Config = state.Config
	if _, _, err := state.applyRequestPivot(contract, args); err != nil {
		t.Fatal(err)
	}
	state.PendingReview.Attempt = uint64(state.Config.EventReviewRetries)
	change, err := state.applyReviewerFailure("failed")
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingReview != nil || state.PendingPivot != nil || len(change.Actions) != 1 || change.Actions[0].Kind != actionPause {
		t.Fatalf("exhausted pivot review wedged candidate: state=%+v change=%+v", state, change)
	}
}

func TestWatchdogPromptCarriesExactStructuredPivot(t *testing.T) {
	state, contract := activeToolState(t)
	args := planPivot(t, &state, contract, "watchdog")
	contract.Config = state.Config
	if _, _, err := state.applyRequestPivot(contract, args); err != nil {
		t.Fatal(err)
	}
	request, err := buildWatchdogSpawnRequest(contract, state.Config, state.PendingReview, state.Config.WatchdogTokenBudget)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(request.Prompt, `"pivot_proposal"`) || !strings.Contains(request.Prompt, state.PendingPivot.ReplacementDigest) || strings.Contains(request.Prompt, `"proposed_change"`) {
		t.Fatalf("watchdog prompt lost strict pivot proposal: %s", request.Prompt)
	}
}

func TestPivotEditIntentSurvivesStrictDurableRefold(t *testing.T) {
	state, contract := activeToolState(t)
	replacement := baselineFromContract(contract)
	replacement.Plan[0].Title = "replacement"
	state.PendingPivot = &pivotCandidateState{Rationale: "evidence changed", Anchor: state.CurrentAnchor, Replacement: replacement, ReplacementDigest: "sha256:" + digestString(mustBaselineJSON(replacement)), Classification: pivotClassificationPlanOnly, Stage: pivotStageEditIntent, EditExpectedVersion: contract.ArtifactVersion}
	raw, _ := json.Marshal(state)
	projection := projectionWithJournal(t, map[string]any{"kind": "pivot.edit_intent", "data": json.RawMessage(raw)})
	durable, ok, err := latestDurableSuperviseState(projection)
	if err != nil || !ok || durable.Run == nil || durable.Run.PendingPivot == nil || durable.Run.PendingPivot.EditExpectedVersion != contract.ArtifactVersion {
		t.Fatalf("durable pivot=%+v ok=%v err=%v", durable, ok, err)
	}
}

func TestPivotArtifactEditCommittedBeforeJournalReplaysDeterministically(t *testing.T) {
	state, previous := activeToolState(t)
	state.Criteria[0].EvidenceRefs = []string{"evidence:stable"}
	replacement := baselineFromContract(previous)
	replacement.Plan = []baselineStep{{ID: "replacement", Title: "Use the reviewed replacement", DoneWhen: "replacement evidence exists"}}
	replacement.AcceptanceCriteria[1] = "replacement criterion"
	state.PendingPivot = &pivotCandidateState{
		Rationale: "current tactic is disproved", Anchor: state.CurrentAnchor, Replacement: cloneBaseline(replacement),
		ReplacementDigest: "sha256:" + digestString(mustBaselineJSON(replacement)), Classification: pivotClassificationContract,
		Stage: pivotStageEditIntent, EditExpectedVersion: previous.ArtifactVersion,
	}
	durableBeforeEdit, _ := json.Marshal(state)
	next := replacement.contract(previous.RunID, state.Config)
	next.ArtifactID, next.ArtifactVersion = previous.ArtifactID, previous.ArtifactVersion+1

	first := state
	if err := first.applySelectedPivot(previous, next); err != nil {
		t.Fatal(err)
	}
	firstRaw, _ := json.Marshal(first)

	// The artifact CAS committed, but the callback timed out before pivot.applied
	// reached the journal. Refold the prior edit intent, query the deterministic
	// next version, and apply the same transition again.
	var rebound runState
	if err := decodeStrictBytes(durableBeforeEdit, &rebound); err != nil {
		t.Fatal(err)
	}
	if err := rebound.PendingPivot.validateForState(rebound, previous); err != nil {
		t.Fatalf("rebound edit intent lost its exact CAS: %v", err)
	}
	if err := rebound.applySelectedPivot(previous, next); err != nil {
		t.Fatal(err)
	}
	reboundRaw, _ := json.Marshal(rebound)
	if string(reboundRaw) != string(firstRaw) {
		t.Fatalf("artifact edit replay diverged\nfirst=%s\nrebound=%s", firstRaw, reboundRaw)
	}
	if !reflect.DeepEqual(rebound.Criteria[0].EvidenceRefs, []string{"evidence:stable"}) || len(rebound.Criteria[1].EvidenceRefs) != 0 || rebound.VerificationGates != nil {
		t.Fatalf("artifact replay changed evidence invalidation semantics: %+v", rebound)
	}
}

func TestTerminalDormancyRequiresExactCleanup(t *testing.T) {
	state := runState{Cancelled: true, CancellationCleaned: true, WorkerRunStatus: workerRunCancelled}
	if !state.dormant() {
		t.Fatal("clean cancellation was not dormant")
	}
	state.Hold = &holdState{ID: "hold"}
	if state.dormant() {
		t.Fatal("cancellation with hold became dormant")
	}
	state = runState{Completed: true, CompletionHandedOff: true}
	if !state.dormant() {
		t.Fatal("handed-off completion without hold was not dormant")
	}
	state.CompletionHandedOff = false
	if state.dormant() {
		t.Fatal("unhanded completion became dormant")
	}
}

func TestRunCancellationRetainsExactReviewerAcrossCrashAndCannotBecomeDormantEarly(t *testing.T) {
	state, _ := activeToolState(t)
	state.WorkerRunStatus = workerRunCancelled
	state.PendingReview = &reviewRequest{ID: "review-1", AgentID: "reviewer-child", Anchor: state.CurrentAnchor, Purpose: reviewPurposeWatchdog, Attempt: 1, Signals: []signal{{Type: "risk", Severity: "high"}}}
	if err := state.cancelWorkflow("operator cancelled"); err != nil {
		t.Fatal(err)
	}
	if state.PendingReview != nil || state.CancellationAgentID != "reviewer-child" || state.CancellationCleaned || state.dormant() {
		t.Fatalf("run cancellation lost its exact reviewer cleanup fence: %+v", state)
	}
	raw, _ := json.Marshal(state)
	durable, ok, err := latestDurableSuperviseState(projectionWithJournal(t, map[string]any{"kind": "run.cancelled", "data": json.RawMessage(raw)}))
	if err != nil || !ok || durable.Run == nil || durable.Run.CancellationAgentID != "reviewer-child" || durable.Run.dormant() {
		t.Fatalf("reviewer cleanup fence did not survive durable refold: durable=%+v ok=%v err=%v", durable, ok, err)
	}
	durable.Run.CancellationAgentID = ""
	durable.Run.CancellationCleaned = true
	if !durable.Run.dormant() || validateDormantRunState(*durable.Run) != nil {
		t.Fatalf("exactly cleaned cancellation did not become dormant: %+v", durable.Run)
	}
}

func TestDormantCompletionIsSelfContainedAfterHistoricalArtifactDisappears(t *testing.T) {
	state, _ := activeToolState(t)
	completed := state.CurrentAnchor
	approval := verdict{Decision: verdictApprove, Anchor: completed, Rationale: "independent evidence verifies completion", EvidenceRefs: []string{"evidence:verified"}}
	state.Completed = true
	state.CompletedAnchor = &completed
	state.CompletionVerdict = &approval
	state.CompletionHandedOff = true
	state.CompletionID = "completion-1"
	state.PendingReview = nil
	state.PendingCompletion = nil
	state.Hold = nil
	if !state.dormant() || validateDormantRunState(state) != nil {
		t.Fatalf("valid completed terminal state was not self-contained: %+v", state)
	}
	broken := state
	broken.CompletionID = ""
	if err := validateDormantRunState(broken); err == nil {
		t.Fatal("completion became reload-dormant without an exact handoff ID")
	}
	broken = state
	broken.Hold = &holdState{ID: "hold", Version: 1, Reason: "completion"}
	if broken.dormant() || validateDormantRunState(broken) == nil {
		t.Fatal("completion became dormant before its exact hold release")
	}
}
