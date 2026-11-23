package tool

import (
	"context"
	"encoding/json"
	"strings"
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
	Save(context.Context, Result) (string, error)
}

// ResultProjection configures the model-visible budget and optional complete
// result store. Its zero value uses the default budget and performs no I/O.
type ResultProjection struct {
	Budget    TruncationPolicy
	Artifacts ResultArtifactStore
}

// Preview projects progress and nested tool results without retaining an artifact.
func (p ResultProjection) Preview(ctx context.Context, result Result) (Result, map[string]any) {
	p.Artifacts = nil
	return p.Project(ctx, result)
}

// Project builds the bounded model view and its durable provenance.
func (p ResultProjection) Project(ctx context.Context, result Result) (Result, map[string]any) {
	policy := p.Budget
	original := result
	result, reservedCollision := toolResultWithoutReservedNamespace(result)
	var protectedJSONFields map[string]any
	if p.Artifacts != nil && ResultNeedsTruncation(result, policy) {
		if ref, err := p.Artifacts.Save(ctx, original); err == nil && strings.TrimSpace(ref) != "" {
			result, protectedJSONFields = toolResultWithArtifactHint(result, ref, reservedCollision)
		}
	}
	canonical, info := TruncateResultWithInfoAndProtectedJSONFields(result, policy, protectedJSONFields)
	truncation := TruncationMetadata(info)
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

// toolResultWithArtifactHint prefers a structured payload for the trusted
// reference, otherwise a text notice. It preserves original block order and
// never reuses or extends a tool-owned system_hint field.
func toolResultWithArtifactHint(result Result, ref string, collision bool) (Result, map[string]any) {
	for index, content := range result.Content {
		block, ok := content.(StructuredContent)
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
		payload[toolResultRuntimeNamespaceKey], _ = json.Marshal(runtimeMetadata)
		raw, err := json.Marshal(payload)
		if err != nil {
			break
		}
		out, _ := CloneResult(result, nil)
		out.Content[index] = StructuredContent{Value: raw}
		return out, map[string]any{toolResultRuntimeNamespaceKey: runtimeMetadata}
	}
	hint := Text(toolResultArtifactHintPrefix + ref + "\nModel-visible tool content was truncated.")
	if len(result.Content) == 1 {
		if text, ok := result.Content[0].(Text); ok {
			result.Content = []ContentBlock{text + "\n\n" + hint}
			return result, nil
		}
	}
	// A large resource can consume the whole budget before a later text block.
	result.Content = append([]ContentBlock{hint}, result.Content...)
	return result, nil
}

func toolResultWithoutReservedNamespace(result Result) (Result, bool) {
	for index, block := range result.Content {
		structured, ok := block.(StructuredContent)
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
		out, _ := CloneResult(result, nil)
		out.Content[index] = StructuredContent{Value: raw}
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
