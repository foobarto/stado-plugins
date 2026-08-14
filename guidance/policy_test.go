package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestGuidancePolicyOwnsThresholdsWordingAndSuppression(t *testing.T) {
	facts := qualityFacts{}
	facts.CurrentInput.Text = "what did we decide in the previous session?"
	snapshot := sessionContext{
		Signals:        []contextSignal{{Type: "repeated_tool_failure", DetectedSequence: 5}},
		Reviews:        []contextReview{{Status: "completed", AsOf: 4}},
		Children:       []contextChild{{ID: "child", Status: "running"}},
		UnreadMessages: 1,
	}
	available := map[string]bool{"session__research": true, "agent__list": true, "agent__read_messages": true, "agent__send_message": true}
	got := buildGuidance(facts, snapshot, available)
	for _, want := range []string{"unreviewed mechanical", "Retained coordination", "`session__research`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("guidance missing %q: %s", want, got)
		}
	}
	if len(got) > maxGuidanceBytes {
		t.Fatalf("guidance exceeded bound: %d", len(got))
	}
	snapshot.Reviews[0].AsOf = 5
	if reviewed := buildGuidance(facts, snapshot, available); strings.Contains(reviewed, "unreviewed mechanical") {
		t.Fatalf("completed review boundary did not suppress signal: %s", reviewed)
	}
}

func TestCatalogUsesSingleEntryPagesPastHugeSchema(t *testing.T) {
	hugeSchema := json.RawMessage(`{"type":"object","description":"` + strings.Repeat("x", 80<<10) + `"}`)
	pages := []catalogTool{
		{Name: "unrelated__huge", Schema: hugeSchema},
		{Name: "session__research", Schema: json.RawMessage(`{"type":"object"}`)},
	}
	calls := 0
	available, err := availableToolsFromCatalog(func(request catalogRequest) (catalogResponse, error) {
		if request.Limit != 1 || request.Offset != calls || (calls > 0 && request.ExpectedDigest != "stable") {
			return catalogResponse{}, fmt.Errorf("request=%+v calls=%d", request, calls)
		}
		next := request.Offset + 1
		response := catalogResponse{Schema: "stado.dev/registry-catalog/v1", RegistryDigest: "stable", Tools: []catalogTool{pages[request.Offset]}}
		if next < len(pages) {
			response.NextOffset = &next
		}
		calls++
		return response, nil
	})
	if err != nil || !available["session__research"] || calls != 2 {
		t.Fatalf("available=%v calls=%d err=%v", available, calls, err)
	}
	if len(hugeSchema) <= 64<<10 || len(hugeSchema) >= hostFactBufferBytes {
		t.Fatalf("fixture size=%d does not exercise the old 64 KiB buffer", len(hugeSchema))
	}
}

func TestContextProjectionCanExceedOld64KiBBuffer(t *testing.T) {
	snapshot := sessionContext{Schema: "stado.dev/session-context-facts/v1"}
	for i := 0; i < 128; i++ {
		snapshot.Signals = append(snapshot.Signals, contextSignal{ID: strings.Repeat("s", 600), Type: "repeated_tool_failure"})
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 64<<10 || len(raw) >= hostFactBufferBytes {
		t.Fatalf("context fixture size=%d does not fit expected (64 KiB, 1 MiB) range", len(raw))
	}
}

func TestGuidanceNeverConsumesPayloadShapedFields(t *testing.T) {
	facts := qualityFacts{}
	facts.CurrentInput.Text = "ordinary coding prompt"
	snapshot := sessionContext{
		Signals:  []contextSignal{{ID: "sig-safe", Type: "repeated_tool_failure", DetectedSequence: 2, CreatedAt: time.Now()}},
		Children: []contextChild{{ID: "child-safe", Status: "running"}},
	}
	got := buildGuidance(facts, snapshot, map[string]bool{"agent__list": true})
	if strings.Contains(got, "sig-safe") || strings.Contains(got, "child-safe") {
		t.Fatalf("opaque IDs entered guidance: %s", got)
	}
}

func TestResearchPolicyRequiresCurrentCeilingAndFastMiss(t *testing.T) {
	facts := qualityFacts{}
	facts.CurrentInput.Text = "this keeps failing again"
	if got := buildGuidance(facts, sessionContext{}, nil); got != "" {
		t.Fatalf("unavailable research tool advertised: %s", got)
	}
	available := map[string]bool{"memory__research": true}
	if got := buildGuidance(facts, sessionContext{}, available); !strings.Contains(got, "`memory__research`") {
		t.Fatalf("research miss not recommended: %s", got)
	}
	facts.FastContext.Present = true
	if got := buildGuidance(facts, sessionContext{}, available); got != "" {
		t.Fatalf("fast match did not suppress research: %s", got)
	}
}
