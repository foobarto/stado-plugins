package main

import "testing"

func TestContractCandidateInitializesExactJournalState(t *testing.T) {
	contract := supervisionContract{
		ArtifactID: "artifact-7", ArtifactVersion: 2,
		RunID: "run-7", Objective: "finish the bounded work",
		Acceptance:     []string{"implementation", "verification"},
		Plan:           []baselineStep{{ID: "build", Title: "Build", DoneWhen: "implementation is complete"}, {ID: "test", Title: "Test", DoneWhen: "verification passes"}},
		DefinitionDone: []string{"all criteria have evidence"}, Verification: []string{"go test ./..."}, Config: defaultConfig(),
	}
	state, err := newContractRunState(contract)
	if err != nil {
		t.Fatal(err)
	}
	if state.ContractID != contract.ArtifactID || state.ContractVersion != contract.ArtifactVersion || state.CriteriaTotal != 2 || len(state.Criteria) != 2 || state.PlanTotal != 2 || state.CurrentAnchor.ActiveStep != "build" {
		t.Fatalf("candidate selection was not captured exactly: %+v", state)
	}
	for index, criterion := range state.Criteria {
		if criterion.CriterionIndex != index {
			t.Fatalf("criterion %d initialized as %+v", index, criterion)
		}
	}
}

func TestContractStateRejectsUnversionedCandidate(t *testing.T) {
	contract := supervisionContract{
		RunID: "run-7", Objective: "finish", Acceptance: []string{"implementation"},
		Plan:           []baselineStep{{ID: "build", Title: "Build", DoneWhen: "implemented"}},
		DefinitionDone: []string{"done"}, Verification: []string{"go test ./..."}, Config: defaultConfig(),
	}
	if _, err := newContractRunState(contract); err == nil {
		t.Fatal("unversioned candidate initialized durable state")
	}
}
