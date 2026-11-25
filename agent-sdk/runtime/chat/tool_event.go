package chat

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/prefixusage"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
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
	event.Message = message
	toolbinding.ApplyResultPresentation(event, result, err, statusOverride, extraMeta...)
	full, _ := tool.CloneResult(result, nil)
	event.Tool.Result = &full
	event.Tool.Outcome = processingOutcome(err)
	if message != nil {
		event.Text = message.TextContent()
	}
	return event
}

func responseMeta(resp *model.Response) map[string]any {
	if resp == nil {
		return nil
	}
	usage := session.ModelUsageMetadata(strings.TrimSpace(resp.Provider), resp.Usage)
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

func toolEventPayload(call model.ToolCall, status string, rawInput map[string]any) *session.EventTool {
	return &session.EventTool{
		ID:     strings.TrimSpace(call.ID),
		Name:   strings.TrimSpace(call.Name),
		Status: strings.TrimSpace(status),
		Input:  session.CloneState(rawInput),
	}
}
