package main

import "testing"

func TestAsyncAgentSpawnAcknowledgementIsStrict(t *testing.T) {
	if ack, err := decodeAsyncAgentSpawnAck([]byte(`{"id":"agent-1","session_id":"child-1","status":"running"}`)); err != nil || ack.ID != "agent-1" {
		t.Fatalf("valid spawn acknowledgement: ack=%+v err=%v", ack, err)
	}
	for _, raw := range []string{
		`{"id":"agent-1","status":"completed"}`,
		`{"id":"agent-1","status":"running","final_text":"already done"}`,
		`{"id":"agent-1","status":"running","authority":"guest"}`,
		`{"status":"running"}`,
	} {
		if _, err := decodeAsyncAgentSpawnAck([]byte(raw)); err == nil {
			t.Fatalf("invalid spawn acknowledgement accepted: %s", raw)
		}
	}
}
