package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	operatorInputSchema           = "stado.dev/operator-input/v1"
	operatorInputDispositionGive  = "deliver"
	operatorInputDispositionLater = "defer"
	operatorInputPhaseClaiming    = "claiming"
	operatorInputPhaseReviewing   = "reviewing"
	operatorInputPhaseRouting     = "routing"
	operatorInputPhaseOverflow    = "overflow_pause"
	operatorInputPhaseRouted      = "routed"
	operatorInputPhaseRecovered   = "terminal_recovered"
	reviewPurposeOperatorInput    = "operator_input"
	maxOperatorInputBytes         = 48 << 10
	maxOperatorInputRoutes        = 16
	maxDeferredInputs             = 64
)

// operatorInputQueuedFact is an immutable broker observation. Command/UI
// provenance is quality context only; neither the text nor its origin grants
// authority to this application.
type operatorInputQueuedFact struct {
	Schema  string `json:"schema"`
	InputID string `json:"input_id"`
	RunID   string `json:"run_id"`
	Version uint64 `json:"version"`
	Ordinal uint64 `json:"ordinal"`
	Text    string `json:"text"`
	Digest  string `json:"digest"`
}

// operatorInputRouteState is the durable application-owned review job. The
// original text stays broker-owned and is recovered from the reviewing
// projection after rebind; retaining only its digest keeps the journal bounded.
type operatorInputRouteState struct {
	InputID         string                     `json:"input_id"`
	PluginID        string                     `json:"plugin_id"`
	RunID           string                     `json:"run_id"`
	ExpectedVersion uint64                     `json:"expected_version"`
	Ordinal         uint64                     `json:"ordinal"`
	Digest          string                     `json:"digest"`
	ReviewID        string                     `json:"review_id"`
	ReviewAnchor    anchor                     `json:"review_anchor"`
	ClaimKey        string                     `json:"claim_idempotency_key"`
	SpawnKey        string                     `json:"spawn_idempotency_key"`
	RouteKey        string                     `json:"route_idempotency_key"`
	Phase           string                     `json:"phase"`
	AgentID         string                     `json:"agent_id,omitempty"`
	AgentOffset     int                        `json:"agent_offset,omitempty"`
	TokenBudget     int                        `json:"token_budget,omitempty"`
	Terminal        *reviewTerminalObservation `json:"terminal,omitempty"`
	ReviewFailures  int                        `json:"review_failures,omitempty"`
	ReviewDeadline  time.Time                  `json:"review_deadline,omitempty"`
	RetryAt         time.Time                  `json:"retry_at,omitempty"`
	LastFailure     string                     `json:"last_failure,omitempty"`
	Disposition     string                     `json:"disposition,omitempty"`
	Label           string                     `json:"label,omitempty"`
	Rationale       string                     `json:"rationale,omitempty"`
	RoutedVersion   uint64                     `json:"routed_version,omitempty"`
	TaskID          string                     `json:"task_id,omitempty"`
	OverflowPaused  bool                       `json:"overflow_pause_requested,omitempty"`
	ObsoleteAgentID string                     `json:"obsolete_completion_agent_id,omitempty"`
	ReleaseHold     bool                       `json:"release_completion_hold,omitempty"`
}

type deferredInputState struct {
	InputID string `json:"input_id"`
	RunID   string `json:"run_id"`
	Ordinal uint64 `json:"ordinal"`
	Digest  string `json:"digest"`
	TaskID  string `json:"task_id"`
}

// operatorInputRecord is the exact broker record returned by claim/route and
// by projection.read for reviewing inputs.
type operatorInputRecord struct {
	ID              string    `json:"id"`
	SessionID       string    `json:"session_id"`
	Generation      uint64    `json:"generation"`
	PluginID        string    `json:"plugin_id"`
	RunID           string    `json:"run_id"`
	Ordinal         uint64    `json:"ordinal"`
	Version         uint64    `json:"version"`
	WALSequence     uint64    `json:"wal_sequence"`
	Text            string    `json:"text"`
	Digest          string    `json:"digest"`
	Status          string    `json:"status"`
	Label           string    `json:"label,omitempty"`
	Rationale       string    `json:"rationale,omitempty"`
	ReviewID        string    `json:"review_id,omitempty"`
	TaskID          string    `json:"task_id,omitempty"`
	ReceiverInputID string    `json:"receiver_input_id,omitempty"`
	Recovered       bool      `json:"recovered,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type operatorInputClassification struct {
	ReviewID    string `json:"review_id"`
	InputID     string `json:"input_id"`
	Anchor      anchor `json:"anchor"`
	Disposition string `json:"disposition"`
	Label       string `json:"label"`
	Rationale   string `json:"rationale"`
}

type operatorInputReviewerResult struct {
	Classification operatorInputClassification `json:"classification"`
}

func decodeOperatorInputQueued(raw []byte) (operatorInputQueuedFact, error) {
	var fact operatorInputQueuedFact
	if err := decodeStrictBytes(raw, &fact); err != nil {
		return operatorInputQueuedFact{}, err
	}
	if fact.Schema != operatorInputSchema || !boundedRequired(fact.InputID, 256) || !boundedRequired(fact.RunID, 256) || fact.Version != 1 || fact.Ordinal == 0 || !validOperatorInputText(fact.Text) || fact.Digest != digestOperatorInput(fact.Text) {
		return operatorInputQueuedFact{}, errors.New("invalid immutable operator-input fact")
	}
	return fact, nil
}

func decodeOperatorInputRecord(raw []byte) (operatorInputRecord, error) {
	var record operatorInputRecord
	if err := decodeStrictBytes(raw, &record); err != nil {
		return operatorInputRecord{}, err
	}
	if !boundedRequired(record.ID, 256) || !boundedRequired(record.SessionID, 256) || record.Generation == 0 || !boundedRequired(record.PluginID, 512) || !boundedRequired(record.RunID, 256) || record.Ordinal == 0 || record.Version == 0 || record.WALSequence == 0 || !validOperatorInputText(record.Text) || record.Digest != digestOperatorInput(record.Text) || record.CreatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) {
		return operatorInputRecord{}, errors.New("invalid broker operator-input record")
	}
	return record, nil
}

func decodeOperatorInputReviewerResult(raw []byte, route *operatorInputRouteState) (operatorInputReviewerResult, error) {
	var result operatorInputReviewerResult
	if route == nil || route.Phase != operatorInputPhaseReviewing {
		return result, errors.New("no exact operator-input review is pending")
	}
	if err := decodeStrictBytes(raw, &result); err != nil {
		return operatorInputReviewerResult{}, err
	}
	value := result.Classification
	if value.ReviewID != route.ReviewID || value.InputID != route.InputID || value.Anchor != route.ReviewAnchor ||
		(value.Disposition != operatorInputDispositionGive && value.Disposition != operatorInputDispositionLater) ||
		!boundedRequired(value.Label, 256) || !boundedRequired(value.Rationale, 4096) {
		return operatorInputReviewerResult{}, errors.New("operator-input reviewer returned an invalid exact classification")
	}
	return result, nil
}

func detectOperatorInputReviewerResult(messages []agentMessage, route *operatorInputRouteState) (operatorInputReviewerResult, bool) {
	var valid operatorInputReviewerResult
	found := false
	for _, message := range messages {
		if message.Role != "assistant" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		candidate, err := decodeOperatorInputReviewerResult([]byte(strings.TrimSpace(message.Content)), route)
		if err == nil {
			valid, found = candidate, true
		}
	}
	return valid, found
}

// consumeOperatorInputReviewerMessages waits for the authenticated terminal
// fact before applying semantic output. Cleanup is diagnostic metadata and
// cannot erase an otherwise valid classification. Any failed or uncertain
// result conservatively defers the immutable original.
func (s *runState) consumeOperatorInputReviewerMessages(route *operatorInputRouteState, messages agentMessages) (bool, error) {
	if s == nil || route == nil || route.Phase != operatorInputPhaseReviewing {
		return false, errors.New("no operator-input reviewer result is pending")
	}
	if !isTerminalAgentStatus(messages.Status) || route.Terminal == nil {
		return false, nil
	}
	if messages.Terminal == nil {
		return false, errors.New("terminal operator-input reviewer read omitted host metadata")
	}
	_, terminalErr := route.Terminal.Terminal.diagnostic()
	_, readErr := messages.Terminal.diagnostic()
	validTerminal := terminalErr == nil && readErr == nil && equalTerminalMetadata(route.Terminal.Terminal, *messages.Terminal) && route.Terminal.InvalidReason == "" && route.Terminal.Child.Status == "completed" && messages.Status == "completed"
	result, validResult := detectOperatorInputReviewerResult(messages.Messages, route)
	if validTerminal && validResult {
		value := result.Classification
		return true, s.applyOperatorInputClassification(route, value.Disposition, value.Label, value.Rationale)
	}
	return true, s.conservativelyDeferOperatorInput(route, "fresh operator-input reviewer was unavailable or uncertain; conservative deferral preserves the immutable original")
}

func operatorInputPollDue(route *operatorInputRouteState, now time.Time) (time.Time, error) {
	if route == nil || route.Phase != operatorInputPhaseReviewing || route.ReviewID == "" {
		return time.Time{}, errors.New("operator-input poll has no durable review")
	}
	if route.RetryAt.After(now) {
		return route.RetryAt, nil
	}
	return now.UTC().Truncate(time.Second).Add(time.Second), nil
}

func validOperatorInputText(value string) bool {
	if strings.TrimSpace(value) == "" || len(value) > maxOperatorInputBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

func digestOperatorInput(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s *runState) beginOperatorInputReview(application string, fact operatorInputQueuedFact) (*operatorInputRouteState, bool, error) {
	if s == nil || s.Schema != policySchema || fact.RunID != s.RunID {
		return nil, false, errors.New("operator input does not belong to an active supervise run")
	}
	terminalRun := s.WorkerRunStatus == workerRunCancelled || s.WorkerRunStatus == workerRunCompleted || s.WorkerRunStatus == workerRunInterrupted || s.WorkerRunStatus == workerRunStopped
	if s.Cancelled && !terminalRun {
		return nil, false, errors.New("cancelled supervise run has not reached terminal input recovery")
	}
	if err := s.CurrentAnchor.validate(); err != nil {
		return nil, false, errors.New("operator input has no exact current worker anchor")
	}
	pluginID, err := pluginNamespaceFromCanonical(application)
	if err != nil {
		return nil, false, err
	}
	if existing := s.operatorInputRoute(fact.InputID); existing != nil {
		if existing.PluginID != pluginID || existing.RunID != fact.RunID || existing.Ordinal != fact.Ordinal || existing.Digest != fact.Digest {
			return nil, false, errors.New("operator input replay changed immutable identity")
		}
		return existing, false, nil
	}
	if fact.Ordinal != s.OperatorInputOrdinal+1 {
		return nil, false, fmt.Errorf("operator input ordinal %d does not follow durable ordinal %d", fact.Ordinal, s.OperatorInputOrdinal)
	}
	if len(s.OperatorInputRoutes) == maxOperatorInputRoutes {
		index := slices.IndexFunc(s.OperatorInputRoutes, func(item operatorInputRouteState) bool {
			return item.Phase == operatorInputPhaseRouted || item.Phase == operatorInputPhaseRecovered
		})
		if index < 0 {
			return nil, false, errors.New("operator input review ledger is backpressured")
		}
		s.OperatorInputRoutes = append(s.OperatorInputRoutes[:index], s.OperatorInputRoutes[index+1:]...)
	}
	reviewID := "input-review-" + digestString(fact.RunID + "\x00" + fact.InputID + "\x00" + fmt.Sprint(fact.Version))[:32]
	route := operatorInputRouteState{
		InputID: fact.InputID, PluginID: pluginID, RunID: fact.RunID, ExpectedVersion: fact.Version,
		Ordinal: fact.Ordinal, Digest: fact.Digest, ReviewID: reviewID, ReviewAnchor: s.CurrentAnchor,
		ClaimKey: "supervise-input-claim:" + digestString(reviewID)[:32],
		SpawnKey: agentSpawnKey(fact.RunID, reviewPurposeOperatorInput, reviewID),
		RouteKey: "supervise-input-route:" + digestString(reviewID)[:32], Phase: operatorInputPhaseClaiming,
	}
	if terminalRun {
		route.Phase = operatorInputPhaseRecovered
		s.OperatorInputRoutes = append(s.OperatorInputRoutes, route)
		s.OperatorInputOrdinal = fact.Ordinal
		return &s.OperatorInputRoutes[len(s.OperatorInputRoutes)-1], true, nil
	}
	completionInFlight := s.PendingCompletion != nil || s.PendingHostVerification != nil || s.LastHostVerification != nil || s.Completed
	if completionInFlight {
		if s.CompletionHandedOff {
			return nil, false, errors.New("operator input raced a broker-acknowledged terminal completion")
		}
		if s.PendingReview != nil {
			route.ObsoleteAgentID = s.PendingReview.AgentID
			s.PendingReview = nil
		}
		route.ReleaseHold = s.Hold != nil
		s.PendingCompletion = nil
		if s.PendingHostVerification != nil {
			s.PendingHostVerification.Obsolete = true
		}
		s.LastHostVerification = nil
		s.VerificationGates = nil
		s.Completed = false
		s.CompletedAnchor = nil
		s.CompletionVerdict = nil
		s.CompletionID = ""
		s.LastVerdict = nil
	}
	s.OperatorInputRoutes = append(s.OperatorInputRoutes, route)
	s.OperatorInputOrdinal = fact.Ordinal
	return &s.OperatorInputRoutes[len(s.OperatorInputRoutes)-1], true, nil
}

func pluginNamespaceFromCanonical(application string) (string, error) {
	if !boundedRequired(application, 1024) {
		return "", errors.New("authenticated application identity is unavailable")
	}
	index := strings.LastIndexByte(application, '@')
	if index < 1 || index == len(application)-1 {
		return "", errors.New("authenticated application identity is not canonical")
	}
	namespace := application[:index]
	if !boundedRequired(namespace, 512) || strings.ContainsAny(namespace, "#@") {
		return "", errors.New("authenticated application namespace is invalid")
	}
	return namespace, nil
}

func (s *runState) operatorInputRoute(inputID string) *operatorInputRouteState {
	if s == nil {
		return nil
	}
	for index := range s.OperatorInputRoutes {
		if s.OperatorInputRoutes[index].InputID == inputID {
			return &s.OperatorInputRoutes[index]
		}
	}
	return nil
}

func (s *runState) operatorInputReviewByChild(childID, ownership string) *operatorInputRouteState {
	for index := range s.OperatorInputRoutes {
		route := &s.OperatorInputRoutes[index]
		if route.Phase != operatorInputPhaseReviewing {
			continue
		}
		if route.AgentID == childID || route.AgentID == "" && ownership == operatorInputReviewOwnership(s.RunID, route) {
			return route
		}
	}
	return nil
}

func operatorInputClaimRequest(route operatorInputRouteState) map[string]any {
	return map[string]any{
		"input_id": route.InputID, "run_id": route.RunID, "expected_version": route.ExpectedVersion,
		"review_id": route.ReviewID, "idempotency_key": route.ClaimKey,
	}
}

func operatorInputRouteRequest(route operatorInputRouteState) map[string]any {
	return map[string]any{
		"input_id": route.InputID, "run_id": route.RunID, "expected_version": route.ExpectedVersion,
		"review_id": route.ReviewID, "disposition": route.Disposition, "label": route.Label,
		"rationale": route.Rationale, "idempotency_key": route.RouteKey,
	}
}

func validateOperatorInputIdentity(identity lifecycleIdentity, route *operatorInputRouteState, record operatorInputRecord) error {
	if route == nil || record.ID != route.InputID || record.SessionID != identity.SessionID || record.Generation != identity.SessionGeneration || record.PluginID != route.PluginID || record.RunID != route.RunID || record.Ordinal != route.Ordinal || record.Digest != route.Digest || record.Digest != digestOperatorInput(record.Text) || record.ReviewID != route.ReviewID || record.ReceiverInputID != "" || record.Recovered {
		return errors.New("broker changed immutable operator-input review identity")
	}
	return nil
}

func (s *runState) acceptOperatorInputClaim(identity lifecycleIdentity, route *operatorInputRouteState, record operatorInputRecord) error {
	if s == nil || route == nil || route.Phase != operatorInputPhaseClaiming || record.Status != operatorInputPhaseReviewing || record.Version != route.ExpectedVersion+1 || record.Label != "" || record.Rationale != "" || record.TaskID != "" {
		return errors.New("broker returned an invalid exact operator-input claim")
	}
	if err := validateOperatorInputIdentity(identity, route, record); err != nil {
		return err
	}
	route.ExpectedVersion = record.Version
	route.Phase = operatorInputPhaseReviewing
	timeout := s.Config.WatchdogTimeoutSecond
	if timeout < 1 {
		timeout = 1
	}
	route.ReviewDeadline = record.UpdatedAt.Add(time.Duration(timeout+30) * time.Second)
	return nil
}

func (s *runState) recordOperatorInputReviewFailure(route *operatorInputRouteState, failure string, now time.Time) (bool, error) {
	if s == nil || route == nil || route.Phase != operatorInputPhaseReviewing || !boundedRequired(failure, 4096) || route.ReviewDeadline.IsZero() {
		return false, errors.New("invalid operator-input reviewer failure")
	}
	route.ReviewFailures++
	route.LastFailure = failure
	exhausted := route.ReviewFailures >= s.Config.EventReviewRetries || !now.Before(route.ReviewDeadline)
	if exhausted {
		route.RetryAt = time.Time{}
		return true, s.conservativelyDeferOperatorInput(route, "fresh operator-input reviewer failed or remained uncertain: "+failure)
	}
	delay := 250 * time.Millisecond
	for attempt := 1; attempt < route.ReviewFailures && delay < 2*time.Second; attempt++ {
		delay *= 2
	}
	if delay > 2*time.Second {
		delay = 2 * time.Second
	}
	route.RetryAt = now.Add(delay)
	return false, nil
}

func (s *runState) acceptOperatorInputReviewSpawn(route *operatorInputRouteState, childID string) (bool, error) {
	if s == nil || route == nil || route.Phase != operatorInputPhaseReviewing || !boundedRequired(childID, 256) {
		return false, errors.New("invalid operator-input reviewer admission")
	}
	changed := route.AgentID != childID || !route.RetryAt.IsZero()
	if !changed {
		return false, nil
	}
	route.AgentID, route.AgentOffset, route.TokenBudget, route.Terminal, route.RetryAt = childID, 0, s.Config.WatchdogTokenBudget, nil, time.Time{}
	return true, nil
}

func (s *runState) applyOperatorInputClassification(route *operatorInputRouteState, disposition, label, rationale string) error {
	if s == nil || route == nil || route.Phase != operatorInputPhaseReviewing || (disposition != operatorInputDispositionGive && disposition != operatorInputDispositionLater) || !boundedRequired(label, 256) || !boundedRequired(rationale, 4096) {
		return errors.New("invalid operator-input quality classification")
	}
	if disposition == operatorInputDispositionLater && len(s.DeferredInputs) >= maxDeferredInputs {
		// The input is already broker-owned and durably claimed. It cannot be
		// dropped, rewritten as related work, or left in reviewing forever merely
		// because this application's bounded continuation set is full. Record the
		// conservative classification, then fail closed by pausing the recurrence;
		// native terminal recovery preserves the exact original and its ordinal.
		route.Disposition, route.Label, route.Rationale = disposition, strings.TrimSpace(label), strings.TrimSpace(rationale)
		route.Phase = operatorInputPhaseOverflow
		return nil
	}
	route.Disposition, route.Label, route.Rationale = disposition, strings.TrimSpace(label), strings.TrimSpace(rationale)
	route.Phase = operatorInputPhaseRouting
	return nil
}

func (s *runState) conservativelyDeferOperatorInput(route *operatorInputRouteState, reason string) error {
	if !boundedRequired(reason, 4096) {
		reason = "fresh operator-input reviewer was unavailable or uncertain; conservative deferral preserves the immutable original"
	}
	return s.applyOperatorInputClassification(route, operatorInputDispositionLater, "deferred supervised follow-up", reason)
}

func (s *runState) acceptOperatorInputRoute(identity lifecycleIdentity, route *operatorInputRouteState, record operatorInputRecord) error {
	if s == nil || route == nil || route.Phase != operatorInputPhaseRouting || record.Version != route.ExpectedVersion+1 || record.Label != route.Label || record.Rationale != route.Rationale {
		return errors.New("no exact operator-input route intent is pending")
	}
	if err := validateOperatorInputIdentity(identity, route, record); err != nil {
		return err
	}
	wantStatus := "ready"
	if route.Disposition == operatorInputDispositionLater {
		wantStatus = "deferred"
	}
	if record.Status != wantStatus {
		return errors.New("broker returned the wrong operator-input disposition")
	}
	if route.Disposition == operatorInputDispositionLater {
		if !boundedRequired(record.TaskID, 256) {
			return errors.New("deferred operator input has no broker task identity")
		}
		for _, item := range s.DeferredInputs {
			if item.InputID == route.InputID || item.Ordinal == route.Ordinal || item.TaskID == record.TaskID {
				return errors.New("duplicate deferred operator-input identity")
			}
		}
		s.DeferredInputs = append(s.DeferredInputs, deferredInputState{InputID: route.InputID, RunID: route.RunID, Ordinal: route.Ordinal, Digest: route.Digest, TaskID: record.TaskID})
	} else if record.TaskID != "" {
		return errors.New("related operator input unexpectedly created a deferred task")
	}
	route.Phase, route.RoutedVersion, route.TaskID, route.Terminal = operatorInputPhaseRouted, record.Version, record.TaskID, nil
	return nil
}

func (s *runState) recoverOperatorInputAfterTerminal(route *operatorInputRouteState) error {
	if s == nil || route == nil || (route.Phase != operatorInputPhaseClaiming && route.Phase != operatorInputPhaseReviewing && route.Phase != operatorInputPhaseRouting && route.Phase != operatorInputPhaseOverflow) {
		return errors.New("no unresolved operator input can be terminal-recovered")
	}
	switch s.WorkerRunStatus {
	case workerRunCancelled, workerRunCompleted, workerRunInterrupted, workerRunStopped:
		route.Phase, route.Terminal = operatorInputPhaseRecovered, nil
		return nil
	default:
		return errors.New("reviewing operator input disappeared while its worker remains non-terminal")
	}
}

func (s runState) continuationInputIDs() ([]string, error) {
	for _, route := range s.OperatorInputRoutes {
		if route.Phase != operatorInputPhaseRouted && route.Phase != operatorInputPhaseRecovered {
			return nil, errors.New("operator input review or routing is still pending")
		}
	}
	if len(s.DeferredInputs) > maxDeferredInputs {
		return nil, errors.New("deferred input set exceeds broker continuation limit")
	}
	deferred := append([]deferredInputState(nil), s.DeferredInputs...)
	sort.Slice(deferred, func(i, j int) bool {
		if deferred[i].Ordinal == deferred[j].Ordinal {
			return deferred[i].InputID < deferred[j].InputID
		}
		return deferred[i].Ordinal < deferred[j].Ordinal
	})
	seen := map[string]bool{}
	result := make([]string, 0, len(deferred))
	lastOrdinal := uint64(0)
	for _, item := range deferred {
		if !boundedRequired(item.InputID, 256) || item.RunID != s.RunID || !boundedRequired(item.TaskID, 256) || item.Ordinal == 0 || item.Ordinal <= lastOrdinal || item.Digest == "" || seen[item.InputID] {
			return nil, errors.New("deferred input set is malformed or duplicated")
		}
		seen[item.InputID] = true
		lastOrdinal = item.Ordinal
		result = append(result, item.InputID)
	}
	return result, nil
}

func cloneOperatorInputRoutes(values []operatorInputRouteState) []operatorInputRouteState {
	if values == nil {
		return nil
	}
	result := make([]operatorInputRouteState, len(values))
	copy(result, values)
	for index := range result {
		if values[index].Terminal != nil {
			terminal := *values[index].Terminal
			terminal.EvidenceRefs = append([]string(nil), values[index].Terminal.EvidenceRefs...)
			result[index].Terminal = &terminal
		}
	}
	return result
}

func decodeReviewingOperatorInputs(raw []byte) ([]operatorInputRecord, error) {
	var projection struct {
		ReviewingInputs []json.RawMessage `json:"reviewing_inputs"`
	}
	if err := json.Unmarshal(raw, &projection); err != nil {
		return nil, err
	}
	result := make([]operatorInputRecord, 0, len(projection.ReviewingInputs))
	seen := map[string]bool{}
	for _, item := range projection.ReviewingInputs {
		record, err := decodeOperatorInputRecord(item)
		if err != nil || record.Status != operatorInputPhaseReviewing || record.ReviewID == "" || seen[record.ID] {
			return nil, errors.New("projection contains an invalid reviewing operator input")
		}
		seen[record.ID] = true
		result = append(result, record)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Ordinal < result[j].Ordinal })
	return result, nil
}
