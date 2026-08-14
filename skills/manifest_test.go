package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestManifestDeclaresExactUnsignedSkillApplication(t *testing.T) {
	raw, err := os.ReadFile("plugin.manifest.template.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Name         string   `json:"name"`
		Capabilities []string `json:"capabilities"`
		Tools        []struct {
			Name         string   `json:"name"`
			Class        string   `json:"class"`
			Schema       string   `json:"schema"`
			Capabilities []string `json:"capabilities"`
		} `json:"tools"`
		Commands   []json.RawMessage `json:"commands"`
		WASMSHA256 string            `json:"wasm_sha256"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "skills" || manifest.WASMSHA256 != "" || len(manifest.Tools) != 2 || manifest.Commands == nil || len(manifest.Commands) != 0 {
		t.Fatalf("manifest = %+v", manifest)
	}
	wantCaps := []string{"context:resource:catalog:skill", "context:resource:open:skill", "registry:catalog", "session:tool-surface"}
	for i := range wantCaps {
		if i >= len(manifest.Capabilities) || manifest.Capabilities[i] != wantCaps[i] {
			t.Fatalf("capabilities = %v", manifest.Capabilities)
		}
	}
	if manifest.Tools[0].Name != "skills__search" || manifest.Tools[0].Class != "NonMutating" || manifest.Tools[1].Name != "skills__load" || manifest.Tools[1].Class != "StateMutating" {
		t.Fatalf("tools = %+v", manifest.Tools)
	}
	var loadSchema struct {
		Required []string                   `json:"required"`
		Props    map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(manifest.Tools[1].Schema), &loadSchema); err != nil {
		t.Fatal(err)
	}
	if len(loadSchema.Required) != 1 || loadSchema.Required[0] != "id" || loadSchema.Props["id"] == nil || loadSchema.Props["name"] != nil {
		t.Fatalf("load schema must select only opaque id: %s", manifest.Tools[1].Schema)
	}
	if got := manifest.Tools[0].Capabilities; len(got) != 1 || got[0] != "context:resource:catalog:skill" {
		t.Fatalf("skills__search capabilities = %v", got)
	}
	if got := manifest.Tools[1].Capabilities; len(got) != len(wantCaps) {
		t.Fatalf("skills__load capabilities = %v", got)
	} else {
		for i := range wantCaps {
			if got[i] != wantCaps[i] {
				t.Fatalf("skills__load capabilities = %v", got)
			}
		}
	}
}
