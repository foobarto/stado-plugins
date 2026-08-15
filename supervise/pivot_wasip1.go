//go:build wasip1

package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

type artifactCandidateAck struct {
	ID        string `json:"id"`
	Version   uint64 `json:"version"`
	Authority string `json:"authority"`
}

func (a *application) resumePivot() (proposalCommandResult, error) {
	pivot := a.state.PendingPivot
	if pivot == nil {
		return proposalCommandResult{}, errors.New("no pivot is pending")
	}
	switch pivot.Stage {
	case pivotStageReviewing:
		return proposalCommandResult{Status: "error", Message: "the structured pivot is still under current-anchor watchdog review"}, nil
	case pivotStageAwaitingUser:
		formatted, err := json.MarshalIndent(map[string]any{
			"classification": pivot.Classification, "rationale": pivot.Rationale,
			"current": baselineFromContract(a.contract), "replacement": pivot.Replacement,
		}, "", "  ")
		if err != nil {
			return proposalCommandResult{}, err
		}
		if _, err := callHostJSON(stadoUIRender, map[string]any{
			"title": "Proposed supervision pivot", "variant": "recommendation", "id": pivotRenderID(pivot.ReplacementDigest),
			"sections": []map[string]any{{"kind": "code", "heading": "Exact reviewed replacement", "code": map[string]string{"language": "json", "content": string(formatted)}}},
			"footer":   "This confirms quality-workflow configuration only. It grants no OS authority, capability, or security approval.",
		}); err != nil {
			return proposalCommandResult{}, err
		}
		title := []byte("Confirm exact supervision pivot")
		body := []byte("Select this exact reviewed replacement as the next session-scoped quality contract candidate. Contract/user-facing changes always require this confirmation; it grants no OS or plugin authority.")
		return a.confirmPivotWithUI(title, body)
	case pivotStageEditIntent:
		if err := a.reconcilePivotEdit(); err != nil {
			return proposalCommandResult{}, err
		}
		return proposalCommandResult{Status: "ok", Message: "selected the exact quality-confirmed supervision pivot and re-anchored the plan"}, nil
	case pivotStageReleasePending, pivotStageCleanupPending:
		if err := a.reconcilePivotEdit(); err != nil {
			return proposalCommandResult{}, err
		}
		return proposalCommandResult{Status: "ok", Message: "finished the exact durable pivot cleanup"}, nil
	default:
		return proposalCommandResult{}, fmt.Errorf("unknown durable pivot stage %q", pivot.Stage)
	}
}

func (a *application) confirmPivotWithUI(title, body []byte) (proposalCommandResult, error) {
	titlePointer, titleLength := slicePointer(title)
	bodyPointer, bodyLength := slicePointer(body)
	approved := stadoUIApprove(titlePointer, titleLength, bodyPointer, bodyLength)
	if approved < 0 {
		return proposalCommandResult{}, errors.New("quality confirmation UI is unavailable; pivot remains pending")
	}
	if approved == 0 {
		prior := clonePivotCandidate(a.state.PendingPivot)
		a.state.PendingPivot.Stage = pivotStageCleanupPending
		if err := a.persist("pivot.user_rejected", prior.ReviewEvidenceRefs); err != nil {
			a.state.PendingPivot = prior
			return proposalCommandResult{}, err
		}
		if a.state.Hold != nil {
			if err := a.execute(transition{Actions: []action{{Kind: actionReleaseHold, Reason: "operator rejected proposed quality pivot"}}}); err != nil {
				return proposalCommandResult{}, err
			}
		}
		if err := a.reconcilePivotEdit(); err != nil {
			return proposalCommandResult{}, err
		}
		return proposalCommandResult{Status: "error", Message: "pivot rejected; the current exact contract remains selected"}, nil
	}
	pivot := a.state.PendingPivot
	if pivot == nil || pivot.Stage != pivotStageAwaitingUser {
		return proposalCommandResult{}, errors.New("pivot confirmation is no longer current")
	}
	pivot.Stage, pivot.EditExpectedVersion = pivotStageEditIntent, a.state.ContractVersion
	if err := a.persist("pivot.edit_intent", pivot.ReviewEvidenceRefs); err != nil {
		return proposalCommandResult{}, err
	}
	if err := a.reconcilePivotEdit(); err != nil {
		return proposalCommandResult{}, err
	}
	return proposalCommandResult{Status: "ok", Message: "selected the exact quality-confirmed supervision pivot and re-anchored the plan"}, nil
}

func (a *application) reconcilePivotEdit() error {
	pivot := a.state.PendingPivot
	if !a.loaded || pivot == nil {
		return nil
	}
	if pivot.Stage == pivotStageReleasePending || pivot.Stage == pivotStageCleanupPending {
		if a.state.Hold != nil {
			if err := a.execute(transition{Actions: []action{{Kind: actionReleaseHold, Reason: "durable pivot transition resolved"}}}); err != nil {
				return err
			}
		}
		prior := clonePivotCandidate(a.state.PendingPivot)
		a.state.PendingPivot = nil
		if err := a.persist("pivot.finished", prior.ReviewEvidenceRefs); err != nil {
			a.state.PendingPivot = prior
			return err
		}
		return nil
	}
	if pivot.Stage != pivotStageEditIntent {
		return nil
	}
	if pivot.EditExpectedVersion != a.state.ContractVersion || pivot.EditExpectedVersion != a.contract.ArtifactVersion || a.contract.ArtifactID != a.state.ContractID {
		return errors.New("pivot edit intent no longer matches the exact selected artifact version")
	}
	expected := pivot.Replacement.contract(a.state.RunID, a.state.Config)
	expected.ArtifactID, expected.ArtifactVersion = a.state.ContractID, pivot.EditExpectedVersion+1
	selected, queryErr := querySelectedContract(expected.ArtifactID, expected.ArtifactVersion)
	if queryErr != nil {
		raw, editErr := callHostJSON(stadoArtifactEdit, map[string]any{
			"kind": "supervision-contract", "id": a.state.ContractID, "expected_version": pivot.EditExpectedVersion,
			"tags": []string{"quality:supervision"}, "groups": []string{"stado/supervision"},
			"evidence_refs": pivot.ReviewEvidenceRefs, "data": expected,
		})
		if editErr == nil {
			var ack artifactCandidateAck
			if err := json.Unmarshal(raw, &ack); err != nil || ack.ID != expected.ArtifactID || ack.Version != expected.ArtifactVersion || ack.Authority != "candidate" {
				return errors.New("artifact broker returned an invalid edited supervision candidate")
			}
		}
		selected, queryErr = querySelectedContract(expected.ArtifactID, expected.ArtifactVersion)
		if queryErr != nil {
			if editErr != nil {
				return fmt.Errorf("edit pivot candidate: %v; exact recovery query: %w", editErr, queryErr)
			}
			return fmt.Errorf("revalidate edited pivot candidate: %w", queryErr)
		}
	}
	matches, err := sameContractData(expected, selected)
	if err != nil || !matches {
		return errors.New("edited supervision candidate does not preserve the exact reviewed replacement")
	}
	previous := a.contract
	if err := a.state.applySelectedPivot(previous, selected); err != nil {
		return err
	}
	a.contract = selected
	if err := a.persist("pivot.applied", append([]string{"artifact:" + selected.ArtifactID + fmt.Sprintf("@%d", selected.ArtifactVersion)}, pivot.ReviewEvidenceRefs...)); err != nil {
		return err
	}
	return a.reconcilePivotEdit()
}
