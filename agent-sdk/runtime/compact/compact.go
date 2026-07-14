package compact

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
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
		open := make(map[string]any, len(typed))
		for key, value := range typed {
			open[key] = value
		}
		for _, key := range compactIntegerKeys() {
			delete(open, key)
		}
		buf, err := json.Marshal(open)
		if err != nil || json.Unmarshal(buf, &out) != nil {
			return CompactEventData{}, false
		}
		if !decodeCompactIntegers(typed, &out) {
			return CompactEventData{}, false
		}
		return normalizeCompactEventData(out), true
	default:
		return CompactEventData{}, false
	}
}

func CompactEventDataValue(in CompactEventData) map[string]any {
	in = normalizeCompactEventData(in)
	out := make(map[string]any)
	if in.Revision != 0 {
		out["revision"] = strconv.Itoa(in.Revision)
	}
	if in.ContractVersion != 0 {
		out["contract_version"] = strconv.Itoa(in.ContractVersion)
	}
	if in.SummarizedThroughID != "" {
		out["summarized_through_id"] = in.SummarizedThroughID
	}
	if in.SummarizedThroughSeq != 0 {
		out["summarized_through_seq"] = strconv.FormatUint(in.SummarizedThroughSeq, 10)
	}
	if in.Generator != "" {
		out["generator"] = in.Generator
	}
	if in.Trigger != "" {
		out["trigger"] = in.Trigger
	}
	if in.SourceEventCount != 0 {
		out["source_event_count"] = strconv.Itoa(in.SourceEventCount)
	}
	if in.TotalTokens != 0 {
		out["total_tokens"] = strconv.Itoa(in.TotalTokens)
	}
	if in.ContextWindowTokens != 0 {
		out["context_window_tokens"] = strconv.Itoa(in.ContextWindowTokens)
	}
	if len(in.DiscoveredTools) > 0 {
		out["discovered_tools"] = append([]string(nil), in.DiscoveredTools...)
	}
	return out
}

func compactIntegerKeys() []string {
	return []string{
		"revision", "contract_version", "summarized_through_seq",
		"source_event_count", "total_tokens", "context_window_tokens",
	}
}

func decodeCompactIntegers(values map[string]any, out *CompactEventData) bool {
	if out == nil {
		return false
	}
	for _, field := range []struct {
		key    string
		target *int
	}{
		{key: "revision", target: &out.Revision},
		{key: "contract_version", target: &out.ContractVersion},
		{key: "source_event_count", target: &out.SourceEventCount},
		{key: "total_tokens", target: &out.TotalTokens},
		{key: "context_window_tokens", target: &out.ContextWindowTokens},
	} {
		value, ok := values[field.key]
		if !ok {
			continue
		}
		parsed, err := compactUint64(value)
		if err != nil || parsed > uint64(maxInt()) {
			return false
		}
		*field.target = int(parsed)
	}
	if value, ok := values["summarized_through_seq"]; ok {
		parsed, err := compactUint64(value)
		if err != nil {
			return false
		}
		out.SummarizedThroughSeq = parsed
	}
	return true
}

func compactUint64(value any) (uint64, error) {
	switch typed := value.(type) {
	case string:
		parsed, err := strconv.ParseUint(typed, 10, 64)
		if err != nil || strconv.FormatUint(parsed, 10) != typed {
			return 0, fmt.Errorf("invalid uint64 decimal")
		}
		return parsed, nil
	case json.Number:
		return compactUint64(typed.String())
	case uint64:
		return typed, nil
	case uint:
		return uint64(typed), nil
	case uint32:
		return uint64(typed), nil
	case int:
		if typed >= 0 {
			return uint64(typed), nil
		}
	case int64:
		if typed >= 0 {
			return uint64(typed), nil
		}
	case int32:
		if typed >= 0 {
			return uint64(typed), nil
		}
	case float64:
		if typed >= 0 && typed <= 1<<53-1 && math.Trunc(typed) == typed {
			return uint64(typed), nil
		}
	}
	return 0, fmt.Errorf("invalid non-negative integer %T", value)
}

func maxInt() int {
	return int(^uint(0) >> 1)
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
	if data, ok := CompactEventDataFromEvent(visible[index]); ok {
		out := make([]*session.Event, 0, len(visible[index:]))
		if replacement := replacementTextEvent(session.EventText(visible[index])); replacement != nil {
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
