package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// appendToolFact commits actual Host calls and their complete outcome once.
func (r *Runtime) appendToolFact(ctx context.Context, ref session.SessionRef, turnID string, call tool.Call, result *tool.Result, callErr error) error {
	var input map[string]any
	decoder := json.NewDecoder(bytes.NewReader(call.Input))
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil {
		return err
	}
	kind := session.EventTypeToolCall
	status := "pending"
	if result != nil {
		kind = session.EventTypeToolResult
		status = "completed"
		if result.IsError || callErr != nil {
			status = "failed"
		}
	}
	event := &session.Event{Type: kind, Visibility: session.VisibilityMirror,
		ID:    fmt.Sprintf("tool:%s:%s:%s", call.Execution.TurnID, call.Execution.ItemID, kind),
		Scope: &session.EventScope{TurnID: turnID, Source: "runtime_tool"}, Actor: session.ActorRef{Kind: session.ActorKindTool, ID: call.ID, Name: call.Name},
		Tool: &session.EventTool{ID: call.ID, Name: call.Name, Status: status, Input: input, Execution: call.Execution, Binding: call.TargetBinding, ModelCall: call.ModelCall}}
	if call.Parent != nil {
		event.Tool.Parent = &session.EventParentTool{Kind: call.Parent.Kind, CallID: call.Parent.ID, Name: call.Parent.Name, Execution: call.Parent.Execution}
	}
	event.IdempotencyKey = event.ID
	if result != nil {
		full, err := tool.CloneResult(*result, nil)
		if err != nil {
			return err
		}
		event.Tool.Result = &full
		event.Tool.Outcome = session.ToolOutcome(codeModeOutcome(full, callErr))
		event.Tool.ErrorCode = string(errorcode.CodeOf(callErr))
		event.Tool.Error = errorText(callErr)
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), approvalResolutionRecoveryTimeout)
	defer cancel()
	var release func()
	bindingIntent := result == nil && isCodeModeChild(call) && call.TargetBinding != nil
	if result != nil && call.Name == tool.ToolSearchToolName || bindingIntent {
		var err error
		release, err = r.acquireSessionWrite(cleanup, ref)
		if err != nil {
			return err
		}
		defer release()
		if bindingIntent {
			events, err := r.sessions.Events(cleanup, session.EventsRequest{SessionRef: ref, IncludeTransient: true})
			if err != nil {
				return err
			}
			if err := validateRecordedCodeModeBinding(events, call); err != nil {
				return err
			}
		}
	}
	persisted, err := r.sessions.AppendEvent(cleanup, session.AppendEventRequest{SessionRef: ref, MutationGuard: session.RuntimeMutationGuard(ctx), Event: event})
	if err != nil {
		var readErr error
		persisted, readErr = r.reconcileCompactionAppend(cleanup, ref, event)
		if persisted == nil {
			return errors.Join(err, readErr)
		}
	}
	if sink, ok := ctx.Value(nestedToolEventSinkKey{}).(func(*session.Event)); ok {
		sink(persisted)
	}
	return nil
}
