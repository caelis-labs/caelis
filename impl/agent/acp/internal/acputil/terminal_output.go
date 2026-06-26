package acputil

import (
	"encoding/json"
	"maps"
	"strings"
	"unicode"

	"github.com/OnslaughtSnail/caelis/protocol/acp/client"
	acpschema "github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

// These keys identify terminal-output-shaped fields seen in ACP tool raw
// output and terminal_output metadata. The whitelist avoids stripping unrelated
// tool ids, command titles, or other literal strings that may validly contain
// markdown fences.
var terminalConsoleFenceOutputKeys = map[string]struct{}{
	"data":           {},
	"error":          {},
	"finalMessage":   {},
	"final_message":  {},
	"latest_output":  {},
	"output":         {},
	"output_preview": {},
	"result":         {},
	"stderr":         {},
	"stdout":         {},
	"text":           {},
}

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
	update.RawOutput = StripTerminalConsoleFenceOutputValue(update.RawOutput)
	update.Content = StripTerminalConsoleFenceToolContent(update.Content)
	update.Meta = StripTerminalConsoleFenceMeta(update.Meta)
	return update
}

func StripTerminalConsoleFenceToolCallUpdate(update client.ToolCallUpdate) client.ToolCallUpdate {
	update.RawOutput = StripTerminalConsoleFenceOutputValue(update.RawOutput)
	update.Content = StripTerminalConsoleFenceToolContent(update.Content)
	update.Meta = StripTerminalConsoleFenceMeta(update.Meta)
	return update
}

func StripTerminalConsoleFenceToolContent(content []client.ToolCallContent) []client.ToolCallContent {
	if len(content) == 0 {
		return nil
	}
	out := make([]client.ToolCallContent, len(content))
	copy(out, content)
	for i := range out {
		if !strings.EqualFold(strings.TrimSpace(out[i].Type), "terminal") {
			continue
		}
		out[i].Content = StripTerminalConsoleFenceOutputValue(out[i].Content)
	}
	return out
}

func StripTerminalConsoleFenceMeta(meta map[string]any) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	out := maps.Clone(meta)
	if raw, ok := out["terminal_output"]; ok {
		out["terminal_output"] = StripTerminalConsoleFenceOutputValue(raw)
	}
	return out
}

func StripTerminalConsoleFenceOutputValue(value any) any {
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
	case json.RawMessage:
		if len(typed) == 0 {
			return typed
		}
		var decoded any
		if err := json.Unmarshal(typed, &decoded); err != nil {
			return typed
		}
		return StripTerminalConsoleFenceOutputValue(decoded)
	case map[string]any:
		out := maps.Clone(typed)
		if isTextContentMap(out) {
			if text, _ := out["text"].(string); text != "" {
				out["text"] = StripTerminalConsoleFenceText(text)
			}
		}
		for key := range terminalConsoleFenceOutputKeys {
			if raw, ok := out[key]; ok {
				out[key] = StripTerminalConsoleFenceOutputValue(raw)
			}
		}
		if raw, ok := out["terminal_output"]; ok {
			out["terminal_output"] = StripTerminalConsoleFenceOutputValue(raw)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = StripTerminalConsoleFenceOutputValue(item)
		}
		return out
	default:
		return value
	}
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
