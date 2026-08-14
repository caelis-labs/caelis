package model

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ParseToolCallArgs parses tool-call argument JSON with compatibility fallback.
// It accepts common model outputs such as fenced JSON and quoted JSON strings.
func ParseToolCallArgs(raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}, nil
	}

	var lastErr error
	for _, c := range toolCallArgCandidates(trimmed) {
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

// ParseToolCallArgsRaw parses tool-call argument JSON and returns valid object
// JSON without decoding numbers through float64. It accepts the same
// compatibility wrappers as ParseToolCallArgs.
func ParseToolCallArgsRaw(raw string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return json.RawMessage(`{}`), nil
	}

	var lastErr error
	for _, c := range toolCallArgCandidates(trimmed) {
		if c == "" {
			continue
		}
		parsed, err := decodeJSONObjectRaw(c)
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

func toolCallArgCandidates(trimmed string) []string {
	candidates := []string{
		trimmed,
		stripCodeFence(trimmed),
	}

	// Try unquoted JSON string wrapper: "{\"k\":\"v\"}".
	if unquoted, ok := unquoteJSON(trimmed); ok {
		candidates = append(candidates, strings.TrimSpace(unquoted))
		candidates = append(candidates, stripCodeFence(strings.TrimSpace(unquoted)))
	}
	return candidates
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

func decodeJSONObjectRaw(input string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(input)
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("invalid tool arguments: multiple JSON values")
		}
		return nil, err
	}
	if decoded == nil {
		return json.RawMessage(`{}`), nil
	}
	if _, ok := decoded.(map[string]any); !ok {
		return nil, fmt.Errorf("json: cannot unmarshal %T into Go value of type map[string]interface {}", decoded)
	}
	return json.RawMessage(trimmed), nil
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
