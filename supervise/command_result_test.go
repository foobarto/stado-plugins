package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProposalCommandResultReportsSelectedCandidateWithoutAuthority(t *testing.T) {
	result, err := newProposalCommandResult("artifact-supervision-7", 3)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"status":"ok","message":"selected session-scoped supervision contract candidate artifact-supervision-7 v3"}`
	if string(raw) != want {
		t.Fatalf("command result\n got: %s\nwant: %s", raw, want)
	}
	if strings.Contains(string(raw), "grant") || strings.Contains(string(raw), "authority") || strings.Contains(string(raw), "activation") {
		t.Fatalf("proposal result conveyed authority-shaped data: %s", raw)
	}
	var decoded proposalCommandResult
	if err := decodeStrictBytes(raw, &decoded); err != nil {
		t.Fatalf("strict result decoder rejected output: %v", err)
	}
	if decoded.Status != "ok" || !strings.Contains(decoded.Message, "artifact-supervision-7 v3") {
		t.Fatalf("candidate reference lost: %+v", decoded)
	}
}

func TestProposalCommandResultRejectsInvalidReference(t *testing.T) {
	for _, test := range []struct {
		id      string
		version uint64
	}{
		{"", 1}, {"artifact", 0}, {strings.Repeat("x", 513), 1},
	} {
		if _, err := newProposalCommandResult(test.id, test.version); err == nil {
			t.Fatalf("accepted invalid candidate reference id=%q version=%d", test.id, test.version)
		}
	}
}

func TestWorkerCommandResultNamesRunWithoutGrantShapedData(t *testing.T) {
	result := proposalCommandResult{Status: "ok", Message: "worker requested", WorkerRunID: "supervise-run-1"}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"status":"ok","message":"worker requested","worker_run_id":"supervise-run-1"}`
	if string(raw) != want {
		t.Fatalf("worker command result\n got: %s\nwant: %s", raw, want)
	}
	for _, forbidden := range []string{"activation", "authority", "grant", "expected_version"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("worker result conveyed host authority %q: %s", forbidden, raw)
		}
	}
}

func TestWorkerResumeCommandResultUsesDedicatedNativeHandoff(t *testing.T) {
	result := proposalCommandResult{Status: "ok", Message: "resume requested", ResumeWorkerRunID: "supervise-run-1"}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"status":"ok","message":"resume requested","resume_worker_run_id":"supervise-run-1"}`
	if string(raw) != want {
		t.Fatalf("worker resume command result\n got: %s\nwant: %s", raw, want)
	}
	if strings.Contains(string(raw), "worker_run_id\":") && !strings.Contains(string(raw), "resume_worker_run_id\":") {
		t.Fatalf("resume result used initial activation field: %s", raw)
	}
}
