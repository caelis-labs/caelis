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

func toolResultEvent(call model.ToolCall, result tool.Result, message *model.Message, extraMeta ...map[string]any) *session.Event {
	return toolEvent(call, result, message, "", extraMeta...)
}

func toolProgressEvent(call model.ToolCall, result tool.Result, extraMeta ...map[string]any) *session.Event {
	return toolEvent(call, result, nil, "in_progress", extraMeta...)
}

func toolEvent(call model.ToolCall, result tool.Result, message *model.Message, statusOverride string, extraMeta ...map[string]any) *session.Event {
	rawInput := mustObject(call.Args)
	rawOutput := toolResultRawOutput(result)
	journal := toolExecutionJournalFromResult(result)
	trustedTaskResult := runtimeTaskResultSourceDeclared(extraMeta)
	resultMetadata := session.CloneState(result.Metadata)
	delete(resultMetadata, tool.MetadataExecutionJournal)
	resultMetadata = withoutUntrustedRuntimeTaskAuthority(resultMetadata, trustedTaskResult)
	metaParts := []map[string]any{resultMetadata}
	metaParts = append(metaParts, extraMeta...)
	metaParts = append(metaParts, toolMeta(call.Name), runtimeTaskResultSourceMeta(trustedTaskResult))
	status := strings.TrimSpace(statusOverride)
	if status == "" {
		status = toolCallStatus(call, result, rawOutput, trustedTaskResult)
	}
	meta := mergeEventMeta(metaParts...)
	event := &session.Event{
		Type: session.EventTypeToolResult,
		Tool: toolEventPayload(call, status, rawInput, rawOutput, nil),
		Meta: meta,
	}
	if journal != nil {
		event.Journal = journal
	}
	if message != nil {
		event.Message = message
		event.Text = message.TextContent()
	}
	return event
}

func runtimeTaskResultSourceDeclared(metaParts []map[string]any) bool {
	for _, meta := range metaParts {
		caelis, _ := meta["caelis"].(map[string]any)
		runtimeMeta, _ := caelis["runtime"].(map[string]any)
		binding, _ := runtimeMeta[toolbinding.MetadataSection].(map[string]any)
		if trusted, _ := binding[toolbinding.MetadataTaskResult].(bool); trusted {
			return true
		}
	}
	return false
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

func toolExecutionJournalFromResult(result tool.Result) *session.ExecutionJournalEntry {
	raw, ok := result.Metadata[tool.MetadataExecutionJournal]
	if !ok || raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var journal session.ExecutionJournalEntry
	if err := json.Unmarshal(data, &journal); err != nil {
		return nil
	}
	journal = session.CloneExecutionJournalEntry(journal)
	if journal.Schema != session.ExecutionJournalSchemaVersion || journal.Kind != session.JournalKindToolExecution || journal.ToolExecution == nil {
		return nil
	}
	return &journal
}

func toolResultRawOutput(result tool.Result) map[string]any {
	for _, part := range result.Content {
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
	texts := make([]string, 0, len(result.Content))
	for _, part := range result.Content {
		if part.Text != nil {
			texts = append(texts, part.Text.Text)
		}
	}
	if len(texts) > 0 {
		return map[string]any{"result": strings.Join(texts, "\n")}
	}
	if result.IsError {
		return map[string]any{"error": "tool call failed"}
	}
	return map[string]any{}
}

func toolCallStatus(call model.ToolCall, result tool.Result, rawOutput map[string]any, trustedTaskResult bool) string {
	if result.IsError {
		return "failed"
	}
	// A returned result completes the invocation. Only the RunCommand producer
	// owns the process state carried in its payload. Other tools, including
	// Spawn, Task, and SendMessage, do not transfer their invocation lifecycle
	// to the target they address or observe.
	if trustedTaskResult &&
		strings.EqualFold(strings.TrimSpace(call.Name), shell.RunCommandToolName) &&
		runtimeTaskKind(result.Metadata) == "command" {
		state, _ := rawOutput["state"].(string)
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

func runtimeTaskKind(meta map[string]any) string {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	taskMeta, _ := runtimeMeta["task"].(map[string]any)
	kind, _ := taskMeta["kind"].(string)
	return strings.ToLower(strings.TrimSpace(kind))
}

func responseMeta(resp *model.Response) map[string]any {
	if resp == nil {
		return nil
	}
	usage := map[string]any{
		"prompt_tokens":       resp.Usage.PromptTokens,
		"cached_input_tokens": resp.Usage.CachedInputTokens,
		"completion_tokens":   resp.Usage.CompletionTokens,
		"reasoning_tokens":    resp.Usage.ReasoningTokens,
		"total_tokens":        resp.Usage.TotalTokens,
		"cost_micros":         resp.Usage.CostMicros,
	}
	if provider := responseUsageAccountingProvider(resp); provider != "" {
		usage["provider"] = provider
	}
	sdk := map[string]any{
		"model":         strings.TrimSpace(resp.Model),
		"provider":      strings.TrimSpace(resp.Provider),
		"finish_reason": string(resp.FinishReason),
		"usage":         usage,
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
		usage.TotalTokens >= usage.PromptTokens+usage.CachedInputTokens+usage.CompletionTokens &&
		usage.PromptTokens+usage.CompletionTokens > 0 {
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

func toolEventPayload(call model.ToolCall, status string, rawInput map[string]any, rawOutput map[string]any, content []session.EventToolContent) *session.EventTool {
	payload := &session.EventTool{
		ID:      strings.TrimSpace(call.ID),
		Name:    strings.TrimSpace(call.Name),
		Status:  strings.TrimSpace(status),
		Input:   session.CloneState(rawInput),
		Output:  session.CloneState(rawOutput),
		Content: cloneEventToolContent(content),
	}
	return payload
}

func cloneEventToolContent(in []session.EventToolContent) []session.EventToolContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]session.EventToolContent, 0, len(in))
	for _, item := range in {
		var oldText *string
		if item.OldText != nil {
			value := *item.OldText
			oldText = &value
		}
		out = append(out, session.EventToolContent{
			Type:       strings.TrimSpace(item.Type),
			Text:       item.Text,
			TerminalID: strings.TrimSpace(item.TerminalID),
			Path:       strings.TrimSpace(item.Path),
			OldText:    oldText,
			NewText:    item.NewText,
		})
	}
	return out
}
