package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestManifestAttenuatesReadOnlyAndMutatingTools(t *testing.T) {
	raw, err := os.ReadFile("plugin.manifest.template.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Capabilities []string `json:"capabilities"`
		Tools        []struct {
			Name         string   `json:"name"`
			Capabilities []string `json:"capabilities"`
			Schema       string   `json:"schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Capabilities) != 2 || manifest.Capabilities[0] != "registry:catalog" || manifest.Capabilities[1] != "session:tool-surface" {
		t.Fatalf("package capabilities = %v", manifest.Capabilities)
	}
	for _, definition := range manifest.Tools {
		want := 2
		if definition.Name == "tools__search" || definition.Name == "tools__categories" || definition.Name == "tools__in_category" {
			want = 1
		}
		if len(definition.Capabilities) != want || definition.Capabilities[0] != "registry:catalog" {
			t.Errorf("%s capabilities = %v, want %d capability(s) starting with registry:catalog", definition.Name, definition.Capabilities, want)
		}
		if want == 2 && definition.Capabilities[1] != "session:tool-surface" {
			t.Errorf("%s capabilities = %v, missing session:tool-surface", definition.Name, definition.Capabilities)
		}
		if definition.Name == "tools__describe" || definition.Name == "tools__activate" || definition.Name == "tools__deactivate" {
			var schema struct {
				Properties struct {
					Names struct {
						MaxItems int `json:"maxItems"`
					} `json:"names"`
				} `json:"properties"`
			}
			if err := json.Unmarshal([]byte(definition.Schema), &schema); err != nil {
				t.Fatalf("%s schema: %v", definition.Name, err)
			}
			if schema.Properties.Names.MaxItems != maxSurfaceEditNames {
				t.Errorf("%s names maxItems = %d, want %d", definition.Name, schema.Properties.Names.MaxItems, maxSurfaceEditNames)
			}
		}
	}
}
