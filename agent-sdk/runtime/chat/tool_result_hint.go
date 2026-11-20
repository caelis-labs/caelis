package chat

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const (
	toolResultArtifactHintPrefix       = "Caelis runtime artifact: "
	toolResultRuntimeNamespaceKey      = "_caelis"
	toolResultRuntimeArtifactTruncated = "model_visible_content_truncated"
)

// toolResultWithArtifactHint prefers a structured payload for the trusted
// reference, otherwise a text suffix. It preserves part order and media.
// Runtime metadata does not reuse or extend a tool-owned system_hint field.
func toolResultWithArtifactHint(result tool.Result, path string, collision bool) (tool.Result, map[string]any, bool) {
	path = strings.TrimSpace(path)
	if path == "" || len(result.Content) == 0 {
		return tool.Result{}, nil, false
	}
	index := -1
findHintBlock:
	for i, block := range result.Content {
		switch block.(type) {
		case tool.StructuredContent:
			index = i
			break findHintBlock
		case tool.Text:
			if index < 0 {
				index = i
			}
		}
	}
	if index < 0 {
		return tool.Result{}, nil, false
	}
	out, _ := tool.CloneResult(result, nil)
	switch block := out.Content[index].(type) {
	case tool.Text:
		if block != "" {
			block += "\n\n"
		}
		out.Content[index] = block + tool.Text(toolResultArtifactHintPrefix+path+"\nModel-visible tool content was truncated.")
		return out, nil, true
	case tool.StructuredContent:
		if !json.Valid(block.Value) {
			return tool.Result{}, nil, false
		}
		var payload map[string]json.RawMessage
		if json.Unmarshal(block.Value, &payload) != nil || payload == nil {
			payload = map[string]json.RawMessage{"result": block.Value}
		}
		runtimeMetadata := toolResultRuntimeArtifactMetadata(path, collision)
		payload[toolResultRuntimeNamespaceKey] = mustJSON(runtimeMetadata)
		raw, err := json.Marshal(payload)
		if err != nil {
			return tool.Result{}, nil, false
		}
		out.Content[index] = tool.StructuredContent{Value: raw}
		return out, map[string]any{toolResultRuntimeNamespaceKey: runtimeMetadata}, true
	default:
		return tool.Result{}, nil, false
	}
}

func toolResultWithoutReservedNamespace(result tool.Result) (tool.Result, bool) {
	for index, block := range result.Content {
		structured, ok := block.(tool.StructuredContent)
		if !ok {
			continue
		}
		var payload map[string]json.RawMessage
		if json.Unmarshal(structured.Value, &payload) != nil {
			return result, false
		}
		if _, exists := payload[toolResultRuntimeNamespaceKey]; !exists {
			return result, false
		}
		delete(payload, toolResultRuntimeNamespaceKey)
		// All values were validated by Unmarshal above.
		raw, _ := json.Marshal(payload)
		out, _ := tool.CloneResult(result, nil)
		out.Content[index] = tool.StructuredContent{Value: raw}
		return out, true
	}
	return result, false
}

func toolResultRuntimeArtifactMetadata(path string, collision bool) map[string]any {
	artifact := map[string]any{
		"path":                             path,
		toolResultRuntimeArtifactTruncated: true,
	}
	if collision {
		artifact["tool_namespace_collision"] = true
	}
	return map[string]any{
		"runtime": map[string]any{
			"artifact": artifact,
		},
	}
}
