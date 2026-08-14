package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

const (
	providerFactsSchema   = "stado.dev/provider-invoke-facts/v1"
	toolErrorSchema       = "stado.tool-error.v1"
	pluginTokenCeiling    = 16384
	maxPromptBytes        = 256 << 10
	maxSystemBytes        = 256 << 10
	maxModelBytes         = 512
	maxToolInputBytes     = 1 << 20
	maxProviderFactsBytes = 1 << 20
	maxProviderNameBytes  = 512
	maxProviderTextBytes  = (1 << 20) - (16 << 10)
)

type invokeArgs struct {
	Prompt      string   `json:"prompt"`
	System      string   `json:"system,omitempty"`
	Model       string   `json:"model,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type providerRequest struct {
	System          string            `json:"system,omitempty"`
	Messages        []providerMessage `json:"messages"`
	Model           string            `json:"model,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
}

type providerMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type providerUsage struct {
	InputTokens      int  `json:"input_tokens"`
	OutputTokens     int  `json:"output_tokens"`
	CacheReadTokens  int  `json:"cache_read_tokens"`
	CacheWriteTokens int  `json:"cache_write_tokens"`
	TotalTokens      int  `json:"total_tokens"`
	Estimated        bool `json:"estimated"`
}

type providerDiagnostic struct {
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
}

type providerFacts struct {
	Schema     string              `json:"schema"`
	Status     string              `json:"status"`
	Text       string              `json:"text,omitempty"`
	Provider   string              `json:"provider,omitempty"`
	Model      string              `json:"model,omitempty"`
	Usage      providerUsage       `json:"usage"`
	Diagnostic *providerDiagnostic `json:"diagnostic,omitempty"`
	Cleanup    *providerDiagnostic `json:"cleanup,omitempty"`
}

type invokeOutcome struct {
	Text    string
	Cleanup *providerDiagnostic
	Usage   providerUsage
}

type toolErrorEnvelope struct {
	Schema  string `json:"schema"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type providerInvoker func([]byte) ([]byte, error)

func invokeTool(raw []byte, invoke providerInvoker) (invokeOutcome, error) {
	args, err := decodeInvokeArgs(raw)
	if err != nil {
		return invokeOutcome{}, err
	}
	request, err := json.Marshal(providerRequest{
		System: args.System, Messages: []providerMessage{{Role: "user", Content: args.Prompt}},
		Model: args.Model, MaxOutputTokens: args.MaxTokens, Temperature: args.Temperature,
	})
	if err != nil {
		return invokeOutcome{}, fmt.Errorf("encode provider request: %w", err)
	}
	rawFacts, err := invoke(request)
	if err != nil {
		return invokeOutcome{}, fmt.Errorf("provider bridge unavailable: %w", err)
	}
	facts, err := decodeProviderFacts(rawFacts)
	if err != nil {
		return invokeOutcome{}, err
	}
	if facts.Status != "completed" {
		kind := "provider_failed"
		fingerprint := ""
		if facts.Diagnostic != nil {
			kind = facts.Diagnostic.Kind
			fingerprint = facts.Diagnostic.Fingerprint
		}
		return invokeOutcome{}, fmt.Errorf("provider invocation %s: %s %s", facts.Status, kind, fingerprint)
	}
	return invokeOutcome{Text: facts.Text, Cleanup: facts.Cleanup, Usage: facts.Usage}, nil
}

func decodeInvokeArgs(raw []byte) (invokeArgs, error) {
	if len(raw) == 0 || len(raw) > maxToolInputBytes {
		return invokeArgs{}, errors.New("llm.invoke input is empty or oversized")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var args invokeArgs
	if err := decoder.Decode(&args); err != nil {
		return invokeArgs{}, fmt.Errorf("llm.invoke arguments: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return invokeArgs{}, fmt.Errorf("llm.invoke arguments: %w", err)
	}
	if args.Prompt == "" || len(args.Prompt) > maxPromptBytes {
		return invokeArgs{}, fmt.Errorf("prompt must contain 1..%d bytes", maxPromptBytes)
	}
	if len(args.System) > maxSystemBytes {
		return invokeArgs{}, fmt.Errorf("system exceeds %d bytes", maxSystemBytes)
	}
	if len(args.Model) > maxModelBytes || strings.TrimSpace(args.Model) != args.Model {
		return invokeArgs{}, fmt.Errorf("model must be at most %d bytes without surrounding whitespace", maxModelBytes)
	}
	if args.MaxTokens < 0 || args.MaxTokens > pluginTokenCeiling {
		return invokeArgs{}, fmt.Errorf("max_tokens must be between 0 and %d", pluginTokenCeiling)
	}
	if args.Temperature != nil && (math.IsNaN(*args.Temperature) || math.IsInf(*args.Temperature, 0) || *args.Temperature < 0 || *args.Temperature > 2) {
		return invokeArgs{}, errors.New("temperature must be between 0 and 2")
	}
	return args, nil
}

func decodeProviderFacts(raw []byte) (providerFacts, error) {
	if len(raw) == 0 || len(raw) > maxProviderFactsBytes {
		return providerFacts{}, errors.New("provider facts are empty or oversized")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var facts providerFacts
	if err := decoder.Decode(&facts); err != nil {
		return providerFacts{}, fmt.Errorf("provider facts: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return providerFacts{}, fmt.Errorf("provider facts: %w", err)
	}
	if facts.Schema != providerFactsSchema {
		return providerFacts{}, fmt.Errorf("provider facts schema %q is unsupported", facts.Schema)
	}
	if len(facts.Provider) > maxProviderNameBytes || len(facts.Model) > maxModelBytes || len(facts.Text) > maxProviderTextBytes {
		return providerFacts{}, errors.New("provider facts contain an oversized provider, model, or text field")
	}
	if facts.Usage.InputTokens < 0 || facts.Usage.OutputTokens < 0 ||
		facts.Usage.CacheReadTokens < 0 || facts.Usage.CacheWriteTokens < 0 ||
		facts.Usage.TotalTokens < 0 {
		return providerFacts{}, errors.New("provider facts contain negative usage")
	}
	if facts.Usage.TotalTokens != saturatingTokenSum(facts.Usage.InputTokens, facts.Usage.OutputTokens) {
		return providerFacts{}, errors.New("provider facts total_tokens is not input_tokens + output_tokens")
	}
	if facts.Usage.TotalTokens > pluginTokenCeiling {
		return providerFacts{}, errors.New("provider facts exceed the signed token ceiling")
	}
	switch facts.Status {
	case "completed":
		if facts.Diagnostic != nil {
			return providerFacts{}, errors.New("completed provider facts contain a semantic diagnostic")
		}
	case "failed", "cancelled":
		if facts.Diagnostic == nil {
			return providerFacts{}, errors.New("failed or cancelled provider facts lack a semantic diagnostic")
		}
	default:
		return providerFacts{}, fmt.Errorf("provider facts status %q is invalid", facts.Status)
	}
	if err := validateDiagnostic(facts.Diagnostic, false); err != nil {
		return providerFacts{}, fmt.Errorf("provider diagnostic: %w", err)
	}
	if err := validateDiagnostic(facts.Cleanup, true); err != nil {
		return providerFacts{}, fmt.Errorf("provider cleanup diagnostic: %w", err)
	}
	return facts, nil
}

func copyBoundedProviderFacts(buffer []byte, n int32) ([]byte, error) {
	if n < 0 {
		return nil, errors.New("host refused provider invocation")
	}
	if int64(n) > int64(len(buffer)) {
		return nil, errors.New("host returned provider facts beyond the declared buffer")
	}
	return append([]byte(nil), buffer[:n]...), nil
}

func validateDiagnostic(diagnostic *providerDiagnostic, cleanup bool) error {
	if diagnostic == nil {
		return nil
	}
	allowed := map[string]bool{
		"provider_construct": true, "provider_unavailable": true, "token_budget": true,
		"provider_stream": true, "provider_stream_incomplete": true,
		"context_cancelled": true, "response_limit": true,
	}
	if cleanup {
		allowed = map[string]bool{"provider_close": true}
	}
	if !allowed[diagnostic.Kind] {
		return fmt.Errorf("kind %q is invalid", diagnostic.Kind)
	}
	if len(diagnostic.Fingerprint) != len("sha256:")+64 || !strings.HasPrefix(diagnostic.Fingerprint, "sha256:") {
		return errors.New("fingerprint is not sha256")
	}
	for _, char := range diagnostic.Fingerprint[len("sha256:"):] {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return errors.New("fingerprint is not lowercase hexadecimal")
		}
	}
	return nil
}

func saturatingTokenSum(input, output int) int {
	if input < 0 || output < 0 {
		return -1
	}
	if output > math.MaxInt-input {
		return math.MaxInt
	}
	return input + output
}

func encodeToolError(message string) ([]byte, error) {
	// The provider/application boundary has no pkg/tool launch-vs-exit
	// classification. Keep FailureUnknown rather than inventing a new enum.
	return json.Marshal(toolErrorEnvelope{Schema: toolErrorSchema, Kind: "", Message: message})
}

func requireEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
