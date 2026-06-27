package acpconvert

import (
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/client"
	"github.com/OnslaughtSnail/caelis/protocol/acp/metautil"
	acpschema "github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func ToolDisplayName(kind string, title string) string {
	if kind = strings.TrimSpace(kind); kind != "" {
		return kind
	}
	return strings.TrimSpace(title)
}

func ToolRawInput(raw any) map[string]any {
	out := acpschema.NormalizeRawMap(raw)
	if len(out) == 0 {
		return nil
	}
	return out
}

func ToolRawOutput(raw any) map[string]any {
	out := acpschema.NormalizeRawMap(raw)
	if len(out) == 0 {
		return nil
	}
	return out
}

func ToolProtocolUpdate(updateType string, tool *session.ProtocolToolCall, meta map[string]any) *session.ProtocolUpdate {
	if tool == nil {
		return &session.ProtocolUpdate{SessionUpdate: strings.TrimSpace(updateType)}
	}
	update := &session.ProtocolUpdate{
		SessionUpdate: strings.TrimSpace(updateType),
		ToolCallID:    strings.TrimSpace(tool.ID),
		Kind:          strings.TrimSpace(tool.Kind),
		Title:         strings.TrimSpace(tool.Title),
		Status:        strings.TrimSpace(tool.Status),
		RawInput:      maps.Clone(tool.RawInput),
		RawOutput:     maps.Clone(tool.RawOutput),
		Meta:          metautil.CloneMap(meta),
	}
	if len(tool.Content) > 0 {
		update.Content = session.CloneProtocolToolCallContent(tool.Content)
	}
	return update
}

func ToolContent(content []client.ToolCallContent) []session.ProtocolToolCallContent {
	if len(content) == 0 {
		return nil
	}
	out := make([]session.ProtocolToolCallContent, 0, len(content))
	for _, item := range content {
		var oldText *string
		if item.OldText != nil {
			value := *item.OldText
			oldText = &value
		}
		out = append(out, session.ProtocolToolCallContent{
			Type:       strings.TrimSpace(item.Type),
			Content:    item.Content,
			TerminalID: strings.TrimSpace(item.TerminalID),
			Path:       strings.TrimSpace(item.Path),
			OldText:    oldText,
			NewText:    item.NewText,
		})
	}
	return session.CloneProtocolToolCallContent(out)
}
