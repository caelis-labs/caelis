package chat

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const (
	toolResultArtifactHintPrefix       = "Caelis runtime artifact: "
	toolResultRuntimeNamespaceKey      = "_caelis"
	toolResultRuntimeArtifactTruncated = "model_visible_content_truncated"
)

// ResultArtifactStore retains complete tool results for later retrieval by host-provided tools.
// Save reads the result and returns a short, model-readable reference, such as a path or URI.
// Implementations must support concurrent calls. A failed save leaves the bounded view available.
type ResultArtifactStore interface {
	Save(context.Context, tool.Result) (string, error)
}

func canonicalToolResult(ctx context.Context, result tool.Result, artifacts ResultArtifactStore) (tool.Result, map[string]any) {
	policy := tool.DefaultTruncationPolicy()
	original := result
	result, reservedCollision := toolResultWithoutReservedNamespace(result)
	var protectedJSONFields map[string]any
	if artifacts != nil && tool.ResultNeedsTruncation(result, policy) {
		if ref, err := artifacts.Save(ctx, original); err == nil && strings.TrimSpace(ref) != "" {
			result, protectedJSONFields = toolResultWithArtifactHint(result, ref, reservedCollision)
		}
	}
	canonical, info := tool.TruncateResultWithInfoAndProtectedJSONFields(result, policy, protectedJSONFields)
	return canonical, mergeEventMeta(
		toolTruncationEventMeta(info),
		toolReservedNamespaceCollisionEventMeta(reservedCollision),
	)
}

// toolResultWithArtifactHint prefers a structured payload for the trusted
// reference, otherwise a text notice. It preserves original block order and
// never reuses or extends a tool-owned system_hint field.
func toolResultWithArtifactHint(result tool.Result, ref string, collision bool) (tool.Result, map[string]any) {
	for index, content := range result.Content {
		block, ok := content.(tool.StructuredContent)
		if !ok {
			continue
		}
		if !json.Valid(block.Value) {
			break
		}
		var payload map[string]json.RawMessage
		if json.Unmarshal(block.Value, &payload) != nil || payload == nil {
			payload = map[string]json.RawMessage{"result": block.Value}
		}
		runtimeMetadata := toolResultRuntimeArtifactMetadata(ref, collision)
		payload[toolResultRuntimeNamespaceKey] = mustJSON(runtimeMetadata)
		raw, err := json.Marshal(payload)
		if err != nil {
			break
		}
		out, _ := tool.CloneResult(result, nil)
		out.Content[index] = tool.StructuredContent{Value: raw}
		return out, map[string]any{toolResultRuntimeNamespaceKey: runtimeMetadata}
	}
	hint := tool.Text(toolResultArtifactHintPrefix + ref + "\nModel-visible tool content was truncated.")
	if len(result.Content) == 1 {
		if text, ok := result.Content[0].(tool.Text); ok {
			result.Content = []tool.ContentBlock{text + "\n\n" + hint}
			return result, nil
		}
	}
	// A large resource can consume the whole budget before a later text block.
	result.Content = append([]tool.ContentBlock{hint}, result.Content...)
	return result, nil
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
