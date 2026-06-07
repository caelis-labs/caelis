package session

import (
	"encoding/json"

	"github.com/OnslaughtSnail/caelis/model"
)

// This file implements the projection layer: converting durable session
// events into model messages and ACP-compatible wire format.
//
// Projections are computed, never stored. The stored event is the source
// of truth; all projections derive from it.

// ─── Model context reconstruction ────────────────────────────────────

// ModelContextFromEvents rebuilds the model message sequence from durable
// session events. Only canonical and overlay events are included.
//
// If a compaction event exists, only events after the last compaction
// are included, with the compaction summary prepended as a system message.
func ModelContextFromEvents(events []Event) []model.Message {
	// Find the last compaction event.
	lastCompactIdx := -1
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == EventKindCompaction && events[i].Visibility.IsPersisted() {
			lastCompactIdx = i
			break
		}
	}

	var msgs []model.Message

	// If compaction exists, add its summary as the first system message.
	if lastCompactIdx >= 0 {
		comp := &events[lastCompactIdx]
		if comp.CompactionPayload != nil && comp.SummaryText != "" {
			msgs = append(msgs, model.Message{
				Role:    model.RoleSystem,
				Content: []model.Part{{Text: comp.SummaryText}},
			})
		}
		if comp.CompactionPayload != nil {
			msgs = append(msgs, retainedCompactionMessagesToModel(comp.RetainedMessages)...)
			msgs = append(msgs, preCompactionMessagesAfterBoundary(events[:lastCompactIdx], comp.Previous)...)
		}
	}

	// Scan events after compaction (or all events if no compaction).
	startIdx := 0
	if lastCompactIdx >= 0 {
		startIdx = lastCompactIdx + 1
	}

	for i := startIdx; i < len(events); i++ {
		e := &events[i]
		if !e.IsModelVisible() {
			continue
		}
		msg := projectEventToModelMessage(e)
		if msg != nil {
			msgs = append(msgs, *msg)
		}
	}
	return msgs
}

// projectEventToModelMessage converts a single event to a model message.
// Returns nil for events that don't produce model messages (plan, lifecycle,
// compaction, notice, handoff).
func projectEventToModelMessage(e *Event) *model.Message {
	switch e.Kind {
	case EventKindUser:
		return projectUserToModel(e)
	case EventKindAssistant:
		return projectAssistantToModel(e)
	case EventKindToolCall:
		return projectToolCallToModel(e)
	case EventKindToolResult:
		return projectToolResultToModel(e)
	case EventKindSystem:
		return projectSystemToModel(e)
	default:
		// plan, lifecycle, compaction, notice, handoff, participant
		// do not produce model messages.
		return nil
	}
}

func retainedCompactionMessagesToModel(messages []CompactionRetainedMessage) []model.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]model.Message, 0, len(messages))
	for _, msg := range messages {
		role := model.Role(msg.Role)
		if role == "" {
			continue
		}
		parts := eventPartsToModelParts(msg.Parts)
		if len(parts) == 0 {
			continue
		}
		out = append(out, model.Message{Role: role, Content: parts})
	}
	return out
}

func preCompactionMessagesAfterBoundary(events []Event, compactedMessages int) []model.Message {
	if compactedMessages <= 0 || len(events) == 0 {
		return nil
	}
	seen := 0
	var out []model.Message
	for i := range events {
		e := &events[i]
		if !e.IsModelVisible() {
			continue
		}
		msg := projectEventToModelMessage(e)
		if msg == nil {
			continue
		}
		if seen >= compactedMessages {
			out = append(out, *msg)
		}
		seen++
	}
	return out
}

func projectUserToModel(e *Event) *model.Message {
	if e.UserPayload == nil {
		return nil
	}
	return &model.Message{
		Role:    model.RoleUser,
		Content: eventPartsToModelParts(e.UserPayload.Parts),
	}
}

func projectAssistantToModel(e *Event) *model.Message {
	if e.AssistantPayload == nil {
		return nil
	}
	return &model.Message{
		Role:    model.RoleAssistant,
		Content: eventPartsToModelParts(e.AssistantPayload.Parts),
	}
}

// projectToolCallToModel converts a tool_call event to an assistant message
// containing the tool_use part. This matches provider APIs where tool_use
// appears in the assistant turn.
func projectToolCallToModel(e *Event) *model.Message {
	if e.ToolCallPayload == nil {
		return nil
	}
	tc := e.ToolCallPayload
	return &model.Message{
		Role: model.RoleAssistant,
		Content: []model.Part{
			{
				ToolUse: &model.ToolUse{
					CallID:  tc.CallID,
					Name:    tc.Name,
					Args:    tc.Args,
					ArgJSON: tc.ArgJSON,
				},
			},
		},
	}
}

func projectToolResultToModel(e *Event) *model.Message {
	if e.ToolResultPayload == nil {
		return nil
	}
	tr := e.ToolResultPayload
	// Synthesize text output from content parts for the tool_result part.
	var output string
	for _, p := range tr.Content {
		if p.Kind == PartKindText {
			output += p.Text
		}
	}
	parts := []model.Part{
		{
			ToolResult: &model.ToolResult{
				CallID:  tr.CallID,
				Content: output,
				IsError: tr.IsError,
			},
		},
	}
	// Add non-text content parts (media, json, file refs) as additional parts.
	for _, p := range tr.Content {
		if p.Kind != PartKindText {
			mp := eventPartToModelPart(p)
			if mp != nil {
				parts = append(parts, *mp)
			}
		}
	}
	return &model.Message{
		Role:    model.RoleTool,
		Content: parts,
	}
}

func projectSystemToModel(e *Event) *model.Message {
	if e.SystemPayload == nil {
		return nil
	}
	return &model.Message{
		Role:    model.RoleSystem,
		Content: eventPartsToModelParts(e.SystemPayload.Parts),
	}
}

// eventPartsToModelParts converts EventParts to model.Parts.
func eventPartsToModelParts(parts []EventPart) []model.Part {
	out := make([]model.Part, 0, len(parts))
	for _, p := range parts {
		mp := eventPartToModelPart(p)
		if mp != nil {
			out = append(out, *mp)
		}
	}
	return out
}

func eventPartToModelPart(p EventPart) *model.Part {
	switch p.Kind {
	case PartKindText:
		return &model.Part{Text: p.Text, ProviderMeta: cloneProviderMeta(p.ProviderMeta)}
	case PartKindReasoning:
		return &model.Part{
			Reasoning: &model.Reasoning{
				Text:       p.Text,
				Visibility: model.ReasoningVisibilityVisible,
				Replay:     replayMetaFromProviderMeta(p.ProviderMeta),
			},
			ProviderMeta: cloneProviderMeta(p.ProviderMeta),
		}
	case PartKindToolUse:
		if p.ToolUse != nil {
			return &model.Part{
				ToolUse: &model.ToolUse{
					CallID:       p.ToolUse.CallID,
					Name:         p.ToolUse.Name,
					Args:         p.ToolUse.Args,
					ProviderMeta: cloneProviderMeta(p.ProviderMeta),
				},
				ProviderMeta: cloneProviderMeta(p.ProviderMeta),
			}
		}
	case PartKindToolResult:
		if p.ToolResultRef != nil {
			return &model.Part{
				ToolResult: &model.ToolResult{
					CallID:  p.ToolResultRef.CallID,
					Content: p.ToolResultRef.Content,
					IsError: p.ToolResultRef.IsError,
				},
				ProviderMeta: cloneProviderMeta(p.ProviderMeta),
			}
		}
	case PartKindMedia:
		if p.Media != nil {
			return &model.Part{
				InlineData: &model.InlineData{
					MIMEType: p.Media.MIMEType,
					Data:     p.Media.Data,
				},
				ProviderMeta: cloneProviderMeta(p.ProviderMeta),
			}
		}
	case PartKindFileRef:
		if p.FileRef != nil {
			return &model.Part{
				FileRef: &model.FileRef{
					URI:      p.FileRef.URI,
					MIMEType: p.FileRef.MIMEType,
				},
				ProviderMeta: cloneProviderMeta(p.ProviderMeta),
			}
		}
	case PartKindJSON:
		// JSON parts are serialized as text for model consumption.
		if p.JSON != nil {
			data, _ := json.Marshal(p.JSON)
			return &model.Part{Text: string(data), ProviderMeta: cloneProviderMeta(p.ProviderMeta)}
		}
	}
	return nil
}

func replayMetaFromProviderMeta(meta map[string]any) *model.ReplayMeta {
	raw, ok := meta["replay"]
	if !ok {
		return nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	replay := &model.ReplayMeta{
		Provider: stringFromAny(values["provider"]),
		Kind:     stringFromAny(values["kind"]),
		Token:    stringFromAny(values["token"]),
	}
	if metadata, ok := values["metadata"].(map[string]any); ok {
		replay.Metadata = cloneProviderMeta(metadata)
	}
	if replay.Provider == "" && replay.Kind == "" && replay.Token == "" && replay.Metadata == nil {
		return nil
	}
	return replay
}

func cloneProviderMeta(meta map[string]any) map[string]any {
	if meta == nil {
		return nil
	}
	cp := make(map[string]any, len(meta))
	for k, v := range meta {
		cp[k] = cloneProviderMetaValue(v)
	}
	return cp
}

func cloneProviderMetaValue(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		return cloneProviderMeta(typed)
	case []any:
		cp := make([]any, len(typed))
		for i, item := range typed {
			cp[i] = cloneProviderMetaValue(item)
		}
		return cp
	default:
		return typed
	}
}

func stringFromAny(v any) string {
	s, _ := v.(string)
	return s
}
