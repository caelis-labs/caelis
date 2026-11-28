package chat

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

type toolObserver struct {
	results chan<- toolbinding.Progress
}

func (r toolObserver) ObserveToolResult(progress toolbinding.Progress) {
	if r.results == nil {
		return
	}
	progress.Result, _ = tool.CloneResult(progress.Result, nil)
	select {
	case r.results <- progress:
	default:
	}
}

type toolExecutionResult struct {
	message model.Message
	event   *session.Event
	err     error
}

func (a *Agent) executeToolCallWithProgressAdmitted(
	ctx context.Context,
	call model.ToolCall,
	step *tool.ModelStepRef,
	yieldProgress func(*session.Event) bool,
	visibility *toolVisibility,
) (model.Message, *session.Event, error) {
	progressCh := make(chan toolbinding.Progress, 16)
	doneCh := make(chan toolExecutionResult, 1)
	progressTaskResultMeta := toolbinding.TaskResultSourceMeta(false)
	if selectedTool, ok := a.lookupRunTool(call.Name, visibility); ok && toolbinding.IsTaskResultSource(selectedTool) {
		progressTaskResultMeta = toolbinding.TaskResultSourceMeta(true)
	}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		message, event, err := a.executeToolCallAdmitted(callCtx, call, step, toolObserver{results: progressCh}.ObserveToolResult, visibility)
		doneCh <- toolExecutionResult{message: message, event: event, err: err}
	}()

	drainProgress := func(done toolExecutionResult) (model.Message, *session.Event, error) {
		// Always prefer the completed tool result so accepted ToolCalls are not
		// dropped when a late progress yield is refused by the consumer.
		for {
			select {
			case progress := <-progressCh:
				if yieldProgress == nil {
					continue
				}
				canonical, truncationMeta := hookrun.FromContext(ctx).Hooks.ToolProjection.Preview(ctx, progress.Result)
				event := session.MarkUIOnly(toolProgressEvent(call, canonical, progress.EventMeta, truncationMeta, progressTaskResultMeta))
				_ = yieldProgress(event)
			default:
				return done.message, done.event, done.err
			}
		}
	}
	for {
		select {
		case progress := <-progressCh:
			if yieldProgress == nil {
				continue
			}
			canonical, truncationMeta := hookrun.FromContext(ctx).Hooks.ToolProjection.Preview(ctx, progress.Result)
			event := session.MarkUIOnly(toolProgressEvent(call, canonical, progress.EventMeta, truncationMeta, progressTaskResultMeta))
			if !yieldProgress(event) {
				cancel()
				<-doneCh
				return model.Message{}, nil, context.Canceled
			}
		case done := <-doneCh:
			return drainProgress(done)
		case <-ctx.Done():
			// Cancellation closes admission; producer completion retains the fence.
			cancel()
			return drainProgress(<-doneCh)
		}
	}
}

func (a *Agent) executeToolCallAdmitted(
	ctx context.Context,
	call model.ToolCall,
	step *tool.ModelStepRef,
	report func(toolbinding.Progress),
	visibility *toolVisibility,
) (model.Message, *session.Event, error) {
	defer step.MarkAdmissionComplete()
	prepared, known := a.preparedCalls[call.ID]
	if !known {
		if err := a.prepareToolCalls(ctx, []model.ToolCall{call}, visibility); err != nil {
			return model.Message{}, nil, err
		}
		prepared = a.preparedCalls[call.ID]
	}
	selectedTool, bindingErr := prepared.target, prepared.err
	if bindingErr != nil {
		rawOutput := tool.ErrorPayload(bindingErr)
		result := tool.Result{
			Content: []tool.ContentBlock{tool.StructuredContent{Value: mustJSON(rawOutput)}},
		}
		canonical, truncationMeta := hookrun.FromContext(ctx).Hooks.ToolProjection.Project(ctx, result)
		message := toolbinding.ResultMessage(call, canonical, bindingErr)
		event := toolResultEvent(call, result, bindingErr, &message, truncationMeta)
		a.applyPreparedFact(event)
		return message, event, nil
	}

	invocation := prepared.call
	ctx = toolbinding.WithOccurrence(ctx, prepared.execution, nil, prepared.binding)
	callCtx, cancel := context.WithCancel(toolbinding.WithProgress(toolbinding.WithPrepared(ctx, invocation, prepared.result, prepared.resultErr), report))
	defer cancel()
	callCtx, observation := toolbinding.Observe(callCtx)
	result, err := toolbinding.Call(callCtx, selectedTool, invocation, toolbinding.Dispatch{Model: a.model, Step: step})
	cancel()
	referenced, imageErr := tool.ReferenceResultImages(ctx, result)
	if imageErr != nil {
		err = &hookrun.ProcessingError{Cause: imageErr, EffectError: hookrun.EffectError(err)}
	}
	result = referenced
	complete, _ := tool.CloneResult(result, nil)

	if err != nil {
		var normalized *codemode.ExecutionError
		var processing *hookrun.ProcessingError
		if !tool.HasFailedReply(err) && !errors.As(err, &processing) && (!codemode.IsDefinition(selectedTool.Definition()) || !errors.As(err, &normalized)) {
			result = modelVisibleToolErrorResult(err)
			completion := observation.ScriptCompletion
			if call.Name == codemode.ToolName && completion == "" {
				completion = "failed"
			}
			observation = &toolbinding.Observation{ScriptCompletion: completion}
		}
	}
	effectErr := hookrun.EffectError(err)
	message, truncationMeta, viewErr := toolbinding.ProjectResult(ctx, selectedTool.Definition(), invocation, result, err, hookrun.FromContext(ctx).Hooks.ToolProjection.Project)
	if viewErr != nil {
		err = &hookrun.ProcessingError{Cause: viewErr, EffectError: effectErr}
		observation.BuiltinDisplay = nil
	}
	eventMeta := toolbinding.MergeEventMeta(observation.EventMeta(), truncationMeta)
	if toolbinding.IsTaskResultSource(selectedTool) {
		eventMeta = toolbinding.MergeEventMeta(eventMeta, toolbinding.TaskResultSourceMeta(true))
	}
	event := toolResultEvent(call, result, err, &message, eventMeta)
	event.Tool.Discovery = observation.Discovery && effectErr == nil
	if err != nil && len(complete.Content) > 0 {
		event.Tool.Result = &complete
	}
	event.Tool.ProcessingFailure = hookrun.ProcessingFailure(err)
	a.applyPreparedFact(event)
	if err == nil && observation.PlanUpdate != nil {
		toolbinding.RecordPlanResult(ctx, event.Tool, observation.PlanUpdate)
	}
	if err != nil {
		event.Tool.Error = err.Error()
		event.Tool.ErrorCode = string(errorcode.CodeOf(err))
	}
	var processing *hookrun.ProcessingError
	if errors.As(err, &processing) {
		event.Tool.Outcome = processingOutcome(effectErr)
		if event.Tool.Outcome == session.ToolOutcomeSucceeded {
			event.Tool.Status = "completed"
		}
	}
	var hookErr *hook.Error
	if errors.As(err, &hookErr) {
		return message, event, err
	}
	return message, event, nil
}

func modelVisibleToolErrorResult(err error) tool.Result {
	return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: mustJSON(tool.ErrorPayload(err))}}}
}

func (a *Agent) lookupTool(name string) (tool.Tool, bool) {
	if a == nil || a.toolsByName == nil {
		return nil, false
	}
	configured, ok := a.toolsByName[name]
	return configured, ok
}

func (a *Agent) lookupRunTool(name string, visibility *toolVisibility) (tool.Tool, bool) {
	if toolbinding.HideViewImage(a.toolsByName[name], a.model) {
		return nil, false
	}
	if visibility == nil {
		return a.lookupTool(name)
	}
	return visibility.lookupTool(name)
}

func processingOutcome(err error) session.ToolOutcome {
	if errorcode.Is(err, errorcode.UnknownOutcome) || errorcode.Is(err, errorcode.Cancelled) || errors.Is(err, context.Canceled) {
		return session.ToolOutcomeUnknown
	}
	if err != nil {
		return session.ToolOutcomeFailed
	}
	return session.ToolOutcomeSucceeded
}
