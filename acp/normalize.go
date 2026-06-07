package acp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/OnslaughtSnail/caelis/session"
)

// NormalizeExternalEvent converts an incoming ACP session/update into
// a canonical session.Event. This is used when external ACP agents
// send events that need to be stored in the canonical event log.
//
// Returns nil if the ACP update type doesn't map to a session event.
func NormalizeExternalEvent(sessionID string, update Update) *session.Event {
	if update == nil {
		return nil
	}
	switch u := update.(type) {
	case ContentChunk:
		return normalizeContentChunk(sessionID, u)
	case ToolCallUpdate:
		return normalizeToolCallUpdate(sessionID, u)
	case PlanUpdate:
		return normalizePlanUpdate(sessionID, u)
	default:
		return nil
	}
}

// NormalizeExternalUpdateJSON decodes one ACP session/update payload and
// converts it into a canonical session event.
func NormalizeExternalUpdateJSON(sessionID string, raw json.RawMessage) (*session.Event, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var probe struct {
		SessionUpdate UpdateKind `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	switch probe.SessionUpdate {
	case UpdateUserMessage, UpdateAgentMessage, UpdateAgentThought:
		var chunk ContentChunk
		if err := json.Unmarshal(raw, &chunk); err != nil {
			return nil, err
		}
		return NormalizeExternalEvent(sessionID, chunk), nil
	case UpdateToolCall, UpdateToolCallInfo:
		var update ToolCallUpdate
		if err := json.Unmarshal(raw, &update); err != nil {
			return nil, err
		}
		return NormalizeExternalEvent(sessionID, update), nil
	case UpdatePlan:
		var update PlanUpdate
		if err := json.Unmarshal(raw, &update); err != nil {
			return nil, err
		}
		return NormalizeExternalEvent(sessionID, update), nil
	case "":
		return nil, fmt.Errorf("acp: sessionUpdate is required")
	default:
		return nil, nil
	}
}

func normalizeContentChunk(sessionID string, chunk ContentChunk) *session.Event {
	parts := contentToParts(chunk.Content, session.PartKindText)
	if len(parts) == 0 {
		return nil
	}
	visibility := contentChunkVisibility(chunk)

	switch chunk.SessionUpdate {
	case UpdateUserMessage:
		return &session.Event{
			SessionRef: session.Ref{SessionID: sessionID},
			Kind:       session.EventKindUser,
			Visibility: visibility,
			UserPayload: &session.UserPayload{
				Parts: parts,
			},
		}
	case UpdateAgentMessage:
		return &session.Event{
			SessionRef: session.Ref{SessionID: sessionID},
			Kind:       session.EventKindAssistant,
			Visibility: visibility,
			AssistantPayload: &session.AssistantPayload{
				Parts: parts,
			},
		}
	case UpdateAgentThought:
		reasoning := contentToParts(chunk.Content, session.PartKindReasoning)
		return &session.Event{
			SessionRef: session.Ref{SessionID: sessionID},
			Kind:       session.EventKindAssistant,
			Visibility: visibility,
			AssistantPayload: &session.AssistantPayload{
				Parts: reasoning,
			},
		}
	default:
		return nil
	}
}

func contentChunkVisibility(chunk ContentChunk) session.Visibility {
	if chunk.Final != nil && !*chunk.Final {
		return session.VisibilityUIOnly
	}
	return session.VisibilityCanonical
}

func normalizeToolCallUpdate(sessionID string, tc ToolCallUpdate) *session.Event {
	switch tc.SessionUpdate {
	case UpdateToolCall:
		return &session.Event{
			SessionRef:   session.Ref{SessionID: sessionID},
			Kind:         session.EventKindToolCall,
			Visibility:   session.VisibilityCanonical,
			ProviderMeta: acpProviderMeta(tc.Meta, tc.Locations),
			ToolCallPayload: &session.ToolCallPayload{
				CallID:  tc.ToolCallID,
				Name:    tc.Title,
				Kind:    tc.Kind,
				Title:   tc.Title,
				Status:  NormalizeToolStatus(tc.Status),
				Args:    extractArgs(tc.RawInput),
				ArgJSON: rawJSON(tc.RawInput),
			},
		}
	case UpdateToolCallInfo:
		return &session.Event{
			SessionRef:   session.Ref{SessionID: sessionID},
			Kind:         session.EventKindToolResult,
			Visibility:   session.VisibilityCanonical,
			ProviderMeta: acpProviderMeta(tc.Meta, tc.Locations),
			ToolResultPayload: &session.ToolResultPayload{
				CallID:  tc.ToolCallID,
				Name:    tc.Title,
				Kind:    tc.Kind,
				Status:  NormalizeToolStatus(tc.Status),
				IsError: NormalizeToolStatus(tc.Status) == "failed",
				Content: toolResultParts(tc),
				Display: toolCallContentToDisplayParts(tc.Content),
			},
		}
	default:
		return nil
	}
}

func normalizePlanUpdate(sessionID string, pu PlanUpdate) *session.Event {
	entries := make([]session.PlanEntry, 0, len(pu.Entries))
	for _, e := range pu.Entries {
		entries = append(entries, session.PlanEntry{
			Content: e.Content,
			Status:  e.Status,
		})
	}
	return &session.Event{
		SessionRef: session.Ref{SessionID: sessionID},
		Kind:       session.EventKindPlan,
		Visibility: session.VisibilityCanonical,
		PlanPayload: &session.PlanPayload{
			Entries: entries,
		},
	}
}

func contentToParts(content any, textKind session.PartKind) []session.EventPart {
	switch c := content.(type) {
	case TextContent:
		return textPart(c.Text, textKind)
	case string:
		return textPart(c, textKind)
	case map[string]any:
		return []session.EventPart{mapContentToPart(c, textKind)}
	case []any:
		parts := make([]session.EventPart, 0, len(c))
		for _, item := range c {
			parts = append(parts, contentToParts(item, textKind)...)
		}
		return parts
	case []ToolCallContent:
		return toolCallContentToDisplayParts(c)
	}
	if content == nil {
		return nil
	}
	return []session.EventPart{{Kind: session.PartKindJSON, JSON: content}}
}

func textPart(text string, kind session.PartKind) []session.EventPart {
	if text == "" {
		return nil
	}
	return []session.EventPart{{Kind: kind, Text: text}}
}

func mapContentToPart(m map[string]any, textKind session.PartKind) session.EventPart {
	if text, ok := m["text"].(string); ok {
		return session.EventPart{Kind: textKind, Text: text}
	}
	contentType, _ := m["type"].(string)
	switch contentType {
	case "text":
		return session.EventPart{Kind: textKind, Text: stringValue(m, "text", "content")}
	case "json":
		if value, ok := m["value"]; ok {
			return session.EventPart{Kind: session.PartKindJSON, JSON: value}
		}
		return session.EventPart{Kind: session.PartKindJSON, JSON: m}
	case "file", "file_ref":
		return session.EventPart{
			Kind: session.PartKindFileRef,
			FileRef: &session.PartFileRef{
				URI:      stringValue(m, "uri", "path"),
				MIMEType: stringValue(m, "mimeType", "mime_type"),
				Name:     stringValue(m, "name"),
			},
		}
	case "image", "audio", "video", "media":
		return session.EventPart{
			Kind: session.PartKindMedia,
			Media: &session.PartMedia{
				Modality: firstNonEmpty(contentType, "media"),
				MIMEType: stringValue(m, "mimeType", "mime_type"),
				Data:     decodeMediaData(stringValue(m, "data")),
				URI:      stringValue(m, "uri"),
			},
		}
	default:
		return session.EventPart{Kind: session.PartKindJSON, JSON: m}
	}
}

// extractArgs converts raw input to a map.
func extractArgs(raw any) map[string]any {
	if raw == nil {
		return nil
	}
	if m, ok := raw.(map[string]any); ok {
		return m
	}
	return nil
}

func rawJSON(raw any) string {
	if raw == nil {
		return ""
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	return string(data)
}

func acpProviderMeta(meta map[string]any, locations []ToolCallLocation) map[string]any {
	if len(meta) == 0 && len(locations) == 0 {
		return nil
	}
	out := make(map[string]any)
	if len(meta) > 0 {
		out["acp_meta"] = meta
	}
	if len(locations) > 0 {
		out["acp_locations"] = append([]ToolCallLocation(nil), locations...)
	}
	return out
}

func toolResultParts(tc ToolCallUpdate) []session.EventPart {
	parts := extractContentParts(tc.RawOutput)
	if len(parts) == 0 && len(tc.Content) > 0 {
		return toolCallContentToDisplayParts(tc.Content)
	}
	return parts
}

// extractContentParts converts raw output to EventParts.
func extractContentParts(raw any) []session.EventPart {
	if raw == nil {
		return nil
	}
	if s, ok := raw.(string); ok {
		return []session.EventPart{{Kind: session.PartKindText, Text: s}}
	}
	if m, ok := raw.(map[string]any); ok {
		return []session.EventPart{mapContentToPart(m, session.PartKindText)}
	}
	// Handle []any (common from JSON unmarshal).
	if arr, ok := raw.([]any); ok {
		var result []session.EventPart
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				result = append(result, mapContentToPart(m, session.PartKindText))
			} else {
				result = append(result, session.EventPart{Kind: session.PartKindJSON, JSON: item})
			}
		}
		return result
	}
	return []session.EventPart{{Kind: session.PartKindJSON, JSON: raw}}
}

func toolCallContentToDisplayParts(content []ToolCallContent) []session.EventPart {
	parts := make([]session.EventPart, 0, len(content))
	for _, item := range content {
		switch item.Type {
		case "text":
			if text, ok := item.Content.(string); ok {
				parts = append(parts, session.EventPart{Kind: session.PartKindText, Text: text})
				continue
			}
		case "terminal":
			parts = append(parts, session.EventPart{Kind: session.PartKindJSON, JSON: item})
			continue
		case "content":
			parts = append(parts, contentToParts(item.Content, session.PartKindText)...)
			continue
		}
		parts = append(parts, session.EventPart{Kind: session.PartKindJSON, JSON: item})
	}
	return parts
}

func stringValue(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func decodeMediaData(value string) []byte {
	if value == "" {
		return nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return decoded
	}
	return []byte(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
