package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

type fakeLeaseClock struct{ now time.Time }

func (c *fakeLeaseClock) Advance(value time.Duration) { c.now = c.now.Add(value) }

type fakeHoldBroker struct {
	clock     *fakeLeaseClock
	id        string
	version   uint64
	transport int
	mutations int
	seen      map[string]holdLeaseAck
	last      holdRenewalRequest
}

func (b *fakeHoldBroker) renew(request holdRenewalRequest) (holdLeaseAck, error) {
	b.transport++
	b.last = request
	if prior, ok := b.seen[request.IdempotencyKey]; ok {
		return prior, nil
	}
	if request.ID != b.id || request.ExpectedVersion != b.version || request.RunID == "" || request.ReasonCode != holdReasonCode || request.TTLMS < 30_000 {
		return holdLeaseAck{}, errors.New("fake broker rejected hold renewal shape")
	}
	b.version++
	b.mutations++
	ack := holdLeaseAck{
		ID: b.id, Version: b.version, Status: "active",
		LeaseUntil: b.clock.now.Add(time.Duration(request.TTLMS) * time.Millisecond),
	}
	b.seen[request.IdempotencyKey] = ack
	return ack, nil
}

func leasedTestState(t *testing.T, clock *fakeLeaseClock) runState {
	t.Helper()
	cfg := defaultConfig()
	cfg.HoldTTLSeconds = 30
	state := testState(t, cfg)
	state.Hold = &holdState{Reason: "review remains unresolved"}
	err := acceptInitialHoldLease(&state, holdLeaseAck{
		ID: "hold-exact", Version: 1, Status: "active",
		LeaseUntil: clock.now.Add(30 * time.Second),
	}, state.Hold.Reason, clock.now)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func newFakeHoldBroker(clock *fakeLeaseClock, state runState) *fakeHoldBroker {
	return &fakeHoldBroker{
		clock: clock, id: state.Hold.ID, version: state.Hold.Version,
		seen: map[string]holdLeaseAck{},
	}
}

func TestHoldRenewalCoversMaximumReviewDuration(t *testing.T) {
	clock := &fakeLeaseClock{now: time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)}
	state := leasedTestState(t, clock)
	broker := newFakeHoldBroker(clock, state)
	initialID := state.Hold.ID
	for elapsed := time.Duration(0); elapsed < time.Hour; elapsed += 5 * time.Second {
		clock.Advance(5 * time.Second)
		if _, err := maintainHoldLease(&state, clock.now, broker.renew); err != nil {
			t.Fatalf("renew at %s: %v", elapsed+5*time.Second, err)
		}
	}
	if state.Hold.ID != initialID || state.Hold.Version != broker.version || !state.Hold.LeaseUntil.After(clock.now) {
		t.Fatalf("long review lost its exact active hold: state=%+v broker_version=%d", state.Hold, broker.version)
	}
	if broker.mutations < 100 || !state.Hold.RenewAt.Before(state.Hold.LeaseUntil) {
		t.Fatalf("renewal cadence did not cover one-hour review: mutations=%d hold=%+v", broker.mutations, state.Hold)
	}
	if broker.last.ID != initialID || broker.last.ExpectedVersion+1 != state.Hold.Version || broker.last.IdempotencyKey != fmt.Sprintf("supervise-hold-renew:%s:v%d", digestString(initialID)[:24], broker.last.ExpectedVersion) {
		t.Fatalf("renewal did not preserve exact CAS/idempotency: %+v", broker.last)
	}
}

func TestHoldRenewalReplaysSafelyAfterRestart(t *testing.T) {
	clock := &fakeLeaseClock{now: time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)}
	state := leasedTestState(t, clock)
	snapshot, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	broker := newFakeHoldBroker(clock, state)
	clock.Advance(16 * time.Second)
	if renewed, err := maintainHoldLease(&state, clock.now, broker.renew); err != nil || !renewed || state.Hold.Version != 2 {
		t.Fatalf("first renewal: renewed=%v state=%+v err=%v", renewed, state.Hold, err)
	}

	// Simulate a crash after the broker committed but before the renewed state
	// reached the application journal. The same exact version-keyed request must
	// replay the prior response instead of minting a second renewal transition.
	var restarted runState
	if err := json.Unmarshal(snapshot, &restarted); err != nil {
		t.Fatal(err)
	}
	if renewed, err := maintainHoldLease(&restarted, clock.now, broker.renew); err != nil || !renewed || restarted.Hold.Version != 2 {
		t.Fatalf("restart replay: renewed=%v state=%+v err=%v", renewed, restarted.Hold, err)
	}
	if broker.transport != 2 || broker.mutations != 1 || restarted.Hold.ID != "hold-exact" {
		t.Fatalf("restart retry was not idempotent: transport=%d mutations=%d hold=%+v", broker.transport, broker.mutations, restarted.Hold)
	}
}

func TestHoldReleaseEffectCommittedBeforeJournalReplaysFromExactDurableCAS(t *testing.T) {
	clock := &fakeLeaseClock{now: time.Date(2026, 8, 14, 9, 30, 0, 0, time.UTC)}
	durable := leasedTestState(t, clock)
	snapshot, err := json.Marshal(durable)
	if err != nil {
		t.Fatal(err)
	}
	ack := holdLeaseAck{ID: durable.Hold.ID, Version: durable.Hold.Version + 1, Status: "released", LeaseUntil: durable.Hold.LeaseUntil}
	if _, err := acceptReleasedHoldLease(&durable, ack); err != nil || durable.Hold != nil {
		t.Fatalf("first exact release response: state=%+v err=%v", durable.Hold, err)
	}

	// The broker effect committed, but callback cancellation lost the journal
	// acknowledgement. Rebinding sees the old exact CAS and the host's stable
	// logical request replays this same terminal response.
	var rebound runState
	if err := json.Unmarshal(snapshot, &rebound); err != nil {
		t.Fatal(err)
	}
	if _, err := acceptReleasedHoldLease(&rebound, ack); err != nil || rebound.Hold != nil {
		t.Fatalf("replayed exact release response: state=%+v err=%v", rebound.Hold, err)
	}
	wrong := leasedTestState(t, clock)
	ack.Version++
	if _, err := acceptReleasedHoldLease(&wrong, ack); err == nil || wrong.Hold == nil {
		t.Fatal("release replay accepted a response from a different CAS version")
	}
}

func TestStaleStopConfirmationKeepsRenewingItsHold(t *testing.T) {
	clock := &fakeLeaseClock{now: time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)}
	cfg := defaultConfig()
	cfg.HoldTTLSeconds = 30
	state := testState(t, cfg)
	old, _ := testAnchor(1, "build", 0)
	current, _ := testAnchor(2, "build", 0)
	state.CurrentAnchor = current
	state.PendingReview = &reviewRequest{ID: "old-review", Anchor: old, Purpose: reviewPurposeWatchdog, Signals: []signal{{Type: "scope_expansion", Severity: "high", EvidenceRefs: []string{"diff:old"}}}}
	change, err := state.applyReviewerResult(reviewerResult{Verdict: verdict{
		Decision: verdictStop, Anchor: old, Rationale: "old evidence may still require intervention", EvidenceRefs: []string{"diff:old"},
	}}, "")
	if err != nil || len(change.Actions) != 2 || state.PendingReview == nil || !state.PendingReview.Confirming || state.Hold == nil {
		t.Fatalf("stale stop did not create held confirmation: change=%+v state=%+v err=%v", change, state, err)
	}
	if err := acceptInitialHoldLease(&state, holdLeaseAck{ID: "hold-stale-stop", Version: 1, Status: "active", LeaseUntil: clock.now.Add(30 * time.Second)}, state.Hold.Reason, clock.now); err != nil {
		t.Fatal(err)
	}
	broker := newFakeHoldBroker(clock, state)
	clock.Advance(16 * time.Second)
	if renewed, err := maintainHoldLease(&state, clock.now, broker.renew); err != nil || !renewed || state.PendingReview == nil || !state.PendingReview.Confirming {
		t.Fatalf("stale-stop confirmation lost renewal: renewed=%v state=%+v err=%v", renewed, state, err)
	}
}

func TestHoldRenewalTerminalCleanupAndFailureAreFailClosed(t *testing.T) {
	clock := &fakeLeaseClock{now: time.Date(2026, 8, 14, 11, 0, 0, 0, time.UTC)}
	state := leasedTestState(t, clock)
	broker := newFakeHoldBroker(clock, state)
	clock.Advance(16 * time.Second)
	state.Completed = true // plugin completion is not yet a host handoff
	if renewed, err := maintainHoldLease(&state, clock.now, broker.renew); err != nil || !renewed {
		t.Fatalf("pre-handoff completion stopped renewing early: renewed=%v err=%v", renewed, err)
	}
	if err := acceptCompletionHandoff(&state, completionHandoffAck{ID: "completion-exact", RunID: state.RunID}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(16 * time.Second)
	if renewed, err := maintainHoldLease(&state, clock.now, broker.renew); err != nil || !renewed {
		t.Fatalf("handoff without exact release stopped renewing early: renewed=%v err=%v", renewed, err)
	}
	state.Hold = nil // exact CAS release completes terminal cleanup
	clock.Advance(time.Hour)
	if renewed, err := maintainHoldLease(&state, clock.now, broker.renew); err != nil || renewed || broker.mutations != 2 {
		t.Fatalf("terminal cleanup renewed a released hold: renewed=%v mutations=%d err=%v", renewed, broker.mutations, err)
	}

	state = leasedTestState(t, clock)
	state.PendingReview = &reviewRequest{ID: "review-preserved", Purpose: reviewPurposeVerifier}
	clock.Advance(16 * time.Second)
	_, renewalErr := maintainHoldLease(&state, clock.now, func(holdRenewalRequest) (holdLeaseAck, error) {
		return holdLeaseAck{}, errors.New("broker unavailable")
	})
	if renewalErr == nil {
		t.Fatal("renewal failure was ignored")
	}
	change := state.failHoldRenewal(renewalErr)
	if state.Hold == nil || !state.Hold.RenewalFailed || state.PendingReview == nil || state.PendingReview.ID != "review-preserved" || len(change.Actions) != 1 || change.Actions[0].Kind != actionPause || !change.Actions[0].OmitHold {
		t.Fatalf("renewal failure did not produce hold-independent fail-closed pause: change=%+v state=%+v", change, state)
	}
	broker = newFakeHoldBroker(clock, state)
	if renewed, err := maintainHoldLease(&state, clock.now, broker.renew); err != nil || !renewed || state.Hold.RenewalFailed || state.PendingReview == nil {
		t.Fatalf("restart recovery did not renew the preserved review hold: renewed=%v state=%+v err=%v", renewed, state, err)
	}
}
