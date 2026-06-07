package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/runner"
	"github.com/OnslaughtSnail/caelis/session"
	"github.com/OnslaughtSnail/caelis/tool"
)

var spawnCounter atomic.Int64

// SpawnDelegator returns an agent.SpawnDelegator backed by this orchestrator.
// The parent agent is needed to resolve child agents from SubAgents().
func (o *Orchestrator) SpawnDelegator(parent agent.Agent, parentSession session.Session, branch string, runID string) agent.SpawnDelegator {
	return &orchestratorDelegator{
		orch:         o,
		parent:       parent,
		parentSess:   parentSession,
		branch:       branch,
		runID:        runID,
	}
}

// orchestratorDelegator implements agent.SpawnDelegator.
type orchestratorDelegator struct {
	orch       *Orchestrator
	parent     agent.Agent
	parentSess session.Session
	branch     string
	runID      string
}

func (d *orchestratorDelegator) Spawn(ctx tool.Context, req agent.SpawnRequest) (agent.SpawnResult, error) {
	childAgent, cfg, err := d.orch.resolveAgent(ctx, req.AgentName, d.parent)
	if err != nil {
		return agent.SpawnResult{}, err
	}

	childSession, err := d.orch.cfg.Sessions.Create(ctx, session.CreateRequest{
		AppName:      d.parentSess.Ref.AppName,
		UserID:       d.parentSess.Ref.UserID,
		WorkspaceKey: d.parentSess.Ref.WorkspaceKey,
		Title:        "SPAWN " + req.AgentName,
		Workspace:    d.parentSess.Workspace.Clone(),
		State: session.State{
			"parent_session": d.parentSess.Ref.String(),
			"parent_run":     firstNonEmpty(req.RunID, d.runID),
			"parent_tool":    "SPAWN",
			"agent":          req.AgentName,
		},
	})
	if err != nil {
		return agent.SpawnResult{}, fmt.Errorf("orchestrator: create child session: %w", err)
	}

	taskID := fmt.Sprintf("task-%d-%d-spawn", time.Now().UnixNano(), spawnCounter.Add(1))
	childCtx, childCancel := context.WithCancel(context.WithoutCancel(ctx))
	anchor := Anchor{
		TaskID:          taskID,
		ChildSessionRef: childSession.Ref,
		AgentName:       req.AgentName,
		AgentID:         fmt.Sprintf("agent-%d", time.Now().UnixNano()),
		ParentCallID:    "", // set by caller if needed
		ParentRunID:     firstNonEmpty(req.RunID, d.runID),
	}

	handle := newChildHandle(anchor, childCancel)
	d.orch.mu.Lock()
	d.orch.children[taskID] = handle
	d.orch.mu.Unlock()

	// Launch child task in background.
	go d.runChildTask(childCtx, childAgent, cfg, childSession, req.Prompt, handle)

	// Return immediately — caller uses TASK tool to wait/continue/cancel.
	return agent.SpawnResult{
		HandleID:     taskID,
		FinalMessage: "task started: " + taskID,
	}, nil
}

func (d *orchestratorDelegator) runChildTask(
	ctx context.Context,
	childAgent agent.Agent,
	cfg *AgentConfig,
	childSession session.Session,
	prompt string,
	handle *ChildHandle,
) {
	defer handle.Cancel() // ensure cancel is called on exit

	if childAgent != nil && cfg.Internal != nil {
		// Internal agent: run via runner.
		d.runInternalChild(ctx, childAgent, childSession, prompt, handle)
	} else if cfg != nil && cfg.External.Command != "" {
		// External ACP agent: run via ACP client.
		d.runExternalChild(ctx, cfg, childSession, prompt, handle)
	} else {
		handle.markFailed(fmt.Errorf("orchestrator: no execution path for agent %q", cfg.Name))
	}
}

func (d *orchestratorDelegator) runInternalChild(
	ctx context.Context,
	childAgent agent.Agent,
	childSession session.Session,
	prompt string,
	handle *ChildHandle,
) {
	childRunner, err := runner.New(runner.Config{
		Agent:         childAgent,
		Sessions:      d.orch.cfg.Sessions,
		ModelRegistry: d.orch.cfg.ModelRegistry,
		ToolRegistry:  d.orch.cfg.ToolRegistry,
		Sandbox:       d.orch.cfg.Sandbox,
		Policy:        d.orch.cfg.Policy,
		Approver:      d.orch.cfg.Approver,
		Tracer:        d.orch.cfg.Tracer,
		Compactor:     d.orch.cfg.Compactor,
		TaskStore:     d.orch.cfg.TaskStore,
		SystemPrompt:  d.orch.cfg.SystemPrompt,
	})
	if err != nil {
		handle.markFailed(fmt.Errorf("orchestrator: create child runner: %w", err))
		d.saveTaskSnapshot(handle)
		return
	}

	// Save running state to task store.
	d.saveTaskSnapshot(handle)

	var lastText string
	for evt, err := range childRunner.Run(ctx, runner.RunRequest{
		SessionRef:  childSession.Ref,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: prompt}}},
		Branch:      d.branch,
	}) {
		if err != nil {
			handle.markFailed(err)
			d.saveTaskSnapshot(handle)
			return
		}
		if evt.Kind == session.EventKindAssistant && evt.Visibility == session.VisibilityCanonical {
			lastText = evt.TextContent()
		}
	}
	handle.markCompleted(lastText)
	d.saveTaskSnapshot(handle)
}

func (d *orchestratorDelegator) saveTaskSnapshot(handle *ChildHandle) {
	if d.orch.cfg.TaskStore == nil {
		return
	}
	anchor := handle.Anchor()
	state := handle.State()
	taskState := runner.TaskStateRunning
	switch state {
	case DelegationCompleted:
		taskState = runner.TaskStateCompleted
	case DelegationFailed:
		taskState = runner.TaskStateFailed
	case DelegationCancelled:
		taskState = runner.TaskStateCancelled
	}
	snap := runner.TaskSnapshot{
		TaskID:     anchor.TaskID,
		SessionRef: anchor.ChildSessionRef.String(),
		State:      taskState,
		Output:     handle.Output(),
	}
	if state != DelegationRunning {
		snap.Ended = time.Now()
	}
	_ = d.orch.cfg.TaskStore.SaveTask(context.Background(), snap)
}

func (d *orchestratorDelegator) runExternalChild(
	ctx context.Context,
	cfg *AgentConfig,
	childSession session.Session,
	prompt string,
	handle *ChildHandle,
) {
	// External ACP agent execution — placeholder for Step 7 (ACP loopback).
	// For now, mark as failed since external agents need the ACP client factory.
	handle.markFailed(fmt.Errorf("orchestrator: external ACP agent %q not yet implemented", cfg.Name))
}

// Wait blocks until the child completes or the yield deadline passes.
func (o *Orchestrator) Wait(ctx context.Context, taskID string, yieldMS int) (agent.SpawnResult, error) {
	o.mu.Lock()
	handle, ok := o.children[taskID]
	o.mu.Unlock()
	if !ok {
		return agent.SpawnResult{}, fmt.Errorf("orchestrator: task %q not found", taskID)
	}

	handle.waitFor(ctx, yieldMS)
	return agent.SpawnResult{
		HandleID:     taskID,
		FinalMessage: handle.Output(),
	}, nil
}

// Continue sends a new prompt to an existing child session.
func (o *Orchestrator) Continue(ctx context.Context, taskID string, prompt string) (agent.SpawnResult, error) {
	o.mu.Lock()
	handle, ok := o.children[taskID]
	o.mu.Unlock()
	if !ok {
		return agent.SpawnResult{}, fmt.Errorf("orchestrator: task %q not found", taskID)
	}

	if handle.State() != DelegationCompleted && handle.State() != DelegationFailed {
		return agent.SpawnResult{}, fmt.Errorf("orchestrator: task %q is still running", taskID)
	}

	// Re-resolve the agent and re-run.
	anchor := handle.Anchor()
	childAgent, cfg, err := o.resolveAgent(ctx, anchor.AgentName, nil)
	if err != nil {
		return agent.SpawnResult{}, err
	}

	childSession, err := o.cfg.Sessions.Get(ctx, anchor.ChildSessionRef)
	if err != nil {
		return agent.SpawnResult{}, fmt.Errorf("orchestrator: load child session: %w", err)
	}

	// Create a new handle for the continued run.
	childCtx, childCancel := context.WithCancel(context.WithoutCancel(ctx))
	newAnchor := anchor
	newAnchor.TaskID = fmt.Sprintf("task-%d-%d-continue", time.Now().UnixNano(), spawnCounter.Add(1))
	newHandle := newChildHandle(newAnchor, childCancel)

	o.mu.Lock()
	o.children[newAnchor.TaskID] = newHandle
	o.mu.Unlock()

	go o.delegatorFor(childAgent, cfg, childSession, newHandle).runChildTask(
		childCtx, childAgent, cfg, childSession, prompt, newHandle,
	)

	newHandle.waitFor(ctx, 0)
	return agent.SpawnResult{
		HandleID:     newAnchor.TaskID,
		FinalMessage: "continued: " + newAnchor.TaskID,
	}, nil
}

// Cancel cancels a running child.
func (o *Orchestrator) Cancel(ctx context.Context, taskID string) error {
	o.mu.Lock()
	handle, ok := o.children[taskID]
	o.mu.Unlock()
	if !ok {
		return fmt.Errorf("orchestrator: task %q not found", taskID)
	}

	handle.Cancel()
	handle.markCancelled()

	// Save cancelled state to task store if available.
	if o.cfg.TaskStore != nil {
		_ = o.cfg.TaskStore.SaveTask(ctx, runner.TaskSnapshot{
			TaskID:     taskID,
			SessionRef: handle.Anchor().ChildSessionRef.String(),
			State:      runner.TaskStateCancelled,
			Ended:      time.Now(),
		})
	}

	return nil
}

// GetChild returns the child handle for the given task ID.
func (o *Orchestrator) GetChild(taskID string) (*ChildHandle, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	h, ok := o.children[taskID]
	return h, ok
}

func (o *Orchestrator) delegatorFor(childAgent agent.Agent, cfg *AgentConfig, childSession session.Session, handle *ChildHandle) *orchestratorDelegator {
	return &orchestratorDelegator{
		orch:       o,
		parent:     nil,
		parentSess: childSession,
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
