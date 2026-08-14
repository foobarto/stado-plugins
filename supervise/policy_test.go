package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func testAnchor(sequence uint64, step string, completed int) (anchor, workerEvent) {
	a := anchor{SessionSequence: sequence, PlanVersion: 1, ActiveStep: step, TreeDigest: "tree-" + string(rune('a'+sequence)), TurnRef: "turn-" + string(rune('a'+sequence))}
	return a, workerEvent{ID: "event-" + string(rune('a'+sequence)), Kind: eventTurnCompleted, Sequence: sequence, Anchor: a, CompletedSteps: completed}
}

func testState(t *testing.T, cfg config) runState {
	t.Helper()
	state, err := newRunState("run-test", cfg)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func signalTypes(items []signal) string {
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, item.Type)
	}
	return strings.Join(values, ",")
}

func TestEventModeAddsPeriodicCadence(t *testing.T) {
	cfg := defaultConfig()
	cfg.ReviewEveryTurns = 3
	state := testState(t, cfg)
	for sequence := uint64(1); sequence <= 3; sequence++ {
		_, event := testAnchor(sequence, "build", int(sequence))
		transition, err := state.observe(event)
		if err != nil {
			t.Fatal(err)
		}
		if sequence < 3 && len(transition.Actions) != 0 {
			t.Fatalf("unexpected early review: %+v", transition)
		}
		if sequence == 3 {
			if state.PendingReview == nil || !strings.Contains(signalTypes(state.PendingReview.Signals), "periodic_turn") {
				t.Fatalf("periodic review missing: %+v", state.PendingReview)
			}
		}
	}
}

func TestStrictLiveAcquiresBarrierBeforeReview(t *testing.T) {
	cfg := defaultConfig()
	cfg.Mode = modeLive
	cfg.StrictLiveBarrier = true
	state := testState(t, cfg)
	_, event := testAnchor(1, "build", 0)
	transition, err := state.observe(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(transition.Actions) != 2 || transition.Actions[0].Kind != actionAcquireHold || transition.Actions[1].Kind != actionStartReview {
		t.Fatalf("strict-live action order = %+v", transition.Actions)
	}
}

func TestFourTurnStallIgnoresActivity(t *testing.T) {
	for _, test := range []struct {
		name        string
		change      func(*workerEvent)
		wantTrigger bool
	}{
		{"tree activity", func(event *workerEvent) { event.Anchor.TreeDigest = "tree-busy" }, true},
		{"evidence activity", func(event *workerEvent) { event.EvidenceCount = 99 }, true},
		{"completed step", func(event *workerEvent) { event.CompletedSteps = 1 }, false},
		{"changed active step", func(event *workerEvent) { event.Anchor.ActiveStep = "verify" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := defaultConfig()
			state := testState(t, cfg)
			for sequence := uint64(1); sequence <= 4; sequence++ {
				_, event := testAnchor(sequence, "build", 0)
				if sequence == 4 {
					test.change(&event)
				}
				transition, err := state.observe(event)
				if err != nil {
					t.Fatal(err)
				}
				if sequence == 4 {
					got := state.PendingReview != nil && strings.Contains(signalTypes(state.PendingReview.Signals), "no_criteria_progress")
					if got != test.wantTrigger {
						t.Fatalf("trigger=%v want=%v transition=%+v", got, test.wantTrigger, transition)
					}
				}
			}
		})
	}
}

func TestRetryThrashAndScopeEvalSignals(t *testing.T) {
	cfg := defaultConfig()
	state := testState(t, cfg)
	for sequence := uint64(1); sequence <= 3; sequence++ {
		a := anchor{SessionSequence: sequence, PlanVersion: 1, ActiveStep: "fix", TreeDigest: "tree", TurnRef: "turn"}
		event := workerEvent{ID: "tool-" + string(rune('a'+sequence)), Kind: eventToolOutcome, Sequence: sequence, Anchor: a, Tool: "npm test", ArgsDigest: "same", ErrorFingerprint: "not-found"}
		transition, err := state.observe(event)
		if err != nil {
			t.Fatal(err)
		}
		if sequence == 2 {
			state.PendingReview = nil // emulate the first review completing
		}
		if sequence == 3 && !strings.Contains(signalTypes(state.PendingReview.Signals), "retry_thrash") {
			t.Fatalf("retry-thrash signal missing: %+v / %+v", transition, state.PendingReview)
		}
	}
}

func TestValidVerdictSurvivesCleanupDiagnostic(t *testing.T) {
	state := testState(t, defaultConfig())
	a, event := testAnchor(1, "build", 0)
	state.CurrentAnchor = a
	state.PendingReview = &reviewRequest{ID: "review", Anchor: a, Signals: []signal{{Type: "periodic_turn"}}}
	result := reviewerResult{Verdict: verdict{Decision: verdictContinue, Anchor: event.Anchor, Rationale: "current evidence is aligned"}}
	transition, err := state.applyReviewerResult(result, "provider close failed")
	if err != nil {
		t.Fatal(err)
	}
	if state.LastVerdict == nil || len(state.Diagnostics) != 1 || transition.Note == "" {
		t.Fatalf("valid verdict was lost: state=%+v transition=%+v", state, transition)
	}
}

func TestStaleVerdictsAreAsymmetric(t *testing.T) {
	old := anchor{SessionSequence: 1, PlanVersion: 1, ActiveStep: "build", TreeDigest: "old", TurnRef: "turn-old"}
	current := anchor{SessionSequence: 2, PlanVersion: 1, ActiveStep: "build", TreeDigest: "current", TurnRef: "turn-current"}

	t.Run("positive discarded", func(t *testing.T) {
		state := testState(t, defaultConfig())
		state.CurrentAnchor = current
		state.PendingReview = &reviewRequest{ID: "old", Anchor: old}
		transition, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictContinue, Anchor: old, Rationale: "old state looked fine"}}, "")
		if err != nil || len(transition.Actions) != 0 || !strings.Contains(transition.Note, "discarded") {
			t.Fatalf("stale positive = %+v, %v", transition, err)
		}
		if state.LastVerdict != nil {
			t.Fatalf("stale positive became durable last verdict: %+v", state.LastVerdict)
		}
	})

	t.Run("correction stays advisory", func(t *testing.T) {
		state := testState(t, defaultConfig())
		state.CurrentAnchor = current
		state.PendingReview = &reviewRequest{ID: "old", Anchor: old}
		transition, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictCorrect, Anchor: old, Rationale: "retry is unproductive", EvidenceRefs: []string{"event-1"}, Correction: "read repository guidance"}}, "")
		if err != nil || len(transition.Actions) != 1 || transition.Actions[0].Kind != actionSteer || !transition.Actions[0].Advisory || state.Hold != nil {
			t.Fatalf("stale correction = %+v state=%+v err=%v", transition, state, err)
		}
	})

	for _, decision := range []verdictDecision{verdictPause, verdictStop} {
		t.Run(string(decision)+" holds and rereviews", func(t *testing.T) {
			state := testState(t, defaultConfig())
			state.CurrentAnchor = current
			state.PendingReview = &reviewRequest{ID: "old", Anchor: old}
			transition, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: decision, Anchor: old, Rationale: "serious drift", EvidenceRefs: []string{"event-1"}}}, "")
			if err != nil || len(transition.Actions) != 2 || transition.Actions[0].Kind != actionAcquireHold || transition.Actions[1].Kind != actionStartReview || state.PendingReview == nil || !state.PendingReview.Confirming || state.PendingReview.Anchor != current {
				t.Fatalf("stale %s = %+v state=%+v err=%v", decision, transition, state, err)
			}
		})
	}
}

func TestStaleStepApprovalCannotLeaveAHiddenPendingClaim(t *testing.T) {
	old := anchor{SessionSequence: 1, PlanVersion: 1, ActiveStep: "build", TreeDigest: "old", TurnRef: "turn-old"}
	current := anchor{SessionSequence: 2, PlanVersion: 1, ActiveStep: "build", TreeDigest: "current", TurnRef: "turn-current"}
	state := testState(t, defaultConfig())
	state.CurrentAnchor = current
	state.PendingStepClaim = &stepCompletionClaim{ActiveStep: "build", CriterionIndex: 0, Anchor: old, EvidenceRefs: []string{"git:old"}}
	state.PendingReview = &reviewRequest{ID: "old-step", Anchor: old, Purpose: reviewPurposeWatchdog, Attempt: 1, Signals: []signal{{Type: "step_completion_claim"}}}
	change, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictApprove, Anchor: old, Rationale: "old step looked complete", EvidenceRefs: []string{"git:old"}}}, "")
	if err != nil || state.PendingStepClaim != nil || state.CompletedSteps != 0 || len(change.Actions) != 0 {
		t.Fatalf("stale step approval retained hidden state: change=%+v state=%+v err=%v", change, state, err)
	}
}

func TestEvalScenarioDetectorSignals(t *testing.T) {
	passed, failed := true, false
	base := func(sequence uint64, kind workerEventKind) workerEvent {
		return workerEvent{
			ID: "event-" + string(rune('a'+sequence)), Kind: kind, Sequence: sequence,
			Anchor: anchor{SessionSequence: sequence, PlanVersion: 1, ActiveStep: "build", TreeDigest: "tree", TurnRef: "turn"},
		}
	}
	tests := []struct {
		name  string
		prior []workerEvent
		event workerEvent
		want  string
	}{
		{
			name: "scope drift",
			event: func() workerEvent {
				event := base(1, eventTreeChanged)
				event.OutOfScopePaths = []string{"go.mod"}
				return event
			}(),
			want: "scope_expansion",
		},
		{
			name: "verification regression",
			prior: []workerEvent{func() workerEvent {
				event := base(1, eventVerification)
				event.VerificationPassed = &passed
				return event
			}()},
			event: func() workerEvent {
				event := base(2, eventVerification)
				event.VerificationPassed = &failed
				return event
			}(),
			want: "verification_regression",
		},
		{
			name: "failed child",
			event: func() workerEvent {
				event := base(1, eventChildLifecycle)
				event.ChildID, event.ChildStatus = "child-1", "failed"
				return event
			}(),
			want: "child_failure",
		},
		{name: "plan pivot", event: base(1, eventPivotRequested), want: "pivot_request"},
		{name: "premature completion", event: base(1, eventCompletionClaimed), want: "completion_claim"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := signalTypes(detect(test.prior, test.event)); !strings.Contains(got, test.want) {
				t.Fatalf("signals=%q want=%q", got, test.want)
			}
		})
	}
}

func TestFreshConfirmationReleasesHoldBeforeContinue(t *testing.T) {
	state := testState(t, defaultConfig())
	current := anchor{SessionSequence: 2, PlanVersion: 1, ActiveStep: "build", TreeDigest: "current", TurnRef: "turn-current"}
	state.CurrentAnchor = current
	state.PendingReview = &reviewRequest{ID: "confirm", Anchor: current, Confirming: true}
	state.Hold = &holdState{ID: "hold-1", Version: 1, Reason: "confirm stale intervention"}
	transition, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictContinue, Anchor: current, Rationale: "current evidence clears the concern"}}, "")
	if err != nil || len(transition.Actions) != 1 || transition.Actions[0].Kind != actionReleaseHold || state.Hold == nil {
		t.Fatalf("fresh release = %+v state=%+v err=%v", transition, state, err)
	}
}

func TestFreshStopIsEnforceable(t *testing.T) {
	state := testState(t, defaultConfig())
	current := anchor{SessionSequence: 2, PlanVersion: 1, ActiveStep: "build", TreeDigest: "current", TurnRef: "turn-current"}
	state.CurrentAnchor = current
	state.PendingReview = &reviewRequest{ID: "confirm", Anchor: current, Confirming: true}
	state.Hold = &holdState{ID: "hold-1", Version: 1, Reason: "stale stop confirmation"}
	transition, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictStop, Anchor: current, Rationale: "current evidence confirms stop", EvidenceRefs: []string{"event-2"}}}, "")
	if err != nil || len(transition.Actions) != 1 || transition.Actions[0].Kind != actionStop {
		t.Fatalf("fresh stop = %+v err=%v", transition, err)
	}
}

func TestStaleInterventionReusesExistingStrictLiveHold(t *testing.T) {
	cfg := defaultConfig()
	cfg.Mode, cfg.StrictLiveBarrier, cfg.WatchdogTokenBudget, cfg.VerifierTokenBudget, cfg.HoldTTLSeconds = modeLive, true, 128, 128, 60
	state := testState(t, cfg)
	a1, event := testAnchor(1, "implement", 0)
	transition, err := state.observe(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(transition.Actions) != 2 || transition.Actions[0].Kind != actionAcquireHold {
		t.Fatalf("strict live review did not acquire initial hold: %#v", transition.Actions)
	}
	state.Hold = &holdState{ID: "hold-1", Version: 1, Reason: "strict live review"}
	state.CurrentAnchor, _ = testAnchor(2, "implement", 0)
	result := reviewerResult{Verdict: verdict{Decision: verdictStop, Anchor: a1, Rationale: "stop may still be required", EvidenceRefs: []string{"e1"}}}
	transition, err = state.applyReviewerResult(result, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(transition.Actions) != 1 || transition.Actions[0].Kind != actionStartReview {
		t.Fatalf("existing durable hold should be reused, got %#v", transition.Actions)
	}
	if state.Hold == nil || state.Hold.ID != "hold-1" || !state.PendingReview.Confirming {
		t.Fatalf("existing hold or confirming review was lost: hold=%#v review=%#v", state.Hold, state.PendingReview)
	}
}

func TestHeldStalePositiveRefreshesCurrentReview(t *testing.T) {
	cfg := defaultConfig()
	cfg.Mode, cfg.StrictLiveBarrier, cfg.WatchdogTokenBudget, cfg.VerifierTokenBudget, cfg.HoldTTLSeconds = modeLive, true, 128, 128, 60
	state := testState(t, cfg)
	a1, event := testAnchor(1, "implement", 0)
	if _, err := state.observe(event); err != nil {
		t.Fatal(err)
	}
	state.Hold = &holdState{ID: "hold-1", Version: 1, Reason: "strict live review"}
	state.CurrentAnchor, _ = testAnchor(2, "implement", 0)
	result := reviewerResult{Verdict: verdict{Decision: verdictContinue, Anchor: a1, Rationale: "earlier state looked fine"}}
	transition, err := state.applyReviewerResult(result, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(transition.Actions) != 1 || transition.Actions[0].Kind != actionStartReview {
		t.Fatalf("held stale positive must refresh rather than wedge or release: %#v", transition.Actions)
	}
	if state.Hold == nil || state.Hold.ID != "hold-1" || state.PendingReview == nil || state.PendingReview.Anchor.SessionSequence != 2 {
		t.Fatalf("hold/current review not preserved: hold=%#v review=%#v", state.Hold, state.PendingReview)
	}
	if state.LastVerdict != nil {
		t.Fatalf("stale positive acquired durable verdict meaning: %#v", state.LastVerdict)
	}
}

func TestHeldStaleCorrectionSteersAndRefreshesWithoutAnotherHold(t *testing.T) {
	cfg := defaultConfig()
	cfg.Mode, cfg.StrictLiveBarrier, cfg.WatchdogTokenBudget, cfg.VerifierTokenBudget, cfg.HoldTTLSeconds = modeLive, true, 128, 128, 60
	state := testState(t, cfg)
	a1, event := testAnchor(1, "implement", 0)
	if _, err := state.observe(event); err != nil {
		t.Fatal(err)
	}
	state.Hold = &holdState{ID: "hold-1", Version: 1, Reason: "strict live review"}
	state.CurrentAnchor, _ = testAnchor(2, "implement", 0)
	result := reviewerResult{Verdict: verdict{Decision: verdictCorrect, Anchor: a1, Rationale: "earlier tactic drifted", Correction: "reconcile the scope", EvidenceRefs: []string{"e1"}}}
	transition, err := state.applyReviewerResult(result, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(transition.Actions) != 2 || transition.Actions[0].Kind != actionSteer || !transition.Actions[0].Advisory || transition.Actions[1].Kind != actionStartReview {
		t.Fatalf("held stale correction policy: %#v", transition.Actions)
	}
	for _, action := range transition.Actions {
		if action.Kind == actionAcquireHold || action.Kind == actionPause {
			t.Fatalf("stale correction added a redundant pause/hold: %#v", transition.Actions)
		}
	}
}

func TestReviewerWireHasNoCostAuthority(t *testing.T) {
	a := anchor{SessionSequence: 1, PlanVersion: 1, ActiveStep: "build", TreeDigest: "tree", TurnRef: "turn"}
	raw := []byte(`{"verdict":{"decision":"continue","anchor":{"session_sequence":1,"plan_version":1,"active_step":"build","tree_digest":"tree"},"rationale":"ok"},"cost_usd":0.01}`)
	if _, err := decodeReviewerResult(raw); err == nil {
		t.Fatal("cost field unexpectedly entered token-only reviewer contract")
	}
	good, _ := json.Marshal(reviewerResult{Verdict: verdict{Decision: verdictContinue, Anchor: a, Rationale: "ok"}})
	if _, err := decodeReviewerResult(good); err != nil {
		t.Fatal(err)
	}
}

func TestDetectReviewResultPrefersValidVerdictOverTerminalNoise(t *testing.T) {
	a := anchor{SessionSequence: 1, PlanVersion: 1, ActiveStep: "build", TreeDigest: "tree", TurnRef: "turn"}
	valid, _ := json.Marshal(reviewerResult{Verdict: verdict{Decision: verdictContinue, Anchor: a, Rationale: "ok"}})
	raw, diagnostic, ok := detectReviewResult([]agentMessage{{Role: "assistant", Content: string(valid)}, {Role: "assistant", Content: "provider cleanup failed"}})
	if !ok || len(raw) == 0 || diagnostic == "" {
		t.Fatalf("result=%s diagnostic=%q ok=%v", raw, diagnostic, ok)
	}
}
