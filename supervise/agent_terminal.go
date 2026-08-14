package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	agentDownFactsSchema     = "stado.dev/agent-down-facts/v1"
	maxAgentTerminalHistory  = 16
	maxAgentDownEvidenceRefs = 8
)

// authenticatedAgentParent is copied from the host-owned lifecycle envelope,
// never from agent.down data. The broker already scoped delivery to this exact
// application session/generation; retaining it in the journal makes the fact's
// parent binding explicit during restart and audit.
type authenticatedAgentParent struct {
	SessionID         string `json:"session_id"`
	SessionGeneration uint64 `json:"session_generation"`
	CanonicalRepoID   string `json:"canonical_repo_id,omitempty"`
}

func (p authenticatedAgentParent) validate() error {
	if !boundedRequired(p.SessionID, 256) || p.SessionGeneration == 0 || len(p.CanonicalRepoID) > 512 {
		return errors.New("agent.down requires an exact bounded authenticated parent session/generation")
	}
	return nil
}

type agentDownFacts struct {
	Schema   string                     `json:"schema"`
	Child    *agentDownChild            `json:"child"`
	Budget   *agentDownBudget           `json:"budget"`
	Terminal *agentDownTerminalMetadata `json:"terminal"`
	Scope    *agentDownScope            `json:"scope"`
	Changes  *agentDownChanges          `json:"changes,omitempty"`
	Failure  *agentDownFailure          `json:"failure,omitempty"`
}

type agentDownChild struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Role      string `json:"role,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Execution string `json:"execution,omitempty"`
}

type agentDownBudget struct {
	TokenLimit     uint64 `json:"token_limit,omitempty"`
	TurnLimit      uint64 `json:"turn_limit,omitempty"`
	TimeoutSeconds uint64 `json:"timeout_seconds,omitempty"`
}

// The pointer members distinguish a required false/zero value from a missing
// required field while decoding strict v1 JSON.
type agentDownTerminalMetadata struct {
	Usage         *agentTokenUsage        `json:"usage"`
	UsageComplete *bool                   `json:"usage_complete"`
	Cleanup       *agentCleanupDiagnostic `json:"cleanup,omitempty"`
}

type agentDownScope struct {
	Ownership           string   `json:"ownership,omitempty"`
	WritePaths          []string `json:"write_paths,omitempty"`
	WritePathsDigest    string   `json:"write_paths_digest,omitempty"`
	WritePathsTruncated bool     `json:"write_paths_truncated,omitempty"`
	Violations          []string `json:"violations,omitempty"`
	ViolationsDigest    string   `json:"violations_digest,omitempty"`
	ViolationsTruncated bool     `json:"violations_truncated,omitempty"`
}

type agentDownChanges struct {
	ForkTreeDigest        string   `json:"fork_tree_digest,omitempty"`
	ChangedPaths          []string `json:"changed_paths,omitempty"`
	ChangedPathsDigest    string   `json:"changed_paths_digest,omitempty"`
	ChangedPathsTruncated bool     `json:"changed_paths_truncated,omitempty"`
}

type agentDownFailure struct {
	Fingerprint string `json:"fingerprint"`
}

// reviewTerminalObservation is the application-owned reduction of generic
// host facts. InvalidReason is derived here from the signed review request
// (for example, a read-only verifier changed files); it is not supplied by the
// host and never becomes a native supervise conclusion.
type reviewTerminalObservation struct {
	BrokerSequence uint64                   `json:"broker_sequence"`
	Parent         authenticatedAgentParent `json:"parent"`
	ReviewID       string                   `json:"review_id,omitempty"`
	Purpose        string                   `json:"purpose,omitempty"`
	Child          agentDownChild           `json:"child"`
	Budget         agentDownBudget          `json:"budget"`
	Terminal       agentTerminalMetadata    `json:"terminal"`
	Scope          agentDownScope           `json:"scope"`
	Changes        *agentDownChanges        `json:"changes,omitempty"`
	Failure        *agentDownFailure        `json:"failure,omitempty"`
	EvidenceRefs   []string                 `json:"evidence_refs,omitempty"`
	InvalidReason  string                   `json:"invalid_reason,omitempty"`
}

func decodeAgentDownFacts(raw []byte) (agentDownFacts, error) {
	if len(raw) == 0 || len(raw) > 64<<10 {
		return agentDownFacts{}, errors.New("agent.down facts JSON must be 1..64 KiB")
	}
	var facts agentDownFacts
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&facts); err != nil {
		return agentDownFacts{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return agentDownFacts{}, errors.New("agent.down facts JSON has trailing value")
		}
		return agentDownFacts{}, err
	}
	if err := facts.validate(); err != nil {
		return agentDownFacts{}, err
	}
	return facts, nil
}

func (f agentDownFacts) validate() error {
	if f.Schema != agentDownFactsSchema {
		return fmt.Errorf("unsupported agent.down facts schema %q", f.Schema)
	}
	if f.Child == nil || f.Budget == nil || f.Terminal == nil || f.Scope == nil || f.Terminal.Usage == nil || f.Terminal.UsageComplete == nil {
		return errors.New("agent.down facts omit required child, budget, terminal, usage, usage_complete, or scope")
	}
	if !boundedRequired(f.Child.AgentID, 256) || !boundedRequired(f.Child.SessionID, 256) || len(f.Child.Role) > 256 || len(f.Child.Mode) > 256 || len(f.Child.Execution) > 256 {
		return errors.New("agent.down child identity exceeds bounds")
	}
	switch f.Child.Status {
	case "completed", "cancelled", "error", "failed", "timeout", "down":
	default:
		return fmt.Errorf("unknown agent.down child status %q", f.Child.Status)
	}
	terminal := f.terminalMetadata()
	if _, err := terminal.diagnostic(); err != nil {
		return err
	}
	if len(f.Scope.Ownership) > 256 || len(f.Scope.WritePaths) > 64 || len(f.Scope.Violations) > 32 {
		return errors.New("agent.down scope facts exceed bounds")
	}
	for _, value := range append(append([]string(nil), f.Scope.WritePaths...), f.Scope.Violations...) {
		if !boundedRequired(value, 128) {
			return errors.New("agent.down scope fact contains an invalid bounded entry")
		}
	}
	if err := validateFactDigest(f.Scope.WritePathsDigest, len(f.Scope.WritePaths) > 0 || f.Scope.WritePathsTruncated, "write paths"); err != nil {
		return err
	}
	if err := validateFactDigest(f.Scope.ViolationsDigest, len(f.Scope.Violations) > 0 || f.Scope.ViolationsTruncated, "scope violations"); err != nil {
		return err
	}
	if f.Changes != nil {
		if len(f.Changes.ChangedPaths) > 64 {
			return errors.New("agent.down change facts exceed path limit")
		}
		for _, value := range f.Changes.ChangedPaths {
			if !boundedRequired(value, 128) {
				return errors.New("agent.down change fact contains an invalid bounded path")
			}
		}
		if err := validateFactDigest(f.Changes.ForkTreeDigest, false, "fork tree"); err != nil {
			return err
		}
		if err := validateFactDigest(f.Changes.ChangedPathsDigest, len(f.Changes.ChangedPaths) > 0 || f.Changes.ChangedPathsTruncated, "changed paths"); err != nil {
			return err
		}
		if f.Changes.ForkTreeDigest == "" && f.Changes.ChangedPathsDigest == "" {
			return errors.New("agent.down changes object contains no immutable digest")
		}
	}
	if f.Failure != nil {
		if err := validateFactDigest(f.Failure.Fingerprint, true, "failure"); err != nil {
			return err
		}
	}
	return nil
}

func (f agentDownFacts) terminalMetadata() agentTerminalMetadata {
	return agentTerminalMetadata{Usage: *f.Terminal.Usage, UsageComplete: *f.Terminal.UsageComplete, Cleanup: f.Terminal.Cleanup}
}

func validateFactDigest(value string, required bool, label string) error {
	if value == "" && !required {
		return nil
	}
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return fmt.Errorf("agent.down %s digest is invalid", label)
	}
	for _, char := range strings.TrimPrefix(value, "sha256:") {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return fmt.Errorf("agent.down %s digest is invalid", label)
		}
	}
	return nil
}

func validateAgentDownEvidenceRefs(childID string, values []string, changes *agentDownChanges) error {
	if len(values) > maxAgentDownEvidenceRefs {
		return errors.New("agent.down evidence references exceed limit")
	}
	seen := map[string]bool{}
	hasTree := false
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value || len(value) > 1024 || seen[value] {
			return errors.New("agent.down contains an invalid or duplicate evidence reference")
		}
		seen[value] = true
		prefix := "git:refs/sessions/" + childID + "/"
		if !strings.HasPrefix(value, prefix) {
			return errors.New("agent.down evidence reference is not bound to the terminal child")
		}
		coordinate := strings.TrimPrefix(value, prefix)
		kind, commit, ok := strings.Cut(coordinate, "@")
		if !ok || strings.Contains(commit, "@") || (kind != "tree" && kind != "trace") || !isLowerHex(commit, 40) {
			return errors.New("agent.down evidence reference is not an immutable child tree/trace coordinate")
		}
		if kind == "tree" {
			hasTree = true
		}
	}
	if changes != nil && !hasTree {
		return errors.New("agent.down change facts require an immutable child tree reference")
	}
	return nil
}

func isLowerHex(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func (s *runState) observeAgentDown(parent authenticatedAgentParent, brokerSequence uint64, evidenceRefs []string, facts agentDownFacts) (bool, error) {
	if s == nil || s.Schema != policySchema || s.RunID == "" {
		return false, errors.New("supervision state unavailable for agent.down")
	}
	if err := parent.validate(); err != nil {
		return false, err
	}
	if brokerSequence == 0 {
		return false, errors.New("agent.down requires a broker sequence")
	}
	if err := facts.validate(); err != nil {
		return false, err
	}
	if err := validateAgentDownEvidenceRefs(facts.Child.SessionID, evidenceRefs, facts.Changes); err != nil {
		return false, err
	}
	if brokerSequence < s.AgentDownSequence {
		return false, errors.New("agent.down broker sequence moved backwards")
	}
	if brokerSequence == s.AgentDownSequence {
		if s.PendingReview != nil && s.PendingReview.Terminal != nil &&
			s.PendingReview.Terminal.BrokerSequence == brokerSequence &&
			s.PendingReview.Terminal.Child.AgentID == facts.Child.AgentID &&
			s.PendingReview.Terminal.Child.SessionID == facts.Child.SessionID {
			return true, nil
		}
		route := s.operatorInputReviewByChild(facts.Child.AgentID, facts.Scope.Ownership)
		return route != nil && route.Terminal != nil && route.Terminal.BrokerSequence == brokerSequence, nil
	}

	observation := reviewTerminalObservation{
		BrokerSequence: brokerSequence, Parent: parent,
		Child: *facts.Child, Budget: *facts.Budget, Terminal: facts.terminalMetadata(),
		Scope: *facts.Scope, Changes: facts.Changes, Failure: facts.Failure,
		EvidenceRefs: append([]string(nil), evidenceRefs...),
	}
	if s.PendingReview != nil && s.PendingReview.AgentID == "" && facts.Scope.Ownership == reviewAgentOwnership(s.RunID, s.PendingReview) {
		// The pending review and its unique spawn intent were durable before
		// admission. Recover the child identity from authenticated terminal facts
		// when a callback lost the successful spawn reply.
		s.PendingReview.AgentID = facts.Child.AgentID
		s.PendingReview.AgentOffset = 0
		s.PendingReview.TokenBudget = reviewTokenBudget(s.Config, s.PendingReview)
	}
	matchingReview := s.PendingReview != nil && s.PendingReview.AgentID == facts.Child.AgentID
	matchingInput := false
	if matchingReview {
		observation.ReviewID = s.PendingReview.ID
		observation.Purpose = s.PendingReview.Purpose
		observation.InvalidReason = reviewTerminalInvalidReason(s.RunID, s.PendingReview, s.Config.WatchdogTimeoutSecond, observation)
		s.PendingReview.Terminal = &observation
		if s.PendingReview.Purpose == reviewPurposeVerifier {
			s.VerifierTokenUsage.add(observation.Terminal.Usage)
		} else {
			s.WatchdogTokenUsage.add(observation.Terminal.Usage)
		}
		for _, diagnostic := range observation.diagnostics() {
			s.Diagnostics = appendBounded(s.Diagnostics, "review agent.down: "+diagnostic, 32)
		}
	} else if route := s.operatorInputReviewByChild(facts.Child.AgentID, facts.Scope.Ownership); route != nil {
		if route.AgentID == "" {
			route.AgentID, route.AgentOffset, route.TokenBudget = facts.Child.AgentID, 0, s.Config.WatchdogTokenBudget
		}
		observation.ReviewID = route.ReviewID
		observation.Purpose = reviewPurposeOperatorInput
		observation.InvalidReason = operatorInputTerminalInvalidReason(s.RunID, route, s.Config.WatchdogTimeoutSecond, observation)
		route.Terminal = &observation
		s.WatchdogTokenUsage.add(observation.Terminal.Usage)
		for _, diagnostic := range observation.diagnostics() {
			s.Diagnostics = appendBounded(s.Diagnostics, "operator-input reviewer agent.down: "+diagnostic, 32)
		}
		matchingInput = true
	}
	s.AgentDownSequence = brokerSequence
	s.AgentTerminals = append(s.AgentTerminals, observation)
	if len(s.AgentTerminals) > maxAgentTerminalHistory {
		s.AgentTerminals = append([]reviewTerminalObservation(nil), s.AgentTerminals[len(s.AgentTerminals)-maxAgentTerminalHistory:]...)
	}
	return matchingReview || matchingInput, nil
}

func operatorInputTerminalInvalidReason(runID string, route *operatorInputRouteState, expectedTimeoutSeconds int, observation reviewTerminalObservation) string {
	if route == nil {
		return "terminal child is not bound to an operator-input review"
	}
	if observation.Child.Role != "explorer" || observation.Child.Mode != "read_only" || observation.Child.Execution != "wait" || observation.Scope.Ownership != operatorInputReviewOwnership(runID, route) {
		return "operator-input review child identity, read-only mode, execution, or ownership did not match the signed spawn request"
	}
	if route.TokenBudget > 0 && observation.Budget.TokenLimit != uint64(route.TokenBudget) {
		return "operator-input review token limit did not match the admitted request"
	}
	if observation.Budget.TurnLimit != 4 {
		return "operator-input review turn limit did not match the admitted request"
	}
	if expectedTimeoutSeconds < 1 || observation.Budget.TimeoutSeconds != uint64(expectedTimeoutSeconds) {
		return "operator-input review timeout did not match the admitted request"
	}
	if len(observation.Scope.WritePaths) > 0 || observation.Scope.WritePathsDigest != "" || observation.Scope.WritePathsTruncated || len(observation.Scope.Violations) > 0 || observation.Scope.ViolationsDigest != "" || observation.Scope.ViolationsTruncated {
		return "read-only operator-input reviewer reported write scope or a scope violation"
	}
	if observation.Changes != nil && (len(observation.Changes.ChangedPaths) > 0 || observation.Changes.ChangedPathsDigest != "" || observation.Changes.ChangedPathsTruncated) {
		return "read-only operator-input reviewer changed repository paths"
	}
	return ""
}

func reviewTerminalInvalidReason(runID string, review *reviewRequest, expectedTimeoutSeconds int, observation reviewTerminalObservation) string {
	if review == nil {
		return "terminal child is not bound to a pending review"
	}
	expectedOwnership := reviewAgentOwnership(runID, review)
	if observation.Child.Role != "explorer" || observation.Child.Mode != "read_only" || observation.Child.Execution != "wait" || observation.Scope.Ownership != expectedOwnership {
		return "review child identity, read-only mode, execution, or ownership did not match the signed spawn request"
	}
	if review.TokenBudget > 0 && observation.Budget.TokenLimit != uint64(review.TokenBudget) {
		return "review child token limit did not match the admitted request"
	}
	if observation.Budget.TurnLimit != 4 {
		return "review child turn limit did not match the admitted request"
	}
	if expectedTimeoutSeconds < 1 || observation.Budget.TimeoutSeconds != uint64(expectedTimeoutSeconds) {
		return "review child timeout did not match the admitted request"
	}
	if len(observation.Scope.WritePaths) > 0 || observation.Scope.WritePathsDigest != "" || observation.Scope.WritePathsTruncated {
		return "read-only review child reported a write scope"
	}
	if len(observation.Scope.Violations) > 0 || observation.Scope.ViolationsDigest != "" || observation.Scope.ViolationsTruncated {
		return "read-only review child reported a scope violation"
	}
	if observation.Changes != nil && (len(observation.Changes.ChangedPaths) > 0 || observation.Changes.ChangedPathsDigest != "" || observation.Changes.ChangedPathsTruncated) {
		return "read-only review child changed repository paths"
	}
	return ""
}

func (o reviewTerminalObservation) diagnostics() []string {
	var out []string
	if diagnostic, err := o.Terminal.diagnostic(); err == nil && diagnostic != "" {
		out = append(out, diagnostic)
	}
	if o.Child.Status != "completed" {
		out = append(out, "terminal child status: "+o.Child.Status)
	}
	if o.Failure != nil {
		out = append(out, "failure "+o.Failure.Fingerprint)
	}
	if o.Scope.ViolationsDigest != "" {
		out = append(out, "scope violations "+o.Scope.ViolationsDigest)
	}
	if o.Changes != nil && o.Changes.ChangedPathsDigest != "" {
		out = append(out, "changed paths "+o.Changes.ChangedPathsDigest)
	}
	if o.InvalidReason != "" {
		out = append(out, "review facts rejected: "+o.InvalidReason)
	}
	return out
}

// agentTerminalMetadata is a broker fact, not reviewer prose. Token counters
// are kept granular and token-only; cleanup is a bounded fingerprint that is
// diagnostic and can never replace an otherwise valid verdict.
type agentTerminalMetadata struct {
	Usage         agentTokenUsage         `json:"usage"`
	UsageComplete bool                    `json:"usage_complete"`
	Cleanup       *agentCleanupDiagnostic `json:"cleanup,omitempty"`
}

type agentTokenUsage struct {
	InputTokens      uint64 `json:"input_tokens"`
	OutputTokens     uint64 `json:"output_tokens"`
	CacheReadTokens  uint64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens uint64 `json:"cache_write_tokens,omitempty"`
}

type agentCleanupDiagnostic struct {
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
}

type agentMessages struct {
	Messages []agentMessage         `json:"messages"`
	Offset   int                    `json:"offset"`
	Status   string                 `json:"status"`
	Terminal *agentTerminalMetadata `json:"terminal,omitempty"`
}

func (t *agentTerminalMetadata) diagnostic() (string, error) {
	if t == nil {
		return "", errors.New("terminal reviewer result omitted host terminal metadata")
	}
	var diagnostics []string
	if !t.UsageComplete {
		diagnostics = append(diagnostics, "host token usage is incomplete")
	}
	if t.Cleanup != nil {
		if strings.TrimSpace(t.Cleanup.Kind) == "" || strings.TrimSpace(t.Cleanup.Kind) != t.Cleanup.Kind || len(t.Cleanup.Kind) > 256 {
			return "", errors.New("host returned malformed bounded cleanup metadata")
		}
		if err := validateFactDigest(t.Cleanup.Fingerprint, true, "cleanup"); err != nil {
			return "", errors.New("host returned malformed bounded cleanup metadata")
		}
		diagnostics = append(diagnostics, fmt.Sprintf("%s cleanup %s", t.Cleanup.Kind, t.Cleanup.Fingerprint))
	}
	return strings.Join(diagnostics, "; "), nil
}

func (u *agentTokenUsage) add(other agentTokenUsage) {
	u.InputTokens = saturatingTokenSum(u.InputTokens, other.InputTokens)
	u.OutputTokens = saturatingTokenSum(u.OutputTokens, other.OutputTokens)
	u.CacheReadTokens = saturatingTokenSum(u.CacheReadTokens, other.CacheReadTokens)
	u.CacheWriteTokens = saturatingTokenSum(u.CacheWriteTokens, other.CacheWriteTokens)
}

func joinDiagnostics(values ...string) string {
	var nonempty []string
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			nonempty = append(nonempty, value)
		}
	}
	return strings.Join(nonempty, "; ")
}

// consumeReviewerMessages interprets only terminal agent output. Waiting for a
// terminal fact guarantees that provider cleanup and measured token usage are
// observed together with the semantic result. The host enforces the per-spawn
// cap; this plugin journals the counters for audit without inventing a second
// currency or budget authority.
func (s *runState) consumeReviewerMessages(messages agentMessages, observedAt ...time.Time) (transition, bool, error) {
	if s == nil || s.PendingReview == nil {
		return transition{}, false, errors.New("no reviewer result is pending")
	}
	if !isTerminalAgentStatus(messages.Status) {
		return transition{}, false, nil
	}
	// Agent polling supplies semantic output, but the durable authenticated
	// agent.down event supplies the child/session binding and terminal facts.
	// A timer racing ahead of that event must wait without consuming output.
	observed := s.PendingReview.Terminal
	if observed == nil {
		return transition{}, false, nil
	}
	if observed.Child.AgentID != s.PendingReview.AgentID || observed.ReviewID != s.PendingReview.ID || observed.Purpose != s.PendingReview.Purpose {
		return transition{}, false, errors.New("review terminal observation is not bound to the pending child")
	}
	if messages.Terminal == nil {
		return transition{}, false, errors.New("terminal reviewer read omitted host terminal metadata")
	}
	terminalDiagnostic, err := observed.Terminal.diagnostic()
	if err != nil {
		return transition{}, false, err
	}
	messageTerminalDiagnostic, err := messages.Terminal.diagnostic()
	if err != nil {
		return transition{}, false, err
	}
	if !equalTerminalMetadata(observed.Terminal, *messages.Terminal) {
		return transition{}, false, errors.New("agent.down and agent read terminal facts disagree")
	}
	verdictRaw, messageDiagnostic, valid := detectReviewResult(messages.Messages)
	combinedDiagnostic := joinDiagnostics(messageDiagnostic, terminalDiagnostic, messageTerminalDiagnostic)
	if messages.Status != "completed" {
		combinedDiagnostic = joinDiagnostics(combinedDiagnostic, "terminal agent status: "+messages.Status)
	}
	if observed.Child.Status != "completed" && observed.Child.Status != messages.Status {
		combinedDiagnostic = joinDiagnostics(combinedDiagnostic, "agent.down child status: "+observed.Child.Status)
	}
	var change transition
	if observed.InvalidReason != "" || messages.Status != "completed" || observed.Child.Status != "completed" {
		change, err = s.applyReviewerFailure(messages.Status, observedAt...)
	} else if valid {
		result, decodeErr := decodeReviewerResult(verdictRaw)
		if decodeErr != nil {
			return transition{}, false, decodeErr
		}
		change, err = s.applyReviewerResult(result, combinedDiagnostic)
	} else {
		change, err = s.applyReviewerFailure(messages.Status, observedAt...)
		if err == nil && combinedDiagnostic != "" {
			s.Diagnostics = appendBounded(s.Diagnostics, "review terminal: "+combinedDiagnostic, 32)
		}
	}
	if err != nil {
		return transition{}, false, err
	}
	// Token facts were accounted exactly once when agent.down was reduced. The
	// read response is equality-checked above, never added a second time.
	return change, true, nil
}

func equalTerminalMetadata(a, b agentTerminalMetadata) bool {
	if a.Usage != b.Usage || a.UsageComplete != b.UsageComplete {
		return false
	}
	if a.Cleanup == nil || b.Cleanup == nil {
		return a.Cleanup == nil && b.Cleanup == nil
	}
	return *a.Cleanup == *b.Cleanup
}
