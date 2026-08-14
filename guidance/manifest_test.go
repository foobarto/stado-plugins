package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestManifestHasOnlyAdvisoryTUIAuthority(t *testing.T) {
	raw, err := os.ReadFile("plugin.manifest.template.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Capabilities []string `json:"capabilities"`
		Tools        []any    `json:"tools"`
		Commands     []any    `json:"commands"`
		Lifecycle    struct {
			Points  []string `json:"points"`
			Failure string   `json:"failure"`
		} `json:"lifecycle"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object["tools"] == nil || object["commands"] == nil {
		t.Fatalf("manifest must explicitly declare empty tools and commands: keys=%v err=%v", object, err)
	}
	wantCapabilities := []string{
		"session:context:read", "registry:catalog",
		"lifecycle:observe:pre_llm", "lifecycle:contribute:pre_llm",
	}
	if !reflect.DeepEqual(manifest.Capabilities, wantCapabilities) {
		t.Fatalf("capabilities = %#v", manifest.Capabilities)
	}
	if !reflect.DeepEqual(manifest.Lifecycle.Points, []string{"pre_llm"}) || manifest.Lifecycle.Failure != "open" {
		t.Fatalf("lifecycle = %#v", manifest.Lifecycle)
	}
	if len(manifest.Tools) != 0 || len(manifest.Commands) != 0 {
		t.Fatalf("guidance unexpectedly exposes tools=%d commands=%d", len(manifest.Tools), len(manifest.Commands))
	}
}
