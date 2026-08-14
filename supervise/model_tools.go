package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const maxToolReceipts = 128

type criterionEvidenceInput struct {
	CriterionIndex int      `json:"criterion_index"`
	EvidenceRefs   []string `json:"evidence_refs"`
}

type reportProgressArgs struct {
	IdempotencyKey     string   `json:"idempotency_key"`
	Anchor             anchor   `json:"anchor"`
	CriterionIndex     int      `json:"criterion_index"`
	EvidenceRefs       []string `json:"evidence_refs"`
	CompleteActiveStep string   `json:"complete_active_step,omitempty"`
}

type requestPivotArgs struct {
	IdempotencyKey string   `json:"idempotency_key"`
	Anchor         anchor   `json:"anchor"`
	Rationale      string   `json:"rationale"`
	Replacement    baseline `json:"replacement"`
}

type requestCompletionArgs struct {
	IdempotencyKey string                   `json:"idempotency_key"`
	Anchor         anchor                   `json:"anchor"`
	Criteria       []criterionEvidenceInput `json:"criteria"`
}

type criterionProgressState struct {
	CriterionIndex int      `json:"criterion_index"`
	EvidenceRefs   []string `json:"evidence_refs,omitempty"`
}

type stepCompletionClaim struct {
	ActiveStep     string   `json:"active_step"`
	CriterionIndex int      `json:"criterion_index"`
	Anchor         anchor   `json:"anchor"`
	EvidenceRefs   []string `json:"evidence_refs"`
}

type pivotCandidateState struct {
	Rationale           string   `json:"rationale"`
	Anchor              anchor   `json:"anchor"`
	Replacement         baseline `json:"replacement"`
	ReplacementDigest   string   `json:"replacement_digest"`
	Classification      string   `json:"classification"`
	Stage               string   `json:"stage"`
	EditExpectedVersion uint64   `json:"edit_expected_version,omitempty"`
	EditedVersion       uint64   `json:"edited_version,omitempty"`
	ReviewEvidenceRefs  []string `json:"review_evidence_refs,omitempty"`
}

const (
	pivotClassificationPlanOnly = "plan_only"
	pivotClassificationContract = "contract_change"
	pivotStageReviewing         = "reviewing"
	pivotStageAwaitingUser      = "awaiting_user_confirmation"
	pivotStageEditIntent        = "edit_intent"
	pivotStageReleasePending    = "applied_release_pending"
	pivotStageCleanupPending    = "rejected_release_pending"
)

type completionCandidateState struct {
	Anchor                  anchor                   `json:"anchor"`
	Criteria                []criterionEvidenceInput `json:"criteria"`
	EvidenceRefs            []string                 `json:"evidence_refs"`
	HostVerificationOutcome string                   `json:"host_verification_outcome"`
	HostVerificationSuite   string                   `json:"host_verification_suite_digest"`
	Stage                   string                   `json:"stage"`
}

type toolReceipt struct {
	Tool          string `json:"tool"`
	KeyDigest     string `json:"key_digest"`
	RequestDigest string `json:"request_digest"`
	Review        string `json:"review"`
}

type modelToolResult struct {
	Status           string `json:"status"`
	Review           string `json:"review"`
	IdempotentReplay bool   `json:"idempotent_replay,omitempty"`
}

func decodeModelToolArgs(raw []byte, target any) error {
	return decodeStrictBytes(raw, target)
}

func decodeStrictBytes(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return errors.New("JSON payload must be 1..1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON payload has trailing value")
		}
		return err
	}
	return nil
}

func (s *runState) applyReportProgress(contract supervisionContract, args reportProgressArgs) (transition, modelToolResult, error) {
	const toolName = "supervise__report_progress"
	replay, result, requestDigest, err := s.prepareModelTool(contract, toolName, args.IdempotencyKey, args.Anchor, args)
	if err != nil || replay {
		return transition{}, result, err
	}
	if err := s.validateCriterionEvidence(contract, criterionEvidenceInput{CriterionIndex: args.CriterionIndex, EvidenceRefs: args.EvidenceRefs}); err != nil {
		return transition{}, modelToolResult{}, err
	}
	if args.CompleteActiveStep != "" && (s.CurrentAnchor.ActiveStep == "" || args.CompleteActiveStep != s.CurrentAnchor.ActiveStep) {
		return transition{}, modelToolResult{}, errors.New("active-step completion claim does not match the exact current plan anchor")
	}
	if args.CompleteActiveStep != "" && (s.PendingReview != nil || s.PendingStepClaim != nil) {
		return transition{}, modelToolResult{}, errors.New("an independent review or step-completion claim is already pending")
	}
	review := "not_required"
	var change transition
	if args.CompleteActiveStep != "" {
		claim := &stepCompletionClaim{ActiveStep: args.CompleteActiveStep, CriterionIndex: args.CriterionIndex, Anchor: args.Anchor, EvidenceRefs: boundedModelEvidenceRefs(args.EvidenceRefs)}
		event := s.modelToolEvent(eventStepClaimed, toolName, requestDigest, args.EvidenceRefs)
		change, err = s.observe(event)
		if err != nil {
			return transition{}, modelToolResult{}, err
		}
		s.PendingStepClaim = claim
		review = reviewDisposition(change)
	}
	s.mergeCriterionEvidence(args.CriterionIndex, args.EvidenceRefs)
	result = modelToolResult{Status: "recorded", Review: review}
	s.recordToolReceipt(toolName, args.IdempotencyKey, requestDigest, review)
	return change, result, nil
}

func (s *runState) applyRequestPivot(contract supervisionContract, args requestPivotArgs) (transition, modelToolResult, error) {
	const toolName = "supervise__request_pivot"
	replay, result, requestDigest, err := s.prepareModelTool(contract, toolName, args.IdempotencyKey, args.Anchor, args)
	if err != nil || replay {
		return transition{}, result, err
	}
	if strings.TrimSpace(args.Rationale) == "" || len(args.Rationale) > 4096 {
		return transition{}, modelToolResult{}, errors.New("pivot request requires bounded rationale")
	}
	if err := args.Replacement.validate(); err != nil {
		return transition{}, modelToolResult{}, fmt.Errorf("pivot replacement: %w", err)
	}
	if s.PendingPivot != nil || s.PendingReview != nil {
		return transition{}, modelToolResult{}, errors.New("a pivot or another independent review is already pending")
	}
	classification, digest, err := classifyPivotReplacement(contract, args.Replacement)
	if err != nil {
		return transition{}, modelToolResult{}, err
	}
	pivot := &pivotCandidateState{
		Rationale: args.Rationale, Anchor: args.Anchor, Replacement: cloneBaseline(args.Replacement),
		ReplacementDigest: digest, Classification: classification, Stage: pivotStageReviewing,
	}
	s.PendingPivot = pivot
	event := s.modelToolEvent(eventPivotRequested, toolName, requestDigest, nil)
	change, err := s.observe(event)
	if err != nil {
		s.PendingPivot = nil
		return transition{}, modelToolResult{}, err
	}
	if s.PendingReview == nil {
		s.PendingPivot = nil
		return transition{}, modelToolResult{}, errors.New("pivot request did not create an independent review")
	}
	s.PendingReview.Pivot = clonePivotCandidate(pivot)
	if s.Hold == nil {
		s.Hold = &holdState{Reason: "structured pivot requires current-anchor quality review"}
		change.Actions = append([]action{{Kind: actionAcquireHold, Reason: s.Hold.Reason}}, change.Actions...)
	}
	review := reviewDisposition(change)
	result = modelToolResult{Status: "recorded", Review: review}
	s.recordToolReceipt(toolName, args.IdempotencyKey, requestDigest, review)
	return change, result, nil
}

func (s *runState) applyRequestCompletion(contract supervisionContract, args requestCompletionArgs) (transition, modelToolResult, error) {
	const toolName = "supervise__request_completion"
	replay, result, requestDigest, err := s.prepareModelTool(contract, toolName, args.IdempotencyKey, args.Anchor, args)
	if err != nil || replay {
		return transition{}, result, err
	}
	if len(args.Criteria) == 0 || len(args.Criteria) > len(contract.Acceptance) {
		return transition{}, modelToolResult{}, errors.New("completion request requires bounded criterion-linked evidence")
	}
	if s.PendingCompletion != nil {
		return transition{}, modelToolResult{}, errors.New("a completion candidate is already under review")
	}
	if s.PendingReview != nil {
		return transition{}, modelToolResult{}, errors.New("completion cannot overlap another independent review")
	}
	if s.CompletedSteps != s.PlanTotal || s.CurrentAnchor.ActiveStep != "completion" || s.PendingStepClaim != nil {
		return transition{}, modelToolResult{}, errors.New("completion is unavailable while an approved plan step remains active")
	}
	criteria := cloneCriterionStates(s.Criteria)
	seen := map[int]bool{}
	for _, item := range args.Criteria {
		if seen[item.CriterionIndex] {
			return transition{}, modelToolResult{}, errors.New("completion request repeats a criterion")
		}
		seen[item.CriterionIndex] = true
		if err := s.validateCriterionEvidence(contract, item); err != nil {
			return transition{}, modelToolResult{}, err
		}
		mergeCriterionEvidence(&criteria, item.CriterionIndex, item.EvidenceRefs)
	}
	for index := range contract.Acceptance {
		if index >= len(criteria) || len(criteria[index].EvidenceRefs) == 0 {
			return transition{}, modelToolResult{}, fmt.Errorf("completion request has no evidence for criterion %d", index)
		}
	}
	gateEvidence, err := s.hostVerificationGateEvidence()
	if err != nil {
		return transition{}, modelToolResult{}, err
	}
	allEvidence := boundedModelEvidenceRefs(append(criterionEvidenceRefs(criteria), gateEvidence...))
	event := s.modelToolEvent(eventCompletionClaimed, toolName, requestDigest, allEvidence)
	change, err := s.observe(event)
	if err != nil {
		return transition{}, modelToolResult{}, err
	}
	s.Criteria = criteria
	s.PendingCompletion = &completionCandidateState{
		Anchor: args.Anchor, Criteria: criterionInputsFromStates(criteria), EvidenceRefs: allEvidence,
		HostVerificationOutcome: s.LastHostVerification.Outcome, HostVerificationSuite: s.LastHostVerification.SuiteDigest,
		Stage: reviewPurposeWatchdog,
	}
	if s.Hold == nil {
		s.Hold = &holdState{Reason: "completion candidate requires current-anchor watchdog and independent verifier"}
		change.Actions = append([]action{{Kind: actionAcquireHold, Reason: s.Hold.Reason}}, change.Actions...)
	}
	review := reviewDisposition(change)
	result = modelToolResult{Status: "recorded", Review: review}
	s.recordToolReceipt(toolName, args.IdempotencyKey, requestDigest, review)
	return change, result, nil
}

func (s *runState) prepareModelTool(contract supervisionContract, toolName, idempotencyKey string, requestedAnchor anchor, request any) (bool, modelToolResult, string, error) {
	if s == nil || s.Schema != policySchema || s.ContractID == "" || s.ContractVersion == 0 || s.CriteriaTotal == 0 {
		return false, modelToolResult{}, "", errors.New("no selected session-scoped supervision contract")
	}
	if contract.ArtifactID != s.ContractID || contract.ArtifactVersion != s.ContractVersion || len(contract.Acceptance) != s.CriteriaTotal || len(contract.Plan) != s.PlanTotal || !configsEqual(contract.Config, s.Config) {
		return false, modelToolResult{}, "", errors.New("selected supervision contract does not match durable application state")
	}
	if s.Completed {
		return false, modelToolResult{}, "", errors.New("supervision run is already verifier-complete")
	}
	if s.Cancelled {
		return false, modelToolResult{}, "", errors.New("supervision run is durably cancelled")
	}
	if !validIdempotencyKey(idempotencyKey) {
		return false, modelToolResult{}, "", errors.New("idempotency_key must be 1..64 visible identifier characters")
	}
	requestDigest, err := canonicalDigest(request)
	if err != nil {
		return false, modelToolResult{}, "", err
	}
	keyDigest := digestString(idempotencyKey)
	for _, receipt := range s.ToolReceipts {
		if receipt.Tool != toolName || receipt.KeyDigest != keyDigest {
			continue
		}
		if receipt.RequestDigest != requestDigest {
			return false, modelToolResult{}, "", errors.New("idempotency key was reused with different tool input")
		}
		return true, modelToolResult{Status: "recorded", Review: receipt.Review, IdempotentReplay: true}, requestDigest, nil
	}
	if err := s.CurrentAnchor.validate(); err != nil || requestedAnchor != s.CurrentAnchor {
		return false, modelToolResult{}, "", errors.New("model tool anchor is not the exact current supervision plan anchor")
	}
	if len(s.ToolReceipts) >= maxToolReceipts {
		return false, modelToolResult{}, "", errors.New("bounded model-tool idempotency ledger is full")
	}
	return false, modelToolResult{}, requestDigest, nil
}

func (s *runState) validateCriterionEvidence(contract supervisionContract, item criterionEvidenceInput) error {
	if item.CriterionIndex < 0 || item.CriterionIndex >= len(contract.Acceptance) {
		return errors.New("criterion_index is outside the selected supervision contract")
	}
	if len(item.EvidenceRefs) == 0 {
		return errors.New("criterion progress requires evidence_refs")
	}
	if err := validateEvidenceRefs(item.EvidenceRefs); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, value := range item.EvidenceRefs {
		if seen[value] {
			return errors.New("criterion evidence_refs must be unique")
		}
		seen[value] = true
	}
	return nil
}

func (s *runState) mergeCriterionEvidence(index int, evidence []string) {
	mergeCriterionEvidence(&s.Criteria, index, evidence)
}

func mergeCriterionEvidence(criteria *[]criterionProgressState, index int, evidence []string) {
	for len(*criteria) <= index {
		*criteria = append(*criteria, criterionProgressState{CriterionIndex: len(*criteria)})
	}
	seen := map[string]bool{}
	for _, value := range (*criteria)[index].EvidenceRefs {
		seen[value] = true
	}
	for _, value := range evidence {
		if !seen[value] && len((*criteria)[index].EvidenceRefs) < maxEvidenceRefs {
			(*criteria)[index].EvidenceRefs = append((*criteria)[index].EvidenceRefs, value)
			seen[value] = true
		}
	}
}

func cloneCriterionStates(values []criterionProgressState) []criterionProgressState {
	result := make([]criterionProgressState, len(values))
	for index, value := range values {
		result[index] = value
		result[index].EvidenceRefs = append([]string(nil), value.EvidenceRefs...)
	}
	return result
}

func (s *runState) modelToolEvent(kind workerEventKind, toolName, requestDigest string, evidence []string) workerEvent {
	return workerEvent{
		ID: "model-tool:" + toolName + ":" + requestDigest[:16], Kind: kind,
		Sequence: s.ObservationSequence + 1, Anchor: s.CurrentAnchor,
		EvidenceRefs: boundedModelEvidenceRefs(evidence),
	}
}

func (s *runState) recordToolReceipt(toolName, key, requestDigest, review string) {
	s.ToolReceipts = append(s.ToolReceipts, toolReceipt{Tool: toolName, KeyDigest: digestString(key), RequestDigest: requestDigest, Review: review})
}

func reviewDisposition(change transition) string {
	for _, item := range change.Actions {
		if item.Kind == actionStartReview {
			return "scheduled"
		}
	}
	if change.Note != "" {
		return "coalesced"
	}
	return "not_required"
}

func criterionEvidenceRefs(criteria []criterionProgressState) []string {
	seen := map[string]bool{}
	var result []string
	for _, item := range criteria {
		for _, value := range item.EvidenceRefs {
			if !seen[value] && len(result) < maxEvidenceRefs {
				seen[value] = true
				result = append(result, value)
			}
		}
	}
	return result
}

func boundedModelEvidenceRefs(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, min(len(values), maxEvidenceRefs))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
		if len(result) == maxEvidenceRefs {
			break
		}
	}
	return result
}

func cloneCriterionInputs(values []criterionEvidenceInput) []criterionEvidenceInput {
	result := make([]criterionEvidenceInput, len(values))
	for index, value := range values {
		result[index] = value
		result[index].EvidenceRefs = append([]string(nil), value.EvidenceRefs...)
	}
	return result
}

func criterionInputsFromStates(values []criterionProgressState) []criterionEvidenceInput {
	result := make([]criterionEvidenceInput, len(values))
	for index, value := range values {
		result[index] = criterionEvidenceInput{
			CriterionIndex: value.CriterionIndex,
			EvidenceRefs:   append([]string(nil), value.EvidenceRefs...),
		}
	}
	return result
}

func canonicalDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func digestString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func validIdempotencyKey(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r) {
			continue
		}
		return false
	}
	return true
}
