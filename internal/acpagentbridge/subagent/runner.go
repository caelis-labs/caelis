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

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpcleanup"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpingress"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acputil"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/sessionconfig"
	"github.com/caelis-labs/caelis/protocol/acp/client"
	acpschema "github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/protocol/acp/semantic"
)

type PermissionHandler func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error)

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
}

type Runner struct {
	registry          *Registry
	clientInfo        *client.Implementation
	clock             func() time.Time
	permissionHandler PermissionHandler
	permissionBridge  PermissionBridge
	placementResolver PlacementResolver

	counter atomic.Uint64
	mu      sync.RWMutex
	runs    map[string]*childRun
}

type childRun struct {
	anchor delegation.Anchor
	client *client.Client
	taskID string
	sink   stream.Sink
	ctx    context.Context
	cancel context.CancelFunc

	mu             sync.RWMutex
	state          delegation.State
	outputPreview  string
	result         string
	agentText      string
	finalAssistant acpschema.FinalAssistantAccumulator
	updatedAt      time.Time
	running        bool
	done           chan struct{}
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
		runs:              map[string]*childRun{},
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
	run := &childRun{
		state:     delegation.StateRunning,
		running:   true,
		taskID:    strings.TrimSpace(spawn.TaskID),
		sink:      spawn.Streams,
		updatedAt: r.clock(),
		done:      make(chan struct{}),
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
	client, err := client.Start(childCtx, client.Config{
		Command:    cfg.Command,
		Args:       append([]string(nil), cfg.Args...),
		Env:        launchEnv,
		WorkDir:    pickWorkDir(cfg.WorkDir, spawn.CWD),
		ClientInfo: r.clientInfo,
		OnUpdate:   func(env client.UpdateEnvelope) { r.handleUpdate(run, env) },
		OnPermissionRequest: func(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			return r.permissionCallback(spawn, cfg, agentID)(ctx, req)
		},
	})
	if err != nil {
		childCancel()
		return delegation.Anchor{}, delegation.Result{}, err
	}
	initialize, err := client.Initialize(ctx)
	if err != nil {
		childCancel()
		_ = client.Close(ctx)
		return delegation.Anchor{}, delegation.Result{}, err
	}
	sessionResp, err := client.NewSession(ctx, strings.TrimSpace(spawn.CWD), nil)
	if err != nil {
		childCancel()
		_ = client.Close(ctx)
		return delegation.Anchor{}, delegation.Result{}, err
	}
	if _, err := sessionconfig.Apply(ctx, client, strings.TrimSpace(sessionResp.SessionID), sessionconfig.State{
		ConfigOptions: sessionResp.ConfigOptions,
		Models:        sessionResp.Models,
	}, cfg.SessionOptions); err != nil {
		childCancel()
		if hasACPSessionCapability(initialize, "close") {
			_ = acpcleanup.CloseSession(ctx, client, strings.TrimSpace(sessionResp.SessionID))
		}
		_ = acpcleanup.CloseClient(ctx, client)
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
		Agent:     cfg.Name,
		AgentID:   agentID,
	}
	run.anchor = anchor
	run.client = client
	runKey, err := childRunKey(anchor)
	if err != nil {
		childCancel()
		_ = client.Close(ctx)
		return delegation.Anchor{}, delegation.Result{}, err
	}
	r.mu.Lock()
	if existing := r.runs[runKey]; existing != nil {
		r.mu.Unlock()
		childCancel()
		_ = client.Close(ctx)
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
		return r.registry.Resolve(req.Target.ExecutionAgent())
	case "":
		return r.registry.Resolve(req.Target.ExecutionAgent())
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

func (r *Runner) Wait(ctx context.Context, anchor delegation.Anchor, yieldTimeMS int) (delegation.Result, error) {
	run, err := r.lookup(anchor)
	if err != nil {
		return delegation.Result{}, err
	}
	return r.waitRun(ctx, run, yieldTimeMS), nil
}

func (r *Runner) Continue(ctx context.Context, anchor delegation.Anchor, req delegation.ContinueRequest) (delegation.Result, error) {
	run, err := r.lookup(anchor)
	if err != nil {
		return delegation.Result{}, err
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return delegation.Result{}, fmt.Errorf("internal/acpagentbridge/subagent: continuation prompt is required")
	}
	run.mu.Lock()
	if run.running {
		run.mu.Unlock()
		return delegation.Result{}, fmt.Errorf("internal/acpagentbridge/subagent: child session %q is still running; use TASK wait before write", run.anchor.SessionID)
	}
	run.state = delegation.StateRunning
	run.running = true
	run.outputPreview = ""
	run.result = ""
	run.agentText = ""
	run.finalAssistant.Reset()
	run.updatedAt = r.clock()
	run.done = make(chan struct{})
	runCtx := run.ctx
	if runCtx == nil {
		runCtx = detachedChildContext(ctx)
	}
	run.mu.Unlock()
	go r.drivePrompt(runCtx, run, prompt)
	return r.waitRun(ctx, run, req.YieldTimeMS), nil
}

func detachedChildContext(ctx context.Context) context.Context {
	return session.ContextWithoutRuntimeLease(context.WithoutCancel(ctx))
}

func (r *Runner) Cancel(ctx context.Context, anchor delegation.Anchor) error {
	run, err := r.lookup(anchor)
	if err != nil {
		return err
	}
	run.mu.RLock()
	client := run.client
	sessionID := run.anchor.SessionID
	run.mu.RUnlock()
	var remoteErr error
	if client != nil {
		remoteErr = client.Cancel(ctx, sessionID)
	}
	if run.cancel != nil {
		run.cancel()
	}
	run.mu.Lock()
	run.running = false
	if remoteErr != nil {
		run.state = delegation.StateInterrupted
		run.outputPreview = "cancellation outcome unknown"
	} else {
		run.state = delegation.StateCancelled
		run.outputPreview = "cancelled"
	}
	run.updatedAt = r.clock()
	run.mu.Unlock()
	return remoteErr
}

func (r *Runner) drivePrompt(ctx context.Context, run *childRun, prompt string) {
	resp, err := run.client.Prompt(ctx, run.anchor.SessionID, prompt, nil)
	run.mu.Lock()
	defer run.mu.Unlock()
	defer close(run.done)
	run.running = false
	run.updatedAt = r.clock()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			if run.state != delegation.StateCancelled {
				run.state = delegation.StateInterrupted
				run.outputPreview = "interrupted"
			}
			run.result = ""
			_ = run.client.Close(context.WithoutCancel(ctx))
			return
		}
		errText := err.Error()
		if stderr := run.client.StderrTail(4096); strings.TrimSpace(stderr) != "" {
			errText += "\nstderr:\n" + stderr
		}
		run.state = delegation.StateFailed
		run.outputPreview = compactPreview(errText)
		run.result = ""
		_ = run.client.Close(context.WithoutCancel(ctx))
		return
	}
	if strings.EqualFold(strings.TrimSpace(resp.StopReason), "cancelled") {
		run.state = delegation.StateCancelled
		run.outputPreview = "cancelled"
		run.result = ""
		_ = run.client.Close(context.WithoutCancel(ctx))
		return
	}
	run.state = delegation.StateCompleted
	run.outputPreview = compactPreview(run.outputPreview)
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
		select {
		case <-ctx.Done():
		case <-run.done:
		case <-time.After(wait):
		}
	}
	run.mu.RLock()
	defer run.mu.RUnlock()
	out := delegation.Result{
		State:         run.state,
		Running:       run.running,
		Yielded:       run.running,
		OutputPreview: strings.TrimSpace(run.outputPreview),
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
			ID:   strings.TrimSpace(firstNonEmpty(run.anchor.Agent, run.anchor.AgentID)),
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
		Name: strings.TrimSpace(firstNonEmpty(run.anchor.Agent, run.anchor.AgentID)),
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
						"agent":      strings.TrimSpace(run.anchor.Agent),
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
