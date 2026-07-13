package turningress

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

// StreamRequestFromACPEvent derives the private live-delivery subscription
// anchor from a main running tool update. The task metadata layout is a
// transitional Control input contract; projection and client delivery remain
// owned by the live feed broker and projector respectively.
func StreamRequestFromACPEvent(env eventstream.Envelope) (acpprojector.StreamRequest, bool) {
	update, ok := env.Update.(schema.ToolCallUpdate)
	if !ok {
		return acpprojector.StreamRequest{}, false
	}
	status := strings.TrimSpace(stringFromPtr(update.Status))
	if status != schema.ToolStatusInProgress {
		return acpprojector.StreamRequest{}, false
	}
	meta := mergeStreamRequestMeta(update.Meta, env.Meta)
	toolName := streamToolNameFromACPUpdate(meta, update)
	if toolName == "" {
		return acpprojector.StreamRequest{}, false
	}
	taskID := firstNonEmpty(
		streamRequestMetaString(meta, gateway.EventMetaRoot, gateway.EventMetaRuntime, "task", "task_id"),
		streamRequestMetaString(meta, gateway.EventMetaRoot, gateway.EventMetaRuntime, "task", "internal_task_id"),
	)
	displayTerminalID := acpTerminalID(update.Content)
	terminalID := firstNonEmpty(
		streamRequestMetaString(meta, gateway.EventMetaRoot, gateway.EventMetaRuntime, "task", "terminal_id"),
		displayTerminalID,
	)
	if taskID == "" && terminalID == "" {
		return acpprojector.StreamRequest{}, false
	}
	scope := env.Scope
	if scope == "" {
		scope = eventstream.ScopeMain
	}
	sessionID := firstNonEmpty(
		strings.TrimSpace(env.SessionID),
		streamRequestMetaString(meta, gateway.EventMetaRoot, gateway.EventMetaRuntime, "task", "session_id"),
	)
	req := acpprojector.StreamRequest{
		HandleID:   strings.TrimSpace(env.HandleID),
		RunID:      strings.TrimSpace(env.RunID),
		TurnID:     strings.TrimSpace(env.TurnID),
		SessionRef: session.SessionRef{SessionID: sessionID},
		CallID:     strings.TrimSpace(update.ToolCallID),
		ToolName:   toolName,
		RawInput:   streamRequestRawInput(update.RawInput),
		Ref: stream.Ref{
			SessionID:  sessionID,
			TaskID:     taskID,
			TerminalID: terminalID,
		},
		DisplayTerminalID: firstNonEmpty(displayTerminalID, strings.TrimSpace(update.ToolCallID)),
		Cursor: stream.Cursor{
			Output: streamRequestInt64(streamRequestMetaAny(meta, gateway.EventMetaRoot, gateway.EventMetaRuntime, "task", "output_cursor")),
		},
		Origin: &acpprojector.StreamOrigin{
			Scope:         scope,
			ScopeID:       strings.TrimSpace(env.ScopeID),
			Actor:         strings.TrimSpace(env.Actor),
			ParticipantID: strings.TrimSpace(env.ParticipantID),
		},
		Actor:         strings.TrimSpace(env.Actor),
		Scope:         scope,
		ParticipantID: strings.TrimSpace(env.ParticipantID),
	}
	if req.SessionRef.SessionID == "" || req.Ref.SessionID == "" || req.CallID == "" || req.ToolName == "" {
		return acpprojector.StreamRequest{}, false
	}
	return req, true
}

func streamToolNameFromACPUpdate(meta map[string]any, update schema.ToolCallUpdate) string {
	if name := strings.TrimSpace(streamRequestMetaString(meta, gateway.EventMetaRoot, gateway.EventMetaRuntime, gateway.EventMetaRuntimeTool, gateway.EventMetaRuntimeToolName)); name != "" {
		return name
	}
	if name := streamToolNameFromTitle(stringFromPtr(update.Title)); name != "" {
		return name
	}
	return strings.TrimSpace(stringFromPtr(update.Kind))
}

func streamToolNameFromTitle(title string) string {
	title = strings.TrimSpace(title)
	fields := strings.Fields(title)
	if len(fields) == 0 {
		return ""
	}
	if canonical, ok := names.Resolve(strings.Trim(fields[0], ":")); ok {
		switch canonical {
		case names.RunCommand, names.Spawn:
			return canonical
		}
	}
	return ""
}

func acpTerminalID(content []schema.ToolCallContent) string {
	for _, item := range content {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			continue
		}
		if terminalID := strings.TrimSpace(item.TerminalID); terminalID != "" {
			return terminalID
		}
	}
	return ""
}

func mergeStreamRequestMeta(base map[string]any, overlay map[string]any) map[string]any {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	out := map[string]any{}
	for key, value := range base {
		out[key] = value
	}
	for key, value := range overlay {
		if baseMap, ok := out[key].(map[string]any); ok {
			if overlayMap, ok := value.(map[string]any); ok {
				out[key] = mergeStreamRequestMeta(baseMap, overlayMap)
				continue
			}
		}
		out[key] = value
	}
	return out
}

func streamRequestMetaAny(values map[string]any, path ...string) any {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = mapped[key]
	}
	return current
}

func streamRequestMetaString(values map[string]any, path ...string) string {
	text, _ := streamRequestMetaAny(values, path...).(string)
	return strings.TrimSpace(text)
}

func streamRequestRawInput(value any) map[string]any {
	if mapped, ok := value.(map[string]any); ok {
		out := make(map[string]any, len(mapped))
		for key, value := range mapped {
			out[key] = value
		}
		return out
	}
	return nil
}

func streamRequestInt64(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

func stringFromPtr(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
