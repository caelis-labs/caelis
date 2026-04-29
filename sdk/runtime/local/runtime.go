package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdkcompact "github.com/OnslaughtSnail/caelis/sdk/compact"
	sdkcontroller "github.com/OnslaughtSnail/caelis/sdk/controller"
	sdkcontrolleracp "github.com/OnslaughtSnail/caelis/sdk/controller/acp"
	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdkplugin "github.com/OnslaughtSnail/caelis/sdk/plugin"
	sdkpolicy "github.com/OnslaughtSnail/caelis/sdk/policy"
	policypresets "github.com/OnslaughtSnail/caelis/sdk/policy/presets"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdkstream "github.com/OnslaughtSnail/caelis/sdk/stream"
	sdksubagent "github.com/OnslaughtSnail/caelis/sdk/subagent"
	sdksubagentacp "github.com/OnslaughtSnail/caelis/sdk/subagent/acp"
	sdktask "github.com/OnslaughtSnail/caelis/sdk/task"
	sdkplan "github.com/OnslaughtSnail/caelis/sdk/tool/builtin/plan"
)

const overflowCompactionRecoveryLimit = 3

// Config defines one baseline local runtime instance.
type Config struct {
	Sessions          sdksession.Service
	AgentFactory      sdkruntime.AgentFactory
	RunIDGenerator    func() string
	Clock             func() time.Time
	Sleep             func(context.Context, time.Duration) error
	Retry             RetryConfig
	Compaction        CompactionConfig
	Compactor         sdkcompact.Engine
	PolicyRegistry    sdkpolicy.Registry
	DefaultPolicyMode string
	Assembly          sdkplugin.ResolvedAssembly
	Controllers       sdkcontroller.Backend
	TaskStore         sdktask.Store
	Subagents         sdksubagent.Runner
}

// Runtime is the baseline local runtime implementation.
type Runtime struct {
	sessions          sdksession.Service
	agentFactory      sdkruntime.AgentFactory
	runIDGenerator    func() string
	clock             func() time.Time
	sleep             func(context.Context, time.Duration) error
	retry             RetryConfig
	compaction        CompactionConfig
	compactor         sdkcompact.Engine
	policies          sdkpolicy.Registry
	defaultPolicyMode string
	assembly          sdkplugin.ResolvedAssembly
	acpRegistry       *sdksubagentacp.Registry
	controllers       sdkcontroller.Backend
	subagents         sdksubagent.Runner
	idCounter         atomic.Uint64
	mu                sync.RWMutex
	runStates         map[string]sdkruntime.RunState
	tasks             *taskRuntime
	terminals         *streamService
}

// New returns one baseline local runtime.
func New(cfg Config) (*Runtime, error) {
	if cfg.Sessions == nil {
		return nil, errors.New("sdk/runtime/local: sessions service is required")
	}
	if cfg.AgentFactory == nil {
		return nil, errors.New("sdk/runtime/local: agent factory is required")
	}
	r := &Runtime{
		sessions:          cfg.Sessions,
		agentFactory:      cfg.AgentFactory,
		runIDGenerator:    cfg.RunIDGenerator,
		clock:             cfg.Clock,
		sleep:             cfg.Sleep,
		retry:             normalizeRetryConfig(cfg.Retry),
		compaction:        normalizeCompactionConfig(cfg.Compaction),
		policies:          cfg.PolicyRegistry,
		defaultPolicyMode: strings.TrimSpace(cfg.DefaultPolicyMode),
		assembly:          sdkplugin.CloneResolvedAssembly(cfg.Assembly),
		controllers:       cfg.Controllers,
		subagents:         cfg.Subagents,
		runStates:         map[string]sdkruntime.RunState{},
	}
	if r.clock == nil {
		r.clock = time.Now
	}
	if r.sleep == nil {
		r.sleep = sleepContext
	}
	if r.policies == nil {
		reg, err := policypresets.NewRegistry()
		if err != nil {
			return nil, err
		}
		r.policies = reg
	}
	if r.defaultPolicyMode == "" {
		r.defaultPolicyMode = policypresets.ModeDefault
	}
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

func (r *Runtime) applyAssembly(assembly sdkplugin.ResolvedAssembly, cfg Config) error {
	assembly = sdkplugin.CloneResolvedAssembly(assembly)
	if len(assembly.Agents) == 0 {
		r.controllers = cfg.Controllers
		r.subagents = cfg.Subagents
		return nil
	}

	registry, err := sdksubagentacp.NewRegistry(assembly.Agents)
	if err != nil {
		return err
	}
	r.acpRegistry = registry
	runner, err := sdksubagentacp.NewRunner(sdksubagentacp.RunnerConfig{Registry: registry})
	if err != nil {
		return err
	}
	manager, err := sdkcontrolleracp.NewManager(sdkcontrolleracp.Config{Registry: registry})
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

func (r *Runtime) UpdateACPAgents(agents []sdkplugin.AgentConfig) error {
	if r == nil {
		return fmt.Errorf("sdk/runtime/local: runtime is unavailable")
	}
	assembly := sdkplugin.ResolvedAssembly{Agents: append([]sdkplugin.AgentConfig(nil), agents...)}
	registry := r.acpRegistry
	if registry == nil {
		return fmt.Errorf("sdk/runtime/local: ACP registry is not configured")
	}
	if err := registry.Replace(assembly.Agents); err != nil {
		return err
	}
	r.mu.Lock()
	r.assembly.Agents = sdkplugin.CloneResolvedAssembly(assembly).Agents
	r.mu.Unlock()
	return nil
}

func validateControlPlaneConfig(cfg Config) error {
	if len(cfg.Assembly.Agents) == 0 {
		return nil
	}
	if cfg.Controllers != nil || cfg.Subagents != nil {
		return errors.New("sdk/runtime/local: Assembly.Agents cannot be combined with explicit Controllers or Subagents")
	}
	return nil
}

// Terminals returns the unified terminal read/subscribe surface for this
// runtime. Task control remains on the TASK tool plane.
func (r *Runtime) Streams() sdkstream.Service {
	if r == nil {
		return nil
	}
	return r.terminals
}

// Run executes one agent turn for one existing session.
func (r *Runtime) Run(
	ctx context.Context,
	req sdkruntime.RunRequest,
) (sdkruntime.RunResult, error) {
	ref := sdksession.NormalizeSessionRef(req.SessionRef)
	session, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return sdkruntime.RunResult{}, err
	}
	session, err = r.ensureSessionController(ctx, session)
	if err != nil {
		return sdkruntime.RunResult{}, err
	}
	if session.Controller.Kind == sdksession.ControllerKindACP {
		return r.runACPControllerTurn(ctx, session, ref, req)
	}

	runID := r.nextID("run", r.runIDGenerator)
	turnID := r.nextID("turn", nil)
	r.setRunState(ref.SessionID, sdkruntime.RunState{
		Status:      sdkruntime.RunLifecycleStatusRunning,
		ActiveRunID: runID,
		UpdatedAt:   r.now(),
	})
	runCtx, cancel := context.WithCancel(ctx)
	handle := newRunner(runID, cancel)
	go r.executeKernelTurn(runCtx, session, ref, runID, turnID, req, handle)
	return sdkruntime.RunResult{
		Session: session,
		Handle:  handle,
	}, nil
}

func (r *Runtime) executeKernelTurn(
	ctx context.Context,
	session sdksession.Session,
	ref sdksession.SessionRef,
	runID string,
	turnID string,
	req sdkruntime.RunRequest,
	handle *runner,
) {
	defer handle.finish()

	batch := make([]*sdksession.Event, 0, 4)
	userEvent := buildUserEvent(session, turnID, req.Input, req.ContentParts)
	if err := r.runWithRetry(ctx, session, ref, runID, turnID, req, userEvent, &batch, handle); err != nil {
		r.setRunState(ref.SessionID, sdkruntime.RunState{
			Status:      interruptedOrFailedStatus(ctx, err),
			ActiveRunID: runID,
			LastError:   err.Error(),
			UpdatedAt:   r.now(),
		})
		handle.publishError(err)
		return
	}
	r.setRunState(ref.SessionID, sdkruntime.RunState{
		Status:      sdkruntime.RunLifecycleStatusCompleted,
		ActiveRunID: runID,
		UpdatedAt:   r.now(),
	})
}

// RunState returns the last known run state for one session.
func (r *Runtime) RunState(
	_ context.Context,
	ref sdksession.SessionRef,
) (sdkruntime.RunState, error) {
	ref = sdksession.NormalizeSessionRef(ref)
	r.mu.RLock()
	defer r.mu.RUnlock()
	state, ok := r.runStates[ref.SessionID]
	if !ok {
		return sdkruntime.RunState{}, sdksession.ErrSessionNotFound
	}
	return state, nil
}

func (r *Runtime) resolveAgent(
	ctx context.Context,
	session sdksession.Session,
	ref sdksession.SessionRef,
	state map[string]any,
	runID string,
	turnID string,
	req sdkruntime.RunRequest,
) (sdkruntime.Agent, error) {
	if req.Agent != nil {
		return req.Agent, nil
	}
	spec := r.applyAssemblySpec(state, req.AgentSpec)
	spec.Request = req.Request.WithDefaults(spec.Request)
	modeName := r.policyMode(spec)
	spec.Tools = r.wrapToolsForRuntime(session, ref, spec, runtimeToolContext{
		mode:              modeName,
		approvalRequester: req.ApprovalRequester,
	})
	spec.Tools = r.wrapToolsForPolicy(session, ref, state, spec, approvalContext{
		ctx:        ctx,
		requester:  req.ApprovalRequester,
		runtime:    r,
		session:    sdksession.CloneSession(session),
		sessionRef: sdksession.NormalizeSessionRef(ref),
		runID:      strings.TrimSpace(runID),
		turnID:     strings.TrimSpace(turnID),
	})
	return r.agentFactory.NewAgent(ctx, spec)
}

func (r *Runtime) setRunState(sessionID string, state sdkruntime.RunState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runStates[strings.TrimSpace(sessionID)] = state
}

func (r *Runtime) runWithRetry(
	ctx context.Context,
	session sdksession.Session,
	ref sdksession.SessionRef,
	runID string,
	turnID string,
	req sdkruntime.RunRequest,
	pendingInput *sdksession.Event,
	batch *[]*sdksession.Event,
	sink *runner,
) error {
	attempt := 0
	overflowRecoveries := 0
	for {
		attemptBatch, emitted, inputPersisted, err := r.runAttempt(ctx, session, ref, runID, turnID, req, pendingInput, sink)
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
				return fmt.Errorf("sdk/runtime/local: context overflow persisted after %d compaction recoveries: %w", overflowCompactionRecoveryLimit, err)
			}
			compacted, compactErr := r.compactAfterOverflow(ctx, session, ref, req, err)
			if compactErr != nil {
				return compactErr
			}
			if !compacted {
				return err
			}
			overflowRecoveries++
			attempt = 0
			continue
		}
		if emitted || !shouldRetry(err) {
			*batch = append(*batch, attemptBatch...)
			return err
		}
		if len(attemptBatch) > 0 {
			*batch = append(*batch, attemptBatch...)
		}
		policy := retryPolicyForError(r.retry, err)
		if attempt >= policy.maxRetries {
			if policy.backpressure {
				return fmt.Errorf("sdk/runtime/local: model request hit provider backpressure after %d retries: %w", policy.maxRetries, err)
			}
			return fmt.Errorf("sdk/runtime/local: model request failed after %d retries: %w", policy.maxRetries, err)
		}
		delay := retryDelayForAttemptWithBounds(attempt, policy.baseDelay, policy.maxDelay)
		notice := buildRetryNoticeEvent(session, turnID, attempt+1, policy.maxRetries, delay, err)
		*batch = append(*batch, notice)
		if sink != nil {
			sink.publishEvent(notice)
		}
		if sleepErr := r.sleep(ctx, delay); sleepErr != nil {
			return sleepErr
		}
		attempt++
	}
}

func (r *Runtime) runAttempt(
	ctx context.Context,
	session sdksession.Session,
	ref sdksession.SessionRef,
	runID string,
	turnID string,
	req sdkruntime.RunRequest,
	pendingInput *sdksession.Event,
	sink *runner,
) ([]*sdksession.Event, bool, bool, error) {
	events, state, err := r.prepareInvocationContext(ctx, session, ref, req, pendingInput)
	if err != nil {
		return nil, false, false, err
	}

	batch := make([]*sdksession.Event, 0, 3)
	inputPersisted := false
	if pendingInput != nil {
		persisted, appendErr := r.sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
			SessionRef: ref,
			Event:      pendingInput,
		})
		if appendErr != nil {
			return nil, false, false, appendErr
		}
		batch = append(batch, persisted)
		events = append(events, sdksession.CloneEvent(persisted))
		inputPersisted = true
		if sink != nil {
			sink.publishEvent(persisted)
		}
	}

	agent, err := r.resolveAgent(ctx, session, ref, state, runID, turnID, req)
	if err != nil {
		return batch, false, inputPersisted, err
	}
	runCtx := sdkruntime.NewContext(sdkruntime.ContextSpec{
		Context: ctx,
		Session: session,
		Events:  events,
		State:   state,
	})

	emitted := false
	for event, runErr := range agent.Run(runCtx) {
		if runErr != nil {
			return batch, emitted, inputPersisted, runErr
		}
		if event == nil {
			continue
		}
		emitted = true
		normalized := normalizeEvent(session, turnID, event)
		if sdksession.IsCanonicalHistoryEvent(normalized) {
			normalized, err = r.sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
				SessionRef: ref,
				Event:      normalized,
			})
			if err != nil {
				return batch, emitted, inputPersisted, err
			}
		}
		batch = append(batch, sdksession.CloneEvent(normalized))
		if sink != nil {
			sink.publishEvent(normalized)
		}
		if planEvent, handled, planErr := r.handlePlanEvent(ctx, ref, turnID, normalized); planErr != nil {
			return batch, emitted, inputPersisted, planErr
		} else if handled {
			batch = append(batch, sdksession.CloneEvent(planEvent))
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

func buildUserEvent(session sdksession.Session, turnID string, input string, parts []sdkmodel.ContentPart) *sdksession.Event {
	if strings.TrimSpace(input) == "" && len(parts) == 0 {
		return nil
	}
	message := sdkmodel.MessageFromTextAndContentParts(sdkmodel.RoleUser, strings.TrimSpace(input), parts)
	return &sdksession.Event{
		Type:       sdksession.EventTypeUser,
		Visibility: sdksession.VisibilityCanonical,
		Actor:      sdksession.ActorRef{Kind: sdksession.ActorKindUser, Name: "user"},
		Scope:      ptrScope(defaultScope(session, turnID)),
		Protocol: &sdksession.EventProtocol{
			UpdateType: string(sdksession.ProtocolUpdateTypeUserMessage),
		},
		Message: &message,
		Text:    message.TextContent(),
	}
}

func normalizeEvent(session sdksession.Session, turnID string, event *sdksession.Event) *sdksession.Event {
	event = sdksession.CloneEvent(event)
	if event == nil {
		return nil
	}
	if event.Type == "" {
		event.Type = sdksession.EventTypeOf(event)
	}
	if event.Visibility == "" {
		event.Visibility = sdksession.VisibilityCanonical
	}
	if event.Text == "" && event.Message != nil {
		event.Text = event.Message.TextContent()
	}
	if event.Scope == nil {
		scope := defaultScope(session, turnID)
		event.Scope = &scope
	}
	if event.Scope.TurnID == "" {
		event.Scope.TurnID = strings.TrimSpace(turnID)
	}
	if event.Scope.Controller.Kind == "" {
		event.Scope.Controller = defaultControllerRef(session)
	}
	if event.Actor.Kind == "" {
		event.Actor = defaultActorForEvent(event)
	}
	return event
}

func buildRetryNoticeEvent(session sdksession.Session, turnID string, attempt int, maxRetries int, delay time.Duration, cause error) *sdksession.Event {
	text := retryWarningText(attempt, maxRetries, delay, cause)
	message := sdkmodel.NewTextMessage(sdkmodel.RoleSystem, text)
	return sdksession.MarkNotice(&sdksession.Event{
		Type:    sdksession.EventTypeNotice,
		Actor:   sdksession.ActorRef{Kind: sdksession.ActorKindSystem, Name: "runtime"},
		Scope:   ptrScope(defaultScope(session, turnID)),
		Message: &message,
		Text:    text,
	}, "warn", text)
}

func defaultScope(session sdksession.Session, turnID string) sdksession.EventScope {
	return sdksession.EventScope{
		TurnID:     strings.TrimSpace(turnID),
		Controller: defaultControllerRef(session),
	}
}

func defaultControllerRef(session sdksession.Session) sdksession.ControllerRef {
	binding := sdksession.CloneControllerBinding(session.Controller)
	kind := binding.Kind
	if kind == "" {
		kind = sdksession.ControllerKindKernel
	}
	return sdksession.ControllerRef{
		Kind:    kind,
		ID:      binding.ControllerID,
		EpochID: binding.EpochID,
	}
}

func defaultActorForEvent(event *sdksession.Event) sdksession.ActorRef {
	switch sdksession.EventTypeOf(event) {
	case sdksession.EventTypeUser:
		return sdksession.ActorRef{Kind: sdksession.ActorKindUser, Name: "user"}
	case sdksession.EventTypeToolResult:
		name := ""
		if event.Message != nil {
			if parts := event.Message.ToolResults(); len(parts) > 0 {
				name = parts[0].Name
			}
		}
		return sdksession.ActorRef{Kind: sdksession.ActorKindTool, Name: strings.TrimSpace(name)}
	case sdksession.EventTypeNotice, sdksession.EventTypeLifecycle, sdksession.EventTypeSystem:
		return sdksession.ActorRef{Kind: sdksession.ActorKindSystem, Name: "runtime"}
	default:
		return sdksession.ActorRef{Kind: sdksession.ActorKindController}
	}
}

func ptrScope(scope sdksession.EventScope) *sdksession.EventScope {
	return &scope
}

func (r *Runtime) handlePlanEvent(
	ctx context.Context,
	ref sdksession.SessionRef,
	turnID string,
	event *sdksession.Event,
) (*sdksession.Event, bool, error) {
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
	planEvent := &sdksession.Event{
		Type:       sdksession.EventTypePlan,
		Visibility: sdksession.VisibilityCanonical,
		Actor:      sdksession.ActorRef{Kind: sdksession.ActorKindController},
		Scope: &sdksession.EventScope{
			TurnID: strings.TrimSpace(turnID),
			Source: "tool_result",
		},
		Protocol: &sdksession.EventProtocol{
			UpdateType: string(sdksession.ProtocolUpdateTypePlan),
			Plan: &sdksession.ProtocolPlan{
				Entries: entriesToProtocol(entries),
			},
		},
		Text: strings.TrimSpace(explanation),
	}
	normalized := normalizeEvent(sdksession.Session{}, turnID, planEvent)
	normalized.Scope.Controller = event.Scope.Controller
	persisted, err := r.sessions.AppendEvent(ctx, sdksession.AppendEventRequest{
		SessionRef: ref,
		Event:      normalized,
	})
	if err != nil {
		return nil, true, err
	}
	return persisted, true, nil
}

func planEntriesFromEvent(event *sdksession.Event) ([]sdkplan.Entry, string, bool) {
	if event == nil || event.Message == nil {
		return nil, "", false
	}
	results := event.Message.ToolResults()
	if len(results) == 0 {
		return nil, "", false
	}
	result := results[0]
	if !strings.EqualFold(strings.TrimSpace(result.Name), sdkplan.ToolName) {
		return nil, "", false
	}
	payload := map[string]any{}
	if len(result.Content) > 0 && result.Content[0].Kind == sdkmodel.PartKindJSON && result.Content[0].JSON != nil {
		_ = json.Unmarshal(result.Content[0].JSONValue(), &payload)
	}
	rawEntries, _ := payload["entries"].([]any)
	entries := make([]sdkplan.Entry, 0, len(rawEntries))
	for _, item := range rawEntries {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		content, _ := row["content"].(string)
		status, _ := row["status"].(string)
		content = strings.TrimSpace(content)
		status = strings.TrimSpace(status)
		if content == "" || status == "" {
			continue
		}
		entries = append(entries, sdkplan.Entry{
			Content: content,
			Status:  sdkplan.Status(status),
		})
	}
	return entries, strings.TrimSpace(stringValue(payload["explanation"])), true
}

func entriesToProtocol(entries []sdkplan.Entry) []sdksession.ProtocolPlanEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]sdksession.ProtocolPlanEntry, 0, len(entries))
	for _, item := range entries {
		out = append(out, sdksession.ProtocolPlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(string(item.Status)),
			Priority: "medium",
		})
	}
	return out
}

func entriesToState(entries []sdkplan.Entry) []map[string]any {
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

func (r *Runtime) policyMode(spec sdkruntime.AgentSpec) string {
	if raw, ok := spec.Metadata["policy_mode"].(string); ok {
		if mode := strings.TrimSpace(raw); mode != "" {
			return mode
		}
	}
	return r.defaultPolicyMode
}

func modeOptionsFromSession(session sdksession.Session, spec sdkruntime.AgentSpec) sdkpolicy.ModeOptions {
	opts := sdkpolicy.ModeOptions{
		WorkspaceRoot: strings.TrimSpace(session.CWD),
		TempRoot:      os.TempDir(),
	}
	if opts.WorkspaceRoot == "" {
		opts.WorkspaceRoot = session.CWD
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

type runner struct {
	runID      string
	cancelFn   context.CancelFunc
	eventsCh   chan runnerEvent
	closeOnce  sync.Once
	mu         sync.Mutex
	cancelled  bool
	closed     bool
	submitFunc func(sdkruntime.Submission) error
}

type runnerEvent struct {
	event *sdksession.Event
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

func (r *runner) Events() iter.Seq2[*sdksession.Event, error] {
	return func(yield func(*sdksession.Event, error) bool) {
		for item := range r.eventsCh {
			if !yield(sdksession.CloneEvent(item.event), item.err) {
				return
			}
		}
	}
}

func (r *runner) Submit(sub sdkruntime.Submission) error {
	r.mu.Lock()
	fn := r.submitFunc
	r.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(sub)
}

func (r *runner) Cancel() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancelled {
		return false
	}
	r.cancelled = true
	if r.cancelFn != nil {
		r.cancelFn()
	}
	return true
}

func (r *runner) Close() error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return nil
}

func (r *runner) publishEvent(event *sdksession.Event) {
	if r == nil || event == nil {
		return
	}
	r.publish(runnerEvent{event: sdksession.CloneEvent(event)})
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
		close(r.eventsCh)
	})
}

func interruptedOrFailedStatus(ctx context.Context, err error) sdkruntime.RunLifecycleStatus {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return sdkruntime.RunLifecycleStatusInterrupted
	}
	return sdkruntime.RunLifecycleStatusFailed
}
