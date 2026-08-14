package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

const turnFactsSchema = "stado.dev/session-turn-facts/v1"

// turnCommittedFacts is a bounded host-authenticated observation envelope.
// None of these fields says whether work is stalled, in scope, risky, a pivot,
// or acceptable completion; those interpretations belong to this plugin.
type turnCommittedFacts struct {
	Schema        string                `json:"schema"`
	Anchor        hostTurnAnchor        `json:"anchor"`
	Tools         []toolOutcomeFact     `json:"tool_outcomes,omitempty"`
	Provider      *providerTokenFacts   `json:"provider_tokens"`
	Verifications []verificationFact    `json:"verification_facts,omitempty"`
	Tree          *treeDiffFact         `json:"tree_diff,omitempty"`
	Assistant     *assistantMessageFact `json:"assistant"`
}

type hostTurnAnchor struct {
	SessionSequence uint64 `json:"session_sequence"`
	TurnRef         string `json:"turn_ref"`
	TreeDigest      string `json:"tree_digest"`
}

type toolOutcomeFact struct {
	ID               string   `json:"id"`
	Tool             string   `json:"tool"`
	Class            string   `json:"class,omitempty"`
	CallDigest       string   `json:"call_digest"`
	ArgsDigest       string   `json:"args_digest,omitempty"`
	ResultDigest     string   `json:"result_digest,omitempty"`
	Outcome          string   `json:"outcome"`
	ErrorFingerprint string   `json:"error_fingerprint,omitempty"`
	EvidenceRefs     []string `json:"evidence_refs,omitempty"`
}

type providerTokenFacts struct {
	InputTokens     uint64 `json:"input_tokens"`
	OutputTokens    uint64 `json:"output_tokens"`
	CachedTokens    uint64 `json:"cached_tokens,omitempty"`
	RunBudgetTokens uint64 `json:"budget_tokens,omitempty"`
}

type verificationFact struct {
	ID            string   `json:"id"`
	CommandDigest string   `json:"command_digest"`
	ResultDigest  string   `json:"result_digest"`
	Outcome       string   `json:"outcome"`
	EvidenceRefs  []string `json:"evidence_refs,omitempty"`
}

type treeDiffFact struct {
	BeforeDigest string   `json:"before_digest"`
	AfterDigest  string   `json:"after_digest"`
	DiffRef      string   `json:"diff_ref"`
	DiffDigest   string   `json:"diff_digest"`
	ChangedPaths []string `json:"changed_paths,omitempty"`
	Bytes        int64    `json:"bytes,omitempty"`
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
}

type assistantMessageFact struct {
	MessageRef string `json:"message_ref"`
	Digest     string `json:"digest"`
	Excerpt    string `json:"excerpt,omitempty"`
}

// applyTurnCommittedFacts folds one mandatory broker event atomically into the
// application-owned policy state. The broker cursor ACK is a separate effect,
// so the durable state records the exact broker sequence and payload digest.
// A callback timeout after this fold can then replay pending effects and ACK
// the same event without double-counting a turn or detector signal.
func (s *runState) applyTurnCommittedFacts(brokerSequence uint64, facts turnCommittedFacts, factsDigest string) (transition, []string, bool, error) {
	if s == nil || brokerSequence == 0 || len(factsDigest) != len("sha256:")+64 || !strings.HasPrefix(factsDigest, "sha256:") || !isLowerHex(strings.TrimPrefix(factsDigest, "sha256:"), 64) {
		return transition{}, nil, false, errors.New("turn event requires an exact broker sequence and payload digest")
	}
	if brokerSequence == s.LastTurnEventSequence {
		if factsDigest != s.LastTurnFactsDigest {
			return transition{}, nil, false, errors.New("turn event sequence was replayed with different authenticated facts")
		}
		return transition{}, nil, true, nil
	}
	if brokerSequence < s.LastTurnEventSequence {
		return transition{}, nil, false, errors.New("turn event broker sequence moved backwards")
	}
	events, err := s.deriveWorkerEvents(facts)
	if err != nil {
		return transition{}, nil, false, err
	}
	var combined transition
	var evidence []string
	for _, event := range events {
		change, err := s.observe(event)
		if err != nil {
			return transition{}, nil, false, err
		}
		combined.Actions = append(combined.Actions, change.Actions...)
		if change.Note != "" {
			combined.Note = change.Note
		}
		evidence = append(evidence, event.ID)
	}
	s.LastTurnEventSequence = brokerSequence
	s.LastTurnFactsDigest = factsDigest
	return combined, boundedModelEvidenceRefs(evidence), false, nil
}

func decodeTurnCommittedFacts(raw []byte) (turnCommittedFacts, error) {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return turnCommittedFacts{}, errors.New("turn facts JSON must be 1..1 MiB")
	}
	var facts turnCommittedFacts
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&facts); err != nil {
		return turnCommittedFacts{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return turnCommittedFacts{}, errors.New("turn facts JSON has trailing value")
		}
		return turnCommittedFacts{}, err
	}
	if err := facts.validate(); err != nil {
		return turnCommittedFacts{}, err
	}
	return facts, nil
}

func (f turnCommittedFacts) validate() error {
	if f.Schema != turnFactsSchema {
		return fmt.Errorf("unsupported turn facts schema %q", f.Schema)
	}
	if f.Anchor.SessionSequence == 0 || !boundedRequired(f.Anchor.TurnRef, 512) || !boundedRequired(f.Anchor.TreeDigest, 512) {
		return errors.New("turn facts require a bounded authenticated anchor")
	}
	if len(f.Tools) > 128 || len(f.Verifications) > 64 {
		return errors.New("turn facts exceed observation count limits")
	}
	if f.Provider == nil {
		return errors.New("turn facts require provider token facts")
	}
	if f.Assistant == nil || !boundedRequired(f.Assistant.MessageRef, 512) || !boundedRequired(f.Assistant.Digest, 512) || len(f.Assistant.Excerpt) > 4096 {
		return errors.New("turn facts require bounded assistant identity and digest")
	}
	for _, item := range f.Tools {
		if !boundedRequired(item.ID, 256) || !boundedRequired(item.Tool, 256) || !boundedRequired(item.CallDigest, 512) || !boundedRequired(item.ArgsDigest, 512) || !boundedRequired(item.ResultDigest, 512) || len(item.Class) > 64 || len(item.ErrorFingerprint) > 512 {
			return errors.New("invalid bounded tool outcome fact")
		}
		switch item.Outcome {
		case "success", "error", "denied", "cancelled":
		default:
			return fmt.Errorf("unknown tool outcome %q", item.Outcome)
		}
		if err := validateEvidenceRefs(item.EvidenceRefs); err != nil {
			return err
		}
	}
	for _, item := range f.Verifications {
		if !boundedRequired(item.ID, 256) || !boundedRequired(item.CommandDigest, 512) || !boundedRequired(item.ResultDigest, 512) {
			return errors.New("invalid bounded verification fact")
		}
		switch item.Outcome {
		case "pass", "fail", "error", "cancelled":
		default:
			return fmt.Errorf("unknown verification outcome %q", item.Outcome)
		}
		if err := validateEvidenceRefs(item.EvidenceRefs); err != nil {
			return err
		}
	}
	if f.Tree != nil {
		if !boundedRequired(f.Tree.BeforeDigest, 512) || !boundedRequired(f.Tree.AfterDigest, 512) || !boundedRequired(f.Tree.DiffRef, 512) || !boundedRequired(f.Tree.DiffDigest, 512) || f.Tree.Bytes < 0 || f.Tree.Bytes > 64<<10 || f.Tree.AfterDigest != f.Anchor.TreeDigest || len(f.Tree.ChangedPaths) > 64 {
			return errors.New("invalid bounded tree/diff fact")
		}
		for _, value := range f.Tree.ChangedPaths {
			if !validRepoPath(value) {
				return errors.New("tree fact contains invalid repository path")
			}
		}
		if err := validateEvidenceRefs(f.Tree.EvidenceRefs); err != nil {
			return err
		}
	}
	return nil
}

func (s *runState) deriveWorkerEvents(f turnCommittedFacts) ([]workerEvent, error) {
	if err := f.validate(); err != nil {
		return nil, err
	}
	if s.CurrentAnchor.SessionSequence != 0 && f.Anchor.SessionSequence <= s.CurrentAnchor.SessionSequence {
		return nil, errors.New("turn facts session sequence did not advance")
	}
	reviewAnchor := anchor{
		SessionSequence: f.Anchor.SessionSequence,
		PlanVersion:     s.CurrentAnchor.PlanVersion, ActiveStep: s.CurrentAnchor.ActiveStep,
		TreeDigest: f.Anchor.TreeDigest, TurnRef: f.Anchor.TurnRef,
	}
	if reviewAnchor.PlanVersion == 0 {
		reviewAnchor.PlanVersion = 1
	}
	sequence := s.ObservationSequence
	next := func(kind workerEventKind, id string) workerEvent {
		sequence++
		return workerEvent{ID: id, Kind: kind, Sequence: sequence, Anchor: reviewAnchor}
	}
	var events []workerEvent
	for _, fact := range f.Tools {
		event := next(eventToolOutcome, "tool:"+fact.ID)
		event.Tool, event.ArgsDigest, event.ErrorFingerprint = fact.Tool, fact.ArgsDigest, fact.ErrorFingerprint
		event.Succeeded = fact.Outcome == "success"
		event.EvidenceRefs = boundedFactRefs(fact.EvidenceRefs)
		events = append(events, event)
		if toolNeedsRiskReview(fact) {
			risk := next(eventRiskBoundary, "risk:"+fact.ID)
			risk.Boundary = fact.Class + ":" + fact.Tool
			risk.EvidenceRefs = boundedFactRefs(fact.EvidenceRefs)
			events = append(events, risk)
		}
	}
	for _, fact := range f.Verifications {
		event := next(eventVerification, "verification:"+fact.ID)
		passed := fact.Outcome == "pass"
		event.VerificationPassed = &passed
		event.VerificationCommandDigest = fact.CommandDigest
		event.VerificationResultDigest = fact.ResultDigest
		event.EvidenceRefs = boundedFactRefs(fact.EvidenceRefs)
		events = append(events, event)
	}
	if f.Tree != nil {
		event := next(eventTreeChanged, "tree:"+f.Tree.DiffRef)
		event.ChangedPathCount = len(f.Tree.ChangedPaths)
		event.ChangedPaths = boundedPaths(f.Tree.ChangedPaths)
		event.OutOfScopePaths = boundedPaths(s.outOfScopePaths(f.Tree.ChangedPaths))
		event.DiffBytes = f.Tree.Bytes
		event.EvidenceRefs = boundedFactRefs(f.Tree.EvidenceRefs)
		events = append(events, event)
	}
	if assistantRequestsPivot(f.Assistant.Excerpt) {
		event := next(eventPivotRequested, "pivot:"+f.Assistant.MessageRef)
		event.EvidenceRefs = []string{f.Assistant.MessageRef}
		events = append(events, event)
	}
	turn := next(eventTurnCompleted, "turn:"+f.Anchor.TurnRef)
	turn.CompletedSteps = s.CompletedSteps
	turn.EvidenceCount = countEvidence(f)
	turn.TokenUsage = saturatingTokenSum(s.CumulativeWorkerTokens, f.Provider.InputTokens, f.Provider.OutputTokens)
	turn.TokenBudget = f.Provider.RunBudgetTokens
	turn.EvidenceRefs = collectEvidenceRefs(f, 4)
	events = append(events, turn)
	return events, nil
}

func (s *runState) outOfScopePaths(changed []string) []string {
	if len(s.Config.AllowedPathPrefixes) == 0 {
		return nil
	}
	var out []string
	for _, value := range changed {
		allowed := false
		for _, prefix := range s.Config.AllowedPathPrefixes {
			if value == prefix || strings.HasPrefix(value, strings.TrimSuffix(prefix, "/")+"/") {
				allowed = true
				break
			}
		}
		if !allowed {
			out = append(out, value)
		}
	}
	return out
}

func toolNeedsRiskReview(f toolOutcomeFact) bool {
	class := strings.ToLower(f.Class)
	if class != "exec" && class != "mutating" {
		return false
	}
	name := strings.ToLower(f.Tool)
	for _, fragment := range []string{"push", "merge", "release", "publish", "deploy", "delete", "destroy"} {
		if strings.Contains(name, fragment) {
			return true
		}
	}
	return false
}

func assistantRequestsPivot(excerpt string) bool {
	text := strings.ToLower(excerpt)
	for _, phrase := range []string{"change the plan", "revise the plan", "need to pivot", "change the objective", "revise the objective"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func countEvidence(f turnCommittedFacts) int {
	return len(collectEvidenceRefs(f, 0))
}

func collectEvidenceRefs(f turnCommittedFacts, limit int) []string {
	seen := map[string]struct{}{}
	var result []string
	add := func(values []string) {
		for _, value := range values {
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			if limit == 0 || len(result) < limit {
				result = append(result, value)
			}
		}
	}
	for _, item := range f.Tools {
		add(item.EvidenceRefs)
	}
	for _, item := range f.Verifications {
		add(item.EvidenceRefs)
	}
	if f.Tree != nil {
		add(f.Tree.EvidenceRefs)
	}
	if limit == 0 {
		return result
	}
	return result
}

func boundedFactRefs(values []string) []string {
	if len(values) > 4 {
		values = values[:4]
	}
	return append([]string(nil), values...)
}

func boundedPaths(values []string) []string {
	if len(values) > 64 {
		values = values[:64]
	}
	return append([]string(nil), values...)
}

func saturatingTokenSum(values ...uint64) uint64 {
	var total uint64
	for _, value := range values {
		if ^uint64(0)-total < value {
			return ^uint64(0)
		}
		total += value
	}
	return total
}

func validateEvidenceRefs(values []string) error {
	if len(values) > maxEvidenceRefs {
		return errors.New("fact has too many evidence references")
	}
	for _, value := range values {
		if !boundedRequired(value, 1024) {
			return errors.New("fact contains invalid evidence reference")
		}
	}
	return nil
}

func boundedRequired(value string, limit int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= limit
}

func validRepoPath(value string) bool {
	return boundedRequired(value, 512) && !strings.HasPrefix(value, "/") && path.Clean(value) == value && value != "." && value != ".." && !strings.HasPrefix(value, "../")
}

func normalizeAllowedPrefixes(values []string) ([]string, error) {
	result := append([]string(nil), values...)
	for _, value := range result {
		if !validRepoPath(strings.TrimSuffix(value, "/")) {
			return nil, fmt.Errorf("invalid allowed path prefix %q", value)
		}
	}
	sort.Strings(result)
	return result, nil
}
