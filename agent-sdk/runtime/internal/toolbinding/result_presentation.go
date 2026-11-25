package toolbinding

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
)

// ApplyResultPresentation projects the bounded result into the Session tool event.
func ApplyResultPresentation(event *session.Event, result tool.Result, err error, statusOverride string, extraMeta ...map[string]any) {
	call := model.ToolCall{Name: event.Tool.Name}
	message := event.Message
	var parts []model.Part
	if message != nil && len(message.ToolResults()) > 0 {
		parts = message.ToolResults()[0].Content
	} else if statusOverride == "in_progress" {
		// Progress carries the preview that was already bounded for live publication.
		parts = tool.ModelParts(result.Content)
	} else {
		view, _ := (tool.ResultProjection{}).Preview(context.Background(), result)
		parts = tool.ModelParts(view.Content)
	}
	rawOutput := toolResultRawOutput(parts, err != nil)
	trustedTaskResult := runtimeTaskResultSourceDeclared(extraMeta)
	resultMetadata := session.CloneState(MergeEventMeta(extraMeta...))
	resultMetadata = withoutUntrustedRuntimeTaskAuthority(resultMetadata, trustedTaskResult)
	metaParts := []map[string]any{resultMetadata}
	metaParts = append(metaParts, ToolMeta(call.Name), TaskResultSourceMeta(trustedTaskResult))
	status := strings.TrimSpace(statusOverride)
	if status == "" {
		status = toolCallStatus(call, resultMetadata, err, rawOutput, trustedTaskResult)
	}
	event.Tool.Status = status
	event.Tool.Output = session.CloneState(rawOutput)
	event.Meta = MergeEventMeta(metaParts...)
}

func runtimeTaskResultSourceDeclared(metaParts []map[string]any) bool {
	for _, meta := range metaParts {
		if isRuntimeTaskResult(meta) {
			return true
		}
	}
	return false
}

func isRuntimeTaskResult(meta map[string]any) bool {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	binding, _ := runtimeMeta[MetadataSection].(map[string]any)
	trusted, _ := binding[MetadataTaskResult].(bool)
	return trusted
}

func withoutUntrustedRuntimeTaskAuthority(meta map[string]any, trusted bool) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	delete(runtimeMeta, MetadataSection)
	if !trusted {
		delete(runtimeMeta, "task")
	}
	return meta
}

func toolResultRawOutput(parts []model.Part, isError bool) map[string]any {
	for _, part := range parts {
		if part.JSON == nil || len(part.JSON.Value) == 0 {
			continue
		}
		var decoded any
		if err := json.Unmarshal(part.JSON.Value, &decoded); err != nil {
			return map[string]any{"result": string(part.JSON.Value)}
		}
		if payload, ok := decoded.(map[string]any); ok {
			return session.CloneState(payload)
		}
		return map[string]any{"result": decoded}
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Text != nil {
			texts = append(texts, part.Text.Text)
		}
	}
	if len(texts) > 0 {
		return map[string]any{"result": strings.Join(texts, "\n")}
	}
	if isError {
		return map[string]any{"error": "tool call failed"}
	}
	return map[string]any{}
}

func toolCallStatus(call model.ToolCall, meta map[string]any, err error, rawOutput map[string]any, trustedTaskResult bool) string {
	if err != nil {
		return "failed"
	}
	// A returned result completes the invocation. Only the RunCommand producer
	// owns the process state carried in its payload. Other tools, including
	// Spawn, Task, and SendMessage, do not transfer their invocation lifecycle
	// to the target they address or observe.
	if trustedTaskResult &&
		((strings.EqualFold(strings.TrimSpace(call.Name), shell.RunCommandToolName) && runtimeTaskKind(meta) == "command") || (call.Name == "js_exec" && runtimeTaskKind(meta) == "code_mode")) {
		state, _ := rawOutput["state"].(string)
		if call.Name == "js_exec" {
			// Returned business data may itself contain a running inner Task.
			// Only the original outer Task producer owns this invocation state.
			state, _ = runtimeTaskMetadata(meta)["state"].(string)
		}
		if strings.TrimSpace(state) == "" {
			if exitCode, ok := intValue(rawOutput["exit_code"]); ok && exitCode != 0 {
				return "failed"
			}
			return "completed"
		}
		switch strings.TrimSpace(state) {
		case "running", "waiting_input", "waiting_approval":
			return strings.TrimSpace(state)
		case "failed", "interrupted", "cancelled", "canceled", "terminated":
			return strings.TrimSpace(state)
		}
	}
	return "completed"
}

func runtimeTaskMetadata(meta map[string]any) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	taskMeta, _ := runtimeMeta["task"].(map[string]any)
	return taskMeta
}

func runtimeTaskKind(meta map[string]any) string {
	kind, _ := runtimeTaskMetadata(meta)["kind"].(string)
	return strings.ToLower(strings.TrimSpace(kind))
}

// ToolMeta records the tool name in Runtime event metadata.
func ToolMeta(name string) map[string]any {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	return map[string]any{
		"caelis": map[string]any{
			"version": 1,
			"runtime": map[string]any{
				"tool": map[string]any{
					"name": name,
				},
			},
		},
	}
}

// TaskResultSourceMeta records whether the bound tool owns Task state.
func TaskResultSourceMeta(trusted bool) map[string]any {
	return map[string]any{
		"caelis": map[string]any{"runtime": map[string]any{
			MetadataSection: map[string]any{MetadataTaskResult: trusted},
		}},
	}
}

func intValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

// MergeEventMeta overlays nested event metadata in argument order.
func MergeEventMeta(parts ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, part := range parts {
		for key, value := range part {
			if existing, ok := out[key].(map[string]any); ok {
				if incoming, ok := value.(map[string]any); ok {
					out[key] = mergeAnyMap(existing, incoming)
					continue
				}
			}
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mergeAnyMap(base map[string]any, overlay map[string]any) map[string]any {
	out := session.CloneState(base)
	for key, value := range overlay {
		if existing, ok := out[key].(map[string]any); ok {
			if incoming, ok := value.(map[string]any); ok {
				out[key] = mergeAnyMap(existing, incoming)
				continue
			}
		}
		out[key] = value
	}
	return out
}
