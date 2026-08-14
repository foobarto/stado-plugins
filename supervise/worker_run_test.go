package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func testWorkerContract(t *testing.T) supervisionContract {
	t.Helper()
	contract := testBaselineProposal().Baseline.contract("supervise-run-1", defaultConfig())
	contract.ArtifactID, contract.ArtifactVersion = "artifact-1", 2
	if err := contract.validate(); err != nil {
		t.Fatal(err)
	}
	return contract
}

func testWorkerRun(t *testing.T, contract supervisionContract, status string, version uint64) applicationWorkerRun {
	t.Helper()
	prompt, err := buildWorkerPrompt(contract)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	run := applicationWorkerRun{
		SessionID: "session-1", Generation: 4, PluginID: "github.com/foobarto/stado-plugins/supervise", Owner: "github.com/foobarto/stado-plugins/supervise",
		RunID: contract.RunID, Version: version, WALSequence: version + 10, Objective: contract.Objective, Prompt: prompt,
		Conflict: "replace_operator_loop", Status: status, CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Add(time.Duration(version) * time.Second).Format(time.RFC3339Nano),
	}
	if status == workerRunResumeRequested || status == workerRunCompleted || status == workerRunInterrupted || status == workerRunStopped {
		run.TerminalReason, run.TerminalSequence = "terminal transition", run.WALSequence
	}
	if status == workerRunCancelled {
		run.TerminalReason = "terminal transition"
	}
	return run
}

func TestWorkerRunCancellationUsesOwnWALAnchorWithoutControlSequence(t *testing.T) {
	contract := testWorkerContract(t)
	cancelled := testWorkerRun(t, contract, workerRunCancelled, 3)
	if err := cancelled.validate(); err != nil {
		t.Fatal(err)
	}
	cancelled.TerminalSequence = cancelled.WALSequence
	if err := cancelled.validate(); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("cancellation accepted a fabricated separate terminal sequence: %v", err)
	}
}

func TestWorkerResumeRequestPreservesExactInterruptedIdentity(t *testing.T) {
	contract := testWorkerContract(t)
	interrupted := testWorkerRun(t, contract, workerRunInterrupted, 3)
	resumed := testWorkerRun(t, contract, workerRunResumeRequested, 4)
	resumed.CreatedAt = interrupted.CreatedAt
	resumed.TerminalReason = interrupted.TerminalReason
	resumed.TerminalSequence = interrupted.TerminalSequence
	if err := validateWorkerResumeAck(interrupted, resumed); err != nil {
		t.Fatal(err)
	}
	changed := resumed
	changed.Prompt += " changed"
	if err := validateWorkerResumeAck(interrupted, changed); err == nil {
		t.Fatal("resume request changed the durable worker payload")
	}
	changed = resumed
	changed.TerminalSequence++
	if err := validateWorkerResumeAck(interrupted, changed); err == nil {
		t.Fatal("resume request changed interruption provenance")
	}
}

func TestWorkerRunResponseIsStrictAndExactlyBound(t *testing.T) {
	contract := testWorkerContract(t)
	run := testWorkerRun(t, contract, workerRunRequested, 1)
	raw, _ := json.Marshal(run)
	decoded, err := decodeWorkerRun(raw)
	if err != nil {
		t.Fatal(err)
	}
	prompt, _ := buildWorkerPrompt(contract)
	if err := validateWorkerRunBinding(decoded, "session-1", 4, contract, contract.Objective, prompt, "replace_operator_loop"); err != nil {
		t.Fatal(err)
	}
	for name, invalid := range map[string][]byte{
		"unknown field":  []byte(strings.Replace(string(raw), `"status":"requested"`, `"status":"requested","authority":"guest"`, 1)),
		"wrong session":  []byte(strings.Replace(string(raw), `"session_id":"session-1"`, `"session_id":"session-2"`, 1)),
		"terminal facts": []byte(strings.Replace(string(raw), `"status":"requested"`, `"status":"requested","terminal_reason":"bad","terminal_sequence":12`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			candidate, decodeErr := decodeWorkerRun(invalid)
			if decodeErr == nil {
				decodeErr = validateWorkerRunBinding(candidate, "session-1", 4, contract, contract.Objective, prompt, "replace_operator_loop")
			}
			if decodeErr == nil {
				t.Fatal("malformed or mismatched worker-run fact was accepted")
			}
		})
	}
}

func TestExactWorkerRunSurvivesManyProjectionItemsAndTruncationFailsClosed(t *testing.T) {
	contract := testWorkerContract(t)
	runs := make([]applicationWorkerRun, 0, 61)
	for index := 0; index < 60; index++ {
		otherContract := contract
		otherContract.RunID = fmt.Sprintf("other-%02d", index)
		runs = append(runs, testWorkerRun(t, otherContract, workerRunCompleted, 3))
	}
	runs = append(runs, testWorkerRun(t, contract, workerRunActive, 2))
	raw, _ := json.Marshal(map[string]any{"worker_runs": runs, "worker_runs_truncated": true, "future_projection_field": true})
	selected, found, err := exactWorkerRunFromProjection(raw, contract.RunID)
	if err != nil || !found || selected.Status != workerRunActive || selected.Version != 2 {
		t.Fatalf("exact worker projection: found=%v selected=%+v err=%v", found, selected, err)
	}
	missingRaw, _ := json.Marshal(map[string]any{"worker_runs": runs[:len(runs)-1], "worker_runs_truncated": true})
	if _, _, err := exactWorkerRunFromProjection(missingRaw, contract.RunID); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncated projection did not fail closed: %v", err)
	}
}

func TestWorkerRunStateIsMonotonicAndCancellationClearsPolicyWork(t *testing.T) {
	contract := testWorkerContract(t)
	state, err := newContractRunState(contract)
	if err != nil {
		t.Fatal(err)
	}
	requested := testWorkerRun(t, contract, workerRunRequested, 1)
	if changed, err := state.observeWorkerRun(requested); err != nil || !changed {
		t.Fatalf("record requested: changed=%v err=%v", changed, err)
	}
	active := testWorkerRun(t, contract, workerRunActive, 2)
	if changed, err := state.observeWorkerRun(active); err != nil || !changed || state.WorkerRunStatus != workerRunActive {
		t.Fatalf("record active: state=%+v changed=%v err=%v", state, changed, err)
	}
	if _, err := state.observeWorkerRun(requested); err == nil {
		t.Fatal("regressed worker CAS version was accepted")
	}
	state.PendingReview = &reviewRequest{ID: "review-1"}
	state.QueuedSignals = []signal{{Type: "periodic_turn"}}
	if err := state.cancelWorkflow("operator cancelled"); err != nil {
		t.Fatal(err)
	}
	if !state.Cancelled || state.PendingReview != nil || len(state.QueuedSignals) != 0 {
		t.Fatalf("workflow cancellation left policy work live: %+v", state)
	}
}

func TestWorkerPromptIsBoundedAndKeepsProgressSemanticsSeparate(t *testing.T) {
	prompt, err := buildWorkerPrompt(testWorkerContract(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"exact selected contract", "criterion evidence distinct from ordered plan progression", "never self-approval"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("worker prompt omitted %q: %s", required, prompt)
		}
	}
	if len(prompt) > maxWorkerPromptBytes {
		t.Fatalf("worker prompt exceeds host bound: %d", len(prompt))
	}
}

func TestWorkerPromptIdentitySurvivesQualityConfirmedContractChange(t *testing.T) {
	before := testWorkerContract(t)
	after := before
	after.Objective = "quality-confirmed replacement objective"
	first, err := buildWorkerPrompt(before)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildWorkerPrompt(after)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("worker recurrence prompt changed with versioned contract: before=%q after=%q", first, second)
	}
}
