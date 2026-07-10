package chat

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func toolResultEvent(call model.ToolCall, result tool.Result, message *model.Message, extraMeta ...map[string]any) *session.Event {
	rawInput := mustObject(call.Args)
	rawOutput := toolResultRawOutput(result)
	journal := toolExecutionJournalFromResult(result)
	resultMetadata := session.CloneState(result.Metadata)
	delete(resultMetadata, tool.MetadataExecutionJournal)
	metaParts := []map[string]any{resultMetadata}
	metaParts = append(metaParts, extraMeta...)
	metaParts = append(metaParts, toolMeta(call.Name))
	status := toolCallStatus(result, rawOutput)
	meta := mergeEventMeta(metaParts...)
	event := &session.Event{
		Type: session.EventTypeToolResult,
		Tool: toolEventPayload(call, status, rawInput, rawOutput, toolResultContent(call, rawInput, rawOutput, meta, status, result.IsError)),
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
	for _, part := range result.Content {
		if part.Text != nil {
			return map[string]any{"result": part.Text.Text}
		}
	}
	if result.IsError {
		return map[string]any{"error": "tool call failed"}
	}
	return map[string]any{}
}

func toolResultContent(call model.ToolCall, input map[string]any, output map[string]any, meta map[string]any, status string, isErr bool) []session.EventToolContent {
	name := strings.ToUpper(strings.TrimSpace(call.Name))
	displayOutput := toolResultDisplayOutput(name, output, meta)
	if name == "TASK" && suppressTaskControlContent(display.ToolTaskAction(input, displayOutput, meta)) {
		return nil
	}
	text := toolResultDisplayText(name, input, displayOutput, meta, status, isErr)
	if strings.TrimSpace(text) == "" && successfulEmptyTerminalResult(name, status, isErr) {
		return nil
	}
	if strings.TrimSpace(text) == "" {
		text = toolResultStatusText(status, isErr)
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	item := session.EventToolContent{
		Type: "content",
		Text: text,
	}
	switch name {
	case "RUN_COMMAND", "SPAWN", "TASK":
		item.Type = "terminal"
		item.TerminalID = toolResultTerminalID(call, displayOutput, meta)
	}
	return []session.EventToolContent{item}
}

func suppressTaskControlContent(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "wait", "cancel":
		return true
	default:
		return false
	}
}

func successfulEmptyTerminalResult(name string, status string, isErr bool) bool {
	if isErr || strings.EqualFold(strings.TrimSpace(status), "failed") {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(status), "completed") {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "RUN_COMMAND", "SPAWN":
		return true
	default:
		return false
	}
}

func toolCallTitle(call model.ToolCall) string {
	name := strings.TrimSpace(call.Name)
	if title := display.SummarizeToolCallTitle(name, mustObject(call.Args)); strings.TrimSpace(title) != "" {
		return title
	}
	return name
}

func toolCallStatus(result tool.Result, rawOutput map[string]any) string {
	if state, _ := rawOutput["state"].(string); strings.TrimSpace(state) != "" {
		switch strings.TrimSpace(state) {
		case "running", "waiting_input", "waiting_approval":
			return strings.TrimSpace(state)
		case "failed", "interrupted", "cancelled", "canceled", "terminated":
			return strings.TrimSpace(state)
		}
	}
	if exitCode, ok := intValue(rawOutput["exit_code"]); ok && exitCode != 0 {
		return "failed"
	}
	if result.IsError {
		return "failed"
	}
	return "completed"
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

func responseInvocation(resp *model.Response) *session.EventInvocation {
	if resp == nil {
		return nil
	}
	provider := strings.TrimSpace(resp.Provider)
	modelName := strings.TrimSpace(resp.Model)
	if provider == "" && modelName == "" && resp.ContextWindowTokens <= 0 {
		return nil
	}
	return &session.EventInvocation{
		Provider:            provider,
		Model:               modelName,
		ContextWindowTokens: resp.ContextWindowTokens,
	}
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

func toolEventPayload(call model.ToolCall, status string, rawInput map[string]any, rawOutput map[string]any, content []session.EventToolContent) *session.EventTool {
	payload := &session.EventTool{
		ID:      strings.TrimSpace(call.ID),
		Name:    strings.TrimSpace(call.Name),
		Kind:    display.ToolKindForName(call.Name),
		Title:   toolCallTitle(call),
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
