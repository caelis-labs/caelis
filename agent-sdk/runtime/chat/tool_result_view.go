package chat

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/model"
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

// ProjectToolResult builds the bounded view of a completed call and applies ModelView
// once. A hook failure returns a safe message paired with the same tool call.
// Nested calls leave artifacts nil and keep their full return value for the script.
func ProjectToolResult(ctx context.Context, def tool.Definition, call tool.Call, result tool.Result, cause error, artifacts ResultArtifactStore) (model.Message, map[string]any, error) {
	bounded, meta := canonicalToolResult(ctx, result, artifacts)
	message := toolResultMessageFromCanonical(model.ToolCall{ID: call.ID, Name: call.Name}, bounded, cause)
	view, err := hookrun.ModelView(ctx, def, call, message)
	return view, meta, err
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
	truncation := tool.TruncationMetadata(info)
	if len(truncation) == 0 && !reservedCollision {
		return canonical, nil
	}
	fields := map[string]any{}
	if len(truncation) > 0 {
		fields["truncation"] = truncation
	}
	if reservedCollision {
		fields["provenance"] = map[string]any{"reserved_namespace_collision": true}
	}
	return canonical, map[string]any{"caelis": map[string]any{"version": 1, "runtime": map[string]any{"tool": fields}}}
}

func toolResultMessageFromCanonical(call model.ToolCall, result tool.Result, err error) model.Message {
	parts := tool.ModelParts(result.Content)
	if len(parts) == 0 {
		parts = []model.Part{model.NewJSONPart(mustJSON(map[string]any{}))}
	}
	message := model.Message{
		Role: model.RoleTool,
		Parts: []model.Part{{
			Kind: model.PartKindToolResult,
			ToolResult: &model.ToolResultPart{
				ToolUseID: strings.TrimSpace(call.ID),
				Name:      strings.TrimSpace(call.Name),
				Content:   parts,
				IsError:   err != nil,
			},
		}},
	}
	return message
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
