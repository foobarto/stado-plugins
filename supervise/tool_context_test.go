package main

import (
	"strings"
	"testing"
)

func TestSupervisionModelContextPublishesExactAnchorWithoutAuthority(t *testing.T) {
	state, contract := activeToolState(t)
	state.Criteria[0].EvidenceRefs = []string{"trace:evidence"}
	context, ready, err := supervisionModelContext(state, contract)
	if err != nil || !ready {
		t.Fatalf("context ready=%v err=%v", ready, err)
	}
	for _, want := range []string{`"session_sequence":7`, `"active_step":"plan-implement"`, `"tree_digest":"tree-current"`, `"contract_id":"contract-1"`, "supervise__report_progress"} {
		if !strings.Contains(context, want) {
			t.Fatalf("context omits %q: %s", want, context)
		}
	}
	if strings.Contains(context, "grant_id") || strings.Contains(context, "authority\"") {
		t.Fatalf("tool context conveyed authority-shaped data: %s", context)
	}
}

func TestSupervisionModelContextUnavailableBeforeAuthenticatedTurn(t *testing.T) {
	state, contract := activeToolState(t)
	state.CurrentAnchor = anchor{PlanVersion: 1, ActiveStep: "plan-implement"}
	if context, ready, err := supervisionModelContext(state, contract); err != nil || ready || context != "" {
		t.Fatalf("pre-anchor context=%q ready=%v err=%v", context, ready, err)
	}
}
