package acp

import (
	"encoding/base64"
	"encoding/json"

	"github.com/OnslaughtSnail/caelis/session"
)

// ProjectEvent converts a canonical session event into one or more ACP
// session/update wire objects. Returns nil for events that don't produce
// ACP updates (compaction, lifecycle, etc.).
//
// The projection is computed on demand — nothing is stored.
// Model-critical data always lives in the canonical event payload,
// never only in _meta.
func ProjectEvent(e *session.Event) []Update {
	if e == nil {
		return nil
	}
	switch e.Kind {
	case session.EventKindUser:
		return projectUser(e)
	case session.EventKindAssistant:
		return projectAssistant(e)
	case session.EventKindToolCall:
		return projectToolCall(e)
	case session.EventKindToolResult:
		return projectToolResult(e)
	case session.EventKindPlan:
		return projectPlan(e)
	default:
		return nil
	}
}

// ProjectToNotification wraps an ACP update in a SessionNotification.
func ProjectToNotification(sessionID string, u Update) SessionNotification {
	return SessionNotification{
		SessionID: sessionID,
		Update:    u,
	}
}

// ─── User message ────────────────────────────────────────────────────

func projectUser(e *session.Event) []Update {
	if e.UserPayload == nil {
		return nil
	}
	content := messageContentFromParts(e.UserPayload.Parts)
	if content == nil {
		return nil
	}
	return []Update{ContentChunk{
		SessionUpdate: UpdateUserMessage,
		Content:       content,
	}}
}

// ─── Assistant message ───────────────────────────────────────────────

func projectAssistant(e *session.Event) []Update {
	if e.AssistantPayload == nil {
		return nil
	}
	var updates []Update

	reasoningParts := filterParts(e.AssistantPayload.Parts, func(p session.EventPart) bool {
		return p.Kind == session.PartKindReasoning
	})
	messageParts := filterParts(e.AssistantPayload.Parts, func(p session.EventPart) bool {
		return p.Kind != session.PartKindReasoning
	})

	// Reasoning first, then text (matches old projector behavior).
	if content := messageContentFromParts(reasoningParts); content != nil {
		updates = append(updates, ContentChunk{
			SessionUpdate: UpdateAgentThought,
			Content:       content,
		})
	}
	if content := messageContentFromParts(messageParts); content != nil {
		updates = append(updates, ContentChunk{
			SessionUpdate: UpdateAgentMessage,
			Content:       content,
		})
	}
	return updates
}

// ─── Tool call ───────────────────────────────────────────────────────

func projectToolCall(e *session.Event) []Update {
	if e.ToolCallPayload == nil {
		return nil
	}
	tc := e.ToolCallPayload
	return []Update{ToolCallUpdate{
		SessionUpdate: UpdateToolCall,
		ToolCallID:    tc.CallID,
		Title:         tc.Title,
		Kind:          normalizeKind(tc.Kind, tc.Name),
		Status:        NormalizeToolStatus(tc.Status),
		RawInput:      rawInputFromToolCall(tc),
		Locations:     locationsFromProviderMeta(e.ProviderMeta),
		Meta:          buildToolMeta(e),
	}}
}

// ─── Tool result ─────────────────────────────────────────────────────

func projectToolResult(e *session.Event) []Update {
	if e.ToolResultPayload == nil {
		return nil
	}
	tr := e.ToolResultPayload
	status := NormalizeToolStatus(tr.Status)
	if tr.IsError {
		status = "failed"
	}

	var rawOutput any
	if len(tr.Content) == 1 && tr.Content[0].Kind == session.PartKindText {
		rawOutput = tr.Content[0].Text
	} else if len(tr.Content) > 0 {
		rawOutput = messageContentFromParts(tr.Content)
	}

	return []Update{ToolCallUpdate{
		SessionUpdate: UpdateToolCallInfo,
		ToolCallID:    tr.CallID,
		Title:         tr.Title,
		Kind:          normalizeKind(tr.Kind, tr.Name),
		Status:        status,
		RawOutput:     rawOutput,
		Locations:     locationsFromProviderMeta(e.ProviderMeta),
		Meta:          buildToolResultMeta(e),
	}}
}

// ─── Plan ────────────────────────────────────────────────────────────

func projectPlan(e *session.Event) []Update {
	if e.PlanPayload == nil {
		return nil
	}
	entries := make([]PlanEntry, 0, len(e.PlanPayload.Entries))
	for _, pe := range e.PlanPayload.Entries {
		entries = append(entries, PlanEntry{
			Content: pe.Content,
			Status:  pe.Status,
		})
	}
	return []Update{PlanUpdate{
		SessionUpdate: UpdatePlan,
		Entries:       entries,
	}}
}

// ─── Helpers ─────────────────────────────────────────────────────────

func messageContentFromParts(parts []session.EventPart) any {
	content := make([]any, 0, len(parts))
	for _, p := range parts {
		if item := eventPartToACPContent(p); item != nil {
			content = append(content, item)
		}
	}
	if len(content) == 0 {
		return nil
	}
	if len(content) == 1 {
		return content[0]
	}
	return content
}

func eventPartToACPContent(p session.EventPart) any {
	switch p.Kind {
	case session.PartKindText, session.PartKindReasoning:
		if p.Text == "" {
			return nil
		}
		return TextContent{Type: "text", Text: p.Text}
	case session.PartKindMedia:
		if p.Media == nil {
			return nil
		}
		m := map[string]any{"type": firstNonEmpty(p.Media.Modality, "media")}
		if p.Media.MIMEType != "" {
			m["mimeType"] = p.Media.MIMEType
		}
		if p.Media.URI != "" {
			m["uri"] = p.Media.URI
		}
		if len(p.Media.Data) > 0 {
			m["data"] = base64.StdEncoding.EncodeToString(p.Media.Data)
		}
		return m
	case session.PartKindFileRef:
		if p.FileRef == nil {
			return nil
		}
		m := map[string]any{"type": "file_ref", "uri": p.FileRef.URI}
		if p.FileRef.MIMEType != "" {
			m["mimeType"] = p.FileRef.MIMEType
		}
		if p.FileRef.Name != "" {
			m["name"] = p.FileRef.Name
		}
		return m
	case session.PartKindJSON:
		return map[string]any{"type": "json", "value": p.JSON}
	case session.PartKindToolUse:
		if p.ToolUse == nil {
			return nil
		}
		return map[string]any{
			"type":       "tool_use",
			"toolCallId": p.ToolUse.CallID,
			"name":       p.ToolUse.Name,
			"args":       p.ToolUse.Args,
		}
	case session.PartKindToolResult:
		if p.ToolResultRef == nil {
			return nil
		}
		return map[string]any{
			"type":       "tool_result",
			"toolCallId": p.ToolResultRef.CallID,
			"name":       p.ToolResultRef.Name,
			"content":    p.ToolResultRef.Content,
			"isError":    p.ToolResultRef.IsError,
		}
	default:
		return nil
	}
}

func filterParts(parts []session.EventPart, keep func(session.EventPart) bool) []session.EventPart {
	out := make([]session.EventPart, 0, len(parts))
	for _, p := range parts {
		if keep(p) {
			out = append(out, p)
		}
	}
	return out
}

func rawInputFromToolCall(tc *session.ToolCallPayload) any {
	if len(tc.Args) > 0 {
		return tc.Args
	}
	if tc.ArgJSON == "" {
		return nil
	}
	var raw any
	if err := json.Unmarshal([]byte(tc.ArgJSON), &raw); err != nil {
		return tc.ArgJSON
	}
	return raw
}

func normalizeKind(kind, name string) string {
	if kind != "" {
		return kind
	}
	return ToolKindForName(name)
}

func locationsFromProviderMeta(meta map[string]any) []ToolCallLocation {
	if len(meta) == 0 {
		return nil
	}
	raw, ok := meta["acp_locations"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []ToolCallLocation:
		return append([]ToolCallLocation(nil), v...)
	case []any:
		out := make([]ToolCallLocation, 0, len(v))
		for _, item := range v {
			if loc := locationFromAny(item); loc != nil {
				out = append(out, *loc)
			}
		}
		return out
	default:
		return nil
	}
}

func locationFromAny(v any) *ToolCallLocation {
	switch loc := v.(type) {
	case ToolCallLocation:
		return &loc
	case map[string]any:
		path, _ := loc["path"].(string)
		if path == "" {
			return nil
		}
		out := ToolCallLocation{Path: path}
		switch line := loc["line"].(type) {
		case float64:
			n := int(line)
			out.Line = &n
		case int:
			n := line
			out.Line = &n
		}
		return &out
	default:
		return nil
	}
}

// buildToolMeta builds _meta for tool_call events.
func buildToolMeta(e *session.Event) map[string]any {
	caelis := make(map[string]any)

	if e.Actor.Scope != "" {
		caelis["scope"] = e.Actor.Scope
	}
	if e.Actor.Source != "" {
		caelis["source"] = e.Actor.Source
	}
	if e.RunID != "" {
		caelis["run_id"] = e.RunID
	}

	if e.ToolCallPayload != nil && len(e.ToolCallPayload.Display) > 0 {
		display := make([]map[string]any, 0, len(e.ToolCallPayload.Display))
		for _, p := range e.ToolCallPayload.Display {
			display = append(display, map[string]any{
				"type": string(p.Kind),
				"text": p.Text,
			})
		}
		caelis["display"] = display
	}

	if len(caelis) == 0 {
		return nil
	}
	return map[string]any{"caelis": caelis}
}

// buildToolResultMeta builds _meta for tool_call_update events.
func buildToolResultMeta(e *session.Event) map[string]any {
	caelis := make(map[string]any)

	if e.RunID != "" {
		caelis["run_id"] = e.RunID
	}

	if e.ToolResultPayload != nil && len(e.ToolResultPayload.Display) > 0 {
		display := make([]map[string]any, 0, len(e.ToolResultPayload.Display))
		for _, p := range e.ToolResultPayload.Display {
			display = append(display, map[string]any{"type": string(p.Kind), "text": p.Text})
		}
		caelis["display"] = display
	}

	if e.ToolResultPayload != nil && e.ToolResultPayload.Truncation != nil {
		caelis["truncation"] = map[string]any{
			"strategy":      e.ToolResultPayload.Truncation.Strategy,
			"original_size": e.ToolResultPayload.Truncation.OriginalSize,
			"truncated_to":  e.ToolResultPayload.Truncation.TruncatedTo,
		}
	}

	if len(caelis) == 0 {
		return nil
	}
	return map[string]any{"caelis": caelis}
}
