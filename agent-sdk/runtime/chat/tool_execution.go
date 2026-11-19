package chat

import (
	"context"
	"errors"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

type toolObserver struct {
	results chan<- tool.Result
}

func (r toolObserver) ObserveToolResult(result tool.Result) {
	if r.results == nil {
		return
	}
	cloned, _ := tool.CloneResult(result, nil)
	select {
	case r.results <- cloned:
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
	visibility *tool.ToolVisibility,
) (model.Message, *session.Event, error) {
	progressCh := make(chan tool.Result, 16)
	doneCh := make(chan toolExecutionResult, 1)
	progressTaskResultMeta := runtimeTaskResultSourceMeta(false)
	if selectedTool, ok := a.lookupRunTool(call.Name, visibility); ok && toolbinding.IsTaskResultSource(selectedTool) {
		progressTaskResultMeta = runtimeTaskResultSourceMeta(true)
	}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		message, event, err := a.executeToolCallAdmitted(callCtx, call, step, toolObserver{results: progressCh}, visibility)
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
				canonical, truncationMeta := canonicalToolResult(progress, nil)
				event := session.MarkUIOnly(toolProgressEvent(call, canonical, truncationMeta, progressTaskResultMeta))
				a.applyPreparedFact(event)
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
			canonical, truncationMeta := canonicalToolResult(progress, nil)
			event := session.MarkUIOnly(toolProgressEvent(call, canonical, truncationMeta, progressTaskResultMeta))
			a.applyPreparedFact(event)
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
	observer tool.Observer,
	visibility *tool.ToolVisibility,
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
			ID:      call.ID,
			Name:    call.Name,
			IsError: true,
			Content: []model.Part{model.NewJSONPart(mustJSON(rawOutput))},
		}
		canonical, truncationMeta := canonicalToolResult(result, a.toolResultArtifacts)
		message := toolResultMessageFromCanonical(call, canonical)
		event := toolResultEvent(call, result, &message, truncationMeta)
		a.applyPreparedFact(event)
		return message, event, nil
	}

	invocation := prepared.call
	invocation.ModelStep = step
	invocation.RuntimeModel = a.model
	invocation.RuntimeReasoning = a.reasoning
	invocation.RuntimeServiceTier = a.request.ServiceTier
	invocation.Observer = observer
	result, err := selectedTool.Call(toolbinding.WithPrepared(ctx, invocation, prepared.result), invocation)
	complete, _ := tool.CloneResult(result, nil)

	if err != nil {
		var normalized *codemode.ExecutionError
		if selectedTool.Definition().Metadata[tool.MetadataToolKind] != codemode.ToolKind || !errors.As(err, &normalized) {
			result = modelVisibleToolErrorResult(call, result, err)
		}
	}
	canonical, truncationMeta := canonicalToolResult(result, a.toolResultArtifacts)
	message := toolResultMessageFromCanonical(call, canonical)
	originalView := model.CloneMessage(message)
	message, viewErr := hookrun.ModelView(ctx, selectedTool.Definition(), invocation, message)
	if viewErr != nil {
		message = hookrun.FailedView(originalView)
		err = &hookrun.ProcessingError{Cause: viewErr, EffectError: err, Failed: complete.IsError}
	}
	eventMeta := truncationMeta
	if toolbinding.IsTaskResultSource(selectedTool) {
		eventMeta = mergeEventMeta(eventMeta, runtimeTaskResultSourceMeta(true))
	}
	event := toolResultEvent(call, result, &message, eventMeta)
	if err != nil && (len(complete.Content) > 0 || complete.StructuredContent != nil) {
		event.Tool.Result = &complete
	}
	event.Tool.ProcessingFailure = hookrun.ProcessingFailure(err)
	a.applyPreparedFact(event)
	if err != nil {
		event.Tool.Error = err.Error()
		event.Tool.ErrorCode = string(errorcode.CodeOf(err))
		if errorcode.Is(err, errorcode.UnknownOutcome) || errors.Is(err, context.Canceled) {
			event.Tool.Outcome = "unknown_outcome"
		}
	}
	var processing *hookrun.ProcessingError
	if errors.As(err, &processing) {
		effect, effectErr := hookrun.EffectResult(complete, err)
		event.Tool.Outcome = session.ToolOutcome(processingOutcome(effect, effectErr))
		if event.Tool.Outcome == session.ToolOutcomeSucceeded {
			event.Tool.Status = "completed"
		}
	}
	var hookErr *agent.HookError
	if errors.As(err, &hookErr) {
		return message, event, err
	}
	return message, event, nil
}

func runtimeTaskResultSourceMeta(trusted bool) map[string]any {
	return map[string]any{
		"caelis": map[string]any{"runtime": map[string]any{
			toolbinding.MetadataSection: map[string]any{toolbinding.MetadataTaskResult: trusted},
		}},
	}
}

func modelVisibleToolErrorResult(call model.ToolCall, result tool.Result, err error) tool.Result {
	scriptCompletion := result.Metadata[codemode.MetadataScriptCompletion]
	result = tool.Result{
		ID:      strings.TrimSpace(call.ID),
		Name:    strings.TrimSpace(call.Name),
		IsError: true,
		Content: []model.Part{model.NewJSONPart(mustJSON(tool.ErrorPayload(err)))},
	}
	result.Metadata = map[string]any{}
	if scriptCompletion != "" && scriptCompletion != nil {
		result.Metadata[codemode.MetadataScriptCompletion] = scriptCompletion
	} else if call.Name == "js_exec" {
		result.Metadata[codemode.MetadataScriptCompletion] = "failed"
	}
	return result
}

func (a *Agent) lookupTool(name string) (tool.Tool, bool) {
	if a == nil || a.toolsByName == nil {
		return nil, false
	}
	configured, ok := a.toolsByName[name]
	return configured, ok
}

func canonicalToolResult(result tool.Result, artifacts *toolResultArtifactStore) (tool.Result, map[string]any) {
	policy := tool.DefaultTruncationPolicy()
	original := result
	result, reservedCollision := toolResultWithoutReservedNamespace(result)
	var protectedJSONFields map[string]any
	if artifacts != nil && tool.ResultNeedsTruncation(result, policy) {
		if path, ok := artifacts.write(original); ok {
			if withHint, protected, ok := toolResultWithArtifactHint(result, path, reservedCollision); ok {
				result = withHint
				protectedJSONFields = protected
			} else {
				_ = artifacts.remove(path)
			}
		}
	}
	canonical, info := tool.TruncateResultWithInfoAndProtectedJSONFields(result, policy, protectedJSONFields)
	return canonical, mergeEventMeta(
		toolTruncationEventMeta(info),
		toolReservedNamespaceCollisionEventMeta(reservedCollision),
	)
}

func toolResultMessageFromCanonical(call model.ToolCall, result tool.Result) model.Message {
	parts := model.CloneParts(result.Content)
	if len(parts) == 0 {
		parts = []model.Part{model.NewJSONPart(mustJSON(map[string]any{}))}
	}
	message := model.Message{
		Role: model.RoleTool,
		Parts: []model.Part{{
			Kind: model.PartKindToolResult,
			ToolResult: &model.ToolResultPart{
				ToolUseID: strings.TrimSpace(call.ID),
				Name:      strings.TrimSpace(call.Name),
				Content:   parts,
				IsError:   result.IsError,
			},
		}},
	}
	return message
}

func (a *Agent) lookupRunTool(name string, visibility *tool.ToolVisibility) (tool.Tool, bool) {
	if visibility == nil {
		return a.lookupTool(name)
	}
	return visibility.LookupTool(name)
}

// CanonicalToolResult projects a complete result for a nested fact without
// changing the value returned to the calling script.
func CanonicalToolResult(call model.ToolCall, result tool.Result) model.Message {
	canonical, _ := canonicalToolResult(result, nil)
	return toolResultMessageFromCanonical(call, canonical)
}

func processingOutcome(r tool.Result, err error) string {
	if errorcode.Is(err, errorcode.UnknownOutcome) || errors.Is(err, context.Canceled) {
		return "unknown_outcome"
	}
	if err != nil || r.IsError {
		return "failed"
	}
	return "succeeded"
}
