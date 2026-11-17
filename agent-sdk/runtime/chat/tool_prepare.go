package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type preparedToolCall struct {
	target tool.Tool
	call   tool.Call
	err    error
	result *tool.Result
}

func (a *Agent) prepareToolCalls(ctx context.Context, calls []model.ToolCall, visibility *tool.ToolVisibility) error {
	a.preparedCalls = make(map[string]preparedToolCall, len(calls))
	a.bindMCPCalls(ctx, calls, visibility)
	// Allocate the entire batch before the first extension sees an occurrence.
	for _, protocol := range calls {
		if _, duplicate := a.preparedCalls[protocol.ID]; duplicate {
			return fmt.Errorf("duplicate tool call ID in model batch: %q", protocol.ID)
		}
		target, actual, binding, err := a.boundTool(protocol, visibility)
		call := tool.Call{ID: protocol.ID, Name: actual.Name, Input: json.RawMessage(actual.Args), TargetBinding: binding}
		if binding != nil {
			original := protocol
			call.ModelCall = &original
		}
		proposal := protocol
		call.ProposedCall = &proposal
		call = toolbinding.Prepare(target, call)
		a.preparedCalls[protocol.ID] = preparedToolCall{target: target, call: call, err: err}
	}
	for i, protocol := range calls {
		prepared := a.preparedCalls[protocol.ID]
		if prepared.err != nil {
			continue
		}
		target, call := prepared.target, prepared.call
		var synthetic *tool.Result
		var prepareErr error
		call, synthetic, prepareErr = toolbinding.PrepareArguments(ctx, target, call)
		if prepareErr != nil {
			return prepareErr
		}
		effective := call.Input
		if call.ModelCall != nil {
			envelope, marshalErr := json.Marshal(map[string]any{"tool_name": call.Name, "arguments": call.Input})
			if marshalErr != nil {
				return marshalErr
			}
			effective = envelope
			fixed := *call.ModelCall
			fixed.Args = string(effective)
			call.ModelCall = &fixed
		}
		calls[i].Args = string(effective)
		a.preparedCalls[protocol.ID] = preparedToolCall{target: target, call: call, result: synthetic}
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
	event.Tool.Execution = p.call.Execution
	event.Tool.Binding = p.call.TargetBinding
	event.Tool.ProposedCall = p.call.ProposedCall

	if p.err == nil {
		event.Tool.Name = p.call.Name
		event.Tool.ModelCall = p.call.ModelCall
		decoder := json.NewDecoder(bytes.NewReader(p.call.Input))
		decoder.UseNumber()
		_ = decoder.Decode(&event.Tool.Input)
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
