package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	hostVerificationStageIntent    = "intent"
	hostVerificationStageRequested = "requested"
)

type hostVerificationRequestState struct {
	Stage                 string   `json:"stage"`
	ID                    string   `json:"id,omitempty"`
	Version               uint64   `json:"version,omitempty"`
	ExpectedWorkerVersion uint64   `json:"expected_worker_version"`
	SourceEventSequence   uint64   `json:"source_event_sequence"`
	Anchor                anchor   `json:"anchor"`
	SourceEvidenceRefs    []string `json:"source_evidence_refs,omitempty"`
	Obsolete              bool     `json:"obsolete,omitempty"`
}

type hostVerificationResultState struct {
	ID                 string   `json:"id"`
	Source             anchor   `json:"source"`
	SuiteDigest        string   `json:"suite_digest"`
	CommandDigests     []string `json:"command_digests,omitempty"`
	Outcome            string   `json:"outcome"`
	FailureKind        string   `json:"failure_kind,omitempty"`
	FailureFingerprint string   `json:"failure_fingerprint,omitempty"`
	EvidenceRefs       []string `json:"evidence_refs,omitempty"`
	Usable             bool     `json:"usable,omitempty"`
	Discarded          bool     `json:"discarded,omitempty"`
}

type hostVerificationSource struct {
	EventSequence   uint64 `json:"event_sequence"`
	SessionSequence uint64 `json:"session_sequence"`
	TurnRef         string `json:"turn_ref"`
	TreeDigest      string `json:"tree_digest"`
}

type hostVerificationCommandFact struct {
	Ordinal            int      `json:"ordinal"`
	CommandDigest      string   `json:"command_digest"`
	ResultDigest       string   `json:"result_digest"`
	Outcome            string   `json:"outcome"`
	FailureKind        string   `json:"failure_kind,omitempty"`
	FailureFingerprint string   `json:"failure_fingerprint,omitempty"`
	EvidenceRefs       []string `json:"evidence_refs,omitempty"`
}

type hostVerificationRecord struct {
	ID                 string                        `json:"id"`
	SessionID          string                        `json:"session_id"`
	Generation         uint64                        `json:"generation"`
	PluginID           string                        `json:"plugin_id"`
	Owner              string                        `json:"owner"`
	RunID              string                        `json:"run_id"`
	WorkerVersion      uint64                        `json:"worker_version"`
	Version            uint64                        `json:"version"`
	WALSequence        uint64                        `json:"wal_sequence"`
	Status             string                        `json:"status"`
	Source             hostVerificationSource        `json:"source"`
	SourceEvidenceRefs []string                      `json:"source_evidence_refs,omitempty"`
	SuiteDigest        string                        `json:"suite_digest,omitempty"`
	CommandDigests     []string                      `json:"command_digests,omitempty"`
	Outcome            string                        `json:"outcome,omitempty"`
	FailureKind        string                        `json:"failure_kind,omitempty"`
	FailureFingerprint string                        `json:"failure_fingerprint,omitempty"`
	Commands           []hostVerificationCommandFact `json:"commands,omitempty"`
	EvidenceRefs       []string                      `json:"evidence_refs,omitempty"`
	CreatedAt          time.Time                     `json:"created_at"`
	UpdatedAt          time.Time                     `json:"updated_at"`
}

// hostVerificationTerminal is the deliberately reduced, factual event shape
// exposed to lifecycle applications. Application identity, worker authority,
// status, WAL order, and timestamps remain authenticated by the outer event or
// broker state and are not accepted from guest-visible event data.
type hostVerificationTerminal struct {
	Schema             string                        `json:"schema"`
	VerificationID     string                        `json:"verification_id"`
	RunID              string                        `json:"run_id"`
	Version            uint64                        `json:"version"`
	Source             hostVerificationSource        `json:"source"`
	SourceEvidenceRefs []string                      `json:"source_evidence_refs,omitempty"`
	SuiteDigest        string                        `json:"suite_digest"`
	CommandDigests     []string                      `json:"command_digests,omitempty"`
	Outcome            string                        `json:"outcome"`
	FailureKind        string                        `json:"failure_kind,omitempty"`
	FailureFingerprint string                        `json:"failure_fingerprint,omitempty"`
	Commands           []hostVerificationCommandFact `json:"commands,omitempty"`
	EvidenceRefs       []string                      `json:"evidence_refs,omitempty"`
}

func validHostFactDigest(value string) bool {
	return len(value) == len("sha256:")+64 && strings.HasPrefix(value, "sha256:") && isLowerHex(strings.TrimPrefix(value, "sha256:"), 64)
}

func validGitTreeDigest(value string) bool {
	return value == "empty" || isLowerHex(value, 40)
}

func hostVerificationEventDigest(data []byte, evidenceRefs []string) string {
	raw, _ := json.Marshal(struct {
		Data         json.RawMessage `json:"data"`
		EvidenceRefs []string        `json:"evidence_refs"`
	}{Data: data, EvidenceRefs: evidenceRefs})
	return "sha256:" + digestString(string(raw))
}

func (p *hostVerificationRequestState) validate() error {
	if p == nil {
		return nil
	}
	if p.ExpectedWorkerVersion == 0 || p.SourceEventSequence == 0 || p.Anchor.validate() != nil {
		return errors.New("host verification intent has an invalid exact worker/source anchor")
	}
	switch p.Stage {
	case hostVerificationStageIntent:
		if p.ID != "" || p.Version != 0 || len(p.SourceEvidenceRefs) != 0 {
			return errors.New("unacknowledged host verification intent contains broker identity")
		}
	case hostVerificationStageRequested:
		if !boundedRequired(p.ID, 256) || p.Version != 1 {
			return errors.New("requested host verification has an invalid broker identity/version")
		}
	default:
		return fmt.Errorf("unknown host verification stage %q", p.Stage)
	}
	if err := validateEvidenceRefs(p.SourceEvidenceRefs); err != nil {
		return err
	}
	return nil
}

func (s *runState) shouldStageHostVerification(facts turnCommittedFacts) bool {
	if s == nil || s.Cancelled || s.Completed || s.CurrentAnchor.ActiveStep != "completion" || s.PendingHostVerification != nil || s.PendingReview != nil || s.PendingPivot != nil || s.PendingStepClaim != nil || s.Hold != nil || s.WorkerRunStatus != workerRunActive || s.WorkerRunVersion == 0 {
		return false
	}
	target := anchor{
		SessionSequence: facts.Anchor.SessionSequence, PlanVersion: s.CurrentAnchor.PlanVersion,
		ActiveStep: "completion", TreeDigest: facts.Anchor.TreeDigest, TurnRef: facts.Anchor.TurnRef,
	}
	return s.LastHostVerification == nil || s.LastHostVerification.Source != target || (s.LastHostVerification.Outcome != "commands_succeeded" && s.LastHostVerification.Outcome != "no_suite")
}

func (s *runState) stageHostVerification(sourceEventSequence uint64, facts turnCommittedFacts) (transition, error) {
	if !s.shouldStageHostVerification(facts) || sourceEventSequence == 0 {
		return transition{}, errors.New("host verification is not available at this turn boundary")
	}
	target := anchor{
		SessionSequence: facts.Anchor.SessionSequence, PlanVersion: s.CurrentAnchor.PlanVersion,
		ActiveStep: "completion", TreeDigest: facts.Anchor.TreeDigest, TurnRef: facts.Anchor.TurnRef,
	}
	s.PendingHostVerification = &hostVerificationRequestState{
		Stage: hostVerificationStageIntent, ExpectedWorkerVersion: s.WorkerRunVersion,
		SourceEventSequence: sourceEventSequence, Anchor: target,
	}
	s.LastHostVerification = nil
	s.VerificationGates = nil
	s.Hold = &holdState{Reason: "operator-configured verification is pending at the exact completion anchor"}
	return transition{Actions: []action{{Kind: actionAcquireHold, Reason: s.Hold.Reason}}, Note: "staged exact asynchronous host verification"}, nil
}

func (s *runState) acceptHostVerificationRequest(record hostVerificationRecord) error {
	pending := s.PendingHostVerification
	if pending == nil || pending.Stage != hostVerificationStageIntent || pending.validate() != nil {
		return errors.New("no exact host verification intent is pending")
	}
	if err := validateRequestedHostVerification(record, s.RunID, pending); err != nil {
		return err
	}
	pending.Stage, pending.ID, pending.Version = hostVerificationStageRequested, record.ID, record.Version
	pending.SourceEvidenceRefs = append([]string(nil), record.SourceEvidenceRefs...)
	return nil
}

func validateRequestedHostVerification(record hostVerificationRecord, runID string, pending *hostVerificationRequestState) error {
	if pending == nil || record.Status != "requested" || record.Version != 1 || !boundedRequired(record.ID, 256) || record.RunID != runID || record.WorkerVersion != pending.ExpectedWorkerVersion || record.Source.EventSequence != pending.SourceEventSequence || record.Source.SessionSequence != pending.Anchor.SessionSequence || record.Source.TurnRef != pending.Anchor.TurnRef || record.Source.TreeDigest != pending.Anchor.TreeDigest || record.WALSequence == 0 || record.SessionID == "" || record.Generation == 0 || record.PluginID == "" || record.Owner != record.PluginID || record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || !record.CreatedAt.Equal(record.UpdatedAt) || len(record.SourceEvidenceRefs) > maxEvidenceRefs || record.SuiteDigest != "" || len(record.CommandDigests) != 0 || record.Outcome != "" || len(record.Commands) != 0 {
		return errors.New("broker returned an invalid requested host verification record")
	}
	if err := validateEvidenceRefs(record.SourceEvidenceRefs); err != nil {
		return err
	}
	return nil
}

func (r hostVerificationTerminal) validate() error {
	if r.Schema != "stado.dev/session-verification-facts/v1" || r.Version == 0 || !boundedRequired(r.VerificationID, 256) || !boundedRequired(r.RunID, 256) || r.Source.EventSequence == 0 || r.Source.SessionSequence == 0 || !boundedRequired(r.Source.TurnRef, 512) || !validGitTreeDigest(r.Source.TreeDigest) || !validHostFactDigest(r.SuiteDigest) || len(r.CommandDigests) > 64 || len(r.Commands) > 64 || len(r.SourceEvidenceRefs) > maxEvidenceRefs || len(r.EvidenceRefs) > maxEvidenceRefs {
		return errors.New("terminal host verification record exceeds its strict factual contract")
	}
	if err := validateEvidenceRefs(r.SourceEvidenceRefs); err != nil {
		return err
	}
	if err := validateEvidenceRefs(r.EvidenceRefs); err != nil {
		return err
	}
	for _, digest := range r.CommandDigests {
		if !validHostFactDigest(digest) {
			return errors.New("terminal host verification has an invalid command digest")
		}
	}
	if len(r.CommandDigests) != len(r.Commands) {
		return errors.New("terminal host verification command plan/fact counts differ")
	}
	seenSucceeded, seenFailed, seenInfrastructure, seenCancelled := 0, 0, 0, 0
	terminalSeen := false
	for index, fact := range r.Commands {
		if fact.Ordinal != index+1 || fact.CommandDigest != r.CommandDigests[index] || !validHostFactDigest(fact.ResultDigest) || len(fact.EvidenceRefs) > maxEvidenceRefs {
			return errors.New("terminal host verification contains an invalid ordered command fact")
		}
		if err := validateEvidenceRefs(fact.EvidenceRefs); err != nil {
			return err
		}
		switch fact.Outcome {
		case "succeeded":
			if terminalSeen || fact.FailureKind != "" || fact.FailureFingerprint != "" {
				return errors.New("terminal host verification contains success after short-circuit or with failure metadata")
			}
			seenSucceeded++
		case "failed", "infrastructure_error", "cancelled":
			if terminalSeen || !validHostVerificationCommandFailureKind(fact.Outcome, fact.FailureKind) || !validHostFactDigest(fact.FailureFingerprint) {
				return errors.New("terminal host verification contains contradictory short-circuit facts")
			}
			terminalSeen = true
			seenFailed += boolInt(fact.Outcome == "failed")
			seenInfrastructure += boolInt(fact.Outcome == "infrastructure_error")
			seenCancelled += boolInt(fact.Outcome == "cancelled")
		case "not_run":
			if fact.FailureKind != "" || fact.FailureFingerprint != "" {
				return errors.New("terminal host verification contains failure metadata on a not-run command")
			}
			terminalSeen = true
		default:
			return errors.New("terminal host verification contains an unknown command outcome")
		}
	}
	switch r.Outcome {
	case "commands_succeeded":
		if len(r.Commands) == 0 || seenSucceeded != len(r.Commands) || seenFailed != 0 || seenInfrastructure != 0 || seenCancelled != 0 || r.FailureKind != "" || r.FailureFingerprint != "" {
			return errors.New("successful host verification has no commands or has a failure")
		}
	case "no_suite":
		if len(r.Commands) != 0 || r.FailureKind != "" || r.FailureFingerprint != "" {
			return errors.New("no-suite host verification contains commands or a failure")
		}
	case "command_failed", "infrastructure_error", "cancelled":
		if !validHostVerificationFailureKind(r.Outcome, r.FailureKind) || !validHostFactDigest(r.FailureFingerprint) {
			return errors.New("failed host verification lacks a typed fingerprinted failure")
		}
		if r.Outcome == "command_failed" && (seenFailed != 1 || seenInfrastructure != 0 || seenCancelled != 0) ||
			r.Outcome == "infrastructure_error" && (r.FailureKind == "suite_changed" && (seenSucceeded != 0 || seenFailed != 0 || seenInfrastructure != 0 || seenCancelled != 0) || r.FailureKind != "suite_changed" && (seenInfrastructure != 1 || seenFailed != 0 || seenCancelled != 0)) ||
			r.Outcome == "cancelled" && r.FailureKind != "stale_anchor" && r.FailureKind != "worker_terminal" && (seenCancelled != 1 || seenFailed != 0 || seenInfrastructure != 0) {
			return errors.New("failed host verification outcome does not match its command facts")
		}
	default:
		return fmt.Errorf("unknown terminal host verification outcome %q", r.Outcome)
	}
	return nil
}

func validHostVerificationCommandFailureKind(outcome, kind string) bool {
	switch outcome {
	case "failed":
		return kind == "command_failed"
	case "infrastructure_error":
		return kind == "infrastructure_error"
	case "cancelled":
		return kind == "cancelled"
	default:
		return false
	}
}

func validHostVerificationFailureKind(outcome, kind string) bool {
	switch outcome {
	case "command_failed":
		return kind == "command_failed"
	case "infrastructure_error":
		return kind == "infrastructure_error" || kind == "suite_changed"
	case "cancelled":
		return kind == "cancelled" || kind == "stale_anchor" || kind == "worker_terminal"
	default:
		return false
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *runState) applyHostVerificationTerminal(record hostVerificationTerminal, parent authenticatedAgentParent, brokerSequence uint64, eventDigest string, eventEvidence []string) (transition, bool, error) {
	if brokerSequence == s.LastHostVerificationEventSequence {
		if eventDigest != s.LastHostVerificationEventDigest {
			return transition{}, false, errors.New("host verification event sequence was replayed with different authenticated facts")
		}
		return s.pendingHostVerificationTerminalTransition(), true, nil
	}
	if brokerSequence < s.LastHostVerificationEventSequence || !validHostFactDigest(eventDigest) {
		return transition{}, false, errors.New("host verification event sequence/digest is invalid or moved backwards")
	}
	pending := s.PendingHostVerification
	if pending == nil || pending.Stage != hostVerificationStageRequested || pending.validate() != nil {
		return transition{}, false, errors.New("no requested host verification matches the terminal event")
	}
	if err := record.validate(); err != nil {
		return transition{}, false, err
	}
	if parent.validate() != nil || brokerSequence <= pending.SourceEventSequence || record.VerificationID != pending.ID || record.RunID != s.RunID || record.Version <= pending.Version || record.Source.EventSequence != pending.SourceEventSequence || record.Source.SessionSequence != pending.Anchor.SessionSequence || record.Source.TurnRef != pending.Anchor.TurnRef || record.Source.TreeDigest != pending.Anchor.TreeDigest || !slices.Equal(record.SourceEvidenceRefs, pending.SourceEvidenceRefs) {
		return transition{}, false, errors.New("terminal host verification does not match the exact application/run/source request")
	}
	if err := validateEvidenceRefs(eventEvidence); err != nil {
		return transition{}, false, err
	}
	walEvidence := "broker:wal:lifecycle_application:" + fmt.Sprint(brokerSequence)
	if !slices.Equal(eventEvidence, record.EvidenceRefs) || !slices.Contains(record.EvidenceRefs, walEvidence) {
		return transition{}, false, errors.New("terminal host verification lacks its exact broker WAL evidence")
	}
	for _, fact := range record.Commands {
		if !slices.Contains(fact.EvidenceRefs, walEvidence) {
			return transition{}, false, errors.New("terminal host verification command fact lacks its exact broker WAL evidence")
		}
	}
	s.PendingHostVerification = nil
	s.VerificationGates = nil
	evidence := []string{"verification:" + record.VerificationID}
	evidence = append(evidence, record.SourceEvidenceRefs...)
	evidence = append(evidence, eventEvidence...)
	evidence = append(evidence, record.EvidenceRefs...)
	for _, fact := range record.Commands {
		evidence = append(evidence, fact.EvidenceRefs...)
	}
	evidence = boundedModelEvidenceRefs(evidence)
	s.LastHostVerification = &hostVerificationResultState{
		ID: record.VerificationID, Source: pending.Anchor, SuiteDigest: record.SuiteDigest,
		CommandDigests: append([]string(nil), record.CommandDigests...), Outcome: record.Outcome,
		FailureKind: record.FailureKind, FailureFingerprint: record.FailureFingerprint, EvidenceRefs: evidence,
		Discarded: pending.Obsolete || s.CurrentAnchor != pending.Anchor,
	}
	s.LastHostVerification.Usable = !s.LastHostVerification.Discarded && (record.Outcome == "commands_succeeded" || record.Outcome == "no_suite")
	if s.LastHostVerification.Usable && record.Outcome == "commands_succeeded" {
		s.VerificationGates = map[string]verificationGateState{}
		for _, fact := range record.Commands {
			s.VerificationGates[fact.CommandDigest] = verificationGateState{
				CommandDigest: fact.CommandDigest, ResultDigest: fact.ResultDigest, Passed: true,
				Anchor: pending.Anchor, EvidenceRefs: boundedModelEvidenceRefs(fact.EvidenceRefs),
			}
		}
	}
	if !s.LastHostVerification.Discarded && record.Outcome == "command_failed" {
		s.AdvisorySteering = appendBounded(s.AdvisorySteering, "[stado supervise verification facts]\nThe operator-configured verification suite failed at the current completion tree. Inspect the referenced audited results, correct the work, and request completion again.\nReason: "+record.FailureKind+" "+record.FailureFingerprint, 2)
	}
	s.LastHostVerificationEventSequence = brokerSequence
	s.LastHostVerificationEventDigest = eventDigest
	s.HostVerificationEffectPending = true
	return s.pendingHostVerificationTerminalTransition(), false, nil
}

func (s *runState) pendingHostVerificationTerminalTransition() transition {
	if s == nil || !s.HostVerificationEffectPending || s.LastHostVerification == nil {
		return transition{}
	}
	result := s.LastHostVerification
	release := func(reason string) []action {
		if s.Hold == nil {
			return nil
		}
		return []action{{Kind: actionReleaseHold, Reason: reason}}
	}
	if result.Discarded {
		return transition{Actions: release("obsolete or stale host verification was discarded"), Note: "discarded stale host verification facts"}
	}
	switch result.Outcome {
	case "commands_succeeded":
		return transition{Actions: release("operator-configured verification reached a factual terminal result"), Note: "host verification gate is ready at the exact completion anchor"}
	case "no_suite":
		return transition{Actions: release("operator configured no native verification suite"), Note: "no native suite existed; completion still requires fresh independent semantic verification"}
	case "command_failed":
		return transition{Actions: release("operator-configured verification rejected the completion tree"), Note: "verification failure returned factual correction context to the worker"}
	case "infrastructure_error", "cancelled":
		actions := release("operator-configured verification could not produce a usable result")
		actions = append(actions, action{Kind: actionPause, OmitHold: true, Reason: "operator-configured verification ended with " + result.Outcome + " (" + result.FailureKind + ")"})
		return transition{Actions: actions, Note: "verification infrastructure/cancellation paused without inferring completion"}
	default:
		return transition{}
	}
}

func (s *runState) markHostVerificationTerminalEffectsApplied() error {
	if s == nil || !s.HostVerificationEffectPending || s.LastHostVerification == nil {
		return errors.New("no terminal host verification effects are pending")
	}
	s.HostVerificationEffectPending = false
	return nil
}

func (s *runState) hostVerificationGateEvidence() ([]string, error) {
	result := s.LastHostVerification
	if s.PendingHostVerification != nil || s.HostVerificationEffectPending || result == nil || !result.Usable || result.Discarded || result.Source != s.CurrentAnchor || (result.Outcome != "commands_succeeded" && result.Outcome != "no_suite") || !validHostFactDigest(result.SuiteDigest) || result.ID == "" {
		return nil, errors.New("operator-configured verification has no exact terminal result eligible for independent completion review")
	}
	if result.Outcome == "no_suite" && len(result.CommandDigests) != 0 || result.Outcome == "commands_succeeded" && len(result.CommandDigests) == 0 {
		return nil, errors.New("operator-configured verification result has an inconsistent suite shape")
	}
	return boundedModelEvidenceRefs(result.EvidenceRefs), nil
}

// validateHostVerificationDurableState rejects partially folded or
// self-contradictory verification state before an application instance is
// rebound. Broker effects may have committed before their following journal
// record, so a pending terminal effect is valid with or without a live hold;
// its exact event sequence/digest remains mandatory in either case.
func (s runState) validateHostVerificationDurableState() error {
	if err := s.PendingHostVerification.validate(); err != nil {
		return err
	}
	if s.PendingHostVerification != nil {
		if s.LastHostVerification != nil || s.HostVerificationEffectPending {
			return errors.New("pending and terminal host verification states overlap")
		}
		if !s.PendingHostVerification.Obsolete && (s.Hold == nil || s.PendingHostVerification.Anchor != s.CurrentAnchor) {
			return errors.New("current host verification request lost its exact anchor hold")
		}
	}
	if (s.LastHostVerificationEventSequence == 0) != (s.LastHostVerificationEventDigest == "") || s.LastHostVerificationEventDigest != "" && !validHostFactDigest(s.LastHostVerificationEventDigest) {
		return errors.New("host verification event replay identity is incomplete")
	}
	result := s.LastHostVerification
	if result == nil {
		if s.HostVerificationEffectPending {
			return errors.New("host verification terminal effect lost its factual result")
		}
		return nil
	}
	if !boundedRequired(result.ID, 256) || result.Source.validate() != nil || !validGitTreeDigest(result.Source.TreeDigest) || !validHostFactDigest(result.SuiteDigest) || len(result.CommandDigests) > 64 || len(result.EvidenceRefs) > maxEvidenceRefs {
		return errors.New("durable host verification result exceeds its factual bounds")
	}
	if err := validateEvidenceRefs(result.EvidenceRefs); err != nil {
		return err
	}
	for _, digest := range result.CommandDigests {
		if !validHostFactDigest(digest) {
			return errors.New("durable host verification contains an invalid command digest")
		}
	}
	switch result.Outcome {
	case "commands_succeeded":
		if len(result.CommandDigests) == 0 || result.FailureKind != "" || result.FailureFingerprint != "" {
			return errors.New("durable successful verification has an invalid suite shape")
		}
	case "no_suite":
		if len(result.CommandDigests) != 0 || result.FailureKind != "" || result.FailureFingerprint != "" {
			return errors.New("durable no-suite verification has an invalid suite shape")
		}
	case "command_failed", "infrastructure_error", "cancelled":
		if !validHostVerificationFailureKind(result.Outcome, result.FailureKind) || !validHostFactDigest(result.FailureFingerprint) {
			return errors.New("durable failed verification lacks its typed fingerprint")
		}
	default:
		return errors.New("durable host verification has an unknown outcome")
	}
	if result.Discarded && result.Usable || result.Usable && (result.Source != s.CurrentAnchor || result.Outcome != "commands_succeeded" && result.Outcome != "no_suite") {
		return errors.New("durable host verification grants usability outside its exact completion anchor")
	}
	if s.LastHostVerificationEventSequence == 0 {
		return errors.New("durable host verification result lacks its replay identity")
	}
	return nil
}
