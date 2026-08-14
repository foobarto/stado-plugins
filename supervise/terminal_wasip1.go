//go:build wasip1

package main

import (
	"errors"
	"time"
)

// finishSetupCancellationCleanup keeps a cancelled setup fenced until the
// exact baseline child cancellation request is accepted and that fact is
// journalled. A failed callback therefore cannot silently turn an active child
// into a dormant application.
func (a *application) finishSetupCancellationCleanup() error {
	if a.setup == nil || a.setup.Phase != setupPhaseCancelled || a.setup.CancellationCleaned {
		return nil
	}
	if a.setup.CancellationAgentID != "" {
		if _, err := callHostJSON(stadoAgentCancel, map[string]string{"id": a.setup.CancellationAgentID}); err != nil {
			return err
		}
	}
	priorID := a.setup.CancellationAgentID
	a.setup.CancellationAgentID = ""
	a.setup.CancellationCleaned = true
	if err := a.persistSetup("setup.cancel_cleanup_complete", nil); err != nil {
		a.setup.CancellationAgentID = priorID
		a.setup.CancellationCleaned = false
		return err
	}
	return nil
}

// finishRunCancellationCleanup orders the exact application-owned cleanup
// boundaries. The worker CAS is already terminal before cancelWorkflow is
// journalled; the remaining reviewer cancellation and hold release must both
// succeed before the run can become dormant.
func (a *application) finishRunCancellationCleanup() error {
	if !a.loaded || !a.state.Cancelled {
		return nil
	}
	if a.state.CancellationAgentID != "" {
		if _, err := callHostJSON(stadoAgentCancel, map[string]string{"id": a.state.CancellationAgentID}); err != nil {
			return err
		}
		priorID := a.state.CancellationAgentID
		a.state.CancellationAgentID = ""
		if err := a.persist("run.cancel_review_cleared", nil); err != nil {
			a.state.CancellationAgentID = priorID
			return err
		}
	}
	if err := a.reconcileHoldAndCompletion(time.Now().UTC()); err != nil {
		return err
	}
	return a.markCancellationCleaned()
}

func (a *application) markCancellationCleaned() error {
	if !a.loaded || !a.state.Cancelled || a.state.CancellationCleaned {
		return nil
	}
	if a.state.CancellationAgentID != "" || a.state.Hold != nil || a.state.PendingReview != nil || !workerRunTerminal(a.state.WorkerRunStatus) {
		return errors.New("cancelled workflow still has an active hold, reviewer, or worker recurrence")
	}
	a.state.CancellationCleaned = true
	if err := a.persist("run.cancel_cleanup_complete", nil); err != nil {
		a.state.CancellationCleaned = false
		return err
	}
	return nil
}
