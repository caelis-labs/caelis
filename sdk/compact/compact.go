package compact

import (
	"context"
	"encoding/json"
	"strings"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

const (
	MetaKeyCompact = "compact"
)

type UsageSource string

const (
	UsageSourceProvider  UsageSource = "provider"
	UsageSourceEstimated UsageSource = "estimated"
)

// UsageSnapshot captures the best-known prompt-budget view before one turn.
type UsageSnapshot struct {
	TotalTokens          int         `json:"total_tokens,omitempty"`
	ContextWindowTokens  int         `json:"context_window_tokens,omitempty"`
	EffectiveInputBudget int         `json:"effective_input_budget,omitempty"`
	EstimatedDeltaTokens int         `json:"estimated_delta_tokens,omitempty"`
	Source               UsageSource `json:"source,omitempty"`
	AsOfEventID          string      `json:"as_of_event_id,omitempty"`
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

type BudgetProvider interface {
	Snapshot(context.Context, Request, []*sdksession.Event) (UsageSnapshot, error)
}

type TriggerPolicy interface {
	Decide(context.Context, UsageSnapshot, Request) (TriggerDecision, error)
}

type CheckpointGenerator interface {
	Generate(context.Context, GenerateRequest) (string, error)
}

type GenerateRequest struct {
	BaseCompactText string
	Events          []*sdksession.Event
}

type SegmentCompactor interface {
	CompactSegmented(context.Context, GenerateRequest) (string, error)
}

type Engine interface {
	Prepare(context.Context, Request) (Result, error)
	CompactOnOverflow(context.Context, Request, error) (Result, error)
}

type ForceEngine interface {
	Force(context.Context, Request, string) (Result, error)
}

type CompactEventData struct {
	Revision            int                 `json:"revision,omitempty"`
	SummarizedThroughID string              `json:"summarized_through_id,omitempty"`
	Generator           string              `json:"generator,omitempty"`
	Trigger             string              `json:"trigger,omitempty"`
	RetainedUserInputs  []string            `json:"retained_user_inputs,omitempty"`
	ReplacementHistory  []*sdksession.Event `json:"replacement_history,omitempty"`
	TotalTokens         int                 `json:"total_tokens,omitempty"`
	ContextWindowTokens int                 `json:"context_window_tokens,omitempty"`
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
	out := make([]string, 0, len(in.RetainedUserInputs))
	seen := map[string]struct{}{}
	for _, item := range in.RetainedUserInputs {
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
	in.RetainedUserInputs = out
	in.ReplacementHistory = normalizeReplacementHistory(in.ReplacementHistory)
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
	if data, ok := CompactEventDataFromEvent(visible[index]); ok {
		if len(data.ReplacementHistory) > 0 {
			out := normalizeReplacementHistory(data.ReplacementHistory)
			for _, event := range visible[index+1:] {
				out = append(out, sdksession.CloneEvent(event))
			}
			return out
		}
		out := make([]*sdksession.Event, 0, len(visible[index:])+4)
		for _, text := range data.RetainedUserInputs {
			msg := sdkmodel.NewTextMessage(sdkmodel.RoleUser, text)
			out = append(out, &sdksession.Event{
				Type:       sdksession.EventTypeUser,
				Visibility: sdksession.VisibilityOverlay,
				Actor:      sdksession.ActorRef{Kind: sdksession.ActorKindUser, Name: "user"},
				Message:    &msg,
				Text:       msg.TextContent(),
				Meta: map[string]any{
					MetaKeyCompact: map[string]any{"retained": true},
				},
			})
		}
		out = append(out, sdksession.CloneEvent(visible[index]))
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

func normalizeReplacementHistory(events []*sdksession.Event) []*sdksession.Event {
	if len(events) == 0 {
		return nil
	}
	out := make([]*sdksession.Event, 0, len(events))
	for _, event := range events {
		if event == nil || !sdksession.IsInvocationVisibleEvent(event) {
			continue
		}
		clone := sdksession.CloneEvent(event)
		clone.ID = ""
		clone.SessionID = ""
		clone.Scope = nil
		clone.Meta = nil
		clone.Notice = nil
		clone.Lifecycle = nil
		if clone.Type == "" {
			clone.Type = sdksession.EventTypeOf(clone)
		}
		if clone.Visibility == "" {
			clone.Visibility = sdksession.VisibilityOverlay
		}
		if clone.Text == "" && clone.Message != nil {
			clone.Text = clone.Message.TextContent()
		}
		out = append(out, clone)
	}
	return out
}
