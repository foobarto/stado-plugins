package main

import (
	"errors"
	"fmt"
	"strings"
)

type proposalCommandResult struct {
	Status            string `json:"status"`
	Message           string `json:"message,omitempty"`
	WorkerRunID       string `json:"worker_run_id,omitempty"`
	ResumeWorkerRunID string `json:"resume_worker_run_id,omitempty"`
}

func newProposalCommandResult(artifactID string, expectedVersion uint64) (proposalCommandResult, error) {
	if strings.TrimSpace(artifactID) == "" || len(artifactID) > 512 || expectedVersion == 0 {
		return proposalCommandResult{}, errors.New("invalid supervision candidate reference")
	}
	return proposalCommandResult{
		Status:  "ok",
		Message: fmt.Sprintf("selected session-scoped supervision contract candidate %s v%d", artifactID, expectedVersion),
	}, nil
}
