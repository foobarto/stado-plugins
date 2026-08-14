package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestManifestDeclaresExactProviderPrimitiveAndNoAuthoritySurface(t *testing.T) {
	raw, err := os.ReadFile("plugin.manifest.template.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version         string   `json:"version"`
		MinStadoVersion string   `json:"min_stado_version"`
		Capabilities    []string `json:"capabilities"`
		Tools           []struct {
			Name         string   `json:"name"`
			Export       string   `json:"export"`
			Class        string   `json:"class"`
			Schema       string   `json:"schema"`
			Capabilities []string `json:"capabilities"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "0.1.0-dev.20260814" || manifest.MinStadoVersion != "0.80.0" {
		t.Fatalf("version=%q min_stado=%q", manifest.Version, manifest.MinStadoVersion)
	}
	if len(manifest.Capabilities) != 1 || manifest.Capabilities[0] != "provider:invoke:16384" {
		t.Fatalf("capabilities=%v", manifest.Capabilities)
	}
	if len(manifest.Tools) != 1 || manifest.Tools[0].Name != "llm__invoke" || manifest.Tools[0].Export != "invoke" {
		t.Fatalf("tools=%+v", manifest.Tools)
	}
	if manifest.Tools[0].Class != "StateMutating" {
		t.Fatalf("tool class=%q, want StateMutating", manifest.Tools[0].Class)
	}
	if len(manifest.Tools[0].Capabilities) != 1 || manifest.Tools[0].Capabilities[0] != "provider:invoke:16384" {
		t.Fatalf("tool capabilities=%v, want exact provider ceiling", manifest.Tools[0].Capabilities)
	}
	var schema struct {
		AdditionalProperties bool                       `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(manifest.Tools[0].Schema), &schema); err != nil {
		t.Fatal(err)
	}
	if schema.AdditionalProperties || schema.Properties["persona"] != nil || schema.Properties["provider"] != nil || schema.Properties["token_budget"] != nil {
		t.Fatalf("schema exposes native policy/authority fields: %s", manifest.Tools[0].Schema)
	}
}
