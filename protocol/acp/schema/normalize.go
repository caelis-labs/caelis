package schema

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
)

// NormalizeRawMap converts ACP raw input/output values into a stable map shape
// for canonical storage and transcript projection.
func NormalizeRawMap(raw any) map[string]any {
	switch typed := raw.(type) {
	case nil:
		return nil
	case map[string]any:
		return maps.Clone(typed)
	case json.RawMessage:
		if len(typed) == 0 {
			return nil
		}
		var decoded any
		if err := json.Unmarshal(typed, &decoded); err == nil {
			return NormalizeRawMap(decoded)
		}
		if text := strings.TrimSpace(string(typed)); text != "" {
			return map[string]any{"text": text}
		}
		return nil
	default:
		if text := ExtractTextValue(typed); strings.TrimSpace(text) != "" {
			return map[string]any{"text": text}
		}
		if text := strings.TrimSpace(fmt.Sprint(typed)); text != "" && text != "<nil>" {
			return map[string]any{"text": text}
		}
		return nil
	}
}

func TextFromRawContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var content any
	if err := json.Unmarshal(raw, &content); err != nil {
		return strings.TrimSpace(string(raw))
	}
	return ExtractTextValue(content)
}

func ExtractTextValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case TextContent:
		return typed.Text
	case *TextContent:
		if typed == nil {
			return ""
		}
		return typed.Text
	case json.RawMessage:
		return TextFromRawContent(typed)
	case []any:
		var out strings.Builder
		for _, item := range typed {
			out.WriteString(ExtractTextValue(item))
		}
		return out.String()
	case map[string]any:
		if typ, _ := typed["type"].(string); strings.EqualFold(strings.TrimSpace(typ), "text") {
			if text, _ := typed["text"].(string); text != "" {
				return text
			}
		}
		for _, key := range []string{"text", "content", "detailedContent"} {
			if nested, ok := typed[key]; ok {
				if text := ExtractTextValue(nested); text != "" {
					return text
				}
			}
		}
	case fmt.Stringer:
		return typed.String()
	}
	return ""
}
