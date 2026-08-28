package session

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
)

// NormalizeProtocolRawMap converts ACP-compatible raw input or output into the
// stable map shape used by normalized Session semantics and presentation.
func NormalizeProtocolRawMap(raw any) map[string]any {
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
			return NormalizeProtocolRawMap(decoded)
		}
		if text := strings.TrimSpace(string(typed)); text != "" {
			return map[string]any{"text": text}
		}
		return nil
	default:
		if text := ExtractProtocolText(typed); strings.TrimSpace(text) != "" {
			return map[string]any{"text": text}
		}
		if text := strings.TrimSpace(fmt.Sprint(typed)); text != "" && text != "<nil>" {
			return map[string]any{"text": text}
		}
		return nil
	}
}

// ExtractProtocolText reads text from ACP-compatible content values without
// treating unrelated structured payloads as narrative content.
func ExtractProtocolText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.RawMessage:
		if len(typed) == 0 {
			return ""
		}
		var content any
		if err := json.Unmarshal(typed, &content); err != nil {
			return strings.TrimSpace(string(typed))
		}
		return ExtractProtocolText(content)
	case []any:
		var out strings.Builder
		for _, item := range typed {
			out.WriteString(ExtractProtocolText(item))
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
				if text := ExtractProtocolText(nested); text != "" {
					return text
				}
			}
		}
	case fmt.Stringer:
		return typed.String()
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil || object == nil {
			return ""
		}
		return ExtractProtocolText(object)
	}
	return ""
}
