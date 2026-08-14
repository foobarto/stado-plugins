package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	resourceCatalogSchema = "stado.dev/context-resource-catalog/v1"
	resourceOpenSchema    = "stado.dev/context-resource-open/v1"
	registryCatalogSchema = "stado.dev/registry-catalog/v1"
	searchResultSchema    = "stado.dev/skills-search-result/v1"
	loadResultSchema      = "stado.dev/skill-load-result/v1"
	maxToolResultBytes    = 1 << 20
)

type resourceFact struct {
	ID                    string   `json:"id"`
	Digest                string   `json:"digest"`
	Kind                  string   `json:"kind"`
	Name                  string   `json:"name"`
	Summary               string   `json:"summary,omitempty"`
	Scope                 string   `json:"scope"`
	Provenance            string   `json:"provenance"`
	ModelVisible          bool     `json:"model_visible"`
	EffectiveAllowedTools []string `json:"effective_allowed_tools,omitempty"`
}

type resourceCatalogRequest struct {
	Kind           string `json:"kind"`
	Offset         int    `json:"offset,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	ExpectedDigest string `json:"expected_digest,omitempty"`
}

type resourceCatalogResponse struct {
	Schema        string         `json:"schema"`
	CatalogDigest string         `json:"catalog_digest"`
	NextOffset    *int           `json:"next_offset,omitempty"`
	Resources     []resourceFact `json:"resources"`
}

type resourceOpenRequest struct {
	Kind          string `json:"kind"`
	ID            string `json:"id"`
	CatalogDigest string `json:"catalog_digest"`
}

type resourceOpenResponse struct {
	Schema string `json:"schema"`
	resourceFact
	ContentFormat string `json:"content_format"`
	Content       string `json:"content"`
}

type registryTool struct {
	Name string `json:"name"`
}

type registryRequest struct {
	Offset         int    `json:"offset,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	ExpectedDigest string `json:"expected_digest,omitempty"`
}

type registryResponse struct {
	Schema         string         `json:"schema"`
	RegistryDigest string         `json:"registry_digest"`
	NextOffset     *int           `json:"next_offset,omitempty"`
	Tools          []registryTool `json:"tools"`
}

type surfaceRequest struct {
	RegistryDigest string   `json:"registry_digest"`
	Activate       []string `json:"activate"`
}

type skillHost interface {
	Catalog(resourceCatalogRequest) (resourceCatalogResponse, error)
	Open(resourceOpenRequest) (resourceOpenResponse, error)
	Registry(registryRequest) (registryResponse, error)
	Apply(surfaceRequest) error
}

type skillCatalog struct {
	digest    string
	resources []resourceFact
}

func loadSkillCatalog(host skillHost) (skillCatalog, error) {
	const (
		pageSize       = 1 // one maximum-size summary must still fit the host page
		maxResources   = 4096
		maxCatalogJSON = 16 << 20
	)
	var result skillCatalog
	offset, serialized := 0, 0
	for {
		page, err := host.Catalog(resourceCatalogRequest{Kind: "skill", Offset: offset, Limit: pageSize, ExpectedDigest: result.digest})
		if err != nil {
			return skillCatalog{}, err
		}
		if page.Schema != resourceCatalogSchema || page.CatalogDigest == "" {
			return skillCatalog{}, errors.New("host returned an invalid skill catalog envelope")
		}
		if result.digest == "" {
			result.digest = page.CatalogDigest
		} else if page.CatalogDigest != result.digest {
			return skillCatalog{}, errors.New("skill catalog changed during pagination")
		}
		for _, resource := range page.Resources {
			if resource.Kind != "skill" || !resource.ModelVisible || resource.ID == "" || resource.Digest == "" || resource.Name == "" {
				return skillCatalog{}, errors.New("host returned an invalid admitted skill fact")
			}
		}
		encoded, err := json.Marshal(page.Resources)
		if err != nil {
			return skillCatalog{}, err
		}
		serialized += len(encoded)
		if serialized > maxCatalogJSON {
			return skillCatalog{}, fmt.Errorf("skill catalog exceeds plugin serialized ceiling of %d bytes", maxCatalogJSON)
		}
		result.resources = append(result.resources, page.Resources...)
		if len(result.resources) > maxResources {
			return skillCatalog{}, fmt.Errorf("skill catalog exceeds plugin ceiling of %d entries", maxResources)
		}
		if page.NextOffset == nil {
			break
		}
		if *page.NextOffset != offset+len(page.Resources) || *page.NextOffset <= offset {
			return skillCatalog{}, errors.New("host returned a non-progressing skill cursor")
		}
		offset = *page.NextOffset
	}
	return result, nil
}

func runTool(host skillHost, name string, raw []byte) ([]byte, error) {
	switch name {
	case "skills__search":
		return searchSkills(host, raw)
	case "skills__load":
		return loadSkill(host, raw)
	default:
		return nil, fmt.Errorf("unknown tool export %q", name)
	}
}

func searchSkills(host skillHost, raw []byte) ([]byte, error) {
	var request struct {
		Query string `json:"query"`
		Limit int    `json:"limit,omitempty"`
	}
	if err := decodeArgs(raw, &request); err != nil {
		return nil, err
	}
	if request.Limit == 0 {
		request.Limit = 50
	}
	if request.Limit < 1 || request.Limit > 200 {
		return nil, errors.New("limit must be 1..200")
	}
	catalog, err := loadSkillCatalog(host)
	if err != nil {
		return nil, err
	}
	query := strings.ToLower(strings.TrimSpace(request.Query))
	type resultFact struct {
		ID                    string   `json:"id"`
		Name                  string   `json:"name"`
		Summary               string   `json:"summary,omitempty"`
		Scope                 string   `json:"scope"`
		Provenance            string   `json:"provenance"`
		EffectiveAllowedTools []string `json:"effective_allowed_tools,omitempty"`
	}
	results := make([]resultFact, 0, request.Limit)
	truncated := false
	for _, resource := range catalog.resources {
		haystack := strings.ToLower(strings.Join([]string{resource.Name, resource.Summary, resource.Scope, resource.Provenance}, " "))
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}
		if len(results) == request.Limit {
			truncated = true
			break
		}
		results = append(results, resultFact{
			ID: resource.ID, Name: resource.Name, Summary: resource.Summary,
			Scope: resource.Scope, Provenance: resource.Provenance,
			EffectiveAllowedTools: append([]string(nil), resource.EffectiveAllowedTools...),
		})
	}
	return json.Marshal(struct {
		Schema        string       `json:"schema"`
		CatalogDigest string       `json:"catalog_digest"`
		Skills        []resultFact `json:"skills"`
		Truncated     bool         `json:"truncated"`
	}{searchResultSchema, catalog.digest, results, truncated})
}

func loadSkill(host skillHost, raw []byte) ([]byte, error) {
	var request struct {
		ID string `json:"id"`
	}
	if err := decodeArgs(raw, &request); err != nil {
		return nil, err
	}
	if request.ID == "" || len(request.ID) > 128 || !utf8.ValidString(request.ID) || strings.TrimSpace(request.ID) != request.ID {
		return nil, errors.New("id must be trimmed valid UTF-8 and 1..128 bytes")
	}
	catalog, err := loadSkillCatalog(host)
	if err != nil {
		return nil, err
	}
	var selected *resourceFact
	for i := range catalog.resources {
		if catalog.resources[i].ID != request.ID {
			continue
		}
		if selected != nil {
			return nil, fmt.Errorf("host returned duplicate resource id %q", request.ID)
		}
		selected = &catalog.resources[i]
	}
	if selected == nil {
		return nil, fmt.Errorf("skill resource id %q not found in the current catalog", request.ID)
	}
	opened, err := host.Open(resourceOpenRequest{Kind: "skill", ID: selected.ID, CatalogDigest: catalog.digest})
	if err != nil {
		return nil, err
	}
	if opened.Schema != resourceOpenSchema || opened.ContentFormat != "text/markdown" || !sameResourceFact(*selected, opened.resourceFact) {
		return nil, errors.New("host open facts do not match the digest-fenced catalog entry")
	}
	activated, err := activateAllowedTools(host, opened.EffectiveAllowedTools)
	if err != nil {
		return nil, err
	}
	result, err := json.Marshal(struct {
		Schema                string   `json:"schema"`
		Name                  string   `json:"name"`
		ID                    string   `json:"id"`
		Digest                string   `json:"digest"`
		Scope                 string   `json:"scope"`
		Provenance            string   `json:"provenance"`
		ContentFormat         string   `json:"content_format"`
		Body                  string   `json:"body"`
		AllowedToolsActivated []string `json:"allowed_tools_activated,omitempty"`
	}{
		loadResultSchema, opened.Name, opened.ID, opened.Digest, opened.Scope,
		opened.Provenance, opened.ContentFormat, opened.Content, activated,
	})
	if err != nil {
		return nil, err
	}
	if len(result) > maxToolResultBytes {
		return nil, fmt.Errorf("skill result exceeds the %d-byte plugin tool-result ceiling", maxToolResultBytes)
	}
	return result, nil
}

func activateAllowedTools(host skillHost, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	registry, err := loadRegistry(host)
	if err != nil {
		return nil, err
	}
	available := make(map[string]bool, len(registry.tools))
	for _, candidate := range registry.tools {
		available[candidate.Name] = true
	}
	activated := append([]string(nil), names...)
	sort.Strings(activated)
	for _, name := range activated {
		if !available[name] {
			return nil, fmt.Errorf("host-projected allowed tool %q is absent from the exact registry catalog", name)
		}
	}
	if err := host.Apply(surfaceRequest{RegistryDigest: registry.digest, Activate: activated}); err != nil {
		return nil, fmt.Errorf("activate skill allowed tools: %w", err)
	}
	return activated, nil
}

type toolRegistry struct {
	digest string
	tools  []registryTool
}

func loadRegistry(host skillHost) (toolRegistry, error) {
	const maxTools = 4096
	var result toolRegistry
	offset := 0
	for {
		page, err := host.Registry(registryRequest{Offset: offset, Limit: 64, ExpectedDigest: result.digest})
		if err != nil {
			return toolRegistry{}, err
		}
		if page.Schema != registryCatalogSchema || page.RegistryDigest == "" {
			return toolRegistry{}, errors.New("host returned an invalid registry catalog envelope")
		}
		if result.digest == "" {
			result.digest = page.RegistryDigest
		} else if result.digest != page.RegistryDigest {
			return toolRegistry{}, errors.New("registry changed during pagination")
		}
		result.tools = append(result.tools, page.Tools...)
		if len(result.tools) > maxTools {
			return toolRegistry{}, fmt.Errorf("registry exceeds plugin ceiling of %d tools", maxTools)
		}
		if page.NextOffset == nil {
			break
		}
		if *page.NextOffset <= offset || *page.NextOffset != offset+len(page.Tools) {
			return toolRegistry{}, errors.New("host returned a non-progressing registry cursor")
		}
		offset = *page.NextOffset
	}
	return result, nil
}

func sameResourceFact(a, b resourceFact) bool {
	left, errLeft := json.Marshal(a)
	right, errRight := json.Marshal(b)
	return errLeft == nil && errRight == nil && bytes.Equal(left, right)
}

func decodeArgs(raw []byte, out any) error {
	if len(raw) > 64<<10 {
		return errors.New("arguments exceed 64 KiB")
	}
	if !utf8.Valid(raw) {
		return errors.New("arguments are not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("invalid arguments: trailing JSON value")
		}
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}
