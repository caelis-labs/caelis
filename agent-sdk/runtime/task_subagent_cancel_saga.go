package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
)

// subagentCancelPhase records the one-way boundary around the remote Cancel
// effect. A durable claim is never blindly re-issued after ownership is lost;
// retries preserve unknown outcome until the producer CompletionSink publishes
// the terminal state. Even effect_applied records only the remote Cancel
// acknowledgement, not proof that the child reached a cancelled terminal.
type subagentCancelPhase string

const (
	subagentCancelPhaseNone    subagentCancelPhase = ""
	subagentCancelPhaseClaimed subagentCancelPhase = "subagent_cancel_claimed"
	subagentCancelPhaseUnknown subagentCancelPhase = "subagent_cancel_unknown_outcome"
	subagentCancelPhaseApplied subagentCancelPhase = "subagent_cancel_effect_applied"

	subagentCancelRestartDiagnostic = "subagent cancellation outcome could not be confirmed after runtime restart"
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
		// A prior Cancel already crossed the remote effect boundary. Only the
		// producer CompletionSink may prove and publish terminal lifecycle;
		// repeated Task cancel observes the durable pending/unknown phase.
		return task.snapshot(), nil
	}
	if runner == nil {
		return task.snapshot(), fmt.Errorf("agent-sdk/runtime: subagent %q cannot be cancelled because its runner is unavailable", task.ref.TaskID)
	}
	if err := tm.persistSubagentCancelPhase(ctx, task, subagentCancelPhaseClaimed,
		"subagent cancellation was claimed; remote outcome is not yet known"); err != nil {
		return task.snapshot(), err
	}
	if err := runner.Cancel(ctx, delegation.CloneAnchor(task.anchor)); err != nil {
		persistErr := tm.persistSubagentCancelPhase(context.WithoutCancel(ctx), task, subagentCancelPhaseUnknown,
			"remote subagent cancellation outcome could not be confirmed")
		return task.snapshot(), errors.Join(err, persistErr)
	}
	if err := tm.persistSubagentCancelPhase(context.WithoutCancel(ctx), task, subagentCancelPhaseApplied,
		"remote subagent cancellation completed; terminal result is pending"); err != nil {
		return task.snapshot(), err
	}
	return task.snapshot(), nil
}

func (tm *taskRuntime) persistSubagentCancelPhase(
	ctx context.Context,
	task *subagentTask,
	phase subagentCancelPhase,
	reason string,
) error {
	if task == nil {
		return nil
	}
	task.mu.Lock()
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if entry.Metadata == nil {
		entry.Metadata = map[string]any{}
	}
	if entry.Spec == nil {
		entry.Spec = map[string]any{}
	}
	entry.Metadata["cancel_phase"] = string(phase)
	entry.Spec["cancel_phase"] = string(phase)
	entry.State = taskapi.StateUnknownOutcome
	entry.Running = true
	entry.SupportsInput = false
	entry.Metadata["state"] = string(taskapi.StateUnknownOutcome)
	entry.Metadata["running"] = true
	normalizeSubagentEntryResult(entry, reason)
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
	task.state = taskapi.StateUnknownOutcome
	task.running = true
	normalizeSubagentResultForState(&task.result, taskapi.StateUnknownOutcome, reason)
	task.metadata["state"] = string(taskapi.StateUnknownOutcome)
	task.metadata["running"] = true
	task.metadata["cancel_phase"] = string(phase)
	task.notifyStreamChangeLocked()
	task.mu.Unlock()
	return nil
}
