package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestScenarioCommandValidatesCatalogEntry(t *testing.T) {
	var stdout, stderr bytes.Buffer
	path := filepath.Join("..", "..", "..", "evals", "scenarios", "retry-thrash.json")
	if err := run([]string{"scenario", path}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"id":"retry-thrash"`) || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestScoreCommandPreservesPairedCriteriaContract(t *testing.T) {
	rows := `{"scenario_id":"x","arm":"unsupervised","provider":"p","model":"m","run_id":"u","trial":"seed","criteria_total":2,"criteria_satisfied":1,"tokens":{"worker":100,"watchdog":0,"verifier":0}}
{"scenario_id":"x","arm":"supervised","provider":"p","model":"m","run_id":"s","trial":"seed","criteria_total":2,"criteria_satisfied":2,"tokens":{"worker":100,"watchdog":50,"verifier":25}}`
	var stdout, stderr bytes.Buffer
	if err := run([]string{"score"}, strings.NewReader(rows), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"scenario_id": "x"`, `"criteria_rate": 0.5`, `"total_tokens": 75`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("score output %q missing %q", stdout.String(), want)
		}
	}
}

func TestScoreCommandRejectsMismatchedCriteriaTotals(t *testing.T) {
	rows := `{"scenario_id":"x","arm":"unsupervised","provider":"p","model":"m","run_id":"u","trial":"seed","criteria_total":2,"tokens":{"worker":1,"watchdog":0,"verifier":0}}
{"scenario_id":"x","arm":"supervised","provider":"p","model":"m","run_id":"s","trial":"seed","criteria_total":3,"tokens":{"worker":1,"watchdog":0,"verifier":0}}`
	err := run([]string{"score"}, strings.NewReader(rows), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "different criteria totals") {
		t.Fatalf("mismatched pair error=%v", err)
	}
}
