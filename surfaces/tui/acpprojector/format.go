package acpprojector

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuidiff"
)

func FormatToolStart(name string, args map[string]any) string {
	return sanitizeToolDisplayText(FormatToolArgsValue(name, args))
}

func FormatToolContent(content []session.ProtocolToolCallContent) string {
	if len(content) == 0 {
		return ""
	}
	parts := make([]string, 0, len(content))
	for _, item := range content {
		switch strings.TrimSpace(item.Type) {
		case "content":
			if text := toolContentText(item.Content); text != "" {
				parts = append(parts, text)
			}
		case "diff":
			if text := toolDiffText(item); text != "" {
				parts = append(parts, text)
			}
		default:
			continue
		}
	}
	return strings.Join(parts, "\n")
}

func toolDiffText(item session.ProtocolToolCallContent) string {
	oldText := ""
	if item.OldText != nil {
		oldText = *item.OldText
	}
	lines := tuidiff.BuildUnifiedLines(oldText, item.NewText)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func toolContentText(raw any) string {
	switch typed := raw.(type) {
	case nil:
		return ""
	case json.RawMessage:
		if len(typed) == 0 {
			return ""
		}
		var decoded any
		if err := json.Unmarshal(typed, &decoded); err != nil {
			return ""
		}
		return toolContentText(decoded)
	case map[string]any:
		if typeText, _ := typed["type"].(string); !strings.EqualFold(strings.TrimSpace(typeText), "text") {
			return ""
		}
		text, _ := typed["text"].(string)
		return text
	default:
		rawJSON, err := json.Marshal(typed)
		if err != nil || len(rawJSON) == 0 {
			return ""
		}
		var decoded struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(rawJSON, &decoded); err != nil {
			return ""
		}
		if !strings.EqualFold(strings.TrimSpace(decoded.Type), "text") {
			return ""
		}
		return decoded.Text
	}
}

func FormatToolArgsValue(name string, raw any) string {
	return sanitizeToolDisplayText(toolArgsWithName(name, raw))
}

func sanitizeToolDisplayText(text string) string {
	text = strings.TrimSpace(text)
	switch strings.ToLower(text) {
	case "", "null", "{}", "[]", "map[]":
		return ""
	default:
		return text
	}
}

func MarshalToolInput(args map[string]any) string {
	return marshalToolInput(args)
}

func toolArgsWithName(name string, raw any) string {
	values, ok := raw.(map[string]any)
	if !ok || len(values) == 0 {
		if value := strings.TrimSpace(primaryValue(raw)); value != "" {
			return truncateInline(value, 120)
		}
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(name), "LIST") && listArgsHaveNoDisplayValue(values) {
		return ""
	}
	kind := strings.ToLower(strings.TrimSpace(asString(values["kind"])))
	switch kind {
	case "search":
		if query := strings.TrimSpace(firstNonEmpty(asString(values["query"]), asString(values["pattern"]), asString(values["text"]))); query != "" {
			return `for "` + truncateInline(query, 96) + `"`
		}
	case "edit":
		if path := strings.TrimSpace(firstNonEmpty(asString(values["path"]), asString(values["target"]))); path != "" {
			return truncateInline(path, 120)
		}
	case "read", "delete", "move":
		if path := strings.TrimSpace(firstNonEmpty(asString(values["path"]), asString(values["source"]), asString(values["target"]))); path != "" {
			return truncateInline(path, 120)
		}
	case "execute":
		if command := strings.TrimSpace(firstNonEmpty(asString(values["command"]), asString(values["cmd"]))); command != "" {
			return truncateInline(command, 120)
		}
	case "fetch":
		if url := strings.TrimSpace(firstNonEmpty(asString(values["url"]), asString(values["uri"]))); url != "" {
			return truncateInline(url, 120)
		}
	}
	if value := strings.TrimSpace(primaryValue(values)); value != "" {
		return truncateInline(value, 120)
	}
	return ""
}

func listArgsHaveNoDisplayValue(values map[string]any) bool {
	for _, key := range []string{"path", "target", "source", "cwd"} {
		if strings.TrimSpace(asString(values[key])) != "" {
			return false
		}
	}
	return true
}

func primaryValue(raw any) string {
	switch typed := raw.(type) {
	case nil:
		return ""
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case map[string]any:
		for _, key := range []string{"path", "command", "query", "url", "target", "source", "text"} {
			if value := strings.TrimSpace(asString(typed[key])); value != "" {
				return value
			}
		}
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return fmt.Sprint(raw)
	}
	return string(data)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func asString(v any) string {
	switch typed := v.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
}

func truncateInline(input string, limit int) string {
	input = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(input, "\n", " "), "\t", " "))
	if limit <= 0 || len([]rune(input)) <= limit {
		return input
	}
	runes := []rune(input)
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

func marshalToolInput(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	data, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return string(data)
}
