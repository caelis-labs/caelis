package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/internal/identity"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
)

// Config defines one baseline local runtime instance.
type Config struct {
	Hooks agent.Hooks
	// HookPurpose names this independently assembled model loop. Empty means chat.
	HookPurpose    agent.HookPurpose
	Sessions       session.Service
	AgentFactory   agent.AgentFactory
	RunIDGenerator func() string
	Clock          func() time.Time
	Compaction     CompactionConfig
	Compactor      compact.Engine
	// Diagnostics receives fixed Runtime classifications only. Callers must not
	// attach user content, Session identities, or workspace paths.
	Diagnostics *slog.Logger
	// PolicyRegistry installs tool admission for direct and Code Mode calls.
	// With no registry or requested profile, the host tools execute directly.
	PolicyRegistry      policy.Registry
	DefaultPolicyMode   string
	SandboxPolicy       sandbox.PolicySnapshot
	DefaultApprovalMode string
	// PersistPolicyConsent uses the embedding's existing configuration owner.
	PersistPolicyConsent     PolicyConsentWriter
	Controllers              controller.Backend
	ControllerContextRouter  controller.ContextRouter
	ControllerRecovery       controller.RecoveryCoordinator
	ControllerEventForwarder agent.ControllerEventForwarder
	TaskStore                task.Store
	// TaskOutput installs producer-only Task observers before external effects.
	// The SDK never reads from or assigns retention semantics to this hook.
	TaskOutput output.Binder
	// TaskActivityChanged reports a committed Task terminal transition. Product
	// hosts may use it to re-evaluate an in-memory Runtime reference; it does not
	// authorize work or replace the durable Task store.
	TaskActivityChanged func(session.SessionRef)
	// TaskCommitted reports a cloned Task entry after its durable mutation commits.
	// Product Control may use it to advance a lightweight status index; the
	// callback never owns Task lifecycle or content delivery.
	TaskCommitted func(*task.Entry)
	Subagents     agent.SubagentRunner
	// ChildApprovalRequester routes approvals independently of a parent Turn,
	// including user-started children and child endpoints reattached after restart.
	ChildApprovalRequester agent.ApprovalRequester
	TraceSink              agent.TraceSink
}

// Runtime is the baseline local runtime implementation.
type Runtime struct {
	hooks                    agent.Hooks
	hookPurpose              agent.HookPurpose
	codeModeMu               sync.Mutex
	codeModeEnvironments     map[session.SessionRef]*codeModeEnvironment
	childApprovalRequester   agent.ApprovalRequester
	sessions                 session.Service
	agentFactory             agent.AgentFactory
	runIDGenerator           func() string
	clock                    func() time.Time
	compaction               CompactionConfig
	compactor                compact.Engine
	diagnostics              *slog.Logger
	compactionRequestMu      sync.RWMutex
	compactionRequests       map[string]compactionRequestSnapshot
	idleCandidateMu          sync.Mutex
	idleCandidates           map[string]*idleCompactionCandidate
	policies                 policy.Registry
	defaultPolicyMode        string
	sandboxPolicy            sandbox.PolicySnapshot
	defaultApprovalMode      approval.Mode
	persistPolicyConsent     PolicyConsentWriter
	authorizationMu          sync.Mutex
	authorizationFlights     map[string]*authorizationFlight
	controllers              controller.Backend
	controllerContextRouter  controller.ContextRouter
	controllerRecovery       controller.RecoveryCoordinator
	controllerEventForwarder agent.ControllerEventForwarder
	subagents                agent.SubagentRunner
	sessionWrites            sessionWriteQueue
	// participantMu linearizes durable participant topology mutations with
	// source/target input reservations; remote receipt waits never hold it.
	participantMu           sync.Mutex
	participantPromptClaims map[participantPromptKey]struct{}
	participantInputPins    map[participantPromptKey]int
	participantInputs       map[*participantInputReservation]struct{}
	mu                      sync.RWMutex
	activeRunners           map[string]activeRun
	approvalWaiters         map[string]approvalWaiter
	tasks                   *taskRuntime
	taskOutput              output.Binder
	lifecycle               agent.TraceOptions
}

// RunnerCompletionWaiterGuaranteed marks that every Runner returned by
// Runtime honors the WaitCompletion contract.
func (*Runtime) RunnerCompletionWaiterGuaranteed() {}

// New returns one baseline local runtime.
func New(cfg Config) (*Runtime, error) {
	if cfg.Sessions == nil {
		return nil, errors.New("agent-sdk/runtime: sessions service is required")
	}
	if cfg.AgentFactory == nil {
		return nil, errors.New("agent-sdk/runtime: agent factory is required")
	}
	if err := cfg.Hooks.Validate(); err != nil {
		return nil, err
	}
	if cfg.HookPurpose == "" {
		cfg.HookPurpose = agent.HookPurposeChat
	}
	switch cfg.HookPurpose {
	case agent.HookPurposeChat, agent.HookPurposeCompaction, agent.HookPurposeSystemAgent:
	default:
		return nil, fmt.Errorf("runtime: invalid hook purpose %q", cfg.HookPurpose)
	}
	r := &Runtime{
		hooks:                    agent.CloneHooks(cfg.Hooks),
		hookPurpose:              cfg.HookPurpose,
		sessions:                 cfg.Sessions,
		agentFactory:             cfg.AgentFactory,
		runIDGenerator:           cfg.RunIDGenerator,
		clock:                    cfg.Clock,
		compaction:               normalizeCompactionConfig(cfg.Compaction),
		diagnostics:              cfg.Diagnostics,
		compactionRequests:       map[string]compactionRequestSnapshot{},
		idleCandidates:           map[string]*idleCompactionCandidate{},
		policies:                 cfg.PolicyRegistry,
		defaultPolicyMode:        strings.TrimSpace(cfg.DefaultPolicyMode),
		sandboxPolicy:            sandbox.ClonePolicySnapshot(cfg.SandboxPolicy),
		defaultApprovalMode:      approval.NormalizeMode(cfg.DefaultApprovalMode),
		persistPolicyConsent:     cfg.PersistPolicyConsent,
		controllers:              cfg.Controllers,
		controllerContextRouter:  cfg.ControllerContextRouter,
		controllerRecovery:       cfg.ControllerRecovery,
		controllerEventForwarder: cfg.ControllerEventForwarder,
		subagents:                cfg.Subagents,
		childApprovalRequester:   cfg.ChildApprovalRequester,
		taskOutput:               cfg.TaskOutput,
		activeRunners:            map[string]activeRun{},
		approvalWaiters:          map[string]approvalWaiter{},
		participantPromptClaims:  map[participantPromptKey]struct{}{},
		lifecycle: agent.TraceOptions{
			TraceSink: cfg.TraceSink,
			Clock:     cfg.Clock,
		},
	}
	if r.clock == nil {
		r.clock = time.Now
	}
	r.lifecycle.Clock = r.clock
	if r.policies == nil && r.defaultPolicyMode != "" {
		return nil, errors.New("agent-sdk/runtime: default policy mode requires a policy registry")
	}
	if r.policies != nil {
		r.defaultPolicyMode = presets.NormalizeModeName(r.defaultPolicyMode)
	}
	if err := validateControllerForwarder(cfg); err != nil {
		return nil, err
	}
	r.compactor = cfg.Compactor
	if r.compactor == nil {
		r.compactor = newCodexStyleCompactor(r.compaction, r.diagnostics)
	}
	r.tasks = newTaskRuntime(r, cfg.TaskStore, cfg.TaskActivityChanged)
	r.tasks.taskCommitted = cfg.TaskCommitted
	return r, nil
}

func (r *Runtime) currentApprovalMode(state map[string]any) approval.Mode {
	if r == nil {
		return approval.CurrentMode(state)
	}
	return approval.CurrentModeOrDefault(state, r.defaultApprovalMode)
}

func validateControllerForwarder(cfg Config) error {
	if cfg.Controllers == nil {
		return nil
	}
	if cfg.ControllerEventForwarder == nil {
		return errors.New("agent-sdk/runtime: controller event forwarder is required when ACP controllers are configured")
	}
	if cfg.ControllerContextRouter == nil {
		return errors.New("agent-sdk/runtime: controller context router is required when controller endpoints are configured")
	}
	if cfg.ControllerRecovery == nil {
		return errors.New("agent-sdk/runtime: controller recovery coordinator is required when ACP controllers are configured")
	}
	return nil
}

// Run executes one agent turn for one existing session.
func (r *Runtime) Run(
	ctx context.Context,
	req agent.RunRequest,
) (out agent.RunResult, runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRunInput(req); err != nil {
		return agent.RunResult{}, err
	}
	ref := session.NormalizeSessionRef(req.SessionRef)
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return agent.RunResult{}, err
	}
	activeSession, err = r.ensureSessionController(ctx, activeSession, session.RuntimeMutationGuard(ctx))
	if err != nil {
		return agent.RunResult{}, err
	}
	if r.hooks.HasLoopPhases() {
		_, supports := r.agentFactory.(interface{ SupportsLoopHooks() })
		if activeSession.Controller.Kind == session.ControllerKindACP || req.Agent != nil || !supports {
			return agent.RunResult{}, errorcode.New(errorcode.Unsupported, "configured loop hook phases are unavailable inside this Agent")
		}
	}
	if activeSession.Controller.Kind != session.ControllerKindACP && req.Agent == nil {
		if err := validateRunRequestAgentSpec(req); err != nil {
			return agent.RunResult{}, err
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	if activeSession.Controller.Kind == session.ControllerKindACP {
		return r.runACPControllerTurn(runCtx, cancel, activeSession, ref, req)
	}

	runID := r.nextID("run", r.runIDGenerator)
	turnID := strings.TrimSpace(req.TurnID)
	if turnID == "" {
		turnID = r.nextID("turn", nil)
	}
	runCtx = context.WithValue(runCtx, taskScopeKey{}, newTaskContinuationScope())
	runCtx = withLifecycleScope(runCtx, lifecycleScope{sessionRef: ref, runID: runID, turnID: turnID})
	if err := r.beginRun(ref, runID); err != nil {
		cancel()
		return agent.RunResult{}, err
	}
	runCtx, releaseFence, fenceErr := r.ownRunFence(runCtx, ref)
	if fenceErr != nil {
		r.unregisterActiveRun(runID)
		cancel()
		return agent.RunResult{}, fenceErr
	}
	started := false
	defer func() {
		if !started {
			runErr = errors.Join(runErr, releaseFence())
			r.unregisterActiveRun(runID)
		}
	}()
	if err := r.reconcileTurnPlan(runCtx, ref, turnID); err != nil {
		cancel()
		return agent.RunResult{}, err
	}
	if err := r.repairToolPairs(runCtx, ref); err != nil {
		cancel()

		return agent.RunResult{}, err
	}
	handle := newRunner(runCtx, runID, cancel, req.SourceObserver)

	r.registerActiveRun(ref, activeSession, turnID, handle)
	started = true
	go func() {
		defer cancel()
		r.executeKernelTurn(runCtx, activeSession, ref, runID, turnID, req, handle, releaseFence)
	}()
	return agent.RunResult{
		Session: activeSession,
		Handle:  handle,
	}, nil
}

func validateRunInput(req agent.RunRequest) error {
	switch normalizeInputKind(req.InputKind) {
	case agent.SubmissionKindConversation:
		if len(req.Inputs) > 0 {
			return errorcode.New(errorcode.InvalidArgument, "agent-sdk/runtime: conversation turns cannot carry Agent communication batches")
		}
		return nil
	case agent.SubmissionKindAgentCommunication:
		if len(req.Inputs) > 0 {
			if err := agent.ValidateSubmissionInputs(agent.Submission{
				Kind: req.InputKind, Text: strings.TrimSpace(req.Input), DisplayInput: strings.TrimSpace(req.DisplayInput),
				ContentParts: req.ContentParts, Actor: req.InputActor, Inputs: req.Inputs,
			}); err != nil {
				return errorcode.Wrap(errorcode.InvalidArgument, "agent-sdk/runtime: "+err.Error(), err)
			}
			return nil
		}
		if err := session.ValidateAgentCommunicationActor(req.InputActor); err != nil {
			return errorcode.Wrap(errorcode.InvalidArgument, "agent-sdk/runtime: invalid Agent communication", err)
		}
		return nil
	default:
		return errorcode.New(errorcode.InvalidArgument, "agent-sdk/runtime: unsupported input kind")
	}
}

func (r *Runtime) beginRun(ref session.SessionRef, runID string) error {
	ref = session.NormalizeSessionRef(ref)
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, current := range r.activeRunners {
		if current.ref.SessionID == ref.SessionID {
			return &agent.RunConflictError{SessionRef: ref, ActiveRunID: id}
		}
	}
	r.activeRunners[runID] = activeRun{ref: ref}

	return nil
}

func (r *Runtime) executeKernelTurn(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	runID string,
	turnID string,
	req agent.RunRequest,
	handle *runner,
	releaseFence func() error,
) {
	defer handle.finish()
	defer func() {
		if err := releaseFence(); err != nil {
			handle.publishError(err)
		}
	}()
	defer r.unregisterActiveRun(runID)

	ctx = hookrun.WithRun(ctx, r.hooks, agent.HookContext{SessionRef: ref, RunID: runID, TurnID: turnID, Purpose: r.hookPurpose})
	hookrun.FromContext(ctx).Trace = r.lifecycle
	inputEvents, inputErr := buildRunInputEvents(activeSession, turnID, req)
	if inputErr != nil {
		handle.publishError(inputErr)
		return
	}
	inputEvents, inputErr = r.prepareHookInputs(ctx, inputEvents)
	if inputErr == nil {
		var start []*session.Event
		start, inputErr = r.runTurnStartHooks(ctx)
		inputEvents = append(inputEvents, start...)
	}
	if inputErr == nil && hookrun.FromContext(ctx).HasEarly() {
		_, supports := r.agentFactory.(interface{ SupportsLoopHooks() })
		if req.Agent != nil || !supports {
			inputErr = errorcode.New(errorcode.Unsupported, "this external Agent cannot enforce an early-response hook decision")
		}
	}
	if inputErr != nil {
		handle.publishError(inputErr)
		return
	}
	lifecycleErr := r.traceOperation(ctx, r.lifecycleEvent(ctx, agent.LifecycleRun, "", ""), func(runCtx context.Context) error {
		return r.traceOperation(runCtx, r.lifecycleEvent(runCtx, agent.LifecycleTurn, "", ""), func(turnCtx context.Context) error {
			runErr := r.runWithOverflowRecovery(turnCtx, activeSession, ref, runID, turnID, req, inputEvents, handle)
			scope := taskScopeFromContext(turnCtx)
			scope.stopCodeMode()
			if runErr != nil {
				scope.revoke(runErr, true)
			}
			return errors.Join(runErr, scope.join())
		})
	})
	if err := errors.Join(lifecycleErr, r.finalizeTurn(ctx, ref, turnID)); err != nil {
		handle.publishError(err)
	}
}

func (r *Runtime) nextID(prefix string, custom func() string) string {
	if custom != nil {
		if id := strings.TrimSpace(custom()); id != "" {
			return id
		}
	}
	return identity.New(prefix)
}

func (r *Runtime) now() time.Time {
	if r != nil && r.clock != nil {
		return r.clock()
	}
	return time.Now()
}
