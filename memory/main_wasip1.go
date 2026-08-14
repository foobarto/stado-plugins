//go:build wasip1

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unsafe"
)

func main() {}

// All authority-bearing values remain behind fixed host imports. The guest
// supplies no migration path/bytes/identity, no artifact scope binding, no
// evidence session selector, and no activation or operator-grant assertion.

//go:wasmimport stado stado_artifact_migrate_legacy_memory_v1
func stadoArtifactMigrateLegacyMemoryV1(reqPtr, reqLen, outPtr, outCap uint32) int32

//go:wasmimport stado stado_artifact_propose
func stadoArtifactPropose(reqPtr, reqLen, outPtr, outCap uint32) int32

//go:wasmimport stado stado_artifact_query
func stadoArtifactQuery(reqPtr, reqLen, outPtr, outCap uint32) int32

//go:wasmimport stado stado_session_journal_append
func stadoSessionJournalAppend(reqPtr, reqLen, outPtr, outCap uint32) int32

//go:wasmimport stado stado_session_projection_read
func stadoSessionProjectionRead(reqPtr, reqLen, outPtr, outCap uint32) int32

//go:wasmimport stado stado_evidence_catalog
func stadoEvidenceCatalog(reqPtr, reqLen, outPtr, outCap uint32) int32

//go:wasmimport stado stado_evidence_open
func stadoEvidenceOpen(reqPtr, reqLen, outPtr, outCap uint32) int32

//go:wasmimport stado stado_provider_invoke
func stadoProviderInvoke(reqPtr, reqLen, outPtr, outCap uint32) int32

var (
	pinned sync.Map
	appMu  sync.Mutex
)

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 {
	if size <= 0 {
		return 0
	}
	buffer := make([]byte, size)
	pointer := uintptr(unsafe.Pointer(&buffer[0]))
	pinned.Store(pointer, buffer)
	return int32(pointer)
}

//go:wasmexport stado_free
func stadoFree(pointer int32, _ int32) { pinned.Delete(uintptr(pointer)) }

type lifecycleResult struct {
	Decision     string                 `json:"decision"`
	Contribution *lifecycleContribution `json:"contribution,omitempty"`
}

type lifecycleContribution struct {
	SystemAppend string `json:"system_append"`
}

type commandEnvelope struct {
	Schema      string          `json:"schema"`
	Application string          `json:"application"`
	Anchor      lifecycleAnchor `json:"anchor"`
	Sequence    uint64          `json:"sequence"`
	Command     string          `json:"command"`
	Args        string          `json:"args,omitempty"`
}

type memoryToolArgs struct {
	Action         string `json:"action"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	Scope          string `json:"scope,omitempty"`
	Summary        string `json:"summary,omitempty"`
	Content        string `json:"content,omitempty"`
	Trigger        string `json:"trigger,omitempty"`
	ActiveOnly     bool   `json:"active_only,omitempty"`
}

type learnToolArgs struct {
	Action string `json:"action"`
	Focus  string `json:"focus,omitempty"`
}

//go:wasmexport stado_plugin_lifecycle
func stadoPluginLifecycle(inputPointer, inputLength, resultPointer, resultCapacity int32) int32 {
	appMu.Lock()
	defer appMu.Unlock()
	continueResult := func() int32 {
		return writeJSON(resultPointer, resultCapacity, lifecycleResult{Decision: "continue"})
	}
	var envelope lifecycleEnvelope
	if decodeStrict(wasmBytes(inputPointer, inputLength), &envelope) != nil || envelope.Schema != "stado.dev/lifecycle/v1" ||
		envelope.Point != "pre_llm" || envelope.Application == "" || envelope.Sequence == 0 || envelope.Anchor.SessionID == "" ||
		envelope.Anchor.SessionGeneration == 0 || len(envelope.Payload) == 0 {
		return continueResult()
	}
	if ensureLegacyMigrated() != nil {
		return continueResult()
	}
	enabled, _, err := readMemorySetting()
	if err != nil || !enabled {
		return continueResult()
	}
	items, complete, err := queryArtifacts(true)
	if err != nil {
		return continueResult()
	}
	contribution, err := contextContribution(items, complete)
	if err != nil || contribution == "" {
		return continueResult()
	}
	return writeJSON(resultPointer, resultCapacity, lifecycleResult{
		Decision: "contribute", Contribution: &lifecycleContribution{SystemAppend: contribution},
	})
}

//go:wasmexport stado_tool_memory
func stadoToolMemory(inputPointer, inputLength, resultPointer, resultCapacity int32) int32 {
	appMu.Lock()
	defer appMu.Unlock()
	if err := ensureLegacyMigrated(); err != nil {
		return writeError(resultPointer, resultCapacity, "legacy memory migration blocked: "+err.Error())
	}
	var args memoryToolArgs
	if err := decodeStrict(wasmBytes(inputPointer, inputLength), &args); err != nil {
		return writeError(resultPointer, resultCapacity, "memory: "+err.Error())
	}
	result, err := runMemoryTool(args)
	if err != nil {
		return writeError(resultPointer, resultCapacity, "memory: "+err.Error())
	}
	return writeJSON(resultPointer, resultCapacity, result)
}

//go:wasmexport stado_tool_learn
func stadoToolLearn(inputPointer, inputLength, resultPointer, resultCapacity int32) int32 {
	appMu.Lock()
	defer appMu.Unlock()
	if err := ensureLegacyMigrated(); err != nil {
		return writeError(resultPointer, resultCapacity, "legacy memory migration blocked: "+err.Error())
	}
	var args learnToolArgs
	if err := decodeStrict(wasmBytes(inputPointer, inputLength), &args); err != nil {
		return writeError(resultPointer, resultCapacity, "learn: "+err.Error())
	}
	if strings.ToLower(strings.TrimSpace(args.Action)) != "review" {
		return writeError(resultPointer, resultCapacity, "learn: action must be review")
	}
	items, err := runLearnReview(args.Focus)
	if err != nil {
		return writeError(resultPointer, resultCapacity, "learn: "+err.Error())
	}
	return writeJSON(resultPointer, resultCapacity, map[string]any{
		"candidates": items, "count": len(items),
		"authority": "candidate", "activation": "requires a future generic trusted presenter and consumed operator-origin grant",
	})
}

//go:wasmexport stado_plugin_command
func stadoPluginCommand(inputPointer, inputLength, resultPointer, resultCapacity int32) int32 {
	appMu.Lock()
	defer appMu.Unlock()
	var envelope commandEnvelope
	if err := decodeStrict(wasmBytes(inputPointer, inputLength), &envelope); err != nil || envelope.Schema != "stado.dev/application-command/v1" ||
		envelope.Application == "" || envelope.Anchor.SessionID == "" || envelope.Anchor.SessionGeneration == 0 || envelope.Sequence == 0 {
		return writeError(resultPointer, resultCapacity, "memory command: invalid authenticated envelope")
	}
	if err := ensureLegacyMigrated(); err != nil {
		return writeError(resultPointer, resultCapacity, "legacy memory migration blocked: "+err.Error())
	}
	var message string
	var err error
	switch envelope.Command {
	case "memory":
		message, err = runMemoryCommand(envelope, strings.TrimSpace(envelope.Args))
	case "learn":
		if args := strings.TrimSpace(envelope.Args); args != "" && args != "review" {
			err = errors.New("usage: /learn [review]")
			break
		}
		var candidates []artifact
		candidates, err = runLearnReview("")
		if err == nil {
			message = fmt.Sprintf("created %d candidate lesson(s); activation is unavailable until the generic trusted presenter can consume an operator-origin grant", len(candidates))
		}
	default:
		err = errors.New("unsupported memory application command")
	}
	if err != nil {
		return writeError(resultPointer, resultCapacity, err.Error())
	}
	return writeJSON(resultPointer, resultCapacity, map[string]string{"status": "ok", "message": message})
}

func runMemoryTool(args memoryToolArgs) (any, error) {
	switch strings.ToLower(strings.TrimSpace(args.Action)) {
	case "list":
		items, complete, err := queryArtifacts(args.ActiveOnly)
		if err != nil {
			return nil, err
		}
		return map[string]any{"items": items, "complete": complete, "count": len(items)}, nil
	case "propose":
		value, err := normalizeMemory(memoryData{Summary: args.Summary, Content: args.Content, Trigger: args.Trigger})
		if err != nil {
			return nil, err
		}
		if err := validateLogicalKey(args.IdempotencyKey); err != nil {
			return nil, err
		}
		scope, err := normalizeScope(args.Scope)
		if err != nil {
			return nil, err
		}
		item, err := proposeArtifact("memory", scope, value, nil, "memory:"+args.IdempotencyKey)
		if err != nil {
			return nil, err
		}
		return map[string]any{"candidate": item, "authority": "candidate"}, nil
	default:
		return nil, errors.New("action must be list or propose")
	}
}

func runMemoryCommand(envelope commandEnvelope, args string) (string, error) {
	parts := strings.Fields(args)
	if len(parts) == 0 || parts[0] == "status" {
		enabled, _, err := readMemorySetting()
		if err != nil {
			return "", err
		}
		if enabled {
			return "memory context contribution is enabled for this authenticated application session", nil
		}
		return "memory context contribution is disabled for this authenticated application session", nil
	}
	switch parts[0] {
	case "on", "off":
		enabled := parts[0] == "on"
		if len(parts) != 1 {
			return "", errors.New("usage: /memory [status|on|off|list|add --key <key> <summary>]")
		}
		if err := setMemorySetting(envelope.Anchor, enabled); err != nil {
			return "", err
		}
		return "memory context contribution set to " + parts[0] + " for this authenticated application session", nil
	case "list":
		items, complete, err := queryArtifacts(false)
		if err != nil {
			return "", err
		}
		return formatArtifactList(items, complete), nil
	case "add":
		if len(parts) < 4 || parts[1] != "--key" {
			return "", errors.New("usage: /memory add --key <key> <summary>")
		}
		if err := validateLogicalKey(parts[2]); err != nil {
			return "", err
		}
		value, err := normalizeMemory(memoryData{Summary: strings.Join(parts[3:], " ")})
		if err != nil {
			return "", err
		}
		item, err := proposeArtifact("memory", "session", value, nil, "memory-command:"+parts[2])
		if err != nil {
			return "", err
		}
		return "created candidate memory " + item.ID + "; activation requires the generic trusted presenter", nil
	default:
		return "", errors.New("usage: /memory [status|on|off|list|add --key <key> <summary>]")
	}
}

func ensureLegacyMigrated() error {
	raw, err := callHostRaw(stadoArtifactMigrateLegacyMemoryV1, struct{}{})
	if err != nil {
		return err
	}
	var result struct {
		Complete    bool     `json:"complete"`
		Quarantined []string `json:"quarantined,omitempty"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.Complete || len(result.Quarantined) != 0 {
		return errors.New("broker did not return an exact completed migration fence")
	}
	return nil
}

func queryArtifacts(activeOnly bool) ([]artifact, bool, error) {
	raw, err := callHostRaw(stadoArtifactQuery, map[string]any{
		"kinds": []string{"self#memory", "self#lesson"}, "active_only": activeOnly, "max_items": 50,
	})
	if err != nil {
		return nil, false, err
	}
	var response queryResponse
	if json.Unmarshal(raw, &response) != nil || !validPageDigest(response.PageDigest) || len(response.Items) > 50 || (!response.Complete && response.NextOffset <= 0) {
		return nil, false, errors.New("artifact broker returned an invalid bounded projection")
	}
	for _, item := range response.Items {
		if _, _, err := artifactData(item); err != nil {
			return nil, false, err
		}
		if activeOnly && item.Authority != "active" {
			return nil, false, errors.New("artifact broker mixed non-active state into active query")
		}
	}
	return response.Items, response.Complete, nil
}

func proposeArtifact(kind, scope string, data any, evidenceReceiptIDs []string, key string) (artifact, error) {
	request := map[string]any{
		"idempotency_key": key, "kind": kind, "scope": scope, "sensitivity": "normal", "data": data,
	}
	if len(evidenceReceiptIDs) != 0 {
		request["evidence_receipt_ids"] = evidenceReceiptIDs
	}
	raw, err := callHostRaw(stadoArtifactPropose, request)
	if err != nil {
		return artifact{}, err
	}
	var item artifact
	if json.Unmarshal(raw, &item) != nil || item.Authority != "candidate" {
		return artifact{}, errors.New("artifact broker did not return a candidate")
	}
	local, got, err := artifactData(item)
	if err != nil || local != kind {
		return artifact{}, errors.New("artifact broker returned the wrong candidate kind")
	}
	want, _ := json.Marshal(data)
	actual, _ := json.Marshal(got)
	if string(want) != string(actual) {
		return artifact{}, errors.New("artifact broker changed candidate data")
	}
	return item, nil
}

func readMemorySetting() (bool, uint64, error) {
	raw, err := readApplicationProjection()
	if err != nil {
		return false, 0, err
	}
	return latestMemorySetting(raw)
}

func readApplicationProjection() ([]byte, error) {
	return callHostRaw(stadoSessionProjectionRead, map[string]any{"journal_limit": 256})
}

func setMemorySetting(anchor lifecycleAnchor, enabled bool) error {
	current, sequence, err := readMemorySetting()
	if err != nil {
		return err
	}
	if current == enabled {
		return nil
	}
	data := memorySetting{Enabled: enabled}
	summary := "memory context contribution disabled"
	if enabled {
		summary = "memory context contribution enabled"
	}
	raw, err := callHostRaw(stadoSessionJournalAppend, map[string]any{
		"idempotency_key": "memory-setting:" + strconv.FormatBool(enabled) + ":after:" + strconv.FormatUint(sequence, 10),
		"run_id":          "memory-context", "kind": "memory.context-setting", "summary": summary, "data": data,
	})
	if err != nil {
		return err
	}
	var ack struct {
		SessionID  string          `json:"session_id"`
		Generation uint64          `json:"generation"`
		Sequence   uint64          `json:"sequence"`
		Kind       string          `json:"kind"`
		Summary    string          `json:"summary"`
		Data       json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &ack) != nil || ack.SessionID != anchor.SessionID || ack.Generation != anchor.SessionGeneration ||
		ack.Sequence == 0 || ack.Kind != "memory.context-setting" || ack.Summary != summary {
		return errors.New("broker journal acknowledgement changed the memory setting identity")
	}
	var acknowledged memorySetting
	if decodeStrict(ack.Data, &acknowledged) != nil || acknowledged != data {
		return errors.New("broker journal acknowledgement changed the memory setting")
	}
	return nil
}

func runLearnReview(focus string) ([]artifact, error) {
	projectionRaw, err := readApplicationProjection()
	if err != nil {
		return nil, err
	}
	durableReviews, err := foldLearnReviews(projectionRaw)
	if err != nil {
		return nil, err
	}
	for _, state := range durableReviews {
		if state.Result != nil && state.Completed == nil {
			// Resume result materialization before reading a newer catalog. The
			// durable intent carries the original broker receipt IDs, so recovery
			// neither reopens evidence nor repeats the provider call.
			return materializeLearnReview(state.Intent, *state.Result)
		}
	}
	catalogRaw, err := callHostRaw(stadoEvidenceCatalog, map[string]any{"corpus": "session", "limit": maxReviewEvidence})
	if err != nil {
		return nil, err
	}
	catalog, err := decodeEvidenceCatalog(catalogRaw)
	if err != nil {
		return nil, err
	}
	opened := make([]evidenceOpened, 0, len(catalog))
	for _, item := range catalog {
		raw, err := callHostRaw(stadoEvidenceOpen, map[string]any{"corpus": "session", "ref": item.Ref})
		if err != nil {
			return nil, err
		}
		var evidence evidenceOpened
		if json.Unmarshal(raw, &evidence) != nil || evidence.Ref != item.Ref || !validPageDigest(evidence.ReceiptID) {
			return nil, errors.New("evidence broker changed an exact session reference")
		}
		opened = append(opened, evidence)
	}
	system, prompt, err := reviewPrompt(opened, focus)
	if err != nil {
		return nil, err
	}
	evidenceDigest, _ := json.Marshal(opened)
	sourceKey := digestBytes(evidenceDigest)
	reviewID := digestBytes([]byte(system + "\x00" + prompt))
	exactReceiptIDs, err := exactEvidenceReceiptIDs(opened)
	if err != nil {
		return nil, err
	}
	intentSeen, durableResult, completed, err := recoverLearnReview(projectionRaw, reviewID)
	if err != nil {
		return nil, err
	}
	if completed {
		return nil, nil
	}
	var suggestions []lessonData
	intent := learnReviewIntent{ReviewID: reviewID, SourceDigest: sourceKey, FocusDigest: digestBytes([]byte(strings.TrimSpace(focus))), ReceiptIDs: exactReceiptIDs}
	for _, item := range opened {
		intent.Refs = append(intent.Refs, item.Ref)
	}
	if durableResult != nil {
		suggestions = durableResult.Suggestions
	} else {
		if intentSeen {
			return nil, errors.New("an identical learn review has durable intent but no durable result; provider reply-loss is ambiguous, so automatic retry is refused")
		}
		if err := appendReviewJournal("learn.review-intent", "learn review provider intent", intent, "learn-intent:"+reviewID); err != nil {
			return nil, err
		}
		// The provider primitive has no durable idempotency key. The intent is
		// durable before invocation and the structured result is journalled
		// before artifact proposals. A crash after provider success but before
		// result append remains inherently ambiguous and is never auto-retried.
		providerRaw, err := callHostRaw(stadoProviderInvoke, map[string]any{
			"system": system, "messages": []map[string]string{{"role": "user", "content": prompt}},
			"max_output_tokens": 3000, "temperature": 0.1,
		})
		if err != nil {
			return nil, err
		}
		facts, err := decodeProviderFacts(providerRaw)
		if err != nil {
			return nil, err
		}
		suggestions, err = decodeSuggestions([]byte(facts.Text))
		if err != nil {
			return nil, fmt.Errorf("provider returned invalid candidate lessons: %w", err)
		}
		result := learnReviewResult{ReviewID: reviewID, Suggestions: suggestions, Provider: facts.Provider, Model: facts.Model, Usage: facts.Usage, Cleanup: facts.Cleanup}
		if err := appendReviewJournal("learn.review-result", "learn review candidate result", result, "learn-result:"+reviewID); err != nil {
			return nil, err
		}
		durableResult = &result
	}
	if durableResult == nil {
		return nil, errors.New("learn review has no durable provider result")
	}
	return materializeLearnReview(intent, *durableResult)
}

func materializeLearnReview(intent learnReviewIntent, result learnReviewResult) ([]artifact, error) {
	if intent.ReviewID == "" || result.ReviewID != intent.ReviewID || len(intent.ReceiptIDs) == 0 {
		return nil, errors.New("durable learn review materialization identity is invalid")
	}
	created := make([]artifact, 0, len(result.Suggestions))
	refs := make([]learnArtifactRef, 0, len(result.Suggestions))
	for _, suggestion := range result.Suggestions {
		encoded, _ := json.Marshal(suggestion)
		item, err := proposeArtifact("lesson", "session", suggestion, intent.ReceiptIDs, "learn:"+intent.SourceDigest[:24]+":"+digestBytes(encoded)[:24])
		if err != nil {
			return nil, fmt.Errorf("propose candidate lesson: %w", err)
		}
		created = append(created, item)
		refs = append(refs, learnArtifactRef{ID: item.ID, Version: item.Version})
	}
	completed := learnReviewCompleted{ReviewID: intent.ReviewID, Artifacts: refs}
	if err := appendReviewJournal("learn.review-completed", "learn review candidates materialized", completed, "learn-completed:"+intent.ReviewID); err != nil {
		return nil, err
	}
	return created, nil
}

func appendReviewJournal(kind, summary string, data any, key string) error {
	rawData, err := json.Marshal(data)
	if err != nil || len(rawData) > maxLearnResultBytes {
		return errors.New("learn review journal data is oversized")
	}
	raw, err := callHostRaw(stadoSessionJournalAppend, map[string]any{
		"idempotency_key": key, "run_id": "memory-learn", "kind": kind, "summary": summary, "data": json.RawMessage(rawData),
	})
	if err != nil {
		return err
	}
	var ack struct {
		Sequence uint64          `json:"sequence"`
		Kind     string          `json:"kind"`
		Summary  string          `json:"summary"`
		Data     json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &ack) != nil || ack.Sequence == 0 || ack.Kind != kind || ack.Summary != summary ||
		digestBytes(ack.Data) != digestBytes(rawData) {
		return errors.New("broker journal acknowledgement changed the learn review state")
	}
	return nil
}

func formatArtifactList(items []artifact, complete bool) string {
	if len(items) == 0 {
		return "no visible memory or lesson artifacts"
	}
	var out strings.Builder
	for _, item := range items {
		local, value, _ := artifactData(item)
		summary := ""
		switch typed := value.(type) {
		case memoryData:
			summary = typed.Summary
		case lessonData:
			summary = typed.Summary
		}
		fmt.Fprintf(&out, "%s %s v%d [%s]: %s\n", local, item.ID, item.Version, item.Authority, summary)
	}
	if !complete {
		out.WriteString("additional artifacts omitted by broker page bound\n")
	}
	return strings.TrimSpace(out.String())
}

func normalizeScope(scope string) (string, error) {
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" {
		return "session", nil
	}
	if scope != "session" && scope != "repo" && scope != "global" {
		return "", errors.New("scope must be session, repo, or global")
	}
	return scope, nil
}

func validateLogicalKey(key string) error {
	if strings.TrimSpace(key) != key || key == "" || len(key) > 64 {
		return errors.New("idempotency_key must contain 1..64 bytes without surrounding whitespace")
	}
	for _, char := range key {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("._:-", char) {
			continue
		}
		return errors.New("idempotency_key contains an unsupported character")
	}
	return nil
}

type hostJSONCall func(uint32, uint32, uint32, uint32) int32

func callHostRaw(call hostJSONCall, request any) ([]byte, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	requestPointer := stadoAlloc(int32(len(raw)))
	defer stadoFree(requestPointer, int32(len(raw)))
	copy(wasmBytes(requestPointer, int32(len(raw))), raw)
	capacity := int32(maxHostResponseBytes)
	responsePointer := stadoAlloc(capacity)
	n := call(uint32(requestPointer), uint32(len(raw)), uint32(responsePointer), uint32(capacity))
	if n < 0 {
		if n < -capacity {
			stadoFree(responsePointer, capacity)
			return nil, errors.New("host import returned an invalid error length")
		}
		payload := append([]byte(nil), wasmBytes(responsePointer, -n)...)
		stadoFree(responsePointer, capacity)
		var envelope struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(payload, &envelope) == nil && envelope.Error != "" {
			return nil, errors.New(envelope.Error)
		}
		return nil, errors.New("host import refused request")
	}
	if n <= 0 || n > capacity {
		stadoFree(responsePointer, capacity)
		return nil, errors.New("host import returned an invalid response length")
	}
	payload := append([]byte(nil), wasmBytes(responsePointer, n)...)
	stadoFree(responsePointer, capacity)
	if !json.Valid(payload) {
		return nil, errors.New("host import returned invalid JSON")
	}
	return payload, nil
}

func wasmBytes(pointer, size int32) []byte {
	if pointer == 0 || size <= 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(pointer))), int(size))
}

func writeJSON(pointer, capacity int32, value any) int32 {
	raw, err := json.Marshal(value)
	if err != nil || capacity <= 0 || int32(len(raw)) > capacity {
		return 0
	}
	copy(wasmBytes(pointer, capacity), raw)
	return int32(len(raw))
}

func writeError(pointer, capacity int32, message string) int32 {
	n := writeJSON(pointer, capacity, map[string]string{"error": message})
	if n <= 0 {
		return -1
	}
	return -n
}
