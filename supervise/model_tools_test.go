package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func activeToolState(t *testing.T) (runState, supervisionContract) {
	t.Helper()
	contract := supervisionContract{
		ArtifactID: "contract-1", ArtifactVersion: 3, RunID: "run-test",
		Objective: "finish the bounded change", Acceptance: []string{"implementation", "verification"},
		Plan:           []baselineStep{{ID: "plan-implement", Title: "implement", DoneWhen: "implementation evidence exists"}, {ID: "plan-verify", Title: "verify", DoneWhen: "verification evidence exists"}},
		DefinitionDone: []string{"all criteria have host evidence"}, Verification: []string{"go test ./..."}, Config: defaultConfig(),
	}
	state := testState(t, contract.Config)
	state.ContractID, state.ContractVersion = contract.ArtifactID, contract.ArtifactVersion
	state.CriteriaTotal = len(contract.Acceptance)
	state.Criteria = []criterionProgressState{{CriterionIndex: 0}, {CriterionIndex: 1}}
	state.PlanTotal, state.PlanSteps = len(contract.Plan), []string{"plan-implement", "plan-verify"}
	state.CurrentAnchor = anchor{
		SessionSequence: 7, PlanVersion: 1, ActiveStep: "plan-implement", TreeDigest: "tree-current",
		TurnRef: "git:refs/sessions/worker-1/tree@0123456789abcdef0123456789abcdef01234567#turn-7-iteration-1",
	}
	state.ObservationSequence = 7
	return state, contract
}

func addCurrentVerificationGates(state *runState, contract supervisionContract) {
	state.VerificationGates = map[string]verificationGateState{}
	var commandDigests []string
	for _, command := range contract.Verification {
		commandDigest := "sha256:" + digestString(command)
		commandDigests = append(commandDigests, commandDigest)
		state.VerificationGates[commandDigest] = verificationGateState{
			CommandDigest: commandDigest, ResultDigest: "sha256:verification-result", Passed: true,
			Anchor: state.CurrentAnchor, EvidenceRefs: []string{"trace:verification"},
		}
	}
	state.LastHostVerification = &hostVerificationResultState{
		ID: "verification-current", Source: state.CurrentAnchor,
		SuiteDigest: "sha256:" + digestString("operator-suite"), CommandDigests: commandDigests,
		Outcome: "commands_succeeded", EvidenceRefs: []string{"trace:verification"}, Usable: true,
	}
}

func TestReportProgressIsBoundToContractCriterionAndExactStep(t *testing.T) {
	state, contract := activeToolState(t)
	progress := reportProgressArgs{IdempotencyKey: "progress-1", Anchor: state.CurrentAnchor, CriterionIndex: 0, EvidenceRefs: []string{"tool:compile"}}
	change, result, err := state.applyReportProgress(contract, progress)
	if err != nil || len(change.Actions) != 0 || result.Review != "not_required" || !reflect.DeepEqual(state.Criteria[0].EvidenceRefs, progress.EvidenceRefs) {
		t.Fatalf("progress result=%+v transition=%+v state=%+v err=%v", result, change, state, err)
	}

	claim := reportProgressArgs{IdempotencyKey: "progress-claim", Anchor: state.CurrentAnchor, CriterionIndex: 0, EvidenceRefs: []string{"diff:bounded"}, CompleteActiveStep: "plan-implement"}
	change, result, err = state.applyReportProgress(contract, claim)
	if err != nil || result.Review != "scheduled" || len(change.Actions) != 1 || change.Actions[0].Kind != actionStartReview || state.PendingStepClaim == nil {
		t.Fatalf("step claim result=%+v transition=%+v state=%+v err=%v", result, change, state, err)
	}
	verdict := reviewerResult{Verdict: verdict{Decision: verdictApprove, Anchor: claim.Anchor, Rationale: "host evidence proves this criterion", EvidenceRefs: []string{"diff:bounded"}}}
	if _, err := state.applyReviewerResult(verdict, ""); err != nil {
		t.Fatal(err)
	}
	if state.CompletedSteps != 1 || state.CurrentAnchor.ActiveStep != "plan-verify" || state.PendingStepClaim != nil {
		t.Fatalf("approved step did not advance plugin plan: %+v", state)
	}
}

func TestPlanProgressIsIndependentFromCriterionEvidenceIndex(t *testing.T) {
	state, contract := activeToolState(t)
	args := reportProgressArgs{IdempotencyKey: "bad-step", Anchor: state.CurrentAnchor, CriterionIndex: 1, EvidenceRefs: []string{"evidence:one"}, CompleteActiveStep: state.CurrentAnchor.ActiveStep}
	change, _, err := state.applyReportProgress(contract, args)
	if err != nil || len(change.Actions) != 1 || state.PendingStepClaim == nil || state.PendingStepClaim.CriterionIndex != 1 {
		t.Fatalf("criterion-linked evidence could not support an independent plan step: change=%+v state=%+v err=%v", change, state, err)
	}
	verdict := reviewerResult{Verdict: verdict{Decision: verdictApprove, Anchor: args.Anchor, Rationale: "evidence proves the plan step", EvidenceRefs: []string{"evidence:one"}}}
	if _, err := state.applyReviewerResult(verdict, ""); err != nil || state.CompletedSteps != 1 || state.CurrentAnchor.ActiveStep != contract.Plan[1].ID {
		t.Fatalf("approved plan step did not advance independently: state=%+v err=%v", state, err)
	}
}

func TestModelToolReviewClaimsCannotOverlap(t *testing.T) {
	state, contract := activeToolState(t)
	state.PendingReview = &reviewRequest{ID: "existing", Anchor: state.CurrentAnchor, Purpose: reviewPurposeWatchdog, Attempt: 1, Signals: []signal{{Type: "periodic_turn"}}}
	claim := reportProgressArgs{IdempotencyKey: "overlap-step", Anchor: state.CurrentAnchor, CriterionIndex: 0, EvidenceRefs: []string{"git:evidence"}, CompleteActiveStep: state.CurrentAnchor.ActiveStep}
	if _, _, err := state.applyReportProgress(contract, claim); err == nil || !strings.Contains(err.Error(), "already pending") {
		t.Fatalf("step claim overlapped an independent review: %v", err)
	}
	state.CompletedSteps, state.CurrentAnchor.ActiveStep = state.PlanTotal, "completion"
	state.Criteria[0].EvidenceRefs, state.Criteria[1].EvidenceRefs = []string{"git:c0"}, []string{"git:c1"}
	addCurrentVerificationGates(&state, contract)
	completion := requestCompletionArgs{IdempotencyKey: "overlap-completion", Anchor: state.CurrentAnchor, Criteria: []criterionEvidenceInput{{CriterionIndex: 0, EvidenceRefs: []string{"git:c0"}}, {CriterionIndex: 1, EvidenceRefs: []string{"git:c1"}}}}
	if _, _, err := state.applyRequestCompletion(contract, completion); err == nil || !strings.Contains(err.Error(), "cannot overlap") {
		t.Fatalf("completion claim overlapped an unrelated review: %v", err)
	}
}

func TestModelToolsRequireActiveContractAndExactAnchor(t *testing.T) {
	state := testState(t, defaultConfig())
	args := requestPivotArgs{IdempotencyKey: "pivot-1", Anchor: anchor{SessionSequence: 1, PlanVersion: 1, ActiveStep: "plan-implement", TreeDigest: "tree", TurnRef: "turn"}, Rationale: "new evidence", Replacement: baseline{}}
	if _, _, err := state.applyRequestPivot(supervisionContract{}, args); err == nil || !strings.Contains(err.Error(), "session-scoped") {
		t.Fatalf("inactive model tool accepted: %v", err)
	}

	state, contract := activeToolState(t)
	args.Anchor.TreeDigest = "stale"
	if _, _, err := state.applyRequestPivot(contract, args); err == nil || !strings.Contains(err.Error(), "exact current") {
		t.Fatalf("stale model tool accepted: %v", err)
	}
}

func TestModelToolIdempotencyReplaysOnlyIdenticalInput(t *testing.T) {
	state, contract := activeToolState(t)
	replacement := baselineFromContract(contract)
	replacement.Plan[0].Title = "implement the verified alternate"
	args := requestPivotArgs{IdempotencyKey: "pivot-1", Anchor: state.CurrentAnchor, Rationale: "the prior approach is blocked", Replacement: replacement}
	if _, result, err := state.applyRequestPivot(contract, args); err != nil || result.IdempotentReplay {
		t.Fatalf("first request result=%+v err=%v", result, err)
	}
	receipts, sequence := len(state.ToolReceipts), state.ObservationSequence
	if change, result, err := state.applyRequestPivot(contract, args); err != nil || !result.IdempotentReplay || len(change.Actions) != 0 {
		t.Fatalf("identical retry result=%+v transition=%+v err=%v", result, change, err)
	}
	if len(state.ToolReceipts) != receipts || state.ObservationSequence != sequence {
		t.Fatal("idempotent replay mutated durable policy state")
	}
	state.CurrentAnchor = anchor{SessionSequence: 8, PlanVersion: 1, ActiveStep: "plan-implement", TreeDigest: "tree-next", TurnRef: "turn-next"}
	if _, result, err := state.applyRequestPivot(contract, args); err != nil || !result.IdempotentReplay {
		t.Fatalf("durable retry after anchor advancement result=%+v err=%v", result, err)
	}
	args.Replacement.Plan[0].Title = "a different change"
	if _, _, err := state.applyRequestPivot(contract, args); err == nil || !strings.Contains(err.Error(), "reused") {
		t.Fatalf("conflicting idempotency reuse accepted: %v", err)
	}
}

func TestRequestPivotStoresBoundedCandidateAndSchedulesReview(t *testing.T) {
	state, contract := activeToolState(t)
	replacement := baselineFromContract(contract)
	replacement.Plan[0].Title = "replace the approach while retaining scope"
	args := requestPivotArgs{IdempotencyKey: "pivot-2", Anchor: state.CurrentAnchor, Rationale: "verification disproved the premise", Replacement: replacement}
	change, result, err := state.applyRequestPivot(contract, args)
	if err != nil || result.Review != "scheduled" || len(change.Actions) != 2 || change.Actions[0].Kind != actionAcquireHold || state.PendingPivot == nil || state.PendingPivot.ReplacementDigest == "" {
		t.Fatalf("pivot result=%+v transition=%+v state=%+v err=%v", result, change, state, err)
	}
	args.IdempotencyKey = "pivot-oversized"
	args.Rationale = strings.Repeat("x", 4097)
	if _, _, err := state.applyRequestPivot(contract, args); err == nil {
		t.Fatal("oversized pivot rationale was accepted")
	}
}

func TestRequestCompletionRequiresCoverageAndIsAtomic(t *testing.T) {
	state, contract := activeToolState(t)
	state.CompletedSteps = state.PlanTotal
	state.CurrentAnchor.ActiveStep = "completion"
	addCurrentVerificationGates(&state, contract)
	state.Criteria[0].EvidenceRefs = []string{"diff:implementation"}
	args := requestCompletionArgs{IdempotencyKey: "complete-1", Anchor: state.CurrentAnchor, Criteria: []criterionEvidenceInput{{CriterionIndex: 1, EvidenceRefs: []string{"verification:go-test"}}}}
	change, result, err := state.applyRequestCompletion(contract, args)
	if err != nil || result.Review != "scheduled" || len(change.Actions) != 2 || change.Actions[0].Kind != actionAcquireHold || change.Actions[1].Kind != actionStartReview || state.PendingCompletion == nil || len(state.PendingCompletion.EvidenceRefs) != 3 {
		t.Fatalf("completion result=%+v transition=%+v state=%+v err=%v", result, change, state, err)
	}
	if got := state.PendingCompletion.Criteria; len(got) != 2 || got[0].CriterionIndex != 0 || !reflect.DeepEqual(got[0].EvidenceRefs, []string{"diff:implementation"}) || got[1].CriterionIndex != 1 {
		t.Fatalf("completion verifier lost previously recorded criterion mapping: %#v", got)
	}

	state, contract = activeToolState(t)
	state.CompletedSteps = state.PlanTotal
	state.CurrentAnchor.ActiveStep = "completion"
	before, _ := json.Marshal(state)
	args = requestCompletionArgs{IdempotencyKey: "complete-missing", Anchor: state.CurrentAnchor, Criteria: []criterionEvidenceInput{{CriterionIndex: 1, EvidenceRefs: []string{"verification:go-test"}}}}
	if _, _, err := state.applyRequestCompletion(contract, args); err == nil || !strings.Contains(err.Error(), "criterion 0") {
		t.Fatalf("incomplete coverage accepted: %v", err)
	}
	after, _ := json.Marshal(state)
	if string(after) != string(before) {
		t.Fatalf("rejected completion mutated state\nbefore=%s\nafter=%s", before, after)
	}
}

func TestCriterionEvidenceReferencesMustBeUnique(t *testing.T) {
	state, contract := activeToolState(t)
	args := reportProgressArgs{IdempotencyKey: "duplicate-evidence", Anchor: state.CurrentAnchor, CriterionIndex: 0, EvidenceRefs: []string{"trace:one", "trace:one"}}
	if _, _, err := state.applyReportProgress(contract, args); err == nil || !strings.Contains(err.Error(), "unique") {
		t.Fatalf("duplicate evidence accepted: %v", err)
	}
}

func TestModelToolReviewRetainsBoundedEvidenceSet(t *testing.T) {
	state, contract := activeToolState(t)
	refs := make([]string, maxEvidenceRefs)
	for index := range refs {
		refs[index] = "trace:" + string(rune('a'+index))
	}
	args := reportProgressArgs{IdempotencyKey: "full-evidence", Anchor: state.CurrentAnchor, CriterionIndex: 0, EvidenceRefs: refs, CompleteActiveStep: state.CurrentAnchor.ActiveStep}
	if _, _, err := state.applyReportProgress(contract, args); err != nil {
		t.Fatal(err)
	}
	if state.PendingReview == nil || len(state.PendingReview.Signals) != 1 || len(state.PendingReview.Signals[0].EvidenceRefs) != maxEvidenceRefs {
		t.Fatalf("review evidence was over-truncated: %#v", state.PendingReview)
	}
}

func TestModelToolDecoderRejectsUnknownAndTrailingInput(t *testing.T) {
	var args requestPivotArgs
	if err := decodeModelToolArgs([]byte(`{"idempotency_key":"x","anchor":{},"rationale":"r","replacement":{},"native_policy":true}`), &args); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown policy field accepted: %v", err)
	}
	if err := decodeModelToolArgs([]byte(`{} {}`), &args); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing JSON accepted: %v", err)
	}
}

func TestCancelledWorkflowRejectsModelTools(t *testing.T) {
	state, contract := activeToolState(t)
	if err := state.cancelWorkflow("operator cancelled"); err != nil {
		t.Fatal(err)
	}
	_, _, err := state.applyReportProgress(contract, reportProgressArgs{
		IdempotencyKey: "after-cancel", Anchor: state.CurrentAnchor,
		CriterionIndex: 0, EvidenceRefs: []string{"diff:late"},
	})
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("cancelled workflow accepted model tool: %v", err)
	}
}

func TestManifestDeclaresCanonicalStrictModelTools(t *testing.T) {
	raw, err := os.ReadFile("plugin.manifest.template.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Capabilities []string `json:"capabilities"`
		Tools        []struct {
			Name   string `json:"name"`
			Class  string `json:"class"`
			Schema string `json:"schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	hasCompletion := false
	for _, capability := range manifest.Capabilities {
		if capability == "session:complete" {
			hasCompletion = true
			break
		}
	}
	if !hasCompletion {
		t.Fatal("manifest omits generic successful-completion capability")
	}
	want := []string{"supervise__report_progress", "supervise__request_pivot", "supervise__request_completion"}
	if len(manifest.Tools) != len(want) {
		t.Fatalf("manifest tools=%d want=%d", len(manifest.Tools), len(want))
	}
	for index, tool := range manifest.Tools {
		if tool.Name != want[index] || tool.Class != "StateMutating" {
			t.Fatalf("manifest tool %d = %+v", index, tool)
		}
		var schema map[string]any
		if err := json.Unmarshal([]byte(tool.Schema), &schema); err != nil {
			t.Fatalf("%s schema: %v", tool.Name, err)
		}
		if schema["additionalProperties"] != false {
			t.Fatalf("%s schema is not closed", tool.Name)
		}
	}
}
