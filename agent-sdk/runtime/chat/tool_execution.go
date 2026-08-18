package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const toolCancellationDrainGrace = 100 * time.Millisecond

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

func (a *Agent) executeToolCallWithProgress(
	ctx context.Context,
	call model.ToolCall,
	yieldProgress func(*session.Event) bool,
) (model.Message, *session.Event, error) {
	return a.executeToolCallWithProgressAdmitted(ctx, call, nil, yieldProgress, nil)
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
	if selectedTool, ok := a.lookupTool(call.Name); ok && toolbinding.IsTaskResultSource(selectedTool) {
		progressTaskResultMeta = runtimeTaskResultSourceMeta(true)
	}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		message, event, err := a.executeToolCallAdmitted(callCtx, call, step, toolObserver{results: progressCh}, visibility)
		doneCh <- toolExecutionResult{message: message, event: event, err: err}
	}()

	drainProgress := func(done toolExecutionResult) (model.Message, *session.Event, error) {
		// Always prefer the terminal tool result so execution journals are not
		// dropped when a late progress yield is refused by the consumer.
		for {
			select {
			case progress := <-progressCh:
				if yieldProgress == nil {
					continue
				}
				canonical, truncationMeta := canonicalToolResult(progress, nil)
				_ = yieldProgress(session.MarkUIOnly(toolProgressEvent(call, canonical, truncationMeta, progressTaskResultMeta)))
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
			if !yieldProgress(session.MarkUIOnly(toolProgressEvent(call, canonical, truncationMeta, progressTaskResultMeta))) {
				return model.Message{}, nil, context.Canceled
			}
		case done := <-doneCh:
			return drainProgress(done)
		case <-ctx.Done():
			// Give context-aware tools a short window to return their terminal
			// execution journal, but never let an unresponsive tool block run
			// cancellation indefinitely. Recovery reconciles the durable
			// cancel_requested record when no terminal result arrives.
			cancel()
			timer := time.NewTimer(toolCancellationDrainGrace)
			defer timer.Stop()
			select {
			case done := <-doneCh:
				return drainProgress(done)
			case <-timer.C:
				return model.Message{}, nil, ctx.Err()
			}
		}
	}
}

func (a *Agent) executeToolCall(ctx context.Context, call model.ToolCall, observer tool.Observer) (model.Message, *session.Event, error) {
	return a.executeToolCallAdmitted(ctx, call, nil, observer, nil)
}

func (a *Agent) executeToolCallAdmitted(
	ctx context.Context,
	call model.ToolCall,
	step *tool.ModelStepRef,
	observer tool.Observer,
	visibility *tool.ToolVisibility,
) (model.Message, *session.Event, error) {
	defer step.MarkAdmissionComplete()
	selectedTool, ok := a.lookupTool(call.Name)
	if !ok {
		rawOutput := tool.ErrorPayload(tool.NewError(tool.ErrorCodeNotFound, fmt.Sprintf("tool %q not found", call.Name)))
		result := tool.Result{
			ID:      call.ID,
			Name:    call.Name,
			IsError: true,
			Content: []model.Part{model.NewJSONPart(mustJSON(rawOutput))},
		}
		canonical, truncationMeta := canonicalToolResult(result, a.toolResultArtifacts)
		message := toolResultMessageFromCanonical(call, canonical)
		return message, toolResultEvent(call, canonical, &message, truncationMeta), nil
	}

	result, err := selectedTool.Call(ctx, tool.Call{
		ID:           strings.TrimSpace(call.ID),
		Name:         strings.TrimSpace(call.Name),
		Input:        json.RawMessage(strings.TrimSpace(call.Args)),
		ModelStep:    step,
		RuntimeModel: a.model,
		Observer:     observer,
	})
	if err != nil {
		result = modelVisibleToolErrorResult(call, result, err)
	}
	if err := model.ValidateRequestCapabilities(a.model, &model.Request{Instructions: result.Content}); err != nil {
		result = modelVisibleToolErrorResult(call, result, err)
	}
	result = admitToolSearchResult(selectedTool.Definition(), call, result, visibility)
	canonical, truncationMeta := canonicalToolResult(result, a.toolResultArtifacts)
	message := toolResultMessageFromCanonical(call, canonical)
	eventMeta := truncationMeta
	if toolbinding.IsTaskResultSource(selectedTool) {
		eventMeta = mergeEventMeta(eventMeta, runtimeTaskResultSourceMeta(true))
	}
	event := toolResultEvent(call, canonical, &message, eventMeta)
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
	executionJournal := result.Metadata[tool.MetadataExecutionJournal]
	result = tool.Result{
		ID:      strings.TrimSpace(call.ID),
		Name:    strings.TrimSpace(call.Name),
		IsError: true,
		Content: []model.Part{model.NewJSONPart(mustJSON(tool.ErrorPayload(err)))},
	}
	if executionJournal != nil {
		result.Metadata = map[string]any{tool.MetadataExecutionJournal: executionJournal}
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

func toolResultMessage(call model.ToolCall, result tool.Result) model.Message {
	canonical, _ := canonicalToolResult(result, nil)
	return toolResultMessageFromCanonical(call, canonical)
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
