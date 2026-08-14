package main

import (
	"encoding/json"
	"testing"
)

func setupJournalEntry(t *testing.T, kind string, state any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"kind": kind, "data": json.RawMessage(raw)}
}

func TestSetupCancelOrRejectCrashRefoldsCommittedOutcomeOnly(t *testing.T) {
	ready := testSetupState(t)
	proposal := testBaselineProposal()
	ready.Phase, ready.Proposal = setupPhaseReady, &proposal
	cancelled := ready
	if err := cancelled.cancel("baseline rejected"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		entries []map[string]any
		phase   setupPhase
	}{
		{"append did not commit", []map[string]any{setupJournalEntry(t, "setup.baseline_ready", ready)}, setupPhaseReady},
		{"append committed before lost reply", []map[string]any{setupJournalEntry(t, "setup.baseline_ready", ready), setupJournalEntry(t, "setup.baseline_rejected", cancelled)}, setupPhaseCancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
			durable, ok, err := latestDurableSuperviseState(projectionWithJournal(t, test.entries...))
			if err != nil || !ok || durable.Setup == nil || durable.Setup.Phase != test.phase {
				t.Fatalf("refold phase=%v durable=%+v ok=%v err=%v", test.phase, durable, ok, err)
			}
		})
	}
}

func TestSetupCancellationBindsLostSpawnReplyBeforeDormancy(t *testing.T) {
	setup := testSetupState(t)
	if setup.Phase != setupPhaseStarting || setup.Attempt != 0 || setup.BaselineAgentID != "" {
		t.Fatalf("unexpected initial spawn intent: %+v", setup)
	}
	// The generic admission committed but the guest lost its reply. Replaying
	// the same logical attempt binds that exact child before cancellation.
	if err := setup.beginBaseline("baseline-child-from-replay"); err != nil {
		t.Fatal(err)
	}
	if err := setup.cancel("operator cancelled"); err != nil {
		t.Fatal(err)
	}
	if setup.CancellationAgentID != "baseline-child-from-replay" || setup.CancellationCleaned {
		t.Fatalf("lost-reply child was not retained as an exact cleanup obligation: %+v", setup)
	}
	raw, _ := json.Marshal(setup)
	durable, ok, err := latestDurableSuperviseState(projectionWithJournal(t, map[string]any{"kind": "setup.cancelled", "data": json.RawMessage(raw)}))
	if err != nil || !ok || durable.Setup == nil || durable.Setup.CancellationAgentID != "baseline-child-from-replay" || durable.Setup.CancellationCleaned {
		t.Fatalf("setup cancellation fence did not survive reload: durable=%+v ok=%v err=%v", durable, ok, err)
	}
}

func TestBaselineTerminalAndReadyCrashRemainReplayable(t *testing.T) {
	setup := testSetupState(t)
	if err := setup.beginBaseline("baseline-child"); err != nil {
		t.Fatal(err)
	}
	running := setup
	complete := true
	facts := agentDownFacts{
		Schema:   agentDownFactsSchema,
		Child:    &agentDownChild{AgentID: "baseline-child", SessionID: "baseline-child", Status: "completed", Role: "explorer", Mode: "read_only", Execution: "wait"},
		Budget:   &agentDownBudget{TokenLimit: uint64(setup.Request.Config.WatchdogTokenBudget), TurnLimit: 4, TimeoutSeconds: uint64(setup.Request.Config.WatchdogTimeoutSecond)},
		Terminal: &agentDownTerminalMetadata{Usage: &agentTokenUsage{}, UsageComplete: &complete},
		Scope:    &agentDownScope{Ownership: currentBaselineAgentOwnership(setup)},
	}
	matched, err := setup.observeAgentDown(setup.Parent, 41, nil, facts)
	if err != nil || !matched {
		t.Fatalf("observe terminal: matched=%v err=%v", matched, err)
	}
	terminal := setup
	proposal := testBaselineProposal()
	proposalRaw, _ := json.Marshal(proposal)
	consumed, err := setup.consumeBaselineMessages(agentMessages{
		Messages: []agentMessage{{Role: "assistant", Content: string(proposalRaw)}}, Offset: 1, Status: "completed",
		Terminal: &agentTerminalMetadata{UsageComplete: true},
	})
	if err != nil || !consumed || setup.Phase != setupPhaseReady {
		t.Fatalf("consume proposal: setup=%+v consumed=%v err=%v", setup, consumed, err)
	}
	ready := setup
	for _, test := range []struct {
		name    string
		entries []map[string]any
		phase   setupPhase
	}{
		{"terminal append absent", []map[string]any{setupJournalEntry(t, "setup.baseline_started", running)}, setupPhaseRunning},
		{"terminal durable ready append absent", []map[string]any{setupJournalEntry(t, "setup.baseline_started", running), setupJournalEntry(t, "setup.baseline_terminal", terminal)}, setupPhaseRunning},
		{"ready append committed before lost reply", []map[string]any{setupJournalEntry(t, "setup.baseline_terminal", terminal), setupJournalEntry(t, "setup.baseline_ready", ready)}, setupPhaseReady},
	} {
		t.Run(test.name, func(t *testing.T) {
			durable, ok, loadErr := latestDurableSuperviseState(projectionWithJournal(t, test.entries...))
			if loadErr != nil || !ok || durable.Setup == nil || durable.Setup.Phase != test.phase {
				t.Fatalf("refold phase=%v durable=%+v ok=%v err=%v", test.phase, durable, ok, loadErr)
			}
			if test.phase == setupPhaseRunning && durable.Setup.Terminal != nil {
				replayed, consumeErr := durable.Setup.consumeBaselineMessages(agentMessages{
					Messages: []agentMessage{{Role: "assistant", Content: string(proposalRaw)}}, Offset: 1, Status: "completed",
					Terminal: &agentTerminalMetadata{UsageComplete: true},
				})
				if consumeErr != nil || !replayed || durable.Setup.Phase != setupPhaseReady {
					t.Fatalf("durable terminal could not replay proposal: setup=%+v replayed=%v err=%v", durable.Setup, replayed, consumeErr)
				}
			}
		})
	}
}

func TestWorkerRequestJournalLostReplyRecoversBothCommitOutcomes(t *testing.T) {
	contract := testWorkerContract(t)
	started, err := newContractRunState(contract)
	if err != nil {
		t.Fatal(err)
	}
	requested := started
	worker := testWorkerRun(t, contract, workerRunRequested, 1)
	if changed, err := requested.observeWorkerRun(worker); err != nil || !changed {
		t.Fatalf("prepare request: changed=%v err=%v", changed, err)
	}
	for _, test := range []struct {
		name    string
		entries []map[string]any
		changed bool
	}{
		{"journal append absent", []map[string]any{setupJournalEntry(t, "run.started", started)}, true},
		{"journal append committed before lost reply", []map[string]any{setupJournalEntry(t, "run.started", started), setupJournalEntry(t, "worker.requested", requested)}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			durable, ok, loadErr := latestDurableSuperviseState(projectionWithJournal(t, test.entries...))
			if loadErr != nil || !ok || durable.Run == nil {
				t.Fatalf("reload worker state: durable=%+v ok=%v err=%v", durable, ok, loadErr)
			}
			changed, observeErr := durable.Run.observeWorkerRun(worker)
			if observeErr != nil || changed != test.changed || durable.Run.WorkerRunStatus != workerRunRequested {
				t.Fatalf("reconcile changed=%v want=%v state=%+v err=%v", changed, test.changed, durable.Run, observeErr)
			}
		})
	}
}

func TestArtifactProposalRunStartLostReplyRefoldsToRetryOrRun(t *testing.T) {
	ready := testSetupState(t)
	proposal := testBaselineProposal()
	ready.Phase, ready.Proposal = setupPhaseReady, &proposal
	contract := proposal.Baseline.contract(ready.RunID, ready.Request.Config)
	contract.ArtifactID, contract.ArtifactVersion = "artifact-1", 1
	run, err := newContractRunState(contract)
	if err != nil {
		t.Fatal(err)
	}
	run.WorkerConflict = ready.Request.WorkerConflict
	for _, test := range []struct {
		name    string
		entries []map[string]any
		setup   bool
	}{
		{"run append absent", []map[string]any{setupJournalEntry(t, "setup.baseline_ready", ready)}, true},
		{"run append committed before lost reply", []map[string]any{setupJournalEntry(t, "setup.baseline_ready", ready), setupJournalEntry(t, "run.started", run)}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			durable, ok, loadErr := latestDurableSuperviseState(projectionWithJournal(t, test.entries...))
			if loadErr != nil || !ok || (durable.Setup != nil) != test.setup || (durable.Run != nil) == test.setup {
				t.Fatalf("artifact/run refold setup=%v durable=%+v ok=%v err=%v", test.setup, durable, ok, loadErr)
			}
		})
	}
}
