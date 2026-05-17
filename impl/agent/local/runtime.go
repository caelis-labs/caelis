package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	acpcontroller "github.com/OnslaughtSnail/caelis/impl/agent/acp/controller"
	acpsubagent "github.com/OnslaughtSnail/caelis/impl/agent/acp/subagent"
	"github.com/OnslaughtSnail/caelis/impl/policy/presets"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/plan"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/policy"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	"github.com/OnslaughtSnail/caelis/ports/subagent"
	"github.com/OnslaughtSnail/caelis/ports/task"
)

const overflowCompactionRecoveryLimit = 3

// Config defines one baseline local runtime instance.
type Config struct {
	Sessions          session.Service
	AgentFactory      agent.AgentFactory
	RunIDGenerator    func() string
	Clock             func() time.Time
	Compaction        CompactionConfig
	Compactor         compact.Engine
	PolicyRegistry    policy.Registry
	DefaultPolicyMode string
	Assembly          assembly.ResolvedAssembly
	Controllers       controller.Backend
	TaskStore         task.Store
	Subagents         subagent.Runner
}

// Runtime is the baseline local runtime implementation.
type Runtime struct {
	sessions          session.Service
	agentFactory      agent.AgentFactory
	runIDGenerator    func() string
	clock             func() time.Time
	compaction        CompactionConfig
	compactor         compact.Engine
	policies          policy.Registry
	defaultPolicyMode string
	assembly          assembly.ResolvedAssembly
	acpRegistry       *acpsubagent.Registry
	controllers       controller.Backend
	subagents         subagent.Runner
	idCounter         atomic.Uint64
	mu                sync.RWMutex
	runStates         map[string]agent.RunState
	permissionGrants  map[string]*permissionGrantStore
	tasks             *taskRuntime
	terminals         *streamService
}

// New returns one baseline local runtime.
func New(cfg Config) (*Runtime, error) {
	if cfg.Sessions == nil {
		return nil, errors.New("impl/agent/local: sessions service is required")
	}
	if cfg.AgentFactory == nil {
		return nil, errors.New("impl/agent/local: agent factory is required")
	}
	r := &Runtime{
		sessions:          cfg.Sessions,
		agentFactory:      cfg.AgentFactory,
		runIDGenerator:    cfg.RunIDGenerator,
		clock:             cfg.Clock,
		compaction:        normalizeCompactionConfig(cfg.Compaction),
		policies:          cfg.PolicyRegistry,
		defaultPolicyMode: strings.TrimSpace(cfg.DefaultPolicyMode),
		assembly:          assembly.CloneResolvedAssembly(cfg.Assembly),
		controllers:       cfg.Controllers,
		subagents:         cfg.Subagents,
		runStates:         map[string]agent.RunState{},
		permissionGrants:  map[string]*permissionGrantStore{},
	}
	if r.clock == nil {
		r.clock = time.Now
	}
	if r.policies == nil {
		reg, err := presets.NewRegistry()
		if err != nil {
			return nil, err
		}
		r.policies = reg
	}
	if r.defaultPolicyMode == "" {
		r.defaultPolicyMode = presets.ModeAutoReview
	}
	r.defaultPolicyMode = normalizePolicyMode(r.defaultPolicyMode)
	if err := validateControlPlaneConfig(cfg); err != nil {
		return nil, err
	}
	if err := r.applyAssembly(cfg.Assembly, cfg); err != nil {
		return nil, err
	}
	r.compactor = cfg.Compactor
	if r.compactor == nil {
		r.compactor = newCodexStyleCompactor(r.compaction)
	}
	r.tasks = newTaskRuntime(r, cfg.TaskStore)
	r.terminals = newStreamService(r.tasks)
	return r, nil
}

func (r *Runtime) applyAssembly(resolved assembly.ResolvedAssembly, cfg Config) error {
	resolved = assembly.CloneResolvedAssembly(resolved)
	if len(resolved.Agents) == 0 {
		r.controllers = cfg.Controllers
		r.subagents = cfg.Subagents
		return nil
	}

	registry, err := acpsubagent.NewRegistry(resolved.Agents)
	if err != nil {
		return err
	}
	r.acpRegistry = registry
	runner, err := acpsubagent.NewRunner(acpsubagent.RunnerConfig{Registry: registry})
	if err != nil {
		return err
	}
	manager, err := acpcontroller.NewManager(acpcontroller.Config{Registry: registry})
	if err != nil {
		return err
	}

	if cfg.Subagents != nil {
		r.subagents = cfg.Subagents
	} else {
		r.subagents = runner
	}
	if cfg.Controllers != nil {
		r.controllers = cfg.Controllers
	} else {
		r.controllers = manager
	}
	return nil
}

func (r *Runtime) UpdateACPAgents(agents []assembly.AgentConfig) error {
	if r == nil {
		return fmt.Errorf("impl/agent/local: runtime is unavailable")
	}
	resolved := assembly.ResolvedAssembly{Agents: append([]assembly.AgentConfig(nil), agents...)}
	registry := r.acpRegistry
	if registry == nil {
		return fmt.Errorf("impl/agent/local: ACP registry is not configured")
	}
	if err := registry.Replace(resolved.Agents); err != nil {
		return err
	}
	r.mu.Lock()
	r.assembly.Agents = assembly.CloneResolvedAssembly(resolved).Agents
	r.mu.Unlock()
	return nil
}

func validateControlPlaneConfig(cfg Config) error {
	if len(cfg.Assembly.Agents) == 0 {
		return nil
	}
	if cfg.Controllers != nil || cfg.Subagents != nil {
		return errors.New("impl/agent/local: Assembly.Agents cannot be combined with explicit Controllers or Subagents")
	}
	return nil
}

// Terminals returns the unified terminal read/subscribe surface for this
// runtime. Task control remains on the TASK tool plane.
func (r *Runtime) Streams() stream.Service {
	if r == nil {
		return nil
	}
	return r.terminals
}

// Run executes one agent turn for one existing session.
func (r *Runtime) Run(
	ctx context.Context,
	req agent.RunRequest,
) (agent.RunResult, error) {
	ref := session.NormalizeSessionRef(req.SessionRef)
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return agent.RunResult{}, err
	}
	activeSession, err = r.ensureSessionController(ctx, activeSession)
	if err != nil {
		return agent.RunResult{}, err
	}
	if activeSession.Controller.Kind == session.ControllerKindACP {
		return r.runACPControllerTurn(ctx, activeSession, ref, req)
	}

	runID := r.nextID("run", r.runIDGenerator)
	turnID := r.nextID("turn", nil)
	r.setRunState(ref.SessionID, agent.RunState{
		Status:      agent.RunLifecycleStatusRunning,
		ActiveRunID: runID,
		UpdatedAt:   r.now(),
	})
	runCtx, cancel := context.WithCancel(ctx)
	handle := newRunner(runID, cancel)
	go r.executeKernelTurn(runCtx, activeSession, ref, runID, turnID, req, handle)
	return agent.RunResult{
		Session: activeSession,
		Handle:  handle,
	}, nil
}

func (r *Runtime) executeKernelTurn(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	runID string,
	turnID string,
	req agent.RunRequest,
	handle *runner,
) {
	defer handle.finish()

	batch := make([]*session.Event, 0, 4)
	userEvent := buildUserEvent(activeSession, turnID, req.Input, req.ContentParts)
	if err := r.runWithOverflowRecovery(ctx, activeSession, ref, runID, turnID, req, userEvent, &batch, handle); err != nil {
		stateErr := err
		if replayErr := r.persistInterruptedAssistantReplay(context.WithoutCancel(ctx), activeSession, ref, turnID, batch, err); replayErr != nil {
			stateErr = errors.Join(err, replayErr)
		}
		r.setRunState(ref.SessionID, agent.RunState{
			Status:      interruptedOrFailedStatus(ctx, err),
			ActiveRunID: runID,
			LastError:   stateErr.Error(),
			UpdatedAt:   r.now(),
		})
		handle.publishError(err)
		return
	}
	r.setRunState(ref.SessionID, agent.RunState{
		Status:      agent.RunLifecycleStatusCompleted,
		ActiveRunID: runID,
		UpdatedAt:   r.now(),
	})
}

func (r *Runtime) persistInterruptedAssistantReplay(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	turnID string,
	events []*session.Event,
	cause error,
) error {
	if r == nil || r.sessions == nil {
		return nil
	}
	event := interruptedAssistantReplayEvent(activeSession, turnID, events, cause)
	if event == nil {
		return nil
	}
	_, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: ref,
		Event:      event,
	})
	return err
}

func interruptedAssistantReplayEvent(
	activeSession session.Session,
	turnID string,
	events []*session.Event,
	cause error,
) *session.Event {
	turnID = strings.TrimSpace(turnID)
	var answer strings.Builder
	var reasoning strings.Builder
	var template *session.Event
	for _, event := range events {
		if !mainTurnEvent(event, turnID) || session.EventTypeOf(event) != session.EventTypeAssistant {
			continue
		}
		if session.IsCanonicalHistoryEvent(event) && assistantEventHasReplayText(event) {
			return nil
		}
		if event.Visibility != session.VisibilityUIOnly {
			continue
		}
		updateType := assistantReplayUpdateType(event)
		text := narrativeEventText(event, updateType)
		if text == "" {
			continue
		}
		template = session.CloneEvent(event)
		switch updateType {
		case string(session.ProtocolUpdateTypeAgentThought):
			reasoning.WriteString(text)
		default:
			answer.WriteString(text)
		}
	}
	answerText := answer.String()
	reasoningText := reasoning.String()
	if strings.TrimSpace(answerText) == "" && strings.TrimSpace(reasoningText) == "" {
		return nil
	}
	if template == nil {
		return nil
	}
	return buildInterruptedAssistantReplayEvent(template, activeSession, turnID, answerText, reasoningText, cause)
}

func buildInterruptedAssistantReplayEvent(
	template *session.Event,
	activeSession session.Session,
	turnID string,
	answerText string,
	reasoningText string,
	cause error,
) *session.Event {
	event := session.CloneEvent(template)
	if event == nil {
		event = &session.Event{}
	}
	event.ID = ""
	event.Type = session.EventTypeAssistant
	event.Visibility = session.VisibilityMirror
	if event.Scope == nil {
		scope := defaultScope(activeSession, turnID)
		event.Scope = &scope
	} else {
		scope := *event.Scope
		if strings.TrimSpace(scope.TurnID) == "" {
			scope.TurnID = strings.TrimSpace(turnID)
		}
		if scope.Controller.Kind == "" {
			scope.Controller = defaultControllerRef(activeSession)
		}
		event.Scope = &scope
	}
	if event.Actor.Kind == "" {
		event.Actor = session.ActorRef{Kind: session.ActorKindController}
	}
	message := model.MessageFromAssistantParts(answerText, reasoningText, nil)
	event.Message = &message
	event.Text = message.TextContent()
	updateType := string(session.ProtocolUpdateTypeAgentMessage)
	content := interruptedAssistantReplayContent(answerText, reasoningText)
	if strings.TrimSpace(answerText) == "" {
		updateType = string(session.ProtocolUpdateTypeAgentThought)
		content = interruptedAssistantReplayContent("", reasoningText)
	}
	event.Protocol = &session.EventProtocol{
		UpdateType: updateType,
		Update: &session.ProtocolUpdate{
			SessionUpdate: updateType,
			Content:       content,
		},
	}
	event.Meta = interruptedReplayMeta(event.Meta, cause, reasoningText)
	return event
}

func interruptedAssistantReplayContent(answerText string, reasoningText string) map[string]any {
	answerText = strings.TrimSpace(answerText)
	reasoningText = strings.TrimSpace(reasoningText)
	if answerText != "" {
		return session.ProtocolTextContent(answerText)
	}
	if reasoningText != "" {
		return session.ProtocolTextContent(reasoningText)
	}
	return nil
}

func mainTurnEvent(event *session.Event, turnID string) bool {
	if event == nil || event.Scope == nil {
		return false
	}
	if strings.TrimSpace(event.Scope.TurnID) != strings.TrimSpace(turnID) {
		return false
	}
	return strings.TrimSpace(event.Scope.Participant.ID) == ""
}

func assistantEventHasReplayText(event *session.Event) bool {
	if event == nil {
		return false
	}
	if strings.TrimSpace(session.EventText(event)) != "" {
		return true
	}
	if event.Message != nil && strings.TrimSpace(event.Message.ReasoningText()) != "" {
		return true
	}
	return false
}

func assistantReplayUpdateType(event *session.Event) string {
	if event != nil && event.Protocol != nil {
		if updateType := strings.TrimSpace(event.Protocol.UpdateType); updateType != "" {
			return updateType
		}
		if event.Protocol.Update != nil {
			if updateType := strings.TrimSpace(event.Protocol.Update.SessionUpdate); updateType != "" {
				return updateType
			}
		}
	}
	return string(session.ProtocolUpdateTypeAgentMessage)
}

func interruptedReplayMeta(meta map[string]any, cause error, reasoningText string) map[string]any {
	out := maps.Clone(meta)
	if out == nil {
		out = map[string]any{}
	}
	caelis, _ := out["caelis"].(map[string]any)
	caelis = maps.Clone(caelis)
	if caelis == nil {
		caelis = map[string]any{}
	}
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	runtimeMeta = maps.Clone(runtimeMeta)
	if runtimeMeta == nil {
		runtimeMeta = map[string]any{}
	}
	replay := map[string]any{"interrupted": true}
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		replay["reason"] = cause.Error()
	}
	if reasoningText = strings.TrimSpace(reasoningText); reasoningText != "" {
		replay["reasoning_text"] = reasoningText
	}
	runtimeMeta["replay"] = replay
	caelis["runtime"] = runtimeMeta
	out["caelis"] = caelis
	return out
}

// RunState returns the last known run state for one session.
func (r *Runtime) RunState(
	_ context.Context,
	ref session.SessionRef,
) (agent.RunState, error) {
	ref = session.NormalizeSessionRef(ref)
	r.mu.RLock()
	defer r.mu.RUnlock()
	state, ok := r.runStates[ref.SessionID]
	if !ok {
		return agent.RunState{}, session.ErrSessionNotFound
	}
	return state, nil
}

func (r *Runtime) resolveAgent(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	state map[string]any,
	runID string,
	turnID string,
	req agent.RunRequest,
) (agent.Agent, error) {
	if req.Agent != nil {
		return req.Agent, nil
	}
	spec := r.applyAssemblySpec(state, req.AgentSpec)
	spec.Request = req.Request.WithDefaults(spec.Request)
	modeName, _ := r.policyForName(ctx, r.policyMode(spec))
	grants := r.permissionGrantStoreForSession(ref)
	if err := r.hydratePermissionGrantStore(ctx, ref, grants); err != nil {
		return nil, err
	}
	spec.Tools = r.wrapToolsForRuntime(activeSession, ref, spec, runtimeToolContext{
		mode:              modeName,
		approvalRequester: req.ApprovalRequester,
		runID:             strings.TrimSpace(runID),
		turnID:            strings.TrimSpace(turnID),
		now:               r.now,
		grants:            grants,
	})
	spec.Tools = r.wrapToolsForPolicy(activeSession, ref, state, spec, approvalContext{
		ctx:        ctx,
		requester:  req.ApprovalRequester,
		runtime:    r,
		session:    session.CloneSession(activeSession),
		sessionRef: session.NormalizeSessionRef(ref),
		runID:      strings.TrimSpace(runID),
		turnID:     strings.TrimSpace(turnID),
		grants:     grants,
	})
	return r.agentFactory.NewAgent(ctx, spec)
}

func (r *Runtime) permissionGrantStoreForSession(ref session.SessionRef) *permissionGrantStore {
	if r == nil {
		return newPermissionGrantStore()
	}
	ref = session.NormalizeSessionRef(ref)
	sessionID := strings.TrimSpace(ref.SessionID)
	if sessionID == "" {
		return newPermissionGrantStore()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.permissionGrants == nil {
		r.permissionGrants = map[string]*permissionGrantStore{}
	}
	store := r.permissionGrants[sessionID]
	if store == nil {
		store = newPermissionGrantStore()
		r.permissionGrants[sessionID] = store
	}
	return store
}

func (r *Runtime) PermissionGrantSnapshot(ref session.SessionRef) PermissionGrantSnapshot {
	if r == nil {
		return PermissionGrantSnapshot{}
	}
	ref = session.NormalizeSessionRef(ref)
	sessionID := strings.TrimSpace(ref.SessionID)
	if sessionID == "" {
		return PermissionGrantSnapshot{}
	}
	r.mu.RLock()
	store := r.permissionGrants[sessionID]
	r.mu.RUnlock()
	if store == nil {
		return PermissionGrantSnapshot{}
	}
	return store.snapshot()
}

func (r *Runtime) hydratePermissionGrantStore(ctx context.Context, ref session.SessionRef, store *permissionGrantStore) error {
	if r == nil || r.sessions == nil || store == nil {
		return nil
	}
	state, err := r.sessions.SnapshotState(ctx, ref)
	if err != nil {
		return err
	}
	store.hydrate(permissionGrantRecordsFromState(state[permissionGrantStateKey]))
	return nil
}

func (r *Runtime) setRunState(sessionID string, state agent.RunState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runStates[strings.TrimSpace(sessionID)] = state
}

func (r *Runtime) runWithOverflowRecovery(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	runID string,
	turnID string,
	req agent.RunRequest,
	pendingInput *session.Event,
	batch *[]*session.Event,
	sink *runner,
) error {
	overflowRecoveries := 0
	for {
		attemptBatch, _, inputPersisted, err := r.runAttempt(ctx, activeSession, ref, runID, turnID, req, pendingInput, sink)
		if inputPersisted {
			pendingInput = nil
		}
		if err == nil {
			*batch = append(*batch, attemptBatch...)
			return nil
		}
		if isCompactionOverflowError(err) {
			*batch = append(*batch, attemptBatch...)
			if overflowRecoveries >= overflowCompactionRecoveryLimit {
				return fmt.Errorf("impl/agent/local: context overflow persisted after %d compaction recoveries: %w", overflowCompactionRecoveryLimit, err)
			}
			compacted, compactErr := r.compactAfterOverflow(ctx, activeSession, ref, req, err)
			if compactErr != nil {
				return compactErr
			}
			if !compacted {
				return err
			}
			overflowRecoveries++
			continue
		}
		*batch = append(*batch, attemptBatch...)
		return err
	}
}

func (r *Runtime) runAttempt(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	runID string,
	turnID string,
	req agent.RunRequest,
	pendingInput *session.Event,
	sink *runner,
) ([]*session.Event, bool, bool, error) {
	events, state, err := r.prepareInvocationContext(ctx, activeSession, ref, req, pendingInput)
	if err != nil {
		return nil, false, false, err
	}

	batch := make([]*session.Event, 0, 3)
	inputPersisted := false
	if pendingInput != nil {
		persisted, appendErr := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef: ref,
			Event:      pendingInput,
		})
		if appendErr != nil {
			return nil, false, false, appendErr
		}
		batch = append(batch, persisted)
		events = append(events, session.CloneEvent(persisted))
		inputPersisted = true
		if sink != nil {
			sink.publishEvent(persisted)
		}
	}

	activeAgent, err := r.resolveAgent(ctx, activeSession, ref, state, runID, turnID, req)
	if err != nil {
		return batch, false, inputPersisted, err
	}
	var drainSubmissions func() []agent.Submission
	if sink != nil {
		drainSubmissions = sink.drainSubmissions
	}
	runCtx := agent.NewContext(agent.ContextSpec{
		Context:          ctx,
		Session:          activeSession,
		Events:           events,
		State:            state,
		DrainSubmissions: drainSubmissions,
	})

	emitted := false
	for event, runErr := range activeAgent.Run(runCtx) {
		if runErr != nil {
			return batch, emitted, inputPersisted, runErr
		}
		if event == nil {
			continue
		}
		emitted = true
		normalized := normalizeEvent(activeSession, turnID, event)
		if session.IsCanonicalHistoryEvent(normalized) {
			normalized, err = r.sessions.AppendEvent(ctx, session.AppendEventRequest{
				SessionRef: ref,
				Event:      normalized,
			})
			if err != nil {
				return batch, emitted, inputPersisted, err
			}
		}
		batch = append(batch, session.CloneEvent(normalized))
		if sink != nil {
			sink.publishEvent(normalized)
		}
		if planEvent, handled, planErr := r.handlePlanEvent(ctx, ref, turnID, normalized); planErr != nil {
			return batch, emitted, inputPersisted, planErr
		} else if handled {
			batch = append(batch, session.CloneEvent(planEvent))
			if sink != nil {
				sink.publishEvent(planEvent)
			}
		}
	}
	if err := r.updateCompactionUsageFromBatch(ctx, ref, batch); err != nil {
		return batch, emitted, inputPersisted, err
	}
	return batch, emitted, inputPersisted, nil
}

func (r *Runtime) nextID(prefix string, custom func() string) string {
	if custom != nil {
		if id := strings.TrimSpace(custom()); id != "" {
			return id
		}
	}
	n := r.idCounter.Add(1)
	return fmt.Sprintf("%s-%d", prefix, n)
}

func (r *Runtime) now() time.Time {
	return r.clock()
}

func buildUserEvent(activeSession session.Session, turnID string, input string, parts []model.ContentPart) *session.Event {
	if strings.TrimSpace(input) == "" && len(parts) == 0 {
		return nil
	}
	message := model.MessageFromTextAndContentParts(model.RoleUser, strings.TrimSpace(input), parts)
	return &session.Event{
		Type:       session.EventTypeUser,
		Visibility: session.VisibilityCanonical,
		Actor:      session.ActorRef{Kind: session.ActorKindUser, Name: "user"},
		Scope:      ptrScope(defaultScope(activeSession, turnID)),
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeUserMessage),
				Content:       session.ProtocolTextContent(message.TextContent()),
			},
		},
		Message: &message,
		Text:    message.TextContent(),
	}
}

func normalizeEvent(activeSession session.Session, turnID string, event *session.Event) *session.Event {
	event = session.CloneEvent(event)
	if event == nil {
		return nil
	}
	if event.Type == "" {
		event.Type = session.EventTypeOf(event)
	}
	if event.Visibility == "" {
		event.Visibility = session.VisibilityCanonical
	}
	if event.Text == "" && event.Message != nil {
		event.Text = event.Message.TextContent()
	}
	if event.Protocol == nil && event.Message == nil {
		switch session.EventTypeOf(event) {
		case session.EventTypeUser:
			event.Protocol = &session.EventProtocol{Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeUserMessage),
				Content:       session.ProtocolTextContent(event.Text),
			}}
		case session.EventTypeAssistant:
			event.Protocol = &session.EventProtocol{Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
				Content:       session.ProtocolTextContent(event.Text),
			}}
		}
	} else if event.Protocol != nil && event.Message == nil {
		protocol := session.CloneEventProtocol(*event.Protocol)
		if protocol.Update != nil && protocol.Update.Content == nil {
			switch session.EventTypeOf(event) {
			case session.EventTypeUser:
				protocol.Update.SessionUpdate = string(session.ProtocolUpdateTypeUserMessage)
				protocol.Update.Content = session.ProtocolTextContent(event.Text)
			case session.EventTypeAssistant:
				protocol.Update.SessionUpdate = firstNonEmpty(protocol.Update.SessionUpdate, string(session.ProtocolUpdateTypeAgentMessage))
				protocol.Update.Content = session.ProtocolTextContent(event.Text)
			}
			event.Protocol = &protocol
		}
	}
	if strings.TrimSpace(event.SessionID) == "" {
		event.SessionID = strings.TrimSpace(activeSession.SessionID)
	}
	if event.Scope == nil {
		scope := defaultScope(activeSession, turnID)
		event.Scope = &scope
	}
	if event.Scope.TurnID == "" {
		event.Scope.TurnID = strings.TrimSpace(turnID)
	}
	if event.Scope.Controller.Kind == "" {
		event.Scope.Controller = defaultControllerRef(activeSession)
	}
	if event.Actor.Kind == "" {
		event.Actor = defaultActorForEvent(event)
	}
	return event
}

func defaultScope(activeSession session.Session, turnID string) session.EventScope {
	return session.EventScope{
		TurnID:     strings.TrimSpace(turnID),
		Controller: defaultControllerRef(activeSession),
	}
}

func defaultControllerRef(activeSession session.Session) session.ControllerRef {
	binding := session.CloneControllerBinding(activeSession.Controller)
	kind := binding.Kind
	if kind == "" {
		kind = session.ControllerKindKernel
	}
	return session.ControllerRef{
		Kind:    kind,
		ID:      binding.ControllerID,
		EpochID: binding.EpochID,
	}
}

func defaultActorForEvent(event *session.Event) session.ActorRef {
	switch session.EventTypeOf(event) {
	case session.EventTypeUser:
		return session.ActorRef{Kind: session.ActorKindUser, Name: "user"}
	case session.EventTypeToolResult:
		name := ""
		if event.Message != nil {
			if parts := event.Message.ToolResults(); len(parts) > 0 {
				name = parts[0].Name
			}
		}
		return session.ActorRef{Kind: session.ActorKindTool, Name: strings.TrimSpace(name)}
	case session.EventTypeNotice, session.EventTypeLifecycle, session.EventTypeSystem:
		return session.ActorRef{Kind: session.ActorKindSystem, Name: "runtime"}
	default:
		return session.ActorRef{Kind: session.ActorKindController}
	}
}

func ptrScope(scope session.EventScope) *session.EventScope {
	return &scope
}

func (r *Runtime) handlePlanEvent(
	ctx context.Context,
	ref session.SessionRef,
	turnID string,
	event *session.Event,
) (*session.Event, bool, error) {
	entries, explanation, ok := planEntriesFromEvent(event)
	if !ok {
		return nil, false, nil
	}
	if err := r.sessions.UpdateState(ctx, ref, func(state map[string]any) (map[string]any, error) {
		if state == nil {
			state = map[string]any{}
		}
		state["plan"] = map[string]any{
			"version":     1,
			"entries":     entriesToState(entries),
			"explanation": explanation,
		}
		return state, nil
	}); err != nil {
		return nil, true, err
	}
	planEvent := &session.Event{
		Type:       session.EventTypePlan,
		Visibility: session.VisibilityCanonical,
		Actor:      session.ActorRef{Kind: session.ActorKindController},
		Scope: &session.EventScope{
			TurnID: strings.TrimSpace(turnID),
			Source: "tool_result",
		},
		Protocol: &session.EventProtocol{
			UpdateType: string(session.ProtocolUpdateTypePlan),
			Plan: &session.ProtocolPlan{
				Entries: entriesToProtocol(entries),
			},
		},
		Text: strings.TrimSpace(explanation),
	}
	normalized := normalizeEvent(session.Session{}, turnID, planEvent)
	normalized.Scope.Controller = event.Scope.Controller
	persisted, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: ref,
		Event:      normalized,
	})
	if err != nil {
		return nil, true, err
	}
	return persisted, true, nil
}

func planEntriesFromEvent(event *session.Event) ([]plan.Entry, string, bool) {
	if event == nil {
		return nil, "", false
	}
	name := strings.TrimSpace(planToolNameFromEvent(event))
	if name == "" && event.Message != nil {
		if results := event.Message.ToolResults(); len(results) > 0 {
			name = strings.TrimSpace(results[0].Name)
		}
	}
	if !strings.EqualFold(name, plan.ToolName) {
		return nil, "", false
	}

	payload := map[string]any{}
	if event.Tool != nil && len(event.Tool.Output) > 0 {
		payload = maps.Clone(event.Tool.Output)
	}
	if len(payload) == 0 {
		if update := session.ProtocolUpdateOf(event); update != nil && len(update.RawOutput) > 0 {
			payload = maps.Clone(update.RawOutput)
		}
	}
	if len(payload) == 0 && event.Message != nil {
		results := event.Message.ToolResults()
		if len(results) == 0 {
			return nil, "", false
		}
		result := results[0]
		if len(result.Content) > 0 && result.Content[0].Kind == model.PartKindJSON && result.Content[0].JSON != nil {
			_ = json.Unmarshal(result.Content[0].JSONValue(), &payload)
		}
	}
	entries := planEntriesFromAny(payload["entries"])
	explanation := strings.TrimSpace(stringValue(payload["explanation"]))
	if len(entries) == 0 {
		entries = planEntriesFromAny(nestedValue(event.Meta, "caelis", "runtime", "tool", "entries"))
	}
	if explanation == "" {
		explanation = nestedString(event.Meta, "caelis", "runtime", "tool", "explanation")
	}
	return entries, explanation, true
}

func planToolNameFromEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	if name := nestedString(event.Meta, "caelis", "runtime", "tool", "name"); name != "" {
		return name
	}
	if event.Tool != nil {
		if name := strings.TrimSpace(event.Tool.Name); name != "" {
			return name
		}
	}
	if event.Protocol != nil && event.Protocol.ToolCall != nil {
		if name := strings.TrimSpace(event.Protocol.ToolCall.Name); name != "" {
			return name
		}
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		if title := strings.Fields(strings.TrimSpace(update.Title)); len(title) > 0 {
			return title[0]
		}
		return strings.TrimSpace(update.Kind)
	}
	return ""
}

func nestedString(values map[string]any, path ...string) string {
	current := nestedValue(values, path...)
	text, _ := current.(string)
	return strings.TrimSpace(text)
}

func nestedValue(values map[string]any, path ...string) any {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = mapped[key]
	}
	return current
}

func planEntriesFromAny(raw any) []plan.Entry {
	if raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var rows []struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil
	}
	entries := make([]plan.Entry, 0, len(rows))
	for _, row := range rows {
		content := row.Content
		status := row.Status
		content = strings.TrimSpace(content)
		status = strings.TrimSpace(status)
		if content == "" || status == "" {
			continue
		}
		entries = append(entries, plan.Entry{
			Content: content,
			Status:  plan.Status(status),
		})
	}
	return entries
}

func entriesToProtocol(entries []plan.Entry) []session.ProtocolPlanEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]session.ProtocolPlanEntry, 0, len(entries))
	for _, item := range entries {
		out = append(out, session.ProtocolPlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(string(item.Status)),
			Priority: "medium",
		})
	}
	return out
}

func entriesToState(entries []plan.Entry) []map[string]any {
	if len(entries) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(entries))
	for _, item := range entries {
		out = append(out, map[string]any{
			"content": strings.TrimSpace(item.Content),
			"status":  strings.TrimSpace(string(item.Status)),
		})
	}
	return out
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func (r *Runtime) policyMode(spec agent.AgentSpec) string {
	mode := strings.TrimSpace(r.defaultPolicyMode)
	if raw, ok := spec.Metadata["policy_mode"].(string); ok {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			mode = trimmed
		}
	}
	normalized := normalizePolicyMode(mode)
	if boolMetadata(spec.Metadata, "sandbox_auto_review_disabled") && normalized == presets.ModeAutoReview {
		return presets.ModeManual
	}
	return normalized
}

func normalizePolicyMode(mode string) string {
	return presets.NormalizeModeName(mode)
}

func modeOptionsFromSession(activeSession session.Session, spec agent.AgentSpec) policy.ModeOptions {
	opts := policy.ModeOptions{
		WorkspaceRoot: strings.TrimSpace(activeSession.CWD),
		TempRoot:      os.TempDir(),
	}
	if opts.WorkspaceRoot == "" {
		opts.WorkspaceRoot = activeSession.CWD
	}
	if values, ok := stringSliceMetadata(spec.Metadata, "policy_extra_read_roots"); ok {
		opts.ExtraReadRoots = values
	}
	if values, ok := stringSliceMetadata(spec.Metadata, "policy_extra_write_roots"); ok {
		opts.ExtraWriteRoots = values
	}
	return opts
}

func stringSliceMetadata(meta map[string]any, key string) ([]string, bool) {
	raw, ok := meta[key]
	if !ok || raw == nil {
		return nil, false
	}
	switch typed := raw.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, one := range typed {
			if trimmed := strings.TrimSpace(one); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out, true
	case []any:
		out := make([]string, 0, len(typed))
		for _, one := range typed {
			text, ok := one.(string)
			if !ok {
				continue
			}
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out, true
	default:
		return nil, false
	}
}

func boolMetadata(meta map[string]any, key string) bool {
	if meta == nil {
		return false
	}
	switch typed := meta[key].(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

type runner struct {
	runID       string
	cancelFn    context.CancelFunc
	eventsCh    chan runnerEvent
	closeOnce   sync.Once
	mu          sync.Mutex
	cancelled   bool
	closed      bool
	submissions []agent.Submission
	cancelHook  func() error
}

type runnerEvent struct {
	event *session.Event
	err   error
}

func newRunner(runID string, cancel context.CancelFunc) *runner {
	return &runner{
		runID:    runID,
		cancelFn: cancel,
		eventsCh: make(chan runnerEvent, 64),
	}
}

func (r *runner) RunID() string { return r.runID }

func (r *runner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for item := range r.eventsCh {
			if !yield(session.CloneEvent(item.event), item.err) {
				return
			}
		}
	}
}

func (r *runner) Submit(sub agent.Submission) error {
	if sub.Kind != agent.SubmissionKindConversation {
		return fmt.Errorf("impl/agent/local: unsupported submission kind %q", sub.Kind)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("impl/agent/local: runner is closed")
	}
	r.submissions = append(r.submissions, agent.CloneSubmission(sub))
	return nil
}

func (r *runner) drainSubmissions() []agent.Submission {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := agent.CloneSubmissions(r.submissions)
	r.submissions = nil
	return out
}

func (r *runner) markClosed() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}

func (r *runner) Cancel() agent.CancelResult {
	r.mu.Lock()
	if r.cancelled {
		r.mu.Unlock()
		return agent.CancelResult{Status: agent.CancelStatusAlreadyCancelled}
	}
	r.cancelled = true
	cancelFn := r.cancelFn
	cancelHook := r.cancelHook
	r.mu.Unlock()

	if cancelFn != nil {
		cancelFn()
	}
	result := agent.CancelResult{Status: agent.CancelStatusCancelled}
	if cancelHook != nil {
		if err := cancelHook(); err != nil {
			result.Err = err
		}
	}
	return result
}

func (r *runner) setCancelHook(fn func() error) {
	r.mu.Lock()
	cancelled := r.cancelled
	r.cancelHook = fn
	r.mu.Unlock()
	if cancelled && fn != nil {
		_ = fn()
	}
}

func (r *runner) Close() error {
	r.markClosed()
	return nil
}

func (r *runner) publishEvent(event *session.Event) {
	if r == nil || event == nil {
		return
	}
	r.publish(runnerEvent{event: session.CloneEvent(event)})
}

func (r *runner) publishError(err error) {
	if r == nil || err == nil {
		return
	}
	r.publish(runnerEvent{err: err})
}

func (r *runner) publish(item runnerEvent) {
	if r == nil {
		return
	}
	select {
	case r.eventsCh <- item:
	default:
		r.eventsCh <- item
	}
}

func (r *runner) finish() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		r.markClosed()
		close(r.eventsCh)
	})
}

func interruptedOrFailedStatus(ctx context.Context, err error) agent.RunLifecycleStatus {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return agent.RunLifecycleStatusInterrupted
	}
	return agent.RunLifecycleStatusFailed
}
