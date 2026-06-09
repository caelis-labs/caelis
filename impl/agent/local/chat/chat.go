package chat

import (
	"context"
	"errors"
	"iter"
	"maps"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

// Factory constructs baseline chat agents from one runtime.AgentSpec.
type Factory struct {
	SystemPrompt string
}

// Agent is the minimal model-backed chat agent.
type Agent struct {
	name         string
	model        model.LLM
	tools        []tool.Tool
	systemPrompt string
	reasoning    model.ReasoningConfig
	request      agent.ModelRequestOptions
}

// New returns one concrete chat agent.
func New(name string, model model.LLM, systemPrompt string) (*Agent, error) {
	if model == nil {
		return nil, errors.New("impl/agent/local/chat: model is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "chat"
	}
	return &Agent{
		name:         name,
		model:        model,
		systemPrompt: strings.TrimSpace(systemPrompt),
	}, nil
}

// NewWithTools returns one chat agent with builtin tool access.
func NewWithTools(name string, model model.LLM, tools []tool.Tool, systemPrompt string) (*Agent, error) {
	agent, err := New(name, model, systemPrompt)
	if err != nil {
		return nil, err
	}
	agent.tools = append([]tool.Tool(nil), tools...)
	return agent, nil
}

// NewAgent constructs one chat agent from one runtime.AgentSpec.
func (f Factory) NewAgent(_ context.Context, spec agent.AgentSpec) (agent.Agent, error) {
	systemPrompt := ""
	if raw, ok := spec.Metadata["system_prompt"].(string); ok {
		systemPrompt = strings.TrimSpace(raw)
	}
	if systemPrompt == "" {
		systemPrompt = strings.TrimSpace(f.SystemPrompt)
	}
	chatAgent, err := NewWithTools(spec.Name, spec.Model, spec.Tools, systemPrompt)
	if err != nil {
		return nil, err
	}
	chatAgent.reasoning = reasoningFromMetadata(spec.Metadata)
	chatAgent.request = spec.Request.WithDefaults(agent.ModelRequestOptions{})
	return chatAgent, nil
}

func (a *Agent) Name() string {
	return a.name
}

func (a *Agent) Run(ctx agent.Context) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		messages := messagesFromContext(ctx)
		stream := a.request.StreamEnabled(false)
		for {
			assistantMessage, calls, final, ok, err := a.collectCanonicalModelStep(ctx, messages, stream, func(event *session.Event) bool {
				return yield(event, nil)
			})
			if !ok {
				return
			}
			if err != nil {
				yield(nil, err)
				return
			}
			if len(calls) == 0 {
				assistantEvent := modelResponseEvent(assistantMessage, final)
				if !yield(assistantEvent, nil) {
					return
				}
				messages = append(messages, assistantMessage)
				if a.drainPendingSubmissions(ctx, &messages, func(event *session.Event) bool {
					return yield(event, nil)
				}) {
					continue
				}
				return
			}
			toolCallEvents := modelToolCallEvents(assistantMessage, final)
			for _, event := range toolCallEvents {
				if !yield(event, nil) {
					return
				}
			}
			messages = append(messages, assistantMessage)
			toolMessages, toolEvents, ok, err := a.executeStepToolCalls(ctx, calls, func(event *session.Event) bool {
				return yield(event, nil)
			})
			if !ok {
				return
			}
			if err != nil {
				yield(nil, err)
				return
			}
			for _, toolEvent := range toolEvents {
				if !yield(toolEvent, nil) {
					return
				}
			}
			messages = append(messages, toolMessages...)
			a.drainPendingSubmissions(ctx, &messages, func(event *session.Event) bool {
				return yield(event, nil)
			})
		}
	}
}

func (a *Agent) collectCanonicalModelStep(
	ctx agent.Context,
	messages []model.Message,
	stream bool,
	yield func(*session.Event) bool,
) (model.Message, []model.ToolCall, *model.Response, bool, error) {
	for attempt := 0; ; attempt++ {
		request := &model.Request{
			Messages:  messages,
			Tools:     tool.ModelSpecs(a.tools),
			Reasoning: a.reasoning,
			Output:    a.request.OutputSpec(),
			Stream:    stream,
		}
		request.Instructions = append(request.Instructions, instructionsFromContext(ctx, a.systemPrompt)...)

		final, err := collectFinalResponse(ctx, a.model, request, yield)
		if err != nil {
			return model.Message{}, nil, nil, true, err
		}

		assistantMessage, calls, err := canonicalizeAssistantToolCalls(final.Message, a.tools...)
		if err == nil {
			return assistantMessage, calls, final, true, nil
		}
		if attempt >= maxInvalidToolCallRepairAttempts {
			return model.Message{}, nil, nil, true, err
		}
		if reset := invalidToolCallAttemptResetEvent(attempt+1, err); reset != nil {
			if yield != nil && !yield(reset) {
				return model.Message{}, nil, nil, false, nil
			}
		}
		for _, event := range invalidToolCallWarningEvents(final.Message, err, !stream) {
			if yield != nil && !yield(event) {
				return model.Message{}, nil, nil, false, nil
			}
		}
	}
}

type stepToolCallResult struct {
	index   int
	message model.Message
	event   *session.Event
	err     error
}

func (a *Agent) executeStepToolCalls(
	ctx context.Context,
	calls []model.ToolCall,
	yieldProgress func(*session.Event) bool,
) ([]model.Message, []*session.Event, bool, error) {
	if len(calls) == 0 {
		return nil, nil, true, nil
	}
	if len(calls) == 1 {
		toolMessage, toolEvent, err := a.executeToolCallWithProgress(ctx, calls[0], yieldProgress)
		if err != nil {
			return nil, nil, true, err
		}
		return []model.Message{toolMessage}, []*session.Event{toolEvent}, true, nil
	}
	if !canExecuteStepToolCallsConcurrently(calls) {
		return a.executeStepToolCallsSerial(ctx, calls, yieldProgress)
	}

	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	progressCh := make(chan *session.Event, len(calls)*16)
	doneCh := make(chan stepToolCallResult, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		i, call := i, call
		wg.Add(1)
		go func() {
			defer wg.Done()
			toolMessage, toolEvent, err := a.executeToolCallWithProgress(callCtx, call, func(event *session.Event) bool {
				if event == nil {
					return true
				}
				select {
				case progressCh <- event:
					return true
				case <-callCtx.Done():
					return false
				}
			})
			doneCh <- stepToolCallResult{index: i, message: toolMessage, event: toolEvent, err: err}
		}()
	}
	go func() {
		wg.Wait()
		close(doneCh)
		close(progressCh)
	}()

	results := make([]stepToolCallResult, len(calls))
	remaining := len(calls)
	var firstErr error
	for remaining > 0 {
		select {
		case progress, ok := <-progressCh:
			if !ok {
				progressCh = nil
				continue
			}
			if progress != nil && yieldProgress != nil && !yieldProgress(progress) {
				cancel()
				return nil, nil, false, nil
			}
		case result, ok := <-doneCh:
			if !ok {
				doneCh = nil
				continue
			}
			results[result.index] = result
			remaining--
			if result.err != nil && firstErr == nil {
				firstErr = result.err
				cancel()
			}
		case <-ctx.Done():
			cancel()
			return nil, nil, true, ctx.Err()
		}
	}
	if progressCh != nil {
		for progress := range progressCh {
			if progress != nil && yieldProgress != nil && !yieldProgress(progress) {
				cancel()
				return nil, nil, false, nil
			}
		}
	}
	if firstErr != nil {
		return nil, nil, true, firstErr
	}

	messages := make([]model.Message, 0, len(results))
	events := make([]*session.Event, 0, len(results))
	for _, result := range results {
		messages = append(messages, result.message)
		events = append(events, result.event)
	}
	return messages, events, true, nil
}

func (a *Agent) executeStepToolCallsSerial(
	ctx context.Context,
	calls []model.ToolCall,
	yieldProgress func(*session.Event) bool,
) ([]model.Message, []*session.Event, bool, error) {
	messages := make([]model.Message, 0, len(calls))
	events := make([]*session.Event, 0, len(calls))
	for _, call := range calls {
		toolMessage, toolEvent, err := a.executeToolCallWithProgress(ctx, call, yieldProgress)
		if err != nil {
			return nil, nil, true, err
		}
		messages = append(messages, toolMessage)
		events = append(events, toolEvent)
	}
	return messages, events, true, nil
}

func canExecuteStepToolCallsConcurrently(calls []model.ToolCall) bool {
	if len(calls) < 2 {
		return false
	}
	for _, call := range calls {
		if !strings.EqualFold(strings.TrimSpace(call.Name), "RUN_COMMAND") {
			return false
		}
	}
	return true
}

func (a *Agent) drainPendingSubmissions(ctx agent.Context, messages *[]model.Message, yield func(*session.Event) bool) bool {
	if ctx == nil {
		return false
	}
	drained := ctx.DrainSubmissions()
	accepted := false
	for _, submission := range drained {
		if !isConversationSubmission(submission) {
			continue
		}
		text := strings.TrimSpace(submission.Text)
		if text == "" && len(submission.ContentParts) == 0 {
			continue
		}
		message := model.MessageFromTextAndContentParts(model.RoleUser, text, submission.ContentParts)
		event := &session.Event{
			Type:       session.EventTypeUser,
			Visibility: session.VisibilityCanonical,
			Actor:      session.ActorRef{Kind: session.ActorKindUser, Name: "user"},
			Message:    &message,
			Text:       message.TextContent(),
			Meta:       pendingSubmissionMeta(submission),
		}
		if !yield(event) {
			return accepted
		}
		*messages = append(*messages, message)
		accepted = true
	}
	return accepted
}

func isConversationSubmission(sub agent.Submission) bool {
	switch sub.Kind {
	case agent.SubmissionKindConversation:
		return true
	default:
		return false
	}
}

func pendingSubmissionMeta(sub agent.Submission) map[string]any {
	meta := maps.Clone(sub.Metadata)
	if len(meta) == 0 {
		return nil
	}
	return meta
}

func instructionsFromContext(_ agent.Context, systemPrompt string) []model.Part {
	out := make([]model.Part, 0, 1)
	if strings.TrimSpace(systemPrompt) != "" {
		out = append(out, model.NewTextPart(strings.TrimSpace(systemPrompt)))
	}
	return out
}

// Metadata returns one stable agent metadata map for upstream assembly.
func Metadata(systemPrompt string) map[string]any {
	systemPrompt = strings.TrimSpace(systemPrompt)
	if systemPrompt == "" {
		return nil
	}
	return map[string]any{"system_prompt": systemPrompt}
}

// CloneMetadata returns one shallow metadata copy.
func CloneMetadata(values map[string]any) map[string]any {
	return maps.Clone(values)
}
