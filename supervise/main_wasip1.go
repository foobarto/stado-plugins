//go:build wasip1

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"
)

func main() {}

// Every authority-bearing import below is a generic stado primitive. There is
// no supervise-specific native import and no filesystem/WAL fallback.

//go:wasmimport stado stado_log
func stadoLog(levelPtr, levelLen, messagePtr, messageLen uint32)

//go:wasmimport stado stado_artifact_propose
func stadoArtifactPropose(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_artifact_query
func stadoArtifactQuery(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_artifact_edit
func stadoArtifactEdit(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_session_journal_append
func stadoSessionJournalAppend(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_session_projection_read
func stadoSessionProjectionRead(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_session_worker_request
func stadoSessionWorkerRequest(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_session_worker_resume
func stadoSessionWorkerResume(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_session_worker_cancel
func stadoSessionWorkerCancel(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_session_hold_acquire
func stadoSessionHoldAcquire(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_session_hold_release
func stadoSessionHoldRelease(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_session_request_pause
func stadoSessionRequestPause(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_session_request_stop
func stadoSessionRequestStop(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_session_complete
func stadoSessionComplete(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_session_verification_request
func stadoSessionVerificationRequest(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_timer_schedule
func stadoTimerSchedule(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_agent_spawn
func stadoAgentSpawn(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_agent_read_messages
func stadoAgentReadMessages(reqPtr, reqLen, respPtr, respCap uint32) int32

var pinned sync.Map

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 {
	if size <= 0 {
		return 0
	}
	buffer := make([]byte, size)
	pointer := uintptr(unsafe.Pointer(&buffer[0]))
	pinned.Store(pointer, buffer)
	return int32(pointer)
}

//go:wasmexport stado_free
func stadoFree(pointer int32, size int32) {
	pinned.Delete(uintptr(pointer))
	_ = size
}

type lifecycleAnchor struct {
	SessionID         string `json:"session_id"`
	SessionGeneration uint64 `json:"session_generation"`
	CanonicalRepoID   string `json:"canonical_repo_id,omitempty"`
}

type eventEnvelope struct {
	Schema      string          `json:"schema"`
	Application string          `json:"application"`
	Anchor      lifecycleAnchor `json:"anchor"`
	Sequence    uint64          `json:"sequence"`
	Event       struct {
		Kind         string          `json:"kind"`
		BrokerSeq    uint64          `json:"broker_sequence"`
		EvidenceRefs []string        `json:"evidence_refs,omitempty"`
		Data         json.RawMessage `json:"data"`
	} `json:"event"`
}

type lifecycleEnvelope struct {
	Schema      string          `json:"schema"`
	Point       string          `json:"point"`
	Application string          `json:"application"`
	Anchor      lifecycleAnchor `json:"anchor"`
	Sequence    uint64          `json:"sequence"`
	Payload     json.RawMessage `json:"payload"`
}

type applicationCommandEnvelope struct {
	Schema      string          `json:"schema"`
	Application string          `json:"application"`
	Anchor      lifecycleAnchor `json:"anchor"`
	Sequence    uint64          `json:"sequence"`
	Command     string          `json:"command"`
	Args        string          `json:"args,omitempty"`
}

type application struct {
	mu             sync.Mutex
	loaded         bool
	reloadRequired bool
	state          runState
	contract       supervisionContract
	setup          *setupState
	anchor         lifecycleAnchor
}

var app application

var errNoSelectedContract = errors.New("no selected session-scoped supervision contract; run /supervise start")
var errHoldReleaseJournal = errors.New("exact hold release committed before application journal update")

// The three manifest-declared model tools use stado's canonical per-tool
// export ABI. The guest supplies policy input; only the persistent application
// instance may bind it to a selected session-scoped contract and authenticated plan
// anchor.

//go:wasmexport stado_tool_supervise__report_progress
func stadoToolSuperviseReportProgress(inputPointer, inputLength, resultPointer, resultCapacity int32) int32 {
	app.mu.Lock()
	defer app.mu.Unlock()
	var args reportProgressArgs
	if err := decodeModelToolArgs(wasmBytes(inputPointer, inputLength), &args); err != nil {
		return writeError(resultPointer, resultCapacity, "supervise progress: "+err.Error())
	}
	return app.runModelTool(resultPointer, resultCapacity, "tool.progress", args.EvidenceRefs, func() (transition, modelToolResult, error) {
		return app.state.applyReportProgress(app.contract, args)
	})
}

//go:wasmexport stado_tool_supervise__request_pivot
func stadoToolSuperviseRequestPivot(inputPointer, inputLength, resultPointer, resultCapacity int32) int32 {
	app.mu.Lock()
	defer app.mu.Unlock()
	var args requestPivotArgs
	if err := decodeModelToolArgs(wasmBytes(inputPointer, inputLength), &args); err != nil {
		return writeError(resultPointer, resultCapacity, "supervise pivot: "+err.Error())
	}
	return app.runModelTool(resultPointer, resultCapacity, "tool.pivot", nil, func() (transition, modelToolResult, error) {
		return app.state.applyRequestPivot(app.contract, args)
	})
}

//go:wasmexport stado_tool_supervise__request_completion
func stadoToolSuperviseRequestCompletion(inputPointer, inputLength, resultPointer, resultCapacity int32) int32 {
	app.mu.Lock()
	defer app.mu.Unlock()
	var args requestCompletionArgs
	if err := decodeModelToolArgs(wasmBytes(inputPointer, inputLength), &args); err != nil {
		return writeError(resultPointer, resultCapacity, "supervise completion: "+err.Error())
	}
	return app.runModelTool(resultPointer, resultCapacity, "tool.completion", criterionInputEvidence(args.Criteria), func() (transition, modelToolResult, error) {
		return app.state.applyRequestCompletion(app.contract, args)
	})
}

// stado_plugin_event is the durable EP-0064 callback. The host must only
// acknowledge the broker event after this returns {"status":"ack"}.
//
//go:wasmexport stado_plugin_event
func stadoPluginEvent(inputPointer, inputLength, resultPointer, resultCapacity int32) int32 {
	app.mu.Lock()
	defer app.mu.Unlock()

	var envelope eventEnvelope
	if err := decodeStrict(wasmBytes(inputPointer, inputLength), &envelope); err != nil {
		return writeError(resultPointer, resultCapacity, "supervise event: "+err.Error())
	}
	if envelope.Schema != "stado.dev/lifecycle/v1" || !boundedRequired(envelope.Application, 1024) || envelope.Sequence == 0 || envelope.Event.BrokerSeq == 0 || len(envelope.Event.Data) == 0 {
		return writeError(resultPointer, resultCapacity, "supervise event: invalid authenticated envelope")
	}
	if err := app.ensureLoaded(envelope.Anchor); err != nil {
		if errors.Is(err, errNoSelectedContract) {
			return writeJSON(resultPointer, resultCapacity, map[string]string{"status": "ack"})
		}
		return writeError(resultPointer, resultCapacity, "supervise state: "+err.Error())
	}
	if app.setup != nil {
		if app.setup.Phase == setupPhaseCancelled {
			if err := app.finishSetupCancellationCleanup(); err != nil {
				return writeError(resultPointer, resultCapacity, "supervise setup cancellation cleanup: "+err.Error())
			}
			return writeJSON(resultPointer, resultCapacity, map[string]string{"status": "ack"})
		}
		if envelope.Event.Kind != "agent.down" {
			return writeError(resultPointer, resultCapacity, "supervise setup: implementation events are blocked before baseline confirmation")
		}
		if err := app.handleSetupAgentDown(envelope); err != nil {
			return writeError(resultPointer, resultCapacity, "supervise setup event: "+err.Error())
		}
		return writeJSON(resultPointer, resultCapacity, map[string]string{"status": "ack"})
	}
	if app.state.dormant() {
		return writeJSON(resultPointer, resultCapacity, map[string]string{"status": "ack"})
	}
	if app.state.Cancelled {
		if err := app.finishRunCancellationCleanup(); err != nil {
			return writeError(resultPointer, resultCapacity, "supervise cancelled cleanup: "+err.Error())
		}
		return writeJSON(resultPointer, resultCapacity, map[string]string{"status": "ack"})
	}
	if err := app.reconcilePivotEdit(); err != nil {
		return writeError(resultPointer, resultCapacity, "supervise pivot transition: "+err.Error())
	}
	// Routing is a two-record transaction: the application journals its stable
	// intent before calling the broker, then journals the exact broker result.
	// A broker commit can therefore outlive a lost guest callback or journal
	// acknowledgement. Reconcile it before consuming any later event so the
	// mandatory cursor cannot hide an unresolved input after a rebind.
	if err := app.reconcileOperatorInputRoutes(); err != nil {
		return writeError(resultPointer, resultCapacity, "supervise operator-input route: "+err.Error())
	}
	if envelope.Event.Kind == "operator.input.queued" {
		if err := app.handleOperatorInput(envelope); err != nil {
			return writeError(resultPointer, resultCapacity, "supervise operator input: "+err.Error())
		}
		return writeJSON(resultPointer, resultCapacity, map[string]string{"status": "ack"})
	}
	if err := app.reconcileHoldAndCompletion(time.Now().UTC()); err != nil {
		return writeError(resultPointer, resultCapacity, "supervise control state: "+err.Error())
	}
	if app.state.dormant() {
		return writeJSON(resultPointer, resultCapacity, map[string]string{"status": "ack"})
	}
	var err error
	switch envelope.Event.Kind {
	case "session.turn_committed":
		err = app.handleTurnCommitted(envelope.Event.BrokerSeq, envelope.Event.Data)
	case "session.verification_finished":
		err = app.handleHostVerificationFinished(envelope)
	case "agent.down":
		var facts agentDownFacts
		facts, err = decodeAgentDownFacts(envelope.Event.Data)
		if err == nil {
			var matchingReview bool
			matchingReview, err = app.state.observeAgentDown(authenticatedAgentParent{
				SessionID: envelope.Anchor.SessionID, SessionGeneration: envelope.Anchor.SessionGeneration,
				CanonicalRepoID: envelope.Anchor.CanonicalRepoID,
			}, envelope.Event.BrokerSeq, envelope.Event.EvidenceRefs, facts)
			if err == nil {
				err = app.persist("review.agent_down_observed", envelope.Event.EvidenceRefs)
			}
			if err == nil && matchingReview {
				if app.state.operatorInputReviewByChild(facts.Child.SessionID, facts.Scope.Ownership) != nil {
					err = app.reconcileOperatorInputRoutes()
				} else {
					err = app.pollReviewer()
				}
			}
		}
	case "timer.due":
		err = app.pollReviewer()
	default:
		err = fmt.Errorf("unsupported subscribed event %q", envelope.Event.Kind)
	}
	if err != nil {
		return writeError(resultPointer, resultCapacity, "supervise event: "+err.Error())
	}
	return writeJSON(resultPointer, resultCapacity, map[string]string{"status": "ack"})
}

// stado_plugin_lifecycle injects already-durable labelled steering at the next
// pre_llm boundary. It never treats mailbox prose or model output as control
// authority; holds/pause/stop stay on typed broker imports.
//
//go:wasmexport stado_plugin_lifecycle
func stadoPluginLifecycle(inputPointer, inputLength, resultPointer, resultCapacity int32) int32 {
	app.mu.Lock()
	defer app.mu.Unlock()

	var envelope lifecycleEnvelope
	if err := decodeStrict(wasmBytes(inputPointer, inputLength), &envelope); err != nil {
		return writeError(resultPointer, resultCapacity, "supervise lifecycle: "+err.Error())
	}
	if envelope.Schema != "stado.dev/lifecycle/v1" || envelope.Point != "pre_llm" {
		return writeJSON(resultPointer, resultCapacity, map[string]string{"decision": "continue"})
	}
	if err := app.ensureLoaded(envelope.Anchor); err != nil {
		if errors.Is(err, errNoSelectedContract) {
			return writeJSON(resultPointer, resultCapacity, map[string]string{"decision": "continue"})
		}
		return writeError(resultPointer, resultCapacity, "supervise state: "+err.Error())
	}
	if app.setup == nil && app.loaded && app.state.Cancelled {
		if err := app.finishRunCancellationCleanup(); err != nil {
			return writeError(resultPointer, resultCapacity, "supervise cancelled cleanup: "+err.Error())
		}
		return writeJSON(resultPointer, resultCapacity, map[string]string{"decision": "continue"})
	}
	if app.setup == nil && app.loaded {
		if app.state.dormant() {
			return writeJSON(resultPointer, resultCapacity, map[string]string{"decision": "continue"})
		}
		if err := app.reconcileOperatorInputRoutes(); err != nil {
			return writeError(resultPointer, resultCapacity, "supervise operator-input route: "+err.Error())
		}
	}
	if app.setup != nil {
		if app.setup.Phase == setupPhaseCancelled {
			if err := app.finishSetupCancellationCleanup(); err != nil {
				return writeError(resultPointer, resultCapacity, "supervise setup cancellation cleanup: "+err.Error())
			}
			return writeJSON(resultPointer, resultCapacity, map[string]string{"decision": "continue"})
		}
		return writeJSON(resultPointer, resultCapacity, map[string]string{
			"decision": "deny", "reason": "supervise setup is not quality-confirmed: " + app.setup.statusMessage(),
		})
	}
	if err := app.reconcileHoldAndCompletion(time.Now().UTC()); err != nil {
		return writeError(resultPointer, resultCapacity, "supervise control state: "+err.Error())
	}
	if err := app.reconcilePivotEdit(); err != nil {
		return writeError(resultPointer, resultCapacity, "supervise pivot transition: "+err.Error())
	}
	// An independently verified completion is terminal for this quality
	// workflow. The typed broker handoff above records normal success; this deny
	// is only a final local barrier against another provider turn racing cleanup.
	if app.state.Completed {
		if app.state.dormant() {
			return writeJSON(resultPointer, resultCapacity, map[string]string{"decision": "continue"})
		}
		return writeJSON(resultPointer, resultCapacity, map[string]string{
			"decision": "deny",
			"reason":   "supervise run is independently verified complete and handed off successfully",
		})
	}
	var payload struct {
		System string `json:"system"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return writeError(resultPointer, resultCapacity, "supervise lifecycle payload: "+err.Error())
	}
	var additions []string
	toolContext, ready, err := supervisionModelContext(app.state, app.contract)
	if err != nil {
		return writeError(resultPointer, resultCapacity, "supervise tool context: "+err.Error())
	}
	if ready {
		additions = append(additions, toolContext)
	}
	if len(app.state.AdvisorySteering) > 0 {
		additions = append(additions, strings.Join(app.state.AdvisorySteering, "\n\n"))
		app.state.AdvisorySteering = nil
		if err := app.persist("steering.delivered", nil); err != nil {
			return writeError(resultPointer, resultCapacity, "persist steering delivery: "+err.Error())
		}
	}
	if len(additions) == 0 {
		return writeJSON(resultPointer, resultCapacity, map[string]string{"decision": "continue"})
	}
	if payload.System != "" {
		payload.System += "\n\n"
	}
	payload.System += strings.Join(additions, "\n\n")
	return writeJSON(resultPointer, resultCapacity, map[string]any{
		"decision": "mutate",
		"mutation": map[string]string{"system": payload.System},
	})
}

// stado_plugin_tick is only a bounded yield opportunity. Durable agent.down
// and timer.due delivery is still required; tick polling is a recovery assist,
// not the delivery guarantee.
//
//go:wasmexport stado_plugin_tick
func stadoPluginTick() int32 {
	app.mu.Lock()
	defer app.mu.Unlock()
	if app.reloadRequired {
		anchor := app.anchor
		if err := app.ensureLoaded(anchor); err != nil {
			logMessage("error", "supervise durable reload: "+err.Error())
			return 0
		}
	}
	if app.setup == nil && app.loaded && app.state.Cancelled {
		if err := app.finishRunCancellationCleanup(); err != nil {
			logMessage("error", "supervise cancelled cleanup: "+err.Error())
		}
		return 0
	}
	if app.setup == nil && app.loaded {
		if app.state.dormant() {
			return 0
		}
		if err := app.reconcileOperatorInputRoutes(); err != nil {
			logMessage("warn", "supervise operator-input recovery tick: "+err.Error())
			return 0
		}
	}
	if app.setup != nil {
		if app.setup.Phase == setupPhaseCancelled {
			if err := app.finishSetupCancellationCleanup(); err != nil {
				logMessage("warn", "supervise setup cancellation cleanup: "+err.Error())
			}
			return 0
		}
		if app.setup.Phase == setupPhaseRunning && app.setup.Terminal != nil {
			if err := app.pollBaseline(); err != nil {
				logMessage("warn", "supervise baseline tick: "+err.Error())
			}
		} else if app.setup.Phase == setupPhaseRunning {
			if err := app.recoverBaselineSpawn(); err != nil {
				logMessage("warn", "supervise baseline recovery tick: "+err.Error())
			}
		}
		return 0
	}
	if !app.loaded {
		return 0
	}
	if err := app.reconcileHoldAndCompletion(time.Now().UTC()); err != nil {
		logMessage("error", "supervise control state: "+err.Error())
		return 0
	}
	if err := app.reconcilePivotEdit(); err != nil {
		logMessage("error", "supervise pivot transition: "+err.Error())
		return 0
	}
	if app.state.PendingReview == nil {
		return 0
	}
	var err error
	if app.state.PendingReview.AgentID == "" {
		err = app.recoverPendingReview()
	} else {
		err = app.pollReviewer()
	}
	if err != nil {
		logMessage("warn", "supervise tick: "+err.Error())
	}
	return 0
}

// stado_plugin_command receives only host-selected commands declared in the
// signed manifest. Setup, baseline review, confirmation, and lifecycle commands
// are owned by this persistent application and use only generic host imports.
//
// The candidate remains application-private workflow configuration. Selecting
// its exact ID/version adds no capability and does not promote it into general
// prompt authority; command origin and UI transport are not security grants.
//
//go:wasmexport stado_plugin_command
func stadoPluginCommand(inputPointer, inputLength, resultPointer, resultCapacity int32) int32 {
	app.mu.Lock()
	defer app.mu.Unlock()

	var envelope applicationCommandEnvelope
	if err := decodeStrict(wasmBytes(inputPointer, inputLength), &envelope); err != nil {
		return writeError(resultPointer, resultCapacity, "supervise command envelope: "+err.Error())
	}
	if envelope.Schema != "stado.dev/application-command/v1" || envelope.Application == "" || envelope.Sequence == 0 || envelope.Command != "supervise" {
		return writeError(resultPointer, resultCapacity, "supervise command: invalid authenticated envelope")
	}
	if envelope.Anchor.SessionID == "" || envelope.Anchor.SessionGeneration == 0 {
		return writeError(resultPointer, resultCapacity, "supervise command: authenticated session identity unavailable")
	}
	if (app.loaded || app.setup != nil) && app.anchor != envelope.Anchor {
		return writeError(resultPointer, resultCapacity, "supervise command: lifecycle application anchor changed")
	}
	if app.reloadRequired {
		if err := app.ensureLoaded(envelope.Anchor); err != nil && !errors.Is(err, errNoSelectedContract) {
			return writeError(resultPointer, resultCapacity, "supervise durable reload: "+err.Error())
		}
	}
	// Cancellation remains an operator escape hatch even when a reviewing
	// input's projection or reviewer is temporarily unavailable. Native worker
	// cancellation recovers the immutable input; later reconciliation marks the
	// application job terminal-recovered.
	cancelCommand := strings.TrimSpace(envelope.Args) == "cancel"
	if app.setup == nil && app.loaded && !cancelCommand {
		if err := app.reconcileOperatorInputRoutes(); err != nil {
			return writeError(resultPointer, resultCapacity, "supervise operator-input route: "+err.Error())
		}
	}
	result, err := app.handleCommand(envelope)
	if err != nil {
		return writeError(resultPointer, resultCapacity, "supervise command: "+err.Error())
	}
	return writeJSON(resultPointer, resultCapacity, result)
}

func (a *application) ensureLoaded(authenticated lifecycleAnchor) error {
	if authenticated.SessionID == "" || authenticated.SessionGeneration == 0 {
		return errors.New("authenticated session identity unavailable")
	}
	if a.reloadRequired {
		// A journal call can be interrupted after the broker commits but before
		// the guest sees its acknowledgement. Discard all instance-only state
		// and refold the authenticated projection before accepting another
		// command, tool, event, or lifecycle boundary.
		a.loaded, a.state, a.contract, a.setup, a.anchor = false, runState{}, supervisionContract{}, nil, lifecycleAnchor{}
	}
	if a.loaded || a.setup != nil {
		if a.anchor != authenticated {
			return errors.New("lifecycle application anchor changed")
		}
		return nil
	}
	projectionRaw, err := callHostJSON(stadoSessionProjectionRead, map[string]any{
		"journal_limit": 256, "control_limit": 32, "verification_limit": 32, "include_terminal": true,
	})
	if err != nil {
		a.reloadRequired = true
		return fmt.Errorf("durable projection unavailable: %w", err)
	}
	durable, ok, err := latestDurableSuperviseState(projectionRaw)
	if err != nil {
		a.reloadRequired = true
		return err
	}
	if !ok {
		a.reloadRequired = false
		return errNoSelectedContract
	}
	if durable.Setup != nil {
		a.setup, a.anchor, a.reloadRequired = durable.Setup, authenticated, false
		return nil
	}
	if durable.Run == nil {
		return errors.New("durable supervise projection contains no setup or run state")
	}
	state := *durable.Run
	if state.dormant() {
		if err := validateDormantRunState(state); err != nil {
			return err
		}
		a.state, a.contract, a.anchor, a.loaded, a.reloadRequired = state, supervisionContract{}, authenticated, true, false
		return nil
	}
	contract, err := querySelectedContract(state.ContractID, state.ContractVersion)
	if err != nil {
		return err
	}
	if state.RunID != contract.RunID || !boundedRequired(state.WorkerObjective, 4<<10) || !configsEqual(state.Config, contract.Config) || state.CriteriaTotal != len(contract.Acceptance) || len(state.Criteria) != len(contract.Acceptance) || state.PlanTotal != len(contract.Plan) || len(state.PlanSteps) != len(contract.Plan) || state.WorkerConflict != "reject" && state.WorkerConflict != "replace_operator_loop" {
		return errors.New("durable state does not match selected contract candidate")
	}
	for index := range contract.Plan {
		if state.PlanSteps[index] != contract.Plan[index].ID {
			return errors.New("durable state does not match selected contract plan")
		}
	}
	if err := state.PendingPivot.validateForState(state, contract); err != nil {
		return err
	}
	if err := state.validateHostVerificationDurableState(); err != nil {
		return err
	}
	a.state = state
	a.contract = contract
	a.anchor = authenticated
	a.loaded = true
	a.reloadRequired = false
	return nil
}

func (a *application) handleTurnCommitted(brokerSequence uint64, raw json.RawMessage) error {
	facts, err := decodeTurnCommittedFacts(raw)
	if err != nil {
		return err
	}
	var staged transition
	if a.state.CurrentAnchor.ActiveStep == "completion" && a.state.PendingHostVerification == nil && a.state.PendingReview == nil && a.state.PendingPivot == nil && a.state.PendingStepClaim == nil && a.state.Hold == nil && !a.state.Cancelled && !a.state.Completed {
		if err := a.reconcileWorkerProjection(); err != nil {
			return fmt.Errorf("reconcile worker before host verification: %w", err)
		}
		if a.state.shouldStageHostVerification(facts) {
			staged, err = a.state.stageHostVerification(brokerSequence, facts)
			if err != nil {
				return err
			}
		}
	}
	change, evidence, replay, err := a.state.applyTurnCommittedFacts(brokerSequence, facts, "sha256:"+digestString(string(raw)))
	if err != nil {
		return err
	}
	if replay {
		if a.state.PendingHostVerification != nil {
			return a.ensureHostVerificationRequest()
		}
		if a.state.PendingReview != nil && a.state.PendingReview.AgentID == "" {
			return a.recoverPendingReview()
		}
		return nil
	}
	if err := a.persist("worker.turn_observed", evidence); err != nil {
		return err
	}
	change.Actions = append(staged.Actions, change.Actions...)
	if err := a.execute(change); err != nil {
		return err
	}
	return a.ensureHostVerificationRequest()
}

func (a *application) pollReviewer() error {
	now := time.Now().UTC()
	if err := a.reconcileHoldAndCompletion(now); err != nil {
		return err
	}
	pending := a.state.PendingReview
	if pending == nil {
		return nil
	}
	if pending.RetryAt.After(now) {
		return a.scheduleReviewRetry(pending)
	}
	if err := a.ensurePendingReviewSpawn(); err != nil {
		return err
	}
	pending = a.state.PendingReview
	if pending == nil || pending.AgentID == "" {
		return errors.New("pending review has no admitted child after recovery")
	}
	raw, err := callHostJSON(stadoAgentReadMessages, map[string]any{
		"id": pending.AgentID, "since": pending.AgentOffset, "timeout_ms": 0,
	})
	if err != nil {
		return err
	}
	var messages agentMessages
	if err := decodeStrict(raw, &messages); err != nil {
		return err
	}
	verdictRaw, _, hasVerdict := detectReviewResult(messages.Messages)
	var verdictEvidence []string
	if hasVerdict {
		result, decodeErr := decodeReviewerResult(verdictRaw)
		if decodeErr != nil {
			return decodeErr
		}
		verdictEvidence = append([]string(nil), result.Verdict.EvidenceRefs...)
	}
	transition, consumed, err := a.state.consumeReviewerMessages(messages, now)
	if err != nil {
		return err
	}
	if consumed {
		journalKind := "review.failed"
		if hasVerdict {
			journalKind = "review.verdict"
		}
		if hasVerdict && a.state.Completed {
			journalKind = "run.completed"
		}
		if err := a.persist(journalKind, verdictEvidence); err != nil {
			return err
		}
		if err := a.execute(transition); err != nil {
			return err
		}
		if err := a.reconcilePivotEdit(); err != nil {
			return err
		}
		if err := a.startQueuedReview(); err != nil {
			return err
		}
		if a.state.Completed {
			return a.finishCompletionHandoff()
		}
		return nil
	}
	// Do not advance past a provisional assistant verdict before the terminal
	// metadata exists; the next poll must still observe that verdict together
	// with host token facts and any later cleanup fingerprint.
	if _, _, provisional := detectReviewResult(messages.Messages); !provisional {
		pending.AgentOffset = messages.Offset
	}
	return a.schedulePoll(pending)
}

type modelToolApply func() (transition, modelToolResult, error)

func (a *application) runModelTool(resultPointer, resultCapacity int32, journalKind string, evidence []string, apply modelToolApply) int32 {
	if a.reloadRequired {
		anchor := a.anchor
		if err := a.ensureLoaded(anchor); err != nil {
			return writeError(resultPointer, resultCapacity, "reload durable supervise state: "+err.Error())
		}
	}
	if !a.loaded || a.contract.ArtifactID == "" || a.contract.ArtifactVersion == 0 {
		return writeError(resultPointer, resultCapacity, "no selected session-scoped supervision contract")
	}
	if a.state.Cancelled {
		return writeError(resultPointer, resultCapacity, "supervise workflow is durably cancelled")
	}
	selected, err := querySelectedContract(a.contract.ArtifactID, a.contract.ArtifactVersion)
	if err != nil || selected.ArtifactID != a.contract.ArtifactID || selected.ArtifactVersion != a.contract.ArtifactVersion {
		return writeError(resultPointer, resultCapacity, "selected supervision contract candidate is unavailable")
	}
	a.contract = selected
	stateRaw, err := json.Marshal(a.state)
	if err != nil {
		return writeError(resultPointer, resultCapacity, "snapshot supervise state: "+err.Error())
	}
	var before runState
	if err := json.Unmarshal(stateRaw, &before); err != nil {
		return writeError(resultPointer, resultCapacity, "snapshot supervise state: "+err.Error())
	}
	change, result, err := apply()
	if err != nil {
		a.state = before
		return writeError(resultPointer, resultCapacity, err.Error())
	}
	if result.IdempotentReplay {
		return writeJSON(resultPointer, resultCapacity, result)
	}
	if err := a.persist(journalKind, boundedModelEvidenceRefs(evidence)); err != nil {
		a.state = before
		return writeError(resultPointer, resultCapacity, "persist supervise model tool: "+err.Error())
	}
	if err := a.execute(change); err != nil {
		return writeError(resultPointer, resultCapacity, "execute supervise review signal: "+err.Error())
	}
	return writeJSON(resultPointer, resultCapacity, result)
}

func (a *application) recoverPendingReview() error {
	now := time.Now().UTC()
	if err := a.reconcileHoldAndCompletion(now); err != nil {
		return err
	}
	pending := a.state.PendingReview
	if pending == nil || pending.AgentID != "" {
		return nil
	}
	if pending.RetryAt.After(now) {
		return a.scheduleReviewRetry(pending)
	}
	var actions []action
	if a.state.Hold != nil && a.state.Hold.ID == "" {
		actions = append(actions, action{Kind: actionAcquireHold, Reason: a.state.Hold.Reason})
	}
	actions = append(actions, action{Kind: actionStartReview, Review: cloneReview(pending), TokenBudget: reviewTokenBudget(a.state.Config, pending)})
	return a.execute(transition{Actions: actions, Note: "recovering durable pending review"})
}

func (a *application) execute(change transition) error {
	for _, next := range change.Actions {
		switch next.Kind {
		case actionAcquireHold:
			response, err := callHostJSON(stadoSessionHoldAcquire, map[string]any{
				"run_id": a.state.RunID, "expected_version": 0,
				"reason_code": holdReasonCode, "reason": next.Reason,
				"ttl_ms": int64(a.state.Config.HoldTTLSeconds) * int64(time.Second/time.Millisecond),
			})
			if err != nil {
				return err
			}
			var hold holdLeaseAck
			if err := json.Unmarshal(response, &hold); err != nil {
				return errors.New("broker returned invalid scheduling hold")
			}
			if err := acceptInitialHoldLease(&a.state, hold, next.Reason, time.Now().UTC()); err != nil {
				return err
			}
			if err := a.persist("hold.acquired", nil); err != nil {
				return err
			}
			if err := a.scheduleHoldWake(time.Now().UTC()); err != nil {
				change := a.state.failHoldRenewal(fmt.Errorf("schedule initial durable hold renewal: %w", err))
				if closeErr := a.execute(change); closeErr != nil {
					return fmt.Errorf("schedule initial durable hold renewal: %v; fail-closed pause also failed: %w", err, closeErr)
				}
				return fmt.Errorf("schedule initial durable hold renewal: %w", err)
			}
		case actionReleaseHold:
			if a.state.Hold == nil || a.state.Hold.ID == "" || a.state.Hold.Version == 0 {
				return errors.New("cannot release an unacknowledged hold")
			}
			raw, err := callHostJSON(stadoSessionHoldRelease, map[string]any{
				"id": a.state.Hold.ID, "run_id": a.state.RunID,
				"expected_version": a.state.Hold.Version,
			})
			if err != nil {
				return err
			}
			var released holdLeaseAck
			if err := json.Unmarshal(raw, &released); err != nil {
				return errors.New("broker returned invalid exact hold release")
			}
			prior, err := acceptReleasedHoldLease(&a.state, released)
			if err != nil {
				return err
			}
			if err := a.persist("hold.released", nil); err != nil {
				a.state.Hold = prior
				return fmt.Errorf("%w: %v", errHoldReleaseJournal, err)
			}
		case actionStartReview:
			if err := a.startReview(next.Review, next.TokenBudget); err != nil {
				return err
			}
		case actionScheduleReviewRetry:
			if err := a.scheduleReviewRetry(next.Review); err != nil {
				return err
			}
		case actionSteer:
			label := "[stado supervise watchdog correction]"
			if next.Label != "" {
				label = next.Label
			}
			if next.Advisory {
				label = "[stado supervise earlier-anchor advisory; reconcile with current state]"
			}
			a.state.AdvisorySteering = appendBounded(a.state.AdvisorySteering, label+"\n"+next.Correction+"\nReason: "+next.Reason, 2)
			if err := a.persist("steering.queued", nil); err != nil {
				return err
			}
		case actionPause, actionStop:
			var evidenceRefs []string
			if a.state.LastVerdict != nil {
				evidenceRefs = append(evidenceRefs, a.state.LastVerdict.EvidenceRefs...)
			}
			request := map[string]any{
				"run_id": a.state.RunID, "reason_code": "supervise.watchdog",
				"reason": next.Reason, "evidence_refs": evidenceRefs,
			}
			if a.state.Hold != nil && !next.OmitHold {
				request["hold_id"] = a.state.Hold.ID
			}
			call := stadoSessionRequestPause
			kind := "pause.requested"
			if next.Kind == actionStop {
				call, kind = stadoSessionRequestStop, "stop.requested"
			}
			if _, err := callHostJSON(call, request); err != nil {
				return err
			}
			if err := a.persist(kind, evidenceRefs); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown policy action %q", next.Kind)
		}
	}
	return nil
}

func (a *application) startReview(review *reviewRequest, tokenBudget int) error {
	if review == nil || review.Attempt == 0 || !review.RetryAt.IsZero() || a.state.PendingReview == nil || review.ID != a.state.PendingReview.ID || review.Attempt != a.state.PendingReview.Attempt {
		return errors.New("review request is no longer current")
	}
	request, err := a.buildReviewSpawnRequest(review, tokenBudget)
	if err != nil {
		return err
	}
	response, err := callHostJSON(stadoAgentSpawn, request)
	if err != nil {
		return err
	}
	spawned, err := decodeAsyncAgentSpawnAck(response)
	if err != nil {
		return err
	}
	a.state.PendingReview.AgentID = spawned.ID
	a.state.PendingReview.AgentOffset = 0
	a.state.PendingReview.TokenBudget = tokenBudget
	if err := a.persist("review.started", nil); err != nil {
		return err
	}
	return a.schedulePoll(a.state.PendingReview)
}

func (a *application) buildReviewSpawnRequest(review *reviewRequest, tokenBudget int) (watchdogSpawnRequest, error) {
	if review != nil && review.Purpose == reviewPurposeVerifier {
		return buildCompletionVerifierSpawnRequest(a.contract, a.state.Config, review, a.state.PendingCompletion, tokenBudget)
	}
	return buildWatchdogSpawnRequest(a.contract, a.state.Config, review, tokenBudget)
}

// ensurePendingReviewSpawn replays one durable logical admission before every
// poll. In-process/module rebind returns the exact original child. A process
// restart intentionally loses both Fleet children and its claim map, so one
// replacement is admitted under the same key and journalled before its result
// can affect policy.
func (a *application) ensurePendingReviewSpawn() error {
	pending := a.state.PendingReview
	if pending == nil {
		return nil
	}
	tokenBudget := pending.TokenBudget
	if tokenBudget == 0 {
		tokenBudget = reviewTokenBudget(a.state.Config, pending)
	}
	request, err := a.buildReviewSpawnRequest(pending, tokenBudget)
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
	if spawned.ID == pending.AgentID {
		return nil
	}
	prior := cloneReview(pending)
	pending.AgentID, pending.AgentOffset, pending.TokenBudget, pending.Terminal, pending.RetryAt = spawned.ID, 0, tokenBudget, nil, time.Time{}
	if err := a.persist("review.recovered", nil); err != nil {
		a.state.PendingReview = prior
		return fmt.Errorf("persist recovered review child: %w", err)
	}
	return nil
}

func (a *application) scheduleReviewRetry(review *reviewRequest) error {
	if review == nil || review.ID == "" || review.Attempt < 2 || review.RetryAt.IsZero() || review.AgentID != "" || review.Terminal != nil {
		return errors.New("review retry timer has no durable staged attempt")
	}
	digest := digestString(a.state.RunID + "\x00" + review.ID)[:16]
	_, err := callHostJSON(stadoTimerSchedule, map[string]any{
		"idempotency_key": fmt.Sprintf("supervise-review-retry:%s:a%d", digest, review.Attempt),
		"run_id":          a.state.RunID, "expected_version": 0,
		"name":    fmt.Sprintf("review-retry-%s-a%d", digest, review.Attempt),
		"due_at":  review.RetryAt.UTC().Format(time.RFC3339Nano),
		"payload": map[string]any{"review_id": review.ID, "attempt": review.Attempt},
	})
	return err
}

func (a *application) schedulePoll(review *reviewRequest) error {
	if review == nil || review.AgentID == "" {
		return nil
	}
	_, err := callHostJSON(stadoTimerSchedule, map[string]any{
		"run_id": a.state.RunID, "expected_version": 0,
		"name":    "review-poll-" + review.ID,
		"due_at":  time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano),
		"payload": map[string]string{"review_id": review.ID, "agent_id": review.AgentID},
	})
	return err
}

func (a *application) maintainHold(now time.Time) error {
	renewed, renewalErr := maintainHoldLease(&a.state, now, func(request holdRenewalRequest) (holdLeaseAck, error) {
		raw, err := callHostJSON(stadoSessionHoldAcquire, request)
		if err != nil {
			return holdLeaseAck{}, err
		}
		var ack holdLeaseAck
		if err := json.Unmarshal(raw, &ack); err != nil {
			return holdLeaseAck{}, errors.New("broker returned malformed hold renewal")
		}
		return ack, nil
	})
	if renewalErr != nil {
		change := a.state.failHoldRenewal(renewalErr)
		if closeErr := a.execute(change); closeErr != nil {
			return fmt.Errorf("%v; fail-closed pause also failed: %w", renewalErr, closeErr)
		}
		return renewalErr
	}
	if renewed {
		if err := a.persist("hold.renewed", nil); err != nil {
			return err
		}
	}
	if err := a.scheduleHoldWake(now); err != nil {
		change := a.state.failHoldRenewal(fmt.Errorf("schedule durable hold renewal: %w", err))
		if closeErr := a.execute(change); closeErr != nil {
			return fmt.Errorf("schedule durable hold renewal: %v; fail-closed pause also failed: %w", err, closeErr)
		}
		return fmt.Errorf("schedule durable hold renewal: %w", err)
	}
	return nil
}

// reconcileHoldAndCompletion orders the two restart-sensitive CAS workflows.
// A journal may lag a successfully released broker hold if the process crashed
// after the release response. Once handoff is durable, replay that exact release
// before trying to renew; otherwise renew first so a long-running handoff never
// opens the worker scheduling barrier.
func (a *application) reconcileHoldAndCompletion(now time.Time) error {
	if a.state.Cancelled {
		if a.state.Hold == nil {
			return nil
		}
		return a.execute(transition{Actions: []action{{Kind: actionReleaseHold, Reason: "supervise workflow cancelled"}}})
	}
	if a.state.Completed && a.state.CompletionHandedOff {
		firstReleaseErr := a.finishCompletionHandoff()
		if firstReleaseErr == nil {
			return nil
		}
		if errors.Is(firstReleaseErr, errHoldReleaseJournal) {
			// The broker release is known to be committed. Replay its exact
			// idempotency key and persist the returned terminal hold; attempting
			// renewal here would incorrectly operate on a released lease.
			return a.finishCompletionHandoff()
		}
		if err := a.maintainHold(now); err != nil {
			return fmt.Errorf("release completion hold: %v; maintain before retry: %w", firstReleaseErr, err)
		}
		if err := a.finishCompletionHandoff(); err != nil {
			return fmt.Errorf("release completion hold after renewal: %w", err)
		}
		return nil
	}
	if err := a.maintainHold(now); err != nil {
		return err
	}
	if a.state.Completed {
		return a.finishCompletionHandoff()
	}
	return nil
}

func (a *application) finishCompletionHandoff() error {
	if !a.state.Completed {
		return nil
	}
	request, err := prepareCompletionHandoff(a.state)
	if err != nil {
		return err
	}
	if !a.state.CompletionHandedOff {
		raw, err := callHostJSON(stadoSessionComplete, request)
		if err != nil {
			return err
		}
		var completion completionHandoffAck
		if err := decodeStrictBytes(raw, &completion); err != nil {
			return errors.New("broker returned malformed successful-completion handoff")
		}
		if err := acceptCompletionHandoff(&a.state, completion); err != nil {
			return err
		}
		if err := a.persist("run.handed_off", request.EvidenceRefs); err != nil {
			return err
		}
	}
	if a.state.Hold != nil {
		return a.execute(transition{Actions: []action{{Kind: actionReleaseHold, Reason: "successful completion handoff recorded"}}})
	}
	return nil
}

func (a *application) scheduleHoldWake(now time.Time) error {
	hold := a.state.Hold
	if hold == nil || hold.ID == "" {
		return nil
	}
	due := hold.RenewAt.UTC()
	if due.IsZero() {
		return errors.New("active hold has no renewal deadline")
	}
	if !due.After(now) {
		due = now.Add(time.Second)
	}
	idPart := digestString(hold.ID)[:16]
	_, err := callHostJSON(stadoTimerSchedule, map[string]any{
		"idempotency_key": fmt.Sprintf("supervise-hold-wake:%s:v%d", idPart, hold.Version),
		"run_id":          a.state.RunID, "expected_version": 0,
		"name":   fmt.Sprintf("hold-renew-%s-v%d", idPart, hold.Version),
		"due_at": due.Format(time.RFC3339Nano),
		"payload": map[string]any{
			"hold_id": hold.ID, "hold_version": hold.Version,
		},
	})
	return err
}

func (a *application) startQueuedReview() error {
	if a.state.Completed || a.state.PendingReview != nil || len(a.state.QueuedSignals) == 0 {
		return nil
	}
	review := &reviewRequest{
		ID:     "review-queued-" + fmt.Sprint(a.state.CurrentAnchor.SessionSequence),
		Anchor: a.state.CurrentAnchor, Signals: append([]signal(nil), a.state.QueuedSignals...), Purpose: reviewPurposeWatchdog, Attempt: 1, Handoff: a.state.WatchdogHandoff,
	}
	a.state.QueuedSignals = nil
	a.state.PendingReview = review
	var actions []action
	if a.state.Config.Mode == modeLive && a.state.Config.StrictLiveBarrier && a.state.Hold == nil {
		a.state.Hold = &holdState{Reason: "strict live review of quality signals queued behind prior work"}
		actions = append(actions, action{Kind: actionAcquireHold, Reason: a.state.Hold.Reason})
	}
	if err := a.persist("review.queued_promoted", nil); err != nil {
		return err
	}
	actions = append(actions, action{Kind: actionStartReview, Review: cloneReview(review), TokenBudget: a.state.Config.WatchdogTokenBudget})
	return a.execute(transition{Actions: actions})
}

func (a *application) persist(kind string, evidence []string) error {
	stateJSON, err := json.Marshal(a.state)
	if err != nil {
		a.reloadRequired = true
		return err
	}
	// The broker caps application journal data at 64 KiB. Keep headroom for
	// future envelope fields and fail explicitly instead of relying on a remote
	// generic rejection after an authority transition.
	if len(stateJSON) > 60<<10 {
		a.reloadRequired = true
		return errors.New("durable supervise state exceeds 60 KiB")
	}
	summary := "supervise application transition: " + kind
	raw, err := callHostJSON(stadoSessionJournalAppend, map[string]any{
		"run_id": a.state.RunID, "kind": kind,
		"summary": summary,
		"data":    json.RawMessage(stateJSON), "evidence_refs": evidence,
	})
	if err == nil {
		err = validateJournalAck(raw, lifecycleIdentity{SessionID: a.anchor.SessionID, SessionGeneration: a.anchor.SessionGeneration}, a.state.RunID, kind, summary, stateJSON, evidence)
	}
	if err != nil {
		a.reloadRequired = true
	}
	return err
}

func querySelectedContract(artifactID string, artifactVersion uint64) (supervisionContract, error) {
	request, err := selectedContractQueryRequest(artifactID, artifactVersion)
	if err != nil {
		return supervisionContract{}, err
	}
	raw, err := callHostJSON(stadoArtifactQuery, request)
	if err != nil {
		return supervisionContract{}, err
	}
	return selectExactContractCandidate(raw, artifactID, artifactVersion)
}

func latestState(raw []byte) (runState, bool, error) {
	var projection struct {
		Journal []struct {
			Kind string          `json:"kind"`
			Data json.RawMessage `json:"data"`
		} `json:"journal"`
	}
	if err := json.Unmarshal(raw, &projection); err != nil {
		return runState{}, false, err
	}
	for i := len(projection.Journal) - 1; i >= 0; i-- {
		entry := projection.Journal[i]
		if !runJournalKind(entry.Kind) {
			continue
		}
		var state runState
		if err := decodeStrict(entry.Data, &state); err != nil {
			return runState{}, false, err
		}
		if state.Schema != policySchema {
			return runState{}, false, errors.New("unsupported durable supervise state schema")
		}
		return state, true, nil
	}
	return runState{}, false, nil
}

type hostJSONCall func(uint32, uint32, uint32, uint32) int32

func callHostJSON(call hostJSONCall, request any) ([]byte, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	response := make([]byte, 1<<20)
	requestPointer, requestLength := slicePointer(raw)
	responsePointer, responseCapacity := slicePointer(response)
	length := call(requestPointer, requestLength, responsePointer, responseCapacity)
	if length < 0 {
		size := -int(length)
		if size > len(response) {
			size = len(response)
		}
		return nil, errors.New(string(response[:size]))
	}
	if length == 0 {
		return []byte(`{}`), nil
	}
	if int(length) > len(response) {
		return nil, errors.New("host response exceeds buffer")
	}
	return append([]byte(nil), response[:length]...), nil
}

func decodeStrict(raw []byte, target any) error {
	return decodeStrictBytes(raw, target)
}

func criterionInputEvidence(criteria []criterionEvidenceInput) []string {
	var result []string
	for _, item := range criteria {
		result = append(result, item.EvidenceRefs...)
	}
	return boundedModelEvidenceRefs(result)
}

func wasmBytes(pointer, size int32) []byte {
	if pointer == 0 || size <= 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(pointer))), int(size))
}

func slicePointer(data []byte) (uint32, uint32) {
	if len(data) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&data[0]))), uint32(len(data))
}

func writeJSON(pointer, capacity int32, value any) int32 {
	raw, err := json.Marshal(value)
	if err != nil {
		return writeError(pointer, capacity, err.Error())
	}
	if int32(len(raw)) > capacity || capacity <= 0 {
		return -1
	}
	copy(wasmBytes(pointer, capacity), raw)
	return int32(len(raw))
}

func writeError(pointer, capacity int32, message string) int32 {
	raw := []byte(message)
	if capacity <= 0 {
		return -1
	}
	if int32(len(raw)) > capacity {
		raw = raw[:capacity]
	}
	copy(wasmBytes(pointer, capacity), raw)
	if len(raw) == 0 {
		return -1
	}
	return -int32(len(raw))
}

func logMessage(level, message string) {
	levelBytes, messageBytes := []byte(level), []byte(message)
	levelPointer, levelLength := slicePointer(levelBytes)
	messagePointer, messageLength := slicePointer(messageBytes)
	stadoLog(levelPointer, levelLength, messagePointer, messageLength)
}
