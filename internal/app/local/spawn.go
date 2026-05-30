package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/core/tool"
	acpexternal "github.com/OnslaughtSnail/caelis/internal/adapters/acpagent/external"
	toolspawn "github.com/OnslaughtSnail/caelis/internal/adapters/tools/spawn"
	"github.com/OnslaughtSnail/caelis/internal/engine/control"
	"github.com/OnslaughtSnail/caelis/internal/engine/loop"
)

type spawnDelegator struct {
	configs []acpexternal.Config
	tasks   *spawnTaskManager
	now     func() time.Time
}

type spawnInput struct {
	Agent       string `json:"agent,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	YieldTimeMS int    `json:"yield_time_ms,omitempty"`
}

func newSpawnDelegator(configs []acpexternal.Config, tasks *spawnTaskManager) *spawnDelegator {
	if len(configs) == 0 {
		return nil
	}
	out := make([]acpexternal.Config, 0, len(configs))
	seen := map[string]struct{}{}
	for _, cfg := range configs {
		id := strings.ToLower(firstNonEmpty(cfg.AgentID, cfg.AgentName, cfg.Command))
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, cfg)
	}
	if len(out) == 0 {
		return nil
	}
	return &spawnDelegator{configs: out, tasks: tasks, now: func() time.Time { return time.Now().UTC() }}
}

func (d *spawnDelegator) Spawn(ctx context.Context, req loop.SpawnRequest) (loop.SpawnResult, error) {
	if d == nil || len(d.configs) == 0 {
		return loop.SpawnResult{}, errors.New("app/local: no ACP agents are available for SPAWN")
	}
	var in spawnInput
	if len(req.Call.Input) > 0 {
		if err := json.Unmarshal(req.Call.Input, &in); err != nil {
			return loop.SpawnResult{}, fmt.Errorf("app/local: invalid SPAWN input: %w", err)
		}
	}
	in.Agent = strings.TrimSpace(in.Agent)
	in.Prompt = strings.TrimSpace(in.Prompt)
	if in.Prompt == "" {
		return loop.SpawnResult{}, errors.New("app/local: SPAWN prompt is required")
	}
	cfg, agent, err := d.resolveAgent(in.Agent)
	if err != nil {
		return loop.SpawnResult{}, err
	}
	if d.tasks != nil {
		return d.spawnTask(ctx, req, in, cfg, agent)
	}
	client, err := acpexternal.StartProcess(ctx, cfg)
	if err != nil {
		return loop.SpawnResult{}, err
	}
	defer client.Close()
	if err := client.InitializeSession(ctx); err != nil {
		return loop.SpawnResult{}, err
	}
	remoteSessionID, err := client.NewCoreSession(ctx, req.Session.Workspace)
	if err != nil {
		return loop.SpawnResult{}, err
	}
	events, err := client.PromptCore(ctx, remoteSessionID, []model.ContentPart{{
		Type: model.ContentPartText,
		Text: in.Prompt,
	}})
	if err != nil {
		return loop.SpawnResult{}, err
	}
	taskID := firstNonEmpty(strings.TrimSpace(req.Call.ID), "spawn-"+agent)
	participant := session.ParticipantBinding{
		ID:           taskID,
		Kind:         session.ParticipantSubagent,
		Role:         session.ParticipantDelegated,
		AgentName:    agent,
		Label:        agent,
		SessionID:    remoteSessionID,
		Source:       "spawn",
		ParentTurnID: strings.TrimSpace(req.TurnID),
		DelegationID: taskID,
		AttachedAt:   d.clock(),
	}
	events = control.NormalizeParticipantEvents(req.Session.SessionID, remoteSessionID, participant, events, d.clock())
	for idx := range events {
		if events[idx].Scope == nil {
			continue
		}
		events[idx].Scope.Source = "spawn"
		events[idx].Scope.Participant = participant
	}
	finalMessage := lastAssistantText(events)
	payload := map[string]any{
		"task_id": taskID,
		"agent":   agent,
		"state":   "completed",
		"running": false,
	}
	if finalMessage != "" {
		payload["final_message"] = finalMessage
	}
	parts, err := toolspawn.ResultParts(payload)
	if err != nil {
		return loop.SpawnResult{}, err
	}
	return loop.SpawnResult{
		Result: tool.Result{
			ID:      strings.TrimSpace(req.Call.ID),
			Name:    toolspawn.ToolName,
			Content: parts,
			Meta: map[string]any{
				"task_id":           taskID,
				"agent":             agent,
				"state":             "completed",
				"running":           false,
				"remote_session_id": remoteSessionID,
				"caelis": map[string]any{
					"version": 1,
					"runtime": map[string]any{
						"tool": map[string]any{
							"name":   toolspawn.ToolName,
							"agent":  agent,
							"prompt": in.Prompt,
						},
						"task": map[string]any{
							"task_id":           taskID,
							"task_kind":         "subagent",
							"state":             "completed",
							"running":           false,
							"agent":             agent,
							"remote_session_id": remoteSessionID,
						},
					},
				},
			},
		},
		Events: events,
	}, nil
}

func (d *spawnDelegator) spawnTask(ctx context.Context, req loop.SpawnRequest, in spawnInput, cfg acpexternal.Config, agent string) (loop.SpawnResult, error) {
	taskID := firstNonEmpty(strings.TrimSpace(req.Call.ID), "spawn-"+agent)
	task, err := d.tasks.Start(spawnTaskStartRequest{
		Session: req.Session,
		TurnID:  req.TurnID,
		TaskID:  taskID,
		Config:  cfg,
		Agent:   agent,
		Prompt:  in.Prompt,
	})
	if err != nil {
		return loop.SpawnResult{}, err
	}
	wait := spawnWaitDuration(in.YieldTimeMS)
	if wait > 0 {
		waitCtx, cancel := context.WithTimeout(ctx, wait)
		_, err := task.Wait(waitCtx)
		cancel()
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			task.MarkAsync()
			return loop.SpawnResult{Result: d.resultForTask(req, task, nil)}, nil
		}
		if err != nil {
			return loop.SpawnResult{}, err
		}
	} else {
		if _, err := task.Wait(ctx); err != nil {
			_ = task.Cancel(context.Background())
			return loop.SpawnResult{}, err
		}
	}
	return loop.SpawnResult{
		Result: d.resultForTask(req, task, task.Events()),
		Events: task.Events(),
	}, nil
}

func (d *spawnDelegator) resultForTask(req loop.SpawnRequest, task *spawnTaskSession, events []session.Event) tool.Result {
	snapshot, _ := task.Snapshot(context.Background())
	output, _ := task.Read(context.Background(), sandbox.OutputCursor{})
	finalMessage := strings.TrimSpace(output.Stdout)
	payload := map[string]any{
		"task_id": task.taskID,
		"agent":   task.agent,
		"state":   string(snapshot.State),
		"running": snapshot.Running,
	}
	if finalMessage == "" && len(events) > 0 {
		finalMessage = lastAssistantText(events)
	}
	if finalMessage != "" && !snapshot.Running {
		payload["final_message"] = finalMessage
	}
	parts, err := toolspawn.ResultParts(payload)
	if err != nil {
		parts = []model.Part{model.NewTextPart("task_id: " + task.taskID)}
	}
	taskMeta := map[string]any{
		"task_id":   task.taskID,
		"task_kind": "subagent",
		"state":     string(snapshot.State),
		"running":   snapshot.Running,
		"agent":     task.agent,
	}
	if remoteSessionID := taskRemoteSessionID(task); remoteSessionID != "" {
		taskMeta["remote_session_id"] = remoteSessionID
	}
	return tool.Result{
		ID:      strings.TrimSpace(req.Call.ID),
		Name:    toolspawn.ToolName,
		IsError: snapshot.State == sandbox.SessionFailed,
		Content: parts,
		Meta: map[string]any{
			"task_id": task.taskID,
			"agent":   task.agent,
			"state":   string(snapshot.State),
			"running": snapshot.Running,
			"caelis": map[string]any{
				"version": 1,
				"runtime": map[string]any{
					"tool": map[string]any{
						"name":  toolspawn.ToolName,
						"agent": task.agent,
					},
					"task": taskMeta,
				},
			},
		},
	}
}

func spawnWaitDuration(ms int) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func taskRemoteSessionID(task *spawnTaskSession) string {
	if task == nil {
		return ""
	}
	task.mu.RLock()
	defer task.mu.RUnlock()
	return strings.TrimSpace(task.remoteSessionID)
}

func (d *spawnDelegator) resolveAgent(requested string) (acpexternal.Config, string, error) {
	if requested == "" {
		if cfg, ok := d.lookupAgent("self"); ok {
			return cfg, "self", nil
		}
		if len(d.configs) == 1 {
			cfg := d.configs[0]
			return cfg, firstNonEmpty(cfg.AgentName, cfg.AgentID, cfg.Command), nil
		}
		return acpexternal.Config{}, "", errors.New("app/local: SPAWN agent is required when no self agent is configured")
	}
	cfg, ok := d.lookupAgent(requested)
	if !ok {
		return acpexternal.Config{}, "", fmt.Errorf("app/local: unknown SPAWN agent %q", requested)
	}
	return cfg, firstNonEmpty(cfg.AgentName, cfg.AgentID, requested), nil
}

func (d *spawnDelegator) lookupAgent(name string) (acpexternal.Config, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return acpexternal.Config{}, false
	}
	for _, cfg := range d.configs {
		if strings.EqualFold(strings.TrimSpace(cfg.AgentID), name) || strings.EqualFold(strings.TrimSpace(cfg.AgentName), name) {
			return cfg, true
		}
	}
	return acpexternal.Config{}, false
}

func (d *spawnDelegator) clock() time.Time {
	if d != nil && d.now != nil {
		return d.now()
	}
	return time.Now().UTC()
}

func lastAssistantText(events []session.Event) string {
	for idx := len(events) - 1; idx >= 0; idx-- {
		event := events[idx]
		if event.Message == nil || event.Message.Role != model.RoleAssistant {
			continue
		}
		if text := strings.TrimSpace(event.Message.TextContent()); text != "" {
			return text
		}
	}
	return ""
}
