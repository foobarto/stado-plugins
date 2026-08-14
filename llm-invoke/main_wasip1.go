//go:build wasip1

package main

import (
	"fmt"
	"sync"
	"unsafe"
)

func main() {}

//go:wasmimport stado stado_provider_invoke
func stadoProviderInvoke(reqPtr, reqLen, outPtr, outCap uint32) int32

//go:wasmimport stado stado_log
func stadoLog(levelPtr, levelLen, msgPtr, msgLen uint32)

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
func stadoFree(pointer int32, _ int32) {
	pinned.Delete(uintptr(pointer))
}

//go:wasmexport stado_tool_invoke
func stadoToolInvoke(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	if argsLen <= 0 || resultCap <= 0 {
		return writeToolError(resultPtr, resultCap, "prompt is required")
	}
	args := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(argsPtr))), int(argsLen))
	outcome, err := invokeTool(args, callProvider)
	if err != nil {
		return writeToolError(resultPtr, resultCap, err.Error())
	}
	if outcome.Cleanup != nil {
		logMessage("warn", fmt.Sprintf("provider cleanup diagnostic kind=%s fingerprint=%s", outcome.Cleanup.Kind, outcome.Cleanup.Fingerprint))
	}
	return writeResult(resultPtr, resultCap, []byte(outcome.Text))
}

func callProvider(request []byte) ([]byte, error) {
	output := make([]byte, maxProviderFactsBytes)
	n := stadoProviderInvoke(
		uint32(uintptr(unsafe.Pointer(&request[0]))), uint32(len(request)),
		uint32(uintptr(unsafe.Pointer(&output[0]))), uint32(len(output)),
	)
	return copyBoundedProviderFacts(output, n)
}

func writeToolError(resultPtr, resultCap int32, message string) int32 {
	envelope, err := encodeToolError(message)
	if err != nil {
		return -1
	}
	n := writeResult(resultPtr, resultCap, envelope)
	if n <= 0 {
		return -1
	}
	return -n
}

func writeResult(resultPtr, resultCap int32, payload []byte) int32 {
	if int32(len(payload)) > resultCap {
		return -1
	}
	destination := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(resultPtr))), int(resultCap))
	copy(destination, payload)
	return int32(len(payload))
}

func logMessage(level, message string) {
	levelBytes := []byte(level)
	messageBytes := []byte(message)
	stadoLog(
		uint32(uintptr(unsafe.Pointer(&levelBytes[0]))), uint32(len(levelBytes)),
		uint32(uintptr(unsafe.Pointer(&messageBytes[0]))), uint32(len(messageBytes)),
	)
}
