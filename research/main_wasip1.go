//go:build wasip1

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"unsafe"
)

func main() {}

//go:wasmimport stado stado_agent_spawn
func stadoAgentSpawn(reqPtr, reqLen, outPtr, outCap uint32) int32

//go:wasmimport stado stado_evidence_catalog
func stadoEvidenceCatalog(reqPtr, reqLen, outPtr, outCap uint32) int32

//go:wasmimport stado stado_evidence_search
func stadoEvidenceSearch(reqPtr, reqLen, outPtr, outCap uint32) int32

//go:wasmimport stado stado_evidence_open
func stadoEvidenceOpen(reqPtr, reqLen, outPtr, outCap uint32) int32

//go:wasmimport stado stado_evidence_validate
func stadoEvidenceValidate(reqPtr, reqLen, outPtr, outCap uint32) int32

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

type wasmResearchHost struct{}

func (wasmResearchHost) Spawn(request spawnRequest) (spawnResult, error) {
	var result spawnResult
	err := callHostJSON(stadoAgentSpawn, request, &result)
	return result, err
}

func (wasmResearchHost) Evidence(operation string, request any, response any) error {
	call := stadoEvidenceCatalog
	switch operation {
	case "catalog":
	case "search":
		call = stadoEvidenceSearch
	case "open":
		call = stadoEvidenceOpen
	case "validate":
		call = stadoEvidenceValidate
	default:
		return errors.New("unsupported evidence operation")
	}
	return callHostJSON(call, request, response)
}

func callHostJSON(call func(uint32, uint32, uint32, uint32) int32, request, response any) error {
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	reqPtr := stadoAlloc(int32(len(raw)))
	defer stadoFree(reqPtr, int32(len(raw)))
	copy(wasmBytes(reqPtr, int32(len(raw))), raw)
	// Host imports encode a too-small output buffer as a negative tool-side
	// error; they do not return the required length. Allocate the package's
	// signed one-megabyte ceiling once so valid large evidence bodies are not
	// mistaken for failures and no retry depends on an ABI guarantee that does
	// not exist.
	capacity := int32(maxHostResult)
	outPtr := stadoAlloc(capacity)
	n := call(uint32(reqPtr), uint32(len(raw)), uint32(outPtr), uint32(capacity))
	if n < 0 {
		if n < -capacity {
			stadoFree(outPtr, capacity)
			return errors.New("host import returned an invalid error length")
		}
		message := append([]byte(nil), wasmBytes(outPtr, -n)...)
		stadoFree(outPtr, capacity)
		var envelope struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(message, &envelope) == nil && envelope.Error != "" {
			return errors.New(envelope.Error)
		}
		return fmt.Errorf("host import refused request")
	}
	if n == 0 || n > capacity {
		stadoFree(outPtr, capacity)
		return errors.New("host import returned an invalid response length")
	}
	payload := append([]byte(nil), wasmBytes(outPtr, n)...)
	stadoFree(outPtr, capacity)
	return decodeStrict(payload, response)
}

func dispatch(run func(researchHost, []byte) ([]byte, error), argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	result, err := run(wasmResearchHost{}, append([]byte(nil), wasmBytes(argsPtr, argsLen)...))
	if err != nil {
		return writeError(resultPtr, resultCap, err.Error())
	}
	return writeResult(resultPtr, resultCap, result)
}

//go:wasmexport stado_tool_memory_research
func stadoToolMemoryResearch(a, b, c, d int32) int32 {
	return dispatch(func(h researchHost, raw []byte) ([]byte, error) { return runResearch(h, "artifact", raw) }, a, b, c, d)
}

//go:wasmexport stado_tool_session_research
func stadoToolSessionResearch(a, b, c, d int32) int32 {
	return dispatch(func(h researchHost, raw []byte) ([]byte, error) { return runResearch(h, "session", raw) }, a, b, c, d)
}

//go:wasmexport stado_tool_artifact_catalog
func stadoToolArtifactCatalog(a, b, c, d int32) int32 {
	return dispatch(func(h researchHost, raw []byte) ([]byte, error) { return runCatalog(h, "artifact", raw) }, a, b, c, d)
}

//go:wasmexport stado_tool_artifact_search
func stadoToolArtifactSearch(a, b, c, d int32) int32 {
	return dispatch(func(h researchHost, raw []byte) ([]byte, error) { return runSearch(h, "artifact", raw) }, a, b, c, d)
}

//go:wasmexport stado_tool_artifact_open
func stadoToolArtifactOpen(a, b, c, d int32) int32 {
	return dispatch(func(h researchHost, raw []byte) ([]byte, error) { return runOpen(h, "artifact", raw) }, a, b, c, d)
}

//go:wasmexport stado_tool_session_catalog
func stadoToolSessionCatalog(a, b, c, d int32) int32 {
	return dispatch(func(h researchHost, raw []byte) ([]byte, error) { return runCatalog(h, "session", raw) }, a, b, c, d)
}

//go:wasmexport stado_tool_session_search
func stadoToolSessionSearch(a, b, c, d int32) int32 {
	return dispatch(func(h researchHost, raw []byte) ([]byte, error) { return runSearch(h, "session", raw) }, a, b, c, d)
}

//go:wasmexport stado_tool_session_open
func stadoToolSessionOpen(a, b, c, d int32) int32 {
	return dispatch(func(h researchHost, raw []byte) ([]byte, error) { return runOpen(h, "session", raw) }, a, b, c, d)
}

func wasmBytes(pointer, size int32) []byte {
	if pointer == 0 || size <= 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(pointer))), int(size))
}

func writeResult(pointer, capacity int32, payload []byte) int32 {
	if capacity <= 0 || int32(len(payload)) > capacity {
		return 0
	}
	copy(wasmBytes(pointer, capacity), payload)
	return int32(len(payload))
}

func writeError(pointer, capacity int32, message string) int32 {
	payload, _ := json.Marshal(map[string]string{"error": message})
	n := writeResult(pointer, capacity, payload)
	if n <= 0 {
		return -1
	}
	return -n
}
