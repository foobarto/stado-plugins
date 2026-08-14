package main

import (
	"encoding/json"
	"testing"
)

func projectionWithJournal(t *testing.T, entries ...map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"journal": entries})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDurableSetupRecoveryWinsOverPriorCancelledRun(t *testing.T) {
	contract := testWorkerContract(t)
	run, err := newContractRunState(contract)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.cancelWorkflow("start over"); err != nil {
		t.Fatal(err)
	}
	setup := testSetupState(t)
	runRaw, _ := json.Marshal(run)
	setupRaw, _ := json.Marshal(setup)
	durable, ok, err := latestDurableSuperviseState(projectionWithJournal(t,
		map[string]any{"kind": "run.cancelled", "data": json.RawMessage(runRaw)},
		map[string]any{"kind": "setup.configured", "data": json.RawMessage(setupRaw)},
	))
	if err != nil || !ok || durable.Setup == nil || durable.Run != nil || durable.Setup.RunID != setup.RunID {
		t.Fatalf("latest setup recovery: durable=%+v ok=%v err=%v", durable, ok, err)
	}
}

func TestDurableCancelledRunRetainsWorkerCASState(t *testing.T) {
	contract := testWorkerContract(t)
	run, err := newContractRunState(contract)
	if err != nil {
		t.Fatal(err)
	}
	worker := testWorkerRun(t, contract, workerRunCancelled, 3)
	if _, err := run.observeWorkerRun(worker); err != nil {
		t.Fatal(err)
	}
	if err := run.cancelWorkflow("operator cancelled"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(run)
	durable, ok, err := latestDurableSuperviseState(projectionWithJournal(t,
		map[string]any{"kind": "run.cancelled", "data": json.RawMessage(raw)},
	))
	if err != nil || !ok || durable.Run == nil || !durable.Run.Cancelled || durable.Run.WorkerRunVersion != 3 || durable.Run.WorkerRunStatus != workerRunCancelled {
		t.Fatalf("cancelled run recovery: durable=%+v ok=%v err=%v", durable, ok, err)
	}
}

func TestDurableRecoveryIncludesVerificationTerminalAndEffectJournals(t *testing.T) {
	state, _ := requestedHostVerification(t)
	state.PendingHostVerification = nil
	state.LastHostVerification = &hostVerificationResultState{
		ID: "verification-1", Source: state.CurrentAnchor,
		SuiteDigest: verificationDigest("suite"), Outcome: "no_suite", Usable: true,
		EvidenceRefs: []string{"broker:wal:lifecycle_application:90"},
	}
	state.LastHostVerificationEventSequence = 90
	state.LastHostVerificationEventDigest = verificationDigest("terminal-event")
	state.HostVerificationEffectPending = true
	raw, _ := json.Marshal(state)
	for _, kind := range []string{"verification.finished", "verification.effects_applied"} {
		durable, ok, err := latestDurableSuperviseState(projectionWithJournal(t, map[string]any{"kind": kind, "data": json.RawMessage(raw)}))
		if err != nil || !ok || durable.Run == nil || durable.Run.LastHostVerification == nil || durable.Run.LastHostVerification.ID != "verification-1" {
			t.Fatalf("%s was skipped during durable refold: durable=%+v ok=%v err=%v", kind, durable, ok, err)
		}
	}
}
