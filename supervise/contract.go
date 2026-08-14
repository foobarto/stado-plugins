package main

import (
	"errors"
	"strings"
)

type supervisionContract struct {
	ArtifactID      string         `json:"-"`
	ArtifactVersion uint64         `json:"-"`
	RunID           string         `json:"run_id"`
	Objective       string         `json:"objective"`
	Constraints     []string       `json:"constraints,omitempty"`
	NonGoals        []string       `json:"non_goals,omitempty"`
	Acceptance      []string       `json:"acceptance_criteria"`
	Plan            []baselineStep `json:"plan"`
	DefinitionDone  []string       `json:"definition_of_done"`
	Verification    []string       `json:"verification"`
	Risks           []string       `json:"risks,omitempty"`
	Config          config         `json:"config"`
}

func (c supervisionContract) validate() error {
	if strings.TrimSpace(c.RunID) == "" || strings.TrimSpace(c.Objective) == "" || len(c.Acceptance) == 0 || len(c.Plan) == 0 || len(c.DefinitionDone) == 0 || len(c.Verification) == 0 {
		return errors.New("supervision contract requires run id, objective, acceptance criteria, ordered plan, definition of done, and verification")
	}
	if len(c.Objective) > 4<<10 || len(c.Constraints) > 64 || len(c.NonGoals) > 64 || len(c.Acceptance) > 64 || len(c.Plan) > 64 || len(c.DefinitionDone) > 64 || len(c.Verification) > 64 || len(c.Risks) > 64 {
		return errors.New("supervision contract exceeds bounds")
	}
	for _, values := range [][]string{c.Constraints, c.NonGoals, c.Acceptance, c.DefinitionDone, c.Verification, c.Risks} {
		for _, value := range values {
			if strings.TrimSpace(value) == "" || len(value) > 4096 {
				return errors.New("supervision contract contains an invalid bounded entry")
			}
		}
	}
	seen := map[string]bool{}
	for _, step := range c.Plan {
		if !boundedRequired(step.ID, 64) || !boundedRequired(step.Title, 1024) || !boundedRequired(step.DoneWhen, 4096) || seen[step.ID] {
			return errors.New("supervision contract contains an invalid or duplicate ordered plan step")
		}
		seen[step.ID] = true
	}
	return c.Config.validate()
}

func newContractRunState(contract supervisionContract) (runState, error) {
	state, err := newRunState(contract.RunID, contract.Config)
	if err != nil {
		return runState{}, err
	}
	if contract.ArtifactID == "" || contract.ArtifactVersion == 0 {
		return runState{}, errors.New("supervision contract candidate reference is unavailable")
	}
	state.ContractID = contract.ArtifactID
	state.ContractVersion = contract.ArtifactVersion
	state.CriteriaTotal = len(contract.Acceptance)
	state.Criteria = make([]criterionProgressState, len(contract.Acceptance))
	for index := range state.Criteria {
		state.Criteria[index].CriterionIndex = index
	}
	state.PlanSteps = make([]string, len(contract.Plan))
	for index := range contract.Plan {
		state.PlanSteps[index] = contract.Plan[index].ID
	}
	state.PlanTotal = len(state.PlanSteps)
	state.CurrentAnchor.ActiveStep = state.PlanSteps[0]
	state.WorkerConflict = "replace_operator_loop"
	state.WorkerObjective = contract.Objective
	return state, nil
}
