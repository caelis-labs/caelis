package model

import (
	"encoding/json"
	"strings"
)

// NormalizeToolInput converts provider-emitted tool arguments into the
// provider-neutral JSON object expected by Caelis tools.
func NormalizeToolInput(raw json.RawMessage) json.RawMessage {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return json.RawMessage(`{}`)
	}
	for range 3 {
		if object, ok := normalizeToolInputObject(value); ok {
			return object
		}
		next := stripJSONCodeFence(value)
		if next != value {
			value = next
			continue
		}
		if unquoted, ok := unquoteJSONString(value); ok {
			value = strings.TrimSpace(unquoted)
			continue
		}
		break
	}
	wrapped, _ := json.Marshal(map[string]any{"raw": value})
	return wrapped
}

// NormalizeToolInputString normalizes provider tool arguments carried as text.
func NormalizeToolInputString(raw string) json.RawMessage {
	return NormalizeToolInput(json.RawMessage(strings.TrimSpace(raw)))
}

func normalizeToolInputObject(value string) (json.RawMessage, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return json.RawMessage(`{}`), true
	}
	if value[0] != '{' || !json.Valid([]byte(value)) {
		return nil, false
	}
	return append(json.RawMessage(nil), value...), true
}

func unquoteJSONString(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", false
	}
	var out string
	if err := json.Unmarshal([]byte(value), &out); err != nil {
		return "", false
	}
	return out, true
}

func stripJSONCodeFence(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "```") {
		return value
	}
	lines := strings.Split(value, "\n")
	if len(lines) < 2 || !strings.HasPrefix(strings.TrimSpace(lines[0]), "```") {
		return value
	}
	end := -1
	for i := len(lines) - 1; i >= 1; i-- {
		if strings.TrimSpace(lines[i]) == "```" {
			end = i
			break
		}
	}
	if end <= 0 {
		return value
	}
	return strings.TrimSpace(strings.Join(lines[1:end], "\n"))
}
