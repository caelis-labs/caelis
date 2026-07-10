package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

// Config defines one baseline local runtime instance.
type Config struct {
	Sessions                 session.Service
	AgentFactory             agent.AgentFactory
	RunIDGenerator           func() string
	Clock                    func() time.Time
	Compaction               CompactionConfig
	Compactor                compact.Engine
	PolicyRegistry           policy.Registry
	DefaultPolicyMode        string
	DefaultApprovalMode      string
	Controllers              controller.Backend
	ControllerEventForwarder agent.ControllerEventForwarder
	TaskStore                task.Store
	Subagents                subagent.Runner
}

// Runtime is the baseline local runtime implementation.
type Runtime struct {
	sessions                 session.Service
	agentFactory             agent.AgentFactory
	runIDGenerator           func() string
	clock                    func() time.Time
	compaction               CompactionConfig
	compactor                compact.Engine
	policies                 policy.Registry
	defaultPolicyMode        string
	defaultApprovalMode      approval.Mode
	controllers              controller.Backend
	controllerEventForwarder agent.ControllerEventForwarder
	subagents                subagent.Runner
	idCounter                atomic.Uint64
	executionMu              sync.Mutex
	mu                       sync.RWMutex
	runStates                map[string]agent.RunState
	activeRunners            map[string]activeRun
	approvalWaiters          map[string]chan agent.ApprovalResponse
	tasks                    *taskRuntime
	terminals                *streamService
}

// New returns one baseline local runtime.
func New(cfg Config) (*Runtime, error) {
	if cfg.Sessions == nil {
		return nil, errors.New("agent-sdk/runtime: sessions service is required")
	}
	if cfg.AgentFactory == nil {
		return nil, errors.New("agent-sdk/runtime: agent factory is required")
	}
	r := &Runtime{
		sessions:                 cfg.Sessions,
		agentFactory:             cfg.AgentFactory,
		runIDGenerator:           cfg.RunIDGenerator,
		clock:                    cfg.Clock,
		compaction:               normalizeCompactionConfig(cfg.Compaction),
		policies:                 cfg.PolicyRegistry,
		defaultPolicyMode:        strings.TrimSpace(cfg.DefaultPolicyMode),
		defaultApprovalMode:      approval.NormalizeMode(cfg.DefaultApprovalMode),
		controllers:              cfg.Controllers,
		controllerEventForwarder: cfg.ControllerEventForwarder,
		subagents:                cfg.Subagents,
		runStates:                map[string]agent.RunState{},
		activeRunners:            map[string]activeRun{},
		approvalWaiters:          map[string]chan agent.ApprovalResponse{},
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
		r.defaultPolicyMode = presets.ModeDefault
	}
	r.defaultPolicyMode = normalizePolicyMode(r.defaultPolicyMode)
	if err := validateControllerForwarder(cfg); err != nil {
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

func (r *Runtime) currentApprovalMode(state map[string]any) approval.Mode {
	if r == nil {
		return approval.CurrentMode(state)
	}
	return approval.CurrentModeOrDefault(state, r.defaultApprovalMode)
}

func validateControllerForwarder(cfg Config) error {
	if !requiresControllerForwarder(cfg) {
		return nil
	}
	if cfg.ControllerEventForwarder == nil {
		return errors.New("agent-sdk/runtime: controller event forwarder is required when ACP controllers are configured")
	}
	return nil
}

func requiresControllerForwarder(cfg Config) bool {
	return cfg.Controllers != nil
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
	if err := r.beginRun(ref, runID); err != nil {
		return agent.RunResult{}, err
	}
	if err := r.recoverIncompleteExecutionJournal(ctx, ref); err != nil {
		r.setRunState(ref.SessionID, agent.RunState{Status: agent.RunLifecycleStatusFailed, ActiveRunID: runID, LastError: err.Error(), UpdatedAt: r.now()})
		return agent.RunResult{}, err
	}
	if err := r.startRunTurnJournal(ctx, ref, runID, turnID); err != nil {
		r.setRunState(ref.SessionID, agent.RunState{Status: agent.RunLifecycleStatusFailed, ActiveRunID: runID, LastError: err.Error(), UpdatedAt: r.now()})
		return agent.RunResult{}, err
	}
	r.setRunState(ref.SessionID, agent.RunState{
		Status:      agent.RunLifecycleStatusRunning,
		ActiveRunID: runID,
		UpdatedAt:   r.now(),
	})
	runCtx, cancel := context.WithCancel(ctx)
	handle := newRunner(runID, cancel)
	handle.setCancelHook(func() error {
		return r.transitionRunTurnJournal(context.Background(), ref, runID, turnID, session.ExecutionCancelRequested, "run cancellation requested")
	})
	r.registerActiveRun(ref, activeSession, handle)
	go r.executeKernelTurn(runCtx, activeSession, ref, runID, turnID, req, handle)
	return agent.RunResult{
		Session: activeSession,
		Handle:  handle,
	}, nil
}

func (r *Runtime) beginRun(ref session.SessionRef, runID string) error {
	ref = session.NormalizeSessionRef(ref)
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.runStates[ref.SessionID]; ok {
		switch current.Status {
		case agent.RunLifecycleStatusRunning, agent.RunLifecycleStatusWaitingApproval:
			return &agent.RunConflictError{SessionRef: ref, ActiveRunID: current.ActiveRunID}
		}
	}
	r.runStates[ref.SessionID] = agent.RunState{
		Status:      agent.RunLifecycleStatusRunning,
		ActiveRunID: strings.TrimSpace(runID),
		UpdatedAt:   r.now(),
	}
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
) {
	defer handle.finish()
	defer r.unregisterActiveRun(runID)

	batch := make([]*session.Event, 0, 4)
	userEvent := buildUserEvent(activeSession, turnID, req.Input, req.DisplayInput, req.ContentParts)
	if err := r.runWithOverflowRecovery(ctx, activeSession, ref, runID, turnID, req, userEvent, &batch, handle); err != nil {
		journalStatus := session.ExecutionFailed
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			journalStatus = session.ExecutionCancelled
		}
		if journalErr := r.transitionRunTurnJournal(context.WithoutCancel(ctx), ref, runID, turnID, journalStatus, err.Error()); journalErr != nil {
			err = errors.Join(err, journalErr)
		}
		r.setRunState(ref.SessionID, agent.RunState{
			Status:      interruptedOrFailedStatus(ctx, err),
			ActiveRunID: runID,
			LastError:   err.Error(),
			UpdatedAt:   r.now(),
		})
		handle.publishError(err)
		return
	}
	if err := r.transitionRunTurnJournal(context.WithoutCancel(ctx), ref, runID, turnID, session.ExecutionSucceeded, ""); err != nil {
		r.setRunState(ref.SessionID, agent.RunState{Status: agent.RunLifecycleStatusFailed, ActiveRunID: runID, LastError: err.Error(), UpdatedAt: r.now()})
		handle.publishError(err)
		return
	}
	r.setRunState(ref.SessionID, agent.RunState{
		Status:      agent.RunLifecycleStatusCompleted,
		ActiveRunID: runID,
		UpdatedAt:   r.now(),
	})
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
