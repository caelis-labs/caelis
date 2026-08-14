package chat

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/prefixusage"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/userdisplay"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/google/uuid"
)

// Factory constructs baseline chat agents from one runtime.AgentSpec.
type Factory struct {
	SystemPrompt string
}

// Agent is the minimal model-backed chat agent.
type Agent struct {
	name                string
	model               model.LLM
	tools               []tool.Tool
	systemPrompt        string
	reasoning           model.ReasoningConfig
	request             agent.ModelRequestOptions
	toolResultArtifacts *toolResultArtifactStore
}

// New returns one concrete chat agent.
func New(name string, model model.LLM, systemPrompt string) (*Agent, error) {
	if model == nil {
		return nil, errors.New("agent-sdk/runtime/chat: model is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "chat"
	}
	return &Agent{
		name:                name,
		model:               model,
		systemPrompt:        strings.TrimSpace(systemPrompt),
		toolResultArtifacts: defaultToolResultArtifactStore(),
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
		watchdog := newDefaultGenerationWatchdog()
		visibility := tool.NewToolVisibilityForModel(a.tools, a.model)
		for event := range ctx.Events().All() {
			if event != nil {
				visibility.ApplyDiscoveredToolNames(tool.DiscoveredToolNamesFromMetadata(event.Meta))
			}
			if event != nil && event.Tool != nil {
				visibility.ApplyToolResult(event.Tool.Name, event.Tool.Output)
			}
		}
		for {
			assistantMessage, calls, final, messageID, requestPrefix, ok, err := a.collectCanonicalModelStep(ctx, messages, stream, watchdog, &visibility, func(event *session.Event) bool {
				return yield(event, nil)
			})
			if !ok {
				return
			}
			if err != nil {
				var loopErr *GenerationLoopError
				if errors.As(err, &loopErr) {
					if !yield(generationLoopEvent(loopErr), nil) {
						return
					}
				}
				yield(nil, err)
				return
			}
			if len(calls) == 0 {
				assistantEvent := modelResponseEvent(assistantMessage, final, messageID, a.model, requestPrefix)
				if !yield(assistantEvent, nil) {
					return
				}
				messages = append(messages, assistantMessage)
				accepted, drainErr := a.drainPendingSubmissions(ctx, &messages, func(event *session.Event) bool {
					return yield(event, nil)
				})
				if drainErr != nil {
					yield(nil, drainErr)
					return
				}
				if accepted {
					watchdog.resetAll()
					continue
				}
				return
			}
			toolCallEvents := modelToolCallEvents(assistantMessage, final, messageID, a.model, requestPrefix)
			for _, event := range toolCallEvents {
				if !yield(event, nil) {
					return
				}
			}
			messages = append(messages, assistantMessage)
			toolMessages, toolEvents, ok, err := a.executeStepToolCalls(ctx, messageID, calls, func(event *session.Event) bool {
				return yield(event, nil)
			}, &visibility)
			if !ok {
				return
			}
			if err != nil {
				yield(nil, err)
				return
			}
			for _, toolEvent := range toolEvents {
				if toolEvent != nil && toolEvent.Tool != nil {
					visibility.ApplyToolResult(toolEvent.Tool.Name, toolEvent.Tool.Output)
				}
				if !yield(toolEvent, nil) {
					return
				}
			}
			messages = append(messages, toolMessages...)
			accepted, drainErr := a.drainPendingSubmissions(ctx, &messages, func(event *session.Event) bool {
				return yield(event, nil)
			})
			if drainErr != nil {
				yield(nil, drainErr)
				return
			}
			if accepted {
				watchdog.resetAll()
			}
		}
	}
}

func (a *Agent) collectCanonicalModelStep(
	ctx agent.Context,
	messages []model.Message,
	stream bool,
	watchdog *generationWatchdog,
	visibility *tool.ToolVisibility,
	yield func(*session.Event) bool,
) (model.Message, []model.ToolCall, *model.Response, string, prefixusage.Snapshot, bool, error) {
	for attempt := 0; ; attempt++ {
		messageID := uuid.NewString()
		request := &model.Request{
			Messages:  messages,
			Tools:     visibility.ModelSpecs(),
			Reasoning: a.reasoning,
			Output:    a.request.OutputSpec(),
			Stream:    stream,
		}
		request.Instructions = append(request.Instructions, instructionsFromContext(ctx, a.systemPrompt)...)

		modelCtx := model.WithProviderRequestMetadata(ctx, model.ProviderRequestMetadata{
			SessionAffinity: ctx.Session().SessionID,
		})
		final, err := collectFinalResponse(modelCtx, a.model, request, messageID, watchdog, yield)
		if err != nil {
			return model.Message{}, nil, nil, "", prefixusage.Snapshot{}, true, err
		}
		final.Message = normalizeAssistantCitations(final.Message, messages)

		assistantMessage, calls, err := canonicalizeAssistantToolCalls(final.Message, a.tools...)
		if err == nil {
			return assistantMessage, calls, final, messageID, prefixusage.ForRequest(request), true, nil
		}
		if attempt >= maxInvalidToolCallRepairAttempts {
			return model.Message{}, nil, nil, "", prefixusage.Snapshot{}, true, err
		}
		if reset := invalidToolCallAttemptResetEvent(attempt + 1); reset != nil {
			if yield != nil && !yield(reset) {
				return model.Message{}, nil, nil, "", prefixusage.Snapshot{}, false, nil
			}
		}
		for _, event := range invalidToolCallWarningEvents(final.Message, err, !stream) {
			if yield != nil && !yield(event) {
				return model.Message{}, nil, nil, "", prefixusage.Snapshot{}, false, nil
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
	stepID string,
	calls []model.ToolCall,
	yieldProgress func(*session.Event) bool,
	visibility *tool.ToolVisibility,
) ([]model.Message, []*session.Event, bool, error) {
	if len(calls) == 0 {
		return nil, nil, true, nil
	}
	if len(calls) == 1 {
		toolMessage, toolEvent, err := a.executeToolCallWithProgressAdmitted(ctx, calls[0], modelStepRef(stepID, 0, len(calls)), yieldProgress, visibility)
		if err != nil {
			return nil, nil, true, err
		}
		return []model.Message{toolMessage}, []*session.Event{toolEvent}, true, nil
	}
	if !a.canExecuteStepToolCallsConcurrently(calls) {
		return a.executeStepToolCallsSerial(ctx, stepID, calls, yieldProgress, visibility)
	}
	stepRefs := tool.NewConcurrentModelStepRefs(stepID, len(calls))

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
			toolMessage, toolEvent, err := a.executeToolCallWithProgressAdmitted(callCtx, call, stepRefs[i], func(event *session.Event) bool {
				if event == nil {
					return true
				}
				select {
				case progressCh <- event:
					return true
				case <-callCtx.Done():
					return false
				}
			}, visibility)
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
	// Nil the parent-done channel after the first cancel so select cannot
	// busy-spin on a permanently ready ctx.Done while draining completions.
	parentDone := ctx.Done()
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
		case <-parentDone:
			// Drain in-flight completions so terminal tool journals are not dropped.
			cancel()
			parentDone = nil
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
	stepID string,
	calls []model.ToolCall,
	yieldProgress func(*session.Event) bool,
	visibility *tool.ToolVisibility,
) ([]model.Message, []*session.Event, bool, error) {
	messages := make([]model.Message, 0, len(calls))
	events := make([]*session.Event, 0, len(calls))
	for index, call := range calls {
		toolMessage, toolEvent, err := a.executeToolCallWithProgressAdmitted(ctx, call, modelStepRef(stepID, index, len(calls)), yieldProgress, visibility)
		if err != nil {
			return nil, nil, true, err
		}
		messages = append(messages, toolMessage)
		events = append(events, toolEvent)
	}
	return messages, events, true, nil
}

func modelStepRef(stepID string, index int, callCount int) *tool.ModelStepRef {
	stepID = strings.TrimSpace(stepID)
	if stepID == "" || index < 0 || callCount <= 0 || index >= callCount {
		return nil
	}
	return &tool.ModelStepRef{ID: stepID, Index: index, CallCount: callCount}
}

func (a *Agent) canExecuteStepToolCallsConcurrently(calls []model.ToolCall) bool {
	if len(calls) < 2 {
		return false
	}
	for _, call := range calls {
		item, ok := a.lookupTool(call.Name)
		if !ok || !item.Definition().Capabilities.ParallelSafe {
			return false
		}
	}
	return true
}

func (a *Agent) drainPendingSubmissions(
	ctx agent.Context,
	messages *[]model.Message,
	yield func(*session.Event) bool,
) (bool, error) {
	if ctx == nil {
		return false, nil
	}
	drained := ctx.DrainSubmissions()
	accepted := false
	for _, submission := range drained {
		if !isModelInputSubmission(submission) {
			continue
		}
		if submission.Kind == agent.SubmissionKindAgentMessage {
			if err := validateAgentMessageSubmission(submission); err != nil {
				return accepted, err
			}
		}
		text := strings.TrimSpace(submission.Text)
		if text == "" && len(submission.ContentParts) == 0 {
			continue
		}
		message, displayText, meta := userdisplay.Resolve(text, submission.DisplayInput, submission.ContentParts, submission.Metadata)
		eventType := session.EventTypeUser
		actor := session.ActorRef{Kind: session.ActorKindUser, Name: "user"}
		if submission.Kind == agent.SubmissionKindAgentMessage {
			eventType = session.EventTypeContext
			actor = session.CloneActorRef(submission.Actor)
			if meta == nil {
				meta = map[string]any{}
			}
			// SubmissionKindAgentMessage is assigned only by the trusted Runtime
			// delivery boundary. Canonicalize its provenance here so a metadata
			// omission cannot silently degrade the provider projection.
			meta["agent_message"] = true
		}
		event := &session.Event{
			IdempotencyKey: agentMessageIdempotencyKey(submission),
			Type:           eventType,
			Visibility:     session.VisibilityCanonical,
			Actor:          actor,
			MessageID:      strings.TrimSpace(submission.MessageID),
			Message:        &message,
			Text:           displayText,
			Meta:           meta,
		}
		if submission.Scope != nil {
			scope := session.CloneEventScope(*submission.Scope)
			event.Scope = &scope
		}
		providerMessage := message
		if eventType == session.EventTypeContext {
			projected, ok := messageFromInvocationEvent(event)
			if !ok {
				return accepted, fmt.Errorf("agent-sdk/runtime/chat: Agent message submission could not be projected into model context")
			}
			providerMessage = projected
		}
		if submission.Persisted {
			*messages = append(*messages, providerMessage)
			accepted = true
			continue
		}
		if eventType == session.EventTypeUser {
			event.Protocol = &session.EventProtocol{Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeUserMessage),
				Content:       session.ProtocolTextContent(displayText),
			}}
		}
		if !yield(event) {
			return accepted, nil
		}
		*messages = append(*messages, providerMessage)
		accepted = true
	}
	return accepted, nil
}

func validateAgentMessageSubmission(sub agent.Submission) error {
	if strings.TrimSpace(sub.MessageID) == "" || !session.ActorRefHasIdentity(sub.Actor) ||
		(strings.TrimSpace(sub.Text) == "" && len(sub.ContentParts) == 0) {
		return fmt.Errorf("agent-sdk/runtime/chat: Agent message submission requires message id, content, and source identity")
	}
	switch sub.Actor.Kind {
	case session.ActorKindController, session.ActorKindParticipant:
		return nil
	default:
		return fmt.Errorf("agent-sdk/runtime/chat: Agent message source kind %q is not allowed", sub.Actor.Kind)
	}
}

func isModelInputSubmission(sub agent.Submission) bool {
	switch sub.Kind {
	case agent.SubmissionKindConversation, agent.SubmissionKindAgentMessage:
		return true
	default:
		return false
	}
}

func agentMessageIdempotencyKey(sub agent.Submission) string {
	if sub.Kind != agent.SubmissionKindAgentMessage {
		return ""
	}
	if id := strings.TrimSpace(sub.MessageID); id != "" {
		return "agent-message:" + id
	}
	return ""
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
	return session.CloneState(values)
}
