package chat

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/prefixusage"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
)

func toolResultEvent(call model.ToolCall, result tool.Result, err error, message *model.Message, extraMeta ...map[string]any) *session.Event {
	return toolEvent(call, result, err, message, "", extraMeta...)
}

func toolProgressEvent(call model.ToolCall, result tool.Result, extraMeta ...map[string]any) *session.Event {
	return toolEvent(call, result, nil, nil, "in_progress", extraMeta...)
}

func toolEvent(call model.ToolCall, result tool.Result, err error, message *model.Message, statusOverride string, extraMeta ...map[string]any) *session.Event {
	event := &session.Event{
		Type: session.EventTypeToolResult,
		Tool: toolEventPayload(call, "", mustObject(call.Args)),
	}
	applyToolResultPresentation(event, call, result, err, message, statusOverride, extraMeta...)
	full, _ := tool.CloneResult(result, nil)
	event.Tool.Result = &full
	event.Tool.Outcome = processingOutcome(err)
	if message != nil {
		event.Message = message
		event.Text = message.TextContent()
	}
	return event
}

// ApplyRuntimeToolResultPresentation applies the chat tool display fields to a
// Runtime-authored tool event. Task authority comes from the bound target.
func ApplyRuntimeToolResultPresentation(event *session.Event, result tool.Result, target tool.Tool, err error, meta map[string]any) {
	call := model.ToolCall{ID: event.Tool.ID, Name: event.Tool.Name}
	applyToolResultPresentation(event, call, result, err, event.Message, "", meta, runtimeTaskResultSourceMeta(toolbinding.IsTaskResultSource(target)))
}

func applyToolResultPresentation(event *session.Event, call model.ToolCall, result tool.Result, err error, message *model.Message, statusOverride string, extraMeta ...map[string]any) {
	var parts []model.Part
	if message != nil && len(message.ToolResults()) > 0 {
		parts = message.ToolResults()[0].Content
	} else {
		view, _ := canonicalToolResult(result, nil)
		parts = tool.ModelParts(view.Content)
	}
	rawOutput := toolResultRawOutput(parts, err != nil)
	trustedTaskResult := runtimeTaskResultSourceDeclared(extraMeta)
	resultMetadata := session.CloneState(mergeEventMeta(extraMeta...))
	resultMetadata = withoutUntrustedRuntimeTaskAuthority(resultMetadata, trustedTaskResult)
	metaParts := []map[string]any{resultMetadata}
	metaParts = append(metaParts, toolMeta(call.Name), runtimeTaskResultSourceMeta(trustedTaskResult))
	status := strings.TrimSpace(statusOverride)
	if status == "" {
		status = toolCallStatus(call, resultMetadata, err, rawOutput, trustedTaskResult)
	}
	event.Tool.Status = status
	event.Tool.Output = session.CloneState(rawOutput)
	event.Meta = mergeEventMeta(metaParts...)
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
	binding, _ := runtimeMeta[toolbinding.MetadataSection].(map[string]any)
	trusted, _ := binding[toolbinding.MetadataTaskResult].(bool)
	return trusted
}

func withoutUntrustedRuntimeTaskAuthority(meta map[string]any, trusted bool) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	delete(runtimeMeta, toolbinding.MetadataSection)
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

func responseMeta(resp *model.Response) map[string]any {
	if resp == nil {
		return nil
	}
	usage := session.ModelUsageMetadata(responseUsageAccountingProvider(resp), resp.Usage)
	sdk := map[string]any{
		"model":         strings.TrimSpace(resp.Model),
		"provider":      strings.TrimSpace(resp.Provider),
		"finish_reason": string(resp.FinishReason),
		"usage":         usage,
	}
	if !resp.Usage.IsReported() {
		delete(sdk, "usage")
	}
	if resp.InvocationID != "" {
		sdk["usage_receipt_id"] = resp.InvocationID
	}
	if resp.ContextWindowTokens > 0 {
		sdk["context_window_tokens"] = resp.ContextWindowTokens
	}
	return map[string]any{
		"caelis": map[string]any{
			"version": 1,
			"sdk":     sdk,
		},
	}
}

func responseUsageAccountingProvider(resp *model.Response) string {
	if resp == nil {
		return ""
	}
	provider := strings.TrimSpace(resp.Provider)
	if !strings.EqualFold(provider, "deepseek") {
		return provider
	}
	usage := resp.Usage
	if usage.CachedInputTokens > 0 &&
		usage.TotalTokens >= usage.PromptTokens+usage.CachedInputTokens+usage.CompletionTokens {
		return "deepseek-anthropic"
	}
	return provider
}

func responseInvocation(resp *model.Response, requestPrefix ...prefixusage.Snapshot) *session.EventInvocation {
	if resp == nil {
		return nil
	}
	provider := strings.TrimSpace(resp.Provider)
	modelName := strings.TrimSpace(resp.Model)
	prefix := prefixusage.Snapshot{}
	if len(requestPrefix) > 0 {
		prefix = requestPrefix[0]
	}
	if provider == "" && modelName == "" && resp.ContextWindowTokens <= 0 && prefix.Fingerprint == "" {
		return nil
	}
	return &session.EventInvocation{
		Provider:                provider,
		Model:                   modelName,
		ContextWindowTokens:     resp.ContextWindowTokens,
		PromptPrefixFingerprint: strings.TrimSpace(prefix.Fingerprint),
		PromptPrefixTokens:      max(prefix.Tokens, 0),
	}
}

func responseInvocationForModel(
	resp *model.Response,
	invocationModel model.LLM,
	requestPrefix ...prefixusage.Snapshot,
) *session.EventInvocation {
	invocation := responseInvocation(resp, requestPrefix...)
	if invocation == nil || invocationModel == nil {
		return invocation
	}
	if modelName := strings.TrimSpace(invocationModel.Name()); modelName != "" {
		invocation.Model = modelName
	}
	if provider, ok := invocationModel.(interface{ ProviderName() string }); ok {
		if providerName := strings.TrimSpace(provider.ProviderName()); providerName != "" {
			invocation.Provider = providerName
		}
	}
	return invocation
}

func toolMeta(name string) map[string]any {
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

func toolTruncationEventMeta(info tool.TruncationInfo) map[string]any {
	truncation := tool.TruncationMetadata(info)
	if len(truncation) == 0 {
		return nil
	}
	return map[string]any{
		"caelis": map[string]any{
			"version": 1,
			"runtime": map[string]any{
				"tool": map[string]any{
					"truncation": truncation,
				},
			},
		},
	}
}

func toolReservedNamespaceCollisionEventMeta(collision bool) map[string]any {
	if !collision {
		return nil
	}
	return map[string]any{
		"caelis": map[string]any{
			"version": 1,
			"runtime": map[string]any{
				"tool": map[string]any{
					"provenance": map[string]any{
						"reserved_namespace_collision": true,
					},
				},
			},
		},
	}
}

func toolEventPayload(call model.ToolCall, status string, rawInput map[string]any) *session.EventTool {
	return &session.EventTool{
		ID:     strings.TrimSpace(call.ID),
		Name:   strings.TrimSpace(call.Name),
		Status: strings.TrimSpace(status),
		Input:  session.CloneState(rawInput),
	}
}
