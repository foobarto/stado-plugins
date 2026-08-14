package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestInvokeToolShapesProviderRequestAndPreservesExplicitZeroTemperature(t *testing.T) {
	var request providerRequest
	outcome, err := invokeTool([]byte(`{"prompt":"hello","system":"be terse","model":"m","max_tokens":8,"temperature":0}`), func(raw []byte) ([]byte, error) {
		if err := json.Unmarshal(raw, &request); err != nil {
			t.Fatal(err)
		}
		return completedFacts("answer", nil), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Text != "answer" || len(request.Messages) != 1 || request.Messages[0].Content != "hello" || request.System != "be terse" || request.Model != "m" || request.MaxOutputTokens != 8 {
		t.Fatalf("outcome=%+v request=%+v", outcome, request)
	}
	if request.Temperature == nil || *request.Temperature != 0 {
		t.Fatalf("explicit zero temperature lost: %+v", request)
	}
}

func TestInvokeToolCleanupDoesNotDiscardValidText(t *testing.T) {
	cleanup := &providerDiagnostic{Kind: "provider_close", Fingerprint: "sha256:" + strings.Repeat("a", 64)}
	outcome, err := invokeTool([]byte(`{"prompt":"hello"}`), func([]byte) ([]byte, error) {
		return completedFacts("valid verdict", cleanup), nil
	})
	if err != nil || outcome.Text != "valid verdict" || outcome.Cleanup == nil || *outcome.Cleanup != *cleanup {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}

func TestProviderFactsRejectInvalidUsageBoundsAndDiagnosticCombinations(t *testing.T) {
	diagnostic := &providerDiagnostic{Kind: "provider_stream", Fingerprint: "sha256:" + strings.Repeat("c", 64)}
	tests := []providerFacts{
		{Schema: providerFactsSchema, Status: "completed", Usage: providerUsage{InputTokens: -1}},
		{Schema: providerFactsSchema, Status: "completed", Usage: providerUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 4}},
		{Schema: providerFactsSchema, Status: "completed", Text: "over", Usage: providerUsage{InputTokens: pluginTokenCeiling, OutputTokens: 1, TotalTokens: pluginTokenCeiling + 1}},
		{Schema: providerFactsSchema, Status: "completed", Provider: strings.Repeat("p", maxProviderNameBytes+1)},
		{Schema: providerFactsSchema, Status: "completed", Model: strings.Repeat("m", maxModelBytes+1)},
		{Schema: providerFactsSchema, Status: "completed", Text: strings.Repeat("x", maxProviderTextBytes+1)},
		{Schema: providerFactsSchema, Status: "completed", Diagnostic: diagnostic},
		{Schema: providerFactsSchema, Status: "failed"},
		{Schema: providerFactsSchema, Status: "failed", Diagnostic: &providerDiagnostic{Kind: "provider_close", Fingerprint: "sha256:" + strings.Repeat("d", 64)}},
		{Schema: providerFactsSchema, Status: "failed", Diagnostic: diagnostic, Cleanup: diagnostic},
	}
	for index, facts := range tests {
		raw, err := json.Marshal(facts)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeProviderFacts(raw); err == nil {
			t.Fatalf("case %d accepted invalid provider facts: %+v", index, facts)
		}
	}
	valid := providerFacts{
		Schema: providerFactsSchema, Status: "completed", Text: "ok",
		Usage: providerUsage{InputTokens: 5, OutputTokens: 3, CacheReadTokens: 4, CacheWriteTokens: 2, TotalTokens: 8},
	}
	raw, _ := json.Marshal(valid)
	if _, err := decodeProviderFacts(raw); err != nil {
		t.Fatalf("cache subdivisions were double-counted or valid facts rejected: %v", err)
	}
}

func TestProviderFactsGuestBufferLengthIsBounded(t *testing.T) {
	buffer := []byte("facts")
	if _, err := copyBoundedProviderFacts(buffer, int32(len(buffer)+1)); err == nil {
		t.Fatal("host length beyond guest buffer was accepted")
	}
	if _, err := copyBoundedProviderFacts(buffer, -1); err == nil {
		t.Fatal("negative host result was accepted")
	}
	got, err := copyBoundedProviderFacts(buffer, int32(len(buffer)))
	if err != nil || string(got) != string(buffer) {
		t.Fatalf("bounded provider facts = %q err=%v", got, err)
	}
}

func TestToolErrorUsesRuntimeEnvelopeSchema(t *testing.T) {
	raw, err := encodeToolError("provider failed")
	if err != nil {
		t.Fatal(err)
	}
	var envelope toolErrorEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Schema != "stado.tool-error.v1" || envelope.Kind != "" || envelope.Message != "provider failed" {
		t.Fatalf("tool error envelope = %+v", envelope)
	}
}

func TestInvokeToolRejectsGuestAuthorityAndProviderFailure(t *testing.T) {
	if _, err := invokeTool([]byte(`{"prompt":"hello","token_budget":1}`), func([]byte) ([]byte, error) { return nil, nil }); err == nil {
		t.Fatal("guest authority field accepted")
	}
	diagnostic := &providerDiagnostic{Kind: "provider_stream", Fingerprint: "sha256:" + strings.Repeat("b", 64)}
	raw, _ := json.Marshal(providerFacts{Schema: providerFactsSchema, Status: "failed", Diagnostic: diagnostic})
	if _, err := invokeTool([]byte(`{"prompt":"hello"}`), func([]byte) ([]byte, error) { return raw, nil }); err == nil || !strings.Contains(err.Error(), diagnostic.Fingerprint) {
		t.Fatalf("provider failure not surfaced safely: %v", err)
	}
}

func TestInvokeToolStrictBoundsAndBridgeFailure(t *testing.T) {
	for _, raw := range []string{
		`{"prompt":""}`,
		`{"prompt":"x","max_tokens":16385}`,
		`{"prompt":"x","temperature":2.1}`,
		`{"prompt":"x"} {}`,
	} {
		if _, err := invokeTool([]byte(raw), func([]byte) ([]byte, error) { return nil, nil }); err == nil {
			t.Fatalf("invalid args accepted: %s", raw)
		}
	}
	if _, err := invokeTool([]byte(`{"prompt":"x"}`), func([]byte) ([]byte, error) { return nil, errors.New("unavailable") }); err == nil {
		t.Fatal("bridge error swallowed")
	}
}

func completedFacts(text string, cleanup *providerDiagnostic) []byte {
	raw, _ := json.Marshal(providerFacts{
		Schema: providerFactsSchema, Status: "completed", Text: text,
		Usage: providerUsage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}, Cleanup: cleanup,
	})
	return raw
}
