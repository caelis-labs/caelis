package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
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
func (r *Runtime) appendToolFact(ctx context.Context, ref session.SessionRef, turnID string, call tool.Call, result *tool.Result, callErr error, targets ...toolFactTarget) (*session.Event, error) {
	var input map[string]any
	decoder := json.NewDecoder(bytes.NewReader(call.Input))
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil {
		return nil, err
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
			return nil, err
		}
		event.Tool.Result = &full
		effectErr := hookrun.EffectError(callErr)
		event.Tool.Outcome = session.ToolOutcome(codeModeOutcome(effectErr))
		observation := toolbinding.Observed(ctx)
		if event.Tool.Outcome == session.ToolOutcomeSucceeded {
			event.Tool.Status = "completed"
		}
		def := tool.Definition{Name: call.Name}
		var source tool.Tool
		if len(targets) > 0 {
			def = tool.CloneDefinition(targets[0].definition)
			source = targets[0].source
		}
		event.Tool.Discovery = observation.Discovery && effectErr == nil
		projection := hookrun.FromContext(ctx).Hooks.ToolProjection
		message, _, viewErr := toolbinding.ProjectResult(ctx, def, call, full, callErr, projection.Preview)
		processingErr = viewErr
		if viewErr != nil {
			observation.BuiltinDisplay = nil
		}
		event.Message = &message
		if ownsNestedCommandPresentation(ctx, call, source) {
			toolbinding.ApplyResultPresentation(event, full, callErr, "", observation.EventMeta(), toolbinding.TaskResultSourceMeta(true))
		} else if callErr == nil && processingErr == nil && isCodeModeChild(ctx) && len(observation.BuiltinDisplay) > 0 {
			toolbinding.ApplyResultPresentation(event, full, nil, "", observation.EventMeta(), toolbinding.TaskResultSourceMeta(false))
		}
		event.Tool.ErrorCode = string(errorcode.CodeOf(callErr))
		event.Tool.ProcessingFailure = hookrun.ProcessingFailure(errors.Join(callErr, processingErr))
		event.Tool.Error = errorText(callErr)
	}
	cleanup, cancel := toolbinding.ResultSettlementContext(ctx)
	defer cancel()
	var release func()
	bindingIntent := result == nil && isCodeModeChild(ctx) && toolbinding.Target(ctx) != nil
	if result != nil && call.Name == mcp.SearchToolName || bindingIntent {
		var err error
		release, err = r.acquireSessionWrite(cleanup, ref)
		if err != nil {
			return nil, err
		}
		defer release()
		if bindingIntent {
			events, err := r.sessions.Events(cleanup, session.EventsRequest{SessionRef: ref, IncludeTransient: true})
			if err != nil {
				return nil, err
			}
			if err := validateRecordedCodeModeBinding(ctx, events, call); err != nil {
				return nil, err
			}
		}
	}
	persisted, err := r.sessions.AppendEvent(cleanup, session.AppendEventRequest{SessionRef: ref, MutationGuard: session.RuntimeMutationGuard(ctx), Event: event})
	if err != nil {
		var readErr error
		persisted, readErr = r.reconcileEventAppend(cleanup, ref, event)
		if persisted == nil {
			return nil, errors.Join(err, readErr)
		}
	}
	if sink, ok := ctx.Value(nestedToolEventSinkKey{}).(func(*session.Event)); ok {
		sink(persisted)
	}
	return persisted, processingErr
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

func (o nestedCommandObserver) ObserveToolResult(progress toolbinding.Progress) {
	fact := *o.fact
	event := &session.Event{
		Type: session.EventTypeToolResult, Visibility: session.VisibilityUIOnly,
		Scope: &session.EventScope{TurnID: fact.Execution.TurnID, Source: "runtime_tool"},
		Actor: session.ActorRef{Kind: session.ActorKindTool, ID: fact.ID, Name: fact.Name},
		Tool:  &fact,
	}
	toolbinding.ApplyResultPresentation(event, progress.Result, nil, "", progress.EventMeta, toolbinding.TaskResultSourceMeta(toolbinding.IsTaskResultSource(o.target)))
	o.publish(event)
}
