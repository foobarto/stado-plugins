//go:build wasip1

package main

import (
	"errors"
	"fmt"
)

func hostVerificationRequestKey(runID string, pending *hostVerificationRequestState) string {
	if pending == nil {
		return ""
	}
	return "supervise-verify:" + digestString(fmt.Sprintf("%s\x00%d\x00%d", runID, pending.ExpectedWorkerVersion, pending.SourceEventSequence))[:32]
}

func (a *application) ensureHostVerificationRequest() error {
	pending := a.state.PendingHostVerification
	if pending == nil {
		return nil
	}
	if err := pending.validate(); err != nil {
		return err
	}
	if pending.Stage == hostVerificationStageRequested {
		return nil
	}
	if a.state.Hold == nil {
		return errors.New("host verification intent lost its scheduling hold")
	}
	if a.state.Hold.ID == "" {
		if err := a.execute(transition{Actions: []action{{Kind: actionAcquireHold, Reason: a.state.Hold.Reason}}}); err != nil {
			return err
		}
	}
	raw, err := callHostJSON(stadoSessionVerificationRequest, map[string]any{
		"run_id": a.state.RunID, "expected_worker_version": pending.ExpectedWorkerVersion,
		"source_event_sequence": pending.SourceEventSequence,
		"idempotency_key":       hostVerificationRequestKey(a.state.RunID, pending),
	})
	if err != nil {
		return err
	}
	var record hostVerificationRecord
	if err := decodeStrictBytes(raw, &record); err != nil {
		return fmt.Errorf("decode requested host verification: %w", err)
	}
	if record.SessionID != a.anchor.SessionID || record.Generation != a.anchor.SessionGeneration {
		return errors.New("requested host verification belongs to another application session")
	}
	if err := a.state.acceptHostVerificationRequest(record); err != nil {
		return err
	}
	evidence := boundedModelEvidenceRefs(append([]string{"verification:" + record.ID}, record.SourceEvidenceRefs...))
	return a.persist("verification.requested", evidence)
}

func (a *application) handleHostVerificationFinished(envelope eventEnvelope) error {
	var record hostVerificationTerminal
	if err := decodeStrictBytes(envelope.Event.Data, &record); err != nil {
		return fmt.Errorf("decode terminal host verification: %w", err)
	}
	change, replay, err := a.state.applyHostVerificationTerminal(record, authenticatedAgentParent{
		SessionID: envelope.Anchor.SessionID, SessionGeneration: envelope.Anchor.SessionGeneration,
		CanonicalRepoID: envelope.Anchor.CanonicalRepoID,
	}, envelope.Event.BrokerSeq, hostVerificationEventDigest(envelope.Event.Data, envelope.Event.EvidenceRefs), envelope.Event.EvidenceRefs)
	if err != nil {
		return err
	}
	evidence := []string{"verification:" + record.VerificationID}
	evidence = append(evidence, record.SourceEvidenceRefs...)
	evidence = append(evidence, envelope.Event.EvidenceRefs...)
	evidence = append(evidence, record.EvidenceRefs...)
	for _, fact := range record.Commands {
		evidence = append(evidence, fact.EvidenceRefs...)
	}
	if !replay {
		if err := a.persist("verification.finished", boundedModelEvidenceRefs(evidence)); err != nil {
			return err
		}
	}
	if err := a.execute(change); err != nil {
		return err
	}
	if a.state.HostVerificationEffectPending {
		if err := a.state.markHostVerificationTerminalEffectsApplied(); err != nil {
			return err
		}
		if err := a.persist("verification.effects_applied", boundedModelEvidenceRefs(evidence)); err != nil {
			return err
		}
	}
	return a.startQueuedReview()
}
