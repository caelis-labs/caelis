package chat

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/internal/modelinvocation"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/prefixusage"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/google/uuid"
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

type collectedResponse struct {
	response  *model.Response
	stream    *textStream
	messageID string
}

func collectFinalResponse(
	ctx context.Context,
	llm model.LLM,
	req *model.Request,
	watchdog *generationWatchdog,
	yieldChunk func(*session.Event) bool,
) (collectedResponse, error) {
	if err := ctx.Err(); err != nil {
		return collectedResponse{}, err
	}
	if err := model.ValidateRequestCapabilities(llm, req); err != nil {
		return collectedResponse{}, err
	}
	mode := hookrun.FromContext(ctx).Hooks.StreamMode
	incremental := mode.PublishesIncrementally()
	irreversible := mode == hook.StreamIncremental
	ctx = model.WithIrreversibleOutputTracking(ctx)
	messageID := uuid.NewString()
	var pipeline *textStream
	var ordinal uint64
	ctx = model.WithInvocationStart(ctx, func(ctx context.Context, in model.Invocation, _ *model.Request) error {
		e := hook.OutputContext{RequestContext: hookrun.RequestEvidence(ctx)}
		e.OutputSource = hook.OutputProvider
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
		}
		return nil
	}
	var final *model.Response
	textFilters := map[int]*citationMarkerStreamFilter{}
	textFilterOrder := make([]int, 0)
	if watchdog != nil {
		watchdog.beginModelStep()
	}
	for event, err := range model.Generate(modelinvocation.WithToolRepair(ctx, req), llm, req) {
		if err != nil {
			return collectedResponse{}, err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return collectedResponse{}, ctxErr
		}
		if err := watchdog.observeStreamEvent(event); err != nil {
			return collectedResponse{}, err
		}
		if event != nil && event.Type == model.StreamEventAttemptReset {
			pipeline = nil
			clear(textFilters)
			textFilterOrder = textFilterOrder[:0]
			if req != nil && req.Stream && incremental && yieldChunk != nil {
				reset := model.AttemptReset{}
				if event.AttemptReset != nil {
					reset = *event.AttemptReset
				}
				if !yieldChunk(session.NewModelAttemptResetForMessage(reset, messageID)) {
					return collectedResponse{}, context.Canceled
				}
			}
			messageID = uuid.NewString()
			continue
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
					return collectedResponse{}, errors.New("stream has no accounted attempt")
				}
				var transformErr error
				copyDelta.TextDelta, transformErr = pipeline.push(ctx, index, copyDelta.TextDelta)
				if transformErr != nil {
					return collectedResponse{}, transformErr
				}
				copyEvent.PartDelta = &copyDelta
				visibleEvent = &copyEvent
			}
			if chunk := chunkEventFromStreamEvent(visibleEvent, messageID); chunk != nil && yieldChunk != nil {
				if !yieldChunk(chunk) {
					return collectedResponse{}, context.Canceled
				}
				if irreversible {
					model.RecordIrreversibleOutput(ctx)
				}
			}
		}
		if event != nil && event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}
	if err := ctx.Err(); err != nil {
		return collectedResponse{}, err
	}
	if incremental {
		for _, index := range textFilterOrder {
			if pending := textFilters[index].Flush(); pending != "" {
				out, err := pipeline.push(ctx, index, pending)
				if err != nil {
					return collectedResponse{}, err
				}
				if err := publish(index, out); err != nil {
					return collectedResponse{}, err
				}
			}
		}
	}

	if final == nil {
		return collectedResponse{}, errors.New("model returned no final response")
	}
	final.Message = normalizeAssistantCitations(final.Message, req.Messages)
	if pipeline == nil {
		return collectedResponse{}, errors.New("final response has no accounted attempt")
	}
	if incremental {
		transformed, transformErr := pipeline.finish(ctx, final.Message, incremental, publish)
		if transformErr != nil {
			return collectedResponse{}, transformErr
		}
		copyFinal := *final
		copyFinal.Message = transformed
		final = &copyFinal
	}

	if err := watchdog.finishModelStep(final); err != nil {
		return collectedResponse{}, err
	}
	if final.ContextWindowTokens <= 0 {
		final.ContextWindowTokens = modelContextWindowTokens(llm)
	}
	return collectedResponse{response: final, stream: pipeline, messageID: messageID}, nil
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
			Tool:       toolEventPayload(call, "pending", rawInput),
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
