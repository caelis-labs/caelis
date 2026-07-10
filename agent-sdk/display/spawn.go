package display

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
)

func SpawnFullDisplayArgs(raw map[string]any) string {
	raw = NormalizeSpawnDisplayRawMap(raw)
	prompt := strings.Join(strings.Fields(NormalizeDisplayArg(MapString(raw, "prompt"))), " ")
	agent := strings.TrimSpace(MapString(raw, "agent"))
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
	handle := firstNonEmpty(
		spawnDisplayHandle(MapString(raw, "handle")),
		spawnDisplayHandle(MapString(raw, "mention")),
		spawnDisplayHandle(MapString(raw, "task_id")),
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

func SpawnDisplayInputForResult(input map[string]any, output map[string]any) map[string]any {
	output = NormalizeSpawnDisplayRawMap(output)
	merged := NormalizeSpawnDisplayRawMap(input)
	if merged == nil {
		merged = map[string]any{}
	}
	for _, key := range []string{"agent", "prompt", "handle", "mention", "task_id"} {
		if strings.TrimSpace(MapString(merged, key)) != "" {
			continue
		}
		if value, ok := output[key]; ok {
			merged[key] = value
		}
	}
	return merged
}

func NormalizeSpawnDisplayRawMap(raw map[string]any) map[string]any {
	if len(raw) == 0 {
		return jsonvalue.CloneMap(raw)
	}
	out := jsonvalue.CloneMap(raw)
	for _, key := range []string{"text", "result", "output", "summary", "content", "stdout", "output_preview", "tool_output", "toolOutput", "raw_output", "rawOutput", "message"} {
		text := MapString(out, key)
		decoded, remainder, ok := SplitLeadingJSONObject(text)
		if !ok || !IsSpawnDisplayJSONObject(decoded) {
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

func SplitLeadingJSONObject(text string) (map[string]any, string, bool) {
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

func IsSpawnDisplayJSONObject(decoded map[string]any) bool {
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

func SpawnDisplayTextCandidate(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	decoded, remainder, ok := SplitLeadingJSONObject(text)
	if !ok || !IsSpawnDisplayJSONObject(decoded) {
		return text
	}
	return strings.TrimSpace(remainder)
}

func SanitizeSpawnHeaderArgs(args string) string {
	args = strings.TrimSpace(args)
	if strings.EqualFold(args, "SPAWN") {
		return ""
	}
	for _, prefix := range []string{"SPAWN ", "spawn "} {
		if strings.HasPrefix(args, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(args, prefix))
		}
	}
	return args
}
