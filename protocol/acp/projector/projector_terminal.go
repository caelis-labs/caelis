package projector

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func terminalTextContent(content any) string {
	switch typed := content.(type) {
	case nil:
		return ""
	case TextContent:
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
		var decoded TextContent
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
		var decoded TextContent
		if err := json.Unmarshal(raw, &decoded); err == nil && strings.EqualFold(strings.TrimSpace(decoded.Type), "text") {
			return decoded.Text
		}
		return ""
	}
}

func withDisplayTerminal(call ToolCall, name string, args map[string]any) ToolCall {
	terminalID, ok := display.DisplayTerminalID(call.ToolCallID, name)
	if !ok {
		return call
	}
	call.Meta = metautil.WithTerminalInfo(call.Meta, terminalID)
	call.Meta, call.Content = terminalExtensionMetaFromContent(call.Meta, terminalID, call.Content)
	return call
}

func withDisplayTerminalUpdate(update ToolCallUpdate, toolCallID string, name string) ToolCallUpdate {
	terminalID, ok := display.DisplayTerminalID(toolCallID, name)
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

func terminalExtensionMetaFromContent(meta map[string]any, terminalID string, content []ToolCallContent) (map[string]any, []ToolCallContent) {
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" {
		return meta, content
	}
	if len(content) == 0 {
		return metautil.WithTerminalInfo(meta, terminalID), []ToolCallContent{terminalAnchorContent(terminalID)}
	}
	out := make([]ToolCallContent, 0, len(content))
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

func terminalAnchorContent(terminalID string) ToolCallContent {
	return ToolCallContent{
		Type:       "terminal",
		TerminalID: strings.TrimSpace(terminalID),
	}
}

func updateStatusFinal(status *string) bool {
	if status == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(*status)) {
	case ToolStatusCompleted, ToolStatusFailed, "interrupted", "cancelled", "canceled", "terminated", "timed_out", "timeout":
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
	if update != nil {
		if name := protocolToolNameFromRawInput(update.RawInput); name != "" {
			return name
		}
		if name := protocolToolNameFromKind(update.Kind); name != "" {
			return name
		}
		if title := strings.Fields(strings.TrimSpace(update.Title)); len(title) > 0 {
			if name := protocolToolNameFromKind(title[0]); name != "" {
				return name
			}
			return title[0]
		}
	}
	return ""
}

func protocolToolNameFromRawInput(rawInput map[string]any) string {
	if len(rawInput) == 0 {
		return ""
	}
	if command := display.MapString(rawInput, "command"); command != "" {
		return "RUN_COMMAND"
	}
	if agent := display.MapString(rawInput, "agent"); agent != "" {
		return "SPAWN"
	}
	if prompt := display.MapString(rawInput, "prompt"); prompt != "" {
		return "SPAWN"
	}
	return ""
}

func protocolToolNameFromKind(kind string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return ""
	}
	switch strings.ToUpper(kind) {
	case "RUN_COMMAND", "SPAWN", "TASK", "READ", "LIST", "GLOB", "SEARCH", "WEB_SEARCH", "WEB_FETCH", "RG", "FIND", "WRITE", "PATCH":
		return strings.ToUpper(kind)
	}
	switch strings.ToLower(kind) {
	case ToolKindExecute:
		return "RUN_COMMAND"
	case ToolKindRead:
		return "READ"
	case ToolKindSearch, ToolKindFetch:
		return "SEARCH"
	case ToolKindEdit, ToolKindDelete, ToolKindMove:
		return "PATCH"
	}
	return kind
}
