package main

import (
	"encoding/json"
	"errors"
	"strings"
)

type durableSuperviseState struct {
	Run   *runState
	Setup *setupState
}

func latestDurableSuperviseState(raw []byte) (durableSuperviseState, bool, error) {
	var projection struct {
		Journal []struct {
			Kind string          `json:"kind"`
			Data json.RawMessage `json:"data"`
		} `json:"journal"`
	}
	if err := json.Unmarshal(raw, &projection); err != nil {
		return durableSuperviseState{}, false, err
	}
	for index := len(projection.Journal) - 1; index >= 0; index-- {
		entry := projection.Journal[index]
		if strings.HasPrefix(entry.Kind, "setup.") {
			var setup setupState
			if err := decodeStrictBytes(entry.Data, &setup); err != nil {
				return durableSuperviseState{}, false, err
			}
			if err := setup.validate(); err != nil {
				return durableSuperviseState{}, false, err
			}
			return durableSuperviseState{Setup: &setup}, true, nil
		}
		if !runJournalKind(entry.Kind) {
			continue
		}
		var state runState
		if err := decodeStrictBytes(entry.Data, &state); err != nil {
			return durableSuperviseState{}, false, err
		}
		if state.Schema != policySchema {
			return durableSuperviseState{}, false, errors.New("unsupported durable supervise state schema")
		}
		return durableSuperviseState{Run: &state}, true, nil
	}
	return durableSuperviseState{}, false, nil
}

func runJournalKind(kind string) bool {
	return strings.HasPrefix(kind, "run.") || strings.HasPrefix(kind, "worker.") || strings.HasPrefix(kind, "review.") ||
		strings.HasPrefix(kind, "hold.") || strings.HasPrefix(kind, "steering.") || strings.HasPrefix(kind, "tool.") ||
		strings.HasPrefix(kind, "input.") || strings.HasPrefix(kind, "pivot.") || strings.HasPrefix(kind, "verification.") ||
		strings.HasSuffix(kind, ".requested")
}
