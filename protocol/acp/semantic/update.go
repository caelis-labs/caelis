package semantic

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

// DecodeUpdate converts one supported ACP wire update into the normalized
// semantic DTO owned by agent-sdk/session.
func DecodeUpdate(wire schema.Update) (*session.ProtocolUpdate, error) {
	if wire == nil {
		return nil, nil
	}
	var update session.ProtocolUpdate
	switch typed := wire.(type) {
	case schema.ContentChunk:
		update = decodeContentChunk(typed)
	case *schema.ContentChunk:
		if typed == nil {
			return nil, nil
		}
		update = decodeContentChunk(*typed)
	case schema.ToolCall:
		update = decodeToolCall(typed)
	case *schema.ToolCall:
		if typed == nil {
			return nil, nil
		}
		update = decodeToolCall(*typed)
	case schema.ToolCallUpdate:
		update = decodeToolCallUpdate(typed)
	case *schema.ToolCallUpdate:
		if typed == nil {
			return nil, nil
		}
		update = decodeToolCallUpdate(*typed)
	case schema.PlanUpdate:
		update = decodePlanUpdate(typed)
	case *schema.PlanUpdate:
		if typed == nil {
			return nil, nil
		}
		update = decodePlanUpdate(*typed)
	default:
		return nil, fmt.Errorf("protocol/acp/semantic: unsupported update %T", wire)
	}
	return cloneUpdate(update), nil
}

// DecodeRawContentUpdate converts the delayed raw content representation used
// by the ACP client into the same normalized SDK semantic DTO as DecodeUpdate.
func DecodeRawContentUpdate(updateType string, content json.RawMessage, messageID string, meta map[string]any) (*session.ProtocolUpdate, error) {
	var decoded any
	if len(content) > 0 {
		if err := json.Unmarshal(content, &decoded); err != nil {
			return nil, fmt.Errorf("protocol/acp/semantic: decode content: %w", err)
		}
	}
	return cloneUpdate(session.ProtocolUpdate{
		SessionUpdate: strings.TrimSpace(updateType),
		Content:       decoded,
		MessageID:     strings.TrimSpace(messageID),
		Meta:          meta,
	}), nil
}

// EncodeUpdate converts one normalized SDK semantic DTO into its ACP wire
// representation. It does not synthesize display titles, kinds, statuses, or
// terminal identifiers.
func EncodeUpdate(update *session.ProtocolUpdate) (schema.Update, error) {
	if update == nil {
		return nil, nil
	}
	normalized := cloneUpdate(*update)
	switch normalized.SessionUpdate {
	case schema.UpdateUserMessage, schema.UpdateAgentMessage, schema.UpdateAgentThought, schema.UpdateCompact:
		return schema.ContentChunk{
			SessionUpdate: normalized.SessionUpdate,
			Content:       normalized.Content,
			MessageID:     normalized.MessageID,
			Meta:          normalized.Meta,
		}, nil
	case schema.UpdateToolCall:
		return schema.ToolCall{
			SessionUpdate: normalized.SessionUpdate,
			ToolCallID:    normalized.ToolCallID,
			Title:         normalized.Title,
			Kind:          normalized.Kind,
			Status:        normalized.Status,
			RawInput:      mapOrNil(normalized.RawInput),
			RawOutput:     mapOrNil(normalized.RawOutput),
			Content:       encodeToolContent(session.ProtocolToolCallContentOf(normalized)),
			Locations:     encodeLocations(normalized.Locations),
			Meta:          normalized.Meta,
		}, nil
	case schema.UpdateToolCallInfo:
		return schema.ToolCallUpdate{
			SessionUpdate: normalized.SessionUpdate,
			ToolCallID:    normalized.ToolCallID,
			Title:         optionalString(normalized.Title),
			Kind:          optionalString(normalized.Kind),
			Status:        optionalString(normalized.Status),
			RawInput:      mapOrNil(normalized.RawInput),
			RawOutput:     mapOrNil(normalized.RawOutput),
			Content:       encodeToolContent(session.ProtocolToolCallContentOf(normalized)),
			Locations:     encodeLocations(normalized.Locations),
			Meta:          normalized.Meta,
		}, nil
	case schema.UpdatePlan:
		entries := make([]schema.PlanEntry, 0, len(normalized.Entries))
		for _, entry := range normalized.Entries {
			entries = append(entries, schema.PlanEntry{
				Content:  entry.Content,
				Status:   entry.Status,
				Priority: entry.Priority,
			})
		}
		return schema.PlanUpdate{SessionUpdate: normalized.SessionUpdate, Entries: entries}, nil
	default:
		return nil, fmt.Errorf("protocol/acp/semantic: unsupported update type %q", normalized.SessionUpdate)
	}
}

func decodeContentChunk(wire schema.ContentChunk) session.ProtocolUpdate {
	return session.ProtocolUpdate{
		SessionUpdate: wire.SessionUpdate,
		Content:       normalizeWireValue(wire.Content),
		MessageID:     wire.MessageID,
		Meta:          wire.Meta,
	}
}

func decodeToolCall(wire schema.ToolCall) session.ProtocolUpdate {
	return session.ProtocolUpdate{
		SessionUpdate: wire.SessionUpdate,
		ToolCallID:    wire.ToolCallID,
		Title:         wire.Title,
		Kind:          wire.Kind,
		Status:        wire.Status,
		RawInput:      schema.NormalizeRawMap(wire.RawInput),
		RawOutput:     schema.NormalizeRawMap(wire.RawOutput),
		Content:       decodeToolContent(wire.Content),
		Locations:     decodeLocations(wire.Locations),
		Meta:          wire.Meta,
	}
}

func decodeToolCallUpdate(wire schema.ToolCallUpdate) session.ProtocolUpdate {
	return session.ProtocolUpdate{
		SessionUpdate: wire.SessionUpdate,
		ToolCallID:    wire.ToolCallID,
		Title:         dereference(wire.Title),
		Kind:          dereference(wire.Kind),
		Status:        dereference(wire.Status),
		RawInput:      schema.NormalizeRawMap(wire.RawInput),
		RawOutput:     schema.NormalizeRawMap(wire.RawOutput),
		Content:       decodeToolContent(wire.Content),
		Locations:     decodeLocations(wire.Locations),
		Meta:          wire.Meta,
	}
}

func decodePlanUpdate(wire schema.PlanUpdate) session.ProtocolUpdate {
	entries := make([]session.ProtocolPlanEntry, 0, len(wire.Entries))
	for _, entry := range wire.Entries {
		entries = append(entries, session.ProtocolPlanEntry{
			Content:  entry.Content,
			Status:   entry.Status,
			Priority: entry.Priority,
		})
	}
	return session.ProtocolUpdate{SessionUpdate: wire.SessionUpdate, Entries: entries}
}

func decodeToolContent(in []schema.ToolCallContent) []session.ProtocolToolCallContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]session.ProtocolToolCallContent, 0, len(in))
	for _, item := range in {
		out = append(out, session.ProtocolToolCallContent{
			Type:       item.Type,
			Content:    normalizeWireValue(item.Content),
			TerminalID: item.TerminalID,
			Path:       item.Path,
			OldText:    cloneString(item.OldText),
			NewText:    item.NewText,
		})
	}
	return out
}

func normalizeWireValue(in any) any {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return in
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return in
	}
	return out
}

func encodeToolContent(in []session.ProtocolToolCallContent) []schema.ToolCallContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]schema.ToolCallContent, 0, len(in))
	for _, item := range in {
		out = append(out, schema.ToolCallContent{
			Type:       item.Type,
			Content:    item.Content,
			TerminalID: item.TerminalID,
			Path:       item.Path,
			OldText:    cloneString(item.OldText),
			NewText:    item.NewText,
		})
	}
	return out
}

func decodeLocations(in []schema.ToolCallLocation) []session.ProtocolToolCallLocation {
	if len(in) == 0 {
		return nil
	}
	out := make([]session.ProtocolToolCallLocation, 0, len(in))
	for _, item := range in {
		out = append(out, session.ProtocolToolCallLocation{Path: item.Path, Line: cloneInt(item.Line)})
	}
	return out
}

func encodeLocations(in []session.ProtocolToolCallLocation) []schema.ToolCallLocation {
	if len(in) == 0 {
		return nil
	}
	out := make([]schema.ToolCallLocation, 0, len(in))
	for _, item := range in {
		out = append(out, schema.ToolCallLocation{Path: item.Path, Line: cloneInt(item.Line)})
	}
	return out
}

func cloneUpdate(update session.ProtocolUpdate) *session.ProtocolUpdate {
	protocol := session.CloneEventProtocol(session.EventProtocol{Update: &update})
	return protocol.Update
}

func mapOrNil(in map[string]any) any {
	if len(in) == 0 {
		return nil
	}
	return in
}

func optionalString(in string) *string {
	if in == "" {
		return nil
	}
	value := in
	return &value
}

func dereference(in *string) string {
	if in == nil {
		return ""
	}
	return *in
}

func cloneString(in *string) *string {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

func cloneInt(in *int) *int {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}
