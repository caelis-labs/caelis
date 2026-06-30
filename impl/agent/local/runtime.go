package local

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	acpcontroller "github.com/OnslaughtSnail/caelis/impl/agent/acp/controller"
	acpsubagent "github.com/OnslaughtSnail/caelis/impl/agent/acp/subagent"
	"github.com/OnslaughtSnail/caelis/impl/policy/presets"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/policy"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	"github.com/OnslaughtSnail/caelis/ports/subagent"
	"github.com/OnslaughtSnail/caelis/ports/task"
)

const overflowCompactionRecoveryLimit = 3

// Config defines one baseline local runtime instance.
type Config struct {
	Sessions            session.Service
	AgentFactory        agent.AgentFactory
	RunIDGenerator      func() string
	Clock               func() time.Time
	Compaction          CompactionConfig
	Compactor           compact.Engine
	PolicyRegistry      policy.Registry
	DefaultPolicyMode   string
	DefaultApprovalMode string
	Assembly            assembly.ResolvedAssembly
	Controllers         controller.Backend
	TaskStore           task.Store
	Subagents           subagent.Runner
}

// Runtime is the baseline local runtime implementation.
type Runtime struct {
	sessions            session.Service
	agentFactory        agent.AgentFactory
	runIDGenerator      func() string
	clock               func() time.Time
	compaction          CompactionConfig
	compactor           compact.Engine
	policies            policy.Registry
	defaultPolicyMode   string
	defaultApprovalMode gateway.ApprovalMode
	assembly            assembly.ResolvedAssembly
	acpRegistry         *acpsubagent.Registry
	controllers         controller.Backend
	subagents           subagent.Runner
	idCounter           atomic.Uint64
	mu                  sync.RWMutex
	runStates           map[string]agent.RunState
	tasks               *taskRuntime
	terminals           *streamService
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
		sessions:            cfg.Sessions,
		agentFactory:        cfg.AgentFactory,
		runIDGenerator:      cfg.RunIDGenerator,
		clock:               cfg.Clock,
		compaction:          normalizeCompactionConfig(cfg.Compaction),
		policies:            cfg.PolicyRegistry,
		defaultPolicyMode:   strings.TrimSpace(cfg.DefaultPolicyMode),
		defaultApprovalMode: gateway.NormalizeApprovalMode(cfg.DefaultApprovalMode),
		assembly:            assembly.CloneResolvedAssembly(cfg.Assembly),
		controllers:         cfg.Controllers,
		subagents:           cfg.Subagents,
		runStates:           map[string]agent.RunState{},
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

func (r *Runtime) currentApprovalMode(state map[string]any) gateway.ApprovalMode {
	if r == nil {
		return gateway.CurrentApprovalMode(state)
	}
	return gateway.CurrentApprovalModeOrDefault(state, r.defaultApprovalMode)
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
	userEvent := buildUserEvent(activeSession, turnID, req.Input, req.DisplayInput, req.ContentParts)
	if err := r.runWithOverflowRecovery(ctx, activeSession, ref, runID, turnID, req, userEvent, &batch, handle); err != nil {
		r.setRunState(ref.SessionID, agent.RunState{
			Status:      interruptedOrFailedStatus(ctx, err),
			ActiveRunID: runID,
			LastError:   err.Error(),
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
