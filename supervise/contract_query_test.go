package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestSelectedContractQueryUsesExactImmutableRef(t *testing.T) {
	request, err := selectedContractQueryRequest("selected", 17)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"kinds":     []string{"self#supervision-contract"},
		"refs":      []map[string]any{{"id": "selected", "version": uint64(17)}},
		"max_items": 1,
	}
	if !reflect.DeepEqual(request, want) {
		t.Fatalf("candidate query = %#v, want %#v", request, want)
	}
	for _, test := range []struct {
		id      string
		version uint64
	}{{"", 1}, {"selected", 0}} {
		if _, err := selectedContractQueryRequest(test.id, test.version); err == nil {
			t.Fatalf("invalid exact ref accepted: %#v", test)
		}
	}
}

func candidateQueryFixture(t *testing.T, authority string, version uint64) []byte {
	t.Helper()
	contract := supervisionContract{
		RunID: "run-7", Objective: "finish", Acceptance: []string{"implementation"},
		Plan:           []baselineStep{{ID: "build", Title: "Build", DoneWhen: "implemented"}},
		DefinitionDone: []string{"done"}, Verification: []string{"go test ./..."}, Config: defaultConfig(),
	}
	data, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"items": []map[string]any{
		{"id": "other", "version": 1, "authority": "candidate", "data": json.RawMessage(data)},
		{"id": "selected", "version": version, "authority": authority, "data": json.RawMessage(data)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSelectExactContractCandidateRequiresJournaledIDVersion(t *testing.T) {
	contract, err := selectExactContractCandidate(candidateQueryFixture(t, "candidate", 3), "selected", 3)
	if err != nil {
		t.Fatal(err)
	}
	if contract.ArtifactID != "selected" || contract.ArtifactVersion != 3 || contract.RunID != "run-7" {
		t.Fatalf("wrong candidate selected: %+v", contract)
	}
	for _, test := range []struct {
		name      string
		authority string
		version   uint64
		wantID    string
		wantVer   uint64
	}{
		{"promoted artifact", "active", 3, "selected", 3},
		{"wrong id", "candidate", 3, "missing", 3},
		{"wrong version", "candidate", 4, "selected", 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := selectExactContractCandidate(candidateQueryFixture(t, test.authority, test.version), test.wantID, test.wantVer)
			if err == nil || !strings.Contains(err.Error(), "exact selected") {
				t.Fatalf("mismatched candidate accepted: %v", err)
			}
		})
	}
}
