package main

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestManifestDeclaresBoundedInteractiveCommandAndWorkerBridges(t *testing.T) {
	raw, err := os.ReadFile("plugin.manifest.template.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version         string   `json:"version"`
		MinStadoVersion string   `json:"min_stado_version"`
		Capabilities    []string `json:"capabilities"`
		Commands        []struct {
			Name      string `json:"name"`
			TimeoutMS int    `json:"timeout_ms"`
		} `json:"commands"`
		Lifecycle struct {
			TimeoutMS int      `json:"timeout_ms"`
			Events    []string `json:"events"`
		} `json:"lifecycle"`
		ArtifactKinds []struct {
			Name   string `json:"name"`
			Schema string `json:"schema"`
		} `json:"artifact_kinds"`
		Tools []struct {
			Name              string `json:"name"`
			Schema            string `json:"schema"`
			ApplicationWorker *struct {
				PlanVisible bool `json:"plan_visible"`
			} `json:"application_worker"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "0.1.0-dev.20260814" || manifest.MinStadoVersion != "0.80.0" {
		t.Fatalf("plugin package version and minimum host version were conflated: package=%q min_stado=%q", manifest.Version, manifest.MinStadoVersion)
	}
	if len(manifest.Commands) != 1 || manifest.Commands[0].Name != "supervise" || manifest.Commands[0].TimeoutMS != 900000 {
		t.Fatalf("interactive command timeout is not the signed C38 contract: %+v", manifest.Commands)
	}
	if manifest.Lifecycle.TimeoutMS <= 0 || manifest.Lifecycle.TimeoutMS > 60000 {
		t.Fatalf("interactive timeout leaked into lifecycle callbacks: %+v", manifest.Lifecycle)
	}
	for _, required := range []string{"artifact:read:self#supervision-contract", "artifact:edit:supervision-contract", "session:worker:request", "session:worker:resume", "session:worker:cancel", "session:verification:request", "session:input:route", "ui:approval", "ui:choice", "agent:cancel", "agent:spawn:configure"} {
		found := false
		for _, capability := range manifest.Capabilities {
			found = found || capability == required
		}
		if !found {
			t.Fatalf("manifest omitted %s", required)
		}
	}
	if !slices.Contains(manifest.Lifecycle.Events, "operator.input.queued") {
		t.Fatalf("manifest omitted mandatory operator input event: %+v", manifest.Lifecycle.Events)
	}
	if !slices.Contains(manifest.Lifecycle.Events, "session.verification_finished") {
		t.Fatalf("manifest omitted targeted asynchronous verification terminal event: %+v", manifest.Lifecycle.Events)
	}
	if len(manifest.Tools) != 3 {
		t.Fatalf("manifest model-tool set changed: %+v", manifest.Tools)
	}
	wantTools := map[string]bool{"supervise__report_progress": true, "supervise__request_pivot": true, "supervise__request_completion": true}
	var pivotSchema map[string]any
	for _, tool := range manifest.Tools {
		if !wantTools[tool.Name] {
			t.Fatalf("manifest contains unexpected model tool %q", tool.Name)
		}
		delete(wantTools, tool.Name)
		if tool.ApplicationWorker == nil || !tool.ApplicationWorker.PlanVisible {
			t.Fatalf("model tool %s is not explicitly projected to the exact application worker in Do and Plan modes", tool.Name)
		}
		if tool.Name == "supervise__request_pivot" {
			if err := json.Unmarshal([]byte(tool.Schema), &pivotSchema); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(wantTools) != 0 {
		t.Fatalf("manifest omitted application-worker tools: %+v", wantTools)
	}
	properties, _ := pivotSchema["properties"].(map[string]any)
	required, _ := pivotSchema["required"].([]any)
	if properties["replacement"] == nil || properties["proposed_change"] != nil || !slices.ContainsFunc(required, func(value any) bool { return value == "replacement" }) {
		t.Fatalf("pivot tool is not bound to a strict structured replacement: %#v", pivotSchema)
	}
	if len(manifest.ArtifactKinds) != 1 || manifest.ArtifactKinds[0].Name != "supervision-contract" {
		t.Fatalf("supervision contract artifact declaration=%+v", manifest.ArtifactKinds)
	}
	var schema struct {
		Properties map[string]struct {
			MaxLength  int      `json:"maxLength"`
			Required   []string `json:"required"`
			Properties map[string]struct {
				MaxLength int      `json:"maxLength"`
				Minimum   int      `json:"minimum"`
				Maximum   int      `json:"maximum"`
				Enum      []string `json:"enum"`
			} `json:"properties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(manifest.ArtifactKinds[0].Schema), &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties["objective"].MaxLength != 4096 {
		t.Fatalf("artifact objective exceeds generic worker request bound: %+v", schema.Properties["objective"])
	}
	profiles := schema.Properties["config"].Properties
	if profiles["watchdog_provider"].MaxLength != 128 || profiles["verifier_model"].MaxLength != 256 || profiles["watchdog_thinking_budget_tokens"].Maximum != 2_000_000 || !slices.Equal(profiles["verifier_reasoning_effort"].Enum, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("artifact schema omitted exact configurable agent profile: %+v", profiles)
	}
	for name, bounds := range map[string][2]int{
		"event_review_retries": {1, 10}, "failed_event_limit": {1, 100}, "correction_limit": {1, 20},
		"live_retry_base_millis": {1, 300_000}, "live_retry_max_millis": {1, 300_000},
	} {
		field := profiles[name]
		if field.Minimum != bounds[0] || field.Maximum != bounds[1] || !slices.Contains(schema.Properties["config"].Required, name) {
			t.Fatalf("artifact schema omitted durable %s bounds/requirement: field=%+v required=%v", name, field, schema.Properties["config"].Required)
		}
	}
}
