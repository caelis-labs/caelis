package chat

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const (
	toolResultArtifactHintPrefix       = "Caelis runtime artifact: "
	toolResultRuntimeNamespaceKey      = "_caelis"
	toolResultRuntimeArtifactTruncated = "model_visible_content_truncated"
)

// toolResultWithArtifactHint handles the single text-or-JSON payload accepted
// by the artifact store. Runtime artifact metadata does not reuse or extend a
// tool-owned system_hint field.
func toolResultWithArtifactHint(result tool.Result, path string, collision bool) (tool.Result, map[string]any, bool) {
	path = strings.TrimSpace(path)
	if path == "" || len(result.Content) != 1 {
		return tool.Result{}, nil, false
	}
	out, _ := tool.CloneResult(result, nil)
	// CloneResult copies the part slice; hint injection also needs isolated part
	// payload pointers so the pre-truncation result remains the artifact source.
	out.Content = model.CloneParts(result.Content)
	part := &out.Content[0]
	switch {
	case part.Text != nil:
		if part.Text.Text != "" {
			part.Text.Text += "\n\n"
		}
		part.Text.Text += toolResultArtifactHintPrefix + path + "\nModel-visible tool content was truncated."
		return out, nil, true
	case part.JSON != nil:
		var payload any
		if json.Unmarshal(part.JSON.Value, &payload) != nil {
			return tool.Result{}, nil, false
		}
		runtimeMetadata := toolResultRuntimeArtifactMetadata(path, collision)
		protected := map[string]any{toolResultRuntimeNamespaceKey: runtimeMetadata}
		if object, ok := payload.(map[string]any); ok {
			object[toolResultRuntimeNamespaceKey] = runtimeMetadata
			part.JSON.Value = mustJSON(object)
			return out, protected, true
		}
		part.JSON.Value = mustJSON(map[string]any{
			"result":                      payload,
			toolResultRuntimeNamespaceKey: runtimeMetadata,
		})
		return out, protected, true
	default:
		return tool.Result{}, nil, false
	}
}

func toolResultWithoutReservedNamespace(result tool.Result) (tool.Result, bool) {
	out, _ := tool.CloneResult(result, nil)
	out.Content = model.CloneParts(result.Content)
	collision := false
	for index := range out.Content {
		part := &out.Content[index]
		if part.JSON == nil {
			continue
		}
		var payload map[string]any
		if json.Unmarshal(part.JSON.Value, &payload) != nil {
			continue
		}
		if _, exists := payload[toolResultRuntimeNamespaceKey]; !exists {
			continue
		}
		delete(payload, toolResultRuntimeNamespaceKey)
		part.JSON.Value = mustJSON(payload)
		collision = true
	}
	return out, collision
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
