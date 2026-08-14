//go:build wasip1

package main

import (
	"encoding/json"
	"sync"
	"unsafe"
)

func main() {}

//go:wasmimport stado stado_context_resource_catalog
func stadoContextResourceCatalog(reqPtr, reqLen, outPtr, outCap uint32) int32

//go:wasmimport stado stado_context_resource_open
func stadoContextResourceOpen(reqPtr, reqLen, outPtr, outCap uint32) int32

//go:wasmimport stado stado_registry_catalog
func stadoRegistryCatalog(reqPtr, reqLen, outPtr, outCap uint32) int32

//go:wasmimport stado stado_session_tool_surface_apply
func stadoSessionToolSurfaceApply(reqPtr, reqLen, outPtr, outCap uint32) int32

var pinned sync.Map

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 {
	if size <= 0 {
		return 0
	}
	buf := make([]byte, size)
	ptr := uintptr(unsafe.Pointer(&buf[0]))
	pinned.Store(ptr, buf)
	return int32(ptr)
}

//go:wasmexport stado_free
func stadoFree(ptr int32, _ int32) { pinned.Delete(uintptr(ptr)) }

type wasmSkillHost struct{}

func (wasmSkillHost) Catalog(request resourceCatalogRequest) (resourceCatalogResponse, error) {
	var response resourceCatalogResponse
	err := callHostJSON(stadoContextResourceCatalog, request, &response, 1<<20)
	return response, err
}

func (wasmSkillHost) Open(request resourceOpenRequest) (resourceOpenResponse, error) {
	var response resourceOpenResponse
	err := callHostJSON(stadoContextResourceOpen, request, &response, 1<<20)
	return response, err
}

func (wasmSkillHost) Registry(request registryRequest) (registryResponse, error) {
	var response registryResponse
	err := callHostJSON(stadoRegistryCatalog, request, &response, 1<<20)
	return response, err
}

func (wasmSkillHost) Apply(request surfaceRequest) error {
	var response any
	return callHostJSON(stadoSessionToolSurfaceApply, request, &response, 1<<20)
}

func callHostJSON(call func(uint32, uint32, uint32, uint32) int32, request, response any, maxCapacity int32) error {
	return callHostJSONProtocol(request, response, maxCapacity, func(raw []byte, capacity int32) (int32, []byte) {
		reqPtr := stadoAlloc(int32(len(raw)))
		defer stadoFree(reqPtr, int32(len(raw)))
		copy(wasmBytes(reqPtr, int32(len(raw))), raw)
		outPtr := stadoAlloc(capacity)
		defer stadoFree(outPtr, capacity)
		n := call(uint32(reqPtr), uint32(len(raw)), uint32(outPtr), uint32(capacity))
		length := n
		if length < 0 {
			length = -length
		}
		if length < 0 || length > capacity {
			return n, nil
		}
		return n, append([]byte(nil), wasmBytes(outPtr, length)...)
	})
}

func dispatch(name string, argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	result, err := runTool(wasmSkillHost{}, name, append([]byte(nil), wasmBytes(argsPtr, argsLen)...))
	if err != nil {
		return writeError(resultPtr, resultCap, err.Error())
	}
	if len(result) > maxToolResultBytes {
		return writeError(resultPtr, resultCap, "tool result exceeds 1 MiB")
	}
	return writeResult(resultPtr, resultCap, result)
}

//go:wasmexport stado_tool_search
func stadoToolSearch(a, b, c, d int32) int32 { return dispatch("skills__search", a, b, c, d) }

//go:wasmexport stado_tool_load
func stadoToolLoad(a, b, c, d int32) int32 { return dispatch("skills__load", a, b, c, d) }

func wasmBytes(ptr, size int32) []byte {
	if ptr == 0 || size <= 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), int(size))
}

func writeResult(ptr, capacity int32, payload []byte) int32 {
	if capacity <= 0 || int32(len(payload)) > capacity {
		return 0
	}
	copy(wasmBytes(ptr, capacity), payload)
	return int32(len(payload))
}

func writeError(ptr, capacity int32, message string) int32 {
	payload, _ := json.Marshal(map[string]string{"error": message})
	n := writeResult(ptr, capacity, payload)
	if n == 0 {
		return -1
	}
	return -n
}
