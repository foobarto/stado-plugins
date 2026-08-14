package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestManifestUsesExactPerToolCapabilitiesAndChildOnlyHelpers(t *testing.T) {
	raw, err := os.ReadFile("plugin.manifest.template.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Tools []struct {
			Name           string    `json:"name"`
			Capabilities   *[]string `json:"capabilities"`
			AgentChildOnly bool      `json:"agent_child_only"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"memory__research":             {"agent:spawn", "evidence:validate"},
		"session__research":            {"agent:spawn", "evidence:validate"},
		"research__artifact_catalog":   {"evidence:catalog:artifact"},
		"research__artifact_search":    {"evidence:search:artifact"},
		"research__artifact_open":      {"evidence:open:artifact"},
		"research__session_catalog":    {"evidence:catalog:session"},
		"research__session_search":     {"evidence:search:session"},
		"research__session_open":       {"evidence:open:session"},
	}
	if len(manifest.Tools) != len(want) {
		t.Fatalf("tools=%d want=%d", len(manifest.Tools), len(want))
	}
	for _, tool := range manifest.Tools {
		caps, ok := want[tool.Name]
		if !ok || tool.Capabilities == nil || !reflect.DeepEqual(*tool.Capabilities, caps) {
			t.Fatalf("tool %q capabilities=%v", tool.Name, tool.Capabilities)
		}
		wantChildOnly := tool.Name != "memory__research" && tool.Name != "session__research"
		if tool.AgentChildOnly != wantChildOnly {
			t.Fatalf("tool %q child_only=%t want=%t", tool.Name, tool.AgentChildOnly, wantChildOnly)
		}
	}
}
