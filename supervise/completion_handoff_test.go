package main

import (
	"encoding/json"
	"testing"
	"time"
)

var testCompletionIdentity = lifecycleIdentity{SessionID: "application-session", SessionGeneration: 7}

func exactCompletionAck(t *testing.T, state runState) completionHandoffAck {
	t.Helper()
	request, err := prepareCompletionHandoff(state)
	if err != nil {
		t.Fatal(err)
	}
	ack := completionHandoffAck{
		ID:                   "completion-exact",
		SessionID:            testCompletionIdentity.SessionID,
		Generation:           testCompletionIdentity.SessionGeneration,
		PluginID:             "github.com/foobarto/stado-plugins/supervise",
		RunID:                request.RunID,
		Summary:              request.Summary,
		EvidenceRefs:         request.EvidenceRefs,
		ContinuationInputIDs: request.ContinuationInputIDs,
		WALSequence:          41,
		CreatedAt:            time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	}
	if len(request.ContinuationInputIDs) > 0 {
		ack.ContinuationDeliveryID = "continuation-delivery-exact"
	}
	return ack
}

func TestCompletionHandoffAckConsumesExactCurrentBrokerFact(t *testing.T) {
	state, _ := completionReviewState(t)
	anchor := state.CurrentAnchor
	if _, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictApprove, Anchor: anchor, Rationale: "send to verifier", EvidenceRefs: []string{"git:criterion-0"}}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{Decision: verdictApprove, Anchor: anchor, Rationale: "verified success", EvidenceRefs: []string{"git:criterion-0"}}}, ""); err != nil {
		t.Fatal(err)
	}
	ack := exactCompletionAck(t, state)
	raw, err := json.Marshal(ack)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeCompletionHandoffAck(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := acceptCompletionHandoff(&state, testCompletionIdentity, decoded); err != nil {
		t.Fatal(err)
	}
	if !state.CompletionHandedOff || state.CompletionID != ack.ID {
		t.Fatalf("completion fact was not accepted exactly: %+v", state)
	}

	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["future_authority"] = true
	unknown, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeCompletionHandoffAck(unknown); err == nil {
		t.Fatal("unknown broker completion field must fail strict decoding")
	}
}
