package main

import (
	"slices"
	"strings"
	"testing"
)

func completionReviewState(t *testing.T) (runState, supervisionContract) {
	t.Helper()
	state, contract := activeToolState(t)
	state.CompletedSteps = state.PlanTotal
	state.CurrentAnchor.ActiveStep = "completion"
	addCurrentVerificationGates(&state, contract)
	state.Criteria[0].EvidenceRefs = []string{"git:criterion-0"}
	state.Criteria[1].EvidenceRefs = []string{"git:criterion-1"}
	args := requestCompletionArgs{
		IdempotencyKey: "completion-1", Anchor: state.CurrentAnchor,
		Criteria: []criterionEvidenceInput{
			{CriterionIndex: 0, EvidenceRefs: []string{"git:criterion-0"}},
			{CriterionIndex: 1, EvidenceRefs: []string{"git:criterion-1"}},
		},
	}
	if _, _, err := state.applyRequestCompletion(contract, args); err != nil {
		t.Fatal(err)
	}
	state.Hold.ID, state.Hold.Version = "hold-completion", 1
	return state, contract
}

func TestCompletionDeterministicGatesRunBeforeReview(t *testing.T) {
	state, contract := activeToolState(t)
	args := requestCompletionArgs{IdempotencyKey: "early", Anchor: state.CurrentAnchor, Criteria: []criterionEvidenceInput{{CriterionIndex: 0, EvidenceRefs: []string{"git:evidence"}}}}
	if _, _, err := state.applyRequestCompletion(contract, args); err == nil || !strings.Contains(err.Error(), "plan step") {
		t.Fatalf("active plan completion accepted: %v", err)
	}

	state, contract = activeToolState(t)
	state.CompletedSteps, state.CurrentAnchor.ActiveStep = state.PlanTotal, "completion"
	state.VerificationGates = nil
	state.LastHostVerification = nil
	state.Criteria[0].EvidenceRefs, state.Criteria[1].EvidenceRefs = []string{"git:c0"}, []string{"git:c1"}
	contract.Verification = []string{"go test ./..."}
	args = requestCompletionArgs{IdempotencyKey: "gated", Anchor: state.CurrentAnchor, Criteria: []criterionEvidenceInput{{CriterionIndex: 0, EvidenceRefs: []string{"git:c0"}}, {CriterionIndex: 1, EvidenceRefs: []string{"git:c1"}}}}
	if _, _, err := state.applyRequestCompletion(contract, args); err == nil || !strings.Contains(err.Error(), "operator-configured verification") {
		t.Fatalf("missing verification gate accepted: %v", err)
	}
	digest := "sha256:" + digestString("go test ./...")
	state.VerificationGates = map[string]verificationGateState{digest: {
		CommandDigest: digest, ResultDigest: "sha256:stale", Passed: true,
		Anchor:       anchor{SessionSequence: state.CurrentAnchor.SessionSequence - 1, PlanVersion: state.CurrentAnchor.PlanVersion, ActiveStep: "completion", TreeDigest: "tree-stale", TurnRef: "turn-stale"},
		EvidenceRefs: []string{"trace:stale-go-test"},
	}}
	state.LastHostVerification = &hostVerificationResultState{
		ID: "verification-stale", Source: anchor{SessionSequence: state.CurrentAnchor.SessionSequence - 1, PlanVersion: state.CurrentAnchor.PlanVersion, ActiveStep: "completion", TreeDigest: "tree-stale", TurnRef: "turn-stale"},
		SuiteDigest: "sha256:" + digestString("operator-suite"), CommandDigests: []string{digest}, Outcome: "commands_succeeded", EvidenceRefs: []string{"trace:stale-go-test"}, Usable: true,
	}
	if _, _, err := state.applyRequestCompletion(contract, args); err == nil || !strings.Contains(err.Error(), "exact terminal result") {
		t.Fatalf("stale verification gate accepted: %v", err)
	}
	passed := true
	event := workerEvent{
		ID: "verification-current", Kind: eventVerification, Sequence: state.ObservationSequence + 1,
		Anchor: state.CurrentAnchor, VerificationPassed: &passed,
		VerificationCommandDigest: digest, VerificationResultDigest: "sha256:result",
		EvidenceRefs: []string{"trace:go-test"},
	}
	if _, err := state.observe(event); err != nil {
		t.Fatal(err)
	}
	state.LastHostVerification = &hostVerificationResultState{
		ID: "verification-current", Source: state.CurrentAnchor,
		SuiteDigest: "sha256:" + digestString("operator-suite"), CommandDigests: []string{digest}, Outcome: "commands_succeeded", EvidenceRefs: []string{"trace:go-test"}, Usable: true,
	}
	args.Anchor = state.CurrentAnchor
	change, _, err := state.applyRequestCompletion(contract, args)
	if err != nil || state.PendingCompletion == nil || len(change.Actions) != 2 {
		t.Fatalf("passing current gate did not admit completion: change=%+v state=%+v err=%v", change, state, err)
	}
}

func TestNoSuiteNeverClaimsBaselineVerificationRan(t *testing.T) {
	state, contract := activeToolState(t)
	state.CompletedSteps, state.CurrentAnchor.ActiveStep = state.PlanTotal, "completion"
	state.Criteria[0].EvidenceRefs, state.Criteria[1].EvidenceRefs = []string{"git:c0"}, []string{"git:c1"}
	state.LastHostVerification = &hostVerificationResultState{
		ID: "verification-no-suite", Source: state.CurrentAnchor,
		SuiteDigest: "sha256:" + digestString("empty-operator-suite"), Outcome: "no_suite",
		EvidenceRefs: []string{"broker:wal:lifecycle_application:90"}, Usable: true,
	}
	args := requestCompletionArgs{
		IdempotencyKey: "completion-no-suite", Anchor: state.CurrentAnchor,
		Criteria: []criterionEvidenceInput{{CriterionIndex: 0, EvidenceRefs: []string{"git:c0"}}, {CriterionIndex: 1, EvidenceRefs: []string{"git:c1"}}},
	}
	if _, _, err := state.applyRequestCompletion(contract, args); err != nil {
		t.Fatal(err)
	}
	if state.PendingCompletion == nil || state.PendingCompletion.HostVerificationOutcome != "no_suite" {
		t.Fatalf("no-suite fact was not carried into independent review: %+v", state.PendingCompletion)
	}
	if _, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictApprove, Anchor: state.CurrentAnchor, Rationale: "send to verifier", EvidenceRefs: []string{"git:c0"}}}, ""); err != nil {
		t.Fatal(err)
	}
	request, err := buildCompletionVerifierSpawnRequest(contract, state.Config, state.PendingReview, state.PendingCompletion, state.Config.VerifierTokenBudget)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"outcome":"no_suite"`, "not a pass", "never executable command authority"} {
		if !strings.Contains(request.Prompt, want) {
			t.Fatalf("no-suite verifier prompt omitted %q: %s", want, request.Prompt)
		}
	}
}

func TestCompletionWatchdogApprovalStartsFreshVerifierWithoutReleasingHold(t *testing.T) {
	state, _ := completionReviewState(t)
	anchor := state.CurrentAnchor
	change, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{
		Decision: verdictApprove, Anchor: anchor, Rationale: "candidate merits independent verification", EvidenceRefs: []string{"git:criterion-0"},
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingReview == nil || state.PendingReview.Purpose != reviewPurposeVerifier || state.PendingReview.AgentID != "" || state.PendingCompletion.Stage != reviewPurposeVerifier {
		t.Fatalf("independent verifier was not staged: %+v", state)
	}
	if len(change.Actions) != 1 || change.Actions[0].Kind != actionStartReview || change.Actions[0].TokenBudget != state.Config.VerifierTokenBudget || state.Hold == nil {
		t.Fatalf("verifier transition=%+v hold=%+v", change, state.Hold)
	}
}

func TestOnlyCurrentVerifierApprovalCompletesAndCleanupCannotEraseIt(t *testing.T) {
	state, _ := completionReviewState(t)
	anchor := state.CurrentAnchor
	if _, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictApprove, Anchor: anchor, Rationale: "send to verifier", EvidenceRefs: []string{"git:criterion-0"}}}, ""); err != nil {
		t.Fatal(err)
	}
	change, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{
		Decision: verdictApprove, Anchor: anchor, Rationale: "all criteria and done conditions are proven", EvidenceRefs: []string{"git:criterion-0", "trace:go-test"},
	}}, "provider cleanup failed after verdict")
	if err != nil {
		t.Fatal(err)
	}
	if !state.Completed || state.CompletedAnchor == nil || *state.CompletedAnchor != anchor || state.CompletionVerdict == nil || state.LastVerdict == nil || state.LastVerdict.Decision != verdictApprove || state.PendingCompletion != nil || state.PendingReview != nil {
		t.Fatalf("valid verifier approval did not complete: %+v", state)
	}
	if len(change.Actions) != 0 || state.Hold == nil || len(state.Diagnostics) == 0 || !strings.Contains(state.Diagnostics[len(state.Diagnostics)-1], "cleanup") {
		t.Fatalf("completion cleanup/hold semantics lost: change=%+v state=%+v", change, state)
	}
}

func TestVerifierCompletionBuildsStableGenericSuccessHandoff(t *testing.T) {
	state, _ := completionReviewState(t)
	anchor := state.CurrentAnchor
	if _, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictApprove, Anchor: anchor, Rationale: "send to verifier", EvidenceRefs: []string{"git:criterion-0"}}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictApprove, Anchor: anchor, Rationale: "verified success", EvidenceRefs: []string{"git:criterion-0"}}}, ""); err != nil {
		t.Fatal(err)
	}
	request, err := prepareCompletionHandoff(state)
	if err != nil {
		t.Fatal(err)
	}
	again, err := prepareCompletionHandoff(state)
	if err != nil || request.IdempotencyKey != again.IdempotencyKey || request.RunID != again.RunID || request.Summary != again.Summary || !slices.Equal(request.EvidenceRefs, again.EvidenceRefs) {
		t.Fatalf("completion handoff request is not restart-stable: first=%+v second=%+v err=%v", request, again, err)
	}
	if request.RunID != state.RunID || request.IdempotencyKey == "" || request.Summary != "verified success" || len(request.EvidenceRefs) != 1 {
		t.Fatalf("completion handoff lost verifier authority: %+v", request)
	}
	ack := exactCompletionAck(t, state)
	ack.ID = "completion-1"
	if err := acceptCompletionHandoff(&state, testCompletionIdentity, ack); err != nil {
		t.Fatal(err)
	}
	if !state.CompletionHandedOff || state.CompletionID != "completion-1" || state.Hold == nil {
		t.Fatalf("handoff should retain exact hold until CAS release: %+v", state)
	}
}

func TestCompletionCarriesExactApplicationOrderedDeferredSet(t *testing.T) {
	state, _ := completionReviewState(t)
	anchor := state.CurrentAnchor
	if _, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictApprove, Anchor: anchor, Rationale: "verify", EvidenceRefs: []string{"git:criterion-0"}}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictApprove, Anchor: anchor, Rationale: "verified", EvidenceRefs: []string{"git:criterion-0"}}}, ""); err != nil {
		t.Fatal(err)
	}
	state.DeferredInputs = []deferredInputState{
		{InputID: "later-2", RunID: state.RunID, Ordinal: 2, Digest: digestOperatorInput("second"), TaskID: "task-2"},
		{InputID: "later-1", RunID: state.RunID, Ordinal: 1, Digest: digestOperatorInput("first"), TaskID: "task-1"},
	}
	request, err := prepareCompletionHandoff(state)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"later-1", "later-2"}
	if !slices.Equal(request.ContinuationInputIDs, want) {
		t.Fatalf("continuation input order=%v want=%v", request.ContinuationInputIDs, want)
	}
	ack := exactCompletionAck(t, state)
	ack.ID = "completion"
	ack.ContinuationInputIDs = []string{"later-1"}
	if err := acceptCompletionHandoff(&state, testCompletionIdentity, ack); err == nil {
		t.Fatal("broker acknowledgement omitted an application-selected deferred input")
	}
	ack.ContinuationInputIDs = want
	if err := acceptCompletionHandoff(&state, testCompletionIdentity, ack); err != nil {
		t.Fatal(err)
	}
}

func TestStaleVerifierApprovalNeverCompletes(t *testing.T) {
	state, _ := completionReviewState(t)
	old := state.CurrentAnchor
	if _, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictApprove, Anchor: old, Rationale: "send to verifier", EvidenceRefs: []string{"git:c0"}}}, ""); err != nil {
		t.Fatal(err)
	}
	state.CurrentAnchor = anchor{SessionSequence: old.SessionSequence + 1, PlanVersion: old.PlanVersion, ActiveStep: "completion", TreeDigest: "tree-new", TurnRef: "turn-new"}
	change, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictApprove, Anchor: old, Rationale: "old snapshot passed", EvidenceRefs: []string{"git:c0"}}}, "")
	if err != nil || state.Completed || state.PendingCompletion != nil || len(change.Actions) != 1 || change.Actions[0].Kind != actionReleaseHold {
		t.Fatalf("stale verifier result=%+v state=%+v err=%v", change, state, err)
	}
}

func TestVerifierFailureProfileIsFailClosedOrAdvisory(t *testing.T) {
	for _, test := range []struct {
		name       string
		profile    string
		wantAction actionKind
		wantKeep   bool
	}{
		{"required", "required", actionPause, true},
		{"default required", "", actionPause, true},
		{"advisory", "advisory", actionReleaseHold, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, _ := completionReviewState(t)
			state.Config.VerifierProfile = test.profile
			state.QueuedSignals = []signal{{Type: "queued-during-completion", Severity: "warning"}}
			if _, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictApprove, Anchor: state.CurrentAnchor, Rationale: "send to verifier", EvidenceRefs: []string{"git:c0"}}}, ""); err != nil {
				t.Fatal(err)
			}
			state.PendingReview.Attempt = uint64(state.Config.EventReviewRetries)
			change, err := state.applyReviewerFailure("timeout")
			if err != nil || len(change.Actions) != 1 || change.Actions[0].Kind != test.wantAction || (state.PendingCompletion != nil) != test.wantKeep || state.Completed {
				t.Fatalf("failure change=%+v state=%+v err=%v", change, state, err)
			}
			if test.wantKeep && len(state.QueuedSignals) != 0 {
				t.Fatalf("required verifier failure left a review able to run behind the fail-closed pause: %+v", state.QueuedSignals)
			}
		})
	}
}

func TestReviewRecoveryUsesPurposeSpecificTokenBudget(t *testing.T) {
	cfg := defaultConfig()
	if got := reviewTokenBudget(cfg, &reviewRequest{Purpose: reviewPurposeWatchdog}); got != cfg.WatchdogTokenBudget {
		t.Fatalf("watchdog recovery budget=%d want=%d", got, cfg.WatchdogTokenBudget)
	}
	if got := reviewTokenBudget(cfg, &reviewRequest{Purpose: reviewPurposeVerifier}); got != cfg.VerifierTokenBudget {
		t.Fatalf("verifier recovery budget=%d want=%d", got, cfg.VerifierTokenBudget)
	}
}
