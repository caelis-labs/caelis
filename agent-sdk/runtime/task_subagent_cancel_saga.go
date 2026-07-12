package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
)

// subagentCancelPhase records the one-way boundary around the remote Cancel
// effect. A durable claim is never blindly re-issued after ownership is lost;
// retries reconcile through Wait and preserve unknown outcome until terminal
// state is observed.
type subagentCancelPhase string

const (
	subagentCancelPhaseNone      subagentCancelPhase = ""
	subagentCancelPhaseClaimed   subagentCancelPhase = "subagent_cancel_claimed"
	subagentCancelPhaseUnknown   subagentCancelPhase = "subagent_cancel_unknown_outcome"
	subagentCancelPhaseApplied   subagentCancelPhase = "subagent_cancel_effect_applied"
	subagentCancelPhaseCompleted subagentCancelPhase = "subagent_cancel_completed"
)

func (tm *taskRuntime) cancelSubagentSaga(ctx context.Context, task *subagentTask) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: task is required")
	}
	task.mu.Lock()
	running := task.running
	phase := subagentCancelPhase(taskStringValue(task.metadata["cancel_phase"]))
	runner := task.runner
	task.mu.Unlock()
	if !running {
		return task.snapshot(), nil
	}
	if phase != subagentCancelPhaseNone {
		return tm.advanceSubagentCancel(ctx, task, phase, 10)
	}
	if runner == nil {
		return task.snapshot(), fmt.Errorf("agent-sdk/runtime: subagent %q cannot be cancelled because its runner is unavailable", task.ref.TaskID)
	}
	if err := tm.persistSubagentCancelPhase(ctx, task, subagentCancelPhaseClaimed,
		"subagent cancellation was claimed; remote outcome is not yet known", nil, false); err != nil {
		return task.snapshot(), err
	}
	if err := runner.Cancel(ctx, delegation.CloneAnchor(task.anchor)); err != nil {
		persistErr := tm.persistSubagentCancelPhase(context.WithoutCancel(ctx), task, subagentCancelPhaseUnknown,
			"remote subagent cancellation outcome could not be confirmed", nil, false)
		return task.snapshot(), errors.Join(err, persistErr)
	}
	if err := tm.persistSubagentCancelPhase(context.WithoutCancel(ctx), task, subagentCancelPhaseApplied,
		"remote subagent cancellation completed; terminal result is pending", nil, false); err != nil {
		return task.snapshot(), err
	}
	return tm.advanceSubagentCancel(ctx, task, subagentCancelPhaseApplied, 10)
}

func (tm *taskRuntime) advanceSubagentCancel(
	ctx context.Context,
	task *subagentTask,
	phase subagentCancelPhase,
	yieldMS int,
) (taskapi.Snapshot, error) {
	if task == nil || task.runner == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: subagent cancellation cannot be reconciled without a runner")
	}
	result, err := task.runner.Wait(ctx, delegation.CloneAnchor(task.anchor), yieldMS)
	if err != nil {
		if phase == subagentCancelPhaseClaimed {
			persistErr := tm.persistSubagentCancelPhase(context.WithoutCancel(ctx), task, subagentCancelPhaseUnknown,
				"remote subagent cancellation outcome could not be confirmed", nil, false)
			return task.snapshot(), errors.Join(err, persistErr)
		}
		return task.snapshot(), err
	}
	if result.State == delegation.StateRunning {
		next := phase
		if phase == subagentCancelPhaseClaimed {
			next = subagentCancelPhaseUnknown
		}
		if err := tm.persistSubagentCancelPhase(ctx, task, next,
			"remote subagent cancellation is not yet terminal", &result, false); err != nil {
			return task.snapshot(), err
		}
		return task.snapshot(), nil
	}
	if err := tm.persistSubagentCancelPhase(ctx, task, subagentCancelPhaseCompleted, "", &result, true); err != nil {
		return task.snapshot(), err
	}
	snapshot := task.snapshot()
	tm.mu.Lock()
	if tm.subagents[task.ref.TaskID] == task {
		delete(tm.subagents, task.ref.TaskID)
	}
	tm.mu.Unlock()
	_ = tm.updateSubagentParticipant(ctx, task, "detached")
	return snapshot, nil
}

func (tm *taskRuntime) persistSubagentCancelPhase(
	ctx context.Context,
	task *subagentTask,
	phase subagentCancelPhase,
	reason string,
	result *delegation.Result,
	terminal bool,
) error {
	if task == nil {
		return nil
	}
	task.mu.Lock()
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if result != nil {
		desired := tm.rehydrateSubagentTask(entry)
		desired.applyResult(*result)
		desired.seedStreamFromResult(*result)
		entry = desired.entrySnapshot(tm.runtime.now())
	}
	if entry.Metadata == nil {
		entry.Metadata = map[string]any{}
	}
	if entry.Spec == nil {
		entry.Spec = map[string]any{}
	}
	entry.Metadata["cancel_phase"] = string(phase)
	entry.Spec["cancel_phase"] = string(phase)
	if terminal {
		entry.State = taskapi.StateCancelled
		entry.Running = false
		entry.SupportsInput = false
		entry.Metadata["state"] = string(taskapi.StateCancelled)
		entry.Metadata["running"] = false
		if entry.Result == nil {
			entry.Result = map[string]any{}
		}
		entry.Result["state"] = string(taskapi.StateCancelled)
		if spawned, ok := entry.Spec["spawn_result"].(map[string]any); ok {
			spawned["state"] = string(taskapi.StateCancelled)
			entry.Spec["spawn_result"] = spawned
		}
	} else {
		entry.State = taskapi.StateUnknownOutcome
		entry.Running = true
		entry.SupportsInput = false
		entry.Metadata["state"] = string(taskapi.StateUnknownOutcome)
		entry.Metadata["running"] = true
		if entry.Result == nil {
			entry.Result = map[string]any{}
		}
		entry.Result["state"] = string(taskapi.StateUnknownOutcome)
		if strings.TrimSpace(reason) != "" {
			entry.Result["error"] = strings.TrimSpace(reason)
		}
	}
	if err := tm.persistSpawnEntry(ctx, entry); err != nil {
		return err
	}
	task.mu.Lock()
	task.revision = entry.Revision
	task.lease = taskapi.CloneLease(entry.Lease)
	task.state = entry.State
	task.running = entry.Running
	task.result = session.CloneState(entry.Result)
	task.metadata = session.CloneState(entry.Metadata)
	if result != nil {
		task.applyResult(*result)
		task.seedStreamFromResult(*result)
	}
	if terminal {
		task.state = taskapi.StateCancelled
		task.running = false
		task.result["state"] = string(taskapi.StateCancelled)
		task.metadata["state"] = string(taskapi.StateCancelled)
		task.metadata["running"] = false
	} else {
		task.state = taskapi.StateUnknownOutcome
		task.running = true
		task.result["state"] = string(taskapi.StateUnknownOutcome)
		task.metadata["state"] = string(taskapi.StateUnknownOutcome)
		task.metadata["running"] = true
		if strings.TrimSpace(reason) != "" {
			task.result["error"] = strings.TrimSpace(reason)
		}
	}
	task.metadata["cancel_phase"] = string(phase)
	task.mu.Unlock()
	return nil
}
