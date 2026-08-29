package subagent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/control/acppermission"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/authentication"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/endpoint"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpcleanup"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpingress"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acputil"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/sessionconfig"
	"github.com/caelis-labs/caelis/internal/acpbridge"
	"github.com/google/uuid"
)

type PermissionHandler func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error)

// PlacementResolver materializes a durable model placement at the external
// effect boundary. The product host owns model configuration and must reject a
// placement whose recorded configuration no longer matches current state.
type PlacementResolver func(context.Context, subagent.SpawnContext, delegation.TargetRequest) (AgentConfig, error)

// SessionPreparer lets the owning Control Host apply process-local preparation
// after the child has created or resumed its durable Session and before generic
// ACP defaults are applied. The returned options are the remaining wire-level
// defaults; product-only values must be consumed by the callback.
type SessionPreparer func(context.Context, subagent.SpawnContext, string, AgentConfig) (controlagents.SessionOptions, error)

type PermissionBridge interface {
	RequestPermission(context.Context, PermissionRequest) (client.RequestPermissionResponse, error)
}

type PermissionRequest struct {
	Spawn   subagent.SpawnContext
	Agent   delegation.Agent
	AgentID string
	Request client.RequestPermissionRequest
}

type RunnerConfig struct {
	Registry          *Registry
	ClientInfo        *acpsdk.Implementation
	Clock             func() time.Time
	PermissionHandler PermissionHandler
	PermissionBridge  PermissionBridge
	PlacementResolver PlacementResolver
	SessionPreparer   SessionPreparer
	EndpointResolver  endpoint.Resolver
}

type Runner struct {
	registry          *Registry
	clientInfo        *acpsdk.Implementation
	clock             func() time.Time
	permissionHandler PermissionHandler
	permissionBridge  PermissionBridge
	placementResolver PlacementResolver
	sessionPreparer   SessionPreparer
	endpointResolver  endpoint.Resolver

	counter atomic.Uint64
	mu      sync.RWMutex
	runs    map[string]*childRun
	slots   map[string]*childSlot
}

type childRun struct {
	anchor                delegation.Anchor
	agentName             string
	invocation            session.EventInvocation
	client                *client.Client
	configuredAuth        controlagents.Authentication
	authenticationMethods []controlagents.AuthenticationMethod
	spawn                 subagent.SpawnContext
	supportsSteering      bool
	promptCapabilities    acpsdk.PromptCapabilities
	taskID                string
	sink                  stream.Sink
	completion            delegation.CompletionSink
	ctx                   context.Context
	cancel                context.CancelFunc
	slot                  *childSlot

	mu              sync.RWMutex
	state           delegation.State
	outputPreview   string
	actionSummary   subagentActionSummary
	failureDetail   string
	result          string
	agentText       string
	finalAssistant  acpbridge.FinalAssistantAccumulator
	inputActor      session.ActorRef
	updatedAt       time.Time
	running         bool
	finishing       bool
	cancelRequested bool
	cancelFailed    bool
	cancelResolved  chan struct{}
	done            chan struct{}
}

func (run *childRun) childSlot() *childSlot {
	if run == nil {
		return nil
	}
	run.mu.RLock()
	defer run.mu.RUnlock()
	return run.slot
}

func (run *childRun) installChildSlot(slot *childSlot) {
	if run == nil || slot == nil {
		return
	}
	run.mu.Lock()
	if run.slot == nil {
		run.slot = slot
	}
	run.mu.Unlock()
}

func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.Registry == nil {
		return nil, fmt.Errorf("agent registry is required")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Runner{
		registry:          cfg.Registry,
		clientInfo:        cfg.ClientInfo,
		clock:             clock,
		permissionHandler: cfg.PermissionHandler,
		permissionBridge:  cfg.PermissionBridge,
		placementResolver: cfg.PlacementResolver,
		sessionPreparer:   cfg.SessionPreparer,
		endpointResolver:  cfg.EndpointResolver,
		runs:              map[string]*childRun{},
		slots:             map[string]*childSlot{},
	}, nil
}

func (r *Runner) Spawn(ctx context.Context, spawn subagent.SpawnContext, req delegation.Request) (delegation.Anchor, delegation.Result, error) {
	req = delegation.CloneRequest(req)
	return r.SpawnTarget(ctx, spawn, delegation.TargetRequest{Target: delegation.AgentTarget(req.Agent), Prompt: req.Prompt})
}

func (r *Runner) SpawnTarget(ctx context.Context, spawn subagent.SpawnContext, req delegation.TargetRequest) (delegation.Anchor, delegation.Result, error) {
	req = delegation.CloneTargetRequest(req)
	if err := delegation.ValidateTarget(req.Target); err != nil {
		return delegation.Anchor{}, delegation.Result{}, err
	}
	cfg, err := r.resolveSpawnConfig(ctx, spawn, req)
	if err != nil {
		return delegation.Anchor{}, delegation.Result{}, err
	}
	agentID := r.stableAgentID(cfg.Name, spawn.TaskID)
	role := spawn.Role
	if role == "" {
		role = session.ParticipantRoleDelegated
	}
	run := &childRun{
		anchor: delegation.Anchor{
			TaskID: strings.TrimSpace(spawn.TaskID), AgentID: agentID,
		},
		state:          delegation.StateRunning,
		running:        true,
		taskID:         strings.TrimSpace(spawn.TaskID),
		sink:           spawn.Streams,
		completion:     spawn.Completion,
		updatedAt:      r.clock(),
		done:           make(chan struct{}),
		agentName:      strings.TrimSpace(cfg.Name),
		invocation:     session.EventInvocation{Provider: strings.TrimSpace(cfg.Name), Model: strings.TrimSpace(req.Target.Placement.Model)},
		configuredAuth: controlagents.NormalizeAuthentication(cfg.Authentication),
		spawn:          spawn,
	}
	detachedCtx := detachedChildContext(ctx)
	childCtx, childCancel := context.WithCancel(detachedCtx)
	run.ctx = childCtx
	run.cancel = childCancel
	target := agent.ChildEndpointRef{
		ParticipantID: agentID,
		EndpointKey:   strings.TrimSpace(spawn.TaskID),
		Role:          role,
		Placement:     req.Target.Placement,
	}
	slot := newPendingChildSlot(target, run)
	slot.beginSetup(run)
	if spawn.ActivityObserver != nil {
		slot.bindObserver(spawn.ActivityAfterCursor, spawn.ActivityObserver)
	} else {
		slot.bindObserver(0, compatibilityActivityObserver{run: run})
	}
	launchEnv := maps.Clone(cfg.Env)
	if strings.EqualFold(strings.TrimSpace(cfg.Name), "self") {
		if launchEnv == nil {
			launchEnv = map[string]string{}
		}
		launchEnv["SDK_ACP_ENABLE_SPAWN"] = "0"
		launchEnv["SDK_ACP_CHILD_NO_SPAWN"] = "1"
	}
	acpClient, err := client.Start(childCtx, client.Config{
		HostedAdapterID:  cfg.HostedAdapterID,
		ConnectionID:     cfg.Name,
		EndpointResolver: r.endpointResolver,
		Command:          cfg.Command,
		Args:             append([]string(nil), cfg.Args...),
		Env:              launchEnv,
		WorkDir:          pickWorkDir(cfg.WorkDir, spawn.CWD),
		ClientInfo:       r.clientInfo,
		OnUpdate:         func(env client.UpdateEnvelope) { r.handleUpdate(run, env) },
		OnPermissionRequest: func(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			return r.permissionCallback(spawn, cfg, agentID)(ctx, req)
		},
	})
	if err != nil {
		childCancel()
		return delegation.Anchor{}, delegation.Result{}, err
	}
	run.mu.Lock()
	run.client = acpClient
	run.mu.Unlock()
	initialize, err := acpClient.Initialize(ctx)
	if err != nil {
		setupErr := spawnedACPSetupError("initialize", cfg, err)
		childCancel()
		_ = acpClient.Close(ctx)
		return delegation.Anchor{}, delegation.Result{}, setupErr
	}
	supportsSteering, err := client.SupportsSessionSteering(initialize)
	if err != nil {
		setupErr := spawnedACPSetupError("negotiate steering with", cfg, err)
		childCancel()
		closeErr := acpcleanup.CloseClient(ctx, acpClient)
		return delegation.Anchor{}, delegation.Result{}, errors.Join(setupErr, closeErr)
	}
	authenticationMethods := authentication.Methods(initialize)
	recovered, err := authentication.OpenNewSession(ctx, authentication.RecoveryConfig{
		Mode:           authentication.RecoveryConfigured,
		Client:         acpClient,
		Initialize:     initialize,
		Methods:        authenticationMethods,
		AgentID:        cfg.Name,
		Authentication: cfg.Authentication,
	}, spawn.CWD, subagentSessionMeta(spawn))
	if err != nil {
		setupErr := spawnedACPSetupError("open Session for", cfg, err)
		childCancel()
		_ = acpClient.Close(ctx)
		return delegation.Anchor{}, delegation.Result{}, setupErr
	}
	sessionResp := recovered.Value
	sessionID := strings.TrimSpace(sessionResp.SessionID)
	sessionOptions := cfg.SessionOptions
	if r.sessionPreparer != nil {
		sessionOptions, err = r.sessionPreparer(ctx, spawn, sessionID, cfg)
		if err != nil {
			childCancel()
			if hasACPSessionCapability(initialize, "close") {
				_ = acpcleanup.CloseSession(ctx, acpClient, sessionID)
			}
			_ = acpcleanup.CloseClient(ctx, acpClient)
			return delegation.Anchor{}, delegation.Result{}, spawnedACPSetupError("prepare Session for", cfg, err)
		}
	}
	if _, err := sessionconfig.Apply(ctx, acpClient, sessionID, sessionconfig.State{
		ConfigOptions: sessionResp.ConfigOptions,
		Models:        sessionResp.Models,
	}, sessionOptions); err != nil {
		childCancel()
		if hasACPSessionCapability(initialize, "close") {
			_ = acpcleanup.CloseSession(ctx, acpClient, sessionID)
		}
		_ = acpcleanup.CloseClient(ctx, acpClient)
		return delegation.Anchor{}, delegation.Result{}, spawnedACPSetupError("configure Session for", cfg, err)
	}
	anchor := delegation.Anchor{
		TaskID:    strings.TrimSpace(spawn.TaskID),
		SessionID: sessionID,
		AgentID:   agentID,
	}
	run.mu.Lock()
	run.anchor = anchor
	run.authenticationMethods = controlagents.CloneAuthenticationMethods(authenticationMethods)
	run.supportsSteering = supportsSteering
	run.promptCapabilities = initialize.AgentCapabilities.PromptCapabilities
	run.mu.Unlock()
	runKey, err := childRunKey(anchor)
	if err != nil {
		childCancel()
		_ = acpClient.Close(ctx)
		return delegation.Anchor{}, delegation.Result{}, err
	}
	target = agent.ChildEndpointRef{
		ParticipantID: anchor.AgentID,
		SessionID:     anchor.SessionID,
		EndpointKey:   anchor.TaskID,
		Role:          role,
		Placement:     req.Target.Placement,
	}
	r.mu.Lock()
	if existing := r.runs[runKey]; existing != nil || r.slots[runKey] != nil {
		r.mu.Unlock()
		childCancel()
		_ = acpClient.Close(ctx)
		return delegation.Anchor{}, delegation.Result{}, fmt.Errorf("subagent run %q is already registered", runKey)
	}
	r.runs[runKey] = run
	r.slots[runKey] = slot
	r.mu.Unlock()
	if err := slot.finalizeTarget(target); err != nil {
		r.mu.Lock()
		if r.runs[runKey] == run {
			delete(r.runs, runKey)
		}
		if r.slots[runKey] == slot {
			delete(r.slots, runKey)
		}
		r.mu.Unlock()
		childCancel()
		_ = acpClient.Close(ctx)
		return delegation.Anchor{}, delegation.Result{}, err
	}
	slot.beginInitialActivity(uuid.NewString(), run)
	dispatchCtx, cancelDispatch := context.WithCancel(ctx)
	dispatchDone := slot.beginPromptDispatch(cancelDispatch)
	r.dispatchInitialPrompt(dispatchCtx, childCtx, slot, run, dispatchDone, strings.TrimSpace(req.Prompt))
	cancelDispatch()
	return anchor, r.waitRun(ctx, run, 0), nil
}

func (r *Runner) dispatchInitialPrompt(
	callerCtx context.Context,
	producerCtx context.Context,
	slot *childSlot,
	run *childRun,
	dispatchDone chan struct{},
	promptText string,
) {
	prompt := acputil.BuildPromptParts(promptText, nil)
	responseCtx, cancelResponse := context.WithCancel(producerCtx)
	prepared, err := run.client.PreparePromptParts(run.anchor.SessionID, prompt, nil)
	fence := newPromptAuthRetryFence(slot, dispatchDone, cancelResponse)
	if err == nil {
		err = prepared.ObserveAuthRequired(fence.observeAuthRequired)
	}
	if err == nil {
		err = prepared.DispatchWithAbort(callerCtx, func() {
			_ = run.client.Close(context.Background())
		})
	}
	slot.opMu.Lock()
	fence.finishLocked(dispatchDone)
	slot.opMu.Unlock()
	if err != nil {
		cancelResponse()
		if prepared != nil {
			prepared.Abandon()
		}
		if client.DispatchMayHaveCommitted(err) {
			err = joinChildInputUnknown("Initial subagent message delivery outcome cannot be confirmed.", err)
		}
		go func() {
			r.finishDrive(producerCtx, run, "", err)
			fence.closeAndFinishCurrent()
		}()
		return
	}
	go r.drivePreparedPrompt(responseCtx, run, prepared, prompt, fence)
}

func spawnedACPSetupError(stage string, cfg AgentConfig, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf(
		"%s spawned ACP child %q: %w",
		strings.TrimSpace(stage),
		firstNonEmpty(strings.TrimSpace(cfg.Name), "unknown"),
		err,
	)
}

func (r *Runner) resolveSpawnConfig(ctx context.Context, spawn subagent.SpawnContext, req delegation.TargetRequest) (AgentConfig, error) {
	placement := delegation.NormalizePlacement(req.Target.Placement)
	switch placement.Kind {
	case delegation.PlacementModel:
		if r.placementResolver == nil {
			return AgentConfig{}, fmt.Errorf("model placement resolution is unavailable")
		}
		cfg, err := r.placementResolver(ctx, spawn, req)
		if err != nil {
			return AgentConfig{}, err
		}
		cfg = normalizeAgentConfig(cfg)
		if cfg.Name == "" || validateAgentEndpoint(cfg) != nil {
			return AgentConfig{}, fmt.Errorf("model placement resolved an invalid Agent configuration")
		}
		return cfg, nil
	case delegation.PlacementAgent:
		if placement.ConfigFingerprint != "" {
			if r.placementResolver == nil {
				return AgentConfig{}, fmt.Errorf("configured placement resolution is unavailable")
			}
			cfg, err := r.placementResolver(ctx, spawn, req)
			if err != nil {
				return AgentConfig{}, err
			}
			cfg = normalizeAgentConfig(cfg)
			if cfg.Name == "" || validateAgentEndpoint(cfg) != nil {
				return AgentConfig{}, fmt.Errorf("configured placement resolved an invalid Agent configuration")
			}
			return cfg, nil
		}
		return r.registry.Resolve(strings.TrimSpace(placement.Agent))
	case "":
		return AgentConfig{}, fmt.Errorf("subagent placement is required")
	default:
		return AgentConfig{}, fmt.Errorf("unsupported subagent placement kind %q", placement.Kind)
	}
}

func hasACPSessionCapability(resp client.InitializeResponse, name string) bool {
	if resp.AgentCapabilities.SessionCapabilities == nil {
		return false
	}
	_, ok := resp.AgentCapabilities.SessionCapabilities[strings.TrimSpace(name)]
	return ok
}

func (r *Runner) Wait(ctx context.Context, anchor delegation.Anchor, yieldTimeMS int) (delegation.Result, error) {
	run, err := r.lookup(anchor)
	if err != nil {
		return delegation.Result{}, err
	}
	return r.waitRun(ctx, run, yieldTimeMS), nil
}

func delegationStateCanStartTurn(state delegation.State) bool {
	switch state {
	case delegation.StateCompleted,
		delegation.StateFailed,
		delegation.StateCancelled,
		delegation.StateInterrupted,
		delegation.StateUnknownOutcome:
		return true
	default:
		return false
	}
}

func detachedChildContext(ctx context.Context) context.Context {
	return session.ContextWithoutRuntimeFence(context.WithoutCancel(ctx))
}

func subagentSessionMeta(spawn subagent.SpawnContext) map[string]any {
	return acputil.NewSubagentSessionMeta(spawn.SessionRef.SessionID, spawn.TaskID, "")
}

func (r *Runner) Cancel(ctx context.Context, anchor delegation.Anchor) error {
	run, err := r.lookup(anchor)
	if err != nil {
		return err
	}
	var slot *childSlot
	if runSlot := run.childSlot(); runSlot != nil {
		slot = runSlot
		_, cancelInput := slot.revokeActiveInput()
		if cancelInput != nil {
			cancelInput()
		}
		slot.mu.Lock()
		dispatching := slot.promptDispatchDone != nil
		slot.mu.Unlock()
		if dispatching {
			run.mu.RLock()
			acpClient := run.client
			run.mu.RUnlock()
			if acpClient != nil {
				_ = acpClient.Close(context.WithoutCancel(ctx))
			}
		}
		slot.opMu.Lock()
		defer slot.opMu.Unlock()
	}
	run.mu.Lock()
	if !run.running || run.finishing {
		run.mu.Unlock()
		return nil
	}
	run.finishing = true
	run.cancelRequested = true
	run.cancelResolved = make(chan struct{})
	client := run.client
	sessionID := run.anchor.SessionID
	turnDone := run.done
	cancelResolved := run.cancelResolved
	run.mu.Unlock()
	var remoteErr error
	if client != nil {
		remoteErr = client.Cancel(ctx, sessionID)
	}
	run.mu.Lock()
	if run.done != turnDone {
		close(cancelResolved)
		run.mu.Unlock()
		return remoteErr
	}
	run.cancelFailed = remoteErr != nil
	run.updatedAt = r.clock()
	close(cancelResolved)
	run.mu.Unlock()
	if run.cancel != nil {
		run.cancel()
	}
	return remoteErr
}

// Quiesce cancels every process-local child run and waits for its terminal
// producer boundary. Host shutdown owns this barrier; it does not add a second
// durable Task state or execution lease.
func (r *Runner) Quiesce(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	runs := make([]*childRun, 0, len(r.runs))
	for _, run := range r.runs {
		if run != nil {
			runs = append(runs, run)
		}
	}
	r.mu.RUnlock()

	// First revoke every child context. One unresponsive transport must not
	// prevent later children from receiving the Host shutdown signal.
	for _, run := range runs {
		if slot := run.childSlot(); slot != nil {
			_, cancelInput := slot.revokeActiveInput()
			if cancelInput != nil {
				cancelInput()
			}
			slot.mu.Lock()
			dispatching := slot.promptDispatchDone != nil
			slot.mu.Unlock()
			if dispatching {
				run.mu.RLock()
				acpClient := run.client
				run.mu.RUnlock()
				if acpClient != nil {
					_ = acpClient.Close(context.WithoutCancel(ctx))
				}
			}
		}
		run.mu.RLock()
		running := run.running
		cancel := run.cancel
		run.mu.RUnlock()
		if running && cancel != nil {
			cancel()
		}
	}

	var errs []error
	for _, run := range runs {
		run.mu.RLock()
		anchor := delegation.CloneAnchor(run.anchor)
		done := run.done
		run.mu.RUnlock()
		if done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				errs = append(errs, fmt.Errorf("wait child Task %q: %w", anchor.TaskID, ctx.Err()))
			}
		}
		run.mu.RLock()
		acpClient := run.client
		run.mu.RUnlock()
		if acpClient != nil {
			if err := acpClient.Close(ctx); err != nil {
				errs = append(errs, fmt.Errorf("close child Task %q: %w", anchor.TaskID, err))
			}
		}
	}
	return errors.Join(errs...)
}

func (r *Runner) finishDrive(ctx context.Context, run *childRun, stopReason string, err error) {
	if run == nil {
		return
	}
	slot := run.childSlot()
	if slot != nil {
		slot.opMu.Lock()
	}
	done := r.finishDriveLocked(ctx, run, stopReason, err)
	if slot != nil {
		slot.opMu.Unlock()
	}
	if done != nil {
		<-done
	}
}

func (r *Runner) finishDriveLocked(ctx context.Context, run *childRun, stopReason string, err error) <-chan struct{} {
	run.mu.Lock()
	if !run.running {
		run.mu.Unlock()
		return nil
	}
	if run.cancelRequested && run.cancelResolved != nil {
		cancelResolved := run.cancelResolved
		run.mu.Unlock()
		<-cancelResolved
		run.mu.Lock()
	}
	run.running = false
	run.finishing = true
	run.updatedAt = r.clock()
	closeClient := false
	if run.cancelRequested {
		if run.cancelFailed {
			run.state = delegation.StateInterrupted
			run.outputPreview = "cancellation outcome unknown"
			run.failureDetail = "subagent cancellation failed"
		} else {
			run.state = delegation.StateCancelled
			run.outputPreview = "cancelled"
			run.failureDetail = ""
		}
		run.result = ""
		closeClient = true
	} else if err != nil {
		if errorcode.Is(err, errorcode.UnknownOutcome) {
			run.state = delegation.StateUnknownOutcome
			run.failureDetail = strings.TrimSpace(err.Error())
			run.outputPreview = run.failureDetail
			run.result = ""
			closeClient = true
		} else if errors.Is(err, context.Canceled) {
			if run.state != delegation.StateCancelled {
				run.state = delegation.StateInterrupted
				run.outputPreview = "interrupted"
				run.failureDetail = "interrupted"
			}
			run.result = ""
			closeClient = true
		} else {
			run.state = delegation.StateFailed
			run.failureDetail = subagentPromptFailureDetail(err)
			run.outputPreview = run.failureDetail
			run.result = ""
			closeClient = true
		}
	} else if strings.EqualFold(strings.TrimSpace(stopReason), "cancelled") {
		run.state = delegation.StateCancelled
		run.outputPreview = "cancelled"
		run.failureDetail = ""
		run.result = ""
		closeClient = true
	} else {
		run.state = delegation.StateCompleted
		run.outputPreview = truncateMiddleUTF8(run.outputPreview, maxSubagentPreviewBytes)
		run.failureDetail = ""
	}
	done := run.done
	if done == nil {
		done = make(chan struct{})
		run.done = done
	}
	slot := run.slot
	acpClient := run.client
	run.mu.Unlock()

	if closeClient && acpClient != nil {
		_ = acpClient.Close(context.WithoutCancel(ctx))
	}
	if slot != nil {
		slot.beginTerminalSettlement(done)
		ack := slot.publishRunResult(run)
		go func() {
			<-ack
			run.mu.Lock()
			run.finishing = false
			run.mu.Unlock()
			slot.finishTerminalSettlement(done)
			// done is the full producer barrier: terminal observation is
			// durable and the endpoint admission fence is clear.
			close(done)
		}()
		return done
	}
	run.mu.RLock()
	completion := run.completion
	result := childResultLocked(run)
	run.mu.RUnlock()
	if completion != nil {
		completion.PublishSubagentCompletion(result)
	}
	run.mu.Lock()
	run.finishing = false
	run.mu.Unlock()
	// done is the full producer barrier: completion publication may persist the
	// durable Task terminal record, so Host quiesce must not cross it early.
	close(done)
	return done
}

func (r *Runner) waitRun(ctx context.Context, run *childRun, yieldTimeMS int) delegation.Result {
	if run == nil {
		return delegation.Result{}
	}
	wait := time.Duration(yieldTimeMS) * time.Millisecond
	if wait < 0 {
		wait = 0
	}
	if wait > 0 {
		run.mu.RLock()
		done := run.done
		run.mu.RUnlock()
		select {
		case <-ctx.Done():
		case <-done:
		case <-time.After(wait):
		}
	}
	run.mu.RLock()
	defer run.mu.RUnlock()
	return childResultLocked(run)
}

func childResultLocked(run *childRun) delegation.Result {
	if run == nil {
		return delegation.Result{}
	}
	out := delegation.Result{
		TaskID:        strings.TrimSpace(run.taskID),
		State:         run.state,
		Running:       run.running,
		Yielded:       run.running,
		OutputPreview: strings.TrimSpace(run.outputPreview),
		Error:         strings.TrimSpace(run.failureDetail),
		Result:        "",
		UpdatedAt:     run.updatedAt,
	}
	if !run.running {
		out.Result = strings.TrimSpace(run.result)
	}
	return delegation.CloneResult(out)
}

func (r *Runner) lookup(anchor delegation.Anchor) (*childRun, error) {
	key, err := childRunKey(anchor)
	if err != nil {
		return nil, err
	}
	r.mu.RLock()
	run := r.runs[key]
	r.mu.RUnlock()
	if run == nil {
		return nil, fmt.Errorf("subagent run %q was not found", key)
	}
	// Defensive isolation: reject anchors whose remote session drifted from the
	// registered child (two endpoints must not share a process-local binding).
	if sessionID := strings.TrimSpace(anchor.SessionID); sessionID != "" &&
		strings.TrimSpace(run.anchor.SessionID) != "" &&
		sessionID != strings.TrimSpace(run.anchor.SessionID) {
		return nil, fmt.Errorf("subagent run %q belongs to another Session", key)
	}
	return run, nil
}

// childRunKey isolates process-local child runs by durable TaskID so two remote
// endpoints that both return a common session id (for example "session-1") cannot
// overwrite each other.
func childRunKey(anchor delegation.Anchor) (string, error) {
	taskID := strings.TrimSpace(anchor.TaskID)
	if taskID == "" {
		return "", fmt.Errorf("subagent task identity is required")
	}
	return taskID, nil
}

// stableAgentID binds participant identity to the durable spawn TaskID so process
// restarts cannot reissue a short counter ID that collides with a prior binding.
func (r *Runner) stableAgentID(name string, taskID string) string {
	taskID = strings.TrimSpace(taskID)
	if taskID != "" {
		return taskID
	}
	return r.nextAgentID(name)
}

func (r *Runner) nextAgentID(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		name = "agent"
	}
	return fmt.Sprintf("%s-%03d", name, r.counter.Add(1))
}

func (r *Runner) permissionCallback(spawn subagent.SpawnContext, cfg AgentConfig, agentID string) PermissionHandler {
	if r.permissionBridge != nil {
		return func(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			resp, err := r.permissionBridge.RequestPermission(ctx, PermissionRequest{
				Spawn: spawn,
				Agent: delegation.Agent{
					Name:        strings.TrimSpace(cfg.Name),
					Description: strings.TrimSpace(cfg.Description),
				},
				AgentID: strings.TrimSpace(agentID),
				Request: req,
			})
			if err != nil {
				return client.RequestPermissionResponse{}, err
			}
			return resp, nil
		}
	}
	if r.permissionHandler != nil {
		return r.permissionHandler
	}
	return func(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
		if spawn.ApprovalRequester != nil {
			approval, err := translateApprovalRequest(spawn, cfg, agentID, req)
			if err != nil {
				return client.RequestPermissionResponse{}, err
			}
			resp, err := spawn.ApprovalRequester.RequestSubagentApproval(ctx, approval)
			if err != nil {
				return client.RequestPermissionResponse{}, err
			}
			if selected, ok := acputil.SelectedOutcome(resp.Outcome, resp.OptionID); ok {
				return selected, nil
			}
		}
		return acputil.RejectOnce(), nil
	}
}

func translateApprovalRequest(
	spawn subagent.SpawnContext,
	cfg AgentConfig,
	agentID string,
	req client.RequestPermissionRequest,
) (subagent.ApprovalRequest, error) {
	wire, err := acpingress.PermissionRequest(req)
	if err != nil {
		return subagent.ApprovalRequest{}, err
	}
	approval, err := acppermission.DecodePermissionRequest(wire)
	if err != nil {
		return subagent.ApprovalRequest{}, err
	}
	options := make([]subagent.ApprovalOption, 0, len(approval.Options))
	for _, item := range approval.Options {
		options = append(options, subagent.ApprovalOption{
			ID:   strings.TrimSpace(item.ID),
			Name: strings.TrimSpace(item.Name),
			Kind: strings.TrimSpace(item.Kind),
		})
	}
	return subagent.ApprovalRequest{
		SessionRef:   session.NormalizeSessionRef(spawn.SessionRef),
		Session:      session.CloneSession(spawn.Session),
		TaskID:       strings.TrimSpace(spawn.TaskID),
		ParentCallID: strings.TrimSpace(spawn.ParentCallID),
		Agent:        firstNonEmpty(strings.TrimSpace(cfg.Name), strings.TrimSpace(agentID)),
		Mode:         strings.TrimSpace(spawn.Mode),
		ToolCall: subagent.ApprovalToolCall{
			ID:        strings.TrimSpace(approval.ToolCall.ID),
			Name:      strings.TrimSpace(approval.ToolCall.Name),
			Kind:      strings.TrimSpace(approval.ToolCall.Kind),
			Title:     strings.TrimSpace(approval.ToolCall.Title),
			Status:    strings.TrimSpace(approval.ToolCall.Status),
			RawInput:  session.CloneState(approval.ToolCall.RawInput),
			RawOutput: session.CloneState(approval.ToolCall.RawOutput),
			Content:   session.CloneProtocolToolCallContent(approval.ToolCall.Content),
		},
		Options: options,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func trimStringPtr(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func subagentPromptFailureDetail(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "subagent prompt timed out"
	}
	return "subagent prompt failed"
}

func pickWorkDir(preferred string, fallback string) string {
	if text := strings.TrimSpace(preferred); text != "" {
		return text
	}
	return strings.TrimSpace(fallback)
}

func (r *Runner) handleUpdate(run *childRun, env client.UpdateEnvelope) {
	if run == nil {
		return
	}
	slot := run.childSlot()
	if slot != nil {
		slot.ingressMu.Lock()
		defer slot.ingressMu.Unlock()
	}
	setupOutput := slot != nil && slot.acceptsSetupOutput(run)
	env.Update = acputil.StripTerminalConsoleFenceUpdate(env.Update)
	var event *session.Event
	var frame *stream.Frame
	run.mu.Lock()
	acceptOutput := run.running || setupOutput
	if !acceptOutput {
		run.mu.Unlock()
		return
	}
	run.updatedAt = r.clock()
	switch update := env.Update.(type) {
	case client.ContentChunk:
		if text := chunkText(update); text != "" {
			switch strings.TrimSpace(update.SessionUpdate) {
			case client.UpdateUserMessage:
				event = run.acpUpdateEvent(env, run.updatedAt)
				markSubagentInputEvent(event, run.inputActor)
			case client.UpdateAgentMessage:
				if acceptOutput {
					textOverride, messageID := run.appendAgentMessageChunkLocked(update.MessageID, text)
					update.MessageID = messageID
					env.Update = update
					run.actionSummary.observeAssistant(messageID, run.agentText)
					run.outputPreview = run.actionSummary.previewOrEmpty()
					if textOverride != "" {
						event = run.acpUpdateEvent(env, run.updatedAt, textOverride)
					}
				}
			case client.UpdateAgentThought:
				if acceptOutput {
					run.clearFinalAssistantLocked()
					run.actionSummary.observeThought(update.MessageID, text)
					run.outputPreview = run.actionSummary.previewOrEmpty()
				}
				event = run.acpUpdateEvent(env, run.updatedAt)
			default:
				break
			}
		}
	case client.ToolCall:
		if acceptOutput {
			run.clearFinalAssistantLocked()
			run.actionSummary.observeTool(update.ToolCallID, toolActivity(update.Title, update.Kind, update.Status), toolContentActivity(update.Content))
			run.outputPreview = run.actionSummary.previewOrEmpty()
		}
		event = run.acpUpdateEvent(env, run.updatedAt)
	case client.ToolCallUpdate:
		if acceptOutput {
			run.clearFinalAssistantLocked()
			run.actionSummary.observeTool(update.ToolCallID, toolActivity(derefString(update.Title), derefString(update.Kind), derefString(update.Status)), toolContentActivity(update.Content))
			run.outputPreview = run.actionSummary.previewOrEmpty()
		}
		event = run.acpUpdateEvent(env, run.updatedAt)
	case client.PlanUpdate:
		if acceptOutput {
			run.clearFinalAssistantLocked()
			run.actionSummary.observeAction(planActivity(update.Entries))
			run.outputPreview = run.actionSummary.previewOrEmpty()
		}
		event = run.acpUpdateEvent(env, run.updatedAt)
	case client.UsageUpdate:
		event = run.acpUpdateEvent(env, run.updatedAt)
	}
	if event != nil {
		frameState := run.state
		frameRunning := run.running
		if setupOutput {
			frameState = delegation.StateRunning
			frameRunning = true
		}
		next := stream.Frame{
			Ref: stream.Ref{
				TaskID:    firstNonEmpty(run.taskID, run.anchor.TaskID),
				SessionID: firstNonEmpty(strings.TrimSpace(env.SessionID), run.anchor.SessionID),
			},
			State:     string(frameState),
			Running:   frameRunning,
			Event:     event,
			UpdatedAt: run.updatedAt,
		}
		frame = &next
	}
	run.mu.Unlock()
	if frame != nil {
		run.emit(*frame)
	}
}

func markSubagentInputEvent(event *session.Event, source session.ActorRef) session.ActorRef {
	if event == nil {
		return session.ActorRef{}
	}
	// ACP calls this a user_message because it is the input side of the child
	// transcript. Within the parent Task stream it is observed Agent input, not
	// a canonical end-user submission to the parent Session.
	event.Type = session.EventTypeContext
	if session.ValidateAgentCommunicationActor(source) != nil {
		source = session.ActorRef{Kind: session.ActorKindController, ID: "parent", Name: "parent"}
	}
	event.Actor = session.CloneActorRef(source)
	header := session.AgentCommunicationPromptHeader(event.Actor)
	event.Text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(event.Text), header))
	protocol := session.NewAgentCommunicationProtocol(session.ProtocolAgentCommunication{Text: event.Text})
	event.Protocol = &protocol
	if event.Scope != nil {
		event.Scope.Source = "agent_input"
	}
	return event.Actor
}

func (run *childRun) acpUpdateEvent(env client.UpdateEnvelope, at time.Time, textOverride ...string) *session.Event {
	if run == nil {
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	scope := &session.EventScope{
		Source: "acp_subagent",
		Controller: session.ControllerRef{
			Kind: session.ControllerKindACP,
			ID:   strings.TrimSpace(firstNonEmpty(run.agentName, run.anchor.AgentID)),
		},
		Participant: session.ParticipantRef{
			ID:           strings.TrimSpace(firstNonEmpty(run.anchor.AgentID, run.taskID, run.anchor.TaskID)),
			Kind:         session.ParticipantKindSubagent,
			Role:         session.ParticipantRoleDelegated,
			DelegationID: strings.TrimSpace(firstNonEmpty(run.taskID, run.anchor.TaskID)),
		},
		ACP: session.ACPRef{
			SessionID: strings.TrimSpace(firstNonEmpty(env.SessionID, run.anchor.SessionID)),
		},
	}
	actor := session.ActorRef{
		Kind: session.ActorKindParticipant,
		ID:   strings.TrimSpace(firstNonEmpty(run.anchor.AgentID, run.taskID, run.anchor.TaskID)),
		Name: strings.TrimSpace(firstNonEmpty(run.agentName, run.anchor.AgentID)),
	}
	opts := acpingress.Options{
		At:         at,
		Scope:      *scope,
		Actor:      actor,
		Invocation: session.CloneEventInvocation(run.invocation),
		Visibility: acpingress.UIOnlyVisibility,
		Meta: map[string]any{
			"caelis": map[string]any{
				"version": 1,
				"runtime": map[string]any{
					"subagent": map[string]any{
						"task_id":    strings.TrimSpace(firstNonEmpty(run.taskID, run.anchor.TaskID)),
						"agent":      strings.TrimSpace(run.agentName),
						"agent_id":   strings.TrimSpace(run.anchor.AgentID),
						"session_id": strings.TrimSpace(firstNonEmpty(env.SessionID, run.anchor.SessionID)),
					},
				},
			},
		},
	}
	if len(textOverride) > 0 {
		opts.TextOverride = textOverride[0]
	}
	return acpingress.NormalizeUpdate(env.Update, opts)
}

func (run *childRun) emit(frame stream.Frame) {
	if run == nil {
		return
	}
	if slot := run.childSlot(); slot != nil {
		slot.publishRunFrame(run, frame)
		return
	}
	if run.sink != nil {
		run.sink.PublishStream(frame)
	}
}

func (run *childRun) appendAgentMessageLocked(text string) string {
	delta, _ := run.appendAgentMessageChunkLocked("", text)
	return delta
}

func (run *childRun) appendAgentMessageChunkLocked(messageID string, text string) (string, string) {
	if run == nil {
		return "", ""
	}
	update := run.finalAssistant.ObserveUpdate(eventstream.ContentChunk{
		SessionUpdate: eventstream.UpdateAgentMessage,
		MessageID:     strings.TrimSpace(messageID),
		Content:       text,
	})
	run.agentText = update.Text
	run.result = update.Text
	return update.Delta, update.MessageID
}

func (run *childRun) clearFinalAssistantLocked() {
	if run == nil {
		return
	}
	run.agentText = ""
	run.result = ""
	run.finalAssistant.Reset()
}

func chunkText(chunk client.ContentChunk) string {
	return acpingress.ContentChunkText(chunk)
}

func toolActivity(title string, kind string, status string) string {
	title = strings.TrimSpace(title)
	kind = strings.TrimSpace(strings.ToLower(kind))
	status = strings.TrimSpace(strings.ToLower(status))
	switch {
	case title != "":
		return title
	case kind != "" && status != "":
		return kind + " " + status
	case kind != "":
		return kind
	default:
		return "working"
	}
}

func derefString(in *string) string {
	if in == nil {
		return ""
	}
	return strings.TrimSpace(*in)
}
