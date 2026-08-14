package subagent

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/authentication"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpcleanup"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/sessionconfig"
	"github.com/caelis-labs/caelis/protocol/acp/client"
)

// reconnectIdleChild recreates only the process-local transport for an
// existing durable child whose prior Turn no longer has a usable transport. It
// never creates a replacement ACP Session or Task.
func (r *Runner) reconnectIdleChild(
	ctx context.Context,
	anchor delegation.Anchor,
	recovery *tasksubagent.ReconnectRequest,
) (*childRun, error) {
	if recovery == nil {
		return nil, fmt.Errorf("internal/acpagentbridge/subagent: child reconnect context is required")
	}
	recovery = tasksubagent.CloneReconnectRequest(recovery)
	spawn := recovery.Spawn
	anchor = delegation.CloneAnchor(anchor)
	if strings.TrimSpace(anchor.TaskID) == "" || strings.TrimSpace(anchor.SessionID) == "" ||
		strings.TrimSpace(anchor.AgentID) == "" {
		return nil, fmt.Errorf("internal/acpagentbridge/subagent: child reconnect anchor is incomplete")
	}
	if strings.TrimSpace(spawn.TaskID) != strings.TrimSpace(anchor.TaskID) ||
		strings.TrimSpace(spawn.SessionRef.SessionID) == "" {
		return nil, fmt.Errorf("internal/acpagentbridge/subagent: child reconnect identity does not match its durable Task")
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

	launchEnv := maps.Clone(cfg.Env)
	if strings.EqualFold(strings.TrimSpace(cfg.Name), "self") {
		if launchEnv == nil {
			launchEnv = map[string]string{}
		}
		launchEnv["SDK_ACP_ENABLE_SPAWN"] = "0"
		launchEnv["SDK_ACP_CHILD_NO_SPAWN"] = "1"
	}
	acpClient, err := client.Start(childCtx, client.Config{
		Command: cfg.Command, Args: append([]string(nil), cfg.Args...), Env: launchEnv,
		WorkDir: pickWorkDir(cfg.WorkDir, spawn.CWD), ClientInfo: r.clientInfo,
		OnUpdate: func(env client.UpdateEnvelope) { r.handleUpdate(run, env) },
		OnPermissionRequest: func(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			return r.permissionCallback(spawn, cfg, anchor.AgentID)(ctx, req)
		},
		OnSessionMessage: func(ctx context.Context, req client.SessionMessageRequest) (client.SessionMessageResponse, error) {
			return r.handleChildMessage(ctx, run, req)
		},
	})
	if err != nil {
		childCancel()
		return nil, err
	}
	initialize, err := acpClient.Initialize(ctx)
	if err != nil {
		childCancel()
		_ = acpcleanup.CloseClient(ctx, acpClient)
		return nil, err
	}
	if !hasACPSessionCapability(initialize, "resume") {
		childCancel()
		_ = acpcleanup.CloseClient(ctx, acpClient)
		return nil, fmt.Errorf("internal/acpagentbridge/subagent: child Agent %q does not support session/resume", cfg.Name)
	}
	if !hasACPMessageCapability(initialize) {
		childCancel()
		_ = acpcleanup.CloseClient(ctx, acpClient)
		return nil, fmt.Errorf("internal/acpagentbridge/subagent: child session %q does not support %s", anchor.SessionID, client.MethodSessionMessage)
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
	run.client = acpClient
	run.authenticationMethods = controlagents.CloneAuthenticationMethods(authenticationMethods)
	run.supportsMessages = true

	runKey, err := childRunKey(anchor)
	if err != nil {
		childCancel()
		_ = acpcleanup.CloseClient(ctx, acpClient)
		return nil, err
	}
	r.mu.Lock()
	if existing := r.runs[runKey]; existing != nil {
		r.mu.Unlock()
		childCancel()
		_ = acpcleanup.CloseClient(context.WithoutCancel(ctx), acpClient)
		if strings.TrimSpace(existing.anchor.SessionID) != strings.TrimSpace(anchor.SessionID) {
			return nil, fmt.Errorf("internal/acpagentbridge/subagent: child run %q session mismatch", runKey)
		}
		return existing, nil
	}
	r.runs[runKey] = run
	r.mu.Unlock()
	return run, nil
}
