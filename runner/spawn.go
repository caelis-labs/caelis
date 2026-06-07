package runner

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/session"
	"github.com/OnslaughtSnail/caelis/tool"
)

var spawnCounter atomic.Int64

type runnerSpawnDelegator struct {
	cfg     Config
	parent  session.Session
	agent   agent.Agent
	branch  string
	taskRun string
}

func newRunnerSpawnDelegator(cfg Config, parent session.Session, current agent.Agent, branch string, runID string) agent.SpawnDelegator {
	return &runnerSpawnDelegator{
		cfg:     cfg,
		parent:  parent,
		agent:   current,
		branch:  branch,
		taskRun: runID,
	}
}

func (d *runnerSpawnDelegator) Spawn(ctx tool.Context, req agent.SpawnRequest) (agent.SpawnResult, error) {
	child := d.findChild(req.AgentName)
	if child == nil {
		name := strings.TrimSpace(req.AgentName)
		if name == "" {
			name = "<default>"
		}
		return agent.SpawnResult{}, fmt.Errorf("child agent %s not found", name)
	}
	childSession, err := d.cfg.Sessions.Create(ctx, session.CreateRequest{
		AppName:      d.parent.Ref.AppName,
		UserID:       d.parent.Ref.UserID,
		WorkspaceKey: d.parent.Ref.WorkspaceKey,
		Title:        "SPAWN " + child.Name(),
		Workspace:    d.parent.Workspace.Clone(),
		State: session.State{
			"parent_session": d.parent.Ref.String(),
			"parent_run":     firstNonEmpty(req.RunID, d.taskRun),
			"parent_tool":    "SPAWN",
			"agent":          child.Name(),
		},
	})
	if err != nil {
		return agent.SpawnResult{}, err
	}
	taskID := fmt.Sprintf("task-%d-%d-spawn", time.Now().UnixNano(), spawnCounter.Add(1))
	taskStore := d.cfg.TaskStore
	if taskStore == nil {
		taskStore = NewMemoryTaskStore()
	}
	started := time.Now()
	childCtx, childCancel := context.WithCancel(ctx)
	if err := taskStore.SaveTask(ctx, TaskSnapshot{
		SessionRef: childSession.Ref.String(),
		TaskID:     taskID,
		State:      TaskStateRunning,
		Started:    started,
	}); err != nil {
		childCancel()
		return agent.SpawnResult{}, err
	}
	if err := registerTaskCancel(ctx, taskStore, taskID, childCancel); err != nil {
		childCancel()
		return agent.SpawnResult{}, err
	}
	childBranch := firstNonEmpty(req.Branch, d.branch)
	go d.runChildTask(childCtx, childCancel, taskStore, taskID, childSession, child, req.Prompt, started, childBranch)
	return agent.SpawnResult{
		HandleID:     taskID,
		FinalMessage: "task started: " + taskID,
	}, nil
}

func (d *runnerSpawnDelegator) runChildTask(ctx context.Context, cancel context.CancelFunc, taskStore TaskStore, taskID string, childSession session.Session, child agent.Agent, prompt string, started time.Time, branch string) {
	defer cancel()
	defer unregisterTaskCancel(context.Background(), taskStore, taskID)
	childCfg := d.cfg
	childCfg.Agent = child
	childCfg.TaskStore = taskStore
	r, err := New(childCfg)
	if err != nil {
		_ = taskStore.SaveTask(context.Background(), TaskSnapshot{
			SessionRef: childSession.Ref.String(),
			TaskID:     taskID,
			State:      TaskStateFailed,
			Error:      err.Error(),
			Started:    started,
			Ended:      time.Now(),
		})
		return
	}
	var final string
	for evt, err := range r.Run(ctx, RunRequest{
		SessionRef: childSession.Ref,
		Branch:     branch,
		UserMessage: model.Message{
			Role:    model.RoleUser,
			Content: []model.Part{{Text: prompt}},
		},
	}) {
		if err != nil {
			state := TaskStateFailed
			if ctx.Err() != nil {
				state = TaskStateCancelled
			}
			_ = taskStore.SaveTask(context.Background(), TaskSnapshot{
				SessionRef: childSession.Ref.String(),
				TaskID:     taskID,
				State:      state,
				Error:      err.Error(),
				Started:    started,
				Ended:      time.Now(),
			})
			return
		}
		if evt.Kind == session.EventKindAssistant {
			if text := strings.TrimSpace(evt.TextContent()); text != "" {
				final = text
			}
		}
	}
	_ = taskStore.SaveTask(context.Background(), TaskSnapshot{
		SessionRef: childSession.Ref.String(),
		TaskID:     taskID,
		State:      TaskStateCompleted,
		Output:     final,
		Started:    started,
		Ended:      time.Now(),
	})
}

func (d *runnerSpawnDelegator) WriteTask(ctx context.Context, taskID string, input string) (TaskSnapshot, error) {
	taskStore := d.cfg.TaskStore
	if taskStore == nil {
		return TaskSnapshot{}, fmt.Errorf("task store unavailable")
	}
	snap, ok, err := taskStore.LoadTask(ctx, taskID)
	if err != nil {
		return TaskSnapshot{}, err
	}
	if !ok {
		return TaskSnapshot{}, fmt.Errorf("task not found: %s", taskID)
	}
	switch snap.State {
	case TaskStateRunning:
		return TaskSnapshot{}, fmt.Errorf("task %s is still running; wait before continuing", taskID)
	case TaskStateCancelled:
		return TaskSnapshot{}, fmt.Errorf("task %s is cancelled", taskID)
	}
	childRef, err := parseSessionRef(snap.SessionRef)
	if err != nil {
		return TaskSnapshot{}, err
	}
	childSession, err := d.cfg.Sessions.Get(ctx, childRef)
	if err != nil {
		return TaskSnapshot{}, err
	}
	child := d.findChild(childSession.State["agent"])
	if child == nil {
		return TaskSnapshot{}, fmt.Errorf("child agent %q not found", childSession.State["agent"])
	}
	started := time.Now()
	childCtx, childCancel := context.WithCancel(ctx)
	running := TaskSnapshot{
		SessionRef: childSession.Ref.String(),
		TaskID:     taskID,
		State:      TaskStateRunning,
		Started:    started,
	}
	if err := taskStore.SaveTask(ctx, running); err != nil {
		childCancel()
		return TaskSnapshot{}, err
	}
	if err := registerTaskCancel(ctx, taskStore, taskID, childCancel); err != nil {
		childCancel()
		return TaskSnapshot{}, err
	}
	go d.runChildTask(childCtx, childCancel, taskStore, taskID, childSession, child, input, started, d.branch)
	return running, nil
}

func registerTaskCancel(ctx context.Context, store TaskStore, taskID string, cancel context.CancelFunc) error {
	if cancelStore, ok := store.(TaskCancelStore); ok {
		return cancelStore.RegisterTaskCancel(ctx, taskID, cancel)
	}
	return nil
}

func unregisterTaskCancel(ctx context.Context, store TaskStore, taskID string) {
	if cancelStore, ok := store.(TaskCancelStore); ok {
		cancelStore.UnregisterTaskCancel(ctx, taskID)
	}
}

func (d *runnerSpawnDelegator) findChild(name string) agent.Agent {
	name = strings.TrimSpace(name)
	if name != "" {
		return d.agent.FindAgent(name)
	}
	children := d.agent.SubAgents()
	if len(children) == 1 {
		return children[0]
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func parseSessionRef(raw string) (session.Ref, error) {
	parts := strings.SplitN(strings.TrimSpace(raw), "/", 4)
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] == "" || parts[3] == "" {
		return session.Ref{}, fmt.Errorf("invalid session ref: %q", raw)
	}
	return session.Ref{
		AppName:      parts[0],
		UserID:       parts[1],
		WorkspaceKey: parts[2],
		SessionID:    parts[3],
	}, nil
}
