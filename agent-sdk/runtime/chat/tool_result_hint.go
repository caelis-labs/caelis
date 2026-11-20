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
// reference, otherwise a text notice. It preserves original block order and
// never reuses or extends a tool-owned system_hint field.
func toolResultWithArtifactHint(result tool.Result, path string, collision bool) (tool.Result, map[string]any, bool) {
	path = strings.TrimSpace(path)
	if path == "" || len(result.Content) == 0 {
		return tool.Result{}, nil, false
	}
	for index, content := range result.Content {
		block, ok := content.(tool.StructuredContent)
		if !ok {
			continue
		}
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
		out, _ := tool.CloneResult(result, nil)
		out.Content[index] = tool.StructuredContent{Value: raw}
		return out, map[string]any{toolResultRuntimeNamespaceKey: runtimeMetadata}, true
	}
	hint := tool.Text(toolResultArtifactHintPrefix + path + "\nModel-visible tool content was truncated.")
	if len(result.Content) == 1 {
		if text, ok := result.Content[0].(tool.Text); ok {
			result.Content = []tool.ContentBlock{text + "\n\n" + hint}
			return result, nil, true
		}
	}
	// A large resource can consume the whole budget before a later text block.
	result.Content = append([]tool.ContentBlock{hint}, result.Content...)
	return result, nil, true
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
