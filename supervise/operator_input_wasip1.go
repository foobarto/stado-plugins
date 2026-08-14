//go:build wasip1

package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

//go:wasmimport stado stado_session_input_claim
func stadoSessionInputClaim(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_session_input_route
func stadoSessionInputRoute(reqPtr, reqLen, respPtr, respCap uint32) int32

func (a *application) handleOperatorInput(envelope eventEnvelope) error {
	if envelope.Event.Kind != "operator.input.queued" || envelope.Event.BrokerSeq == 0 || len(envelope.Event.EvidenceRefs) != 0 {
		return errors.New("invalid authenticated operator-input event envelope")
	}
	fact, err := decodeOperatorInputQueued(envelope.Event.Data)
	if err != nil {
		return err
	}
	if fact.RunID != a.state.RunID {
		return errors.New("operator input targets a different application run")
	}
	route, created, err := a.state.beginOperatorInputReview(envelope.Application, fact)
	if err != nil {
		return err
	}
	if created {
		// Completion invalidation and the fresh review intent become durable in
		// the same append, before cancelling a prior child, releasing its hold,
		// claiming the input, or acknowledging the mandatory event.
		if err := a.persist("input.review_intent", nil); err != nil {
			return fmt.Errorf("persist operator-input review intent: %w", err)
		}
		route = a.state.operatorInputRoute(fact.InputID)
	}
	if route == nil {
		return errors.New("durable operator-input review disappeared")
	}
	if err := a.cleanupInvalidatedCompletion(route); err != nil {
		return err
	}
	if route.Phase == operatorInputPhaseRecovered || route.Phase == operatorInputPhaseRouted {
		return nil
	}
	if route.Phase == operatorInputPhaseClaiming {
		if err := a.claimOperatorInput(route); err != nil {
			return err
		}
	}
	return a.reconcileOneOperatorInputReview(fact.InputID, fact.Text)
}

func (a *application) cleanupInvalidatedCompletion(route *operatorInputRouteState) error {
	if route == nil {
		return errors.New("operator-input cleanup has no durable review")
	}
	changed := false
	if route.ObsoleteAgentID != "" {
		// Cancellation is a best-effort settlement of an obsolete quality
		// child. Its later authenticated terminal event is harmless because the
		// completion candidate was already invalidated durably.
		if _, err := callHostJSON(stadoAgentCancel, map[string]string{"id": route.ObsoleteAgentID}); err != nil {
			logMessage("warn", "cancel obsolete completion reviewer: "+err.Error())
		}
		route.ObsoleteAgentID = ""
		changed = true
	}
	if route.ReleaseHold {
		if a.state.Hold != nil && a.state.Hold.ID != "" && a.state.Hold.LeaseUntil.After(time.Now().UTC()) {
			if err := a.execute(transition{Actions: []action{{Kind: actionReleaseHold, Reason: "completion invalidated by queued operator input"}}}); err != nil {
				return fmt.Errorf("release invalidated completion hold: %w", err)
			}
		} else {
			// An unacknowledged placeholder granted no broker hold, and an expired
			// lease no longer fences scheduling. In both cases durable completion
			// invalidation is sufficient; no authority-shaped release is guessed.
			a.state.Hold = nil
		}
		route = a.state.operatorInputRoute(route.InputID)
		if route == nil {
			return errors.New("operator-input review disappeared after hold release")
		}
		route.ReleaseHold = false
		changed = true
	}
	if changed {
		if err := a.persist("input.completion_cleanup", nil); err != nil {
			return fmt.Errorf("persist invalidated completion cleanup: %w", err)
		}
	}
	return nil
}

func (a *application) claimOperatorInput(route *operatorInputRouteState) error {
	if route == nil || route.Phase != operatorInputPhaseClaiming {
		return errors.New("operator-input claim has no durable intent")
	}
	raw, err := callHostJSON(stadoSessionInputClaim, operatorInputClaimRequest(*route))
	if err != nil {
		return err
	}
	record, err := decodeOperatorInputRecord(raw)
	if err != nil {
		return fmt.Errorf("decode exact operator-input claim: %w", err)
	}
	prior := cloneOperatorInputRoutes(a.state.OperatorInputRoutes)
	if err := a.state.acceptOperatorInputClaim(lifecycleIdentity{SessionID: a.anchor.SessionID, SessionGeneration: a.anchor.SessionGeneration}, route, record); err != nil {
		return err
	}
	if err := a.persist("input.claimed", []string{"operator-input:" + route.InputID}); err != nil {
		a.state.OperatorInputRoutes = prior
		return fmt.Errorf("persist exact operator-input claim: %w", err)
	}
	return nil
}

func (a *application) readReviewingOperatorInputs() ([]operatorInputRecord, error) {
	raw, err := callHostJSON(stadoSessionProjectionRead, map[string]any{
		"journal_limit": 1, "control_limit": 1, "worker_limit": 16, "include_terminal": true,
	})
	if err != nil {
		return nil, err
	}
	return decodeReviewingOperatorInputs(raw)
}

func (a *application) reconcileOperatorInputRoutes() error {
	inputs, err := a.readReviewingOperatorInputs()
	if err != nil {
		return err
	}
	byID := make(map[string]operatorInputRecord, len(inputs))
	for _, input := range inputs {
		byID[input.ID] = input
	}
	for index := 0; index < len(a.state.OperatorInputRoutes); index++ {
		inputID := a.state.OperatorInputRoutes[index].InputID
		route := a.state.operatorInputRoute(inputID)
		if route == nil {
			return errors.New("operator-input review ledger changed during reconciliation")
		}
		if err := a.cleanupInvalidatedCompletion(route); err != nil {
			return err
		}
		route = a.state.operatorInputRoute(inputID)
		switch route.Phase {
		case operatorInputPhaseClaiming:
			if record, ok := byID[inputID]; ok {
				prior := cloneOperatorInputRoutes(a.state.OperatorInputRoutes)
				if err := a.state.acceptOperatorInputClaim(lifecycleIdentity{SessionID: a.anchor.SessionID, SessionGeneration: a.anchor.SessionGeneration}, route, record); err != nil {
					return err
				}
				if err := a.persist("input.claim_recovered", []string{"operator-input:" + inputID}); err != nil {
					a.state.OperatorInputRoutes = prior
					return err
				}
				if err := a.reconcileOneOperatorInputReview(inputID, record.Text); err != nil {
					return err
				}
			} else {
				if err := a.reconcileWorkerProjection(); err != nil {
					return err
				}
				route = a.state.operatorInputRoute(inputID)
				switch a.state.WorkerRunStatus {
				case workerRunCancelled, workerRunCompleted, workerRunInterrupted, workerRunStopped:
					prior := cloneOperatorInputRoutes(a.state.OperatorInputRoutes)
					if err := a.state.recoverOperatorInputAfterTerminal(route); err != nil {
						return err
					}
					if err := a.persist("input.terminal_recovered", []string{"operator-input:" + inputID}); err != nil {
						a.state.OperatorInputRoutes = prior
						return err
					}
				}
			}
		case operatorInputPhaseReviewing:
			record, ok := byID[inputID]
			if !ok {
				if err := a.reconcileWorkerProjection(); err != nil {
					return err
				}
				prior := cloneOperatorInputRoutes(a.state.OperatorInputRoutes)
				if err := a.state.recoverOperatorInputAfterTerminal(route); err != nil {
					return err
				}
				if err := a.persist("input.terminal_recovered", []string{"operator-input:" + inputID}); err != nil {
					a.state.OperatorInputRoutes = prior
					return err
				}
				continue
			}
			if err := validateOperatorInputIdentity(lifecycleIdentity{SessionID: a.anchor.SessionID, SessionGeneration: a.anchor.SessionGeneration}, route, record); err != nil || record.Version != route.ExpectedVersion {
				return errors.New("reviewing operator-input projection changed exact review identity")
			}
			if err := a.reconcileOneOperatorInputReview(inputID, record.Text); err != nil {
				return err
			}
		case operatorInputPhaseRouting:
			if err := a.reconcileWorkerProjection(); err != nil {
				return err
			}
			route = a.state.operatorInputRoute(inputID)
			switch a.state.WorkerRunStatus {
			case workerRunCancelled, workerRunCompleted, workerRunInterrupted, workerRunStopped:
				prior := cloneOperatorInputRoutes(a.state.OperatorInputRoutes)
				if err := a.state.recoverOperatorInputAfterTerminal(route); err != nil {
					return err
				}
				if err := a.persist("input.terminal_recovered", []string{"operator-input:" + inputID}); err != nil {
					a.state.OperatorInputRoutes = prior
					return err
				}
			default:
				if err := a.reconcileOneOperatorInputRoute(inputID); err != nil {
					return err
				}
			}
		case operatorInputPhaseOverflow:
			if err := a.reconcileOperatorInputOverflow(inputID); err != nil {
				return err
			}
		case operatorInputPhaseRouted, operatorInputPhaseRecovered:
		default:
			return fmt.Errorf("unknown operator-input review phase %q", route.Phase)
		}
	}
	return nil
}

func (a *application) reconcileOneOperatorInputReview(inputID, original string) error {
	route := a.state.operatorInputRoute(inputID)
	if route == nil || route.Phase != operatorInputPhaseReviewing {
		return errors.New("operator-input fresh review is unavailable")
	}
	if !validOperatorInputText(original) || digestOperatorInput(original) != route.Digest {
		return errors.New("reviewing projection changed the immutable operator input")
	}
	now := time.Now().UTC()
	if route.RetryAt.After(now) {
		return a.scheduleOperatorInputPoll(route)
	}
	if !now.Before(route.ReviewDeadline) {
		return a.handleOperatorInputReviewFailure(inputID, errors.New("fresh reviewer deadline expired"), now)
	}
	if err := a.ensureOperatorInputReviewSpawn(inputID, original); err != nil {
		return a.handleOperatorInputReviewFailure(inputID, err, now)
	}
	return a.pollOperatorInputReviewer(inputID)
}

// ensureOperatorInputReviewSpawn deliberately replays the stable logical spawn
// before every read. Same-process callback/rebind returns the same child;
// after a full process restart Fleet has no old child and returns one exact
// replacement, which is journalled before its messages can affect policy.
func (a *application) ensureOperatorInputReviewSpawn(inputID, original string) error {
	route := a.state.operatorInputRoute(inputID)
	if route == nil || route.Phase != operatorInputPhaseReviewing {
		return errors.New("operator-input fresh review is unavailable")
	}
	request, err := buildOperatorInputReviewSpawnRequest(a.contract, a.state.Config, route, original)
	if err != nil {
		return err
	}
	raw, err := callHostJSON(stadoAgentSpawn, request)
	if err != nil {
		return err
	}
	spawned, err := decodeAsyncAgentSpawnAck(raw)
	if err != nil {
		return err
	}
	prior := cloneOperatorInputRoutes(a.state.OperatorInputRoutes)
	changed, err := a.state.acceptOperatorInputReviewSpawn(route, spawned.ID)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if err := a.persist("input.review_recovered", nil); err != nil {
		a.state.OperatorInputRoutes = prior
		return fmt.Errorf("persist operator-input reviewer child: %w", err)
	}
	return nil
}

func (a *application) pollOperatorInputReviewer(inputID string) error {
	route := a.state.operatorInputRoute(inputID)
	if route == nil || route.Phase != operatorInputPhaseReviewing || route.AgentID == "" {
		return errors.New("operator-input reviewer is not durably admitted")
	}
	raw, err := callHostJSON(stadoAgentReadMessages, map[string]any{"id": route.AgentID, "since": route.AgentOffset, "timeout_ms": 0})
	if err != nil {
		return a.handleOperatorInputReviewFailure(inputID, err, time.Now().UTC())
	}
	var messages agentMessages
	if err := decodeStrict(raw, &messages); err != nil {
		return a.handleOperatorInputReviewFailure(inputID, err, time.Now().UTC())
	}
	if !isTerminalAgentStatus(messages.Status) || route.Terminal == nil {
		if !isTerminalAgentStatus(messages.Status) {
			route.AgentOffset = messages.Offset
		}
		return a.scheduleOperatorInputPoll(route)
	}
	prior := cloneOperatorInputRoutes(a.state.OperatorInputRoutes)
	consumed, err := a.state.consumeOperatorInputReviewerMessages(route, messages)
	if err != nil {
		a.state.OperatorInputRoutes = prior
		return a.handleOperatorInputReviewFailure(inputID, err, time.Now().UTC())
	}
	if !consumed {
		return a.scheduleOperatorInputPoll(route)
	}
	if err := a.persist("input.classified", []string{"operator-input:" + inputID}); err != nil {
		a.state.OperatorInputRoutes = prior
		return fmt.Errorf("persist fresh operator-input classification: %w", err)
	}
	return a.reconcileOperatorInputDisposition(inputID)
}

func (a *application) handleOperatorInputReviewFailure(inputID string, cause error, now time.Time) error {
	route := a.state.operatorInputRoute(inputID)
	if route == nil || route.Phase != operatorInputPhaseReviewing {
		return errors.New("operator-input reviewer failure has no durable job")
	}
	failure := strings.TrimSpace(cause.Error())
	if len(failure) > 4000 {
		failure = failure[:4000]
	}
	prior := cloneOperatorInputRoutes(a.state.OperatorInputRoutes)
	exhausted, err := a.state.recordOperatorInputReviewFailure(route, failure, now.UTC())
	if err != nil {
		return err
	}
	kind := "input.review_retry"
	if exhausted {
		kind = "input.review_failed_deferred"
	}
	if err := a.persist(kind, []string{"operator-input:" + inputID}); err != nil {
		a.state.OperatorInputRoutes = prior
		return fmt.Errorf("persist operator-input reviewer failure posture: %w", err)
	}
	if exhausted {
		if route.AgentID != "" {
			if _, cancelErr := callHostJSON(stadoAgentCancel, map[string]string{"id": route.AgentID}); cancelErr != nil {
				logMessage("warn", "cancel exhausted operator-input reviewer: "+cancelErr.Error())
			}
		}
		return a.reconcileOperatorInputDisposition(inputID)
	}
	return a.scheduleOperatorInputPoll(route)
}

func (a *application) reconcileOperatorInputDisposition(inputID string) error {
	route := a.state.operatorInputRoute(inputID)
	if route == nil {
		return errors.New("operator-input disposition is unavailable")
	}
	switch route.Phase {
	case operatorInputPhaseRouting:
		return a.reconcileOneOperatorInputRoute(inputID)
	case operatorInputPhaseOverflow:
		return a.reconcileOperatorInputOverflow(inputID)
	default:
		return fmt.Errorf("operator-input disposition has invalid phase %q", route.Phase)
	}
}

func (a *application) reconcileOperatorInputOverflow(inputID string) error {
	route := a.state.operatorInputRoute(inputID)
	if route == nil || route.Phase != operatorInputPhaseOverflow || route.Disposition != operatorInputDispositionLater {
		return errors.New("operator-input overflow posture is unavailable")
	}
	if err := a.reconcileWorkerProjection(); err != nil {
		return err
	}
	route = a.state.operatorInputRoute(inputID)
	switch a.state.WorkerRunStatus {
	case workerRunCancelled, workerRunCompleted, workerRunInterrupted, workerRunStopped:
		prior := cloneOperatorInputRoutes(a.state.OperatorInputRoutes)
		if err := a.state.recoverOperatorInputAfterTerminal(route); err != nil {
			return err
		}
		if err := a.persist("input.overflow_terminal_recovered", []string{"operator-input:" + inputID}); err != nil {
			a.state.OperatorInputRoutes = prior
			return err
		}
		return nil
	}
	if route.OverflowPaused {
		return nil
	}
	prior := cloneOperatorInputRoutes(a.state.OperatorInputRoutes)
	route.OverflowPaused = true
	reason := "deferred operator-input continuation bound reached; pause for native terminal recovery of the immutable original"
	if err := a.execute(transition{Actions: []action{{Kind: actionPause, Reason: reason, OmitHold: true}}, Note: "operator-input continuation overflow failed closed"}); err != nil {
		a.state.OperatorInputRoutes = prior
		return err
	}
	return nil
}

func (a *application) scheduleOperatorInputPoll(route *operatorInputRouteState) error {
	now := time.Now().UTC()
	due, err := operatorInputPollDue(route, now)
	if err != nil {
		return err
	}
	payload := map[string]string{"input_id": route.InputID, "review_id": route.ReviewID}
	if route.AgentID != "" {
		payload["agent_id"] = route.AgentID
	}
	_, err = callHostJSON(stadoTimerSchedule, map[string]any{
		"idempotency_key": "supervise-input-poll:" + digestString(route.ReviewID + "\x00" + route.AgentID + "\x00" + due.Format(time.RFC3339Nano))[:32],
		"run_id":          a.state.RunID, "expected_version": 0, "name": "input-poll-" + digestString(route.ReviewID)[:16],
		"due_at": due.Format(time.RFC3339Nano), "payload": payload,
	})
	return err
}

func (a *application) reconcileOneOperatorInputRoute(inputID string) error {
	route := a.state.operatorInputRoute(inputID)
	if route == nil || route.Phase != operatorInputPhaseRouting {
		return errors.New("operator-input route intent is unavailable")
	}
	raw, err := callHostJSON(stadoSessionInputRoute, operatorInputRouteRequest(*route))
	if err != nil {
		return err
	}
	record, err := decodeOperatorInputRecord(raw)
	if err != nil {
		return fmt.Errorf("decode exact operator-input route: %w", err)
	}
	priorRoutes, priorDeferred := cloneOperatorInputRoutes(a.state.OperatorInputRoutes), append([]deferredInputState(nil), a.state.DeferredInputs...)
	if err := a.state.acceptOperatorInputRoute(lifecycleIdentity{SessionID: a.anchor.SessionID, SessionGeneration: a.anchor.SessionGeneration}, route, record); err != nil {
		return err
	}
	if err := a.persist("input.routed", []string{"operator-input:" + inputID}); err != nil {
		a.state.OperatorInputRoutes, a.state.DeferredInputs = priorRoutes, priorDeferred
		return fmt.Errorf("persist exact operator-input route: %w", err)
	}
	return nil
}
