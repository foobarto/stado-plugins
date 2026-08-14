package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	maxGuidanceItems    = 3
	maxGuidanceBytes    = 1200
	hostFactBufferBytes = 1 << 20
	maxCatalogTools     = 4096
)

type catalogRequest struct {
	Offset         int    `json:"offset,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	ExpectedDigest string `json:"expected_digest,omitempty"`
}

type catalogTool struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

type catalogResponse struct {
	Schema         string        `json:"schema"`
	RegistryDigest string        `json:"registry_digest"`
	NextOffset     *int          `json:"next_offset,omitempty"`
	Tools          []catalogTool `json:"tools"`
}

var guidanceToolNames = map[string]bool{
	"agent__list": true, "agent__read_messages": true,
	"agent__send_message": true, "session__research": true,
	"memory__research": true,
}

func availableToolsFromCatalog(read func(catalogRequest) (catalogResponse, error)) (map[string]bool, error) {
	available := make(map[string]bool)
	offset := 0
	digest := ""
	for page := 0; page < maxCatalogTools; page++ {
		response, err := read(catalogRequest{Offset: offset, Limit: 1, ExpectedDigest: digest})
		if err != nil {
			return nil, err
		}
		if response.Schema != "stado.dev/registry-catalog/v1" || response.RegistryDigest == "" || (digest != "" && response.RegistryDigest != digest) {
			return nil, fmt.Errorf("invalid registry catalog snapshot")
		}
		digest = response.RegistryDigest
		for _, candidate := range response.Tools {
			if guidanceToolNames[candidate.Name] {
				available[candidate.Name] = true
			}
		}
		if len(available) == len(guidanceToolNames) || response.NextOffset == nil {
			return available, nil
		}
		if *response.NextOffset <= offset {
			return nil, fmt.Errorf("registry catalog did not advance")
		}
		offset = *response.NextOffset
	}
	return nil, fmt.Errorf("registry catalog exceeds guidance tool bound")
}

type qualityFacts struct {
	CurrentInput struct {
		Text      string `json:"text"`
		Digest    string `json:"digest"`
		Truncated bool   `json:"truncated"`
	} `json:"current_input"`
	FastContext struct {
		Present bool   `json:"present"`
		Digest  string `json:"digest,omitempty"`
	} `json:"fast_context"`
}

type contextSignal struct {
	ID               string    `json:"id"`
	Type             string    `json:"type"`
	DetectorVersion  int       `json:"detector_version"`
	Confidence       string    `json:"confidence"`
	DetectedSequence uint64    `json:"detected_sequence"`
	CreatedAt        time.Time `json:"created_at"`
	ExpiresAt        time.Time `json:"expires_at"`
}

type contextChild struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Generation uint64 `json:"generation,omitempty"`
}

type sessionContext struct {
	Schema            string          `json:"schema"`
	AsOfSequence      uint64          `json:"as_of_sequence"`
	Digest            string          `json:"digest"`
	Signals           []contextSignal `json:"signals"`
	SignalsTruncated  bool            `json:"signals_truncated"`
	Children          []contextChild  `json:"children"`
	ChildrenTruncated bool            `json:"children_truncated"`
	UnreadMessages    int             `json:"unread_messages"`
}

func buildGuidance(facts qualityFacts, snapshot sessionContext, available map[string]bool) string {
	items := make([]string, 0, maxGuidanceItems)
	if learning := learningGuidance(snapshot); learning != "" {
		items = append(items, learning)
	}
	if coordination := coordinationGuidance(snapshot, available); coordination != "" {
		items = append(items, coordination)
	}
	if research := researchGuidance(facts, available); research != "" {
		items = append(items, research)
	}
	if len(items) == 0 {
		return ""
	}
	if len(items) > maxGuidanceItems {
		items = items[:maxGuidanceItems]
	}
	header := "Stado workflow guidance (signed application advice; below operator and repository instructions; grants no tools or authority):"
	var out strings.Builder
	out.WriteString(header)
	for _, item := range items {
		candidate := out.String() + "\n- " + item
		if len(candidate) > maxGuidanceBytes {
			break
		}
		out.WriteString("\n- ")
		out.WriteString(item)
	}
	if out.String() == header {
		return ""
	}
	return out.String()
}

func learningGuidance(snapshot sessionContext) string {
	types := make(map[string]bool)
	for _, signal := range snapshot.Signals {
		types[signal.Type] = true
	}
	if len(types) == 0 {
		return ""
	}
	labels := make([]string, 0, len(types))
	for kind := range types {
		labels = append(labels, signalLabel(kind))
	}
	sort.Strings(labels)
	if len(labels) > 3 {
		labels = labels[:3]
	}
	return fmt.Sprintf("%d mechanical learning signal shape(s) are active (%s). Preserve reusable corrections as candidate lessons and ask the operator to run `/learn [focus]` at the natural response boundary. Candidates remain pending review; never run or claim approval.", len(types), strings.Join(labels, ", "))
}

func coordinationGuidance(snapshot sessionContext, available map[string]bool) string {
	active := 0
	for _, child := range snapshot.Children {
		switch child.Status {
		case "active", "admitted", "starting", "running", "idle":
			active++
		}
	}
	if active == 0 && snapshot.UnreadMessages == 0 {
		return ""
	}
	actions := make([]string, 0, 2)
	for _, name := range []string{"agent__list", "agent__read_messages"} {
		if available[name] {
			actions = append(actions, "`"+name+"`")
		}
	}
	if len(actions) == 0 {
		return ""
	}
	followUp := ""
	if available["agent__send_message"] {
		followUp = "; use `agent__send_message` for bounded follow-up"
	}
	return fmt.Sprintf("Retained coordination needs attention: %d active child handle(s), %d unread data message(s). Check %s before duplicating work%s.", active, snapshot.UnreadMessages, strings.Join(actions, " and "), followUp)
}

func researchGuidance(facts qualityFacts, available map[string]bool) string {
	q := strings.ToLower(facts.CurrentInput.Text)
	historical := containsAny(q, "previous session", "older session", "past session", "earlier session", "historical session", "what did we decide", "prior decision")
	recurring := containsAny(q, "remember", "recurring", "keeps failing", "keep failing", "convention", "previously", "prior approach") || containsToken(q, "again")
	if historical && available["session__research"] {
		return "This request depends on older session evidence. Prefer `session__research` so raw history stays out of the main context; use its precise cited synthesis and treat citation integrity as provenance, not entailment."
	}
	if recurring && !facts.FastContext.Present && available["memory__research"] {
		return "Fast retrieval supplied no matching context for a recurring-memory-shaped request. Prefer `memory__research` for an isolated search before repeating exploration or assumptions."
	}
	return ""
}

func signalLabel(kind string) string {
	switch kind {
	case "repeated_tool_failure":
		return "repeated tool failure"
	case "argument_changed_then_success":
		return "corrected tool arguments"
	case "verification_fail_then_pass":
		return "verification recovery"
	case "recurring_permission_or_scope_denial":
		return "recurring policy denial"
	case "explicit_operator_correction":
		return "operator correction"
	default:
		return "typed correction"
	}
}

func containsAny(value string, shapes ...string) bool {
	for _, shape := range shapes {
		if strings.Contains(value, shape) {
			return true
		}
	}
	return false
}

func containsToken(value, token string) bool {
	for _, field := range strings.FieldsFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_')
	}) {
		if field == token {
			return true
		}
	}
	return false
}
