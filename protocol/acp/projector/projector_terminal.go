package projector

import (
	"encoding/json"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/displaypolicy"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/metautil"
)

func terminalOutputMetaFromEventToolContent(content []session.EventToolContent, displayTerminalID string) map[string]any {
	displayTerminalID = strings.TrimSpace(displayTerminalID)
	var terminalID string
	var text terminalTextAccumulator
	for _, item := range content {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			continue
		}
		if terminalID == "" {
			terminalID = firstNonEmpty(displayTerminalID, strings.TrimSpace(item.TerminalID))
		}
		if item.Text != "" {
			text.appendPart(item.Text)
		}
	}
	if terminalID == "" || text.len() == 0 {
		return nil
	}
	return metautil.WithRuntimeSection(nil, metautil.Terminal, map[string]any{
		"terminal_id": terminalID,
		"data":        text.string(),
	})
}

func terminalOutputMetaFromProtocolContent(content []session.ProtocolToolCallContent, displayTerminalID string) map[string]any {
	displayTerminalID = strings.TrimSpace(displayTerminalID)
	var terminalID string
	var text terminalTextAccumulator
	for _, item := range content {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			continue
		}
		if terminalID == "" {
			terminalID = firstNonEmpty(displayTerminalID, strings.TrimSpace(item.TerminalID))
		}
		if part := terminalTextContent(item.Content); part != "" {
			text.appendPart(part)
		}
	}
	if terminalID == "" || text.len() == 0 {
		return nil
	}
	return metautil.WithRuntimeSection(nil, metautil.Terminal, map[string]any{
		"terminal_id": terminalID,
		"data":        text.string(),
	})
}

type terminalTextAccumulator struct {
	buf      strings.Builder
	lastByte byte
	hasLast  bool
}

func (a *terminalTextAccumulator) len() int {
	if a == nil {
		return 0
	}
	return a.buf.Len()
}

func (a *terminalTextAccumulator) string() string {
	if a == nil {
		return ""
	}
	return a.buf.String()
}

func (a *terminalTextAccumulator) appendPart(part string) {
	if a == nil || part == "" {
		return
	}
	if a.hasLast && a.lastByte != '\n' && !strings.HasPrefix(part, "\n") {
		a.buf.WriteByte('\n')
		a.lastByte = '\n'
		a.hasLast = true
	}
	a.buf.WriteString(part)
	if n := len(part); n > 0 {
		a.lastByte = part[n-1]
		a.hasLast = true
	}
}

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
	terminalID, ok := displaypolicy.DisplayTerminalID(call.ToolCallID, name)
	if !ok {
		return call
	}
	hasDisplayTerminal := false
	for i := range call.Content {
		if strings.EqualFold(strings.TrimSpace(call.Content[i].Type), "terminal") {
			call.Content[i].TerminalID = terminalID
			call.Content[i].Content = nil
			hasDisplayTerminal = true
		}
	}
	if !hasDisplayTerminal {
		call.Content = append(call.Content, ToolCallContent{
			Type:       "terminal",
			TerminalID: terminalID,
		})
	}
	call.Meta = mergeMeta(call.Meta, displayTerminalInfoMeta(terminalID, name, args))
	return call
}

func protocolToolNameForUpdate(event *session.Event, update *session.ProtocolUpdate) string {
	if update != nil {
		if name := terminalInfoToolName(update.Meta); name != "" {
			return name
		}
		if kind := strings.TrimSpace(update.Kind); kind != "" {
			return kind
		}
		if title := strings.Fields(strings.TrimSpace(update.Title)); len(title) > 0 {
			return title[0]
		}
	}
	return ""
}

func terminalInfoToolName(meta map[string]any) string {
	info := metautil.RuntimeSection(meta, metautil.Terminal)
	return firstNonEmpty(
		displaypolicy.MapString(info, "tool"),
		displaypolicy.MapString(info, "tool_name"),
		displaypolicy.MapString(info, "name"),
	)
}

func displayTerminalInfoMeta(terminalID string, name string, args map[string]any) map[string]any {
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" {
		return nil
	}
	info := map[string]any{"terminal_id": terminalID}
	if name = strings.TrimSpace(name); name != "" {
		info["tool"] = name
	}
	if cwd := firstNonEmpty(displaypolicy.MapString(args, "workdir"), displaypolicy.MapString(args, "cwd")); cwd != "" {
		info["cwd"] = cwd
	}
	return metautil.WithRuntimeSection(nil, metautil.Terminal, info)
}
