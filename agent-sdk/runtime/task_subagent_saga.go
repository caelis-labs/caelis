package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

const (
	spawnStatusPrepared       = "prepared"
	spawnStatusSpawning       = "spawning"
	spawnStatusSpawned        = "spawned"
	spawnStatusCommitted      = "committed"
	spawnStatusCompensated    = "compensated"
	spawnStatusUnknownOutcome = "unknown_outcome"
)

func (tm *taskRuntime) beginSubagentSpawn(
	ctx context.Context,
	ref session.SessionRef,
	taskID string,
	spawnID string,
	agent string,
	prompt string,
	handle string,
	runner subagent.Runner,
) (*taskapi.Entry, taskapi.Snapshot, bool, error) {
	if tm == nil || tm.store == nil {
		return nil, taskapi.Snapshot{}, false, errors.New("agent-sdk/runtime: durable task store is required before subagent spawn")
	}
	if existing, err := tm.store.Get(ctx, taskID); err == nil && existing != nil {
		if taskSpecString(existing.Spec, "spawn_identity") != strings.TrimSpace(spawnID) ||
			taskSpecString(existing.Spec, "agent") != strings.TrimSpace(agent) ||
			taskSpecString(existing.Spec, "prompt") != strings.TrimSpace(prompt) {
			return nil, taskapi.Snapshot{}, false, fmt.Errorf("agent-sdk/runtime: spawn identity %q conflicts with durable intent", spawnID)
		}
		status := taskStringValue(existing.Metadata["spawn_status"])
		switch status {
		case spawnStatusCommitted:
			task := tm.rehydrateSubagentTask(existing)
			tm.rememberRehydratedSubagent(task)
			return existing, task.snapshot(), true, nil
		case spawnStatusPrepared:
			// The durable record proves the external effect was not requested yet.
		case spawnStatusSpawned:
			task := tm.rehydrateSubagentTask(existing)
			task.runner = runner
			tm.rememberRehydratedSubagent(task)
			cause := errors.New("agent-sdk/runtime: restart found spawned child before saga commit")
			compensateErr := tm.compensateSubagentSpawn(ctx, task, cause, false)
			return existing, task.snapshot(), true, compensateErr
		case spawnStatusSpawning, spawnStatusUnknownOutcome:
			next := taskapi.CloneEntry(existing)
			next.State = taskapi.StateUnknownOutcome
			next.Running = false
			setSpawnEntryStatus(next, spawnStatusUnknownOutcome, "restart found an uncommitted external spawn boundary")
			_ = tm.persistTaskEntry(context.WithoutCancel(ctx), next)
			return next, snapshotFromTaskEntry(next), true, fmt.Errorf("agent-sdk/runtime: subagent spawn %q has unknown outcome; refusing blind respawn", spawnID)
		case spawnStatusCompensated:
			return existing, snapshotFromTaskEntry(existing), true, fmt.Errorf("agent-sdk/runtime: subagent spawn %q was compensated", spawnID)
		default:
			return existing, snapshotFromTaskEntry(existing), true, fmt.Errorf("agent-sdk/runtime: subagent spawn %q has invalid durable status %q", spawnID, status)
		}
		setSpawnEntryStatus(existing, spawnStatusSpawning, "")
		if err := tm.persistTaskEntry(ctx, existing); err != nil {
			return nil, taskapi.Snapshot{}, false, err
		}
		return existing, taskapi.Snapshot{}, false, nil
	}

	now := tm.runtime.now()
	entry := &taskapi.Entry{
		TaskID: taskID, Kind: taskapi.KindSubagent, Session: session.NormalizeSessionRef(ref),
		Title: "SPAWN " + strings.TrimSpace(agent), State: taskapi.StatePrepared, CreatedAt: now, UpdatedAt: now,
		SupportsCancel: true,
		Spec: map[string]any{
			"spawn_identity": strings.TrimSpace(spawnID), "agent": strings.TrimSpace(agent), "prompt": strings.TrimSpace(prompt),
			"handle": strings.TrimSpace(handle), "terminal_id": subagentTerminalID(taskID), "turn_seq": int64(1),
		},
		Metadata: map[string]any{"spawn_status": spawnStatusPrepared, "spawn_identity": strings.TrimSpace(spawnID)},
	}
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return nil, taskapi.Snapshot{}, false, err
	}
	setSpawnEntryStatus(entry, spawnStatusSpawning, "")
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return nil, taskapi.Snapshot{}, false, err
	}
	return entry, taskapi.Snapshot{}, false, nil
}

func (tm *taskRuntime) rememberRehydratedSubagent(task *subagentTask) {
	if tm == nil || task == nil {
		return
	}
	tm.mu.Lock()
	tm.subagents[task.ref.TaskID] = task
	tm.rememberSubagentHandleLocked(task.sessionRef.SessionID, task.handle)
	tm.mu.Unlock()
}

func setSpawnEntryStatus(entry *taskapi.Entry, status string, reason string) {
	if entry == nil {
		return
	}
	if entry.Metadata == nil {
		entry.Metadata = map[string]any{}
	}
	entry.Metadata["spawn_status"] = strings.TrimSpace(status)
	if strings.TrimSpace(reason) != "" {
		entry.Metadata["spawn_reason"] = strings.TrimSpace(reason)
	}
}

func snapshotFromTaskEntry(entry *taskapi.Entry) taskapi.Snapshot {
	if entry == nil {
		return taskapi.Snapshot{}
	}
	return taskapi.Snapshot{
		Ref:      taskapi.Ref{TaskID: entry.TaskID, SessionID: taskSpecString(entry.Spec, "session_id"), TerminalID: taskSpecString(entry.Spec, "terminal_id")},
		Revision: entry.Revision, Kind: entry.Kind, Title: entry.Title, State: entry.State, Running: entry.Running,
		SupportsInput: entry.SupportsInput, SupportsCancel: entry.SupportsCancel, CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt,
		Result: session.CloneState(entry.Result), Metadata: session.CloneState(entry.Metadata),
	}
}

func (tm *taskRuntime) markSubagentSpawnStatus(ctx context.Context, task *subagentTask, status string, reason string) error {
	if task == nil {
		return nil
	}
	task.mu.Lock()
	if task.metadata == nil {
		task.metadata = map[string]any{}
	}
	task.metadata["spawn_status"] = status
	if reason != "" {
		task.metadata["spawn_reason"] = strings.TrimSpace(reason)
	}
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	return tm.persistTaskEntry(ctx, entry)
}

func (tm *taskRuntime) compensateSubagentSpawn(ctx context.Context, task *subagentTask, cause error, participantAttached bool) error {
	if task == nil {
		return cause
	}
	cancelErr := task.runner.Cancel(context.WithoutCancel(ctx), delegation.CloneAnchor(task.anchor))
	status := spawnStatusCompensated
	state := taskapi.StateCancelled
	if cancelErr != nil {
		status = spawnStatusUnknownOutcome
		state = taskapi.StateUnknownOutcome
	}
	task.mu.Lock()
	task.running = false
	task.state = state
	if task.result == nil {
		task.result = map[string]any{}
	}
	task.result["state"] = string(state)
	task.result["error"] = strings.TrimSpace(cause.Error())
	if task.metadata == nil {
		task.metadata = map[string]any{}
	}
	task.metadata["spawn_status"] = status
	task.metadata["spawn_reason"] = strings.TrimSpace(cause.Error())
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	persistErr := tm.persistTaskEntry(context.WithoutCancel(ctx), entry)
	var detachErr error
	if participantAttached {
		detachErr = tm.detachSubagentParticipant(context.WithoutCancel(ctx), task)
	}
	return errors.Join(cause, cancelErr, persistErr, detachErr)
}

func (tm *taskRuntime) detachSubagentParticipant(ctx context.Context, task *subagentTask) error {
	lifecycle, ok := tm.runtime.sessions.(session.ParticipantLifecycleService)
	if !ok {
		return errors.New("agent-sdk/runtime: participant lifecycle store does not support atomic compensation")
	}
	active, err := tm.runtime.sessions.Session(ctx, task.sessionRef)
	if err != nil {
		return err
	}
	binding, ok := participantBinding(active, task.anchor.AgentID)
	if !ok {
		return nil
	}
	event := participantLifecycleEvent(active, binding, "detached", tm.runtime.now())
	_, _, err = lifecycle.RemoveParticipantWithEvent(ctx, session.RemoveParticipantWithEventRequest{
		SessionRef: task.sessionRef, ExpectedRevision: &active.Revision, ParticipantID: binding.ID, Event: event,
	})
	return err
}
