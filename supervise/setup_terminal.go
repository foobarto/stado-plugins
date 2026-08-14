package main

import (
	"errors"
	"fmt"
	"strings"
)

func (s *setupState) observeAgentDown(parent authenticatedAgentParent, brokerSequence uint64, evidenceRefs []string, facts agentDownFacts) (bool, error) {
	if s == nil || s.validate() != nil {
		return false, errors.New("durable supervise setup is unavailable for agent.down")
	}
	if parent != s.Parent {
		return false, errors.New("baseline agent.down parent does not match the exact setup anchor")
	}
	if brokerSequence == 0 {
		return false, errors.New("baseline agent.down requires a broker sequence")
	}
	if err := facts.validate(); err != nil {
		return false, err
	}
	// The durable setup already names a unique next-spawn intent. If admission
	// completes but the callback loses its reply, terminal facts may arrive
	// while the projection still says starting/failed. Adopt only the exact
	// host-published child whose ownership matches that intent.
	if (s.Phase == setupPhaseStarting || s.Phase == setupPhaseFailed) && facts.Scope.Ownership == baselineAgentOwnership(*s) {
		if err := s.beginBaseline(facts.Child.AgentID); err != nil {
			return false, err
		}
	}
	if facts.Child.AgentID != s.BaselineAgentID || s.Phase != setupPhaseRunning {
		return false, nil
	}
	if err := validateAgentDownEvidenceRefs(facts.Child.SessionID, evidenceRefs, facts.Changes); err != nil {
		return false, err
	}
	if brokerSequence < s.AgentDownSequence {
		return false, errors.New("baseline agent.down broker sequence moved backwards")
	}
	if brokerSequence == s.AgentDownSequence {
		return s.Terminal != nil && s.Terminal.BrokerSequence == brokerSequence && s.Terminal.Child.AgentID == facts.Child.AgentID && s.Terminal.Child.SessionID == facts.Child.SessionID, nil
	}
	observation := reviewTerminalObservation{
		BrokerSequence: brokerSequence, Parent: parent, Purpose: "baseline",
		Child: *facts.Child, Budget: *facts.Budget, Terminal: facts.terminalMetadata(), Scope: *facts.Scope,
		Changes: facts.Changes, Failure: facts.Failure, EvidenceRefs: append([]string(nil), evidenceRefs...),
	}
	observation.InvalidReason = baselineTerminalInvalidReason(*s, observation)
	s.AgentDownSequence = brokerSequence
	s.Terminal = &observation
	s.BaselineTokenUsage.add(observation.Terminal.Usage)
	for _, diagnostic := range observation.diagnostics() {
		s.Diagnostics = appendBounded(s.Diagnostics, "baseline agent.down: "+diagnostic, 32)
	}
	return true, nil
}

func baselineTerminalInvalidReason(setup setupState, observation reviewTerminalObservation) string {
	if observation.Child.Role != "explorer" || observation.Child.Mode != "read_only" || observation.Child.Execution != "wait" || observation.Scope.Ownership != currentBaselineAgentOwnership(setup) {
		return "baseline child identity, read-only mode, execution, or ownership did not match the signed spawn request"
	}
	if observation.Budget.TokenLimit != uint64(setup.Request.Config.WatchdogTokenBudget) || observation.Budget.TurnLimit != 4 || observation.Budget.TimeoutSeconds != uint64(setup.Request.Config.WatchdogTimeoutSecond) {
		return "baseline child admitted budget did not match the signed spawn request"
	}
	if len(observation.Scope.WritePaths) > 0 || observation.Scope.WritePathsDigest != "" || observation.Scope.WritePathsTruncated {
		return "read-only baseline child reported a write scope"
	}
	if len(observation.Scope.Violations) > 0 || observation.Scope.ViolationsDigest != "" || observation.Scope.ViolationsTruncated {
		return "read-only baseline child reported a scope violation"
	}
	if observation.Changes != nil && (len(observation.Changes.ChangedPaths) > 0 || observation.Changes.ChangedPathsDigest != "" || observation.Changes.ChangedPathsTruncated) {
		return "read-only baseline child changed repository paths"
	}
	return ""
}

func (s *setupState) consumeBaselineMessages(messages agentMessages) (bool, error) {
	if s == nil || s.Phase != setupPhaseRunning || s.Terminal == nil {
		return false, errors.New("no authenticated baseline terminal result is pending")
	}
	if !isTerminalAgentStatus(messages.Status) {
		return false, nil
	}
	if messages.Terminal == nil || !equalTerminalMetadata(s.Terminal.Terminal, *messages.Terminal) {
		return false, errors.New("baseline agent.down and agent read terminal facts disagree")
	}
	terminalDiagnostic, err := s.Terminal.Terminal.diagnostic()
	if err != nil {
		return false, err
	}
	proposal, messageDiagnostic, valid := detectBaselineProposal(messages.Messages)
	diagnostic := joinDiagnostics(messageDiagnostic, terminalDiagnostic)
	if messages.Status != "completed" {
		diagnostic = joinDiagnostics(diagnostic, "terminal agent status: "+messages.Status)
	}
	if s.Terminal.Child.Status != "completed" && s.Terminal.Child.Status != messages.Status {
		diagnostic = joinDiagnostics(diagnostic, "agent.down child status: "+s.Terminal.Child.Status)
	}
	if diagnostic != "" {
		s.Diagnostics = appendBounded(s.Diagnostics, "baseline terminal: "+diagnostic, 32)
	}
	if s.Terminal.InvalidReason != "" || messages.Status != "completed" || s.Terminal.Child.Status != "completed" {
		reason := s.Terminal.InvalidReason
		if reason == "" {
			reason = "baseline child did not complete successfully"
		}
		if err := s.markBaselineFailed("baseline child facts rejected: " + reason); err != nil {
			return false, err
		}
		return true, nil
	}
	if !valid {
		reason := "fresh baseline reviewer returned no valid proposal"
		if strings.TrimSpace(messages.Status) != "" {
			reason += " (" + messages.Status + ")"
		}
		if err := s.markBaselineFailed(reason); err != nil {
			return false, err
		}
		return true, nil
	}
	s.Proposal = &proposal
	s.Phase, s.LastError, s.BaselineOffset = setupPhaseReady, "", messages.Offset
	return true, nil
}

func baselineFailureMessage(setup setupState) string {
	if setup.Attempt >= maxBaselineAttempts {
		return fmt.Sprintf("baseline proposal failed after %d bounded fresh attempts; cancel or start a new setup", setup.Attempt)
	}
	return fmt.Sprintf("baseline proposal attempt %d failed; /supervise resume starts a fresh bounded attempt", setup.Attempt)
}
