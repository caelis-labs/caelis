package compact

import (
	"context"
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
	// ServiceTier applies the selected provider service level when compaction
	// must issue a fresh model request.
	ServiceTier model.ServiceTier
	// InContextRequest is an exact, already assembled model request whose prefix
	// may still be hot at the provider. The compactor clones it before use.
	InContextRequest *model.Request
	// Coverage binds the frozen request to the source prefix and summarized delta.
	Coverage *Coverage
	// RequireInContext rejects missing source bindings and forbids independent
	// generation if the frozen in-context request fails.
	RequireInContext bool
	// RuntimeAppendix is deterministic continuity owned by Runtime rather than
	// inferred by the summarizing model.
	RuntimeAppendix string
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

// CompactEventData is the canonical Session checkpoint contract.
type CompactEventData = session.CheckpointData

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

// CompactEventDataFromEvent decodes canonical checkpoint coverage.
func CompactEventDataFromEvent(event *session.Event) (CompactEventData, bool) {
	return session.CheckpointDataFromEvent(event)
}

// CompactEventDataValue encodes checkpoint counters without floating-point loss.
func CompactEventDataValue(in CompactEventData) map[string]any {
	return session.CheckpointDataValue(in)
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
	if data, ok := CompactEventDataFromEvent(visible[index]); ok {
		out := make([]*session.Event, 0, len(visible[index:]))
		if replacement := replacementTextEvent(visible[index]); replacement != nil {
			out = append(out, replacement)
		}
		out = append(out, eventsAfterCheckpointCoverage(visible, index, data)...)
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
	if data, ok := CompactEventDataFromEvent(visible[index]); ok {
		return eventsAfterCheckpointCoverage(visible, index, data)
	}
	return nonCompactEvents(visible[index+1:])
}

func eventsAfterCheckpointCoverage(events []*session.Event, checkpointIndex int, data CompactEventData) []*session.Event {
	if data.ContractVersion != CompactContractVersion || data.SummarizedThroughSeq == 0 {
		return nonCompactEvents(events[checkpointIndex+1:])
	}
	out := make([]*session.Event, 0, len(events)-checkpointIndex-1)
	for index, event := range events {
		if event == nil || IsCompactEvent(event) {
			continue
		}
		if event.Seq > data.SummarizedThroughSeq || (event.Seq == 0 && index > checkpointIndex) {
			out = append(out, session.CloneEvent(event))
		}
	}
	return out
}

func nonCompactEvents(events []*session.Event) []*session.Event {
	out := make([]*session.Event, 0, len(events))
	for _, event := range events {
		if event == nil || IsCompactEvent(event) {
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

func replacementTextEvent(source *session.Event) *session.Event {
	if source == nil {
		return nil
	}
	text := strings.TrimSpace(session.EventText(source))
	if text == "" {
		return nil
	}
	cloned := session.CloneEvent(source)
	return &session.Event{
		Type:       session.EventTypeCompact,
		Visibility: session.VisibilityCanonical,
		Actor:      session.ActorRef{Kind: session.ActorKindSystem, Name: "runtime"},
		Text:       text,
		Meta:       cloned.Meta,
	}
}
