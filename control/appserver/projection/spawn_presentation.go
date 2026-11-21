package projection

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// SpawnFullDisplayArgs projects the thread label and prompt from tool arguments
// or a legacy result envelope.
func SpawnFullDisplayArgs(raw map[string]any) string {
	raw = normalizeSpawnDisplayRawMap(raw)
	prompt := strings.Join(strings.Fields(display.MapString(raw, "prompt")), " ")
	agent := strings.TrimSpace(display.MapString(raw, "agent"))
	target := spawnDisplayTarget(raw, agent)
	if target == "" {
		return prompt
	}
	if prompt == "" {
		return target
	}
	return target + ": " + prompt
}

func spawnDisplayTarget(raw map[string]any, agent string) string {
	handle := firstTaskDisplayValue(
		spawnDisplayHandle(display.MapString(raw, "handle")),
		spawnDisplayHandle(display.MapString(raw, "mention")),
		spawnDisplayHandle(display.MapString(raw, "task_id")),
	)
	agent = strings.TrimSpace(agent)
	if handle == "" {
		return agent
	}
	if agent != "" && !strings.EqualFold(handle, agent) {
		return handle + "[" + agent + "]"
	}
	return handle
}

func spawnDisplayHandle(value string) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "@")
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "task-") || strings.Contains(lower, "-task-") {
		return ""
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return ""
	}
	return value
}

// SpawnDisplayInputForResult fills missing display arguments from a result.
func SpawnDisplayInputForResult(input map[string]any, output map[string]any) map[string]any {
	output = normalizeSpawnDisplayRawMap(output)
	merged := normalizeSpawnDisplayRawMap(input)
	if merged == nil {
		merged = map[string]any{}
	}
	for _, key := range []string{"agent", "prompt", "handle", "mention", "task_id"} {
		if strings.TrimSpace(display.MapString(merged, key)) != "" {
			continue
		}
		if value, ok := output[key]; ok {
			merged[key] = value
		}
	}
	return merged
}

func normalizeSpawnDisplayRawMap(raw map[string]any) map[string]any {
	if len(raw) == 0 {
		return session.CloneState(raw)
	}
	out := session.CloneState(raw)
	for _, key := range []string{"text", "result", "output", "summary", "content", "stdout", "output_preview", "tool_output", "toolOutput", "raw_output", "rawOutput", "message"} {
		text := display.MapString(out, key)
		decoded, remainder, ok := splitLeadingJSONObject(text)
		if !ok || !isSpawnDisplayJSONObject(decoded) {
			continue
		}
		for decodedKey, value := range decoded {
			if _, exists := out[decodedKey]; !exists {
				out[decodedKey] = value
			}
		}
		if strings.TrimSpace(remainder) != "" {
			out[key] = strings.TrimSpace(remainder)
		} else {
			delete(out, key)
		}
	}
	return out
}

func splitLeadingJSONObject(text string) (map[string]any, string, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "{") {
		return nil, text, false
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	var decoded map[string]any
	if err := decoder.Decode(&decoded); err != nil || len(decoded) == 0 {
		return nil, text, false
	}
	offset := int(decoder.InputOffset())
	if offset < 0 || offset > len(text) {
		return nil, text, false
	}
	return decoded, strings.TrimSpace(text[offset:]), true
}

func isSpawnDisplayJSONObject(decoded map[string]any) bool {
	if len(decoded) == 0 {
		return false
	}
	for _, key := range []string{"agent", "prompt", "task_id", "handle", "mention", "terminal_id", "running", "supports_input", "supports_cancel"} {
		if _, ok := decoded[key]; ok {
			return true
		}
	}
	return false
}
