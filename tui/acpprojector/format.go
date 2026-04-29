package acpprojector

import (
	"encoding/json"
	"fmt"
	"strings"
)

func FormatToolStart(name string, args map[string]any) string {
	return sanitizeToolDisplayText(FormatToolArgsValue(name, args))
}

func FormatToolResult(name string, args map[string]any, result map[string]any, status string) string {
	if result != nil {
		if display := strings.TrimSpace(asString(result["summary"])); display != "" {
			return display
		}
	}
	name = strings.TrimSpace(strings.ToUpper(name))
	_ = args
	summary := strings.TrimSpace(toolOutput(result))
	if summary == "" {
		if strings.EqualFold(status, "failed") {
			summary = "failed"
		} else {
			summary = "completed"
		}
	}
	if strings.EqualFold(summary, name) {
		if strings.EqualFold(status, "failed") {
			return "failed"
		}
		return "completed"
	}
	return summary
}

func FormatToolArgsValue(name string, raw any) string {
	values, ok := raw.(map[string]any)
	if ok && values != nil {
		if display := sanitizeToolDisplayText(asString(values["_display"])); display != "" {
			return display
		}
		if _, hasDisplay := values["_display"]; hasDisplay && len(values) == 1 {
			return ""
		}
	}
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
	kind := strings.ToLower(strings.TrimSpace(firstNonEmpty(asString(values["_acp_kind"]), asString(values["kind"]))))
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
	if title := strings.TrimSpace(asString(values["_acp_title"])); title != "" {
		if summary := titleSummary(name, kind, title); summary != "" {
			return truncateInline(summary, 120)
		}
	}
	if value := strings.TrimSpace(primaryValue(values)); value != "" {
		return truncateInline(value, 120)
	}
	return ""
}

func toolOutput(raw any) string {
	values, ok := raw.(map[string]any)
	if !ok || len(values) == 0 {
		if ok && len(values) == 0 {
			return ""
		}
		if value := strings.TrimSpace(extractACPDisplayText(raw)); value != "" {
			return truncateInline(value, 160)
		}
		if value := strings.TrimSpace(primaryValue(raw)); value != "" {
			return truncateInline(value, 160)
		}
		return ""
	}
	for _, key := range []string{"error", "stderr", "message", "summary", "result", "stdout"} {
		if value := strings.TrimSpace(asString(values[key])); value != "" && value != "{}" && value != "map[]" {
			return truncateInline(value, 160)
		}
	}
	for _, key := range []string{"content", "detailedContent", "text"} {
		if value := strings.TrimSpace(extractACPDisplayText(values[key])); value != "" {
			return truncateInline(value, 160)
		}
	}
	if path := strings.TrimSpace(asString(values["path"])); path != "" {
		if exitCode, ok := asInt(values["exit_code"]); ok {
			return truncateInline(fmt.Sprintf("%s (exit %d)", path, exitCode), 160)
		}
		return truncateInline(path, 160)
	}
	if exitCode, ok := asInt(values["exit_code"]); ok {
		return fmt.Sprintf("exit %d", exitCode)
	}
	if value := strings.TrimSpace(primaryValue(values)); value != "" {
		return truncateInline(value, 160)
	}
	rawJSON, err := json.Marshal(values)
	if err != nil {
		return ""
	}
	return truncateInline(string(rawJSON), 160)
}

func extractACPDisplayText(raw any) string {
	switch typed := raw.(type) {
	case nil:
		return ""
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return ""
		}
		if decoded := decodeACPJSONTextString(text); decoded != "" {
			return decoded
		}
		return normalizeInlineText(text)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(extractACPDisplayText(item)); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if text := strings.TrimSpace(extractACPDisplayText(typed["text"])); text != "" {
			return text
		}
		if text := strings.TrimSpace(extractACPDisplayText(typed["value"])); text != "" {
			return text
		}
		if text := strings.TrimSpace(extractACPDisplayText(typed["content"])); text != "" {
			return text
		}
		if text := strings.TrimSpace(extractACPDisplayText(typed["detailedContent"])); text != "" {
			return text
		}
	}
	return ""
}

func decodeACPJSONTextString(input string) string {
	if !strings.HasPrefix(strings.TrimSpace(input), "{") && !strings.HasPrefix(strings.TrimSpace(input), "[") {
		return ""
	}
	var payload any
	if err := json.Unmarshal([]byte(input), &payload); err != nil {
		return ""
	}
	return normalizeInlineText(extractACPDisplayText(payload))
}

func normalizeInlineText(input string) string {
	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")
	lines := strings.Split(input, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
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

func titleSummary(name string, kind string, title string) string {
	title = strings.TrimSpace(title)
	name = strings.TrimSpace(strings.ToUpper(name))
	kind = strings.TrimSpace(strings.ToLower(kind))
	if title == "" {
		return ""
	}
	if prefix := name + " "; len(title) > len(prefix) && strings.EqualFold(title[:len(prefix)], prefix) {
		return strings.TrimSpace(title[len(prefix):])
	}
	if prefix := kind + " "; len(title) > len(prefix) && strings.EqualFold(title[:len(prefix)], prefix) {
		return strings.TrimSpace(title[len(prefix):])
	}
	return title
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

func asInt(v any) (int, bool) {
	switch typed := v.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
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
