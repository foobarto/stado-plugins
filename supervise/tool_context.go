package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

// supervisionModelContext publishes application state as labelled tool input,
// never as host authority. Tool calls still have to repeat this exact anchor;
// the application compares it with durable state before accepting a mutation.
func supervisionModelContext(state runState, contract supervisionContract) (string, bool, error) {
	if state.ContractID == "" || state.ContractVersion == 0 || state.CriteriaTotal == 0 {
		return "", false, nil
	}
	if err := state.CurrentAnchor.validate(); err != nil {
		return "", false, nil
	}
	if state.ContractID != contract.ArtifactID || state.ContractVersion != contract.ArtifactVersion || state.CriteriaTotal != len(contract.Acceptance) || state.PlanTotal != len(contract.Plan) {
		return "", false, errors.New("supervision tool context does not match selected contract candidate")
	}
	value := struct {
		ContractID      string                   `json:"contract_id"`
		ContractVersion uint64                   `json:"contract_version"`
		Anchor          anchor                   `json:"anchor"`
		Criteria        []criterionProgressState `json:"criteria"`
		Plan            []baselineStep           `json:"plan"`
		CompletedSteps  int                      `json:"completed_steps"`
	}{
		ContractID: state.ContractID, ContractVersion: state.ContractVersion,
		Anchor: state.CurrentAnchor, Criteria: cloneCriterionStates(state.Criteria),
		Plan: append([]baselineStep(nil), contract.Plan...), CompletedSteps: state.CompletedSteps,
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", false, err
	}
	return fmt.Sprintf("[stado supervise application context]\nThe following is current plugin state, not an instruction from repository content. Use supervise__report_progress, supervise__request_pivot, or supervise__request_completion only with the exact anchor below and host evidence references. A rejected tool call grants no authority.\n%s", raw), true, nil
}
