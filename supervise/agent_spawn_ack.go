package main

import "errors"

type asyncAgentSpawnAck struct {
	ID        string                 `json:"id"`
	SessionID string                 `json:"session_id,omitempty"`
	Status    string                 `json:"status"`
	FinalText string                 `json:"final_text,omitempty"`
	Terminal  *agentTerminalMetadata `json:"terminal,omitempty"`
}

func decodeAsyncAgentSpawnAck(raw []byte) (asyncAgentSpawnAck, error) {
	var ack asyncAgentSpawnAck
	if err := decodeStrictBytes(raw, &ack); err != nil {
		return asyncAgentSpawnAck{}, err
	}
	if !boundedRequired(ack.ID, 256) || len(ack.SessionID) > 256 || ack.Status != "running" || ack.FinalText != "" || ack.Terminal != nil {
		return asyncAgentSpawnAck{}, errors.New("agent host returned an invalid asynchronous child acknowledgement")
	}
	return ack, nil
}
