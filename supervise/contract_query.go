package main

import (
	"bytes"
	"encoding/json"
	"errors"
)

func sameContractData(first, second supervisionContract) (bool, error) {
	left, err := json.Marshal(first)
	if err != nil {
		return false, err
	}
	right, err := json.Marshal(second)
	if err != nil {
		return false, err
	}
	return bytes.Equal(left, right), nil
}

type contractQueryResponse struct {
	Items []struct {
		ID        string          `json:"id"`
		Version   uint64          `json:"version"`
		Authority string          `json:"authority"`
		Data      json.RawMessage `json:"data"`
	} `json:"items"`
}

func selectedContractQueryRequest(artifactID string, artifactVersion uint64) (map[string]any, error) {
	if artifactID == "" || artifactVersion == 0 {
		return nil, errors.New("selected supervision contract reference is unavailable")
	}
	const qualifiedKind = "self#supervision-contract"
	return map[string]any{
		"kinds":     []string{qualifiedKind},
		"refs":      []map[string]any{{"id": artifactID, "version": artifactVersion}},
		"max_items": 1,
	}, nil
}

func selectExactContractCandidate(raw []byte, artifactID string, artifactVersion uint64) (supervisionContract, error) {
	if artifactID == "" || artifactVersion == 0 {
		return supervisionContract{}, errors.New("selected supervision contract reference is unavailable")
	}
	var response contractQueryResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return supervisionContract{}, err
	}
	for _, item := range response.Items {
		if item.ID != artifactID || item.Version != artifactVersion || item.Authority != "candidate" {
			continue
		}
		var contract supervisionContract
		if err := decodeStrictBytes(item.Data, &contract); err != nil {
			return supervisionContract{}, err
		}
		contract.ArtifactID, contract.ArtifactVersion = item.ID, item.Version
		if err := contract.validate(); err != nil {
			return supervisionContract{}, err
		}
		return contract, nil
	}
	return supervisionContract{}, errors.New("exact selected supervision contract candidate is unavailable")
}
