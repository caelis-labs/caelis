package chat

import (
	"context"
	"errors"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/prefixusage"
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
	watchdog *generationWatchdog,
	yieldChunk func(*session.Event) bool,
) (*model.Response, *textStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := model.ValidateRequestCapabilities(llm, req); err != nil {
		return nil, nil, err
	}
	incremental := hookrun.FromContext(ctx).Hooks.StreamMode == agent.StreamIncremental
	ctx = model.WithIrreversibleOutputTracking(ctx)
	var pipeline *textStream
	var ordinal uint64
	ctx = model.WithInvocationStart(ctx, func(ctx context.Context, in model.Invocation, _ *model.Request) error {
		e := hookrun.RequestEvidence(ctx)
		e.OutputSource = agent.HookOutputProvider
		e.InvocationID = in.ID
		ordinal++
		e.AttemptOrdinal = ordinal
		var err error
		pipeline, err = newTextStream(ctx, e)
		return err
	})
	publish := func(index int, text string) error {
		chunk := chunkEventFromStreamEvent(&model.StreamEvent{Type: model.StreamEventPartDelta, PartDelta: &model.PartDelta{Index: index, Kind: model.PartKindText, TextDelta: text}}, messageID)
		if chunk != nil && yieldChunk != nil {
			if !yieldChunk(chunk) {
				return context.Canceled
			}
			model.RecordIrreversibleOutput(ctx)
		}
		return nil
	}
	var final *model.Response
	textFilters := map[int]*citationMarkerStreamFilter{}
	textFilterOrder := make([]int, 0)
	if watchdog != nil {
		watchdog.beginModelStep()
	}
	for event, err := range model.Generate(ctx, llm, req) {
		if err != nil {
			return nil, nil, err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, ctxErr
		}
		if event != nil && event.Type == model.StreamEventAttemptReset {
			pipeline = nil
			clear(textFilters)
			textFilterOrder = textFilterOrder[:0]
		}
		if err := watchdog.observeStreamEvent(event); err != nil {
			return nil, nil, err
		}
		if req != nil && req.Stream && incremental {
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
				if pipeline == nil {
					return nil, nil, errors.New("stream has no accounted attempt")
				}
				var transformErr error
				copyDelta.TextDelta, transformErr = pipeline.push(ctx, index, copyDelta.TextDelta)
				if transformErr != nil {
					return nil, nil, transformErr
				}
				copyEvent.PartDelta = &copyDelta
				visibleEvent = &copyEvent
			}
			if chunk := chunkEventFromStreamEvent(visibleEvent, messageID); chunk != nil && yieldChunk != nil {
				if !yieldChunk(chunk) {
					return nil, nil, context.Canceled
				}
				if visibleEvent.PartDelta != nil {
					model.RecordIrreversibleOutput(ctx)
				}
			}
		}
		if event != nil && event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if incremental {
		for _, index := range textFilterOrder {
			if pending := textFilters[index].Flush(); pending != "" {
				out, err := pipeline.push(ctx, index, pending)
				if err != nil {
					return nil, nil, err
				}
				if err := publish(index, out); err != nil {
					return nil, nil, err
				}
			}
		}
	}

	if final == nil {
		return nil, nil, errors.New("model returned no final response")
	}
	final.Message = normalizeAssistantCitations(final.Message, req.Messages)
	if pipeline == nil {
		return nil, nil, errors.New("final response has no accounted attempt")
	}
	if incremental {
		transformed, transformErr := pipeline.finish(ctx, final.Message, incremental, publish)
		if transformErr != nil {
			return nil, nil, transformErr
		}
		copyFinal := *final
		copyFinal.Message = transformed
		final = &copyFinal
	}

	if err := watchdog.finishModelStep(final); err != nil {
		return nil, nil, err
	}
	if final.ContextWindowTokens <= 0 {
		final.ContextWindowTokens = modelContextWindowTokens(llm)
	}
	return final, pipeline, nil
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
		return session.NewModelAttemptReset(reset)
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

func modelResponseEvent(
	message model.Message,
	resp *model.Response,
	messageID string,
	invocationModel model.LLM,
	requestPrefix ...prefixusage.Snapshot,
) *session.Event {
	out := &session.Event{
		Type:       session.EventTypeOf(&session.Event{Message: &message}),
		Visibility: session.VisibilityCanonical,
		MessageID:  strings.TrimSpace(messageID),
		Message:    &message,
		Text:       message.TextContent(),
	}
	if resp != nil {
		out.Meta = responseMeta(resp)
		out.Invocation = responseInvocationForModel(resp, invocationModel, requestPrefix...)
	}
	return out
}

func modelToolCallEvents(
	message model.Message,
	resp *model.Response,
	messageID string,
	invocationModel model.LLM,
	requestPrefix ...prefixusage.Snapshot,
) []*session.Event {
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
		event.Tool.BatchID = messageID
		if i == 0 {
			event.MessageID = strings.TrimSpace(messageID)
			event.Message = &message
			event.Text = message.TextContent()
			if resp != nil {
				event.Invocation = responseInvocationForModel(resp, invocationModel, requestPrefix...)
			}
		}
		out = append(out, event)
	}
	return out
}
