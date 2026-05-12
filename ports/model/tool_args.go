package model

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseToolCallArgs parses tool-call argument JSON with compatibility fallback.
// It accepts common model outputs such as fenced JSON and quoted JSON strings.
func ParseToolCallArgs(raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}, nil
	}

	candidates := []string{
		trimmed,
		stripCodeFence(trimmed),
	}

	// Try unquoted JSON string wrapper: "{\"k\":\"v\"}".
	if unquoted, ok := unquoteJSON(trimmed); ok {
		candidates = append(candidates, strings.TrimSpace(unquoted))
		candidates = append(candidates, stripCodeFence(strings.TrimSpace(unquoted)))
	}

	var lastErr error
	for _, c := range candidates {
		if c == "" {
			continue
		}
		parsed, err := decodeJSONObject(c)
		if err == nil {
			return parsed, nil
		}
		lastErr = err
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("invalid tool arguments")
	}
	return nil, lastErr
}

func decodeJSONObject(input string) (map[string]any, error) {
	var out map[string]any
	if err := json.Unmarshal([]byte(input), &out); err != nil {
		return nil, err
	}
	if out == nil {
		return map[string]any{}, nil
	}
	return out, nil
}

func unquoteJSON(input string) (string, bool) {
	trimmed := strings.TrimSpace(input)
	if len(trimmed) < 2 || trimmed[0] != '"' || trimmed[len(trimmed)-1] != '"' {
		return "", false
	}
	var out string
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return "", false
	}
	return out, true
}

func stripCodeFence(input string) string {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		return trimmed
	}
	if !strings.HasPrefix(lines[0], "```") {
		return trimmed
	}
	end := -1
	for i := len(lines) - 1; i >= 1; i-- {
		if strings.TrimSpace(lines[i]) == "```" {
			end = i
			break
		}
	}
	if end <= 0 {
		return trimmed
	}
	body := strings.Join(lines[1:end], "\n")
	return strings.TrimSpace(body)
}
