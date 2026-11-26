package runtime

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/internal/sdkctx"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
)

// Config defines one baseline local runtime instance.
type Config struct {
	Hooks hook.Set
	// HookPurpose names this independently assembled model loop. Empty means chat.
	HookPurpose hook.Purpose
	// Sessions is shared across runs. Each RunRequest selects one Session by its ref.
	Sessions       session.Service
	AgentFactory   agent.AgentFactory
	RunIDGenerator func() string
	Clock          func() time.Time
	Compaction     CompactionConfig
	Compactor      compact.Engine
	// Diagnostics receives fixed Runtime classifications only. Callers must not
	// attach user content, Session identities, or workspace paths.
	Diagnostics         *slog.Logger
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
	TaskOutput task.OutputBinder
	// TaskActivityChanged reports a committed Task terminal transition. Product
	// hosts may use it to re-evaluate an in-memory Runtime reference; it does not
	// authorize work or replace the durable Task store.
	TaskActivityChanged func(session.SessionRef)
	// TaskCommitted reports a cloned Task entry after its durable mutation commits.
	// Product Control may use it to advance a lightweight status index; the
	// callback never owns Task lifecycle or content delivery.
	TaskCommitted func(*task.Entry)
	Subagents     task.SubagentRunner
	// ChildApprovalRequester routes approvals independently of a parent Turn,
	// including user-started children and child endpoints reattached after restart.
	ChildApprovalRequester approval.Requester
	TraceSink              agent.TraceSink
}

// Runtime is the baseline local runtime implementation.
type Runtime struct {
	hooks                    hook.Set
	hookPurpose              hook.Purpose
	codeModeMu               sync.Mutex
	codeModeEnvironments     map[session.SessionRef]*codeModeEnvironment
	childApprovalRequester   approval.Requester
	sessions                 session.Service
	participantBindings      session.ParticipantBindingStore
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
	subagents                task.SubagentRunner
	sessionWrites            sessionWriteQueue
	// participantMu linearizes durable participant topology mutations with
	// source/target input reservations; remote receipt waits never hold it.
	participantMu        sync.Mutex
	participantInputPins map[participantPromptKey]int
	participantInputs    map[*participantInputReservation]struct{}
	mu                   sync.RWMutex
	activeRunners        map[string]activeRun
	approvalWaiters      map[approvalKey]approvalWaiter
	tasks                *taskRuntime
	taskOutput           task.OutputBinder
	lifecycle            agent.TraceOptions
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
		cfg.HookPurpose = hook.PurposeChat
	}
	switch cfg.HookPurpose {
	case hook.PurposeChat, hook.PurposeCompaction, hook.PurposeSystemAgent:
	default:
		return nil, fmt.Errorf("runtime: invalid hook purpose %q", cfg.HookPurpose)
	}
	r := &Runtime{
		hooks:                    cfg.Hooks.Clone(),
		hookPurpose:              cfg.HookPurpose,
		sessions:                 cfg.Sessions,
		agentFactory:             cfg.AgentFactory,
		runIDGenerator:           cfg.RunIDGenerator,
		clock:                    cfg.Clock,
		compaction:               normalizeCompactionConfig(cfg.Compaction),
		diagnostics:              cfg.Diagnostics,
		compactionRequests:       map[string]compactionRequestSnapshot{},
		idleCandidates:           map[string]*idleCompactionCandidate{},
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
		approvalWaiters:          map[approvalKey]approvalWaiter{},
		lifecycle: agent.TraceOptions{
			TraceSink: cfg.TraceSink,
			Clock:     cfg.Clock,
		},
	}
	if r.clock == nil {
		r.clock = time.Now
	}
	r.lifecycle.Clock = r.clock
	if r.hooks.ToolPolicy == nil && r.defaultPolicyMode != "" {
		return nil, errors.New("agent-sdk/runtime: default policy mode requires a policy registry")
	}
	if r.hooks.ToolPolicy != nil {
		r.defaultPolicyMode = policy.NormalizeModeName(r.defaultPolicyMode)
	}
	r.participantBindings, _ = cfg.Sessions.(session.ParticipantBindingStore)
	if err := validateControllerForwarder(cfg); err != nil {
		return nil, err
	}
	if (r.controllers != nil || r.subagents != nil) && r.participantBindings == nil {
		return nil, errors.New("agent-sdk/runtime: controllers and subagents require Session participant binding storage")
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

func (r *Runtime) nextID(prefix string, custom func() string) string {
	if custom != nil {
		if id := strings.TrimSpace(custom()); id != "" {
			return id
		}
	}
	return sdkctx.NewID(prefix)
}

func (r *Runtime) now() time.Time {
	if r != nil && r.clock != nil {
		return r.clock()
	}
	return time.Now()
}
