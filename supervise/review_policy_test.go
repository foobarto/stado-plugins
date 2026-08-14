package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func pendingPolicyReview(state runState, attempt uint64) *reviewRequest {
	return &reviewRequest{
		ID: "retry-review", Anchor: state.CurrentAnchor, Purpose: reviewPurposeWatchdog, Attempt: attempt,
		Signals: []signal{{Type: "retry_thrash", Severity: "high", EvidenceRefs: []string{"event:retry"}}},
	}
}

func TestEventReviewRetriesThreeFreshAttemptsDurably(t *testing.T) {
	state, _ := activeToolState(t)
	now := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	state.PendingReview = pendingPolicyReview(state, 1)
	firstOwnership := reviewAgentOwnership(state.RunID, state.PendingReview)
	for failedAttempt := uint64(1); failedAttempt < 3; failedAttempt++ {
		change, err := state.applyReviewerFailure("timeout", now)
		if err != nil || len(change.Actions) != 1 || change.Actions[0].Kind != actionScheduleReviewRetry || state.PendingReview == nil || state.PendingReview.Attempt != failedAttempt+1 || !state.PendingReview.RetryAt.Equal(now.Add(250*time.Millisecond)) {
			t.Fatalf("attempt %d retry: change=%+v state=%+v err=%v", failedAttempt, change, state, err)
		}
		if state.PendingReview.AgentID != "" || state.PendingReview.Terminal != nil || state.PendingReview.LastFailure != "timeout" {
			t.Fatalf("attempt %d retained old child: %+v", failedAttempt, state.PendingReview)
		}
		encoded, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		var rebound runState
		if decodeErr := decodeStrictBytes(encoded, &rebound); decodeErr != nil {
			t.Fatalf("attempt %d did not survive durable replay: %v", failedAttempt, decodeErr)
		}
		if rebound.PendingReview == nil || rebound.PendingReview.Attempt != state.PendingReview.Attempt || !rebound.PendingReview.RetryAt.Equal(state.PendingReview.RetryAt) || rebound.PendingReview.LastFailure != state.PendingReview.LastFailure {
			t.Fatalf("attempt %d replay lost retry state: %+v", failedAttempt, rebound.PendingReview)
		}
		state = rebound
		now = now.Add(time.Second)
	}
	if got := reviewAgentOwnership(state.RunID, state.PendingReview); got == firstOwnership {
		t.Fatalf("fresh retry reused child ownership: %q", got)
	}
	change, err := state.applyReviewerFailure("timeout", now)
	if err != nil || len(change.Actions) != 0 || state.PendingReview != nil || state.FailedEventStreak != 1 {
		t.Fatalf("third attempt did not exhaust one trigger: change=%+v state=%+v err=%v", change, state, err)
	}
}

func TestExhaustedStepReviewClearsClaimForFreshSubmission(t *testing.T) {
	state, _ := activeToolState(t)
	state.PendingStepClaim = &stepCompletionClaim{ActiveStep: state.CurrentAnchor.ActiveStep, CriterionIndex: 0, Anchor: state.CurrentAnchor, EvidenceRefs: []string{"git:claim"}}
	state.PendingReview = &reviewRequest{
		ID: "step-review", Anchor: state.CurrentAnchor, Purpose: reviewPurposeWatchdog,
		Attempt: uint64(state.Config.EventReviewRetries), Signals: []signal{{Type: "step_completion_claim", EvidenceRefs: []string{"git:claim"}}},
	}
	change, err := state.applyReviewerFailure("timeout", time.Now())
	if err != nil || state.PendingReview != nil || state.PendingStepClaim != nil || len(change.Actions) != 0 {
		t.Fatalf("exhausted step review left a hidden claim: change=%+v state=%+v err=%v", change, state, err)
	}
}

func TestTenConsecutiveExhaustedEventTriggersPauseAndSuccessResets(t *testing.T) {
	state, _ := activeToolState(t)
	state.Config.EventReviewRetries = 3
	state.Config.FailedEventLimit = 10
	for trigger := 1; trigger <= 9; trigger++ {
		state.PendingReview = pendingPolicyReview(state, 3)
		change, err := state.applyReviewerFailure("error", time.Unix(int64(trigger), 0))
		if err != nil || len(change.Actions) != 0 || state.FailedEventStreak != trigger {
			t.Fatalf("trigger %d: change=%+v streak=%d err=%v", trigger, change, state.FailedEventStreak, err)
		}
	}
	state.PendingReview = pendingPolicyReview(state, 1)
	if _, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictContinue, Anchor: state.CurrentAnchor, Rationale: "fresh review recovered"}}, ""); err != nil {
		t.Fatal(err)
	}
	if state.FailedEventStreak != 0 {
		t.Fatalf("success retained failure streak %d", state.FailedEventStreak)
	}
	state.FailedEventStreak = 9
	state.PendingReview = pendingPolicyReview(state, 3)
	change, err := state.applyReviewerFailure("error", time.Unix(20, 0))
	if err != nil || len(change.Actions) != 1 || change.Actions[0].Kind != actionPause || !strings.Contains(change.Actions[0].Reason, "10 consecutive") {
		t.Fatalf("tenth trigger did not pause: change=%+v err=%v", change, err)
	}
}

func TestLiveRetryUsesCappedExponentialDurableDelay(t *testing.T) {
	state, _ := activeToolState(t)
	state.Config.Mode = modeLive
	state.Config.LiveRetryBaseMillis, state.Config.LiveRetryMaxMillis = 500, 8000
	state.PendingReview = pendingPolicyReview(state, 1)
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	for index, delay := range want {
		change, err := state.applyReviewerFailure("error", now)
		if err != nil || len(change.Actions) != 1 || change.Actions[0].Kind != actionScheduleReviewRetry || state.PendingReview == nil || !state.PendingReview.RetryAt.Equal(now.Add(delay)) {
			t.Fatalf("live retry %d: due=%s want=%s change=%+v err=%v", index, state.PendingReview.RetryAt, now.Add(delay), change, err)
		}
		now = now.Add(20 * time.Second)
	}
	if state.FailedEventStreak != 0 {
		t.Fatalf("live retry incorrectly exhausted a trigger: %d", state.FailedEventStreak)
	}
}

func TestWatchdogHandoffIsBoundedDurableAndPassedToFreshReview(t *testing.T) {
	state, contract := activeToolState(t)
	state.Config.ReviewEveryTurns = 1
	handoff := watchdogHandoff{OpenConcerns: []string{"documentation proof"}, MissingEvidence: []string{"trace:full-test"}, SuggestedProbes: []string{"read docs and test output"}}
	state.PendingReview = pendingPolicyReview(state, 1)
	result := reviewerResult{Verdict: verdict{Decision: verdictContinue, Anchor: state.CurrentAnchor, Rationale: "continue with bounded concerns", Handoff: handoff}}
	if _, err := state.applyReviewerResult(result, "cleanup after semantic verdict"); err != nil {
		t.Fatal(err)
	}
	if state.WatchdogHandoff.OpenConcerns[0] != "documentation proof" || state.FailedEventStreak != 0 {
		t.Fatalf("handoff/result not folded: %+v", state)
	}
	_, event := testAnchor(8, state.CurrentAnchor.ActiveStep, state.CompletedSteps)
	event.Anchor.TreeDigest = state.CurrentAnchor.TreeDigest
	event.Anchor.TurnRef = "git:refs/sessions/worker-1/tree@0123456789abcdef0123456789abcdef01234567#turn-8-iteration-1"
	change, err := state.observe(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(change.Actions) == 0 || state.PendingReview == nil {
		t.Fatalf("fresh review not created: %+v", change)
	}
	request, err := buildWatchdogSpawnRequest(contract, state.Config, state.PendingReview, state.Config.WatchdogTokenBudget)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(request.Prompt, `"previous_watchdog_handoff":{"open_concerns":["documentation proof"]`) {
		t.Fatalf("fresh reviewer prompt lost bounded handoff: %s", request.Prompt)
	}
	tooLarge := watchdogHandoff{OpenConcerns: make([]string, 17)}
	for index := range tooLarge.OpenConcerns {
		tooLarge.OpenConcerns[index] = fmt.Sprintf("concern-%d", index)
	}
	if err := tooLarge.validate(); err == nil {
		t.Fatal("accepted oversized watchdog handoff")
	}
	encoded, _ := json.Marshal(state)
	var rebound runState
	if err := decodeStrictBytes(encoded, &rebound); err != nil || rebound.WatchdogHandoff.OpenConcerns[0] != "documentation proof" {
		t.Fatalf("handoff did not survive journal-shaped rebind: %+v err=%v", rebound.WatchdogHandoff, err)
	}
}

func TestCorrectionFollowUpPausesAfterThreeFailedCorrections(t *testing.T) {
	state, _ := activeToolState(t)
	state.Config.CorrectionLimit = 3
	state.PendingReview = pendingPolicyReview(state, 1)
	initial := reviewerResult{Verdict: verdict{Decision: verdictCorrect, Anchor: state.CurrentAnchor, Rationale: "first correction", Correction: "read the failing test", EvidenceRefs: []string{"trace:failure"}}}
	change, err := state.applyReviewerResult(initial, "")
	if err != nil || len(change.Actions) != 1 || change.Actions[0].Kind != actionSteer || state.CorrectionFollowUp == nil {
		t.Fatalf("initial correction=%+v state=%+v err=%v", change, state, err)
	}
	for failure := 1; failure <= 3; failure++ {
		state.PendingReview = &reviewRequest{ID: fmt.Sprintf("followup-%d", failure), Anchor: state.CurrentAnchor, Purpose: reviewPurposeWatchdog, Attempt: 1, Signals: []signal{{Type: "correction_follow_up", EvidenceRefs: []string{"trace:turn"}}}}
		result := reviewerResult{Verdict: verdict{Decision: verdictCorrect, Anchor: state.CurrentAnchor, Rationale: "still off track", Correction: "try a bounded alternative", EvidenceRefs: []string{"trace:still-failing"}}}
		change, err = state.applyReviewerResult(result, "")
		if err != nil {
			t.Fatal(err)
		}
		if failure < 3 && (len(change.Actions) != 1 || change.Actions[0].Kind != actionSteer || state.CorrectionFollowUp == nil) {
			t.Fatalf("failed correction %d did not schedule follow-up: %+v", failure, change)
		}
	}
	if len(change.Actions) != 1 || change.Actions[0].Kind != actionPause || state.FailedCorrectionCount != 3 || state.CorrectionFollowUp != nil {
		t.Fatalf("third failed correction did not pause: change=%+v state=%+v", change, state)
	}
}

func TestNextWorkerTurnForcesCorrectionFollowUpReview(t *testing.T) {
	state, _ := activeToolState(t)
	state.CorrectionFollowUp = &correctionFollowUpState{IssuedAt: state.CurrentAnchor, CorrectionRef: "sha256:" + digestString("fix"), EvidenceRefs: []string{"trace:correction"}}
	_, event := testAnchor(8, state.CurrentAnchor.ActiveStep, state.CompletedSteps)
	event.Anchor.TreeDigest = "tree-next"
	event.Anchor.TurnRef = "git:refs/sessions/worker-1/tree@89abcdef0123456789abcdef0123456789abcdef#turn-8-iteration-1"
	change, err := state.observe(event)
	if err != nil || state.PendingReview == nil || !reviewContainsSignal(state.PendingReview, "correction_follow_up") || len(change.Actions) != 1 || change.Actions[0].Kind != actionStartReview {
		t.Fatalf("correction follow-up trigger missing: change=%+v state=%+v err=%v", change, state, err)
	}
}
