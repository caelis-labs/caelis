package session

import (
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
)

type EventPlanPayload struct {
	Entries []EventPlanEntry `json:"entries,omitempty"`
}

type EventPlanEntry struct {
	Content  string `json:"content,omitempty"`
	Status   string `json:"status,omitempty"`
	Priority string `json:"priority,omitempty"`
}

// ModelMessageOf returns the canonical model-visible message carried by one
// event. It intentionally does not infer model replay input from Protocol,
// _meta, Text, or other projection-only state.
func ModelMessageOf(event *Event) (model.Message, bool) {
	if event == nil || event.Message == nil {
		return model.Message{}, false
	}
	return model.CloneMessage(*event.Message), true
}

func PlanPayloadOf(event *Event) *EventPlanPayload {
	if event == nil || event.PlanPayload == nil {
		return nil
	}
	out := cloneEventPlanPayload(*event.PlanPayload)
	return &out
}

func EventToolProjection(event *Event) *EventTool {
	if event == nil || event.Tool == nil {
		return nil
	}
	out := *event.Tool
	out.ID = strings.TrimSpace(out.ID)
	out.Name = strings.TrimSpace(out.Name)
	out.Kind = strings.TrimSpace(out.Kind)
	out.Title = strings.TrimSpace(out.Title)
	out.Status = strings.TrimSpace(out.Status)
	out.Input = maps.Clone(event.Tool.Input)
	out.Output = maps.Clone(event.Tool.Output)
	out.Content = cloneEventToolContent(event.Tool.Content)
	out.Locations = cloneEventToolLocations(event.Tool.Locations)
	return &out
}

func cloneEventPlanPayload(in EventPlanPayload) EventPlanPayload {
	out := in
	if len(in.Entries) > 0 {
		out.Entries = make([]EventPlanEntry, 0, len(in.Entries))
		for _, item := range in.Entries {
			out.Entries = append(out.Entries, EventPlanEntry{
				Content:  strings.TrimSpace(item.Content),
				Status:   strings.TrimSpace(item.Status),
				Priority: strings.TrimSpace(item.Priority),
			})
		}
	}
	return out
}

func cloneEventToolContent(in []EventToolContent) []EventToolContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]EventToolContent, 0, len(in))
	for _, item := range in {
		cp := item
		if item.OldText != nil {
			value := *item.OldText
			cp.OldText = &value
		}
		out = append(out, cp)
	}
	return out
}

func cloneEventToolLocations(in []EventToolLocation) []EventToolLocation {
	if len(in) == 0 {
		return nil
	}
	out := make([]EventToolLocation, 0, len(in))
	for _, item := range in {
		cp := item
		if item.Line != nil {
			value := *item.Line
			cp.Line = &value
		}
		out = append(out, cp)
	}
	return out
}
