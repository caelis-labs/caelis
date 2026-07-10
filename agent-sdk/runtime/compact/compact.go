package compact

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const (
	MetaKeyCompact         = "compact"
	CompactContractVersion = 1
	CompactNoticeLabel     = display.CompactNoticeLabel
	CompactFailureLabel    = display.CompactFailureNoticeLabel
)

type UsageSource string

const (
	UsageSourceProvider  UsageSource = "provider"
	UsageSourceEstimated UsageSource = "estimated"
)

// UsageSnapshot captures the best-known prompt-budget view before one turn.
type UsageSnapshot struct {
	TotalTokens           int         `json:"total_tokens,omitempty"`
	ContextWindowTokens   int         `json:"context_window_tokens,omitempty"`
	EffectiveInputBudget  int         `json:"effective_input_budget,omitempty"`
	EstimatedDeltaTokens  int         `json:"estimated_delta_tokens,omitempty"`
	EstimatedPrefixTokens int         `json:"estimated_prefix_tokens,omitempty"`
	Source                UsageSource `json:"source,omitempty"`
	AsOfEventID           string      `json:"as_of_event_id,omitempty"`
}

type Request struct {
	Session       session.Session
	SessionRef    session.SessionRef
	Events        []*session.Event
	PendingEvents []*session.Event
	Model         model.LLM
}

type Result struct {
	Compacted    bool
	CompactText  string
	CompactEvent *session.Event
	PromptEvents []*session.Event
	Usage        UsageSnapshot
}

type TriggerDecision struct {
	ShouldCompact bool
	Reason        string
}

type Engine interface {
	Prepare(context.Context, Request) (Result, error)
	CompactOnOverflow(context.Context, Request, error) (Result, error)
}

type ForceEngine interface {
	Force(context.Context, Request, string) (Result, error)
}

type CompactEventData struct {
	Revision             int      `json:"revision,omitempty"`
	ContractVersion      int      `json:"contract_version,omitempty"`
	SummarizedThroughID  string   `json:"summarized_through_id,omitempty"`
	SummarizedThroughSeq uint64   `json:"summarized_through_seq,omitempty"`
	Generator            string   `json:"generator,omitempty"`
	Trigger              string   `json:"trigger,omitempty"`
	SourceEventCount     int      `json:"source_event_count,omitempty"`
	TotalTokens          int      `json:"total_tokens,omitempty"`
	ContextWindowTokens  int      `json:"context_window_tokens,omitempty"`
	DiscoveredTools      []string `json:"discovered_tools,omitempty"`
}

type ContextWindowProvider interface {
	ContextWindowTokens() int
}

func IsCompactEvent(event *session.Event) bool {
	if event == nil {
		return false
	}
	if event.Type == session.EventTypeCompact {
		return true
	}
	if event.Meta == nil {
		return false
	}
	_, ok := event.Meta[MetaKeyCompact]
	return ok
}

func CompactEventDataFromEvent(event *session.Event) (CompactEventData, bool) {
	if event == nil || event.Meta == nil {
		return CompactEventData{}, false
	}
	raw, ok := event.Meta[MetaKeyCompact]
	if !ok {
		return CompactEventData{}, false
	}
	switch typed := raw.(type) {
	case CompactEventData:
		return normalizeCompactEventData(typed), true
	case map[string]any:
		var out CompactEventData
		buf, _ := json.Marshal(typed)
		_ = json.Unmarshal(buf, &out)
		return normalizeCompactEventData(out), true
	default:
		return CompactEventData{}, false
	}
}

func CompactEventDataValue(in CompactEventData) map[string]any {
	in = normalizeCompactEventData(in)
	buf, _ := json.Marshal(in)
	out := map[string]any{}
	_ = json.Unmarshal(buf, &out)
	if len(out) == 0 {
		return map[string]any{}
	}
	return out
}

func normalizeCompactEventData(in CompactEventData) CompactEventData {
	in.SummarizedThroughID = strings.TrimSpace(in.SummarizedThroughID)
	in.Generator = strings.TrimSpace(in.Generator)
	in.Trigger = strings.TrimSpace(in.Trigger)
	if in.ContractVersion < 0 {
		in.ContractVersion = 0
	}
	if in.SourceEventCount < 0 {
		in.SourceEventCount = 0
	}
	in.DiscoveredTools = normalizeDiscoveredTools(in.DiscoveredTools)
	return in
}

func normalizeDiscoveredTools(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		canonical := strings.ToUpper(name)
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		out = append(out, name)
	}
	return out
}

func PromptEventsFromLatestCompact(events []*session.Event) []*session.Event {
	visible := filterPromptVisibleEvents(events)
	if len(visible) == 0 {
		return nil
	}
	index := lastCompactIndex(visible)
	if index < 0 {
		return session.CloneEvents(visible)
	}
	if _, ok := CompactEventDataFromEvent(visible[index]); ok {
		out := make([]*session.Event, 0, len(visible[index:]))
		if replacement := replacementTextEvent(session.EventText(visible[index])); replacement != nil {
			out = append(out, replacement)
		}
		for _, event := range visible[index+1:] {
			if IsCompactEvent(event) {
				continue
			}
			out = append(out, session.CloneEvent(event))
		}
		return out
	}
	return session.CloneEvents(visible[index:])
}

// EventsAfterLatestCompact returns prompt-visible source events after the last
// compact checkpoint. It does not inject the compact checkpoint replacement
// used by PromptEventsFromLatestCompact for model prompts.
func EventsAfterLatestCompact(events []*session.Event) []*session.Event {
	visible := filterPromptVisibleEvents(events)
	if len(visible) == 0 {
		return nil
	}
	index := lastCompactIndex(visible)
	if index < 0 {
		return session.CloneEvents(visible)
	}
	out := make([]*session.Event, 0, len(visible)-index-1)
	for _, event := range visible[index+1:] {
		if IsCompactEvent(event) {
			continue
		}
		out = append(out, session.CloneEvent(event))
	}
	return out
}

func LatestCompactEvent(events []*session.Event) (*session.Event, CompactEventData, bool) {
	visible := filterPromptVisibleEvents(events)
	if len(visible) == 0 {
		return nil, CompactEventData{}, false
	}
	index := lastCompactIndex(visible)
	if index < 0 {
		return nil, CompactEventData{}, false
	}
	data, _ := CompactEventDataFromEvent(visible[index])
	return session.CloneEvent(visible[index]), data, true
}

func filterPromptVisibleEvents(events []*session.Event) []*session.Event {
	out := make([]*session.Event, 0, len(events))
	for _, event := range events {
		if !session.IsInvocationVisibleEvent(event) {
			continue
		}
		out = append(out, session.CloneEvent(event))
	}
	return out
}

func lastCompactIndex(events []*session.Event) int {
	bestIndex := -1
	var bestCoverage uint64
	legacyIndex := -1
	for i, event := range events {
		if !IsCompactEvent(event) {
			continue
		}
		data, ok := CompactEventDataFromEvent(event)
		if !ok || data.ContractVersion != CompactContractVersion || data.SummarizedThroughSeq == 0 {
			legacyIndex = i
			continue
		}
		if event.Seq > 0 && data.SummarizedThroughSeq >= event.Seq {
			continue
		}
		if bestIndex < 0 || data.SummarizedThroughSeq > bestCoverage ||
			(data.SummarizedThroughSeq == bestCoverage && event.Seq >= events[bestIndex].Seq) {
			bestIndex = i
			bestCoverage = data.SummarizedThroughSeq
		}
	}
	if bestIndex >= 0 {
		return bestIndex
	}
	return legacyIndex
}

func replacementTextEvent(text string) *session.Event {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return &session.Event{
		Type:       session.EventTypeUser,
		Visibility: session.VisibilityCanonical,
		Actor:      session.ActorRef{Kind: session.ActorKindUser, Name: "user"},
		Text:       text,
	}
}
