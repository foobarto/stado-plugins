package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestManifestIsCandidateOnlyAndDeclaresExactApplicationSurface(t *testing.T) {
	raw, err := os.ReadFile("plugin.manifest.template.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Capabilities []string `json:"capabilities"`
		Tools        []struct {
			Name               string    `json:"name"`
			Capabilities       *[]string `json:"capabilities"`
			ApplicationSession *struct {
				PlanVisible bool `json:"plan_visible"`
			} `json:"application_session"`
		} `json:"tools"`
		Commands []struct {
			Name string `json:"name"`
		} `json:"commands"`
		Kinds []struct {
			Name  string `json:"name"`
			Index []struct {
				Pointer string `json:"pointer"`
				Role    string `json:"role"`
			} `json:"index"`
		} `json:"artifact_kinds"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, capability := range manifest.Capabilities {
		if strings.Contains(capability, "activ") || strings.Contains(capability, "authority") || strings.Contains(capability, "approve") {
			t.Fatalf("manifest exposes fresh-promotion authority: %q", capability)
		}
	}
	if len(manifest.Tools) != 2 || manifest.Tools[0].Name != "memory" || manifest.Tools[1].Name != "learn" ||
		manifest.Tools[0].ApplicationSession == nil || manifest.Tools[1].ApplicationSession == nil ||
		manifest.Tools[0].ApplicationSession.PlanVisible || manifest.Tools[1].ApplicationSession.PlanVisible ||
		manifest.Tools[0].Capabilities != nil || manifest.Tools[1].Capabilities != nil {
		t.Fatalf("tools=%+v", manifest.Tools)
	}
	if len(manifest.Commands) != 2 || manifest.Commands[0].Name != "memory" || manifest.Commands[1].Name != "learn" {
		t.Fatalf("commands=%+v", manifest.Commands)
	}
	if len(manifest.Kinds) != 2 || len(manifest.Kinds[0].Index) == 0 || len(manifest.Kinds[1].Index) == 0 {
		t.Fatalf("artifact kinds lack signed projection descriptors: %+v", manifest.Kinds)
	}
}
