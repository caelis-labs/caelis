package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
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

func (a *Agent) executeToolCallWithProgress(
	ctx context.Context,
	call model.ToolCall,
	yieldProgress func(*session.Event) bool,
) (model.Message, *session.Event, error) {
	progressCh := make(chan tool.Result, 16)
	doneCh := make(chan toolExecutionResult, 1)
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		message, event, err := a.executeToolCall(callCtx, call, toolObserver{results: progressCh})
		doneCh <- toolExecutionResult{message: message, event: event, err: err}
	}()

	for {
		select {
		case progress := <-progressCh:
			if yieldProgress == nil {
				continue
			}
			canonical, truncationMeta := canonicalToolResult(progress)
			if !yieldProgress(session.MarkUIOnly(toolResultEvent(call, canonical, nil, truncationMeta))) {
				return model.Message{}, nil, context.Canceled
			}
		case done := <-doneCh:
			for {
				select {
				case progress := <-progressCh:
					canonical, truncationMeta := canonicalToolResult(progress)
					if yieldProgress != nil && !yieldProgress(session.MarkUIOnly(toolResultEvent(call, canonical, nil, truncationMeta))) {
						return model.Message{}, nil, context.Canceled
					}
				default:
					return done.message, done.event, done.err
				}
			}
		case <-ctx.Done():
			return model.Message{}, nil, ctx.Err()
		}
	}
}

func (a *Agent) executeToolCall(ctx context.Context, call model.ToolCall, observer tool.Observer) (model.Message, *session.Event, error) {
	selectedTool, ok := a.lookupTool(call.Name)
	if !ok {
		rawOutput := tool.ErrorPayload(tool.NewError(tool.ErrorCodeNotFound, fmt.Sprintf("tool %q not found", call.Name)))
		result := tool.Result{
			ID:      call.ID,
			Name:    call.Name,
			IsError: true,
			Content: []model.Part{model.NewJSONPart(mustJSON(rawOutput))},
		}
		canonical, truncationMeta := canonicalToolResult(result)
		message := toolResultMessageFromCanonical(call, canonical)
		return message, toolResultEvent(call, canonical, &message, truncationMeta), nil
	}

	result, err := selectedTool.Call(ctx, tool.Call{
		ID:           strings.TrimSpace(call.ID),
		Name:         strings.TrimSpace(call.Name),
		Input:        json.RawMessage(strings.TrimSpace(call.Args)),
		RuntimeModel: a.model,
		Observer:     observer,
	})
	if err != nil {
		result = tool.Result{
			ID:      strings.TrimSpace(call.ID),
			Name:    strings.TrimSpace(call.Name),
			IsError: true,
			Content: []model.Part{model.NewJSONPart(mustJSON(tool.ErrorPayload(err)))},
		}
	}
	canonical, truncationMeta := canonicalToolResult(result)
	message := toolResultMessageFromCanonical(call, canonical)
	event := toolResultEvent(call, canonical, &message, truncationMeta)
	return message, event, nil
}

func (a *Agent) lookupTool(name string) (tool.Tool, bool) {
	name = strings.TrimSpace(strings.ToUpper(name))
	for _, item := range a.tools {
		if item == nil {
			continue
		}
		if strings.TrimSpace(strings.ToUpper(item.Definition().Name)) == name {
			return item, true
		}
	}
	return nil, false
}

func toolResultMessage(call model.ToolCall, result tool.Result) model.Message {
	message, _ := toolResultMessageWithMeta(call, result)
	return message
}

func canonicalToolResult(result tool.Result) (tool.Result, map[string]any) {
	canonical, info := tool.TruncateResultWithInfo(result, tool.DefaultTruncationPolicy())
	return canonical, toolTruncationEventMeta(info)
}

func toolResultMessageWithMeta(call model.ToolCall, result tool.Result) (model.Message, map[string]any) {
	result, truncationMeta := canonicalToolResult(result)
	return toolResultMessageFromCanonical(call, result), truncationMeta
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
