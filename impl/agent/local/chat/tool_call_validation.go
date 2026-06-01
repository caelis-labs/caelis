package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

const maxInvalidToolCallRepairAttempts = 2

type invalidToolCallError struct {
	Tool   string
	Reason string
}

func (e invalidToolCallError) Error() string {
	toolName := strings.TrimSpace(e.Tool)
	if toolName == "" {
		toolName = "tool"
	}
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "invalid arguments"
	}
	return fmt.Sprintf("invalid model tool call for %s: %s", toolName, reason)
}

func canonicalizeAssistantToolCalls(message model.Message) (model.Message, []model.ToolCall, error) {
	cloned := model.CloneMessage(message)
	calls := cloned.ToolCalls()
	if len(calls) == 0 {
		return cloned, nil, nil
	}
	canonical := make([]model.ToolCall, 0, len(calls))
	for _, call := range calls {
		if strings.TrimSpace(call.ID) == "" {
			return model.Message{}, nil, invalidToolCallError{Tool: call.Name, Reason: "missing tool call id"}
		}
		if strings.TrimSpace(call.Name) == "" {
			return model.Message{}, nil, invalidToolCallError{Tool: call.Name, Reason: "missing tool name"}
		}
		raw, err := model.ParseToolCallArgsRaw(call.Args)
		if err != nil {
			return model.Message{}, nil, invalidToolCallError{Tool: call.Name, Reason: err.Error()}
		}
		call.Args = string(raw)
		canonical = append(canonical, call)
	}
	callIndex := 0
	for i := range cloned.Parts {
		if cloned.Parts[i].ToolUse == nil {
			continue
		}
		if callIndex >= len(canonical) {
			break
		}
		cloned.Parts[i].ToolUse.Input = json.RawMessage(canonical[callIndex].Args)
		callIndex++
	}
	return cloned, canonical, nil
}

func toolCallsHaveValidArgs(calls []model.ToolCall) bool {
	for _, call := range calls {
		if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Name) == "" {
			return false
		}
		if _, err := model.ParseToolCallArgsRaw(call.Args); err != nil {
			return false
		}
	}
	return true
}

func invalidToolCallWarningEvents(message model.Message, err error, includeNarrative bool) []*session.Event {
	out := make([]*session.Event, 0, 3)
	if includeNarrative {
		if event := invalidToolCallNarrativeEvent(message); event != nil {
			out = append(out, event)
		}
	}
	calls := invalidToolCalls(message)
	if len(calls) == 0 {
		out = append(out, session.MarkNotice(&session.Event{}, "warning", invalidToolCallWarningText(model.ToolCall{}, err)))
		return out
	}
	for i, call := range calls {
		call = warningToolCall(call, i)
		out = append(out, session.MarkUIOnly(&session.Event{
			Type: session.EventTypeToolCall,
			Tool: toolEventPayload(call, "pending", mustObject(call.Args), nil, nil),
			Meta: toolMeta(call.Name),
		}))
		result := tool.Result{
			ID:      call.ID,
			Name:    call.Name,
			IsError: true,
			Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
				"error":      invalidToolCallWarningText(call, err),
				"error_code": "invalid_input",
			}))},
		}
		out = append(out, session.MarkUIOnly(toolResultEvent(call, result, nil)))
	}
	return out
}

func invalidToolCallNarrativeEvent(message model.Message) *session.Event {
	cloned := model.CloneMessage(message)
	parts := make([]model.Part, 0, len(cloned.Parts))
	for _, part := range cloned.Parts {
		switch part.Kind {
		case model.PartKindReasoning, model.PartKindText:
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	narrative := model.NewMessage(cloned.Role, parts...)
	return session.MarkUIOnly(&session.Event{
		Type:    session.EventTypeAssistant,
		Message: &narrative,
		Text:    narrative.TextContent(),
	})
}

func invalidToolCalls(message model.Message) []model.ToolCall {
	calls := message.ToolCalls()
	out := make([]model.ToolCall, 0, len(calls))
	for _, call := range calls {
		if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Name) == "" {
			out = append(out, call)
			continue
		}
		if _, err := model.ParseToolCallArgsRaw(call.Args); err != nil {
			out = append(out, call)
		}
	}
	return out
}

func warningToolCall(call model.ToolCall, index int) model.ToolCall {
	if strings.TrimSpace(call.ID) == "" {
		call.ID = fmt.Sprintf("invalid-tool-call-%d", index+1)
	}
	if strings.TrimSpace(call.Name) == "" {
		call.Name = "TOOL"
	}
	return call
}

func invalidToolCallWarningText(call model.ToolCall, err error) string {
	toolName := strings.TrimSpace(call.Name)
	if toolName == "" {
		if typed, ok := err.(invalidToolCallError); ok {
			toolName = strings.TrimSpace(typed.Tool)
		}
	}
	if toolName == "" {
		toolName = "tool"
	}
	reason := invalidToolCallReason(err)
	return fmt.Sprintf("decode tool call input for %s: %s", toolName, reason)
}

func invalidToolCallReason(err error) string {
	if typed, ok := err.(invalidToolCallError); ok {
		if reason := strings.TrimSpace(typed.Reason); reason != "" {
			return reason
		}
	}
	reason := strings.TrimSpace(fmt.Sprint(err))
	if reason == "" {
		return "invalid arguments"
	}
	return reason
}
