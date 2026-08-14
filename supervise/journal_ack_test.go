package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func journalAckFixture(t *testing.T, data []byte) []byte {
	t.Helper()
	raw, err := json.Marshal(applicationJournalAck{
		ID: "journal-1", SessionID: "session-1", Generation: 4, PluginID: "github.com/foobarto/stado-plugins/supervise",
		RunID: "run-1", Sequence: 2, WALSequence: 9, Kind: "setup.configured",
		Summary: "supervise application transition: setup.configured", Data: data,
		EvidenceRefs: []string{"evidence:1"}, CreatedAt: time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestJournalAcknowledgementMustEchoExactDurableTransition(t *testing.T) {
	data := []byte(`{"schema":"state/v1","step":1}`)
	raw := journalAckFixture(t, data)
	if err := validateJournalAck(raw, lifecycleIdentity{SessionID: "session-1", SessionGeneration: 4}, "run-1", "setup.configured", "supervise application transition: setup.configured", data, []string{"evidence:1"}); err != nil {
		t.Fatal(err)
	}
	for name, invalid := range map[string][]byte{
		"wrong session": bytes.ReplaceAll(raw, []byte(`"session_id":"session-1"`), []byte(`"session_id":"session-2"`)),
		"wrong data":    bytes.ReplaceAll(raw, []byte(`"step":1`), []byte(`"step":2`)),
		"unknown field": bytes.ReplaceAll(raw, []byte(`"id":"journal-1"`), []byte(`"id":"journal-1","authority":"guest"`)),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateJournalAck(invalid, lifecycleIdentity{SessionID: "session-1", SessionGeneration: 4}, "run-1", "setup.configured", "supervise application transition: setup.configured", data, []string{"evidence:1"}); err == nil {
				t.Fatal("mismatched journal acknowledgement was accepted")
			}
		})
	}
}
