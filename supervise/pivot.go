package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

func pivotRenderID(replacementDigest string) string {
	return "supervise-pivot-" + digestString(replacementDigest)[:32]
}

func baselineFromContract(contract supervisionContract) baseline {
	return baseline{
		Objective: contract.Objective, Constraints: append([]string(nil), contract.Constraints...),
		NonGoals: append([]string(nil), contract.NonGoals...), AcceptanceCriteria: append([]string(nil), contract.Acceptance...),
		Plan: append([]baselineStep(nil), contract.Plan...), DefinitionOfDone: append([]string(nil), contract.DefinitionDone...),
		Verification: append([]string(nil), contract.Verification...), Risks: append([]string(nil), contract.Risks...),
	}
}

func cloneBaseline(in baseline) baseline {
	out := in
	out.Constraints = append([]string(nil), in.Constraints...)
	out.NonGoals = append([]string(nil), in.NonGoals...)
	out.AcceptanceCriteria = append([]string(nil), in.AcceptanceCriteria...)
	out.Plan = append([]baselineStep(nil), in.Plan...)
	out.DefinitionOfDone = append([]string(nil), in.DefinitionOfDone...)
	out.Verification = append([]string(nil), in.Verification...)
	out.Risks = append([]string(nil), in.Risks...)
	return out
}

func clonePivotCandidate(in *pivotCandidateState) *pivotCandidateState {
	if in == nil {
		return nil
	}
	out := *in
	out.Replacement = cloneBaseline(in.Replacement)
	out.ReviewEvidenceRefs = append([]string(nil), in.ReviewEvidenceRefs...)
	return &out
}

func (p *pivotCandidateState) validateForState(state runState, contract supervisionContract) error {
	if p == nil {
		return nil
	}
	if !boundedRequired(p.Rationale, 4096) || p.Anchor.validate() != nil || p.Replacement.validate() != nil ||
		p.ReplacementDigest != "sha256:"+digestString(mustBaselineJSON(p.Replacement)) ||
		(p.Classification != pivotClassificationPlanOnly && p.Classification != pivotClassificationContract) {
		return errors.New("durable pivot contains an invalid structured proposal")
	}
	switch p.Stage {
	case pivotStageReviewing, pivotStageAwaitingUser, pivotStageCleanupPending:
		if p.EditedVersion != 0 {
			return errors.New("unselected pivot contains an edited artifact version")
		}
	case pivotStageEditIntent:
		if p.EditExpectedVersion != state.ContractVersion || p.EditedVersion != 0 {
			return errors.New("pivot edit intent is not bound to the selected artifact version")
		}
	case pivotStageReleasePending:
		if p.EditedVersion != state.ContractVersion || p.EditExpectedVersion+1 != p.EditedVersion ||
			mustBaselineJSON(p.Replacement) != mustBaselineJSON(baselineFromContract(contract)) {
			return errors.New("applied pivot cleanup is not bound to the selected replacement version")
		}
	default:
		return fmt.Errorf("durable pivot has unknown stage %q", p.Stage)
	}
	return nil
}

func classifyPivotReplacement(contract supervisionContract, replacement baseline) (string, string, error) {
	if err := contract.validate(); err != nil {
		return "", "", err
	}
	if err := replacement.validate(); err != nil {
		return "", "", err
	}
	old := baselineFromContract(contract)
	oldRaw, _ := json.Marshal(old)
	newRaw, _ := json.Marshal(replacement)
	if bytes.Equal(oldRaw, newRaw) {
		return "", "", errors.New("pivot replacement is identical to the selected contract")
	}
	old.Plan, replacement.Plan = nil, nil
	oldWithoutPlan, _ := json.Marshal(old)
	newWithoutPlan, _ := json.Marshal(replacement)
	classification := pivotClassificationContract
	if bytes.Equal(oldWithoutPlan, newWithoutPlan) {
		classification = pivotClassificationPlanOnly
	}
	return classification, "sha256:" + digestString(string(newRaw)), nil
}

func (s *runState) applyPivotReviewerVerdict(v verdict, reviewed *pivotCandidateState) (transition, error) {
	if s.PendingPivot == nil || reviewed == nil || s.PendingPivot.Stage != pivotStageReviewing ||
		s.PendingPivot.ReplacementDigest != reviewed.ReplacementDigest || s.PendingPivot.Anchor != v.Anchor {
		return transition{}, errors.New("pivot verdict does not match the exact pending replacement")
	}
	s.PendingPivot.ReviewEvidenceRefs = boundedModelEvidenceRefs(v.EvidenceRefs)
	s.LastVerdict = &v
	s.WatchdogHandoff = cloneWatchdogHandoff(v.Handoff)
	release := func(reason string) transition {
		s.PendingPivot.Stage = pivotStageCleanupPending
		if s.Hold == nil {
			return transition{Note: reason}
		}
		return transition{Actions: []action{{Kind: actionReleaseHold, Reason: reason}}, Note: reason}
	}
	switch v.Decision {
	case verdictApprove:
		if s.PendingPivot.Classification == pivotClassificationPlanOnly && s.Config.PivotApproval == "watchdog" {
			s.PendingPivot.Stage = pivotStageEditIntent
			s.PendingPivot.EditExpectedVersion = s.ContractVersion
			return transition{Note: "watchdog approved configured plan-only pivot; exact artifact edit is staged"}, nil
		}
		s.PendingPivot.Stage = pivotStageAwaitingUser
		return transition{Note: "pivot awaits explicit quality confirmation through /supervise resume"}, nil
	case verdictContinue:
		return release("watchdog rejected the proposed pivot"), nil
	case verdictCorrect:
		change := release("watchdog requested a different structured pivot proposal")
		change.Actions = append(change.Actions, action{Kind: actionSteer, Correction: v.Correction, Reason: v.Rationale})
		return change, nil
	case verdictPause:
		s.PendingPivot = nil
		return transition{Actions: []action{{Kind: actionPause, Reason: v.Rationale}}, Note: "pivot review requested pause"}, nil
	case verdictStop:
		s.PendingPivot = nil
		return transition{Actions: []action{{Kind: actionStop, Reason: v.Rationale}}, Note: "pivot review requested stop"}, nil
	default:
		return transition{}, fmt.Errorf("unsupported pivot verdict %q", v.Decision)
	}
}

func (s *runState) applySelectedPivot(previous, contract supervisionContract) error {
	if s.PendingPivot == nil || s.PendingPivot.Stage != pivotStageEditIntent || contract.ArtifactID != s.ContractID ||
		contract.ArtifactVersion != s.PendingPivot.EditExpectedVersion+1 || contract.RunID != s.RunID || !configsEqual(contract.Config, s.Config) {
		return errors.New("edited pivot candidate does not match the durable transition intent")
	}
	if got := "sha256:" + digestString(mustBaselineJSON(baselineFromContract(contract))); got != s.PendingPivot.ReplacementDigest {
		return errors.New("edited pivot candidate does not match the reviewed replacement")
	}
	if s.CurrentAnchor.PlanVersion == ^uint64(0) {
		return errors.New("plan version cannot advance beyond uint64")
	}
	pivot := s.PendingPivot
	oldCriteria := cloneCriterionStates(s.Criteria)
	// The caller supplies the old contract separately through the durable pivot
	// replacement. Criterion evidence is retained only at unchanged exact
	// indices; moving or rewriting a criterion invalidates its prior evidence.
	newCriteria := make([]criterionProgressState, len(contract.Acceptance))
	for index := range newCriteria {
		newCriteria[index].CriterionIndex = index
		if index < len(previous.Acceptance) && index < len(oldCriteria) && previous.Acceptance[index] == contract.Acceptance[index] {
			newCriteria[index].EvidenceRefs = boundedModelEvidenceRefs(oldCriteria[index].EvidenceRefs)
		}
	}
	s.Criteria = newCriteria
	s.CriteriaTotal = len(newCriteria)
	s.ContractVersion = contract.ArtifactVersion
	s.PlanSteps = make([]string, len(contract.Plan))
	for index := range contract.Plan {
		s.PlanSteps[index] = contract.Plan[index].ID
	}
	s.PlanTotal, s.CompletedSteps = len(s.PlanSteps), 0
	s.CurrentAnchor.PlanVersion++
	s.CurrentAnchor.ActiveStep = s.PlanSteps[0]
	s.PendingStepClaim, s.PendingCompletion = nil, nil
	s.PendingHostVerification, s.LastHostVerification = nil, nil
	s.HostVerificationEffectPending = false
	s.VerificationGates = nil
	s.CompletionVerdict, s.CompletedAnchor = nil, nil
	s.Completed, s.CompletionHandedOff, s.CompletionID = false, false, ""
	s.Detector = detectorState{LastEmittedSeq: map[string]uint64{}}
	s.QueuedSignals, s.CorrectionFollowUp = nil, nil
	s.FailedCorrectionCount = 0
	s.LastVerdict = nil
	s.WatchdogHandoff = watchdogHandoff{}
	s.AdvisorySteering = nil
	pivot.Stage, pivot.EditedVersion = pivotStageReleasePending, contract.ArtifactVersion
	s.PendingPivot = pivot
	return nil
}

func mustBaselineJSON(value baseline) string { raw, _ := json.Marshal(value); return string(raw) }
