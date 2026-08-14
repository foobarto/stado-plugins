package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeResearchHost struct {
	spawnRequest spawnRequest
	spawnResult  spawnResult
	spawnErr     error
	operations   []string
	requests     []any
	evidenceRaw  json.RawMessage
	evidenceErr  error
}

func (h *fakeResearchHost) Spawn(request spawnRequest) (spawnResult, error) {
	h.spawnRequest = request
	return h.spawnResult, h.spawnErr
}

func (h *fakeResearchHost) Evidence(operation string, request any, response any) error {
	h.operations = append(h.operations, operation)
	h.requests = append(h.requests, request)
	if h.evidenceErr != nil {
		return h.evidenceErr
	}
	raw, ok := response.(*json.RawMessage)
	if !ok {
		return errors.New("unexpected response type")
	}
	*raw = append((*raw)[:0], h.evidenceRaw...)
	return nil
}

func validChildResult(corpus string) string {
	result := researchResult{
		Answer: "answer", Confidence: "high",
		Claims: []claim{{Text: "claim", Citations: []citation{{
			Ref:     evidenceRef{Corpus: corpus, Kind: "record", ID: "one", Locator: "exact", Digest: "sha256:abc"},
			Excerpt: "exact bytes",
		}}}},
	}
	raw, _ := json.Marshal(result)
	return string(raw)
}

func TestRunResearchSpawnsExactReadOnlyChildAndValidates(t *testing.T) {
	validated := []byte(validChildResult("artifact"))
	host := &fakeResearchHost{
		spawnResult: spawnResult{ID: "agent-1", SessionID: "child-1", Status: "completed", FinalText: validChildResult("artifact")},
		evidenceRaw: validated,
	}
	result, err := runResearch(host, "artifact", []byte(`{"query":"what changed?"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != string(validated) {
		t.Fatalf("result = %s", result)
	}
	wantTools := []string{"research__artifact_catalog", "research__artifact_search", "research__artifact_open"}
	request := host.spawnRequest
	if request.Async || !request.Ephemeral || request.Persona != "researcher" || request.Role != "explorer" ||
		request.Mode != "read_only" || request.ToolProfile != "read_only" || !reflect.DeepEqual(request.NarrowTools, wantTools) ||
		request.MaxTurns != childTurns || request.TimeoutSeconds != childTimeout || request.TokenBudget != childTokenBudget {
		t.Fatalf("spawn request widened: %+v", request)
	}
	if !strings.Contains(request.Prompt, "untrusted data") || !strings.Contains(request.Prompt, `"query":"what changed?"`) {
		t.Fatalf("prompt omitted policy/query: %s", request.Prompt)
	}
	if !reflect.DeepEqual(host.operations, []string{"validate"}) {
		t.Fatalf("operations = %v", host.operations)
	}
	validation, ok := host.requests[0].(validationRequest)
	if !ok || validation.ChildSession != "child-1" || validation.Result.Answer != "answer" {
		t.Fatalf("validation request = %#v", host.requests[0])
	}
}

func TestRunResearchSessionUsesOnlySessionHelpers(t *testing.T) {
	host := &fakeResearchHost{
		spawnResult: spawnResult{ID: "agent-1", SessionID: "child-1", Status: "completed", FinalText: validChildResult("session")},
		evidenceRaw: []byte(validChildResult("session")),
	}
	if _, err := runResearch(host, "session", []byte(`{"query":"find the earlier decision"}`)); err != nil {
		t.Fatal(err)
	}
	want := []string{"research__session_catalog", "research__session_search", "research__session_open"}
	if !reflect.DeepEqual(host.spawnRequest.NarrowTools, want) {
		t.Fatalf("narrow tools = %v, want %v", host.spawnRequest.NarrowTools, want)
	}
}

func TestRunResearchRejectsUnvalidatedOrMalformedChild(t *testing.T) {
	tests := []struct {
		name string
		host *fakeResearchHost
	}{
		{"non-terminal", &fakeResearchHost{spawnResult: spawnResult{SessionID: "child", Status: "running"}}},
		{"markdown-fenced", &fakeResearchHost{spawnResult: spawnResult{SessionID: "child", Status: "completed", FinalText: "```json\n{}\n```"}}},
		{"validation denied", &fakeResearchHost{spawnResult: spawnResult{SessionID: "child", Status: "completed", FinalText: validChildResult("artifact")}, evidenceErr: errors.New("fabricated citation")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := runResearch(test.host, "artifact", []byte(`{"query":"q"}`)); err == nil {
				t.Fatal("invalid child result was accepted")
			}
		})
	}
}

func TestInnerToolsFixCorpusAndPreserveExactRef(t *testing.T) {
	ref := evidenceRef{Corpus: "artifact", Kind: "lesson", ID: "art-1", Version: 2, Locator: "artifact:art-1@2", Digest: "sha256:abc"}
	host := &fakeResearchHost{evidenceRaw: []byte(`{"ok":true}`)}
	if _, err := runCatalog(host, "artifact", []byte(`{"limit":7}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := runSearch(host, "session", []byte(`{"query":"needle","limit":3}`)); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(openArgs{Ref: ref})
	if _, err := runOpen(host, "artifact", raw); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(host.operations, []string{"catalog", "search", "open"}) {
		t.Fatalf("operations = %v", host.operations)
	}
	if got := host.requests[0].(evidenceRequest); got.Corpus != "artifact" || got.Limit != 7 {
		t.Fatalf("catalog request = %+v", got)
	}
	if got := host.requests[1].(evidenceRequest); got.Corpus != "session" || got.Query != "needle" || got.Limit != 3 {
		t.Fatalf("search request = %+v", got)
	}
	if got := host.requests[2].(evidenceRequest); got.Corpus != "artifact" || got.Ref != ref {
		t.Fatalf("open request = %+v", got)
	}

	ref.Corpus = "session"
	raw, _ = json.Marshal(openArgs{Ref: ref})
	if _, err := runOpen(host, "artifact", raw); err == nil {
		t.Fatal("cross-corpus ref was accepted")
	}
}

func TestToolInputsAreStrictAndBounded(t *testing.T) {
	host := &fakeResearchHost{}
	for _, raw := range []string{`{}`, `{"query":""}`, `{"query":"q","foreign_session":"x"}`, `{"query":"q"} {}`} {
		if _, err := runResearch(host, "artifact", []byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestInnerEvidenceResultMayExceedLegacy64KiBBuffer(t *testing.T) {
	payload := []byte(`{"body":"` + strings.Repeat("x", 96<<10) + `"}`)
	host := &fakeResearchHost{evidenceRaw: payload}
	result, err := runCatalog(host, "artifact", []byte(`{"limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != len(payload) || len(result) <= 64<<10 {
		t.Fatalf("large evidence result length=%d want=%d", len(result), len(payload))
	}
}
