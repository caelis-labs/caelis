package subagent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/authentication"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpcleanup"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/sessionconfig"
)

func (r *Runner) reconnectChildEndpointLocked(
	ctx context.Context,
	anchor delegation.Anchor,
	recovery *tasksubagent.ReconnectRequest,
	slot *childSlot,
) (*childRun, error) {
	if recovery == nil {
		return nil, fmt.Errorf("target Agent reconnect context is required")
	}
	recovery = tasksubagent.CloneReconnectRequest(recovery)
	spawn := recovery.Spawn
	anchor = delegation.CloneAnchor(anchor)
	if strings.TrimSpace(anchor.TaskID) == "" || strings.TrimSpace(anchor.SessionID) == "" ||
		strings.TrimSpace(anchor.AgentID) == "" {
		return nil, fmt.Errorf("target Agent reconnect anchor is incomplete")
	}
	if strings.TrimSpace(spawn.TaskID) != strings.TrimSpace(anchor.TaskID) ||
		strings.TrimSpace(spawn.SessionRef.SessionID) == "" {
		return nil, fmt.Errorf("target Agent reconnect identity does not match its Task")
	}
	if err := delegation.ValidateTarget(recovery.Target); err != nil {
		return nil, err
	}
	cfg, err := r.resolveSpawnConfig(ctx, spawn, delegation.TargetRequest{Target: recovery.Target})
	if err != nil {
		return nil, err
	}

	done := make(chan struct{})
	close(done)
	run := &childRun{
		anchor: anchor, agentName: strings.TrimSpace(cfg.Name),
		configuredAuth: controlagents.NormalizeAuthentication(cfg.Authentication),
		spawn:          spawn, taskID: strings.TrimSpace(anchor.TaskID), sink: spawn.Streams,
		completion: spawn.Completion, state: delegation.StateCompleted,
		updatedAt: r.clock(), done: done,
	}
	detachedCtx := detachedChildContext(ctx)
	childCtx, childCancel := context.WithCancel(detachedCtx)
	run.ctx = childCtx
	run.cancel = childCancel
	if slot == nil {
		slot = newChildSlot(childEndpointFromReconnect(anchor, recovery), nil)
	}
	activityCheckpoint := slot.activityCheckpoint()
	previousRun := slot.currentRun()
	setupCommitted := false
	run.installChildSlot(slot)
	slot.beginSetup(run)
	defer func() {
		if !setupCommitted {
			slot.restoreActivity(activityCheckpoint, previousRun)
		}
	}()

	launchEnv := maps.Clone(cfg.Env)
	if strings.EqualFold(strings.TrimSpace(cfg.Name), "self") {
		if launchEnv == nil {
			launchEnv = map[string]string{}
		}
		launchEnv["SDK_ACP_ENABLE_SPAWN"] = "0"
		launchEnv["SDK_ACP_CHILD_NO_SPAWN"] = "1"
	}
	acpClient, err := client.Start(childCtx, client.Config{
		HostedAdapterID: cfg.HostedAdapterID, ConnectionID: cfg.Name, EndpointResolver: r.endpointResolver,
		Command: cfg.Command, Args: append([]string(nil), cfg.Args...), Env: launchEnv,
		WorkDir: pickWorkDir(cfg.WorkDir, spawn.CWD), ClientInfo: r.clientInfo,
		OnUpdate: func(env client.UpdateEnvelope) { r.handleUpdate(run, env) },
		OnPermissionRequest: func(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			return r.permissionCallback(spawn, cfg, anchor.AgentID)(ctx, req)
		},
	})
	if err != nil {
		childCancel()
		return nil, err
	}
	run.mu.Lock()
	run.client = acpClient
	run.mu.Unlock()
	initialize, err := acpClient.Initialize(ctx)
	if err != nil {
		childCancel()
		_ = acpcleanup.CloseClient(ctx, acpClient)
		return nil, err
	}
	supportsSteering, err := client.SupportsSessionSteering(initialize)
	if err != nil {
		childCancel()
		closeErr := acpcleanup.CloseClient(ctx, acpClient)
		return nil, fmt.Errorf("negotiate Target Agent messaging capability: %w", errors.Join(err, closeErr))
	}
	if !hasACPSessionCapability(initialize, "resume") {
		childCancel()
		_ = acpcleanup.CloseClient(ctx, acpClient)
		return nil, fmt.Errorf("target Agent %q does not support session/resume", cfg.Name)
	}
	authenticationMethods := authentication.Methods(initialize)
	recovered, err := authentication.ResumeSession(ctx, authentication.RecoveryConfig{
		Mode: authentication.RecoveryConfigured, Client: acpClient, Initialize: initialize,
		Methods: authenticationMethods, AgentID: cfg.Name, Authentication: cfg.Authentication,
	}, anchor.SessionID, spawn.CWD, subagentSessionMeta(spawn))
	if err != nil {
		childCancel()
		_ = acpcleanup.CloseClient(ctx, acpClient)
		return nil, err
	}
	sessionOptions := cfg.SessionOptions
	if r.sessionPreparer != nil {
		sessionOptions, err = r.sessionPreparer(ctx, spawn, anchor.SessionID, cfg)
		if err != nil {
			childCancel()
			_ = acpcleanup.CloseClient(ctx, acpClient)
			return nil, err
		}
	}
	if _, err := sessionconfig.Apply(ctx, acpClient, anchor.SessionID, sessionconfig.State{
		ConfigOptions: recovered.Value.ConfigOptions,
		Models:        recovered.Value.Models,
	}, sessionOptions); err != nil {
		childCancel()
		_ = acpcleanup.CloseClient(ctx, acpClient)
		return nil, err
	}
	run.mu.Lock()
	run.authenticationMethods = controlagents.CloneAuthenticationMethods(authenticationMethods)
	run.supportsSteering = supportsSteering
	run.promptCapabilities = initialize.AgentCapabilities.PromptCapabilities
	run.mu.Unlock()

	runKey, err := childRunKey(anchor)
	if err != nil {
		childCancel()
		_ = acpcleanup.CloseClient(ctx, acpClient)
		return nil, err
	}
	if err := slot.finalizeTarget(childEndpointFromReconnect(anchor, recovery)); err != nil {
		childCancel()
		_ = acpcleanup.CloseClient(ctx, acpClient)
		return nil, err
	}
	slot.mu.Lock()
	hasObserver := slot.observer != nil
	slot.mu.Unlock()
	if !hasObserver && recovery.Spawn.ActivityObserver != nil {
		slot.bindObserver(recovery.Spawn.ActivityAfterCursor, recovery.Spawn.ActivityObserver)
	} else if !hasObserver {
		slot.bindObserver(0, compatibilityActivityObserver{run: run})
	}
	r.mu.Lock()
	if r.runs == nil {
		r.runs = map[string]*childRun{}
	}
	if r.slots == nil {
		r.slots = map[string]*childSlot{}
	}
	r.runs[runKey] = run
	r.slots[runKey] = slot
	r.mu.Unlock()
	setupCommitted = true
	return run, nil
}

func childEndpointFromReconnect(anchor delegation.Anchor, recovery *tasksubagent.ReconnectRequest) agent.ChildEndpointRef {
	target := agent.ChildEndpointRef{
		ParticipantID: strings.TrimSpace(anchor.AgentID),
		SessionID:     strings.TrimSpace(anchor.SessionID),
		EndpointKey:   strings.TrimSpace(anchor.TaskID),
	}
	if recovery != nil {
		target.Role = recovery.Spawn.Role
		if target.Role == "" {
			target.Role = session.ParticipantRoleDelegated
		}
		target.Placement = recovery.Target.Placement
	}
	return agent.NormalizeChildEndpointRef(target)
}
