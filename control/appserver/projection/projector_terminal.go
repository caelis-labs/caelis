package projection

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func terminalTextContent(content any) string {
	switch typed := content.(type) {
	case nil:
		return ""
	case eventstream.TextContent:
		if strings.EqualFold(strings.TrimSpace(typed.Type), "text") {
			return typed.Text
		}
		return ""
	case map[string]any:
		if typ, _ := typed["type"].(string); !strings.EqualFold(strings.TrimSpace(typ), "text") {
			return ""
		}
		text, _ := typed["text"].(string)
		return text
	case json.RawMessage:
		if len(typed) == 0 {
			return ""
		}
		var decoded eventstream.TextContent
		if err := json.Unmarshal(typed, &decoded); err == nil && strings.EqualFold(strings.TrimSpace(decoded.Type), "text") {
			return decoded.Text
		}
		var generic any
		if err := json.Unmarshal(typed, &generic); err != nil {
			return ""
		}
		return terminalTextContent(generic)
	default:
		raw, err := json.Marshal(typed)
		if err != nil || len(raw) == 0 {
			return ""
		}
		var decoded eventstream.TextContent
		if err := json.Unmarshal(raw, &decoded); err == nil && strings.EqualFold(strings.TrimSpace(decoded.Type), "text") {
			return decoded.Text
		}
		return ""
	}
}

func withDisplayTerminal(call eventstream.ToolCall, name string, args map[string]any) eventstream.ToolCall {
	call.Meta = acpMetaWithToolName(call.Meta, name)
	terminalID, ok := projectedDisplayTerminalID(call.ToolCallID, name)
	if !ok {
		return call
	}
	call.Meta = metautil.WithTerminalInfo(call.Meta, terminalID)
	call.Meta, call.Content = terminalExtensionMetaFromContent(call.Meta, terminalID, call.Content)
	return call
}

func withDisplayTerminalUpdate(update eventstream.ToolCallUpdate, toolCallID string, name string) eventstream.ToolCallUpdate {
	update.Meta = acpMetaWithToolName(update.Meta, name)
	terminalID, ok := projectedDisplayTerminalID(toolCallID, name)
	if !ok || strings.TrimSpace(terminalID) == "" {
		return update
	}
	update.Meta = metautil.WithTerminalInfo(update.Meta, terminalID)
	update.Meta, update.Content = terminalExtensionMetaFromContent(update.Meta, terminalID, update.Content)
	if updateStatusFinal(update.Status) {
		update.Meta = metautil.WithTerminalExit(update.Meta, terminalID, terminalExitCode(update.RawOutput), nil)
	}
	return update
}

func terminalExtensionMetaFromContent(meta map[string]any, terminalID string, content []eventstream.ToolCallContent) (map[string]any, []eventstream.ToolCallContent) {
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" {
		return meta, content
	}
	if len(content) == 0 {
		return metautil.WithTerminalInfo(meta, terminalID), []eventstream.ToolCallContent{terminalAnchorContent(terminalID)}
	}
	out := make([]eventstream.ToolCallContent, 0, len(content))
	var text strings.Builder
	for _, item := range content {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			out = append(out, item)
			continue
		}
		if id := strings.TrimSpace(item.TerminalID); id != "" {
			terminalID = id
		}
		if part := terminalTextContent(item.Content); part != "" {
			text.WriteString(part)
		}
	}
	if terminalID == "" {
		return meta, out
	}
	meta = metautil.WithTerminalInfo(meta, terminalID)
	if text.Len() > 0 {
		meta = metautil.WithTerminalOutput(meta, terminalID, text.String())
	}
	out = append(out, terminalAnchorContent(terminalID))
	return meta, out
}

func terminalAnchorContent(terminalID string) eventstream.ToolCallContent {
	return eventstream.ToolCallContent{
		Type:       "terminal",
		TerminalID: strings.TrimSpace(terminalID),
	}
}

func updateStatusFinal(status *string) bool {
	if status == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(*status)) {
	case eventstream.ToolStatusCompleted, eventstream.ToolStatusFailed, "interrupted", "cancelled", "canceled", "terminated", "timed_out", "timeout":
		return true
	default:
		return false
	}
}

func terminalExitCode(raw any) *int {
	values, ok := raw.(map[string]any)
	if !ok || len(values) == 0 {
		return nil
	}
	switch typed := values["exit_code"].(type) {
	case int:
		code := typed
		return &code
	case int64:
		code := int(typed)
		return &code
	case float64:
		code := int(typed)
		return &code
	default:
		return nil
	}
}

func protocolToolNameForUpdate(event *session.Event, update *session.ProtocolUpdate) string {
	var updateMeta map[string]any
	var eventMeta map[string]any
	var legacyKind string
	if event != nil {
		eventMeta = event.Meta
	}
	if update != nil {
		updateMeta = update.Meta
		legacyKind = update.Kind
	}
	// Standard ACP kind and title are presentation fields, not exact tool
	// identity. Only canonical Runtime facts or the maintained display extension
	// may populate caelis.runtime.tool.name. Historical internal updates that put
	// a non-standard exact Definition.Name in Kind retain that compatibility.
	candidates := []string{
		protocolCanonicalEventToolName(event, update),
		protocolToolNameFromMeta(updateMeta),
		protocolToolNameFromMeta(eventMeta),
		protocolToolNameFromLegacyKind(legacyKind),
	}
	for _, candidate := range candidates {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return ""
}

// protocolCanonicalEventToolName reads only the canonical durable tool
// payload. It deliberately does not use session.CanonicalToolName here because
// that helper also falls back to protocol title/kind and display metadata;
// those candidates have their own explicit positions in the projection ladder
// below.
func protocolCanonicalEventToolName(event *session.Event, update *session.ProtocolUpdate) string {
	if event == nil {
		return ""
	}
	if event.Tool != nil {
		if name := strings.TrimSpace(event.Tool.Name); name != "" {
			return name
		}
	}
	if event.Message == nil {
		return ""
	}
	callID := ""
	if update != nil {
		callID = strings.TrimSpace(update.ToolCallID)
	}
	if callID == "" && event.Tool != nil {
		callID = strings.TrimSpace(event.Tool.ID)
	}
	calls := event.Message.ToolCalls()
	if callID != "" {
		for _, call := range calls {
			if strings.TrimSpace(call.ID) == callID {
				return strings.TrimSpace(call.Name)
			}
		}
		return ""
	}
	if len(calls) == 1 {
		return strings.TrimSpace(calls[0].Name)
	}
	return ""
}

func protocolToolNameFromMeta(meta map[string]any) string {
	return strings.TrimSpace(metautil.String(
		meta,
		metautil.Root,
		metautil.Runtime,
		metautil.RuntimeTool,
		metautil.RuntimeToolName,
	))
}

func protocolToolNameFromLegacyKind(kind string) string {
	kind = strings.TrimSpace(kind)
	switch strings.ToLower(kind) {
	case "", eventstream.ToolKindRead, eventstream.ToolKindEdit, eventstream.ToolKindDelete, eventstream.ToolKindMove,
		eventstream.ToolKindSearch, eventstream.ToolKindExecute, eventstream.ToolKindThink, eventstream.ToolKindFetch,
		eventstream.ToolKindSwitch, eventstream.ToolKindOther:
		return ""
	default:
		return kind
	}
}
