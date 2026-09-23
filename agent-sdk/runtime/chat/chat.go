package chat

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/internal/agentcommunication"
	"github.com/caelis-labs/caelis/agent-sdk/internal/runtimeinput"
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
	deferredTools       tool.Source
	toolsByName         map[string]tool.Tool
	systemPrompt        string
	reasoning           model.ReasoningConfig
	request             agent.ModelRequestOptions
	toolResultArtifacts *toolResultArtifactStore
	resolveModelRequest agent.ModelRequestResolver
	instructions        []model.Part
	admitModelRequest   func(context.Context, agent.ModelRequestAdmission) error
	discoveredTools     []string
}

// New returns one concrete chat agent.
func New(name string, model model.LLM, systemPrompt string) (*Agent, error) {
	if model == nil {
		return nil, errors.New("model is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "chat"
	}
	return &Agent{
		name:                name,
		model:               model,
		toolsByName:         map[string]tool.Tool{},
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
	for index, configured := range agent.tools {
		if configured == nil {
			continue
		}
		definition := configured.Definition()
		name := definition.Name
		switch {
		case name == "":
			return nil, fmt.Errorf("tool at index %d has an empty Definition.Name", index)
		case name != strings.TrimSpace(name):
			return nil, fmt.Errorf("tool Definition.Name %q has surrounding whitespace", name)
		case agent.toolsByName[name] != nil:
			return nil, fmt.Errorf("duplicate tool Definition.Name %q", name)
		default:
			agent.toolsByName[name] = configured
		}
	}
	return agent, nil
}

// NewAgent constructs one chat agent from one runtime.AgentSpec.
func (f Factory) NewAgent(_ context.Context, spec agent.AgentSpec) (agent.Agent, error) {
	if spec.ResolveModelRequest != nil {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			name = "chat"
		}
		return &Agent{name: name, resolveModelRequest: spec.ResolveModelRequest, toolResultArtifacts: defaultToolResultArtifactStore()}, nil
	}
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
	chatAgent.deferredTools = spec.DeferredTools
	chatAgent.reasoning = reasoningFromMetadata(spec.Metadata)
	chatAgent.request = spec.Request.WithDefaults(agent.ModelRequestOptions{})
	return chatAgent, nil
}

func (a *Agent) Name() string {
	return a.name
}

func (a *Agent) Run(ctx agent.Context) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		// A request snapshot belongs to this Run, never to the shared factory
		// product. Its tools remain fixed until their response has been executed.
		runAgent := *a
		a := &runAgent
		messages := messagesFromContext(ctx)
		watchdog := newDefaultGenerationWatchdog()
		visibility := tool.NewToolVisibilityForModel(a.tools, a.model)
		a.refreshDeferredTools(&visibility)
		for event := range ctx.Events().All() {
			if event != nil {
				visibility.ApplyDiscoveredToolNames(tool.DiscoveredToolNamesFromMetadata(event.Meta))
			}
			if event != nil && event.Tool != nil {
				visibility.ApplyToolResult(event.Tool.Name, event.Tool.Output)
			}
		}
		for {
			assistantMessage, calls, final, messageID, requestPrefix, ok, err := a.collectCanonicalModelStep(ctx, messages, watchdog, &visibility, func(event *session.Event) bool {
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
				accepted, drainErr := a.drainFinalSubmissions(ctx, &messages, func(event *session.Event) bool {
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
			toolCtx := model.WithProviderRequestMetadata(ctx, model.ProviderRequestMetadata{
				SessionAffinity: ctx.Session().SessionID,
			})
			toolMessages, toolEvents, ok, err := a.executeStepToolCalls(toolCtx, messageID, calls, func(event *session.Event) bool {
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
					a.rememberRequestToolDiscovery(toolEvent)
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
	watchdog *generationWatchdog,
	visibility *tool.ToolVisibility,
	yield func(*session.Event) bool,
) (model.Message, []model.ToolCall, *model.Response, string, prefixusage.Snapshot, bool, error) {
	invalidAttempts := 0
	for {
		if err := ctx.Err(); err != nil {
			return model.Message{}, nil, nil, "", prefixusage.Snapshot{}, true, err
		}
		if err := a.refreshModelRequest(ctx, visibility); err != nil {
			return model.Message{}, nil, nil, "", prefixusage.Snapshot{}, true, err
		}
		a.refreshDeferredTools(visibility)
		stream := a.request.StreamEnabled(false)
		messageID := uuid.NewString()
		request := &model.Request{
			Messages:    messages,
			Tools:       visibility.ModelSpecs(),
			Reasoning:   a.reasoning,
			Output:      a.request.OutputSpec(),
			ServiceTier: a.request.ServiceTier,
			Stream:      stream,
		}
		if a.resolveModelRequest != nil {
			request.Instructions = model.CloneParts(a.instructions)
		} else {
			request.Instructions = instructionsFromContext(ctx, a.systemPrompt)
		}

		modelCtx := model.WithProviderRequestMetadata(ctx, model.ProviderRequestMetadata{
			SessionAffinity: ctx.Session().SessionID,
		})
		var receipts []model.Invocation
		modelCtx = model.WithInvocationObserver(modelCtx, func(in model.Invocation) { receipts = append(receipts, in) })
		if admit := a.admitModelRequest; admit != nil {
			modelCtx = model.WithInvocationAdmission(modelCtx, func(ctx context.Context, _ *model.Request) error {
				return admit(ctx, agent.ModelRequestAdmission{RequestID: uuid.NewString()})
			})
		}
		final, err := collectFinalResponse(modelCtx, a.model, request, messageID, watchdog, yield)
		for _, receipt := range receipts {
			if yield != nil && !yield(session.NewModelInvocationReceipt(receipt, "chat")) {
				return model.Message{}, nil, nil, "", prefixusage.Snapshot{}, false, nil
			}
		}
		if err != nil {
			if a.resolveModelRequest != nil && errors.Is(err, agent.ErrModelRequestSnapshotStale) {
				watchdog.resetAll()
				continue
			}
			return model.Message{}, nil, nil, "", prefixusage.Snapshot{}, true, err
		}
		final.Message = normalizeAssistantCitations(final.Message, messages)

		assistantMessage, calls, err := validateAssistantToolCalls(final.Message)
		if err == nil {
			return assistantMessage, calls, final, messageID, prefixusage.ForRequest(request), true, nil
		}
		if invalidAttempts >= maxInvalidToolCallRepairAttempts {
			return model.Message{}, nil, nil, "", prefixusage.Snapshot{}, true, err
		}
		invalidAttempts++
		if reset := invalidToolCallAttemptResetEvent(invalidAttempts); reset != nil {
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
	if !a.canExecuteStepToolCallsConcurrently(calls, visibility) {
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

func (a *Agent) canExecuteStepToolCallsConcurrently(calls []model.ToolCall, visibility *tool.ToolVisibility) bool {
	if len(calls) < 2 {
		return false
	}
	for _, call := range calls {
		item, ok := a.lookupRunTool(call.Name, visibility)
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
	return a.drainSubmissions(ctx, ctx.DrainSubmissions(), messages, yield)
}

func (a *Agent) drainFinalSubmissions(ctx agent.Context, messages *[]model.Message, yield func(*session.Event) bool) (bool, error) {
	drainer, ok := ctx.(runtimeinput.FinalSubmissionDrainer)
	if !ok {
		return a.drainPendingSubmissions(ctx, messages, yield)
	}
	for {
		drained := drainer.DrainFinalSubmissions()
		accepted, err := a.drainSubmissions(ctx, drained, messages, yield)
		if accepted || err != nil || len(drained) == 0 {
			return accepted, err
		}
		// Ignored or empty submissions do not continue the model loop. Drain
		// again so a concurrent valid input is admitted or closure is atomic.
	}
}

func (a *Agent) drainSubmissions(ctx agent.Context, drained []agent.Submission, messages *[]model.Message, yield func(*session.Event) bool) (bool, error) {
	accepted := false
	for _, submission := range drained {
		if len(submission.Inputs) > 0 {
			if err := agent.ValidateSubmissionInputs(submission); err != nil {
				return accepted, err
			}
			committer, ok := ctx.(runtimeinput.BatchCommitter)
			if !ok {
				return accepted, fmt.Errorf("Agent communication batch requires Runtime safe-point persistence")
			}
			committed, err := committer.CommitAgentInputBatch(submission.Inputs)
			if err != nil {
				return accepted, err
			}
			*messages = append(*messages, committed...)
			accepted = accepted || len(committed) > 0
			continue
		}
		if !isModelInputSubmission(submission) {
			continue
		}
		text := strings.TrimSpace(submission.Text)
		if text == "" && len(submission.ContentParts) == 0 {
			continue
		}
		message, displayText, meta := userdisplay.Resolve(text, submission.DisplayInput, submission.ContentParts, submission.Metadata)
		eventType := session.EventTypeUser
		actor := session.ActorRef{Kind: session.ActorKindUser, Name: "user"}
		if submission.Kind == runtimeinput.ModelContext || submission.Kind == agent.SubmissionKindAgentCommunication {
			if !session.ActorRefHasIdentity(submission.Actor) {
				return accepted, fmt.Errorf("context submission requires source identity")
			}
			eventType = session.EventTypeContext
			actor = session.CloneActorRef(submission.Actor)
		} else if session.ActorRefHasIdentity(submission.Actor) {
			actor = session.CloneActorRef(submission.Actor)
		}
		event := &session.Event{
			Type:       eventType,
			Visibility: session.VisibilityCanonical,
			Actor:      actor,
			Message:    &message,
			Text:       displayText,
			Meta:       meta,
		}
		providerMessage := message
		if eventType == session.EventTypeUser {
			event.Protocol = &session.EventProtocol{Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeUserMessage),
				Content:       session.ProtocolTextContent(displayText),
			}}
		} else if submission.Kind == agent.SubmissionKindAgentCommunication {
			prepared, err := agentcommunication.AppendSender(message, actor)
			if err != nil {
				return accepted, fmt.Errorf("submit model context: %w", err)
			}
			providerMessage = prepared
			event.Message = &providerMessage
			protocol := session.NewAgentCommunicationProtocol(session.ProtocolAgentCommunication{Text: displayText})
			event.Protocol = &protocol
		}
		if !yield(event) {
			return accepted, nil
		}
		*messages = append(*messages, providerMessage)
		accepted = true
	}
	return accepted, nil
}

func isModelInputSubmission(sub agent.Submission) bool {
	switch sub.Kind {
	case agent.SubmissionKindConversation, agent.SubmissionKindAgentCommunication, runtimeinput.ModelContext:
		return true
	default:
		return false
	}
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

func (a *Agent) refreshDeferredTools(visibility *tool.ToolVisibility) {
	if a.deferredTools != nil {
		visibility.RefreshDeferredTools(a.deferredTools.Tools(), a.model)
	}
}
