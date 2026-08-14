package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

type applicationJournalAck struct {
	ID           string          `json:"id"`
	SessionID    string          `json:"session_id"`
	Generation   uint64          `json:"generation"`
	PluginID     string          `json:"plugin_id"`
	RunID        string          `json:"run_id"`
	Sequence     uint64          `json:"sequence"`
	WALSequence  uint64          `json:"wal_sequence"`
	Kind         string          `json:"kind"`
	Summary      string          `json:"summary"`
	Data         json.RawMessage `json:"data,omitempty"`
	EvidenceRefs []string        `json:"evidence_refs,omitempty"`
	CreatedAt    string          `json:"created_at"`
}

func validateJournalAck(raw []byte, anchor lifecycleIdentity, runID, kind, summary string, data []byte, evidence []string) error {
	var ack applicationJournalAck
	if err := decodeStrictBytes(raw, &ack); err != nil {
		return err
	}
	if !boundedRequired(ack.ID, 256) || ack.SessionID != anchor.SessionID || ack.Generation != anchor.SessionGeneration ||
		!boundedRequired(ack.PluginID, 512) || ack.RunID != runID || ack.Sequence == 0 || ack.WALSequence == 0 ||
		ack.Kind != kind || ack.Summary != summary || !slices.Equal(ack.EvidenceRefs, evidence) {
		return errors.New("application journal acknowledgement does not match the exact append")
	}
	if _, err := time.Parse(time.RFC3339Nano, ack.CreatedAt); err != nil {
		return errors.New("application journal acknowledgement has an invalid timestamp")
	}
	equal, err := equalJSON(ack.Data, data)
	if err != nil {
		return err
	}
	if !equal {
		return errors.New("application journal acknowledgement changed durable state data")
	}
	return nil
}

type lifecycleIdentity struct {
	SessionID         string
	SessionGeneration uint64
}

func equalJSON(first, second []byte) (bool, error) {
	var compactFirst, compactSecond bytes.Buffer
	if err := json.Compact(&compactFirst, first); err != nil {
		return false, fmt.Errorf("compact acknowledged JSON: %w", err)
	}
	if err := json.Compact(&compactSecond, second); err != nil {
		return false, fmt.Errorf("compact expected JSON: %w", err)
	}
	return bytes.Equal(compactFirst.Bytes(), compactSecond.Bytes()), nil
}
