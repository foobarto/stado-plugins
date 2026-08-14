package main

import "errors"

func workerRunTerminal(status string) bool {
	switch status {
	case "", workerRunCancelled, workerRunCompleted, workerRunInterrupted, workerRunStopped:
		return true
	default:
		return false
	}
}

func (s runState) dormant() bool {
	if s.Cancelled {
		return s.CancellationCleaned && s.CancellationAgentID == "" && s.Hold == nil && s.PendingReview == nil && s.PendingHostVerification == nil && !s.HostVerificationEffectPending && workerRunTerminal(s.WorkerRunStatus)
	}
	return s.Completed && s.CompletionHandedOff && s.Hold == nil && s.PendingHostVerification == nil && !s.HostVerificationEffectPending
}

// validateDormantRunState validates only facts owned by the durable
// application journal. A terminal, fully cleaned run no longer depends on the
// continued availability of its historical artifact candidate merely to ACK
// unrelated lifecycle work. Active or partially cleaned runs still revalidate
// the exact candidate in ensureLoaded.
func validateDormantRunState(s runState) error {
	if !s.dormant() || s.Schema != policySchema || s.RunID == "" || s.ContractID == "" || s.ContractVersion == 0 || s.Config.validate() != nil {
		return errors.New("durable supervise run is not a valid dormant terminal state")
	}
	if err := s.validateHostVerificationDurableState(); err != nil {
		return err
	}
	if s.Cancelled {
		if s.Completed || s.CancellationAgentID != "" || s.Hold != nil || s.PendingReview != nil || s.PendingHostVerification != nil || s.HostVerificationEffectPending || !workerRunTerminal(s.WorkerRunStatus) {
			return errors.New("cancelled supervise run has incomplete terminal cleanup")
		}
		return nil
	}
	if !s.Completed || !s.CompletionHandedOff || s.CompletionID == "" || s.CompletedAnchor == nil || s.CompletedAnchor.validate() != nil || s.CompletionVerdict == nil || s.CompletionVerdict.validate() != nil || s.CompletionVerdict.Decision != verdictApprove || s.CompletionVerdict.Anchor != *s.CompletedAnchor || s.PendingReview != nil || s.PendingCompletion != nil || s.PendingHostVerification != nil || s.HostVerificationEffectPending || s.Hold != nil {
		return errors.New("completed supervise run has incomplete terminal cleanup")
	}
	return nil
}
