//go:build wasip1

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

//go:wasmimport stado stado_ui_choose
func stadoUIChoose(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_ui_approve
func stadoUIApprove(titlePtr, titleLen, bodyPtr, bodyLen uint32) int32

//go:wasmimport stado stado_ui_print
func stadoUIPrint(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_ui_render
func stadoUIRender(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_agent_cancel
func stadoAgentCancel(reqPtr, reqLen, respPtr, respCap uint32) int32

func (a *application) handleCommand(envelope applicationCommandEnvelope) (proposalCommandResult, error) {
	args := strings.TrimSpace(envelope.Args)
	command, value := args, ""
	if before, after, ok := strings.Cut(args, " "); ok {
		command, value = before, strings.TrimSpace(after)
	}
	switch command {
	case "status":
		if value != "" {
			return proposalCommandResult{Status: "error", Message: "usage: /supervise status"}, nil
		}
		return a.commandStatus(envelope.Anchor)
	case "resume":
		if value != "" {
			return proposalCommandResult{Status: "error", Message: "usage: /supervise resume"}, nil
		}
		return a.commandResume(envelope.Anchor)
	case "cancel":
		if value != "" {
			return proposalCommandResult{Status: "error", Message: "usage: /supervise cancel"}, nil
		}
		return a.commandCancel(envelope.Anchor)
	case "start":
		return a.commandStart(envelope, value)
	case "":
		return a.commandStart(envelope, "")
	default:
		// `/supervise <objective>` is the documented shorthand. It enters the
		// same plugin-owned wizard; command origin is not treated as authority.
		return a.commandStart(envelope, args)
	}
}

func (a *application) commandStatus(anchor lifecycleAnchor) (proposalCommandResult, error) {
	if !a.loaded && a.setup == nil {
		if err := a.ensureLoaded(anchor); err != nil && !errors.Is(err, errNoSelectedContract) {
			return proposalCommandResult{}, err
		}
	}
	if a.setup != nil {
		return proposalCommandResult{Status: "ok", Message: a.setup.statusMessage()}, nil
	}
	if !a.loaded {
		return proposalCommandResult{Status: "ok", Message: "no supervise setup or selected contract exists for this application session"}, nil
	}
	if !a.state.dormant() {
		if err := a.reconcileWorkerProjection(); err != nil {
			return proposalCommandResult{}, err
		}
	}
	message := fmt.Sprintf("run %s: %d/%d plan steps complete, %d worker turns observed", a.state.RunID, a.state.CompletedSteps, a.state.PlanTotal, a.state.TurnsSeen)
	if a.state.Completed {
		message += "; independently verified complete"
		if a.state.CompletionHandedOff {
			message += "; successful-completion handoff recorded"
		} else {
			message += "; successful-completion handoff pending"
		}
	}
	if a.state.Cancelled {
		message += "; durably cancelled"
		if a.state.CancelReason != "" {
			message += ": " + a.state.CancelReason
		}
	}
	if a.state.WorkerRunStatus != "" {
		message += "; worker recurrence " + a.state.WorkerRunStatus + " at version " + fmt.Sprint(a.state.WorkerRunVersion)
	}
	if a.state.PendingReview != nil {
		message += "; watchdog review pending at sequence " + fmt.Sprint(a.state.PendingReview.Anchor.SessionSequence)
	}
	if a.state.PendingPivot != nil {
		message += "; pivot " + a.state.PendingPivot.Classification + " is " + a.state.PendingPivot.Stage
	}
	return proposalCommandResult{Status: "ok", Message: message}, nil
}

func (a *application) commandStart(envelope applicationCommandEnvelope, objectiveSeed string) (proposalCommandResult, error) {
	if !a.loaded && a.setup == nil {
		if err := a.ensureLoaded(envelope.Anchor); err != nil && !errors.Is(err, errNoSelectedContract) {
			return proposalCommandResult{}, err
		}
	}
	if a.loaded && !a.state.Cancelled && !a.state.dormant() {
		return proposalCommandResult{Status: "error", Message: "a session-scoped supervision contract is already selected; use status, resume, or cancel"}, nil
	}
	if a.loaded && a.state.Cancelled {
		if err := a.finishRunCancellationCleanup(); err != nil {
			return proposalCommandResult{}, fmt.Errorf("finish cancelled workflow cleanup before restart: %w", err)
		}
		if !a.state.dormant() {
			return proposalCommandResult{}, errors.New("cancelled workflow cleanup is not durably complete")
		}
	}
	if a.loaded && a.state.Completed && !a.state.dormant() {
		return proposalCommandResult{Status: "error", Message: "verified completion cleanup is still in progress"}, nil
	}
	if a.setup != nil && a.setup.Phase != setupPhaseCancelled {
		return proposalCommandResult{Status: "error", Message: a.setup.statusMessage()}, nil
	}
	if a.setup != nil {
		if err := a.finishSetupCancellationCleanup(); err != nil {
			return proposalCommandResult{}, fmt.Errorf("finish cancelled setup cleanup before restart: %w", err)
		}
		if !a.setup.CancellationCleaned {
			return proposalCommandResult{}, errors.New("cancelled setup cleanup is not durably complete")
		}
	}
	request, err := runSetupWizard(objectiveSeed, hostSetupChoice)
	if errors.Is(err, errWizardCancelled) {
		return proposalCommandResult{Status: "error", Message: "setup cancelled before any baseline agent or contract candidate was created"}, nil
	}
	if err != nil {
		return proposalCommandResult{}, fmt.Errorf("run supervise setup UI: %w", err)
	}
	setup, err := newSetupState(request, authenticatedAgentParent{
		SessionID: envelope.Anchor.SessionID, SessionGeneration: envelope.Anchor.SessionGeneration, CanonicalRepoID: envelope.Anchor.CanonicalRepoID,
	})
	if err != nil {
		return proposalCommandResult{}, err
	}
	priorState, priorContract, priorSetup, priorLoaded := a.state, a.contract, a.setup, a.loaded
	a.state, a.contract, a.setup, a.loaded, a.anchor = runState{}, supervisionContract{}, &setup, false, envelope.Anchor
	if err := a.persistSetup("setup.configured", nil); err != nil {
		a.state, a.contract, a.setup, a.loaded = priorState, priorContract, priorSetup, priorLoaded
		return proposalCommandResult{}, fmt.Errorf("persist supervise setup before baseline spawn: %w", err)
	}
	if err := a.spawnBaseline(); err != nil {
		return proposalCommandResult{}, err
	}
	return proposalCommandResult{Status: "ok", Message: a.setup.statusMessage() + "; implementation remains blocked until the proposal is confirmed"}, nil
}

func (a *application) commandResume(anchor lifecycleAnchor) (proposalCommandResult, error) {
	if !a.loaded && a.setup == nil {
		if err := a.ensureLoaded(anchor); err != nil {
			if errors.Is(err, errNoSelectedContract) {
				return proposalCommandResult{Status: "error", Message: "nothing to resume; run /supervise start <objective>"}, nil
			}
			return proposalCommandResult{}, err
		}
	}
	if a.loaded {
		if a.state.PendingPivot != nil {
			return a.resumePivot()
		}
		// Worker recurrence recovery is kept behind the isolated C27 bridge.
		return a.resumeWorkerRun()
	}
	if a.setup == nil {
		return proposalCommandResult{Status: "error", Message: "nothing to resume"}, nil
	}
	switch a.setup.Phase {
	case setupPhaseStarting, setupPhaseFailed:
		if a.setup.Attempt >= maxBaselineAttempts {
			return proposalCommandResult{Status: "error", Message: baselineFailureMessage(*a.setup)}, nil
		}
		if err := a.spawnBaseline(); err != nil {
			return proposalCommandResult{}, err
		}
		return proposalCommandResult{Status: "ok", Message: a.setup.statusMessage()}, nil
	case setupPhaseRunning:
		if a.setup.Terminal != nil {
			if err := a.pollBaseline(); err != nil {
				return proposalCommandResult{}, err
			}
			if a.setup.Phase == setupPhaseReady {
				return a.confirmBaseline()
			}
		} else if err := a.recoverBaselineSpawn(); err != nil {
			return proposalCommandResult{}, err
		}
		return proposalCommandResult{Status: "ok", Message: a.setup.statusMessage()}, nil
	case setupPhaseReady:
		return a.confirmBaseline()
	case setupPhaseCancelled:
		return proposalCommandResult{Status: "error", Message: "setup is cancelled; start a new supervised run explicitly"}, nil
	default:
		return proposalCommandResult{}, errors.New("unknown durable setup phase")
	}
}

func (a *application) commandCancel(anchor lifecycleAnchor) (proposalCommandResult, error) {
	if !a.loaded && a.setup == nil {
		if err := a.ensureLoaded(anchor); err != nil {
			if errors.Is(err, errNoSelectedContract) {
				return proposalCommandResult{Status: "error", Message: "nothing to cancel"}, nil
			}
			return proposalCommandResult{}, err
		}
	}
	if a.loaded {
		return a.cancelWorkerRun()
	}
	if a.setup == nil || a.setup.Phase == setupPhaseCancelled {
		if err := a.finishSetupCancellationCleanup(); err != nil {
			return proposalCommandResult{}, err
		}
		return proposalCommandResult{Status: "ok", Message: "supervise setup is already durably cancelled and cleaned"}, nil
	}
	if a.setup.Phase == setupPhaseStarting {
		// A baseline admission may have committed while its reply or following
		// setup.baseline_started append was lost. Replay the durable logical key
		// and bind the exact child before cancellation; an empty local ID is not
		// evidence that no child exists.
		if err := a.spawnBaseline(); err != nil {
			return proposalCommandResult{}, fmt.Errorf("recover baseline admission before cancellation: %w", err)
		}
	}
	prior := *a.setup
	if err := a.setup.cancel("cancelled through the application command"); err != nil {
		return proposalCommandResult{}, err
	}
	if err := a.persistSetup("setup.cancelled", nil); err != nil {
		*a.setup = prior
		return proposalCommandResult{}, err
	}
	if err := a.finishSetupCancellationCleanup(); err != nil {
		return proposalCommandResult{}, err
	}
	return proposalCommandResult{Status: "ok", Message: "supervise setup cancelled; no contract candidate or worker was activated"}, nil
}

func hostSetupChoice(request uiChoiceRequest) (uiChoiceResponse, error) {
	raw, err := callHostJSON(stadoUIChoose, request)
	if err != nil {
		return uiChoiceResponse{}, err
	}
	var response uiChoiceResponse
	if err := decodeStrict(raw, &response); err != nil {
		return uiChoiceResponse{}, err
	}
	return response, nil
}

func (a *application) spawnBaseline() error {
	if a.setup == nil {
		return errors.New("baseline setup state is unavailable")
	}
	request, err := buildBaselineSpawnRequest(*a.setup, a.setup.Attempt+1)
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
	prior := *a.setup
	if err := a.setup.beginBaseline(spawned.ID); err != nil {
		return err
	}
	if err := a.persistSetup("setup.baseline_started", nil); err != nil {
		_, _ = callHostJSON(stadoAgentCancel, map[string]string{"id": spawned.ID})
		*a.setup = prior
		return fmt.Errorf("persist fresh baseline child: %w", err)
	}
	return nil
}

// recoverBaselineSpawn replays the exact durable logical admission. A module
// rebind in the same process returns the original child. After a process
// restart the old Fleet child no longer exists, so the process-local claim map
// admits one replacement under the same logical key and this transition binds
// its new ID without consuming another baseline attempt.
func (a *application) recoverBaselineSpawn() error {
	if a.setup == nil || a.setup.Phase != setupPhaseRunning || a.setup.Terminal != nil || a.setup.Attempt < 1 {
		return nil
	}
	request, err := buildBaselineSpawnRequest(*a.setup, a.setup.Attempt)
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
	if spawned.ID == a.setup.BaselineAgentID {
		return nil
	}
	prior := *a.setup
	a.setup.BaselineAgentID, a.setup.BaselineOffset = spawned.ID, 0
	a.setup.Terminal, a.setup.Proposal, a.setup.LastError = nil, nil, ""
	if err := a.persistSetup("setup.baseline_recovered", nil); err != nil {
		*a.setup = prior
		return fmt.Errorf("persist recovered baseline child: %w", err)
	}
	return nil
}

func (a *application) pollBaseline() error {
	if a.setup == nil || a.setup.Phase != setupPhaseRunning || a.setup.BaselineAgentID == "" || a.setup.Terminal == nil {
		return nil
	}
	raw, err := callHostJSON(stadoAgentReadMessages, map[string]any{
		"id": a.setup.BaselineAgentID, "since": a.setup.BaselineOffset, "timeout_ms": 0,
	})
	if err != nil {
		return err
	}
	var messages agentMessages
	if err := decodeStrict(raw, &messages); err != nil {
		return err
	}
	prior := *a.setup
	consumed, err := a.setup.consumeBaselineMessages(messages)
	if err != nil || !consumed {
		return err
	}
	kind := "setup.baseline_failed"
	if a.setup.Phase == setupPhaseReady {
		kind = "setup.baseline_ready"
	}
	if err := a.persistSetup(kind, a.setup.Terminal.EvidenceRefs); err != nil {
		*a.setup = prior
		return err
	}
	message := baselineFailureMessage(*a.setup)
	if a.setup.Phase == setupPhaseReady {
		message = "Fresh supervise baseline is ready. Run /supervise resume to review and confirm it before implementation begins."
	}
	_, _ = callHostJSON(stadoUIPrint, map[string]string{"text": message, "severity": "info"})
	return nil
}

func (a *application) handleSetupAgentDown(envelope eventEnvelope) error {
	if a.setup == nil {
		return errors.New("setup state is unavailable")
	}
	facts, err := decodeAgentDownFacts(envelope.Event.Data)
	if err != nil {
		return err
	}
	prior := *a.setup
	matching, err := a.setup.observeAgentDown(authenticatedAgentParent{
		SessionID: envelope.Anchor.SessionID, SessionGeneration: envelope.Anchor.SessionGeneration, CanonicalRepoID: envelope.Anchor.CanonicalRepoID,
	}, envelope.Event.BrokerSeq, envelope.Event.EvidenceRefs, facts)
	if err != nil || !matching {
		return err
	}
	if err := a.persistSetup("setup.baseline_terminal", envelope.Event.EvidenceRefs); err != nil {
		*a.setup = prior
		return err
	}
	return a.pollBaseline()
}

func (a *application) confirmBaseline() (proposalCommandResult, error) {
	if a.setup == nil || a.setup.Phase != setupPhaseReady || a.setup.Proposal == nil {
		return proposalCommandResult{}, errors.New("no fresh baseline proposal is ready for confirmation")
	}
	formatted, err := json.MarshalIndent(a.setup.Proposal.Baseline, "", "  ")
	if err != nil {
		return proposalCommandResult{}, err
	}
	if _, err := callHostJSON(stadoUIRender, map[string]any{
		"title": "Proposed supervised-work baseline", "variant": "recommendation", "id": "supervise-baseline",
		"sections": []map[string]any{{"kind": "code", "heading": "Quality contract candidate", "code": map[string]string{"language": "json", "content": string(formatted)}}},
		"footer":   "No implementation starts until this quality-workflow proposal is confirmed.",
	}); err != nil {
		return proposalCommandResult{}, fmt.Errorf("render baseline proposal: %w", err)
	}
	title := []byte("Confirm supervised-work baseline")
	body := []byte("Confirm this exact objective, constraints, non-goals, criteria, ordered plan, definition of done, verification, and risks as the application's quality contract. This UI choice grants no security authority or plugin capability.")
	titlePointer, titleLength := slicePointer(title)
	bodyPointer, bodyLength := slicePointer(body)
	approved := stadoUIApprove(titlePointer, titleLength, bodyPointer, bodyLength)
	if approved < 0 {
		return proposalCommandResult{}, errors.New("quality confirmation UI is unavailable; baseline remains unselected")
	}
	if approved == 0 {
		prior := *a.setup
		if err := a.setup.cancel("baseline proposal was not confirmed"); err != nil {
			return proposalCommandResult{}, err
		}
		if err := a.persistSetup("setup.baseline_rejected", nil); err != nil {
			*a.setup = prior
			return proposalCommandResult{}, err
		}
		if err := a.finishSetupCancellationCleanup(); err != nil {
			return proposalCommandResult{}, err
		}
		return proposalCommandResult{Status: "error", Message: "baseline rejected; no contract candidate or worker was activated"}, nil
	}
	contract := a.setup.Proposal.Baseline.contract(a.setup.RunID, a.setup.Request.Config)
	response, err := callHostJSON(stadoArtifactPropose, map[string]any{
		"kind": "supervision-contract", "scope": "session", "tags": []string{"quality:supervision"},
		"groups": []string{"stado/supervision"}, "data": contract,
	})
	if err != nil {
		return proposalCommandResult{}, err
	}
	var candidate struct {
		ID        string `json:"id"`
		Version   uint64 `json:"version"`
		Authority string `json:"authority"`
	}
	if err := json.Unmarshal(response, &candidate); err != nil || candidate.ID == "" || candidate.Version == 0 || candidate.Authority != "candidate" {
		return proposalCommandResult{}, errors.New("artifact broker returned an invalid supervision candidate")
	}
	contract.ArtifactID, contract.ArtifactVersion = candidate.ID, candidate.Version
	selected, err := querySelectedContract(candidate.ID, candidate.Version)
	if err != nil {
		return proposalCommandResult{}, fmt.Errorf("revalidate proposed supervision candidate: %w", err)
	}
	matches, err := sameContractData(contract, selected)
	if err != nil || !matches {
		return proposalCommandResult{}, errors.New("proposed supervision candidate does not preserve the exact confirmed contract")
	}
	contract = selected
	state, err := newContractRunState(contract)
	if err != nil {
		return proposalCommandResult{}, err
	}
	state.WorkerConflict = a.setup.Request.WorkerConflict
	priorSetup := a.setup
	a.state, a.contract, a.setup, a.loaded = state, contract, nil, true
	if err := a.persist("run.started", []string{"artifact:" + candidate.ID}); err != nil {
		a.state, a.contract, a.setup, a.loaded = runState{}, supervisionContract{}, priorSetup, false
		return proposalCommandResult{}, fmt.Errorf("persist selected supervision contract: %w", err)
	}
	return a.requestWorkerRun(candidate.ID, candidate.Version)
}

func (a *application) persistSetup(kind string, evidence []string) error {
	if a.setup == nil {
		a.reloadRequired = true
		return errors.New("no supervise setup state to persist")
	}
	if err := a.setup.validate(); err != nil {
		a.reloadRequired = true
		return err
	}
	stateJSON, err := json.Marshal(a.setup)
	if err != nil {
		a.reloadRequired = true
		return err
	}
	if len(stateJSON) > 60<<10 {
		a.reloadRequired = true
		return errors.New("durable supervise setup state exceeds 60 KiB")
	}
	summary := "supervise application transition: " + kind
	raw, err := callHostJSON(stadoSessionJournalAppend, map[string]any{
		"run_id": a.setup.RunID, "kind": kind, "summary": summary,
		"data": json.RawMessage(stateJSON), "evidence_refs": evidence,
	})
	if err == nil {
		err = validateJournalAck(raw, lifecycleIdentity{SessionID: a.anchor.SessionID, SessionGeneration: a.anchor.SessionGeneration}, a.setup.RunID, kind, summary, stateJSON, evidence)
	}
	if err != nil {
		a.reloadRequired = true
	}
	return err
}

func (a *application) requestWorkerRun(candidateID string, candidateVersion uint64) (proposalCommandResult, error) {
	if !a.loaded || a.state.RunID == "" || candidateID != a.state.ContractID || candidateVersion != a.state.ContractVersion {
		return proposalCommandResult{}, errors.New("selected contract and durable worker request are not exactly bound")
	}
	if a.state.Cancelled || a.state.Completed {
		return proposalCommandResult{}, errors.New("terminal supervise workflow cannot request worker recurrence")
	}
	prompt, err := buildWorkerPrompt(a.contract)
	if err != nil {
		return proposalCommandResult{}, err
	}
	raw, err := callHostJSON(stadoSessionWorkerRequest, map[string]any{
		"run_id": a.state.RunID, "objective": a.contract.Objective, "prompt": prompt,
		"conflict": a.setupWorkerConflict(), "idempotency_key": "supervise-worker-request:" + a.state.RunID,
	})
	if err != nil {
		return proposalCommandResult{}, err
	}
	run, err := decodeWorkerRun(raw)
	if err != nil {
		return proposalCommandResult{}, fmt.Errorf("decode worker-run request: %w", err)
	}
	if err := validateWorkerRunBinding(run, a.anchor.SessionID, a.anchor.SessionGeneration, a.contract, a.state.WorkerObjective, prompt, a.setupWorkerConflict()); err != nil {
		return proposalCommandResult{}, err
	}
	if run.Status != workerRunRequested || run.Version != 1 {
		return proposalCommandResult{}, errors.New("new worker recurrence was not returned as requested version 1")
	}
	if _, err := a.state.observeWorkerRun(run); err != nil {
		return proposalCommandResult{}, err
	}
	if err := a.persist("worker.requested", []string{"artifact:" + candidateID}); err != nil {
		return proposalCommandResult{}, fmt.Errorf("persist worker recurrence request: %w", err)
	}
	return proposalCommandResult{
		Status: "ok", WorkerRunID: run.RunID,
		Message: fmt.Sprintf("confirmed supervision candidate %s v%d; requested exact worker recurrence %s", candidateID, candidateVersion, run.RunID),
	}, nil
}

// setupWorkerConflict returns the exact conflict choice copied from the
// confirmed setup into durable run state before the setup record is retired.
func (a *application) setupWorkerConflict() string {
	if a.state.WorkerConflict == "reject" || a.state.WorkerConflict == "replace_operator_loop" {
		return a.state.WorkerConflict
	}
	return "replace_operator_loop"
}

func (a *application) readExactWorkerRun() (applicationWorkerRun, bool, error) {
	raw, err := callHostJSON(stadoSessionProjectionRead, map[string]any{
		"journal_limit": 1, "control_limit": 1, "worker_limit": 16, "include_terminal": true,
	})
	if err != nil {
		return applicationWorkerRun{}, false, err
	}
	return exactWorkerRunFromProjection(raw, a.state.RunID)
}

func (a *application) reconcileWorkerProjection() error {
	run, found, err := a.readExactWorkerRun()
	if err != nil {
		return err
	}
	if !found {
		if a.state.WorkerRunVersion != 0 {
			return errors.New("durable worker recurrence disappeared from its authenticated broker projection")
		}
		return nil
	}
	prompt, err := buildWorkerPrompt(a.contract)
	if err != nil {
		return err
	}
	if err := validateWorkerRunBinding(run, a.anchor.SessionID, a.anchor.SessionGeneration, a.contract, a.state.WorkerObjective, prompt, a.setupWorkerConflict()); err != nil {
		return err
	}
	changed, err := a.state.observeWorkerRun(run)
	if err != nil || !changed {
		return err
	}
	return a.persist("worker.reconciled", nil)
}

func (a *application) resumeWorkerRun() (proposalCommandResult, error) {
	if a.state.Cancelled {
		return proposalCommandResult{Status: "error", Message: "supervise workflow is durably cancelled; start a new run explicitly"}, nil
	}
	if a.state.Completed {
		return proposalCommandResult{Status: "ok", Message: "supervise workflow is already independently verified complete"}, nil
	}
	run, found, err := a.readExactWorkerRun()
	if err != nil {
		return proposalCommandResult{}, err
	}
	if !found {
		if a.state.WorkerRunVersion != 0 {
			return proposalCommandResult{}, errors.New("durable worker recurrence disappeared from its authenticated broker projection")
		}
		return a.requestWorkerRun(a.state.ContractID, a.state.ContractVersion)
	}
	prompt, err := buildWorkerPrompt(a.contract)
	if err != nil {
		return proposalCommandResult{}, err
	}
	if err := validateWorkerRunBinding(run, a.anchor.SessionID, a.anchor.SessionGeneration, a.contract, a.state.WorkerObjective, prompt, a.setupWorkerConflict()); err != nil {
		return proposalCommandResult{}, err
	}
	changed, err := a.state.observeWorkerRun(run)
	if err != nil {
		return proposalCommandResult{}, err
	}
	if changed {
		if err := a.persist("worker.reconciled", nil); err != nil {
			return proposalCommandResult{}, err
		}
	}
	switch run.Status {
	case workerRunRequested:
		return proposalCommandResult{Status: "ok", WorkerRunID: run.RunID, Message: "reconciled exact worker recurrence " + run.RunID + " at " + run.Status}, nil
	case workerRunResumeRequested:
		return proposalCommandResult{Status: "ok", ResumeWorkerRunID: run.RunID, Message: "reconciled exact worker resume request " + run.RunID}, nil
	case workerRunActive:
		return proposalCommandResult{Status: "ok", Message: "worker recurrence is already active: " + run.RunID}, nil
	case workerRunInterrupted:
		raw, resumeErr := callHostJSON(stadoSessionWorkerResume, map[string]any{
			"run_id": run.RunID, "expected_version": run.Version,
			"idempotency_key": fmt.Sprintf("supervise-worker-resume:%s:v%d", run.RunID, run.Version),
		})
		if resumeErr != nil {
			return proposalCommandResult{}, resumeErr
		}
		resumed, decodeErr := decodeWorkerRun(raw)
		if decodeErr != nil || validateWorkerRunBinding(resumed, a.anchor.SessionID, a.anchor.SessionGeneration, a.contract, a.state.WorkerObjective, prompt, a.setupWorkerConflict()) != nil || validateWorkerResumeAck(run, resumed) != nil {
			return proposalCommandResult{}, errors.New("broker returned an invalid exact worker resume request")
		}
		if _, observeErr := a.state.observeWorkerRun(resumed); observeErr != nil {
			return proposalCommandResult{}, observeErr
		}
		if persistErr := a.persist("worker.resume_requested", nil); persistErr != nil {
			return proposalCommandResult{}, fmt.Errorf("persist worker resume request: %w", persistErr)
		}
		return proposalCommandResult{Status: "ok", ResumeWorkerRunID: resumed.RunID, Message: "requested exact worker recurrence resume " + resumed.RunID}, nil
	case workerRunCompleted:
		return proposalCommandResult{Status: "ok", Message: "worker recurrence is already terminal after successful completion"}, nil
	default:
		return proposalCommandResult{Status: "error", Message: "worker recurrence is terminal (" + run.Status + "); cancel this quality workflow and start a new supervised run"}, nil
	}
}

func (a *application) cancelWorkerRun() (proposalCommandResult, error) {
	if a.state.Completed {
		return proposalCommandResult{Status: "error", Message: "independently verified completion is already terminal and cannot be cancelled"}, nil
	}
	if a.state.Cancelled {
		if err := a.finishRunCancellationCleanup(); err != nil {
			return proposalCommandResult{}, err
		}
		return proposalCommandResult{Status: "ok", Message: "supervise workflow is already durably cancelled", CancelWorkerRunID: cancellationHandoffRunID(a.state)}, nil
	}
	run, found, err := a.readExactWorkerRun()
	if err != nil {
		return proposalCommandResult{}, err
	}
	if found {
		prompt, promptErr := buildWorkerPrompt(a.contract)
		if promptErr != nil {
			return proposalCommandResult{}, promptErr
		}
		if err := validateWorkerRunBinding(run, a.anchor.SessionID, a.anchor.SessionGeneration, a.contract, a.state.WorkerObjective, prompt, a.setupWorkerConflict()); err != nil {
			return proposalCommandResult{}, err
		}
		if run.Status == workerRunCompleted {
			return proposalCommandResult{Status: "error", Message: "worker recurrence is already terminal after successful completion"}, nil
		}
		if run.Status == workerRunRequested || run.Status == workerRunResumeRequested || run.Status == workerRunActive {
			raw, cancelErr := callHostJSON(stadoSessionWorkerCancel, map[string]any{
				"run_id": run.RunID, "expected_version": run.Version,
				"reason": "operator cancelled the supervise quality workflow", "idempotency_key": "supervise-worker-cancel:" + run.RunID,
			})
			if cancelErr != nil {
				return proposalCommandResult{}, cancelErr
			}
			cancelled, decodeErr := decodeWorkerRun(raw)
			if decodeErr != nil || validateWorkerRunBinding(cancelled, a.anchor.SessionID, a.anchor.SessionGeneration, a.contract, a.state.WorkerObjective, prompt, a.setupWorkerConflict()) != nil || cancelled.Status != workerRunCancelled || cancelled.Version != run.Version+1 {
				return proposalCommandResult{}, errors.New("broker returned an invalid exact worker cancellation")
			}
			run = cancelled
		}
		if _, err := a.state.observeWorkerRun(run); err != nil {
			return proposalCommandResult{}, err
		}
	}
	if a.state.PendingReview != nil && a.state.PendingReview.AgentID == "" {
		// Replaying the durable spawn intent recovers the exact live child after
		// a lost reply. Cancellation must not erase that intent and then claim
		// cleanup while an indistinguishable reviewer is still running.
		if err := a.ensurePendingReviewSpawn(); err != nil {
			return proposalCommandResult{}, fmt.Errorf("recover pending reviewer before cancellation: %w", err)
		}
	}
	if err := a.state.cancelWorkflow("cancelled through the supervise application command"); err != nil {
		return proposalCommandResult{}, err
	}
	if err := a.persist("run.cancelled", nil); err != nil {
		return proposalCommandResult{}, err
	}
	if err := a.finishRunCancellationCleanup(); err != nil {
		return proposalCommandResult{}, err
	}
	return proposalCommandResult{Status: "ok", Message: "supervise workflow and its worker recurrence are durably cancelled", CancelWorkerRunID: cancellationHandoffRunID(a.state)}, nil
}
