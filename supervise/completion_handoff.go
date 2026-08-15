package main

import (
	"errors"
	"slices"
	"time"
)

type completionHandoffRequest struct {
	IdempotencyKey       string   `json:"idempotency_key"`
	RunID                string   `json:"run_id"`
	Summary              string   `json:"summary,omitempty"`
	EvidenceRefs         []string `json:"evidence_refs,omitempty"`
	ContinuationInputIDs []string `json:"continuation_input_ids,omitempty"`
}

type completionHandoffAck struct {
	ID                     string   `json:"id"`
	SessionID              string   `json:"session_id"`
	Generation             uint64   `json:"generation"`
	PluginID               string   `json:"plugin_id"`
	RunID                  string   `json:"run_id"`
	Summary                string   `json:"summary,omitempty"`
	EvidenceRefs           []string `json:"evidence_refs,omitempty"`
	ContinuationInputIDs   []string `json:"continuation_input_ids,omitempty"`
	ContinuationDeliveryID string   `json:"continuation_delivery_id,omitempty"`
	ContinuationDelivered  bool     `json:"continuation_delivered,omitempty"`
	WALSequence            uint64   `json:"wal_sequence"`
	CreatedAt              string   `json:"created_at"`
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

func decodeCompletionHandoffAck(raw []byte) (completionHandoffAck, error) {
	var ack completionHandoffAck
	if err := decodeStrictBytes(raw, &ack); err != nil {
		return completionHandoffAck{}, err
	}
	return ack, nil
}

func acceptCompletionHandoff(state *runState, identity lifecycleIdentity, ack completionHandoffAck) error {
	if state == nil {
		return errors.New("broker returned invalid successful-completion handoff")
	}
	want, err := prepareCompletionHandoff(*state)
	if err != nil {
		return err
	}
	if !state.Completed || !boundedRequired(ack.ID, 256) || ack.SessionID != identity.SessionID ||
		ack.Generation != identity.SessionGeneration || !boundedRequired(ack.PluginID, 512) ||
		ack.RunID != state.RunID || ack.Summary != want.Summary || !slices.Equal(ack.EvidenceRefs, want.EvidenceRefs) ||
		!slices.Equal(ack.ContinuationInputIDs, want.ContinuationInputIDs) || ack.WALSequence == 0 ||
		ack.ContinuationDelivered {
		return errors.New("broker returned invalid successful-completion handoff")
	}
	if len(want.ContinuationInputIDs) == 0 {
		if ack.ContinuationDeliveryID != "" {
			return errors.New("broker returned invalid successful-completion handoff")
		}
	} else if !boundedRequired(ack.ContinuationDeliveryID, 256) {
		return errors.New("broker returned invalid successful-completion handoff")
	}
	if _, err := time.Parse(time.RFC3339Nano, ack.CreatedAt); err != nil {
		return errors.New("broker returned invalid successful-completion handoff")
	}
	state.CompletionHandedOff = true
	state.CompletionID = ack.ID
	return nil
}
