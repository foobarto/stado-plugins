package main

import (
	"errors"
	"slices"
)

type completionHandoffRequest struct {
	IdempotencyKey       string   `json:"idempotency_key"`
	RunID                string   `json:"run_id"`
	Summary              string   `json:"summary,omitempty"`
	EvidenceRefs         []string `json:"evidence_refs,omitempty"`
	ContinuationInputIDs []string `json:"continuation_input_ids,omitempty"`
}

type completionHandoffAck struct {
	ID                   string   `json:"id"`
	RunID                string   `json:"run_id"`
	ContinuationInputIDs []string `json:"continuation_input_ids,omitempty"`
}

func prepareCompletionHandoff(state runState) (completionHandoffRequest, error) {
	if !state.Completed || state.CompletionVerdict == nil || state.CompletedAnchor == nil || state.RunID == "" {
		return completionHandoffRequest{}, errors.New("verifier-complete state has no anchored verdict")
	}
	continuation, err := state.continuationInputIDs()
	if err != nil {
		return completionHandoffRequest{}, err
	}
	return completionHandoffRequest{
		IdempotencyKey:       "supervise-complete:" + digestString(state.RunID)[:24],
		RunID:                state.RunID,
		Summary:              state.CompletionVerdict.Rationale,
		EvidenceRefs:         boundedModelEvidenceRefs(state.CompletionVerdict.EvidenceRefs),
		ContinuationInputIDs: continuation,
	}, nil
}

func acceptCompletionHandoff(state *runState, ack completionHandoffAck) error {
	if state == nil {
		return errors.New("broker returned invalid successful-completion handoff")
	}
	want, err := state.continuationInputIDs()
	if err != nil {
		return err
	}
	if !state.Completed || ack.ID == "" || ack.RunID != state.RunID || !slices.Equal(ack.ContinuationInputIDs, want) {
		return errors.New("broker returned invalid successful-completion handoff")
	}
	state.CompletionHandedOff = true
	state.CompletionID = ack.ID
	return nil
}
