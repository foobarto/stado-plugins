package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeSkillHost struct {
	resources []resourceFact
	contents  map[string]string
	registry  []registryTool
	applied   []string
	stale     bool
}

func (h *fakeSkillHost) Catalog(request resourceCatalogRequest) (resourceCatalogResponse, error) {
	digest := "sha256:skills"
	if h.stale && request.Offset > 0 {
		digest = "sha256:changed"
	}
	if request.ExpectedDigest != "" && request.ExpectedDigest != digest {
		return resourceCatalogResponse{}, errors.New("stale")
	}
	end := request.Offset + request.Limit
	if end > len(h.resources) {
		end = len(h.resources)
	}
	var next *int
	if end < len(h.resources) {
		value := end
		next = &value
	}
	return resourceCatalogResponse{Schema: resourceCatalogSchema, CatalogDigest: digest, NextOffset: next, Resources: h.resources[request.Offset:end]}, nil
}

func (h *fakeSkillHost) Open(request resourceOpenRequest) (resourceOpenResponse, error) {
	if request.CatalogDigest != "sha256:skills" {
		return resourceOpenResponse{}, errors.New("stale")
	}
	for _, resource := range h.resources {
		if resource.ID == request.ID {
			return resourceOpenResponse{Schema: resourceOpenSchema, resourceFact: resource, ContentFormat: "text/markdown", Content: h.contents[resource.ID]}, nil
		}
	}
	return resourceOpenResponse{}, errors.New("not found")
}

func (h *fakeSkillHost) Registry(request registryRequest) (registryResponse, error) {
	digest := "sha256:registry"
	end := request.Offset + request.Limit
	if end > len(h.registry) {
		end = len(h.registry)
	}
	var next *int
	if end < len(h.registry) {
		value := end
		next = &value
	}
	return registryResponse{Schema: registryCatalogSchema, RegistryDigest: digest, NextOffset: next, Tools: h.registry[request.Offset:end]}, nil
}

func (h *fakeSkillHost) Apply(request surfaceRequest) error {
	if request.RegistryDigest != "sha256:registry" {
		return errors.New("wrong registry")
	}
	h.applied = append([]string(nil), request.Activate...)
	return nil
}

func testSkillHost() *fakeSkillHost {
	return &fakeSkillHost{
		resources: []resourceFact{
			{ID: "sha256:a", Digest: "sha256:body-a", Kind: "skill", Name: "alpha", Summary: "review code", Scope: "project", Provenance: "project-discovered", ModelVisible: true},
			{ID: "sha256:b", Digest: "sha256:body-b", Kind: "skill", Name: "beta", Summary: "investigate failures", Scope: "persona", Provenance: "persona-declared", ModelVisible: true, EffectiveAllowedTools: []string{"fs__read"}},
		},
		contents: map[string]string{"sha256:a": "alpha body", "sha256:b": "beta body"},
		registry: []registryTool{{Name: "fs__read"}},
	}
}

func TestSearchOwnsMatchingAndFormatting(t *testing.T) {
	host := testSkillHost()
	raw, err := runTool(host, "skills__search", []byte(`{"query":"failures","limit":10}`))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Schema string `json:"schema"`
		Skills []struct {
			Name       string `json:"name"`
			Provenance string `json:"provenance"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Schema != searchResultSchema || len(result.Skills) != 1 || result.Skills[0].Name != "beta" || result.Skills[0].Provenance != "persona-declared" {
		t.Fatalf("result = %s", raw)
	}
}

func TestLoadReturnsOrdinaryLabeledToolResultAndActivatesEffectiveTools(t *testing.T) {
	host := testSkillHost()
	raw, err := runTool(host, "skills__load", []byte(`{"id":"sha256:b"}`))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Schema     string   `json:"schema"`
		Body       string   `json:"body"`
		Scope      string   `json:"scope"`
		Provenance string   `json:"provenance"`
		Activated  []string `json:"allowed_tools_activated"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Schema != loadResultSchema || result.Body != "beta body" || result.Scope != "persona" || result.Provenance != "persona-declared" {
		t.Fatalf("result = %s", raw)
	}
	if len(result.Activated) != 1 || result.Activated[0] != "fs__read" || len(host.applied) != 1 || host.applied[0] != "fs__read" {
		t.Fatalf("activated result=%v host=%v", result.Activated, host.applied)
	}
}

func TestLoadFailsClosedOnRegistryMismatchAndStrictArguments(t *testing.T) {
	host := testSkillHost()
	host.registry = nil
	if _, err := runTool(host, "skills__load", []byte(`{"id":"sha256:b"}`)); err == nil {
		t.Fatal("load ignored an allowed tool absent from the registry catalog")
	}
	if _, err := runTool(testSkillHost(), "skills__load", []byte(`{"id":"sha256:b","inject_role":"user"}`)); err == nil {
		t.Fatal("load accepted an unknown role-injection field")
	}
}

func TestLoadUsesOpaqueIDWhenDisplayNamesCollideAndRejectsFabrication(t *testing.T) {
	host := testSkillHost()
	host.resources = append(host.resources, resourceFact{
		ID: "sha256:c", Digest: "sha256:body-c", Kind: "skill", Name: "beta",
		Summary: "same display name, different source", Scope: "project",
		Provenance: "project-discovered", ModelVisible: true,
	})
	host.contents["sha256:c"] = "second beta body"
	raw, err := runTool(host, "skills__load", []byte(`{"id":"sha256:c"}`))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.ID != "sha256:c" || result.Name != "beta" || result.Body != "second beta body" {
		t.Fatalf("exact duplicate-name selection = %+v", result)
	}
	if _, err := runTool(host, "skills__load", []byte(`{"id":"sha256:fabricated"}`)); err == nil || !strings.Contains(err.Error(), "current catalog") {
		t.Fatalf("fabricated id error = %v", err)
	}
	if _, err := runTool(host, "skills__load", []byte(`{"name":"beta"}`)); err == nil {
		t.Fatal("display-name selector remained accepted")
	}
}

func TestLoadWorstCaseEscaped128KiBBodyFitsGenericResult(t *testing.T) {
	host := testSkillHost()
	host.contents["sha256:b"] = strings.Repeat("\x01", 128<<10)
	raw, err := runTool(host, "skills__load", []byte(`{"id":"sha256:b"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxToolResultBytes {
		t.Fatalf("escaped tool result = %d bytes, exceeds %d", len(raw), maxToolResultBytes)
	}
}

func TestCatalogPaginationIsDigestFenced(t *testing.T) {
	host := testSkillHost()
	host.stale = true
	if _, err := runTool(host, "skills__search", []byte(`{}`)); err == nil {
		t.Fatal("search accepted a catalog change between pages")
	}
}
