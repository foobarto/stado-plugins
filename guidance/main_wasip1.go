//go:build wasip1

package main

import (
	"encoding/json"
	"errors"
	"sync"
	"unsafe"
)

func main() {}

// Both imports are bounded fact projections. The host resolves the opaque
// application/session binding and the live session tool ceiling; guest JSON
// cannot select a session, generation, repository, or broader registry.

//go:wasmimport stado stado_session_context_read
func stadoSessionContextRead(reqPtr, reqLen, outPtr, outCap uint32) int32

//go:wasmimport stado stado_registry_catalog
func stadoRegistryCatalog(reqPtr, reqLen, outPtr, outCap uint32) int32

var pinned sync.Map

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

type lifecycleEnvelope struct {
	Schema  string          `json:"schema"`
	Point   string          `json:"point"`
	Payload json.RawMessage `json:"payload"`
}

type preLLMPayload struct {
	QualityFacts *qualityFacts `json:"quality_facts"`
}

type lifecycleResult struct {
	Decision     string                 `json:"decision"`
	Contribution *lifecycleContribution `json:"contribution,omitempty"`
}

type lifecycleContribution struct {
	SystemAppend string `json:"system_append"`
}

// stadoPluginLifecycle turns bounded facts into bounded advice. CurrentInput
// and FastContext are untrusted quality observations, not operator text or
// authority. Every malformed input/import/catalog condition fails open with a
// plain continue response, so this advisory app cannot become a turn gate.
//
//go:wasmexport stado_plugin_lifecycle
func stadoPluginLifecycle(inputPointer, inputLength, resultPointer, resultCapacity int32) int32 {
	continueResult := func() int32 {
		return writeJSON(resultPointer, resultCapacity, lifecycleResult{Decision: "continue"})
	}
	var envelope lifecycleEnvelope
	if json.Unmarshal(wasmBytes(inputPointer, inputLength), &envelope) != nil || envelope.Schema != "stado.dev/lifecycle/v1" || envelope.Point != "pre_llm" {
		return continueResult()
	}
	var payload preLLMPayload
	if json.Unmarshal(envelope.Payload, &payload) != nil || payload.QualityFacts == nil {
		return continueResult()
	}
	var snapshot sessionContext
	if callHostJSON(stadoSessionContextRead, struct{}{}, &snapshot) != nil || snapshot.Schema != "stado.dev/session-context-facts/v1" {
		return continueResult()
	}
	available, err := readAvailableTools()
	if err != nil {
		return continueResult()
	}
	guidance := buildGuidance(*payload.QualityFacts, snapshot, available)
	if guidance == "" {
		return continueResult()
	}
	return writeJSON(resultPointer, resultCapacity, lifecycleResult{
		Decision: "contribute", Contribution: &lifecycleContribution{SystemAppend: guidance},
	})
}

func readAvailableTools() (map[string]bool, error) {
	return availableToolsFromCatalog(func(request catalogRequest) (catalogResponse, error) {
		var response catalogResponse
		err := callHostJSON(stadoRegistryCatalog, request, &response)
		return response, err
	})
}

func callHostJSON(call func(uint32, uint32, uint32, uint32) int32, request, response any) error {
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	requestPointer := stadoAlloc(int32(len(raw)))
	defer stadoFree(requestPointer, int32(len(raw)))
	copy(wasmBytes(requestPointer, int32(len(raw))), raw)
	capacity := int32(hostFactBufferBytes)
	resultPointer := stadoAlloc(capacity)
	n := call(uint32(requestPointer), uint32(len(raw)), uint32(resultPointer), uint32(capacity))
	if n < 0 || n > capacity {
		stadoFree(resultPointer, capacity)
		return errors.New("host fact projection failed")
	}
	payload := append([]byte(nil), wasmBytes(resultPointer, n)...)
	stadoFree(resultPointer, capacity)
	if n == 0 {
		return errors.New("host fact projection returned no data")
	}
	return json.Unmarshal(payload, response)
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
