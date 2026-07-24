package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

func commandCancelPhase(phase string) bool {
	switch strings.TrimSpace(phase) {
	case commandPhaseCancelClaimed, commandPhaseCancelUnknown, commandPhaseCancelApplied:
		return true
	default:
		return false
	}
}

func commandUnknownWhileRunningPhase(phase string) bool {
	return strings.TrimSpace(phase) == commandPhaseUnknown ||
		strings.TrimSpace(phase) == commandPhaseEffectClaimed || commandCancelPhase(phase)
}

func (tm *taskRuntime) cancelCommandClaimed(ctx context.Context, task *commandTask) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: command task is required")
	}
	if task.commandOutcomeUnattached() {
		return task.snapshotWithoutSession(tm.runtime.now()), nil
	}
	task.mu.Lock()
	running := task.running
	phase := taskStringValue(task.metadata["command_phase"])
	if !running {
		task.mu.Unlock()
		return tm.snapshotExistingCommand(ctx, task, 0)
	}
	task.mu.Unlock()
	switch phase {
	case commandPhaseCancelClaimed:
		return tm.executeClaimedCommandCancel(ctx, task)
	case commandPhaseCancelUnknown:
		return tm.reconcileCommandCancel(ctx, task, false)
	case commandPhaseCancelApplied:
		return tm.reconcileCommandCancel(ctx, task, true)
	}
	if _, err := tm.persistCommandCancelPhase(ctx, task, commandPhaseCancelClaimed,
		"command cancellation was claimed; external effect outcome is not yet known"); err != nil {
		return task.snapshotWithoutSession(tm.runtime.now()), err
	}
	return tm.executeClaimedCommandCancel(ctx, task)
}

func (tm *taskRuntime) executeClaimedCommandCancel(ctx context.Context, task *commandTask) (taskapi.Snapshot, error) {
	if task == nil || task.session == nil {
		return task.snapshotWithoutSession(tm.runtime.now()), errors.New("agent-sdk/runtime: claimed command cancellation has no live session")
	}
	if err := task.session.Terminate(ctx); err != nil {
		status, statusErr := task.session.Status(context.WithoutCancel(ctx))
		if statusErr == nil && !status.Running {
			return tm.reconcileCommandStatus(context.WithoutCancel(ctx), task, status)
		}
		snapshot, persistErr := tm.persistCommandCancelPhase(context.WithoutCancel(ctx), task, commandPhaseCancelUnknown,
			"command cancellation outcome could not be confirmed")
		return snapshot, errors.Join(err, statusErr, persistErr)
	}
	if _, err := tm.persistCommandCancelPhase(context.WithoutCancel(ctx), task, commandPhaseCancelApplied,
		"command cancellation effect completed; terminal status is pending"); err != nil {
		return task.snapshotWithoutSession(tm.runtime.now()), err
	}
	return tm.waitCommand(ctx, task, taskCancelWait)
}

func (tm *taskRuntime) reconcileCommandCancel(ctx context.Context, task *commandTask, wait bool) (taskapi.Snapshot, error) {
	if task == nil || task.session == nil {
		return task.snapshotWithoutSession(tm.runtime.now()), errors.New("agent-sdk/runtime: command cancellation outcome has no live session")
	}
	if wait {
		return tm.waitCommand(ctx, task, taskCancelWait)
	}
	status, err := task.session.Status(ctx)
	if err != nil {
		return task.snapshotWithoutSession(tm.runtime.now()), err
	}
	if !status.Running {
		return tm.reconcileCommandStatus(ctx, task, status)
	}
	task.mu.Lock()
	snapshot := task.snapshotLocked(status)
	task.mu.Unlock()
	return snapshot, nil
}

func (tm *taskRuntime) persistCommandCancelPhase(ctx context.Context, task *commandTask, phase, reason string) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, errors.New("agent-sdk/runtime: command task is required")
	}
	task.mu.Lock()
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	entry.State = taskapi.StateUnknownOutcome
	entry.Running = false
	entry.Metadata = session.CloneState(entry.Metadata)
	entry.Metadata["state"] = string(taskapi.StateUnknownOutcome)
	entry.Metadata["running"] = false
	entry.Metadata["command_phase"] = phase
	entry.Result = map[string]any{"state": string(taskapi.StateUnknownOutcome), "error": strings.TrimSpace(reason)}
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return task.snapshotWithoutSession(tm.runtime.now()), err
	}
	applyCommandEntry(task, entry)
	return task.snapshotWithoutSession(tm.runtime.now()), nil
}
