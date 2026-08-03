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

	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/authentication"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpcleanup"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpingress"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acputil"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/sessionconfig"
	"github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	acpschema "github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/protocol/acp/semantic"
)

type PermissionHandler func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error)

// MessageHandler routes a child-originated message within the parent topology.
// Spawn and Anchor are trusted identities; wire-level From is not authoritative.
type MessageHandler func(context.Context, subagent.SpawnContext, delegation.Anchor, agentmessage.Request) (agentmessage.Response, error)

// PlacementResolver materializes a durable model placement at the external
// effect boundary. The product host owns model configuration and must reject a
// placement whose recorded configuration no longer matches current state.
type PlacementResolver func(context.Context, subagent.SpawnContext, delegation.TargetRequest) (AgentConfig, error)

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
	ClientInfo        *client.Implementation
	Clock             func() time.Time
	PermissionHandler PermissionHandler
	PermissionBridge  PermissionBridge
	PlacementResolver PlacementResolver
	MessageHandler    MessageHandler
}

type Runner struct {
	registry          *Registry
	clientInfo        *client.Implementation
	clock             func() time.Time
	permissionHandler PermissionHandler
	permissionBridge  PermissionBridge
	placementResolver PlacementResolver
	messageHandler    MessageHandler

	counter atomic.Uint64
	mu      sync.RWMutex
	runs    map[string]*childRun
}

type childRun struct {
	anchor                delegation.Anchor
	agentName             string
	client                *client.Client
	configuredAuth        controlagents.Authentication
	authenticationMethods []controlagents.AuthenticationMethod
	spawn                 subagent.SpawnContext
	supportsMessages      bool
	taskID                string
	sink                  stream.Sink
	completion            delegation.CompletionSink
	ctx                   context.Context
	cancel                context.CancelFunc

	mu              sync.RWMutex
	state           delegation.State
	outputPreview   string
	failureDetail   string
	result          string
	agentText       string
	finalAssistant  acpschema.FinalAssistantAccumulator
	updatedAt       time.Time
	running         bool
	finishing       bool
	cancelRequested bool
	cancelFailed    bool
	cancelResolved  chan struct{}
	done            chan struct{}
}

func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.Registry == nil {
		return nil, fmt.Errorf("internal/acpagentbridge/subagent: registry is required")
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
		messageHandler:    cfg.MessageHandler,
		runs:              map[string]*childRun{},
	}, nil
}

// BindMessageHandler connects the runner to its owning Runtime after product
// composition has constructed both sides.
func (r *Runner) BindMessageHandler(handler MessageHandler) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.messageHandler = handler
	r.mu.Unlock()
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
	run := &childRun{
		state:          delegation.StateRunning,
		running:        true,
		taskID:         strings.TrimSpace(spawn.TaskID),
		sink:           spawn.Streams,
		completion:     spawn.Completion,
		updatedAt:      r.clock(),
		done:           make(chan struct{}),
		agentName:      strings.TrimSpace(cfg.Name),
		configuredAuth: controlagents.NormalizeAuthentication(cfg.Authentication),
		spawn:          spawn,
	}
	detachedCtx := detachedChildContext(ctx)
	childCtx, childCancel := context.WithCancel(detachedCtx)
	run.ctx = childCtx
	run.cancel = childCancel
	agentID := r.stableAgentID(cfg.Name, spawn.TaskID)
	launchEnv := maps.Clone(cfg.Env)
	if strings.EqualFold(strings.TrimSpace(cfg.Name), "self") {
		if launchEnv == nil {
			launchEnv = map[string]string{}
		}
		launchEnv["SDK_ACP_ENABLE_SPAWN"] = "0"
		launchEnv["SDK_ACP_CHILD_NO_SPAWN"] = "1"
	}
	acpClient, err := client.Start(childCtx, client.Config{
		Command:    cfg.Command,
		Args:       append([]string(nil), cfg.Args...),
		Env:        launchEnv,
		WorkDir:    pickWorkDir(cfg.WorkDir, spawn.CWD),
		ClientInfo: r.clientInfo,
		OnUpdate:   func(env client.UpdateEnvelope) { r.handleUpdate(run, env) },
		OnPermissionRequest: func(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			return r.permissionCallback(spawn, cfg, agentID)(ctx, req)
		},
		OnSessionMessage: func(ctx context.Context, req client.SessionMessageRequest) (client.SessionMessageResponse, error) {
			return r.handleChildMessage(ctx, run, req)
		},
	})
	if err != nil {
		childCancel()
		return delegation.Anchor{}, delegation.Result{}, err
	}
	initialize, err := acpClient.Initialize(ctx)
	if err != nil {
		childCancel()
		_ = acpClient.Close(ctx)
		return delegation.Anchor{}, delegation.Result{}, err
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
		childCancel()
		_ = acpClient.Close(ctx)
		return delegation.Anchor{}, delegation.Result{}, err
	}
	sessionResp := recovered.Value
	if _, err := sessionconfig.Apply(ctx, acpClient, strings.TrimSpace(sessionResp.SessionID), sessionconfig.State{
		ConfigOptions: sessionResp.ConfigOptions,
		Models:        sessionResp.Models,
	}, cfg.SessionOptions); err != nil {
		childCancel()
		if hasACPSessionCapability(initialize, "close") {
			_ = acpcleanup.CloseSession(ctx, acpClient, strings.TrimSpace(sessionResp.SessionID))
		}
		_ = acpcleanup.CloseClient(ctx, acpClient)
		return delegation.Anchor{}, delegation.Result{}, err
	}
	// Do not call session/set_mode for spawned ACP children here. External ACP
	// agents own their session-mode vocabulary, while Caelis approval modes
	// (manual/auto-review) are parent-routing policy. Permission requests from
	// the child still bridge through OnPermissionRequest above; Caelis self ACP
	// children that need launch-time approval policy get it from assembly args.
	anchor := delegation.Anchor{
		TaskID:    strings.TrimSpace(spawn.TaskID),
		SessionID: strings.TrimSpace(sessionResp.SessionID),
		AgentID:   agentID,
	}
	run.anchor = anchor
	run.client = acpClient
	run.authenticationMethods = controlagents.CloneAuthenticationMethods(authenticationMethods)
	run.supportsMessages = hasACPMessageCapability(initialize)
	runKey, err := childRunKey(anchor)
	if err != nil {
		childCancel()
		_ = acpClient.Close(ctx)
		return delegation.Anchor{}, delegation.Result{}, err
	}
	r.mu.Lock()
	if existing := r.runs[runKey]; existing != nil {
		r.mu.Unlock()
		childCancel()
		_ = acpClient.Close(ctx)
		return delegation.Anchor{}, delegation.Result{}, fmt.Errorf("internal/acpagentbridge/subagent: child run %q already registered", runKey)
	}
	r.runs[runKey] = run
	r.mu.Unlock()
	go r.drivePrompt(childCtx, run, strings.TrimSpace(req.Prompt))
	return anchor, r.waitRun(ctx, run, 0), nil
}

func (r *Runner) resolveSpawnConfig(ctx context.Context, spawn subagent.SpawnContext, req delegation.TargetRequest) (AgentConfig, error) {
	placement := delegation.NormalizePlacement(req.Target.Placement)
	switch placement.Kind {
	case delegation.PlacementModel:
		if r.placementResolver == nil {
			return AgentConfig{}, fmt.Errorf("internal/acpagentbridge/subagent: model placement resolver is unavailable")
		}
		cfg, err := r.placementResolver(ctx, spawn, req)
		if err != nil {
			return AgentConfig{}, err
		}
		cfg = normalizeAgentConfig(cfg)
		if cfg.Name == "" || cfg.Command == "" {
			return AgentConfig{}, fmt.Errorf("internal/acpagentbridge/subagent: model placement resolved an invalid Agent configuration")
		}
		return cfg, nil
	case delegation.PlacementAgent:
		if placement.ConfigFingerprint != "" {
			if r.placementResolver == nil {
				return AgentConfig{}, fmt.Errorf("internal/acpagentbridge/subagent: configured placement resolver is unavailable")
			}
			cfg, err := r.placementResolver(ctx, spawn, req)
			if err != nil {
				return AgentConfig{}, err
			}
			cfg = normalizeAgentConfig(cfg)
			if cfg.Name == "" || cfg.Command == "" {
				return AgentConfig{}, fmt.Errorf("internal/acpagentbridge/subagent: configured placement resolved an invalid Agent configuration")
			}
			return cfg, nil
		}
		return r.registry.Resolve(strings.TrimSpace(placement.Agent))
	case "":
		return AgentConfig{}, fmt.Errorf("internal/acpagentbridge/subagent: placement is required")
	default:
		return AgentConfig{}, fmt.Errorf("internal/acpagentbridge/subagent: unsupported placement kind %q", placement.Kind)
	}
}

func hasACPSessionCapability(resp client.InitializeResponse, name string) bool {
	if resp.AgentCapabilities.SessionCapabilities == nil {
		return false
	}
	_, ok := resp.AgentCapabilities.SessionCapabilities[strings.TrimSpace(name)]
	return ok
}

func hasACPMessageCapability(resp client.InitializeResponse) bool {
	if resp.AgentCapabilities.Meta == nil {
		return false
	}
	_, ok := resp.AgentCapabilities.Meta[client.MethodSessionMessage]
	return ok
}

func (r *Runner) Wait(ctx context.Context, anchor delegation.Anchor, yieldTimeMS int) (delegation.Result, error) {
	run, err := r.lookup(anchor)
	if err != nil {
		return delegation.Result{}, err
	}
	return r.waitRun(ctx, run, yieldTimeMS), nil
}

// Message delivers to a running child or queues a new message-authored turn on
// a completed child. For a completed child, returning transfers asynchronous
// delivery ownership to the runner; it does not wait for target consumption or
// Turn completion.
func (r *Runner) Message(ctx context.Context, anchor delegation.Anchor, req subagent.MessageRequest) (delegation.Result, error) {
	run, err := r.lookup(anchor)
	if err != nil {
		return delegation.Result{}, err
	}
	messageReq := agentmessage.NormalizeRequest(req.Request)
	if messageReq.MessageID == "" || messageReq.Text == "" {
		return delegation.Result{}, fmt.Errorf("internal/acpagentbridge/subagent: message id and text are required")
	}
	run.mu.Lock()
	if !run.supportsMessages {
		run.mu.Unlock()
		return delegation.Result{}, fmt.Errorf("internal/acpagentbridge/subagent: child session %q does not support %s", run.anchor.SessionID, client.MethodSessionMessage)
	}
	if run.running || run.finishing {
		run.mu.Unlock()
		resp, err := r.callSessionMessage(ctx, run, messageReq)
		if err != nil {
			var unknown *messageTurnOutcomeUnknownError
			if errors.As(err, &unknown) {
				detail := unknown.Error()
				return delegation.Result{
					TaskID: run.taskID, State: delegation.StateUnknownOutcome,
					OutputPreview: detail, Error: detail, UpdatedAt: r.clock(),
				}, nil
			}
			return delegation.Result{}, err
		}
		if !resp.Accepted {
			return delegation.Result{}, fmt.Errorf("internal/acpagentbridge/subagent: child rejected message %q", messageReq.MessageID)
		}
		return r.waitRun(ctx, run, 0), nil
	}
	if run.state != delegation.StateCompleted {
		state := run.state
		run.mu.Unlock()
		return delegation.Result{}, fmt.Errorf("internal/acpagentbridge/subagent: child session %q is %s", run.anchor.SessionID, state)
	}
	run.state = delegation.StateRunning
	run.running = true
	run.outputPreview = ""
	run.failureDetail = ""
	run.result = ""
	run.agentText = ""
	run.finalAssistant.Reset()
	run.updatedAt = r.clock()
	run.finishing = false
	run.cancelRequested = false
	run.cancelFailed = false
	run.cancelResolved = nil
	run.done = make(chan struct{})
	run.completion = req.Completion
	runCtx := run.ctx
	if runCtx == nil {
		runCtx = detachedChildContext(ctx)
	}
	run.mu.Unlock()
	go r.driveMessage(runCtx, run, messageReq)
	return r.waitRun(ctx, run, 0), nil
}

func detachedChildContext(ctx context.Context) context.Context {
	return session.ContextWithoutRuntimeLease(context.WithoutCancel(ctx))
}

func subagentSessionMeta(spawn subagent.SpawnContext) map[string]any {
	return metautil.WithCompactRuntimeSection(nil, metautil.RuntimeSession, map[string]any{
		metautil.RuntimeSessionKind:     metautil.RuntimeSessionKindSubagent,
		metautil.RuntimeSessionParentID: spawn.SessionRef.SessionID,
		metautil.RuntimeTaskID:          spawn.TaskID,
	})
}

func (r *Runner) Cancel(ctx context.Context, anchor delegation.Anchor) error {
	run, err := r.lookup(anchor)
	if err != nil {
		return err
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

func (r *Runner) drivePrompt(ctx context.Context, run *childRun, prompt string) {
	resp, err := authentication.RecoverConfiguredCall(
		ctx,
		run.client,
		controlagents.CloneAuthenticationMethods(run.authenticationMethods),
		run.agentName,
		run.configuredAuth,
		func(callCtx context.Context, activeClient *client.Client) (client.PromptResponse, error) {
			return activeClient.Prompt(callCtx, run.anchor.SessionID, prompt, nil)
		},
	)
	r.finishDrive(ctx, run, resp.StopReason, err)
}

func (r *Runner) driveMessage(ctx context.Context, run *childRun, req agentmessage.Request) {
	resp, err := r.callSessionMessage(ctx, run, req)
	if err == nil && !resp.Accepted {
		err = fmt.Errorf("child rejected message %q", req.MessageID)
	}
	if err == nil && !strings.EqualFold(strings.TrimSpace(resp.State), string(delegation.StateCompleted)) {
		err = &messageTurnOutcomeUnknownError{state: strings.TrimSpace(resp.State)}
	}
	r.finishDrive(ctx, run, "end_turn", err)
}

type messageTurnOutcomeUnknownError struct {
	state string
	cause error
}

func (e *messageTurnOutcomeUnknownError) Error() string {
	if e == nil {
		return "child message delivery outcome is unknown"
	}
	if e.cause != nil {
		return fmt.Sprintf("child message delivery outcome is unknown: %v", e.cause)
	}
	state := strings.TrimSpace(e.state)
	if state == "" {
		state = "unspecified"
	}
	return fmt.Sprintf("child accepted message but returned non-terminal state %q", state)
}

func (e *messageTurnOutcomeUnknownError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (r *Runner) callSessionMessage(ctx context.Context, run *childRun, req agentmessage.Request) (client.SessionMessageResponse, error) {
	if run == nil {
		return client.SessionMessageResponse{}, fmt.Errorf("internal/acpagentbridge/subagent: child run is unavailable")
	}
	run.mu.RLock()
	acpClient := run.client
	sessionID := strings.TrimSpace(run.anchor.SessionID)
	agentID := firstNonEmpty(run.agentName, run.anchor.Agent)
	configured := controlagents.NormalizeAuthentication(run.configuredAuth)
	methods := controlagents.CloneAuthenticationMethods(run.authenticationMethods)
	run.mu.RUnlock()
	if acpClient == nil || sessionID == "" {
		return client.SessionMessageResponse{}, fmt.Errorf("internal/acpagentbridge/subagent: child message transport is unavailable")
	}
	response, err := authentication.RecoverConfiguredCall(
		ctx, acpClient, methods, agentID, configured,
		func(callCtx context.Context, activeClient *client.Client) (client.SessionMessageResponse, error) {
			return activeClient.SessionMessage(callCtx, client.SessionMessageRequest{
				SessionID: sessionID, MessageID: req.MessageID, To: req.To,
				From: firstNonEmpty(req.From.Name, req.From.ID), Message: req.Text,
			})
		},
	)
	if err != nil {
		// Once the RPC is dispatched, a transport or callback error cannot prove
		// that the target failed to persist the stable MessageID. Preserve that
		// uncertainty so callers never turn response loss into a fresh delivery.
		return client.SessionMessageResponse{}, &messageTurnOutcomeUnknownError{cause: err}
	}
	return response, nil
}

func (r *Runner) finishDrive(ctx context.Context, run *childRun, stopReason string, err error) {
	run.mu.Lock()
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
		var unknown *messageTurnOutcomeUnknownError
		if errors.As(err, &unknown) {
			run.state = delegation.StateUnknownOutcome
			run.failureDetail = unknown.Error()
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
		run.outputPreview = compactPreview(run.outputPreview)
		run.failureDetail = ""
	}
	result := childResultLocked(run)
	completion := run.completion
	done := run.done
	run.mu.Unlock()

	if closeClient && run.client != nil {
		_ = run.client.Close(context.WithoutCancel(ctx))
	}
	run.mu.Lock()
	run.finishing = false
	run.mu.Unlock()
	close(done)
	if completion != nil {
		completion.PublishSubagentCompletion(result)
	}
}

func (r *Runner) handleChildMessage(ctx context.Context, run *childRun, req client.SessionMessageRequest) (client.SessionMessageResponse, error) {
	if run == nil {
		return client.SessionMessageResponse{}, fmt.Errorf("internal/acpagentbridge/subagent: child run is unavailable")
	}
	r.mu.RLock()
	handler := r.messageHandler
	r.mu.RUnlock()
	if handler == nil {
		return client.SessionMessageResponse{}, fmt.Errorf("internal/acpagentbridge/subagent: message router is unavailable")
	}
	messageReq := agentmessage.Request{
		MessageID: strings.TrimSpace(req.MessageID), To: strings.TrimSpace(req.To), Text: strings.TrimSpace(req.Message),
	}
	response, err := handler(ctx, run.spawn, delegation.CloneAnchor(run.anchor), messageReq)
	if err != nil {
		return client.SessionMessageResponse{}, err
	}
	return client.SessionMessageResponse{
		MessageID: response.MessageID, Accepted: response.Accepted, State: response.State,
		TurnID: response.TurnID, StartedTurn: response.StartedTurn,
	}, nil
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
		return nil, fmt.Errorf("internal/acpagentbridge/subagent: child run %q not found", key)
	}
	// Defensive isolation: reject anchors whose remote session drifted from the
	// registered child (two endpoints must not share a process-local binding).
	if sessionID := strings.TrimSpace(anchor.SessionID); sessionID != "" &&
		strings.TrimSpace(run.anchor.SessionID) != "" &&
		sessionID != strings.TrimSpace(run.anchor.SessionID) {
		return nil, fmt.Errorf("internal/acpagentbridge/subagent: child run %q session mismatch", key)
	}
	return run, nil
}

// childRunKey isolates process-local child runs by durable TaskID so two remote
// endpoints that both return a common session id (for example "session-1") cannot
// overwrite each other.
func childRunKey(anchor delegation.Anchor) (string, error) {
	taskID := strings.TrimSpace(anchor.TaskID)
	if taskID == "" {
		return "", fmt.Errorf("internal/acpagentbridge/subagent: task_id is required")
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
	_, approval, _, err := semantic.DecodePermissionRequest(req)
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

func compactPreview(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	lines := strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' })
	if len(lines) == 0 {
		return ""
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "" {
		last = strings.TrimSpace(text)
	}
	if len(last) <= 160 {
		return last
	}
	return strings.TrimSpace(last[:80]) + " ...[truncated]... " + strings.TrimSpace(last[len(last)-48:])
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
	env.Update = acputil.StripTerminalConsoleFenceUpdate(env.Update)
	var event *session.Event
	var frame *stream.Frame
	run.mu.Lock()
	run.updatedAt = r.clock()
	switch update := env.Update.(type) {
	case client.ContentChunk:
		if text := chunkText(update); text != "" {
			switch strings.TrimSpace(update.SessionUpdate) {
			case client.UpdateUserMessage:
				event = run.acpUpdateEvent(env, run.updatedAt)
				markSubagentInputEvent(event)
			case client.UpdateAgentMessage:
				textOverride := run.appendAgentMessageChunkLocked(update.MessageID, text)
				run.outputPreview = compactPreview(run.agentText)
				if textOverride != "" {
					event = run.acpUpdateEvent(env, run.updatedAt, textOverride)
				}
			case client.UpdateAgentThought:
				run.clearFinalAssistantLocked()
				event = run.acpUpdateEvent(env, run.updatedAt)
			default:
				break
			}
		}
	case client.ToolCall:
		run.clearFinalAssistantLocked()
		run.outputPreview = compactPreview(toolActivity(update.Title, update.Kind, update.Status))
		event = run.acpUpdateEvent(env, run.updatedAt)
	case client.ToolCallUpdate:
		run.clearFinalAssistantLocked()
		run.outputPreview = compactPreview(toolActivity(derefString(update.Title), derefString(update.Kind), derefString(update.Status)))
		event = run.acpUpdateEvent(env, run.updatedAt)
	case client.PlanUpdate:
		run.clearFinalAssistantLocked()
		run.outputPreview = "updating plan"
		event = run.acpUpdateEvent(env, run.updatedAt)
	}
	if event != nil {
		next := stream.Frame{
			Ref: stream.Ref{
				TaskID:    firstNonEmpty(run.taskID, run.anchor.TaskID),
				SessionID: firstNonEmpty(strings.TrimSpace(env.SessionID), run.anchor.SessionID),
			},
			State:     string(run.state),
			Running:   run.running,
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

func markSubagentInputEvent(event *session.Event) {
	if event == nil {
		return
	}
	// ACP calls this a user_message because it is the input side of the child
	// transcript. Within the parent Task stream it is an Agent-to-Agent context
	// message, not a canonical end-user submission.
	event.Type = session.EventTypeContext
	event.Actor = session.ActorRef{Kind: session.ActorKindController, ID: "parent", Name: "parent"}
	if event.Scope != nil {
		event.Scope.Source = "agent_message_input"
	}
	if event.Meta == nil {
		event.Meta = map[string]any{}
	}
	event.Meta["agent_message"] = true
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
	if run == nil || run.sink == nil {
		return
	}
	run.sink.PublishStream(frame)
}

func (run *childRun) appendAgentMessageLocked(text string) string {
	return run.appendAgentMessageChunkLocked("", text)
}

func (run *childRun) appendAgentMessageChunkLocked(messageID string, text string) string {
	if run == nil {
		return ""
	}
	update := run.finalAssistant.ObserveUpdate(acpschema.ContentChunk{
		SessionUpdate: acpschema.UpdateAgentMessage,
		MessageID:     strings.TrimSpace(messageID),
		Content:       text,
	})
	run.agentText = update.Text
	run.result = update.Text
	return update.Delta
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
		return strings.ToLower(title)
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
