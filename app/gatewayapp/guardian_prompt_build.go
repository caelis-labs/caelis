package gatewayapp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
)

const (
	guardianMaxActionDepth           = 16
	guardianMaxActionNodes           = 2048
	guardianMaxActionCollectionItems = 512
	guardianMaxActionBytes           = 128 * 1024
)

type guardianPromptItems struct {
	Text                   string
	UserEvidence           []string
	ParentCursor           guardianParentCanonicalCursor
	MandatoryInputTooLarge bool
	ContextTrimmed         bool
}

func guardianCompactionConfig(llm model.LLM, output *model.OutputSpec) sdkruntime.CompactionConfig {
	contextWindow := 0
	if provider, ok := llm.(interface{ ContextWindowTokens() int }); ok {
		contextWindow = provider.ContextWindowTokens()
	}
	cfg := defaultCompactionConfig(contextWindow)
	prefix := sdkruntime.EvaluateModelRequestBudget(llm, &model.Request{
		Instructions: []model.Part{model.NewTextPart(guardianPolicyPrompt())},
		Output:       model.CloneOutputSpec(output),
	}, cfg)
	cfg.EstimatedPromptPrefixTokens = prefix.Usage.TotalTokens
	return cfg
}

func guardianVisibleText(event *session.Event) string {
	if event == nil {
		return ""
	}
	if event.Message != nil {
		if text := event.Message.TextContent(); strings.TrimSpace(text) != "" {
			return text
		}
	}
	return session.EventText(event)
}

func guardianModelRequest(history []*session.Event, input string, output *model.OutputSpec) *model.Request {
	messages := make([]model.Message, 0, len(history)+1)
	for _, event := range compact.PromptEventsFromLatestCompact(history) {
		if event == nil {
			continue
		}
		if session.EventTypeOf(event) == session.EventTypeCompact {
			if text := strings.TrimSpace(session.EventText(event)); text != "" {
				messages = append(messages, model.NewTextMessage(model.RoleUser, text))
			}
			continue
		}
		if message, ok := session.ModelMessageOf(event); ok {
			messages = append(messages, message)
			continue
		}
		text := strings.TrimSpace(session.EventText(event))
		switch session.EventTypeOf(event) {
		case session.EventTypeUser:
			if text != "" {
				messages = append(messages, model.NewTextMessage(model.RoleUser, text))
			}
		case session.EventTypeAssistant:
			if text != "" {
				messages = append(messages, model.NewTextMessage(model.RoleAssistant, text))
			}
		}
	}
	if input = strings.TrimSpace(input); input != "" {
		messages = append(messages, model.NewTextMessage(model.RoleUser, input))
	}
	var tools []model.ToolSpec
	for _, t := range (&guardianQueries{}).tools() {
		definition := t.Definition()
		tools = append(tools, model.NewFunctionToolSpec(definition.Name, definition.Description, definition.InputSchema))
	}
	return &model.Request{
		Instructions: []model.Part{model.NewTextPart(guardianPolicyPrompt() + "\n\n" + guardianEnvironmentContext(sandbox.NetworkEnabled))},
		Messages:     messages,
		Tools:        tools,
		Output:       model.CloneOutputSpec(output),
	}
}

func guardianPlannedActionJSON(req kernel.ApprovalReviewRequest) (string, bool, error) {
	action := map[string]any{"origin": req.RuntimeRequest.Origin, "tool_call_id": req.RuntimeRequest.Call.ID}
	if snapshot, ok := req.RuntimeRequest.Metadata[policy.MetadataSandboxPolicy].(sandbox.PolicySnapshot); ok && !sandbox.PolicySnapshotEmpty(snapshot) {
		action["runtime_sandbox"] = sandbox.SummarizePolicy(snapshot)
	}
	toolName := ""
	if req.Approval != nil {
		toolName = strings.TrimSpace(req.Approval.ToolName)
	}
	toolName = firstNonEmpty(toolName, strings.TrimSpace(req.RuntimeRequest.Tool.Name), strings.TrimSpace(req.RuntimeRequest.Call.Name))
	action["tool"] = firstNonEmpty(toolName, "unknown")
	if req.Approval != nil {
		if req.Approval.ToolTitle != "" {
			action["title"] = req.Approval.ToolTitle
		}
		if req.Approval.ToolKind != "" {
			action["kind"] = req.Approval.ToolKind
		}
		if len(req.Approval.Content) > 0 {
			action["content"] = req.Approval.Content
		}
		if len(req.Approval.RawOutput) > 0 {
			action["request_diagnostics"] = req.Approval.RawOutput
		}
		if req.Approval.Reason != "" {
			action["reason"] = req.Approval.Reason
		}
		if req.Approval.Justification != "" {
			action["justification"] = req.Approval.Justification
		}
		if req.Approval.SandboxPermissions != "" {
			action["sandbox_permissions"] = req.Approval.SandboxPermissions
		}
		if len(req.Approval.RawInput) > 0 {
			action["arguments"] = req.Approval.RawInput
		}
	}
	if _, hasArguments := action["arguments"]; !hasArguments && len(req.RuntimeRequest.Call.Input) > 0 {
		if raw := rawJSONMap(req.RuntimeRequest.Call.Input); len(raw) > 0 {
			action["arguments"] = raw
		}
	}
	if !guardianActionWithinStructuralLimits(action) {
		return guardianOversizedActionJSON(), true, nil
	}
	raw, err := json.MarshalIndent(action, "", "  ")
	if err != nil {
		return "", false, err
	}
	return string(raw), false, nil
}

func guardianOversizedActionJSON() string {
	return `{"error":"planned action exceeded Guardian structural limits"}`
}

func guardianActionWithinStructuralLimits(value any) bool {
	nodes, bytes := 0, 0
	var visit func(any, int) bool
	visit = func(current any, depth int) bool {
		if depth > guardianMaxActionDepth {
			return false
		}
		nodes++
		if nodes > guardianMaxActionNodes {
			return false
		}
		switch typed := current.(type) {
		case string:
			bytes += len(typed)
		case json.RawMessage:
			bytes += len(typed)
		case []byte:
			bytes += len(typed)
		case map[string]any:
			if len(typed) > guardianMaxActionCollectionItems {
				return false
			}
			for key, item := range typed {
				bytes += len(key)
				if !visit(item, depth+1) {
					return false
				}
			}
		case []any:
			if len(typed) > guardianMaxActionCollectionItems {
				return false
			}
			for _, item := range typed {
				if !visit(item, depth+1) {
					return false
				}
			}
		}
		return bytes <= guardianMaxActionBytes
	}
	return visit(value, 1)
}

func rawJSONMap(raw []byte) map[string]any {
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func mustPrettyJSON(value any) string {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(raw)
}
