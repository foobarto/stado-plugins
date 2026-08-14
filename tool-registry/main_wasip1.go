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

type wasmRegistryHost struct{}

func (wasmRegistryHost) Catalog(request catalogRequest) (catalogResponse, error) {
	var response catalogResponse
	err := callHostJSON(stadoRegistryCatalog, request, &response)
	return response, err
}

func (wasmRegistryHost) Apply(request surfaceRequest) ([]byte, error) {
	var response json.RawMessage
	err := callHostJSON(stadoSessionToolSurfaceApply, request, &response)
	return response, err
}

func callHostJSON(call func(uint32, uint32, uint32, uint32) int32, request, response any) error {
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	reqPtr := stadoAlloc(int32(len(raw)))
	defer stadoFree(reqPtr, int32(len(raw)))
	copy(wasmBytes(reqPtr, int32(len(raw))), raw)
	// Host imports return a negative tool-side error when the response buffer
	// is too small; they do not return a required positive length. Allocate the
	// documented bounded ceiling once so a valid large catalog page cannot be
	// mistaken for a host failure.
	capacity := int32(1 << 20)
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
		return fmt.Errorf("host import failed: %s", message)
	}
	if n == 0 || n > capacity {
		stadoFree(outPtr, capacity)
		return errors.New("host import returned an invalid response length")
	}
	payload := append([]byte(nil), wasmBytes(outPtr, n)...)
	stadoFree(outPtr, capacity)
	return json.Unmarshal(payload, response)
}

func dispatch(name string, argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	result, err := runTool(wasmRegistryHost{}, name, append([]byte(nil), wasmBytes(argsPtr, argsLen)...))
	if err != nil {
		return writeError(resultPtr, resultCap, err.Error())
	}
	if len(result) > 1<<20 {
		return writeError(resultPtr, resultCap, "tool result exceeds 1 MiB; narrow the query or requested name set")
	}
	return writeResult(resultPtr, resultCap, result)
}

//go:wasmexport stado_tool_search
func stadoToolSearch(a, b, c, d int32) int32 { return dispatch("tools__search", a, b, c, d) }

//go:wasmexport stado_tool_describe
func stadoToolDescribe(a, b, c, d int32) int32 { return dispatch("tools__describe", a, b, c, d) }

//go:wasmexport stado_tool_categories
func stadoToolCategories(a, b, c, d int32) int32 { return dispatch("tools__categories", a, b, c, d) }

//go:wasmexport stado_tool_in_category
func stadoToolInCategory(a, b, c, d int32) int32 { return dispatch("tools__in_category", a, b, c, d) }

//go:wasmexport stado_tool_activate
func stadoToolActivate(a, b, c, d int32) int32 { return dispatch("tools__activate", a, b, c, d) }

//go:wasmexport stado_tool_deactivate
func stadoToolDeactivate(a, b, c, d int32) int32 { return dispatch("tools__deactivate", a, b, c, d) }

//go:wasmexport stado_tool_plugin_load
func stadoToolPluginLoad(a, b, c, d int32) int32 { return dispatch("plugin__load", a, b, c, d) }

//go:wasmexport stado_tool_plugin_unload
func stadoToolPluginUnload(a, b, c, d int32) int32 { return dispatch("plugin__unload", a, b, c, d) }

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
