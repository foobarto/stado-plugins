package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	catalogSchema       = "stado.dev/registry-catalog/v1"
	maxSurfaceEditNames = 4096
)

type catalogTool struct {
	Name            string          `json:"name"`
	Canonical       string          `json:"canonical"`
	Description     string          `json:"description"`
	Schema          json.RawMessage `json:"schema"`
	Class           string          `json:"class"`
	Categories      []string        `json:"categories,omitempty"`
	ExtraCategories []string        `json:"extra_categories,omitempty"`
	Plugin          string          `json:"plugin"`
	SourceNamespace string          `json:"source_namespace"`
}

type catalogRequest struct {
	Offset         int    `json:"offset,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	ExpectedDigest string `json:"expected_digest,omitempty"`
}

type catalogResponse struct {
	Schema         string        `json:"schema"`
	RegistryDigest string        `json:"registry_digest"`
	NextOffset     *int          `json:"next_offset,omitempty"`
	Tools          []catalogTool `json:"tools"`
}

type surfaceRequest struct {
	RegistryDigest string   `json:"registry_digest"`
	Activate       []string `json:"activate,omitempty"`
	Deactivate     []string `json:"deactivate,omitempty"`
}

type registryHost interface {
	Catalog(catalogRequest) (catalogResponse, error)
	Apply(surfaceRequest) ([]byte, error)
}

type catalog struct {
	digest string
	tools  []catalogTool
}

func loadCatalog(host registryHost) (catalog, error) {
	const (
		// A single host fact is capped below the host's 1 MiB response
		// ceiling. Page one-at-a-time so a valid maximum-size schema cannot
		// make discovery fail merely because it shared a requested page.
		pageSize       = 1
		maxTools       = 4096
		maxCatalogJSON = 16 << 20
	)
	var result catalog
	offset := 0
	serialized := 0
	for {
		page, err := host.Catalog(catalogRequest{Offset: offset, Limit: pageSize, ExpectedDigest: result.digest})
		if err != nil {
			return catalog{}, err
		}
		if page.Schema != catalogSchema || page.RegistryDigest == "" {
			return catalog{}, errors.New("host returned an invalid registry catalog envelope")
		}
		if result.digest == "" {
			result.digest = page.RegistryDigest
		} else if page.RegistryDigest != result.digest {
			return catalog{}, errors.New("registry catalog changed during pagination")
		}
		pageJSON, err := json.Marshal(page.Tools)
		if err != nil {
			return catalog{}, fmt.Errorf("measure registry catalog page: %w", err)
		}
		serialized += len(pageJSON)
		if serialized > maxCatalogJSON {
			return catalog{}, fmt.Errorf("registry catalog exceeds plugin serialized ceiling of %d bytes", maxCatalogJSON)
		}
		result.tools = append(result.tools, page.Tools...)
		if len(result.tools) > maxTools {
			return catalog{}, fmt.Errorf("registry catalog exceeds plugin ceiling of %d tools", maxTools)
		}
		if page.NextOffset == nil {
			break
		}
		if *page.NextOffset <= offset || *page.NextOffset != offset+len(page.Tools) {
			return catalog{}, errors.New("host returned a non-progressing registry cursor")
		}
		offset = *page.NextOffset
	}
	return result, nil
}

func runTool(host registryHost, name string, args []byte) ([]byte, error) {
	cat, err := loadCatalog(host)
	if err != nil {
		return nil, err
	}
	switch name {
	case "tools__search":
		return search(cat, args)
	case "tools__describe":
		return describe(host, cat, args)
	case "tools__categories":
		return categories(cat, args)
	case "tools__in_category":
		return inCategory(cat, args)
	case "tools__activate":
		return editNamed(host, cat, args, true)
	case "tools__deactivate":
		return editNamed(host, cat, args, false)
	case "plugin__load":
		return editSource(host, cat, args, true)
	case "plugin__unload":
		return editSource(host, cat, args, false)
	default:
		return nil, fmt.Errorf("unknown tool export %q", name)
	}
}

func search(cat catalog, raw []byte) ([]byte, error) {
	var req struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := decodeArgs(raw, &req); err != nil {
		return nil, err
	}
	if req.Limit == 0 {
		req.Limit = 200
	}
	if req.Limit < 1 || req.Limit > 500 {
		return nil, errors.New("limit must be 1..500")
	}
	query := strings.ToLower(strings.TrimSpace(req.Query))
	type lightTool struct {
		Name            string   `json:"name"`
		Canonical       string   `json:"canonical"`
		Summary         string   `json:"summary"`
		Categories      []string `json:"categories,omitempty"`
		ExtraCategories []string `json:"extra_categories,omitempty"`
		Plugin          string   `json:"plugin"`
		SourceNamespace string   `json:"source_namespace"`
	}
	matched := make([]lightTool, 0, req.Limit)
	truncated := false
	for _, candidate := range cat.tools {
		haystack := strings.ToLower(strings.Join(append(append([]string{candidate.Name, candidate.Canonical, candidate.Description, candidate.Plugin, candidate.SourceNamespace}, candidate.Categories...), candidate.ExtraCategories...), " "))
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}
		if len(matched) == req.Limit {
			truncated = true
			break
		}
		matched = append(matched, lightTool{
			Name: candidate.Name, Canonical: candidate.Canonical, Summary: summarize(candidate.Description, 100),
			Categories: candidate.Categories, ExtraCategories: candidate.ExtraCategories,
			Plugin:          candidate.Plugin,
			SourceNamespace: candidate.SourceNamespace,
		})
	}
	return json.Marshal(struct {
		Tools     []lightTool `json:"tools"`
		Truncated bool        `json:"truncated"`
	}{matched, truncated})
}

func describe(host registryHost, cat catalog, raw []byte) ([]byte, error) {
	names, err := requestedNames(raw)
	if err != nil {
		return nil, err
	}
	index := indexCatalog(cat.tools)
	out := make([]map[string]any, 0, len(names))
	activate := make([]string, 0, len(names))
	for _, name := range names {
		candidate, ok := index[name]
		if !ok {
			out = append(out, map[string]any{"name": name, "error": "not found"})
			continue
		}
		entry := map[string]any{
			"name": candidate.Name, "canonical": candidate.Canonical,
			"description": candidate.Description,
			"schema":      candidate.Schema, "class": candidate.Class,
			"plugin":           candidate.Plugin,
			"source_namespace": candidate.SourceNamespace,
		}
		if len(candidate.Categories) > 0 {
			entry["categories"] = candidate.Categories
		}
		if len(candidate.ExtraCategories) > 0 {
			entry["extra_categories"] = candidate.ExtraCategories
		}
		out = append(out, entry)
		activate = append(activate, candidate.Name)
	}
	if len(activate) > 0 {
		if _, err := host.Apply(surfaceRequest{RegistryDigest: cat.digest, Activate: activate}); err != nil {
			return nil, fmt.Errorf("activate described tools: %w", err)
		}
	}
	return json.Marshal(out)
}

func categories(cat catalog, raw []byte) ([]byte, error) {
	var req struct {
		Query string `json:"query"`
	}
	if err := decodeArgs(raw, &req); err != nil {
		return nil, err
	}
	query := strings.ToLower(strings.TrimSpace(req.Query))
	seen := map[string]bool{}
	for _, candidate := range cat.tools {
		for _, category := range candidate.Categories {
			if query == "" || strings.Contains(strings.ToLower(category), query) {
				seen[category] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for category := range seen {
		out = append(out, category)
	}
	sort.Strings(out)
	return json.Marshal(out)
}

func inCategory(cat catalog, raw []byte) ([]byte, error) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeArgs(raw, &req); err != nil {
		return nil, err
	}
	if req.Name == "" {
		return nil, errors.New("name is required")
	}
	var out []map[string]any
	for _, candidate := range cat.tools {
		if !contains(candidate.Categories, req.Name) && !contains(candidate.ExtraCategories, req.Name) {
			continue
		}
		out = append(out, map[string]any{
			"name": candidate.Name, "canonical": candidate.Canonical,
			"summary":    summarize(candidate.Description, 100),
			"categories": candidate.Categories, "extra_categories": candidate.ExtraCategories,
			"plugin":           candidate.Plugin,
			"source_namespace": candidate.SourceNamespace,
		})
	}
	return json.Marshal(out)
}

func editNamed(host registryHost, cat catalog, raw []byte, activate bool) ([]byte, error) {
	names, err := requestedNames(raw)
	if err != nil {
		return nil, err
	}
	index := indexCatalog(cat.tools)
	valid := make([]string, 0, len(names))
	out := make([]map[string]any, 0, len(names))
	verb := "deactivated"
	if activate {
		verb = "activated"
	}
	for _, name := range names {
		candidate, ok := index[name]
		if !ok {
			out = append(out, map[string]any{"name": name, "error": "not found"})
			continue
		}
		valid = append(valid, candidate.Name)
		out = append(out, map[string]any{"name": candidate.Name, verb: true})
	}
	if len(valid) > 0 {
		request := surfaceRequest{RegistryDigest: cat.digest}
		if activate {
			request.Activate = valid
		} else {
			request.Deactivate = valid
		}
		if _, err := host.Apply(request); err != nil {
			return nil, err
		}
	}
	return json.Marshal(out)
}

func editSource(host registryHost, cat catalog, raw []byte, activate bool) ([]byte, error) {
	var req struct {
		Plugin string `json:"plugin"`
	}
	if err := decodeArgs(raw, &req); err != nil {
		return nil, err
	}
	if req.Plugin == "" {
		return nil, errors.New("plugin (exact source_namespace) is required")
	}
	var names []string
	for _, candidate := range cat.tools {
		if candidate.SourceNamespace == req.Plugin {
			names = append(names, candidate.Name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no catalog tools found for exact source namespace %q", req.Plugin)
	}
	request := surfaceRequest{RegistryDigest: cat.digest}
	if activate {
		request.Activate = names
	} else {
		request.Deactivate = names
	}
	if _, err := host.Apply(request); err != nil {
		return nil, err
	}
	key := "deactivated"
	if activate {
		key = "activated"
	}
	return json.Marshal(map[string]any{"plugin": req.Plugin, key: names})
}

func requestedNames(raw []byte) ([]string, error) {
	var req struct {
		Name  string   `json:"name"`
		Names []string `json:"names"`
	}
	if err := decodeArgs(raw, &req); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(req.Names)+1)
	for _, name := range append([]string{req.Name}, req.Names...) {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, errors.New("provide name or names")
	}
	if len(out) > maxSurfaceEditNames {
		return nil, fmt.Errorf("at most %d names are allowed", maxSurfaceEditNames)
	}
	return out, nil
}

func decodeArgs(raw []byte, out any) error {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("invalid args: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("invalid args: trailing JSON value")
		}
		return fmt.Errorf("invalid args: %w", err)
	}
	return nil
}

func indexCatalog(tools []catalogTool) map[string]catalogTool {
	out := make(map[string]catalogTool, len(tools))
	for _, candidate := range tools {
		out[candidate.Name] = candidate
	}
	return out
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func summarize(value string, maxRunes int) string {
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}
