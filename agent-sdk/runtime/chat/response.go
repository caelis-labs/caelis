package chat

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func reasoningFromMetadata(meta map[string]any) model.ReasoningConfig {
	var reasoning model.ReasoningConfig
	if raw, ok := meta["reasoning_effort"].(string); ok {
		reasoning.Effort = strings.TrimSpace(raw)
	}
	switch raw := meta["reasoning_budget_tokens"].(type) {
	case int:
		reasoning.BudgetTokens = raw
	case int64:
		reasoning.BudgetTokens = int(raw)
	case float64:
		reasoning.BudgetTokens = int(raw)
	}
	return reasoning
}

func collectFinalResponse(
	ctx context.Context,
	llm model.LLM,
	req *model.Request,
	messageID string,
	yieldChunk func(*session.Event) bool,
) (*model.Response, error) {
	var final *model.Response
	textFilters := map[int]*citationMarkerStreamFilter{}
	textFilterOrder := make([]int, 0)
	for event, err := range llm.Generate(ctx, req) {
		if err != nil {
			return nil, err
		}
		if req != nil && req.Stream {
			visibleEvent := event
			if event != nil && event.PartDelta != nil && event.PartDelta.Kind == model.PartKindText {
				index := event.PartDelta.Index
				filter := textFilters[index]
				if filter == nil {
					filter = &citationMarkerStreamFilter{}
					textFilters[index] = filter
					textFilterOrder = append(textFilterOrder, index)
				}
				copyEvent := *event
				copyDelta := *event.PartDelta
				copyDelta.TextDelta = filter.Push(copyDelta.TextDelta)
				copyEvent.PartDelta = &copyDelta
				visibleEvent = &copyEvent
			}
			if chunk := chunkEventFromStreamEvent(visibleEvent, messageID); chunk != nil && yieldChunk != nil {
				if !yieldChunk(chunk) {
					return nil, context.Canceled
				}
			}
		}
		if event != nil && event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}
	if req != nil && req.Stream && yieldChunk != nil {
		for _, index := range textFilterOrder {
			if pending := textFilters[index].Flush(); pending != "" {
				chunk := chunkEventFromStreamEvent(&model.StreamEvent{
					Type:      model.StreamEventPartDelta,
					PartDelta: &model.PartDelta{Index: index, Kind: model.PartKindText, TextDelta: pending},
				}, messageID)
				if chunk != nil && !yieldChunk(chunk) {
					return nil, context.Canceled
				}
			}
		}
	}
	if final == nil {
		return nil, errors.New("agent-sdk/runtime/chat: model returned no final response")
	}
	if final.ContextWindowTokens <= 0 {
		final.ContextWindowTokens = modelContextWindowTokens(llm)
	}
	return final, nil
}

func modelContextWindowTokens(llm model.LLM) int {
	if llm == nil {
		return 0
	}
	provider, ok := llm.(interface{ ContextWindowTokens() int })
	if !ok {
		return 0
	}
	return provider.ContextWindowTokens()
}

func chunkEventFromStreamEvent(event *model.StreamEvent, messageID string) *session.Event {
	if event == nil {
		return nil
	}
	if event.Type == model.StreamEventAttemptReset {
		reset := model.AttemptReset{}
		if event.AttemptReset != nil {
			reset = *event.AttemptReset
		}
		return modelAttemptResetEvent(reset)
	}
	if event.PartDelta == nil {
		return nil
	}
	delta := event.PartDelta
	switch delta.Kind {
	case model.PartKindReasoning:
		if delta.TextDelta == "" {
			return nil
		}
		message := model.NewReasoningMessage(model.RoleAssistant, delta.TextDelta, model.ReasoningVisibilityVisible)
		return session.MarkUIOnly(&session.Event{
			Type:      session.EventTypeAssistant,
			MessageID: strings.TrimSpace(messageID),
			Message:   &message,
			Text:      delta.TextDelta,
			Protocol: &session.EventProtocol{
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeAgentThought),
					MessageID:     strings.TrimSpace(messageID),
					Content:       session.ProtocolTextContent(delta.TextDelta),
				},
			},
		})
	case model.PartKindText:
		if delta.TextDelta == "" {
			return nil
		}
		message := model.NewTextMessage(model.RoleAssistant, delta.TextDelta)
		return session.MarkUIOnly(&session.Event{
			Type:      session.EventTypeAssistant,
			MessageID: strings.TrimSpace(messageID),
			Message:   &message,
			Text:      delta.TextDelta,
			Protocol: &session.EventProtocol{
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
					MessageID:     strings.TrimSpace(messageID),
					Content:       session.ProtocolTextContent(delta.TextDelta),
				},
			},
		})
	default:
		return nil
	}
}

func modelAttemptResetEvent(reset model.AttemptReset) *session.Event {
	meta := map[string]any{
		"caelis": map[string]any{
			"version": 1,
			"runtime": map[string]any{
				"attempt_reset": map[string]any{},
			},
		},
	}
	resetMeta, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := resetMeta["runtime"].(map[string]any)
	attemptMeta, _ := runtimeMeta["attempt_reset"].(map[string]any)
	if reset.Attempt > 0 {
		attemptMeta["attempt"] = reset.Attempt
	}
	if reset.MaxRetries > 0 {
		attemptMeta["max_retries"] = reset.MaxRetries
	}
	if reset.RetryDelayMillis > 0 {
		attemptMeta["retry_delay_ms"] = reset.RetryDelayMillis
	}
	attemptMeta["retrying"] = reset.Retrying
	return session.MarkUIOnly(&session.Event{
		Type: session.EventTypeLifecycle,
		Text: "model attempt reset",
		Lifecycle: &session.EventLifecycle{
			Status: "attempt_reset",
			Reason: "model_retry",
		},
		Meta: meta,
	})
}

func modelResponseEvent(message model.Message, resp *model.Response, messageID string) *session.Event {
	out := &session.Event{
		Type:       session.EventTypeOf(&session.Event{Message: &message}),
		Visibility: session.VisibilityCanonical,
		MessageID:  strings.TrimSpace(messageID),
		Message:    &message,
		Text:       message.TextContent(),
	}
	if resp != nil {
		out.Meta = responseMeta(resp)
		out.Invocation = responseInvocation(resp)
	}
	return out
}

func modelToolCallEvents(message model.Message, resp *model.Response, messageID string) []*session.Event {
	calls := message.ToolCalls()
	if len(calls) == 0 {
		return nil
	}
	out := make([]*session.Event, 0, len(calls))
	baseMeta := map[string]any{}
	if resp != nil {
		baseMeta = responseMeta(resp)
	}
	for i, call := range calls {
		rawInput := mustObject(call.Args)
		meta := toolMeta(call.Name)
		if i == 0 {
			meta = mergeEventMeta(baseMeta, meta)
		}
		event := &session.Event{
			Type:       session.EventTypeToolCall,
			Visibility: session.VisibilityCanonical,
			Tool:       toolEventPayload(call, "pending", rawInput, nil, nil),
			Meta:       meta,
		}
		if i == 0 {
			event.MessageID = strings.TrimSpace(messageID)
			event.Message = &message
			event.Text = message.TextContent()
			if resp != nil {
				event.Invocation = responseInvocation(resp)
			}
		}
		out = append(out, event)
	}
	return out
}
