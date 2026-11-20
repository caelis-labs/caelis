package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type preparedToolCall struct {
	target    tool.Tool
	call      tool.Call
	err       error
	result    *tool.Result
	resultErr error
	execution tool.InvocationContext
	binding   *tool.Binding

	// Model envelopes belong to Chat and its Session facts.
	modelCall    *model.ToolCall
	proposedCall *model.ToolCall
}

func (a *Agent) prepareToolCalls(ctx context.Context, calls []model.ToolCall, visibility *toolVisibility) error {
	a.preparedCalls = make(map[string]preparedToolCall, len(calls))
	a.bindMCPCalls(ctx, calls, visibility)
	// Allocate the entire batch before the first extension sees an occurrence.
	for _, protocol := range calls {
		if _, duplicate := a.preparedCalls[protocol.ID]; duplicate {
			return fmt.Errorf("duplicate tool call ID in model batch: %q", protocol.ID)
		}
		target, actual, binding, err := a.boundTool(protocol, visibility)
		call := tool.Call{ID: protocol.ID, Name: actual.Name, Input: json.RawMessage(actual.Args)}
		prepared := preparedToolCall{target: target, call: call, execution: toolbinding.Allocate(target, call), binding: binding, err: err, proposedCall: &protocol}
		if _, routed := a.boundCalls[protocol.ID]; routed && actual.Name != "" {
			prepared.modelCall = &protocol
		}
		a.preparedCalls[protocol.ID] = prepared
	}
	for i, protocol := range calls {
		prepared := a.preparedCalls[protocol.ID]
		if prepared.err != nil {
			continue
		}
		target, call := prepared.target, prepared.call
		var synthetic *tool.Result
		var prepareErr error
		call, synthetic, prepareErr = toolbinding.PrepareArguments(toolbinding.WithOccurrence(ctx, prepared.execution, nil, prepared.binding), target, call)
		if prepareErr != nil && synthetic == nil {
			var hookErr *agent.HookError
			if errorcode.Is(prepareErr, tool.ErrorCodeInvalidInput) && !errors.As(prepareErr, &hookErr) {
				prepared.err = prepareErr
				a.preparedCalls[protocol.ID] = prepared
				continue
			}
			return prepareErr
		}
		effective := call.Input
		if prepared.modelCall != nil {
			envelope, marshalErr := json.Marshal(map[string]any{"tool_name": call.Name, "arguments": call.Input})
			if marshalErr != nil {
				return marshalErr
			}
			effective = envelope
			modelCall := protocol
			modelCall.Args = string(effective)
			prepared.modelCall = &modelCall
		}
		calls[i].Args = string(effective)
		prepared.call, prepared.result, prepared.resultErr = call, synthetic, prepareErr
		a.preparedCalls[protocol.ID] = prepared
	}
	return nil
}
func (a *Agent) applyPreparedFact(event *session.Event) {
	if event == nil || event.Tool == nil {
		return
	}
	p, ok := a.preparedCalls[event.Tool.ID]
	if !ok {
		return
	}
	if !session.IsUIOnly(event) {
		event.Tool.Execution = p.execution
		event.Tool.Binding = p.binding
		event.Tool.ProposedCall = p.proposedCall
	}

	if p.call.Name != "" {
		event.Tool.Name = p.call.Name
		event.Tool.ModelCall = p.modelCall
		event.Tool.Input = mustObject(string(p.call.Input))
		if p.modelCall != nil {
			event.Meta = mergeEventMeta(event.Meta, toolMeta(p.call.Name))
		}
	}
}

func effectiveAssistantMessage(message model.Message, calls []model.ToolCall) model.Message {
	message = model.CloneMessage(message)
	byID := map[string]model.ToolCall{}
	for _, c := range calls {
		byID[c.ID] = c
	}
	for i := range message.Parts {
		if use := message.Parts[i].ToolUse; use != nil {
			if c, ok := byID[use.ID]; ok {
				use.Input = json.RawMessage(c.Args)
			}
		}
	}
	return message
}
