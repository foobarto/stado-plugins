package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type fakeRegistryHost struct {
	tools    []catalogTool
	digest   string
	edits    []surfaceRequest
	requests []catalogRequest
	pageCap  int
}

func (h *fakeRegistryHost) Catalog(req catalogRequest) (catalogResponse, error) {
	h.requests = append(h.requests, req)
	if req.ExpectedDigest != "" && req.ExpectedDigest != h.digest {
		return catalogResponse{}, errors.New("stale")
	}
	limit := req.Limit
	if h.pageCap > 0 && limit > h.pageCap {
		limit = h.pageCap
	}
	end := req.Offset + limit
	if end > len(h.tools) {
		end = len(h.tools)
	}
	response := catalogResponse{Schema: catalogSchema, RegistryDigest: h.digest, Tools: h.tools[req.Offset:end]}
	if end < len(h.tools) {
		response.NextOffset = &end
	}
	return response, nil
}

func (h *fakeRegistryHost) Apply(req surfaceRequest) ([]byte, error) {
	if req.RegistryDigest != h.digest {
		return nil, errors.New("stale")
	}
	h.edits = append(h.edits, req)
	return []byte(`{"ok":true}`), nil
}

func fixtureHost() *fakeRegistryHost {
	return &fakeRegistryHost{digest: "digest-1", pageCap: 2, tools: []catalogTool{
		{Name: "fs__read", Canonical: "fs.read", Plugin: "fs", Description: "Read a file", Schema: json.RawMessage(`{"type":"object"}`), Class: "non-mutating", Categories: []string{"filesystem"}, SourceNamespace: "stado.dev/bundled/fs"},
		{Name: "fs__write", Canonical: "fs.write", Plugin: "fs", Description: "Write a file", Schema: json.RawMessage(`{"type":"object"}`), Class: "mutating", Categories: []string{"filesystem"}, ExtraCategories: []string{"editing"}, SourceNamespace: "stado.dev/bundled/fs"},
		{Name: "shell__bash", Canonical: "shell.bash", Plugin: "shell", Description: "Execute shell commands", Schema: json.RawMessage(`{"type":"object"}`), Class: "exec", Categories: []string{"shell"}, SourceNamespace: "stado.dev/bundled/shell"},
	}}
}

func TestSearchCategoryAndDescriptionPolicyLivesInPlugin(t *testing.T) {
	host := fixtureHost()
	result, err := runTool(host, "tools__search", []byte(`{"query":"filesystem","limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), `"fs__read"`) || !strings.Contains(string(result), `"truncated":true`) {
		t.Fatalf("search result = %s", result)
	}
	if !strings.Contains(string(result), `"canonical":"fs.read"`) || !strings.Contains(string(result), `"plugin":"fs"`) {
		t.Fatalf("search omitted authenticated canonical/display facts: %s", result)
	}
	result, err = runTool(host, "tools__categories", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != `["filesystem","shell"]` || strings.Contains(string(result), "editing") {
		t.Fatalf("categories = %s", result)
	}
	result, err = runTool(host, "tools__in_category", []byte(`{"name":"editing"}`))
	if err != nil || !strings.Contains(string(result), `"fs__write"`) {
		t.Fatalf("in_category = %s err=%v", result, err)
	}
}

func TestDescribeActivatesExactFoundBatch(t *testing.T) {
	host := fixtureHost()
	result, err := runTool(host, "tools__describe", []byte(`{"name":"fs__read","names":["missing","fs__read","fs__write"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(result), `"name":"fs__read"`) != 1 || !strings.Contains(string(result), `"error":"not found"`) {
		t.Fatalf("describe = %s", result)
	}
	if len(host.edits) != 1 || strings.Join(host.edits[0].Activate, ",") != "fs__read,fs__write" || host.edits[0].RegistryDigest != host.digest {
		t.Fatalf("edits = %+v", host.edits)
	}
}

func TestActivateDeactivateAndExactSourceGrouping(t *testing.T) {
	host := fixtureHost()
	if _, err := runTool(host, "tools__activate", []byte(`{"names":["fs__read","missing"]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := runTool(host, "tools__deactivate", []byte(`{"name":"fs__read"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := runTool(host, "plugin__load", []byte(`{"plugin":"stado.dev/bundled/fs"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := runTool(host, "plugin__unload", []byte(`{"plugin":"stado.dev/bundled/shell"}`)); err != nil {
		t.Fatal(err)
	}
	if len(host.edits) != 4 {
		t.Fatalf("edits = %+v", host.edits)
	}
	if got := strings.Join(host.edits[2].Activate, ","); got != "fs__read,fs__write" {
		t.Fatalf("source load = %s", got)
	}
	if got := strings.Join(host.edits[3].Deactivate, ","); got != "shell__bash" {
		t.Fatalf("source unload = %s", got)
	}
}

func TestPluginLoadAppliesSixtyFiveNamesInOneAtomicBatch(t *testing.T) {
	host := &fakeRegistryHost{digest: "digest-1", pageCap: 1}
	for i := 0; i < 65; i++ {
		host.tools = append(host.tools, catalogTool{
			Name:            fmt.Sprintf("tool__%02d", i),
			SourceNamespace: "stado.dev/example/large",
		})
	}
	if _, err := runTool(host, "plugin__load", []byte(`{"plugin":"stado.dev/example/large"}`)); err != nil {
		t.Fatal(err)
	}
	if len(host.edits) != 1 || len(host.edits[0].Activate) != 65 {
		t.Fatalf("plugin load split or truncated the batch: %+v", host.edits)
	}
}

func TestRequestedNamesAllows4096AndRejects4097(t *testing.T) {
	names := make([]string, maxSurfaceEditNames+1)
	for i := range names {
		names[i] = fmt.Sprintf("tool__%04d", i)
	}
	raw, err := json.Marshal(map[string]any{"names": names[:maxSurfaceEditNames]})
	if err != nil {
		t.Fatal(err)
	}
	got, err := requestedNames(raw)
	if err != nil || len(got) != maxSurfaceEditNames {
		t.Fatalf("4096 names: got=%d err=%v", len(got), err)
	}
	raw, err = json.Marshal(map[string]any{"names": names})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := requestedNames(raw); err == nil {
		t.Fatal("4097 names were accepted")
	}
}

func TestCatalogPaginationAndStrictArgs(t *testing.T) {
	host := fixtureHost()
	cat, err := loadCatalog(host)
	if err != nil || len(cat.tools) != 3 || cat.digest != host.digest {
		t.Fatalf("catalog = %+v err=%v", cat, err)
	}
	if _, err := runTool(host, "tools__search", []byte(`{"query":"fs","native_policy":true}`)); err == nil {
		t.Fatal("unknown workflow field accepted")
	}
	if _, err := runTool(host, "tools__search", []byte(`{} {}`)); err == nil {
		t.Fatal("trailing JSON value accepted")
	}
	if _, err := runTool(host, "plugin__load", []byte(`{"plugin":"fs"}`)); err == nil {
		t.Fatal("display alias selected source authority")
	}
	for _, request := range host.requests {
		if request.Limit != 1 {
			t.Fatalf("catalog page limit = %d, want one-entry worst-case-safe paging", request.Limit)
		}
	}
}
