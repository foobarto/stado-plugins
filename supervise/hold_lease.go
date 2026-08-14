package main

import (
	"errors"
	"fmt"
	"time"
)

const holdReasonCode = "supervise.review"

type holdLeaseAck struct {
	ID         string    `json:"id"`
	Version    uint64    `json:"version"`
	Status     string    `json:"status"`
	LeaseUntil time.Time `json:"lease_until"`
}

type holdRenewalRequest struct {
	IdempotencyKey  string `json:"idempotency_key"`
	ID              string `json:"id"`
	RunID           string `json:"run_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	ReasonCode      string `json:"reason_code"`
	Reason          string `json:"reason,omitempty"`
	TTLMS           int64  `json:"ttl_ms"`
}

type holdRenewer func(holdRenewalRequest) (holdLeaseAck, error)

func holdRenewAt(now, leaseUntil time.Time, ttl time.Duration) (time.Time, error) {
	if ttl < 30*time.Second || ttl > time.Hour {
		return time.Time{}, errors.New("hold TTL is outside the configured broker bounds")
	}
	now, leaseUntil = now.UTC(), leaseUntil.UTC()
	if !leaseUntil.After(now) {
		return time.Time{}, errors.New("broker hold lease is not current")
	}
	// Renew halfway through the lease. This leaves at least half the configured
	// TTL for timer delivery, restart recovery, and an idempotent retry.
	renewAt := leaseUntil.Add(-ttl / 2)
	if renewAt.Before(now) {
		renewAt = now
	}
	return renewAt, nil
}

func acceptInitialHoldLease(state *runState, ack holdLeaseAck, reason string, now time.Time) error {
	if state == nil || state.Hold == nil || state.Hold.ID != "" || state.Hold.Version != 0 {
		return errors.New("no unacknowledged hold acquisition is pending")
	}
	if ack.ID == "" || ack.Version != 1 || ack.Status != "active" {
		return errors.New("broker returned invalid active hold identity")
	}
	renewAt, err := holdRenewAt(now, ack.LeaseUntil, time.Duration(state.Config.HoldTTLSeconds)*time.Second)
	if err != nil {
		return err
	}
	state.Hold = &holdState{
		ID: ack.ID, Version: ack.Version, Reason: reason,
		LeaseUntil: ack.LeaseUntil.UTC(), RenewAt: renewAt,
	}
	return nil
}

func acceptReleasedHoldLease(state *runState, ack holdLeaseAck) (*holdState, error) {
	if state == nil || state.Hold == nil || state.Hold.ID == "" || state.Hold.Version == 0 {
		return nil, errors.New("cannot accept release without an exact acknowledged hold")
	}
	prior := *state.Hold
	if ack.ID != prior.ID || ack.Version != prior.Version+1 || ack.Status != "released" {
		return nil, errors.New("broker returned invalid exact hold release")
	}
	state.Hold = nil
	return &prior, nil
}

func maintainHoldLease(state *runState, now time.Time, renew holdRenewer) (bool, error) {
	if state == nil || state.Hold == nil || state.Hold.ID == "" {
		return false, nil
	}
	hold := state.Hold
	now = now.UTC()
	if hold.Version == 0 || hold.LeaseUntil.IsZero() || hold.RenewAt.IsZero() {
		return false, errors.New("acknowledged hold has no restart-safe lease metadata")
	}
	if now.Before(hold.RenewAt) {
		return false, nil
	}
	if !hold.LeaseUntil.After(now) {
		return false, errors.New("hold lease expired before renewal")
	}
	if renew == nil {
		return false, errors.New("hold renewer is unavailable")
	}
	request := holdRenewalRequest{
		IdempotencyKey: fmt.Sprintf("supervise-hold-renew:%s:v%d", digestString(hold.ID)[:24], hold.Version),
		ID:             hold.ID, RunID: state.RunID, ExpectedVersion: hold.Version,
		ReasonCode: holdReasonCode, Reason: hold.Reason,
		TTLMS: int64(state.Config.HoldTTLSeconds) * int64(time.Second/time.Millisecond),
	}
	ack, err := renew(request)
	if err != nil {
		return false, fmt.Errorf("renew broker hold %s version %d: %w", hold.ID, hold.Version, err)
	}
	if ack.ID != hold.ID || ack.Version != hold.Version+1 || ack.Status != "active" {
		return false, errors.New("broker renewal changed hold identity or version unexpectedly")
	}
	renewAt, err := holdRenewAt(now, ack.LeaseUntil, time.Duration(state.Config.HoldTTLSeconds)*time.Second)
	if err != nil {
		return false, err
	}
	hold.Version = ack.Version
	hold.LeaseUntil = ack.LeaseUntil.UTC()
	hold.RenewAt = renewAt
	hold.RenewalFailed = false
	return true, nil
}

func (s *runState) failHoldRenewal(cause error) transition {
	if s == nil || s.Hold == nil {
		return transition{}
	}
	s.Hold.RenewalFailed = true
	s.Diagnostics = appendBounded(s.Diagnostics, "hold renewal failed closed: "+cause.Error(), 32)
	return transition{Actions: []action{{
		Kind: actionPause, OmitHold: true,
		Reason: "supervise could not renew its scheduling hold; paused without inferring review or completion",
	}}, Note: "hold renewal failed closed"}
}
