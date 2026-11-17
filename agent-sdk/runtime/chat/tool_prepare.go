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
}

func (a *Agent) prepareToolCalls(ctx context.Context, calls []model.ToolCall, visibility *tool.ToolVisibility) error {
	a.preparedCalls = make(map[string]preparedToolCall, len(calls))
	a.bindMCPCalls(ctx, calls, visibility)
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
		call = toolbinding.Prepare(target, call)
		a.preparedCalls[protocol.ID] = preparedToolCall{target: target, call: call, err: err}
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

	if p.err == nil {
		event.Tool.Name = p.call.Name
		event.Tool.ModelCall = p.call.ModelCall
		decoder := json.NewDecoder(bytes.NewReader(p.call.Input))
		decoder.UseNumber()
		_ = decoder.Decode(&event.Tool.Input)
	}
}
