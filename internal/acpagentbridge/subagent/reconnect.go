package subagent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/authentication"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpcleanup"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/sessionconfig"
)

func (r *Runner) reconnectChildEndpointLocked(ctx context.Context, anchor delegation.Anchor, recovery *tasksubagent.ReconnectRequest, slot *childSlot) (*childRun, error) {
	run, _, err := r.loadChildEndpointLocked(ctx, anchor, recovery, slot, true, false)
	return run, err
}

func (r *Runner) loadChildEndpointLocked(
	ctx context.Context,
	anchor delegation.Anchor,
	recovery *tasksubagent.ReconnectRequest,
	slot *childSlot,
	configure bool,
	metadataOnly bool,
) (reconnected *childRun, loaded session.LoadedSession, reconnectErr error) {
	if slot != nil {
		defer func() {
			r.logChildError(ctx, slot.currentRun(), "endpoint_reconnect", reconnectErr)
		}()
	}
	if recovery == nil {
		return nil, session.LoadedSession{}, fmt.Errorf("target Agent reconnect context is required")
	}
	recovery = tasksubagent.CloneReconnectRequest(recovery)
	spawn := recovery.Spawn
	anchor = delegation.CloneAnchor(anchor)
	if strings.TrimSpace(anchor.TaskID) == "" || strings.TrimSpace(anchor.SessionID) == "" ||
		strings.TrimSpace(anchor.AgentID) == "" {
		return nil, session.LoadedSession{}, fmt.Errorf("target Agent reconnect anchor is incomplete")
	}
	if strings.TrimSpace(spawn.TaskID) != strings.TrimSpace(anchor.TaskID) ||
		strings.TrimSpace(spawn.SessionRef.SessionID) == "" {
		return nil, session.LoadedSession{}, fmt.Errorf("target Agent reconnect identity does not match its Task")
	}
	if err := delegation.ValidateTarget(recovery.Target); err != nil {
		return nil, session.LoadedSession{}, err
	}
	cfg, err := r.resolveSpawnConfig(ctx, spawn, delegation.TargetRequest{Target: recovery.Target})
	if err != nil {
		return nil, session.LoadedSession{}, err
	}

	done := make(chan struct{})
	close(done)
	run := &childRun{
		anchor: anchor, agentName: strings.TrimSpace(cfg.Name),
		configuredAuth: controlagents.NormalizeAuthentication(cfg.Authentication),
		spawn:          spawn, taskID: strings.TrimSpace(anchor.TaskID), output: spawn.Output,
		completion: spawn.Completion, state: delegation.StateCompleted, observationOnly: !configure,
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

	collector := newHistoryCollector(r, anchor, cfg.Name)
	defer func() { collector.closeStaging() }()
	run.replay = collector
	launchEnv := childRecoveryEnvironment(cfg)

	acpClient, err := client.Start(childCtx, client.Config{
		MCPGrant:        cfg.MCPGrant,
		MCPServers:      cfg.MCPServers,
		HostedAdapterID: cfg.HostedAdapterID, ConnectionID: cfg.Name, EndpointResolver: r.endpointResolver,
		Command: cfg.Command, Args: append([]string(nil), cfg.Args...), Env: launchEnv,
		WorkDir: pickWorkDir(cfg.WorkDir, spawn.CWD), ClientInfo: r.clientInfo,
		OnUpdate:            func(env client.UpdateEnvelope) { r.handleRecoveredUpdate(run, env) },
		OnPermissionRequest: boundChildPermissionHandler(run, r.permissionCallback(spawn, cfg, anchor.AgentID)),
	})
	if err != nil {
		childCancel()
		return nil, session.LoadedSession{}, err
	}
	run.mu.Lock()
	run.client = acpClient
	run.mu.Unlock()
	initialize, err := acpClient.Initialize(ctx)
	if err != nil {
		childCancel()
		_ = acpcleanup.CloseClient(ctx, acpClient)
		return nil, session.LoadedSession{}, err
	}
	supportsSteering, err := client.SupportsSessionSteering(initialize)
	if err != nil {
		childCancel()
		closeErr := acpcleanup.CloseClient(ctx, acpClient)
		return nil, session.LoadedSession{}, fmt.Errorf("negotiate Target Agent messaging capability: %w", errors.Join(err, closeErr))
	}
	if !initialize.AgentCapabilities.LoadSession {
		childCancel()
		_ = acpcleanup.CloseClient(ctx, acpClient)
		return nil, session.LoadedSession{}, errorcode.New(errorcode.Unsupported, fmt.Sprintf("Target Agent %q does not support session/load", cfg.Name))
	}
	authenticationMethods := authentication.Methods(initialize)
	recovered, err := authentication.RecoverConfiguredCall(ctx, acpClient, authenticationMethods, cfg.Name,
		controlagents.NormalizeAuthentication(cfg.Authentication),
		func(loadCtx context.Context, activeClient *client.Client) (client.LoadSessionResponse, error) {
			// A proven auth-required retry starts a new complete replay, never appends
			// a rejected load's partial history or initialization notices.
			collector.closeStaging()
			collector = newHistoryCollector(r, anchor, cfg.Name)
			if metadataOnly {
				if _, ok := spawn.Output.(output.StreamingHistoryObserver); ok {
					collector.openStaging()
				}
			}
			run.mu.Lock()
			run.replay = collector
			run.mu.Unlock()
			return activeClient.LoadSessionWithReplayEnd(loadCtx, anchor.SessionID, spawn.CWD, subagentSessionMeta(spawn),
				func(boundaryCtx context.Context) error {
					if err := collector.errSnapshot(); err != nil {
						return err
					}
					if observer, ok := spawn.Output.(output.StreamingHistoryObserver); ok && collector.staging != nil {
						if err := observer.ReplaceTaskHistoryStream(boundaryCtx, collector.streamEvents); err != nil {
							return err
						}
					} else if observer, ok := spawn.Output.(output.HistoryObserver); ok {
						if err := observer.ReplaceTaskHistory(boundaryCtx, collector.eventsSnapshot()); err != nil {
							return err
						}
					}
					run.mu.Lock()
					run.replay = nil
					run.mu.Unlock()
					return nil
				})
		})
	if err != nil {
		childCancel()
		_ = acpcleanup.CloseClient(ctx, acpClient)
		return nil, session.LoadedSession{}, err
	}
	run.pendingConfiguration = &childPendingConfiguration{config: cfg, state: sessionconfig.State{ConfigOptions: recovered.ConfigOptions, Models: recovered.Models}}
	if configure {
		if err := r.applyChildConfiguration(ctx, run); err != nil {
			childCancel()
			_ = acpcleanup.CloseClient(ctx, acpClient)
			return nil, session.LoadedSession{}, err
		}
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
		return nil, session.LoadedSession{}, err
	}
	if err := slot.finalizeTarget(childEndpointFromReconnect(anchor, recovery)); err != nil {
		childCancel()
		_ = acpcleanup.CloseClient(ctx, acpClient)
		return nil, session.LoadedSession{}, err
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
	loaded = session.LoadedSession{Session: session.Session{SessionRef: session.SessionRef{SessionID: anchor.SessionID}, CWD: spawn.CWD}}
	if !metadataOnly {
		loaded.Events = collector.eventsSnapshot()
	}
	return run, loaded, nil
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

// A read-only history open defers configuration until an authorized input.
// Keeping it on the run allows that input to reuse the loaded ACP connection.
type childPendingConfiguration struct {
	config AgentConfig
	state  sessionconfig.State
}

func (r *Runner) applyChildConfiguration(ctx context.Context, run *childRun) error {
	run.mu.RLock()
	pending, spawn, sessionID := run.pendingConfiguration, run.spawn, run.anchor.SessionID
	run.mu.RUnlock()
	if pending == nil {
		return nil
	}
	run.mu.Lock()
	run.observationOnly = false
	run.mu.Unlock()
	options := pending.config.SessionOptions
	if r.sessionPreparer != nil {
		var err error
		options, err = r.sessionPreparer(ctx, spawn, sessionID, pending.config)
		if err != nil {
			return err
		}
	}
	if _, err := sessionconfig.Apply(ctx, run.client, sessionID, pending.state, options); err != nil {
		return err
	}
	run.mu.Lock()
	run.pendingConfiguration = nil
	run.mu.Unlock()
	return nil
}

func (r *Runner) handleRecoveredUpdate(run *childRun, env client.UpdateEnvelope) {
	run.mu.RLock()
	replay := run.replay
	run.mu.RUnlock()
	if replay != nil {
		replay.observe(env)
		return
	}
	r.handleUpdate(run, env)
}
