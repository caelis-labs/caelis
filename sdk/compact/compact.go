package compact

import (
	"context"
	"encoding/json"
	"strings"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

const (
	MetaKeyCompact         = "compact"
	CompactContractVersion = 1
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
	Session       sdksession.Session
	SessionRef    sdksession.SessionRef
	Events        []*sdksession.Event
	PendingEvents []*sdksession.Event
	Model         sdkmodel.LLM
}

type Result struct {
	Compacted    bool
	CompactText  string
	CompactEvent *sdksession.Event
	PromptEvents []*sdksession.Event
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
	Revision            int    `json:"revision,omitempty"`
	ContractVersion     int    `json:"contract_version,omitempty"`
	SummarizedThroughID string `json:"summarized_through_id,omitempty"`
	Generator           string `json:"generator,omitempty"`
	Trigger             string `json:"trigger,omitempty"`
	SourceEventCount    int    `json:"source_event_count,omitempty"`
	TotalTokens         int    `json:"total_tokens,omitempty"`
	ContextWindowTokens int    `json:"context_window_tokens,omitempty"`
}

type ContextWindowProvider interface {
	ContextWindowTokens() int
}

func IsCompactEvent(event *sdksession.Event) bool {
	if event == nil {
		return false
	}
	if event.Type == sdksession.EventTypeCompact {
		return true
	}
	if event.Meta == nil {
		return false
	}
	_, ok := event.Meta[MetaKeyCompact]
	return ok
}

func CompactEventDataFromEvent(event *sdksession.Event) (CompactEventData, bool) {
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
	return in
}

func PromptEventsFromLatestCompact(events []*sdksession.Event) []*sdksession.Event {
	visible := filterPromptVisibleEvents(events)
	if len(visible) == 0 {
		return nil
	}
	index := lastCompactIndex(visible)
	if index < 0 {
		return sdksession.CloneEvents(visible)
	}
	if _, ok := CompactEventDataFromEvent(visible[index]); ok {
		out := make([]*sdksession.Event, 0, len(visible[index:]))
		if legacy := legacyPromptEventsFromCompactEvent(visible[index]); len(legacy) > 0 {
			out = append(out, legacy...)
		} else {
			if replacement := replacementTextEvent(sdksession.EventText(visible[index])); replacement != nil {
				out = append(out, replacement)
			}
		}
		for _, event := range visible[index+1:] {
			out = append(out, sdksession.CloneEvent(event))
		}
		return out
	}
	return sdksession.CloneEvents(visible[index:])
}

func LatestCompactEvent(events []*sdksession.Event) (*sdksession.Event, CompactEventData, bool) {
	visible := filterPromptVisibleEvents(events)
	if len(visible) == 0 {
		return nil, CompactEventData{}, false
	}
	index := lastCompactIndex(visible)
	if index < 0 {
		return nil, CompactEventData{}, false
	}
	data, _ := CompactEventDataFromEvent(visible[index])
	return sdksession.CloneEvent(visible[index]), data, true
}

func filterPromptVisibleEvents(events []*sdksession.Event) []*sdksession.Event {
	out := make([]*sdksession.Event, 0, len(events))
	for _, event := range events {
		if !sdksession.IsInvocationVisibleEvent(event) {
			continue
		}
		out = append(out, sdksession.CloneEvent(event))
	}
	return out
}

func lastCompactIndex(events []*sdksession.Event) int {
	for i := len(events) - 1; i >= 0; i-- {
		if IsCompactEvent(events[i]) {
			return i
		}
	}
	return -1
}

func legacyPromptEventsFromCompactEvent(event *sdksession.Event) []*sdksession.Event {
	if event == nil || event.Meta == nil {
		return nil
	}
	raw, ok := event.Meta[MetaKeyCompact]
	if !ok {
		return nil
	}
	meta := compactMetaMap(raw)
	if len(meta) == 0 {
		return nil
	}
	if out := legacyReplacementHistoryEvents(meta["replacement_history"]); len(out) > 0 {
		return out
	}
	retained := legacyRetainedUserInputs(meta["retained_user_inputs"])
	if len(retained) == 0 {
		return nil
	}
	out := make([]*sdksession.Event, 0, len(retained)+1)
	for _, text := range retained {
		if replacement := replacementTextEvent(text); replacement != nil {
			out = append(out, replacement)
		}
	}
	if replacement := replacementTextEvent(sdksession.EventText(event)); replacement != nil {
		out = append(out, replacement)
	}
	return out
}

func compactMetaMap(raw any) map[string]any {
	switch typed := raw.(type) {
	case map[string]any:
		return typed
	default:
		buf, err := json.Marshal(raw)
		if err != nil {
			return nil
		}
		out := map[string]any{}
		if err := json.Unmarshal(buf, &out); err != nil {
			return nil
		}
		return out
	}
}

func legacyReplacementHistoryEvents(raw any) []*sdksession.Event {
	if raw == nil {
		return nil
	}
	buf, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var decoded []*sdksession.Event
	if err := json.Unmarshal(buf, &decoded); err != nil {
		return nil
	}
	out := make([]*sdksession.Event, 0, len(decoded))
	for _, event := range decoded {
		if event == nil || !sdksession.IsInvocationVisibleEvent(event) {
			continue
		}
		if replacement := replacementTextEvent(sdksession.EventText(event)); replacement != nil {
			out = append(out, replacement)
		}
	}
	return out
}

func legacyRetainedUserInputs(raw any) []string {
	if raw == nil {
		return nil
	}
	buf, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var decoded []string
	if err := json.Unmarshal(buf, &decoded); err != nil {
		return nil
	}
	out := make([]string, 0, len(decoded))
	seen := map[string]struct{}{}
	for _, item := range decoded {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func replacementTextEvent(text string) *sdksession.Event {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return &sdksession.Event{
		Type:       sdksession.EventTypeUser,
		Visibility: sdksession.VisibilityOverlay,
		Actor:      sdksession.ActorRef{Kind: sdksession.ActorKindUser, Name: "user"},
		Text:       text,
	}
}
