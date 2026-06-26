package acputil

import (
	"strings"
	"unicode"

	"github.com/OnslaughtSnail/caelis/protocol/acp/client"
	acpschema "github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

// StripTerminalConsoleFenceText removes a whole-output ```console fence added
// by some ACP agents around terminal output. Embedded or non-console fences are
// preserved.
func StripTerminalConsoleFenceText(text string) string {
	body, ok := stripWholeConsoleFence(text)
	if !ok {
		return text
	}
	return body
}

func StripTerminalConsoleFenceUpdate(update client.Update) client.Update {
	switch typed := update.(type) {
	case client.ToolCall:
		return StripTerminalConsoleFenceToolCall(typed)
	case client.ToolCallUpdate:
		return StripTerminalConsoleFenceToolCallUpdate(typed)
	default:
		return update
	}
}

func StripTerminalConsoleFenceToolCall(update client.ToolCall) client.ToolCall {
	stripContent := shouldStripConsoleFenceFromContent(update.Kind, update.Meta)
	update.Content = StripTerminalConsoleFenceToolContent(update.Content, stripContent)
	return update
}

func StripTerminalConsoleFenceToolCallUpdate(update client.ToolCallUpdate) client.ToolCallUpdate {
	kind := ""
	if update.Kind != nil {
		kind = *update.Kind
	}
	stripContent := shouldStripConsoleFenceFromContent(kind, update.Meta)
	update.Content = StripTerminalConsoleFenceToolContent(update.Content, stripContent)
	return update
}

func StripTerminalConsoleFenceToolContent(content []client.ToolCallContent, stripContent bool) []client.ToolCallContent {
	if len(content) == 0 {
		return nil
	}
	out := make([]client.ToolCallContent, len(content))
	copy(out, content)
	for i := range out {
		contentType := strings.TrimSpace(out[i].Type)
		if !strings.EqualFold(contentType, "terminal") && (!stripContent || !strings.EqualFold(contentType, "content")) {
			continue
		}
		out[i].Content = stripTerminalConsoleFenceContentValue(out[i].Content)
	}
	return out
}

func stripTerminalConsoleFenceContentValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return StripTerminalConsoleFenceText(typed)
	case acpschema.TextContent:
		typed.Text = StripTerminalConsoleFenceText(typed.Text)
		return typed
	case *acpschema.TextContent:
		if typed == nil {
			return nil
		}
		copy := *typed
		copy.Text = StripTerminalConsoleFenceText(copy.Text)
		return &copy
	case map[string]any:
		out := cloneMap(typed)
		if isTextContentMap(typed) {
			if text, _ := out["text"].(string); text != "" {
				out["text"] = StripTerminalConsoleFenceText(text)
			}
		}
		return out
	default:
		return value
	}
}

func shouldStripConsoleFenceFromContent(kind string, meta map[string]any) bool {
	if strings.EqualFold(strings.TrimSpace(kind), acpschema.ToolKindExecute) {
		return true
	}
	claudeCode, _ := meta["claudeCode"].(map[string]any)
	toolName, _ := claudeCode["toolName"].(string)
	return strings.EqualFold(strings.TrimSpace(toolName), "Bash")
}

func stripWholeConsoleFence(text string) (string, bool) {
	trimmedLeft := strings.TrimLeftFunc(text, unicode.IsSpace)
	if !strings.HasPrefix(trimmedLeft, "```") {
		return "", false
	}
	afterOpen := trimmedLeft[len("```"):]
	openLineLen := strings.IndexAny(afterOpen, "\r\n")
	if openLineLen < 0 {
		return "", false
	}
	if !strings.EqualFold(strings.TrimSpace(afterOpen[:openLineLen]), "console") {
		return "", false
	}
	bodyStart := openLineLen + 1
	if afterOpen[openLineLen] == '\r' && openLineLen+1 < len(afterOpen) && afterOpen[openLineLen+1] == '\n' {
		bodyStart = openLineLen + 2
	}
	bodyWithClose := afterOpen[bodyStart:]
	trimmedRight := strings.TrimRightFunc(bodyWithClose, unicode.IsSpace)
	if strings.TrimSpace(trimmedRight) == "```" {
		return "", true
	}
	closeLineStart := strings.LastIndexAny(trimmedRight, "\r\n")
	if closeLineStart < 0 {
		return "", false
	}
	if strings.TrimSpace(trimmedRight[closeLineStart+1:]) != "```" {
		return "", false
	}
	return trimmedRight[:closeLineStart+1], true
}

func isTextContentMap(value map[string]any) bool {
	if len(value) == 0 {
		return false
	}
	typ, _ := value["type"].(string)
	return strings.EqualFold(strings.TrimSpace(typ), "text")
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
