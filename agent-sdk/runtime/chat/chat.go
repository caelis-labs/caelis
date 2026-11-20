package chat

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/internal/runtimeinput"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/mcpdiscovery"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/prefixusage"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/toolset"
	"github.com/google/uuid"
)

// SupportsLoopHooks declares that this factory owns the typed chat phases.
func (Factory) SupportsLoopHooks() {}

// Factory constructs baseline chat agents from one AgentSpec.
type Factory struct {
	SystemPrompt string
}

// Agent is the minimal model-backed chat agent.
type Agent struct {
	validateModelRequest func(context.Context) (func(), error)
	models               map[string]model.LLM
	modelRevision        string
	preparedCalls        map[string]preparedToolCall
	yieldedResults       map[*session.Event]bool
	outputStream         *textStream

	name                string
	model               model.LLM
	tools               []tool.Tool
	deferredTools       toolset.Source
	toolsByName         map[string]tool.Tool
	systemPrompt        string
	reasoning           model.ReasoningConfig
	request             agent.ModelRequestOptions
	toolResultArtifacts *toolResultArtifactStore
	resolveModelRequest agent.ModelRequestResolver
	instructions        []model.Part
	admitModelRequest   func(context.Context, agent.ModelRequestAdmission) error
	releaseModelRequest func()
	discoveries         map[string]tool.Binding
	boundCalls          map[string]boundMCPCall

	resolveSessionInstructions agent.SessionInstructionsResolver
	summaryID                  string
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
		return &Agent{name: name, models: spec.Models, resolveModelRequest: spec.ResolveModelRequest,
			resolveSessionInstructions: spec.ResolveSessionInstructions, toolResultArtifacts: defaultToolResultArtifactStore()}, nil
	}
	systemPrompt := ""
	if raw, ok := spec.Metadata["system_prompt"].(string); ok {
		systemPrompt = strings.TrimSpace(raw)
	}
	if systemPrompt == "" {
		systemPrompt = strings.TrimSpace(f.SystemPrompt)
	}
	if catalog, _ := spec.Metadata[codemode.MetadataCatalog].(string); catalog != "" {
		systemPrompt += "\n\n" + catalog
	}
	chatAgent, err := NewWithTools(spec.Name, spec.Model, spec.Tools, systemPrompt)
	if err != nil {
		return nil, err
	}
	chatAgent.models = spec.Models
	chatAgent.deferredTools = spec.DeferredTools
	chatAgent.reasoning = reasoningFromMetadata(spec.Metadata)
	chatAgent.request = spec.Request.WithDefaults(agent.ModelRequestOptions{})
	chatAgent.resolveSessionInstructions = spec.ResolveSessionInstructions
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
		runAgent.yieldedResults = map[*session.Event]bool{}
		a := &runAgent
		defer func() {
			if a.releaseModelRequest != nil {
				a.releaseModelRequest()
			}
		}()
		messages, historyErr := messagesFromContextChecked(ctx)
		if historyErr != nil {
			yield(nil, historyErr)
			return
		}
		a.discoveries = mcpdiscovery.FromContext(ctx)
		a.summaryID = ""
		events := ctx.Events()
		if a.resolveSessionInstructions != nil && events.Len() > 0 {
			a.summaryID = promptSummaryID(events.At(0))
		}
		for event := range events.All() {
			mcpdiscovery.Apply(a.discoveries, event)
		}
		watchdog := newDefaultGenerationWatchdog()
		visibility := newToolVisibility(a.tools)
		for {
			a.outputStream = nil
			var assistantMessage model.Message
			var calls []model.ToolCall
			var final *model.Response
			var messageID string
			var requestPrefix prefixusage.Snapshot
			ok := true
			var err error
			if early := hookrun.FromContext(ctx).TakeEarly(); early != nil {
				if err = a.refreshModelRequest(ctx); err != nil {
					yield(nil, err)
					return
				}
				evidence := early.Evidence
				evidence.ConfigurationRevision = a.modelRevision
				a.outputStream, err = newTextStream(ctx, evidence)
				if err != nil {
					yield(nil, err)
					return
				}
				assistantMessage = early.Message
				final = &model.Response{Message: early.Message}
				messageID = uuid.NewString()
			} else {
				assistantMessage, calls, final, messageID, requestPrefix, ok, err = a.collectCanonicalModelStep(ctx, &messages, watchdog, &visibility, func(event *session.Event) bool {
					return yield(event, nil)
				})
			}

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
				candidate, feedback, completeErr := a.completeCandidate(ctx, assistantMessage)
				accepted, pendingErr := a.drainPendingSubmissions(ctx, &messages, func(e *session.Event) bool { return yield(e, nil) })
				if pendingErr != nil {
					yield(nil, pendingErr)
					return
				}
				if accepted {
					watchdog.resetAll()
					continue
				}
				if completeErr != nil {
					yield(nil, completeErr)
					return
				}
				if feedback != nil {
					if !yield(feedback, nil) {
						return
					}
					hookrun.FromContext(ctx).CommitFeedback(feedback.ID)
					messages = append(messages, *feedback.Message)
					continue
				}
				accepted, drainErr := a.drainFinalSubmissions(ctx, &messages, func(e *session.Event) bool { return yield(e, nil) })
				if drainErr != nil {
					yield(nil, drainErr)
					return
				}
				if accepted {
					watchdog.resetAll()
					continue
				}
				if err := ctx.Err(); err != nil {
					yield(nil, err)
					return
				}
				candidate, err = a.finishOutput(ctx, candidate, messages)
				if err != nil {
					yield(nil, err)
					return
				}
				final.Message = candidate
				if !yield(modelResponseEvent(candidate, final, messageID, a.model, requestPrefix), nil) {
					return
				}
				return
			}

			assistantMessage, err = a.finishOutput(ctx, assistantMessage, messages)
			if err != nil {
				yield(nil, err)
				return
			}
			if err := a.prepareToolCalls(ctx, calls, &visibility); err != nil {
				yield(nil, err)
				return
			}
			assistantMessage = effectiveAssistantMessage(assistantMessage, calls)
			final.Message = assistantMessage
			toolCallEvents := modelToolCallEvents(assistantMessage, final, messageID, a.model, requestPrefix)
			for _, event := range toolCallEvents {
				a.applyPreparedFact(event)
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
			terminalTool := false
			for _, toolEvent := range toolEvents {
				if !a.yieldedResults[toolEvent] && !yield(toolEvent, nil) {
					return
				}
				// yield returns only after Runtime confirms the canonical append.
				mcpdiscovery.Apply(a.discoveries, toolEvent)
				if toolEvent != nil && toolEvent.Tool != nil && toolEvent.Tool.TurnComplete {
					// The terminal callback receipt is durable before this Run exits.
					// Do not consume queued input or issue another model request.
					terminalTool = true
				}
			}
			if err != nil {
				yield(nil, err)
				return
			}
			if terminalTool {
				return
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
	messages *[]model.Message,
	watchdog *generationWatchdog,
	visibility *toolVisibility,
	yield func(*session.Event) bool,
) (model.Message, []model.ToolCall, *model.Response, string, prefixusage.Snapshot, bool, error) {
	invalidAttempts := 0
	for {
		if err := ctx.Err(); err != nil {
			return model.Message{}, nil, nil, "", prefixusage.Snapshot{}, true, err
		}
		if err := a.refreshModelRequest(ctx); err != nil {
			return model.Message{}, nil, nil, "", prefixusage.Snapshot{}, true, err
		}
		*visibility = newToolVisibility(a.tools)
		stream := a.request.StreamEnabled(false)
		messageID := uuid.NewString()
		request := &model.Request{
			Messages:    *messages,
			Tools:       visibility.modelSpecs(),
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
		if invalidAttempts > 0 {
			// The rejected envelope never entered Session or tool dispatch.
			// Give this regeneration feedback without replaying malformed input.
			request.Instructions = append(request.Instructions, model.NewTextPart(toolCallRepairInstruction))
		}

		run := hookrun.FromContext(ctx)
		logicalID, assembly := run.RequestAssembly(uuid.NewString)
		evidence := run.Evidence
		evidence.ConfigurationRevision = a.modelRevision
		evidence.LogicalRequestID = logicalID
		evidence.AssemblyRevision = assembly
		prepared, cached, prepareErr := a.prepareModel(ctx, evidence, request)
		// ContextEdit updates the same Run context before tools, feedback or a
		// stale assembly can continue. Only the checkpoint owner mutates it.
		*messages = model.CloneMessages(request.Messages)
		if prepareErr != nil {
			if a.resolveModelRequest != nil && errors.Is(prepareErr, agent.ErrModelRequestSnapshotStale) {
				continue
			}
			return model.Message{}, nil, nil, "", prefixusage.Snapshot{}, true, prepareErr
		}
		request = prepared
		var persistInstructions func(context.Context) error
		if cached == nil && a.resolveSessionInstructions != nil {
			instructions, persist, err := a.resolveSessionInstructions(ctx, a.summaryID)
			if err != nil {
				return model.Message{}, nil, nil, "", prefixusage.Snapshot{}, true, err
			}
			if instructions != "" {
				request.Instructions = append(request.Instructions, model.NewTextPart(instructions))
			}
			persistInstructions = persist
		}
		bindRequestSource(ctx, request)
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
		if persistInstructions != nil {
			var once sync.Once
			var persistErr error
			modelCtx = model.WithInvocationStart(modelCtx, func(ctx context.Context, _ model.Invocation, _ *model.Request) error {
				once.Do(func() { persistErr = persistInstructions(ctx) })
				return persistErr
			})
		}
		modelCtx = modelHookContext(modelCtx, evidence)
		final := cached
		var err error
		if final == nil {
			evidence.OutputSource = agent.HookOutputProvider
			final, a.outputStream, err = collectFinalResponse(modelCtx, a.model, request, messageID, watchdog, yield)
		} else {
			evidence.OutputSource = agent.HookOutputCache
			a.outputStream, err = newTextStream(ctx, evidence)
		}
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
		resultEvidence := evidence
		resultEvidence.InvocationID = final.InvocationID
		resultEvidence.AttemptOrdinal = uint64(len(receipts))
		final, err = resultModel(ctx, resultEvidence, final)
		if err != nil {
			return model.Message{}, nil, nil, "", prefixusage.Snapshot{}, true, err
		}
		if hookrun.FromContext(ctx).Hooks.StreamMode != agent.StreamIncremental {
			final.Message = normalizeAssistantCitations(final.Message, *messages)
		}

		assistantMessage, calls, err := validateAssistantToolCalls(final.Message)
		if err == nil {
			run.FinishRequest(logicalID)
			return assistantMessage, calls, final, messageID, prefixusage.ForRequest(request), true, nil
		}
		if invalidAttempts >= maxInvalidToolCallRepairAttempts {
			return model.Message{}, nil, nil, "", prefixusage.Snapshot{}, true, err
		}
		invalidAttempts++
		run.FinishRequest(logicalID)
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
		drained, err := drainer.DrainFinalSubmissions()
		if err != nil {
			return false, err
		}
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
		if committer, ok := ctx.(runtimeinput.SubmissionCommitter); ok && (len(submission.Inputs) > 0 || isModelInputSubmission(submission)) {
			committed, err := committer.CommitSubmission(submission)
			if err != nil {
				return accepted, err
			}
			*messages = append(*messages, committed...)
			accepted = accepted || len(committed) > 0
			continue
		}
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
		event, err := runtimeinput.NewEvent(submission.Kind, agent.AgentCommunicationInput{
			Source: submission.Actor, Input: submission.Text, DisplayInput: submission.DisplayInput, ContentParts: submission.ContentParts,
		}, submission.Metadata)
		if err != nil {
			return accepted, err
		}
		if event == nil {
			continue
		}
		if !yield(event) {
			return accepted, nil
		}
		*messages = append(*messages, *event.Message)
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
