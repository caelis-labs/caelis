package chat

import (
	"context"
	"errors"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/google/uuid"
)

type toolSearchInvocationObserverKey struct{}

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
				event := toolProgressEvent(call, canonical, truncationMeta, progressTaskResultMeta)
				a.presentMCPEvent(event)
				_ = yieldProgress(session.MarkUIOnly(event))
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
			event := toolProgressEvent(call, canonical, truncationMeta, progressTaskResultMeta)
			a.presentMCPEvent(event)
			if !yieldProgress(session.MarkUIOnly(event)) {
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
		if call.Name == tool.MCPCallToolName {
			bindingErr = mcpdiscovery.WithEntryHint(bindingErr, tool.ToolSearchToolName)
		}
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
		a.presentMCPEvent(event)
		return message, event, nil
	}

	if tool.IsToolSearchDefinition(selectedTool.Definition()) {
		observer, _ := ctx.Value(toolSearchInvocationObserverKey{}).(func(model.Invocation))
		var admission func(context.Context, *model.Request) error
		if admit := a.admitModelRequest; admit != nil {
			admission = func(ctx context.Context, _ *model.Request) error {
				return admit(ctx, agent.ModelRequestAdmission{RequestID: uuid.NewString()})
			}
		}
		ctx = toolsearch.WithInvocationAccounting(ctx, observer, admission)
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
		if tool.IsMCPDefinition(selectedTool.Definition()) {
			err = mcpdiscovery.WithEntryHint(err, tool.ToolSearchToolName)
		}
		var normalized *codemode.ExecutionError
		if selectedTool.Definition().Metadata[tool.MetadataToolKind] != codemode.ToolKind || !errors.As(err, &normalized) {
			result = modelVisibleToolErrorResult(call, result, err)
		}
	}
	result = a.admitToolSearchResult(ctx, selectedTool.Definition(), call, result)
	canonical, truncationMeta := canonicalToolResult(result, a.toolResultArtifacts)
	message := toolResultMessageFromCanonical(call, canonical)
	message, viewErr := hookrun.ModelView(ctx, selectedTool.Definition(), invocation, message)
	if viewErr != nil {
		return model.Message{}, nil, viewErr
	}
	eventMeta := truncationMeta
	if toolbinding.IsTaskResultSource(selectedTool) {
		eventMeta = mergeEventMeta(eventMeta, runtimeTaskResultSourceMeta(true))
	}
	event := toolResultEvent(call, result, &message, eventMeta)
	if err != nil && (len(complete.Content) > 0 || complete.StructuredContent != nil) {
		event.Tool.Result = &complete
	}
	a.applyPreparedFact(event)
	if err != nil {
		event.Tool.Error = err.Error()
		event.Tool.ErrorCode = string(errorcode.CodeOf(err))
		if errorcode.Is(err, errorcode.UnknownOutcome) || errors.Is(err, context.Canceled) {
			event.Tool.Outcome = "unknown_outcome"
		}
	}
	a.presentMCPEvent(event)
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
