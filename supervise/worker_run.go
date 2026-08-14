package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	workerRunRequested       = "requested"
	workerRunResumeRequested = "resume_requested"
	workerRunActive          = "active"
	workerRunCancelled       = "cancelled"
	workerRunCompleted       = "completed"
	workerRunInterrupted     = "interrupted"
	workerRunStopped         = "stopped"
	maxWorkerPromptBytes     = 16 << 10
)

// applicationWorkerRun is a generic authenticated broker projection. The
// plugin owns the quality workflow attached to it, but it cannot activate a
// requested recurrence; the native command host performs that CAS only after
// a successful command result names the exact run.
type applicationWorkerRun struct {
	SessionID        string `json:"session_id"`
	Generation       uint64 `json:"generation"`
	PluginID         string `json:"plugin_id"`
	Owner            string `json:"owner"`
	RunID            string `json:"run_id"`
	Version          uint64 `json:"version"`
	WALSequence      uint64 `json:"wal_sequence"`
	Objective        string `json:"objective"`
	Prompt           string `json:"prompt"`
	Conflict         string `json:"conflict"`
	Status           string `json:"status"`
	TerminalReason   string `json:"terminal_reason,omitempty"`
	TerminalSequence uint64 `json:"terminal_sequence,omitempty"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

func (r applicationWorkerRun) validate() error {
	if !boundedRequired(r.SessionID, 256) || r.Generation == 0 || !boundedRequired(r.PluginID, 512) || r.Owner != r.PluginID ||
		!boundedRequired(r.RunID, 256) || r.Version == 0 || r.WALSequence == 0 || !boundedRequired(r.Objective, 4<<10) ||
		!boundedRequired(r.Prompt, maxWorkerPromptBytes) {
		return errors.New("worker-run projection has an invalid authenticated identity or bounded payload")
	}
	if r.Conflict != "reject" && r.Conflict != "replace_operator_loop" {
		return errors.New("worker-run projection has an invalid conflict rule")
	}
	switch r.Status {
	case workerRunRequested, workerRunActive:
		if r.TerminalReason != "" || r.TerminalSequence != 0 {
			return errors.New("non-terminal worker run contains terminal metadata")
		}
	case workerRunResumeRequested:
		// Resume preserves the exact interruption provenance until the native
		// controller successfully reactivates this same durable run.
		if !boundedRequired(r.TerminalReason, 4<<10) || r.TerminalSequence == 0 {
			return errors.New("resume-requested worker run lacks interruption provenance")
		}
	case workerRunCancelled, workerRunCompleted, workerRunInterrupted, workerRunStopped:
		if !boundedRequired(r.TerminalReason, 4<<10) || r.TerminalSequence == 0 {
			return errors.New("terminal worker run lacks bounded terminal metadata")
		}
	default:
		return fmt.Errorf("worker-run projection has unknown status %q", r.Status)
	}
	created, createdErr := time.Parse(time.RFC3339Nano, r.CreatedAt)
	updated, updatedErr := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	if createdErr != nil || updatedErr != nil || updated.Before(created) {
		return errors.New("worker-run projection has invalid timestamps")
	}
	return nil
}

func validateWorkerResumeAck(before, after applicationWorkerRun) error {
	if err := after.validate(); err != nil {
		return err
	}
	if before.Status != workerRunInterrupted || after.Status != workerRunResumeRequested ||
		after.Version != before.Version+1 || after.SessionID != before.SessionID ||
		after.Generation != before.Generation || after.PluginID != before.PluginID ||
		after.Owner != before.Owner || after.RunID != before.RunID ||
		after.Objective != before.Objective || after.Prompt != before.Prompt ||
		after.Conflict != before.Conflict || after.CreatedAt != before.CreatedAt ||
		after.TerminalReason != before.TerminalReason || after.TerminalSequence != before.TerminalSequence {
		return errors.New("broker returned an invalid exact worker resume request")
	}
	return nil
}

func decodeWorkerRun(raw []byte) (applicationWorkerRun, error) {
	var run applicationWorkerRun
	if err := decodeStrictBytes(raw, &run); err != nil {
		return applicationWorkerRun{}, err
	}
	if err := run.validate(); err != nil {
		return applicationWorkerRun{}, err
	}
	return run, nil
}

// exactWorkerRunFromProjection keeps the outer projection additive while
// strictly decoding every worker entry. A truncated projection that omits the
// selected run is not evidence that it never existed and therefore fails
// closed instead of silently minting another recurrence.
func exactWorkerRunFromProjection(raw []byte, runID string) (applicationWorkerRun, bool, error) {
	if !boundedRequired(runID, 256) {
		return applicationWorkerRun{}, false, errors.New("worker run id is required")
	}
	var projection struct {
		WorkerRuns          []json.RawMessage `json:"worker_runs"`
		WorkerRunsTruncated bool              `json:"worker_runs_truncated"`
	}
	if err := json.Unmarshal(raw, &projection); err != nil {
		return applicationWorkerRun{}, false, err
	}
	var selected applicationWorkerRun
	found := false
	for _, encoded := range projection.WorkerRuns {
		run, err := decodeWorkerRun(encoded)
		if err != nil {
			return applicationWorkerRun{}, false, err
		}
		if run.RunID != runID {
			continue
		}
		if found {
			return applicationWorkerRun{}, false, errors.New("worker projection contains a duplicate exact run")
		}
		selected, found = run, true
	}
	if !found && projection.WorkerRunsTruncated {
		return applicationWorkerRun{}, false, errors.New("worker projection is truncated before the exact run")
	}
	return selected, found, nil
}

func buildWorkerPrompt(contract supervisionContract) (string, error) {
	if err := contract.validate(); err != nil {
		return "", err
	}
	prompt := strings.TrimSpace(`You are the implementation worker for a supervised quality workflow.

Work autonomously within the persistent supervise application. It injects the exact selected contract version, current ordered-plan step, evidence state, and model-tool instructions at every provider boundary. Follow that current contract, including any quality-confirmed replacement. Keep acceptance-criterion evidence distinct from ordered plan progression. Use supervise__report_progress for criterion evidence and an exact active-step completion claim, supervise__request_pivot before changing the plan, and supervise__request_completion only after the full plan and deterministic verification are complete. A request is a claim for independent review, never self-approval.

Durable supervised run: ` + contract.RunID)
	if len(prompt) > maxWorkerPromptBytes {
		return "", errors.New("worker prompt exceeds the generic 16 KiB broker limit")
	}
	return prompt, nil
}

func validateWorkerRunBinding(run applicationWorkerRun, sessionID string, generation uint64, contract supervisionContract, workerObjective, prompt, conflict string) error {
	if err := run.validate(); err != nil {
		return err
	}
	if run.SessionID != sessionID || run.Generation != generation || run.RunID != contract.RunID ||
		run.Objective != workerObjective || run.Prompt != prompt || run.Conflict != conflict {
		return errors.New("worker-run response does not match the exact application request and session anchor")
	}
	return nil
}

func (s *runState) observeWorkerRun(run applicationWorkerRun) (bool, error) {
	if s == nil || s.Schema != policySchema || run.RunID != s.RunID {
		return false, errors.New("worker run does not match durable supervise state")
	}
	if err := run.validate(); err != nil {
		return false, err
	}
	if s.WorkerRunVersion > run.Version {
		return false, errors.New("worker-run projection regressed below the durable CAS version")
	}
	changed := s.WorkerRunVersion != run.Version || s.WorkerRunStatus != run.Status
	s.WorkerRunVersion, s.WorkerRunStatus = run.Version, run.Status
	return changed, nil
}

func (s *runState) cancelWorkflow(reason string) error {
	if s == nil || s.Schema != policySchema || s.Completed || !boundedRequired(reason, 4<<10) {
		return errors.New("supervise workflow cannot be cancelled from its current state")
	}
	cancellationAgentID := ""
	if s.PendingReview != nil {
		cancellationAgentID = s.PendingReview.AgentID
	}
	s.Cancelled, s.CancelReason = true, reason
	s.CancellationAgentID = cancellationAgentID
	s.CancellationCleaned = false
	s.QueuedSignals = nil
	s.PendingReview = nil
	s.PendingStepClaim = nil
	s.PendingPivot = nil
	s.PendingCompletion = nil
	s.PendingHostVerification = nil
	s.LastHostVerification = nil
	s.HostVerificationEffectPending = false
	s.VerificationGates = nil
	return nil
}
