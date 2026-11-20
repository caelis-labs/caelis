package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

type toolFactTarget struct {
	definition tool.Definition
	source     tool.Tool
}

// appendToolFact commits actual Host calls and their complete outcome once.
func (r *Runtime) appendToolFact(ctx context.Context, ref session.SessionRef, turnID string, call tool.Call, result *tool.Result, callErr error, targets ...toolFactTarget) error {
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
		if callErr != nil {
			status = "failed"
		}
	}
	execution := tool.InvocationFromContext(ctx)
	event := &session.Event{Type: kind, Visibility: session.VisibilityMirror,
		ID:    fmt.Sprintf("tool:%s:%s:%s", execution.TurnID, execution.ItemID, kind),
		Scope: &session.EventScope{TurnID: turnID, Source: "runtime_tool"}, Actor: session.ActorRef{Kind: session.ActorKindTool, ID: call.ID, Name: call.Name},
		Tool: runtimeToolFact(ctx, call, status, input)}
	event.IdempotencyKey = event.ID
	var processingErr error
	if result != nil {
		full, err := tool.CloneResult(*result, nil)
		if err != nil {
			return err
		}
		event.Tool.Result = &full
		effect, effectErr := hookrun.EffectResult(full, callErr)
		event.Tool.Outcome = session.ToolOutcome(codeModeOutcome(effect, effectErr))
		if event.Tool.Outcome == session.ToolOutcomeSucceeded {
			event.Tool.Status = "completed"
		}
		message := chat.CanonicalToolResult(model.ToolCall{ID: call.ID, Name: call.Name}, full, callErr)
		original := model.CloneMessage(message)
		def := tool.Definition{Name: call.Name}
		var source tool.Tool
		if len(targets) > 0 {
			def = tool.CloneDefinition(targets[0].definition)
			source = targets[0].source
		}
		message, processingErr = hookrun.ModelView(ctx, def, call, message)
		if processingErr != nil {
			message = hookrun.FailedView(original)
			event.Tool.Error = processingErr.Error()
		}
		event.Message = &message
		if ownsNestedCommandPresentation(ctx, call, source) {
			chat.ApplyRuntimeToolResultPresentation(event, full, source, callErr)
		}
		event.Tool.ErrorCode = string(errorcode.CodeOf(callErr))
		event.Tool.ProcessingFailure = hookrun.ProcessingFailure(errors.Join(callErr, processingErr))
		event.Tool.Error = errorText(callErr)
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), approvalResolutionRecoveryTimeout)
	defer cancel()
	var release func()
	bindingIntent := result == nil && isCodeModeChild(ctx) && toolbinding.Target(ctx) != nil
	if result != nil && call.Name == mcp.SearchToolName || bindingIntent {
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
			if err := validateRecordedCodeModeBinding(ctx, events, call); err != nil {
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
	return processingErr
}

func runtimeToolFact(ctx context.Context, call tool.Call, status string, input map[string]any) *session.EventTool {
	execution := tool.InvocationFromContext(ctx)
	fact := &session.EventTool{ID: call.ID, Name: call.Name, Status: status, Input: input, Execution: execution, Binding: toolbinding.Target(ctx)}
	if parent := tool.ParentFromContext(ctx); parent != nil {
		parentExecution := execution
		parentExecution.ItemID = parent.ItemID
		fact.Parent = &session.EventParentTool{Kind: parent.Kind, CallID: parent.ID, Name: parent.Name, Execution: parentExecution}
	}
	return fact
}

func ownsNestedCommandPresentation(ctx context.Context, call tool.Call, target tool.Tool) bool {
	return isCodeModeChild(ctx) && call.Name == shell.RunCommandToolName && toolbinding.IsTaskResultSource(target)
}

type nestedCommandObserver struct {
	publish func(*session.Event)
	fact    *session.EventTool
	target  tool.Tool
}

func (o nestedCommandObserver) ObserveToolResult(result tool.Result) {
	fact := *o.fact
	event := &session.Event{
		Type: session.EventTypeToolResult, Visibility: session.VisibilityUIOnly,
		Scope: &session.EventScope{TurnID: fact.Execution.TurnID, Source: "runtime_tool"},
		Actor: session.ActorRef{Kind: session.ActorKindTool, ID: fact.ID, Name: fact.Name},
		Tool:  &fact,
	}
	chat.ApplyRuntimeToolResultPresentation(event, result, o.target, nil)
	o.publish(event)
}
