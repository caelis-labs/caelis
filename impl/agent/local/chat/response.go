package chat

import (
	"context"
	"errors"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
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
	yieldChunk func(*session.Event) bool,
) (*model.Response, error) {
	var final *model.Response
	for event, err := range llm.Generate(ctx, req) {
		if err != nil {
			return nil, err
		}
		if req != nil && req.Stream {
			if chunk := chunkEventFromStreamEvent(event); chunk != nil && yieldChunk != nil {
				if !yieldChunk(chunk) {
					return nil, context.Canceled
				}
			}
		}
		if event != nil && event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}
	if final == nil {
		return nil, errors.New("impl/agent/local/chat: model returned no final response")
	}
	return final, nil
}

func chunkEventFromStreamEvent(event *model.StreamEvent) *session.Event {
	if event == nil {
		return nil
	}
	if event.Type == model.StreamEventAttemptReset {
		attempt := 0
		cause := ""
		retrying := false
		if event.AttemptReset != nil {
			attempt = event.AttemptReset.Attempt
			cause = event.AttemptReset.Cause
			retrying = event.AttemptReset.Retrying
		}
		return modelAttemptResetEvent(attempt, cause, retrying)
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
			Type:    session.EventTypeAssistant,
			Message: &message,
			Text:    delta.TextDelta,
			Protocol: &session.EventProtocol{
				UpdateType: string(session.ProtocolUpdateTypeAgentThought),
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeAgentThought),
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
			Type:    session.EventTypeAssistant,
			Message: &message,
			Text:    delta.TextDelta,
			Protocol: &session.EventProtocol{
				UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
					Content:       session.ProtocolTextContent(delta.TextDelta),
				},
			},
		})
	default:
		return nil
	}
}

func modelAttemptResetEvent(attempt int, cause string, retrying bool) *session.Event {
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
	if attempt > 0 {
		attemptMeta["attempt"] = attempt
	}
	if cause := strings.TrimSpace(cause); cause != "" {
		attemptMeta["cause"] = cause
	}
	attemptMeta["retrying"] = retrying
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

func modelResponseEvent(message model.Message, resp *model.Response) *session.Event {
	out := &session.Event{
		Type:       session.EventTypeOf(&session.Event{Message: &message}),
		Visibility: session.VisibilityCanonical,
		Message:    &message,
		Text:       message.TextContent(),
	}
	if resp != nil {
		out.Meta = responseMeta(resp)
		out.Invocation = responseInvocation(resp)
	}
	return out
}

func modelToolCallEvents(message model.Message, resp *model.Response) []*session.Event {
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
