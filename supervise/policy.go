package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	policySchema      = "stado.dev/supervise/policy/v1"
	maxHistoryEvents  = 16
	maxQueuedSignals  = 16
	maxEvidenceRefs   = 32
	maxCorrectionSize = 8 << 10
)

type mode string

const (
	modeEvent mode = "event"
	modeLive  mode = "live"
)

// config is application policy. Spend authority is deliberately token-only;
// provider cost metadata is neither accepted nor consulted by this type.
type config struct {
	Mode                         mode     `json:"mode"`
	AssuranceProfile             string   `json:"assurance_profile"`
	PivotApproval                string   `json:"pivot_approval"`
	ReviewEveryTurns             int      `json:"review_every_turns,omitempty"`
	StrictLiveBarrier            bool     `json:"strict_live_barrier,omitempty"`
	AllowedPathPrefixes          []string `json:"allowed_path_prefixes,omitempty"`
	WatchdogProvider             string   `json:"watchdog_provider,omitempty"`
	WatchdogModel                string   `json:"watchdog_model,omitempty"`
	WatchdogThinking             string   `json:"watchdog_thinking,omitempty"`
	WatchdogThinkingBudgetTokens int      `json:"watchdog_thinking_budget_tokens,omitempty"`
	WatchdogReasoningEffort      string   `json:"watchdog_reasoning_effort,omitempty"`
	VerifierProvider             string   `json:"verifier_provider,omitempty"`
	VerifierModel                string   `json:"verifier_model,omitempty"`
	VerifierThinking             string   `json:"verifier_thinking,omitempty"`
	VerifierThinkingBudgetTokens int      `json:"verifier_thinking_budget_tokens,omitempty"`
	VerifierReasoningEffort      string   `json:"verifier_reasoning_effort,omitempty"`
	WatchdogTokenBudget          int      `json:"watchdog_token_budget"`
	VerifierTokenBudget          int      `json:"verifier_token_budget"`
	EventReviewRetries           int      `json:"event_review_retries"`
	FailedEventLimit             int      `json:"failed_event_limit"`
	CorrectionLimit              int      `json:"correction_limit"`
	LiveRetryBaseMillis          int      `json:"live_retry_base_millis"`
	LiveRetryMaxMillis           int      `json:"live_retry_max_millis"`
	VerifierProfile              string   `json:"verifier_profile,omitempty"`
	WatchdogTimeoutSecond        int      `json:"watchdog_timeout_seconds,omitempty"`
	HoldTTLSeconds               int      `json:"hold_ttl_seconds"`
}

func defaultConfig() config {
	return config{
		Mode: modeEvent, AssuranceProfile: "standard", PivotApproval: "user", WatchdogTokenBudget: 16_384,
		VerifierTokenBudget: 24_576, EventReviewRetries: 3, FailedEventLimit: 10, CorrectionLimit: 3,
		LiveRetryBaseMillis: 500, LiveRetryMaxMillis: 8_000, WatchdogTimeoutSecond: 120,
		HoldTTLSeconds: 600,
	}
}

func (c config) validate() error {
	if c.Mode != modeEvent && c.Mode != modeLive {
		return fmt.Errorf("invalid mode %q", c.Mode)
	}
	if c.AssuranceProfile != "standard" && c.AssuranceProfile != "high_assurance" && c.AssuranceProfile != "custom" {
		return errors.New("assurance_profile must be standard, high_assurance, or custom")
	}
	if c.PivotApproval != "user" && c.PivotApproval != "watchdog" {
		return errors.New("pivot_approval must be user or watchdog")
	}
	if c.ReviewEveryTurns < 0 || c.ReviewEveryTurns > 10_000 {
		return errors.New("review_every_turns must be in 0..10000")
	}
	if c.StrictLiveBarrier && c.Mode != modeLive {
		return errors.New("strict_live_barrier requires live mode")
	}
	if c.WatchdogTokenBudget < 1 || c.WatchdogTokenBudget > 1_000_000 ||
		c.VerifierTokenBudget < 1 || c.VerifierTokenBudget > 1_000_000 {
		return errors.New("reviewer token budgets must be in 1..1000000")
	}
	if c.EventReviewRetries < 1 || c.EventReviewRetries > 10 || c.FailedEventLimit < 1 || c.FailedEventLimit > 100 || c.CorrectionLimit < 1 || c.CorrectionLimit > 20 {
		return errors.New("review retry, trigger-failure, or correction limit is outside its bounded range")
	}
	if c.LiveRetryBaseMillis < 1 || c.LiveRetryMaxMillis < c.LiveRetryBaseMillis || c.LiveRetryMaxMillis > 300_000 {
		return errors.New("live retry backoff is outside its bounded range")
	}
	if c.VerifierProfile != "" && c.VerifierProfile != "required" && c.VerifierProfile != "advisory" {
		return errors.New("verifier_profile must be required or advisory")
	}
	if c.AssuranceProfile == "high_assurance" && c.VerifierProfile == "advisory" {
		return errors.New("high_assurance cannot use an advisory completion verifier")
	}
	if c.WatchdogTimeoutSecond < 0 || c.WatchdogTimeoutSecond > 3600 {
		return errors.New("watchdog timeout must be in 0..3600 seconds")
	}
	if c.HoldTTLSeconds < 30 || c.HoldTTLSeconds > 3600 {
		return errors.New("hold TTL must be in 30..3600 seconds")
	}
	if len(c.AllowedPathPrefixes) > 64 {
		return errors.New("allowed path prefix list exceeds 64 entries")
	}
	if err := validateSpawnProfile("watchdog", c.WatchdogProvider, c.WatchdogModel, c.WatchdogThinking, c.WatchdogThinkingBudgetTokens, c.WatchdogReasoningEffort, c.WatchdogTokenBudget); err != nil {
		return err
	}
	if err := validateSpawnProfile("verifier", c.VerifierProvider, c.VerifierModel, c.VerifierThinking, c.VerifierThinkingBudgetTokens, c.VerifierReasoningEffort, c.VerifierTokenBudget); err != nil {
		return err
	}
	if _, err := normalizeAllowedPrefixes(c.AllowedPathPrefixes); err != nil {
		return err
	}
	return nil
}

func validateSpawnProfile(role, provider, model, thinking string, thinkingBudget int, effort string, tokenBudget int) error {
	if len(provider) > 128 || strings.TrimSpace(provider) != provider || len(model) > 256 || strings.TrimSpace(model) != model {
		return fmt.Errorf("%s provider/model overrides must be bounded and trimmed", role)
	}
	if provider != "" && model == "" {
		return fmt.Errorf("%s provider override requires an exact model", role)
	}
	switch thinking {
	case "", "auto", "on", "off":
	default:
		return fmt.Errorf("%s thinking must be auto, on, off, or inherited", role)
	}
	if thinkingBudget < 0 || thinkingBudget > 2_000_000 || thinkingBudget > tokenBudget {
		return fmt.Errorf("%s thinking budget must be in 0..2000000 and no greater than its token cap", role)
	}
	switch effort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Errorf("%s reasoning effort is invalid", role)
	}
	return nil
}

func (c config) verifierRequired() bool { return c.VerifierProfile != "advisory" }

func configsEqual(a, b config) bool {
	return a.Mode == b.Mode && a.AssuranceProfile == b.AssuranceProfile && a.PivotApproval == b.PivotApproval && a.ReviewEveryTurns == b.ReviewEveryTurns && a.StrictLiveBarrier == b.StrictLiveBarrier &&
		a.WatchdogTokenBudget == b.WatchdogTokenBudget && a.VerifierTokenBudget == b.VerifierTokenBudget &&
		a.EventReviewRetries == b.EventReviewRetries && a.FailedEventLimit == b.FailedEventLimit && a.CorrectionLimit == b.CorrectionLimit && a.LiveRetryBaseMillis == b.LiveRetryBaseMillis && a.LiveRetryMaxMillis == b.LiveRetryMaxMillis &&
		a.VerifierProfile == b.VerifierProfile &&
		a.WatchdogProvider == b.WatchdogProvider && a.WatchdogModel == b.WatchdogModel && a.WatchdogThinking == b.WatchdogThinking && a.WatchdogThinkingBudgetTokens == b.WatchdogThinkingBudgetTokens && a.WatchdogReasoningEffort == b.WatchdogReasoningEffort &&
		a.VerifierProvider == b.VerifierProvider && a.VerifierModel == b.VerifierModel && a.VerifierThinking == b.VerifierThinking && a.VerifierThinkingBudgetTokens == b.VerifierThinkingBudgetTokens && a.VerifierReasoningEffort == b.VerifierReasoningEffort &&
		a.WatchdogTimeoutSecond == b.WatchdogTimeoutSecond && a.HoldTTLSeconds == b.HoldTTLSeconds &&
		slices.Equal(a.AllowedPathPrefixes, b.AllowedPathPrefixes)
}

type anchor struct {
	SessionSequence uint64 `json:"session_sequence"`
	PlanVersion     uint64 `json:"plan_version"`
	ActiveStep      string `json:"active_step,omitempty"`
	TreeDigest      string `json:"tree_digest"`
	TurnRef         string `json:"turn_ref"`
}

func (a anchor) validate() error {
	if a.SessionSequence == 0 || a.PlanVersion == 0 || strings.TrimSpace(a.TreeDigest) == "" || strings.TrimSpace(a.TurnRef) == "" {
		return errors.New("incomplete authenticated worker anchor")
	}
	if len(a.ActiveStep) > 256 || len(a.TreeDigest) > 512 || len(a.TurnRef) > 512 {
		return errors.New("worker anchor exceeds bounds")
	}
	return nil
}

type workerEventKind string

const (
	eventTurnCompleted     workerEventKind = "turn_completed"
	eventToolOutcome       workerEventKind = "tool_outcome"
	eventVerification      workerEventKind = "verification"
	eventTreeChanged       workerEventKind = "tree_changed"
	eventChildLifecycle    workerEventKind = "child_lifecycle"
	eventStepClaimed       workerEventKind = "step_completion_claimed"
	eventPivotRequested    workerEventKind = "pivot_requested"
	eventRiskBoundary      workerEventKind = "risk_boundary"
	eventCompletionClaimed workerEventKind = "completion_claimed"
)

type workerEvent struct {
	ID                        string          `json:"id"`
	Kind                      workerEventKind `json:"kind"`
	Sequence                  uint64          `json:"sequence"`
	Anchor                    anchor          `json:"anchor"`
	Tool                      string          `json:"tool,omitempty"`
	ArgsDigest                string          `json:"args_digest,omitempty"`
	ErrorFingerprint          string          `json:"error_fingerprint,omitempty"`
	Succeeded                 bool            `json:"succeeded,omitempty"`
	VerificationPassed        *bool           `json:"verification_passed,omitempty"`
	VerificationCommandDigest string          `json:"verification_command_digest,omitempty"`
	VerificationResultDigest  string          `json:"verification_result_digest,omitempty"`
	ChangedPaths              []string        `json:"changed_paths,omitempty"`
	ChangedPathCount          int             `json:"changed_path_count,omitempty"`
	OutOfScopePaths           []string        `json:"out_of_scope_paths,omitempty"`
	DiffBytes                 int64           `json:"diff_bytes,omitempty"`
	CompletedSteps            int             `json:"completed_steps,omitempty"`
	EvidenceCount             int             `json:"evidence_count,omitempty"`
	TokenUsage                uint64          `json:"token_usage,omitempty"`
	TokenBudget               uint64          `json:"token_budget,omitempty"`
	ChildID                   string          `json:"child_id,omitempty"`
	ChildStatus               string          `json:"child_status,omitempty"`
	Boundary                  string          `json:"boundary,omitempty"`
	EvidenceRefs              []string        `json:"evidence_refs,omitempty"`
}

func (e workerEvent) validate() error {
	if e.ID == "" || len(e.ID) > 256 || e.Sequence == 0 {
		return errors.New("worker event requires bounded id and sequence")
	}
	if err := e.Anchor.validate(); err != nil {
		return err
	}
	switch e.Kind {
	case eventTurnCompleted, eventToolOutcome, eventVerification, eventTreeChanged,
		eventChildLifecycle, eventStepClaimed, eventPivotRequested,
		eventRiskBoundary, eventCompletionClaimed:
	default:
		return fmt.Errorf("unknown worker event kind %q", e.Kind)
	}
	if len(e.ChangedPaths) > 64 || len(e.OutOfScopePaths) > 64 {
		return errors.New("worker event path list exceeds limit")
	}
	if err := validateEvidenceRefs(e.EvidenceRefs); err != nil {
		return err
	}
	for _, value := range append(append([]string(nil), e.ChangedPaths...), e.OutOfScopePaths...) {
		if value == "" || len(value) > 512 {
			return errors.New("worker event contains an invalid bounded path")
		}
	}
	for name, value := range map[string]string{
		"tool": e.Tool, "args digest": e.ArgsDigest, "error fingerprint": e.ErrorFingerprint,
		"child id": e.ChildID, "child status": e.ChildStatus, "risk boundary": e.Boundary,
		"verification command digest": e.VerificationCommandDigest, "verification result digest": e.VerificationResultDigest,
	} {
		if len(value) > 512 {
			return fmt.Errorf("worker event %s exceeds limit", name)
		}
	}
	if e.DiffBytes < 0 || e.ChangedPathCount < 0 || e.CompletedSteps < 0 || e.EvidenceCount < 0 {
		return errors.New("worker event counters cannot be negative")
	}
	return nil
}

type signal struct {
	Type         string            `json:"type"`
	Severity     string            `json:"severity"`
	EvidenceRefs []string          `json:"evidence_refs"`
	Attributes   map[string]string `json:"attributes,omitempty"`
}

type detectorState struct {
	History        []workerEvent     `json:"history,omitempty"`
	LastEmittedSeq map[string]uint64 `json:"last_emitted_sequence,omitempty"`
}

type reviewRequest struct {
	ID          string                     `json:"id"`
	Anchor      anchor                     `json:"anchor"`
	Signals     []signal                   `json:"signals"`
	Confirming  bool                       `json:"confirming_stale_intervention,omitempty"`
	Purpose     string                     `json:"purpose,omitempty"`
	AgentID     string                     `json:"agent_id,omitempty"`
	AgentOffset int                        `json:"agent_offset,omitempty"`
	TokenBudget int                        `json:"token_budget,omitempty"`
	Terminal    *reviewTerminalObservation `json:"terminal,omitempty"`
	Attempt     uint64                     `json:"attempt"`
	RetryAt     time.Time                  `json:"retry_at,omitempty"`
	LastFailure string                     `json:"last_failure,omitempty"`
	Handoff     watchdogHandoff            `json:"handoff,omitempty"`
	Pivot       *pivotCandidateState       `json:"pivot,omitempty"`
}

const (
	reviewPurposeWatchdog = "watchdog"
	reviewPurposeVerifier = "completion_verifier"
)

type verificationGateState struct {
	CommandDigest string   `json:"command_digest"`
	ResultDigest  string   `json:"result_digest"`
	Passed        bool     `json:"passed"`
	Anchor        anchor   `json:"anchor"`
	EvidenceRefs  []string `json:"evidence_refs,omitempty"`
}

type holdState struct {
	ID            string    `json:"id,omitempty"`
	Version       uint64    `json:"version,omitempty"`
	Reason        string    `json:"reason"`
	LeaseUntil    time.Time `json:"lease_until,omitempty"`
	RenewAt       time.Time `json:"renew_at,omitempty"`
	RenewalFailed bool      `json:"renewal_failed,omitempty"`
}

type watchdogHandoff struct {
	OpenConcerns    []string `json:"open_concerns,omitempty"`
	Hypotheses      []string `json:"hypotheses,omitempty"`
	Interventions   []string `json:"interventions,omitempty"`
	MissingEvidence []string `json:"missing_evidence,omitempty"`
	SuggestedProbes []string `json:"suggested_probes,omitempty"`
}

type correctionFollowUpState struct {
	IssuedAt      anchor   `json:"issued_at"`
	CorrectionRef string   `json:"correction_ref"`
	EvidenceRefs  []string `json:"evidence_refs,omitempty"`
}

type runState struct {
	Schema                            string                           `json:"schema"`
	RunID                             string                           `json:"run_id"`
	ContractID                        string                           `json:"contract_id,omitempty"`
	ContractVersion                   uint64                           `json:"contract_version,omitempty"`
	CriteriaTotal                     int                              `json:"criteria_total,omitempty"`
	Criteria                          []criterionProgressState         `json:"criteria,omitempty"`
	PlanTotal                         int                              `json:"plan_total,omitempty"`
	PlanSteps                         []string                         `json:"plan_steps,omitempty"`
	Config                            config                           `json:"config"`
	CurrentAnchor                     anchor                           `json:"current_anchor"`
	ObservationSequence               uint64                           `json:"observation_sequence"`
	LastTurnEventSequence             uint64                           `json:"last_turn_event_sequence,omitempty"`
	LastTurnFactsDigest               string                           `json:"last_turn_facts_digest,omitempty"`
	TurnsSeen                         int                              `json:"turns_seen"`
	CompletedSteps                    int                              `json:"completed_steps"`
	CumulativeWorkerTokens            uint64                           `json:"cumulative_worker_tokens"`
	WatchdogTokenUsage                agentTokenUsage                  `json:"watchdog_token_usage"`
	VerifierTokenUsage                agentTokenUsage                  `json:"verifier_token_usage"`
	FailedEventStreak                 int                              `json:"failed_event_streak,omitempty"`
	FailedCorrectionCount             int                              `json:"failed_correction_count,omitempty"`
	WatchdogHandoff                   watchdogHandoff                  `json:"watchdog_handoff,omitempty"`
	CorrectionFollowUp                *correctionFollowUpState         `json:"correction_follow_up,omitempty"`
	AgentDownSequence                 uint64                           `json:"agent_down_sequence,omitempty"`
	AgentTerminals                    []reviewTerminalObservation      `json:"agent_terminals,omitempty"`
	Detector                          detectorState                    `json:"detector"`
	QueuedSignals                     []signal                         `json:"queued_signals,omitempty"`
	PendingReview                     *reviewRequest                   `json:"pending_review,omitempty"`
	PendingStepClaim                  *stepCompletionClaim             `json:"pending_step_claim,omitempty"`
	PendingPivot                      *pivotCandidateState             `json:"pending_pivot,omitempty"`
	PendingCompletion                 *completionCandidateState        `json:"pending_completion,omitempty"`
	PendingHostVerification           *hostVerificationRequestState    `json:"pending_host_verification,omitempty"`
	LastHostVerification              *hostVerificationResultState     `json:"last_host_verification,omitempty"`
	HostVerificationEffectPending     bool                             `json:"host_verification_effect_pending,omitempty"`
	LastHostVerificationEventSequence uint64                           `json:"last_host_verification_event_sequence,omitempty"`
	LastHostVerificationEventDigest   string                           `json:"last_host_verification_event_digest,omitempty"`
	VerificationGates                 map[string]verificationGateState `json:"verification_gates,omitempty"`
	Completed                         bool                             `json:"completed,omitempty"`
	CompletedAnchor                   *anchor                          `json:"completed_anchor,omitempty"`
	CompletionVerdict                 *verdict                         `json:"completion_verdict,omitempty"`
	CompletionHandedOff               bool                             `json:"completion_handed_off,omitempty"`
	CompletionID                      string                           `json:"completion_id,omitempty"`
	WorkerRunVersion                  uint64                           `json:"worker_run_version,omitempty"`
	WorkerRunStatus                   string                           `json:"worker_run_status,omitempty"`
	WorkerObjective                   string                           `json:"worker_objective,omitempty"`
	WorkerConflict                    string                           `json:"worker_conflict,omitempty"`
	Cancelled                         bool                             `json:"cancelled,omitempty"`
	CancelReason                      string                           `json:"cancel_reason,omitempty"`
	CancellationAgentID               string                           `json:"cancellation_agent_id,omitempty"`
	CancellationCleaned               bool                             `json:"cancellation_cleaned,omitempty"`
	OperatorInputOrdinal              uint64                           `json:"operator_input_ordinal,omitempty"`
	OperatorInputRoutes               []operatorInputRouteState        `json:"operator_input_routes,omitempty"`
	DeferredInputs                    []deferredInputState             `json:"deferred_inputs,omitempty"`
	ToolReceipts                      []toolReceipt                    `json:"tool_receipts,omitempty"`
	Hold                              *holdState                       `json:"hold,omitempty"`
	LastVerdict                       *verdict                         `json:"last_verdict,omitempty"`
	AdvisorySteering                  []string                         `json:"advisory_steering,omitempty"`
	Diagnostics                       []string                         `json:"diagnostics,omitempty"`
}

func newRunState(runID string, cfg config) (runState, error) {
	if strings.TrimSpace(runID) == "" || len(runID) > 256 {
		return runState{}, errors.New("run id required")
	}
	if err := cfg.validate(); err != nil {
		return runState{}, err
	}
	return runState{
		Schema: policySchema, RunID: runID, Config: cfg,
		CurrentAnchor: anchor{PlanVersion: 1},
		Detector:      detectorState{LastEmittedSeq: map[string]uint64{}},
	}, nil
}

type actionKind string

const (
	actionAcquireHold         actionKind = "acquire_hold"
	actionReleaseHold         actionKind = "release_hold"
	actionStartReview         actionKind = "start_review"
	actionScheduleReviewRetry actionKind = "schedule_review_retry"
	actionSteer               actionKind = "steer"
	actionPause               actionKind = "request_pause"
	actionStop                actionKind = "request_stop"
)

type action struct {
	Kind        actionKind     `json:"kind"`
	Label       string         `json:"label,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	Advisory    bool           `json:"advisory,omitempty"`
	Correction  string         `json:"correction,omitempty"`
	Review      *reviewRequest `json:"review,omitempty"`
	TokenBudget int            `json:"token_budget,omitempty"`
	OmitHold    bool           `json:"omit_hold,omitempty"`
}

type transition struct {
	Actions []action `json:"actions,omitempty"`
	Note    string   `json:"note,omitempty"`
}

// observe applies deterministic application-owned detectors. Tree churn and
// evidence count are retained as context, but only an active-step change or
// completed-step progress resets the four-turn progress-stall window.
func (s *runState) observe(event workerEvent) (transition, error) {
	if s == nil || s.Schema != policySchema {
		return transition{}, errors.New("supervision state unavailable")
	}
	if err := event.validate(); err != nil {
		return transition{}, err
	}
	if event.Sequence <= s.ObservationSequence {
		return transition{}, errors.New("worker observation sequence did not advance")
	}
	prior := append([]workerEvent(nil), s.Detector.History...)
	signals := detect(prior, event)
	s.CurrentAnchor = event.Anchor
	s.ObservationSequence = event.Sequence
	if event.Kind == eventTurnCompleted {
		s.TurnsSeen++
		s.CumulativeWorkerTokens = event.TokenUsage
		if s.Config.Mode == modeLive {
			signals = append(signals, signal{Type: "live_turn", Severity: "info", EvidenceRefs: []string{event.ID}})
		}
		if n := s.Config.ReviewEveryTurns; n > 0 && s.TurnsSeen%n == 0 {
			signals = append(signals, signal{Type: "periodic_turn", Severity: "info", EvidenceRefs: []string{event.ID}, Attributes: map[string]string{"every": fmt.Sprint(n)}})
		}
		if s.CorrectionFollowUp != nil && event.Anchor.SessionSequence > s.CorrectionFollowUp.IssuedAt.SessionSequence {
			signals = append(signals, signal{Type: "correction_follow_up", Severity: "high", EvidenceRefs: append([]string(nil), s.CorrectionFollowUp.EvidenceRefs...), Attributes: map[string]string{"correction_ref": s.CorrectionFollowUp.CorrectionRef}})
		}
	}
	if event.Kind == eventVerification && event.VerificationPassed != nil && event.VerificationCommandDigest != "" {
		if s.VerificationGates == nil {
			s.VerificationGates = map[string]verificationGateState{}
		}
		s.VerificationGates[event.VerificationCommandDigest] = verificationGateState{
			CommandDigest: event.VerificationCommandDigest, ResultDigest: event.VerificationResultDigest,
			Passed: *event.VerificationPassed, Anchor: event.Anchor,
			EvidenceRefs: boundedModelEvidenceRefs(event.EvidenceRefs),
		}
	}
	// Path lists are needed for the current scope detector only. Retaining them
	// in every state snapshot would let ordinary tree churn exhaust the broker's
	// bounded journal payload without improving later detector decisions.
	historyEvent := event
	historyEvent.ChangedPaths = nil
	historyEvent.OutOfScopePaths = nil
	historyEvent.Boundary = ""
	s.Detector.History = append(s.Detector.History, historyEvent)
	if len(s.Detector.History) > maxHistoryEvents {
		s.Detector.History = append([]workerEvent(nil), s.Detector.History[len(s.Detector.History)-maxHistoryEvents:]...)
	}
	signals = s.filterCooldown(event.Sequence, signals)
	if len(signals) == 0 {
		return transition{}, nil
	}
	if s.PendingReview != nil || s.PendingHostVerification != nil {
		s.QueuedSignals = appendBoundedSignals(s.QueuedSignals, signals)
		return transition{Note: "coalesced behind active quality work"}, nil
	}
	review := &reviewRequest{ID: reviewID(event, signals), Anchor: event.Anchor, Signals: signals, Purpose: reviewPurposeWatchdog, Attempt: 1, Handoff: s.WatchdogHandoff}
	if event.Kind == eventPivotRequested {
		if s.PendingPivot == nil || s.PendingPivot.Stage != pivotStageReviewing || s.PendingPivot.Anchor != event.Anchor {
			return transition{}, errors.New("pivot review lacks an exact current-anchor proposal")
		}
		review.Pivot = clonePivotCandidate(s.PendingPivot)
	}
	s.PendingReview = review
	actions := make([]action, 0, 2)
	if s.Config.Mode == modeLive && s.Config.StrictLiveBarrier {
		s.Hold = &holdState{Reason: "strict live review at current worker anchor"}
		actions = append(actions, action{Kind: actionAcquireHold, Reason: s.Hold.Reason})
	}
	actions = append(actions, action{Kind: actionStartReview, Review: cloneReview(review), TokenBudget: s.Config.WatchdogTokenBudget})
	return transition{Actions: actions}, nil
}

func (s *runState) filterCooldown(sequence uint64, in []signal) []signal {
	if s.Detector.LastEmittedSeq == nil {
		s.Detector.LastEmittedSeq = map[string]uint64{}
	}
	out := make([]signal, 0, len(in))
	for _, item := range in {
		shape := signalShape(item)
		last, seen := s.Detector.LastEmittedSeq[shape]
		if seen && !urgentSignal(item.Type) && sequence <= last+2 {
			continue
		}
		s.Detector.LastEmittedSeq[shape] = sequence
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

func detect(prior []workerEvent, event workerEvent) []signal {
	ref := func(events ...workerEvent) []string {
		out := make([]string, 0, len(events))
		seen := map[string]struct{}{}
		for _, item := range events {
			values := item.EvidenceRefs
			if len(values) == 0 {
				values = []string{item.ID}
			}
			for _, value := range values {
				if _, exists := seen[value]; exists {
					continue
				}
				seen[value] = struct{}{}
				out = append(out, value)
				if len(out) == maxEvidenceRefs {
					return out
				}
			}
		}
		return out
	}
	var out []signal
	switch event.Kind {
	case eventToolOutcome:
		if !event.Succeeded && event.ErrorFingerprint != "" {
			var matches []workerEvent
			for i := len(prior) - 1; i >= 0 && len(matches) < 2; i-- {
				item := prior[i]
				if item.Kind == eventToolOutcome && !item.Succeeded && item.Tool == event.Tool && item.ErrorFingerprint == event.ErrorFingerprint {
					matches = append(matches, item)
				}
			}
			if len(matches) >= 1 {
				out = append(out, signal{Type: "repeated_failure", Severity: "warning", EvidenceRefs: ref(matches[0], event), Attributes: map[string]string{"tool": event.Tool, "fingerprint": event.ErrorFingerprint}})
			}
			if len(matches) >= 2 && event.ArgsDigest != "" && matches[0].ArgsDigest == event.ArgsDigest && matches[1].ArgsDigest == event.ArgsDigest {
				out = append(out, signal{Type: "retry_thrash", Severity: "high", EvidenceRefs: ref(matches[1], matches[0], event), Attributes: map[string]string{"tool": event.Tool, "args_digest": event.ArgsDigest}})
			}
		}
	case eventVerification:
		if event.VerificationPassed != nil && !*event.VerificationPassed {
			if previous, ok := lastEvent(prior, func(item workerEvent) bool { return item.Kind == eventVerification && item.VerificationPassed != nil }); ok && *previous.VerificationPassed {
				out = append(out, signal{Type: "verification_regression", Severity: "high", EvidenceRefs: ref(previous, event)})
			}
		}
	case eventTreeChanged:
		if len(event.OutOfScopePaths) > 0 {
			out = append(out, signal{Type: "scope_expansion", Severity: "high", EvidenceRefs: ref(event), Attributes: map[string]string{"paths": boundedJoin(event.OutOfScopePaths, 256)}})
		} else if event.ChangedPathCount > 12 || event.DiffBytes > 32_768 {
			out = append(out, signal{Type: "scope_expansion", Severity: "warning", EvidenceRefs: ref(event), Attributes: map[string]string{"changed_path_count": fmt.Sprint(event.ChangedPathCount), "diff_bytes": fmt.Sprint(event.DiffBytes)}})
		}
	case eventTurnCompleted:
		turns := []workerEvent{event}
		for i := len(prior) - 1; i >= 0 && len(turns) < 4; i-- {
			if prior[i].Kind == eventTurnCompleted {
				turns = append(turns, prior[i])
			}
		}
		if len(turns) == 4 {
			stalled := true
			for _, item := range turns[1:] {
				if item.CompletedSteps != event.CompletedSteps || item.Anchor.ActiveStep != event.Anchor.ActiveStep {
					stalled = false
					break
				}
			}
			if stalled {
				out = append(out, signal{Type: "no_criteria_progress", Severity: "warning", EvidenceRefs: ref(turns[3], turns[2], turns[1], turns[0]), Attributes: map[string]string{
					"step": event.Anchor.ActiveStep, "completed_steps": fmt.Sprint(event.CompletedSteps), "evidence_count": fmt.Sprint(event.EvidenceCount), "tree_digest": event.Anchor.TreeDigest,
				}})
			}
		}
		if event.TokenBudget > 0 && event.TokenUsage >= event.TokenBudget-event.TokenBudget/5 {
			out = append(out, signal{Type: "budget_burn", Severity: "warning", EvidenceRefs: ref(event), Attributes: map[string]string{"used_percent": fmt.Sprint(tokenPercent(event.TokenUsage, event.TokenBudget))}})
		}
	case eventChildLifecycle:
		switch event.ChildStatus {
		case "failed", "error", "timeout", "budget_exhausted", "restart_exhausted":
			out = append(out, signal{Type: "child_failure", Severity: "high", EvidenceRefs: ref(event), Attributes: map[string]string{"child": event.ChildID, "status": event.ChildStatus}})
		}
	case eventStepClaimed:
		out = append(out, signal{Type: "step_completion_claim", Severity: "info", EvidenceRefs: ref(event)})
	case eventPivotRequested:
		out = append(out, signal{Type: "pivot_request", Severity: "high", EvidenceRefs: ref(event)})
	case eventRiskBoundary:
		out = append(out, signal{Type: "risky_boundary", Severity: "critical", EvidenceRefs: ref(event), Attributes: map[string]string{"boundary": event.Boundary}})
	case eventCompletionClaimed:
		out = append(out, signal{Type: "completion_claim", Severity: "high", EvidenceRefs: ref(event)})
	}
	return out
}

func tokenPercent(used, total uint64) uint64 {
	if total == 0 {
		return 0
	}
	if used >= total {
		return 100
	}
	hi, lo := bits.Mul64(used, 100)
	value, _ := bits.Div64(hi, lo, total)
	return value
}

type verdictDecision string

const (
	verdictApprove  verdictDecision = "approve"
	verdictContinue verdictDecision = "continue"
	verdictCorrect  verdictDecision = "correct"
	verdictPause    verdictDecision = "pause"
	verdictStop     verdictDecision = "stop"
)

type verdict struct {
	Decision     verdictDecision `json:"decision"`
	Anchor       anchor          `json:"anchor"`
	Rationale    string          `json:"rationale"`
	EvidenceRefs []string        `json:"evidence_refs,omitempty"`
	Correction   string          `json:"correction,omitempty"`
	Handoff      watchdogHandoff `json:"handoff,omitempty"`
}

type reviewerResult struct {
	Verdict verdict `json:"verdict"`
}

func decodeReviewerResult(raw []byte) (reviewerResult, error) {
	var result reviewerResult
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil {
		return reviewerResult{}, err
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return reviewerResult{}, errors.New("reviewer returned multiple JSON values")
		}
		return reviewerResult{}, fmt.Errorf("invalid trailing reviewer output: %w", err)
	}
	if err := result.Verdict.validate(); err != nil {
		return reviewerResult{}, err
	}
	return result, nil
}

func (v verdict) validate() error {
	if err := v.Anchor.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(v.Rationale) == "" || len(v.Rationale) > 4096 {
		return errors.New("verdict requires bounded rationale")
	}
	if len(v.EvidenceRefs) > maxEvidenceRefs {
		return errors.New("verdict has too many evidence references")
	}
	if err := v.Handoff.validate(); err != nil {
		return err
	}
	switch v.Decision {
	case verdictApprove, verdictContinue:
	case verdictCorrect:
		if strings.TrimSpace(v.Correction) == "" || len(v.Correction) > maxCorrectionSize {
			return errors.New("correction verdict requires bounded steering")
		}
	case verdictPause, verdictStop:
	default:
		return fmt.Errorf("invalid verdict decision %q", v.Decision)
	}
	if (v.Decision == verdictApprove || v.Decision == verdictCorrect || v.Decision == verdictPause || v.Decision == verdictStop) && len(v.EvidenceRefs) == 0 {
		return errors.New("authority-bearing verdict requires evidence references")
	}
	return nil
}

func (h watchdogHandoff) validate() error {
	total := 0
	for _, values := range [][]string{h.OpenConcerns, h.Hypotheses, h.Interventions, h.MissingEvidence, h.SuggestedProbes} {
		total += len(values)
		for _, value := range values {
			if !boundedRequired(value, 512) {
				return errors.New("watchdog handoff contains an invalid bounded item")
			}
		}
	}
	if total > 16 {
		return errors.New("watchdog handoff exceeds its 16-item durable bound")
	}
	return nil
}

// applyReviewerResult applies a successfully parsed verdict before looking at
// terminalDiagnostic. Provider cleanup and incomplete usage are terminal
// metadata: they are journalled, but cannot erase a valid semantic result.
func (s *runState) applyReviewerResult(result reviewerResult, terminalDiagnostic string) (transition, error) {
	if s == nil || s.PendingReview == nil {
		return transition{}, errors.New("no review is pending")
	}
	if s.PendingReview.Purpose == reviewPurposeVerifier {
		return s.applyCompletionVerifierResult(result, terminalDiagnostic)
	}
	if err := result.Verdict.validate(); err != nil {
		return transition{}, err
	}
	if result.Verdict.Anchor != s.PendingReview.Anchor {
		return transition{}, errors.New("reviewer verdict does not match the review anchor")
	}
	if terminalDiagnostic != "" {
		s.Diagnostics = appendBounded(s.Diagnostics, "review terminal: "+terminalDiagnostic, 32)
	}
	v := result.Verdict
	wasConfirming := s.PendingReview.Confirming
	reviewedStepClaim := reviewContainsSignal(s.PendingReview, "step_completion_claim")
	reviewedCompletion := reviewContainsSignal(s.PendingReview, "completion_claim")
	reviewedCorrection := reviewContainsSignal(s.PendingReview, "correction_follow_up")
	reviewedPivot := reviewContainsSignal(s.PendingReview, "pivot_request")
	reviewPivot := clonePivotCandidate(s.PendingReview.Pivot)
	s.PendingReview = nil
	stale := v.Anchor != s.CurrentAnchor
	if stale {
		if reviewedPivot {
			if s.PendingPivot == nil || reviewPivot == nil || s.PendingPivot.ReplacementDigest != reviewPivot.ReplacementDigest || s.PendingPivot.Stage != pivotStageReviewing {
				return transition{}, errors.New("stale pivot verdict has no exact durable proposal")
			}
			s.PendingPivot.Anchor = s.CurrentAnchor
			refresh := &reviewRequest{
				ID: "pivot-refresh-" + s.RunID + "-" + fmt.Sprint(s.CurrentAnchor.SessionSequence), Anchor: s.CurrentAnchor,
				Purpose: reviewPurposeWatchdog, Attempt: 1, Handoff: s.WatchdogHandoff, Pivot: clonePivotCandidate(s.PendingPivot),
				Signals: []signal{{Type: "pivot_request", Severity: "high", EvidenceRefs: append([]string(nil), v.EvidenceRefs...)}},
			}
			s.PendingReview = refresh
			actions := []action{{Kind: actionStartReview, Review: cloneReview(refresh), TokenBudget: s.Config.WatchdogTokenBudget}}
			if s.Hold == nil {
				s.Hold = &holdState{Reason: "fresh current-anchor review required for stale pivot verdict"}
				actions = append([]action{{Kind: actionAcquireHold, Reason: s.Hold.Reason}}, actions...)
			}
			return transition{Actions: actions, Note: "discarded stale pivot verdict and scheduled a fresh anchored review"}, nil
		}
		if reviewedCompletion && s.PendingCompletion != nil {
			s.PendingCompletion = nil
			var actions []action
			if v.Decision == verdictCorrect {
				actions = append(actions, action{Kind: actionSteer, Advisory: true, Correction: v.Correction, Reason: v.Rationale})
			}
			if s.Hold != nil {
				actions = append(actions, action{Kind: actionReleaseHold, Reason: "stale completion candidate invalidated"})
			}
			return transition{Actions: actions, Note: "invalidated stale completion candidate without inferring completion"}, nil
		}
		// A step claim is a current-anchor state transition, not evidence that
		// can be approved retroactively. Clear the exact old claim before the
		// generic asymmetric stale-verdict policy runs; the worker may submit a
		// fresh claim, while stale correction/pause/stop still retain their
		// ordinary advisory or confirmation semantics.
		if reviewedStepClaim && s.PendingStepClaim != nil {
			s.PendingStepClaim = nil
		}
		refreshHeldReview := func(actions []action) transition {
			review := &reviewRequest{
				ID:         "refresh-" + s.RunID + "-" + fmt.Sprint(s.CurrentAnchor.SessionSequence),
				Anchor:     s.CurrentAnchor,
				Confirming: wasConfirming,
				Purpose:    reviewPurposeWatchdog,
				Attempt:    1,
				Handoff:    s.WatchdogHandoff,
				Signals: []signal{{
					Type: "stale_held_review", Severity: "high",
					EvidenceRefs: append([]string(nil), v.EvidenceRefs...),
					Attributes:   map[string]string{"prior_decision": string(v.Decision), "prior_rationale": v.Rationale},
				}},
			}
			s.PendingReview = review
			actions = append(actions, action{Kind: actionStartReview, Review: cloneReview(review), TokenBudget: s.Config.WatchdogTokenBudget})
			return transition{Actions: actions, Note: "kept existing hold and refreshed stale review at current anchor"}
		}
		switch v.Decision {
		case verdictContinue, verdictApprove:
			if s.Hold != nil {
				return refreshHeldReview(nil), nil
			}
			return transition{Note: "discarded stale positive verdict"}, nil
		case verdictCorrect:
			steer := action{Kind: actionSteer, Advisory: true, Correction: v.Correction, Reason: v.Rationale}
			if s.Hold != nil {
				return refreshHeldReview([]action{steer}), nil
			}
			return transition{Actions: []action{steer}, Note: "stale correction retained only as earlier-anchor advice"}, nil
		case verdictPause, verdictStop:
			review := &reviewRequest{
				ID:     "confirm-" + s.RunID + "-" + fmt.Sprint(s.CurrentAnchor.SessionSequence),
				Anchor: s.CurrentAnchor, Confirming: true, Purpose: reviewPurposeWatchdog, Attempt: 1, Handoff: s.WatchdogHandoff,
				Signals: []signal{{Type: "stale_intervention", Severity: map[bool]string{true: "critical", false: "high"}[v.Decision == verdictStop], EvidenceRefs: append([]string(nil), v.EvidenceRefs...), Attributes: map[string]string{"prior_decision": string(v.Decision), "prior_rationale": v.Rationale}}},
			}
			holdReason := "fresh review required for stale " + string(v.Decision)
			s.PendingReview = review
			actions := make([]action, 0, 2)
			if s.Hold == nil {
				s.Hold = &holdState{Reason: holdReason}
				actions = append(actions, action{Kind: actionAcquireHold, Reason: holdReason})
			}
			actions = append(actions, action{Kind: actionStartReview, Review: cloneReview(review), TokenBudget: s.Config.WatchdogTokenBudget})
			return transition{Actions: actions, Note: "stale intervention held for current-anchor confirmation"}, nil
		}
	}
	s.LastVerdict = &v
	s.WatchdogHandoff = cloneWatchdogHandoff(v.Handoff)
	s.FailedEventStreak = 0
	if reviewedPivot {
		return s.applyPivotReviewerVerdict(v, reviewPivot)
	}

	var actions []action
	release := func() {
		if s.Hold != nil {
			actions = append(actions, action{Kind: actionReleaseHold, Reason: "current-anchor verdict resolved review hold"})
		}
	}
	switch v.Decision {
	case verdictContinue, verdictApprove:
		if reviewedCorrection {
			s.CorrectionFollowUp = nil
			s.FailedCorrectionCount = 0
		}
		if v.Decision == verdictApprove && reviewedCompletion {
			if s.PendingCompletion == nil || s.PendingCompletion.Anchor != v.Anchor {
				return transition{}, errors.New("approved completion review has no exact current candidate")
			}
			verifier := &reviewRequest{
				ID:     "verifier-" + s.RunID + "-" + fmt.Sprint(v.Anchor.SessionSequence),
				Anchor: v.Anchor, Purpose: reviewPurposeVerifier, Attempt: 1,
				Signals: []signal{{Type: "completion_verification", Severity: "critical", EvidenceRefs: append([]string(nil), s.PendingCompletion.EvidenceRefs...)}},
			}
			s.PendingCompletion.Stage = reviewPurposeVerifier
			s.PendingReview = verifier
			return transition{Actions: []action{{Kind: actionStartReview, Review: cloneReview(verifier), TokenBudget: s.Config.VerifierTokenBudget}}, Note: "watchdog approved candidate; started independent completion verifier"}, nil
		}
		if reviewedCompletion {
			s.PendingCompletion = nil
		}
		if v.Decision == verdictApprove && reviewedStepClaim && s.PendingStepClaim != nil && s.PendingStepClaim.Anchor == v.Anchor && s.PendingStepClaim.ActiveStep == s.CurrentAnchor.ActiveStep {
			if s.CompletedSteps < s.PlanTotal {
				s.CompletedSteps++
			}
			s.PendingStepClaim = nil
			if s.CompletedSteps < s.PlanTotal {
				s.CurrentAnchor.ActiveStep = s.PlanSteps[s.CompletedSteps]
			} else {
				s.CurrentAnchor.ActiveStep = "completion"
			}
		} else if reviewedStepClaim {
			s.PendingStepClaim = nil
		}
		release()
	case verdictCorrect:
		if reviewedCompletion {
			s.PendingCompletion = nil
		}
		if reviewedStepClaim {
			s.PendingStepClaim = nil
		}
		if reviewedCorrection {
			s.FailedCorrectionCount++
		}
		s.CorrectionFollowUp = &correctionFollowUpState{IssuedAt: v.Anchor, CorrectionRef: "sha256:" + digestString(v.Correction), EvidenceRefs: boundedModelEvidenceRefs(v.EvidenceRefs)}
		if s.FailedCorrectionCount >= s.Config.CorrectionLimit {
			s.CorrectionFollowUp = nil
			actions = append(actions, action{Kind: actionPause, Reason: "watchdog corrections did not restore alignment"})
		} else {
			release()
			actions = append(actions, action{Kind: actionSteer, Correction: v.Correction, Reason: v.Rationale})
		}
	case verdictPause:
		if reviewedCorrection {
			s.CorrectionFollowUp = nil
		}
		if reviewedCompletion {
			s.PendingCompletion = nil
		}
		if reviewedStepClaim {
			s.PendingStepClaim = nil
		}
		actions = append(actions, action{Kind: actionPause, Reason: v.Rationale})
	case verdictStop:
		if reviewedCorrection {
			s.CorrectionFollowUp = nil
		}
		if reviewedCompletion {
			s.PendingCompletion = nil
		}
		if reviewedStepClaim {
			s.PendingStepClaim = nil
		}
		actions = append(actions, action{Kind: actionStop, Reason: v.Rationale})
	}
	note := "applied current-anchor verdict"
	if wasConfirming {
		note = "applied fresh verdict after stale intervention hold"
	}
	return transition{Actions: actions, Note: note}, nil
}

// applyCompletionVerifierResult is deliberately separate from watchdog policy:
// the verifier is a fresh provider/agent instance and only its current-anchor
// approve verdict can mark the durable quality workflow complete. Terminal
// metadata is diagnostic and cannot erase a valid parsed verdict.
func (s *runState) applyCompletionVerifierResult(result reviewerResult, terminalDiagnostic string) (transition, error) {
	if s == nil || s.PendingReview == nil || s.PendingReview.Purpose != reviewPurposeVerifier || s.PendingCompletion == nil {
		return transition{}, errors.New("no completion verification is pending")
	}
	if err := result.Verdict.validate(); err != nil {
		return transition{}, err
	}
	if result.Verdict.Anchor != s.PendingReview.Anchor {
		return transition{}, errors.New("verifier verdict does not match the requested completion anchor")
	}
	if terminalDiagnostic != "" {
		s.Diagnostics = appendBounded(s.Diagnostics, "verifier terminal: "+terminalDiagnostic, 32)
	}
	v := result.Verdict
	s.PendingReview = nil
	release := func(reason string) []action {
		if s.Hold == nil {
			return nil
		}
		return []action{{Kind: actionReleaseHold, Reason: reason}}
	}
	if v.Anchor != s.CurrentAnchor || s.PendingCompletion.Anchor != s.CurrentAnchor {
		s.PendingCompletion = nil
		actions := release("stale completion verification invalidated")
		if v.Decision == verdictCorrect {
			actions = append(actions, action{Kind: actionSteer, Advisory: true, Correction: v.Correction, Reason: v.Rationale})
		}
		return transition{Actions: actions, Note: "discarded stale verifier result and resumed incomplete work"}, nil
	}
	s.LastVerdict = &v
	switch v.Decision {
	case verdictApprove:
		s.Completed = true
		s.QueuedSignals = nil
		completedAnchor := s.CurrentAnchor
		s.CompletedAnchor = &completedAnchor
		s.CompletionVerdict = &v
		s.PendingCompletion = nil
		return transition{Note: "independent verifier approved exact current completion; durable hold retained pending generic successful-completion handoff"}, nil
	case verdictContinue:
		s.PendingCompletion = nil
		return transition{Actions: release("independent verifier rejected completion"), Note: "independent verifier rejected completion; resumed work"}, nil
	case verdictCorrect:
		s.PendingCompletion = nil
		actions := release("independent verifier requested correction")
		actions = append(actions, action{Kind: actionSteer, Correction: v.Correction, Reason: v.Rationale})
		return transition{Actions: actions, Note: "independent verifier rejected completion with correction"}, nil
	case verdictPause:
		s.PendingCompletion = nil
		return transition{Actions: []action{{Kind: actionPause, Reason: v.Rationale}}, Note: "independent verifier paused incomplete work"}, nil
	case verdictStop:
		s.PendingCompletion = nil
		return transition{Actions: []action{{Kind: actionStop, Reason: v.Rationale}}, Note: "independent verifier stopped incomplete work"}, nil
	default:
		return transition{}, errors.New("unsupported completion verifier verdict")
	}
}

func (s *runState) applyReviewerFailure(status string, observedAt ...time.Time) (transition, error) {
	if s == nil || s.PendingReview == nil || !isTerminalAgentStatus(status) {
		return transition{}, errors.New("no terminal review failure is pending")
	}
	now := time.Now().UTC()
	if len(observedAt) > 0 {
		now = observedAt[0].UTC()
	}
	s.Diagnostics = appendBounded(s.Diagnostics, "review failed without a valid verdict: "+status, 32)
	pending := s.PendingReview
	if pending.Attempt == 0 {
		pending.Attempt = 1
	}
	boundedAttempts := pending.Purpose == reviewPurposeVerifier || s.Config.Mode == modeEvent
	if !boundedAttempts || pending.Attempt < uint64(s.Config.EventReviewRetries) {
		if pending.Attempt == ^uint64(0) {
			return transition{}, errors.New("review retry attempt counter exhausted")
		}
		pending.Attempt++
		pending.AgentID, pending.AgentOffset, pending.TokenBudget, pending.Terminal = "", 0, 0, nil
		pending.LastFailure = status
		pending.RetryAt = now.Add(reviewRetryDelay(s.Config, pending.Attempt-1, pending.Purpose))
		return transition{Actions: []action{{Kind: actionScheduleReviewRetry, Review: cloneReview(pending)}}, Note: "durably staged fresh reviewer retry"}, nil
	}
	if pending.Purpose == reviewPurposeVerifier {
		s.PendingReview = nil
		if s.Config.verifierRequired() {
			s.QueuedSignals = nil
			if s.PendingCompletion != nil {
				s.PendingCompletion.Stage = "verifier_failed"
			}
			return transition{Actions: []action{{Kind: actionPause, Reason: "required independent completion verifier failed without a valid verdict"}}, Note: "required completion verifier failed closed"}, nil
		}
		s.PendingCompletion = nil
		var actions []action
		if s.Hold != nil {
			actions = append(actions, action{Kind: actionReleaseHold, Reason: "advisory completion verifier unavailable"})
		}
		return transition{Actions: actions, Note: "advisory verifier failure invalidated completion and resumed work"}, nil
	}
	if reviewContainsSignal(pending, "step_completion_claim") {
		s.PendingStepClaim = nil
	}
	s.FailedEventStreak++
	if s.Hold != nil {
		s.PendingReview = nil
		if pending.Pivot != nil {
			s.PendingPivot = nil
		}
		s.QueuedSignals = nil
		return transition{Actions: []action{{Kind: actionPause, Reason: "required current-anchor review failed without a valid verdict"}}, Note: "held review failed closed"}, nil
	}
	s.PendingReview = nil
	if s.Config.AssuranceProfile == "high_assurance" || s.FailedEventStreak >= s.Config.FailedEventLimit {
		s.QueuedSignals = nil
		reason := fmt.Sprintf("watchdog failed for %d consecutive detector triggers", s.Config.FailedEventLimit)
		if s.Config.AssuranceProfile == "high_assurance" {
			reason = "high-assurance watchdog failed without a valid verdict"
		}
		return transition{Actions: []action{{Kind: actionPause, Reason: reason}}, Note: "watchdog failure policy paused the run"}, nil
	}
	return transition{Note: "advisory watchdog review failed without approval"}, nil
}

func reviewRetryDelay(cfg config, failedAttempt uint64, purpose string) time.Duration {
	if purpose == reviewPurposeVerifier || cfg.Mode == modeEvent {
		return 250 * time.Millisecond
	}
	value, maximum := uint64(cfg.LiveRetryBaseMillis), uint64(cfg.LiveRetryMaxMillis)
	for step := uint64(1); step < failedAttempt && value < maximum; step++ {
		if value > maximum/2 {
			value = maximum
		} else {
			value *= 2
		}
	}
	if value > maximum {
		value = maximum
	}
	return time.Duration(value) * time.Millisecond
}

func isTerminalAgentStatus(status string) bool {
	switch status {
	case "completed", "failed", "error", "cancelled", "timeout", "down", "budget_exhausted", "restart_exhausted":
		return true
	default:
		return false
	}
}

func reviewTokenBudget(cfg config, review *reviewRequest) int {
	if review != nil && review.Purpose == reviewPurposeVerifier {
		return cfg.VerifierTokenBudget
	}
	return cfg.WatchdogTokenBudget
}

func detectReviewResult(messages []agentMessage) ([]byte, string, bool) {
	var valid []byte
	var diagnostics []string
	for _, message := range messages {
		if message.Role != "assistant" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		candidate := []byte(strings.TrimSpace(message.Content))
		if _, err := decodeReviewerResult(candidate); err == nil {
			valid = append([]byte(nil), candidate...)
		} else {
			diagnostics = append(diagnostics, "ignored non-verdict assistant message")
		}
	}
	return valid, strings.Join(diagnostics, "; "), len(valid) != 0
}

type agentMessage struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`
	Source  string `json:"source,omitempty"`
	Offset  int    `json:"offset,omitempty"`
	Summary string `json:"summary,omitempty"`
}

func lastEvent(events []workerEvent, match func(workerEvent) bool) (workerEvent, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if match(events[i]) {
			return events[i], true
		}
	}
	return workerEvent{}, false
}

func urgentSignal(kind string) bool {
	switch kind {
	case "pivot_request", "risky_boundary", "completion_claim", "step_completion_claim", "stale_intervention", "correction_follow_up", "live_turn", "periodic_turn":
		return true
	default:
		return false
	}
}

func signalShape(item signal) string {
	keys := make([]string, 0, len(item.Attributes))
	for key := range item.Attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	out.WriteString(item.Type)
	for _, key := range keys {
		out.WriteByte('|')
		out.WriteString(key)
		out.WriteByte('=')
		out.WriteString(item.Attributes[key])
	}
	return out.String()
}

func reviewID(event workerEvent, signals []signal) string {
	var kinds []string
	for _, item := range signals {
		kinds = append(kinds, item.Type)
	}
	return "review-" + fmt.Sprint(event.Sequence) + "-" + strings.Join(kinds, "+")
}

func appendBoundedSignals(current, extra []signal) []signal {
	current = append(current, extra...)
	if len(current) > maxQueuedSignals {
		current = current[len(current)-maxQueuedSignals:]
	}
	return current
}

func appendBounded(current []string, value string, limit int) []string {
	current = append(current, value)
	if len(current) > limit {
		current = current[len(current)-limit:]
	}
	return current
}

func cloneReview(in *reviewRequest) *reviewRequest {
	if in == nil {
		return nil
	}
	out := *in
	out.Signals = append([]signal(nil), in.Signals...)
	out.Handoff = cloneWatchdogHandoff(in.Handoff)
	out.Pivot = clonePivotCandidate(in.Pivot)
	if in.Terminal != nil {
		terminal := *in.Terminal
		terminal.EvidenceRefs = append([]string(nil), in.Terminal.EvidenceRefs...)
		terminal.Scope.WritePaths = append([]string(nil), in.Terminal.Scope.WritePaths...)
		terminal.Scope.Violations = append([]string(nil), in.Terminal.Scope.Violations...)
		if in.Terminal.Changes != nil {
			changes := *in.Terminal.Changes
			changes.ChangedPaths = append([]string(nil), in.Terminal.Changes.ChangedPaths...)
			terminal.Changes = &changes
		}
		out.Terminal = &terminal
	}
	return &out
}

func cloneWatchdogHandoff(in watchdogHandoff) watchdogHandoff {
	return watchdogHandoff{
		OpenConcerns: append([]string(nil), in.OpenConcerns...), Hypotheses: append([]string(nil), in.Hypotheses...),
		Interventions: append([]string(nil), in.Interventions...), MissingEvidence: append([]string(nil), in.MissingEvidence...),
		SuggestedProbes: append([]string(nil), in.SuggestedProbes...),
	}
}

func reviewContainsSignal(review *reviewRequest, kind string) bool {
	if review == nil {
		return false
	}
	for _, item := range review.Signals {
		if item.Type == kind {
			return true
		}
	}
	return false
}

func boundedJoin(values []string, limit int) string {
	joined := strings.Join(values, ",")
	if len(joined) <= limit {
		return joined
	}
	return joined[:limit]
}
