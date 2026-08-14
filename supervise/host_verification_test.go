package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func verificationDigest(seed string) string { return "sha256:" + digestString(seed) }

func verificationWALEvidence(sequence uint64) []string {
	return []string{"broker:wal:lifecycle_application:" + fmt.Sprint(sequence)}
}

func stagedHostVerificationState(t *testing.T) (runState, turnCommittedFacts) {
	t.Helper()
	state, _ := activeToolState(t)
	state.CompletedSteps = state.PlanTotal
	state.CurrentAnchor.ActiveStep = "completion"
	state.WorkerRunStatus = workerRunActive
	state.WorkerRunVersion = 4
	facts := sampleTurnFacts(state.CurrentAnchor.SessionSequence + 1)
	facts.Anchor.TreeDigest = "0123456789abcdef0123456789abcdef01234567"
	facts.Anchor.TurnRef = "git:refs/sessions/worker-1/tree@0123456789abcdef0123456789abcdef01234567#turn-8-iteration-1"
	change, err := state.stageHostVerification(70, facts)
	if err != nil || len(change.Actions) != 1 || change.Actions[0].Kind != actionAcquireHold {
		t.Fatalf("stage host verification: change=%+v err=%v", change, err)
	}
	raw, _ := json.Marshal(facts)
	if _, _, replay, err := state.applyTurnCommittedFacts(70, facts, verificationDigest(string(raw))); err != nil || replay {
		t.Fatalf("fold verification source turn: replay=%v err=%v", replay, err)
	}
	return state, facts
}

func requestedHostVerification(t *testing.T) (runState, hostVerificationRecord) {
	t.Helper()
	state, _ := stagedHostVerificationState(t)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	pending := state.PendingHostVerification
	record := hostVerificationRecord{
		ID: "verification-1", SessionID: "parent-session", Generation: 7,
		PluginID: "github.com/foobarto/stado-plugins/supervise", Owner: "github.com/foobarto/stado-plugins/supervise",
		RunID: state.RunID, WorkerVersion: pending.ExpectedWorkerVersion, Version: 1, WALSequence: 71, Status: "requested",
		Source: hostVerificationSource{
			EventSequence: pending.SourceEventSequence, SessionSequence: pending.Anchor.SessionSequence,
			TurnRef: pending.Anchor.TurnRef, TreeDigest: pending.Anchor.TreeDigest,
		},
		SourceEvidenceRefs: []string{"turn:70"}, CreatedAt: now, UpdatedAt: now,
	}
	if err := state.acceptHostVerificationRequest(record); err != nil {
		t.Fatal(err)
	}
	state.Hold.ID, state.Hold.Version = "hold-verification", 1
	return state, record
}

func terminalHostVerification(requested hostVerificationRecord, outcome string, commands []hostVerificationCommandFact) hostVerificationTerminal {
	record := hostVerificationTerminal{
		Schema: "stado.dev/session-verification-facts/v1", VerificationID: requested.ID,
		RunID: requested.RunID, Version: requested.Version + 2, Source: requested.Source,
		SourceEvidenceRefs: append([]string(nil), requested.SourceEvidenceRefs...),
		SuiteDigest:        verificationDigest("operator-suite"), Outcome: outcome,
	}
	for _, command := range commands {
		command.EvidenceRefs = append(command.EvidenceRefs, verificationWALEvidence(90)...)
		record.Commands = append(record.Commands, command)
		record.CommandDigests = append(record.CommandDigests, command.CommandDigest)
	}
	record.EvidenceRefs = append([]string{"verification:audit"}, verificationWALEvidence(90)...)
	if outcome == "command_failed" || outcome == "infrastructure_error" || outcome == "cancelled" {
		record.FailureKind = outcome
		record.FailureFingerprint = verificationDigest(outcome)
	}
	return record
}

func testVerificationParent() authenticatedAgentParent {
	return authenticatedAgentParent{SessionID: "parent-session", SessionGeneration: 7, CanonicalRepoID: "repo-1"}
}

func TestCrossRepoSessionVerificationFactsFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/session-verification-facts-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var facts hostVerificationTerminal
	if err := decodeStrictBytes(raw, &facts); err != nil {
		t.Fatalf("host fixture rejected by plugin decoder: %v", err)
	}
	if err := facts.validate(); err != nil {
		t.Fatalf("host fixture rejected by plugin factual validator: %v", err)
	}
	if facts.Schema != "stado.dev/session-verification-facts/v1" || facts.Outcome != "command_failed" || facts.FailureKind != "command_failed" || len(facts.Commands) != 2 || facts.Commands[0].Outcome != "succeeded" || facts.Commands[1].Outcome != "failed" {
		t.Fatalf("host fixture reduced incorrectly: %+v", facts)
	}
	state, _ := activeToolState(t)
	state.RunID = facts.RunID
	state.CurrentAnchor = anchor{
		SessionSequence: facts.Source.SessionSequence, PlanVersion: 1, ActiveStep: "completion",
		TreeDigest: facts.Source.TreeDigest, TurnRef: facts.Source.TurnRef,
	}
	state.PendingHostVerification = &hostVerificationRequestState{
		Stage: hostVerificationStageRequested, ID: facts.VerificationID, Version: 1,
		ExpectedWorkerVersion: 4, SourceEventSequence: facts.Source.EventSequence,
		Anchor: state.CurrentAnchor, SourceEvidenceRefs: append([]string(nil), facts.SourceEvidenceRefs...),
	}
	state.Hold = &holdState{ID: "fixture-hold", Version: 1, Reason: "fixture verification"}
	change, replay, err := state.applyHostVerificationTerminal(facts, testVerificationParent(), 45, hostVerificationEventDigest(raw, facts.EvidenceRefs), facts.EvidenceRefs)
	if err != nil || replay || len(change.Actions) != 1 || change.Actions[0].Kind != actionReleaseHold || state.LastHostVerification == nil || state.LastHostVerification.Usable || len(state.AdvisorySteering) != 1 {
		t.Fatalf("host fixture policy fold: change=%+v replay=%v state=%+v err=%v", change, replay, state, err)
	}
}

func TestHostVerificationRequestReplyLossRebindsExactIntent(t *testing.T) {
	state, _ := stagedHostVerificationState(t)
	beforeReply, _ := json.Marshal(state)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	pending := state.PendingHostVerification
	response := hostVerificationRecord{
		ID: "verification-exact", SessionID: "parent-session", Generation: 7, PluginID: "plugin", Owner: "plugin",
		RunID: state.RunID, WorkerVersion: pending.ExpectedWorkerVersion, Version: 1, WALSequence: 71, Status: "requested",
		Source:    hostVerificationSource{EventSequence: pending.SourceEventSequence, SessionSequence: pending.Anchor.SessionSequence, TurnRef: pending.Anchor.TurnRef, TreeDigest: pending.Anchor.TreeDigest},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := state.acceptHostVerificationRequest(response); err != nil {
		t.Fatal(err)
	}
	first, _ := json.Marshal(state)
	var rebound runState
	if err := decodeStrictBytes(beforeReply, &rebound); err != nil {
		t.Fatal(err)
	}
	if err := rebound.acceptHostVerificationRequest(response); err != nil {
		t.Fatal(err)
	}
	replayed, _ := json.Marshal(rebound)
	if string(first) != string(replayed) {
		t.Fatalf("request reply replay diverged\nfirst=%s\nreplay=%s", first, replayed)
	}
	bad := response
	bad.WorkerVersion++
	var prior runState
	_ = decodeStrictBytes(beforeReply, &prior)
	if err := prior.acceptHostVerificationRequest(bad); err == nil {
		t.Fatal("verification response for another worker version was accepted")
	}
}

func TestMultiCommandHostVerificationBecomesExactCompletionGate(t *testing.T) {
	state, requested := requestedHostVerification(t)
	commands := []hostVerificationCommandFact{
		{Ordinal: 1, CommandDigest: verificationDigest("command-1"), ResultDigest: verificationDigest("result-1"), Outcome: "succeeded", EvidenceRefs: []string{"audit:command-1"}},
		{Ordinal: 2, CommandDigest: verificationDigest("command-2"), ResultDigest: verificationDigest("result-2"), Outcome: "succeeded", EvidenceRefs: []string{"audit:command-2"}},
	}
	record := terminalHostVerification(requested, "commands_succeeded", commands)
	brokerSequence := uint64(90)
	digest := verificationDigest("terminal-event")
	change, replay, err := state.applyHostVerificationTerminal(record, testVerificationParent(), brokerSequence, digest, record.EvidenceRefs)
	if err != nil || replay || len(change.Actions) != 1 || change.Actions[0].Kind != actionReleaseHold || state.PendingHostVerification != nil || state.LastHostVerification == nil || !state.LastHostVerification.Usable || len(state.VerificationGates) != 2 || !state.HostVerificationEffectPending {
		t.Fatalf("terminal multi-command result: change=%+v replay=%v state=%+v err=%v", change, replay, state, err)
	}
	if _, err := state.hostVerificationGateEvidence(); err == nil {
		t.Fatal("terminal facts became usable before their hold/reaction effects finished")
	}

	// Callback timeout after verification.finished but before hold release must
	// replay the exact pending effect without applying the terminal facts twice.
	change, replay, err = state.applyHostVerificationTerminal(record, testVerificationParent(), brokerSequence, digest, record.EvidenceRefs)
	if err != nil || !replay || len(change.Actions) != 1 || change.Actions[0].Kind != actionReleaseHold {
		t.Fatalf("terminal event replay: change=%+v replay=%v err=%v", change, replay, err)
	}
	state.Hold = nil // exact idempotent CAS release committed
	if err := state.markHostVerificationTerminalEffectsApplied(); err != nil {
		t.Fatal(err)
	}
	if evidence, err := state.hostVerificationGateEvidence(); err != nil || len(evidence) < 3 {
		t.Fatalf("completion gate evidence=%v err=%v", evidence, err)
	}
	change, replay, err = state.applyHostVerificationTerminal(record, testVerificationParent(), brokerSequence, digest, record.EvidenceRefs)
	if err != nil || !replay || len(change.Actions) != 0 {
		t.Fatalf("post-cleanup terminal replay: change=%+v replay=%v err=%v", change, replay, err)
	}
	if _, _, err := state.applyHostVerificationTerminal(record, testVerificationParent(), brokerSequence, verificationDigest("different"), record.EvidenceRefs); err == nil {
		t.Fatal("terminal event sequence replayed with different facts")
	}
}

func TestRepeatedOperatorCommandsRemainOrderedFacts(t *testing.T) {
	state, requested := requestedHostVerification(t)
	digest := verificationDigest("same-command")
	record := terminalHostVerification(requested, "commands_succeeded", []hostVerificationCommandFact{
		{Ordinal: 1, CommandDigest: digest, ResultDigest: verificationDigest("first-run"), Outcome: "succeeded"},
		{Ordinal: 2, CommandDigest: digest, ResultDigest: verificationDigest("second-run"), Outcome: "succeeded"},
	})
	if _, _, err := state.applyHostVerificationTerminal(record, testVerificationParent(), 90, verificationDigest("repeated"), record.EvidenceRefs); err != nil {
		t.Fatalf("host-valid repeated commands were rejected: %v", err)
	}
	if len(state.LastHostVerification.CommandDigests) != 2 {
		t.Fatalf("ordered repeated facts collapsed in durable result: %+v", state.LastHostVerification)
	}
	state.Hold = nil
	if err := state.markHostVerificationTerminalEffectsApplied(); err != nil {
		t.Fatal(err)
	}
	if _, err := state.hostVerificationGateEvidence(); err != nil {
		t.Fatalf("repeated operator commands prevented semantic completion review: %v", err)
	}
}

func TestNoSuiteIsFactualGateWhileFailuresNeverApproveCompletion(t *testing.T) {
	state, requested := requestedHostVerification(t)
	noSuite := terminalHostVerification(requested, "no_suite", nil)
	if change, _, err := state.applyHostVerificationTerminal(noSuite, testVerificationParent(), 90, verificationDigest("no-suite"), noSuite.EvidenceRefs); err != nil || len(change.Actions) != 1 || state.LastHostVerification == nil || !state.LastHostVerification.Usable {
		t.Fatalf("no-suite policy: change=%+v state=%+v err=%v", change, state, err)
	}
	state.Hold = nil
	if err := state.markHostVerificationTerminalEffectsApplied(); err != nil {
		t.Fatal(err)
	}
	if _, err := state.hostVerificationGateEvidence(); err != nil {
		t.Fatalf("no-suite did not defer semantic completion to independent verifier: %v", err)
	}

	failed, request := requestedHostVerification(t)
	command := hostVerificationCommandFact{Ordinal: 1, CommandDigest: verificationDigest("command"), ResultDigest: verificationDigest("failed-result"), Outcome: "failed", FailureKind: "command_failed", FailureFingerprint: verificationDigest("exit-1"), EvidenceRefs: []string{"audit:failure"}}
	failure := terminalHostVerification(request, "command_failed", []hostVerificationCommandFact{command})
	change, _, err := failed.applyHostVerificationTerminal(failure, testVerificationParent(), 90, verificationDigest("failed"), failure.EvidenceRefs)
	if err != nil || len(change.Actions) != 1 || change.Actions[0].Kind != actionReleaseHold || failed.LastHostVerification == nil || failed.LastHostVerification.Usable || len(failed.AdvisorySteering) != 1 {
		t.Fatalf("command failure policy: change=%+v state=%+v err=%v", change, failed, err)
	}
	if _, err := failed.hostVerificationGateEvidence(); err == nil {
		t.Fatal("command failure became a completion gate")
	}

	infrastructure, request := requestedHostVerification(t)
	infra := terminalHostVerification(request, "infrastructure_error", nil)
	infra.FailureKind = "suite_changed"
	change, _, err = infrastructure.applyHostVerificationTerminal(infra, testVerificationParent(), 90, verificationDigest("infra"), infra.EvidenceRefs)
	if err != nil || len(change.Actions) != 2 || change.Actions[0].Kind != actionReleaseHold || change.Actions[1].Kind != actionPause || !change.Actions[1].OmitHold || infrastructure.LastHostVerification.Usable {
		t.Fatalf("infrastructure policy: change=%+v state=%+v err=%v", change, infrastructure, err)
	}

	cancelled, request := requestedHostVerification(t)
	cancelledFact := hostVerificationCommandFact{Ordinal: 1, CommandDigest: verificationDigest("cancelled-command"), ResultDigest: verificationDigest("not-run"), Outcome: "not_run"}
	cancelledRecord := terminalHostVerification(request, "cancelled", []hostVerificationCommandFact{cancelledFact})
	cancelledRecord.FailureKind = "worker_terminal"
	change, _, err = cancelled.applyHostVerificationTerminal(cancelledRecord, testVerificationParent(), 90, verificationDigest("cancelled"), cancelledRecord.EvidenceRefs)
	if err != nil || len(change.Actions) != 2 || change.Actions[1].Kind != actionPause {
		t.Fatalf("worker-terminal cancellation facts were rejected: change=%+v state=%+v err=%v", change, cancelled, err)
	}
}

func TestStaleObsoleteAndPlaintextVerificationResultsFailClosed(t *testing.T) {
	state, requested := requestedHostVerification(t)
	state.CurrentAnchor.SessionSequence++
	state.CurrentAnchor.TurnRef = "git:refs/sessions/worker-1/tree@0123456789abcdef0123456789abcdef01234567#turn-9-iteration-1"
	record := terminalHostVerification(requested, "commands_succeeded", []hostVerificationCommandFact{{Ordinal: 1, CommandDigest: verificationDigest("command"), ResultDigest: verificationDigest("result"), Outcome: "succeeded"}})
	change, _, err := state.applyHostVerificationTerminal(record, testVerificationParent(), 90, verificationDigest("stale"), record.EvidenceRefs)
	if err != nil || len(change.Actions) != 1 || change.Actions[0].Kind != actionReleaseHold || state.LastHostVerification == nil || state.LastHostVerification.Usable {
		t.Fatalf("stale verification policy: change=%+v state=%+v err=%v", change, state, err)
	}
	if _, err := state.hostVerificationGateEvidence(); err == nil {
		t.Fatal("stale verification became a completion gate")
	}

	raw, _ := json.Marshal(record)
	withCommand := strings.TrimSuffix(string(raw), "}") + `,"command_text":"go test ./..."}`
	var decoded hostVerificationTerminal
	if err := decodeStrictBytes([]byte(withCommand), &decoded); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("plaintext command escaped the safe terminal schema: %v", err)
	}

	obsolete, obsoleteRequest := requestedHostVerification(t)
	obsolete.PendingHostVerification.Obsolete = true
	obsoleteInfra := terminalHostVerification(obsoleteRequest, "infrastructure_error", nil)
	obsoleteInfra.FailureKind = "suite_changed"
	change, _, err = obsolete.applyHostVerificationTerminal(obsoleteInfra, testVerificationParent(), 90, verificationDigest("obsolete"), obsoleteInfra.EvidenceRefs)
	if err != nil || len(change.Actions) != 1 || change.Actions[0].Kind != actionReleaseHold || obsolete.LastHostVerification == nil || !obsolete.LastHostVerification.Discarded {
		t.Fatalf("obsolete same-anchor result was not discarded without pause: change=%+v state=%+v err=%v", change, obsolete, err)
	}
}

func TestHostVerificationUsesGitTreeDigestsNotFactDigests(t *testing.T) {
	state, requested := requestedHostVerification(t)
	record := terminalHostVerification(requested, "no_suite", nil)
	record.Source.TreeDigest = verificationDigest("not-a-git-tree")
	if _, _, err := state.applyHostVerificationTerminal(record, testVerificationParent(), 90, verificationDigest("bad-tree"), record.EvidenceRefs); err == nil {
		t.Fatal("sha256 fact digest was accepted as a git tree digest")
	}
	if !validGitTreeDigest("empty") || !validGitTreeDigest("0123456789abcdef0123456789abcdef01234567") || validGitTreeDigest(verificationDigest("tree")) {
		t.Fatal("git tree digest validator does not match the host ABI")
	}
}

func TestHostVerificationDurableReloadRejectsContradictoryAuthority(t *testing.T) {
	state, requested := requestedHostVerification(t)
	if err := state.validateHostVerificationDurableState(); err != nil {
		t.Fatalf("valid requested state did not reload: %v", err)
	}
	record := terminalHostVerification(requested, "commands_succeeded", []hostVerificationCommandFact{{Ordinal: 1, CommandDigest: verificationDigest("command"), ResultDigest: verificationDigest("result"), Outcome: "succeeded"}})
	if _, _, err := state.applyHostVerificationTerminal(record, testVerificationParent(), 90, verificationDigest("terminal"), record.EvidenceRefs); err != nil {
		t.Fatal(err)
	}
	if err := state.validateHostVerificationDurableState(); err != nil {
		t.Fatalf("valid terminal effect did not reload: %v", err)
	}
	corrupt := state
	result := *state.LastHostVerification
	corrupt.LastHostVerification = &result
	corrupt.LastHostVerification.Discarded = true
	if err := corrupt.validateHostVerificationDurableState(); err == nil {
		t.Fatal("discarded terminal facts retained completion usability after reload")
	}
	corrupt = state
	corrupt.LastHostVerificationEventDigest = verificationDigest("different")
	corrupt.LastHostVerificationEventSequence = 0
	if err := corrupt.validateHostVerificationDurableState(); err == nil {
		t.Fatal("partial callback replay identity survived reload")
	}
}

func TestHostVerificationReplayDigestBindsOuterEvidence(t *testing.T) {
	data := []byte(`{"schema":"stado.dev/session-verification-facts/v1"}`)
	first := hostVerificationEventDigest(data, []string{"broker:wal:lifecycle_application:90"})
	changed := hostVerificationEventDigest(data, []string{"broker:wal:lifecycle_application:91"})
	if first == changed || !validHostFactDigest(first) || !validHostFactDigest(changed) {
		t.Fatal("terminal replay identity did not bind the authenticated outer evidence")
	}
}

func TestHostVerificationRequiresExactOuterAndResultEvidence(t *testing.T) {
	state, requested := requestedHostVerification(t)
	record := terminalHostVerification(requested, "no_suite", nil)
	outer := verificationWALEvidence(90) // omits the result's audited reference
	if _, _, err := state.applyHostVerificationTerminal(record, testVerificationParent(), 90, verificationDigest("evidence-mismatch"), outer); err == nil {
		t.Fatal("terminal result was accepted with divergent authenticated outer evidence")
	}
}

func TestHostVerificationRejectsContradictoryShortCircuitFacts(t *testing.T) {
	_, requested := requestedHostVerification(t)
	success := hostVerificationCommandFact{Ordinal: 1, CommandDigest: verificationDigest("command-1"), ResultDigest: verificationDigest("result-1"), Outcome: "succeeded"}
	failure := hostVerificationCommandFact{Ordinal: 2, CommandDigest: verificationDigest("command-2"), ResultDigest: verificationDigest("result-2"), Outcome: "failed", FailureKind: "command_failed", FailureFingerprint: verificationDigest("exit-1")}
	notRun := hostVerificationCommandFact{Ordinal: 3, CommandDigest: verificationDigest("command-3"), ResultDigest: verificationDigest("empty"), Outcome: "not_run"}
	valid := terminalHostVerification(requested, "command_failed", []hostVerificationCommandFact{success, failure, notRun})
	valid.FailureKind, valid.FailureFingerprint = "command_failed", verificationDigest("failed")
	if err := valid.validate(); err != nil {
		t.Fatalf("valid short-circuit sequence was rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*hostVerificationTerminal)
	}{
		{"success after failure", func(record *hostVerificationTerminal) {
			record.Commands[1], record.Commands[0] = record.Commands[0], record.Commands[1]
			record.Commands[0].Ordinal, record.Commands[1].Ordinal = 1, 2
			record.CommandDigests[0], record.CommandDigests[1] = record.Commands[0].CommandDigest, record.Commands[1].CommandDigest
		}},
		{"not-run before failure", func(record *hostVerificationTerminal) {
			record.Commands[1], record.Commands[2] = record.Commands[2], record.Commands[1]
			record.Commands[1].Ordinal, record.Commands[2].Ordinal = 2, 3
			record.CommandDigests[1], record.CommandDigests[2] = record.Commands[1].CommandDigest, record.Commands[2].CommandDigest
		}},
		{"success has failure metadata", func(record *hostVerificationTerminal) {
			record.Commands[0].FailureKind, record.Commands[0].FailureFingerprint = "impossible", verificationDigest("impossible")
		}},
		{"not-run has failure metadata", func(record *hostVerificationTerminal) {
			record.Commands[2].FailureKind, record.Commands[2].FailureFingerprint = "impossible", verificationDigest("impossible")
		}},
		{"failed command has unknown failure kind", func(record *hostVerificationTerminal) {
			record.Commands[1].FailureKind = "exit_status"
		}},
		{"command-failed result has unknown failure kind", func(record *hostVerificationTerminal) {
			record.FailureKind = "verification_command_failed"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := valid
			record.Commands = append([]hostVerificationCommandFact(nil), valid.Commands...)
			record.CommandDigests = append([]string(nil), valid.CommandDigests...)
			test.mutate(&record)
			if err := record.validate(); err == nil {
				t.Fatal("contradictory terminal command order was accepted")
			}
		})
	}
}

func TestHostVerificationRejectsMalformedInfrastructureAndCancellationKinds(t *testing.T) {
	_, requested := requestedHostVerification(t)

	infrastructureFact := hostVerificationCommandFact{Ordinal: 1, CommandDigest: verificationDigest("infra-command"), ResultDigest: verificationDigest("infra-result"), Outcome: "infrastructure_error", FailureKind: "infrastructure_error", FailureFingerprint: verificationDigest("infra-fingerprint")}
	infrastructure := terminalHostVerification(requested, "infrastructure_error", []hostVerificationCommandFact{infrastructureFact})
	if err := infrastructure.validate(); err != nil {
		t.Fatalf("valid infrastructure failure was rejected: %v", err)
	}

	cancelledFact := hostVerificationCommandFact{Ordinal: 1, CommandDigest: verificationDigest("cancel-command"), ResultDigest: verificationDigest("cancel-result"), Outcome: "cancelled", FailureKind: "cancelled", FailureFingerprint: verificationDigest("cancel-fingerprint")}
	cancelled := terminalHostVerification(requested, "cancelled", []hostVerificationCommandFact{cancelledFact})
	if err := cancelled.validate(); err != nil {
		t.Fatalf("valid cancellation was rejected: %v", err)
	}

	for _, test := range []struct {
		name   string
		valid  hostVerificationTerminal
		mutate func(*hostVerificationTerminal)
	}{
		{"infrastructure command kind", infrastructure, func(record *hostVerificationTerminal) { record.Commands[0].FailureKind = "command_failed" }},
		{"infrastructure result kind", infrastructure, func(record *hostVerificationTerminal) { record.FailureKind = "verification_infrastructure_error" }},
		{"cancelled command kind", cancelled, func(record *hostVerificationTerminal) { record.Commands[0].FailureKind = "worker_terminal" }},
		{"cancelled result kind", cancelled, func(record *hostVerificationTerminal) { record.FailureKind = "verification_cancelled" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := test.valid
			record.Commands = append([]hostVerificationCommandFact(nil), test.valid.Commands...)
			test.mutate(&record)
			if err := record.validate(); err == nil {
				t.Fatal("terminal facts with a producer-invalid failure kind were accepted")
			}
		})
	}
}
