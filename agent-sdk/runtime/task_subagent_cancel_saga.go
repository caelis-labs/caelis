package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
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

	subagentCancelPhaseKey   = "cancel_phase"
	subagentCancelTurnSeqKey = "cancel_turn_seq"
)

func subagentCancelTurnSeq(values map[string]any) (int64, bool) {
	turnSeq, ok := taskInt64Value(values[subagentCancelTurnSeqKey])
	return turnSeq, ok && turnSeq > 0
}

func (tm *taskRuntime) cancelSubagentSaga(ctx context.Context, task *subagentTask) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("task is required")
	}
	task.mu.Lock()
	running := task.running
	turnSeq := max(task.turnSeq, 1)
	phase := subagentCancelPhase(taskStringValue(task.metadata[subagentCancelPhaseKey]))
	cancelTurnSeq, cancelTurnScoped := subagentCancelTurnSeq(task.metadata)
	runner := task.runner
	anchor := delegation.CloneAnchor(task.anchor)
	task.mu.Unlock()

	// A non-terminal journal makes the Task conservatively appear running. Its
	// stored generation remains authoritative even when it is one ahead of the
	// last output-derived Task generation.
	if running && phase != subagentCancelPhaseNone {
		if !cancelTurnScoped {
			cancelTurnSeq = turnSeq
		}
		return tm.advanceSubagentCancel(ctx, task, phase, cancelTurnSeq, 10)
	}

	if !running {
		if runner == nil {
			return task.snapshot(), nil
		}
		// Task lifecycle is derived from child activity, so an accepted
		// SendMessage can start a runner-owned Turn before its first event opens
		// the next Task generation. Sample that owner without turning admission
		// into a Task write; a truly idle endpoint remains an idempotent no-op.
		if !subagentRunnerTurnIsLive(ctx, runner, anchor) {
			return task.snapshot(), nil
		}
		targetTurnSeq := turnSeq + 1
		if phase != subagentCancelPhaseNone && cancelTurnScoped && cancelTurnSeq == targetTurnSeq {
			return tm.advanceSubagentCancel(ctx, task, phase, cancelTurnSeq, 10)
		}
		cancelTurnSeq = targetTurnSeq
		// A terminal or legacy journal on the idle observed Turn belongs to the
		// prior activity. The live endpoint proves a later, not-yet-observed Turn.
		phase = subagentCancelPhaseNone
	} else {
		cancelTurnSeq = turnSeq
	}
	if phase != subagentCancelPhaseNone {
		return tm.advanceSubagentCancel(ctx, task, phase, cancelTurnSeq, 10)
	}
	if runner == nil {
		return task.snapshot(), fmt.Errorf("subagent %q cannot be cancelled because its runner is unavailable", task.ref.TaskID)
	}
	persisted, err := tm.persistSubagentCancelPhase(ctx, task, cancelTurnSeq, subagentCancelPhaseClaimed,
		"subagent cancellation was claimed; remote outcome is not yet known", nil, false)
	if err != nil {
		return task.snapshot(), err
	}
	if !persisted {
		return task.snapshot(), nil
	}
	if err := runner.Cancel(ctx, anchor); err != nil {
		_, persistErr := tm.persistSubagentCancelPhase(context.WithoutCancel(ctx), task, cancelTurnSeq, subagentCancelPhaseUnknown,
			"remote subagent cancellation outcome could not be confirmed", nil, false)
		return task.snapshot(), errors.Join(err, persistErr)
	}
	persisted, err = tm.persistSubagentCancelPhase(context.WithoutCancel(ctx), task, cancelTurnSeq, subagentCancelPhaseApplied,
		"remote subagent cancellation was requested; terminal result is pending", nil, false)
	if err != nil {
		return task.snapshot(), err
	}
	if !persisted {
		return task.snapshot(), nil
	}
	return tm.advanceSubagentCancel(ctx, task, subagentCancelPhaseApplied, cancelTurnSeq, 10)
}

func subagentRunnerTurnIsLive(ctx context.Context, runner tasksubagent.Runner, anchor delegation.Anchor) bool {
	if runner == nil {
		return false
	}
	current, err := runner.Wait(ctx, delegation.CloneAnchor(anchor), 0)
	return err == nil && subagentCancelResultPending(current)
}

func (tm *taskRuntime) advanceSubagentCancel(
	ctx context.Context,
	task *subagentTask,
	phase subagentCancelPhase,
	cancelTurnSeq int64,
	yieldMS int,
) (taskapi.Snapshot, error) {
	if task == nil || task.runner == nil {
		return taskapi.Snapshot{}, fmt.Errorf("subagent cancellation cannot be reconciled without a runner")
	}
	result, err := task.runner.Wait(ctx, delegation.CloneAnchor(task.anchor), yieldMS)
	if err != nil {
		if phase == subagentCancelPhaseClaimed {
			_, persistErr := tm.persistSubagentCancelPhase(context.WithoutCancel(ctx), task, cancelTurnSeq, subagentCancelPhaseUnknown,
				"remote subagent cancellation outcome could not be confirmed", nil, false)
			return task.snapshot(), errors.Join(err, persistErr)
		}
		return task.snapshot(), err
	}
	if subagentCancelResultPending(result) {
		next := phase
		if phase == subagentCancelPhaseClaimed {
			next = subagentCancelPhaseUnknown
		}
		if _, err := tm.persistSubagentCancelPhase(ctx, task, cancelTurnSeq, next,
			"remote subagent cancellation is not yet terminal", &result, false); err != nil {
			return task.snapshot(), err
		}
		return task.snapshot(), nil
	}
	if !subagentCancelResultTerminal(result) {
		return task.snapshot(), fmt.Errorf("subagent cancellation reconciliation returned invalid state %q", result.State)
	}
	persisted, err := tm.persistSubagentCancelPhase(ctx, task, cancelTurnSeq, subagentCancelPhaseCompleted, "", &result, true)
	if err != nil {
		return task.snapshot(), err
	}
	if !persisted {
		// The endpoint may finish before its first activity event opens the
		// preselected Task generation. Producer activity/completion remains the
		// authority for that generation; keep cancellation pending until then.
		return task.snapshot(), nil
	}
	snapshot := task.snapshot()
	// Cancel ends the current Turn, not the stable child identity. Keep the
	// participant addressable so a later SendMessage can resume the same remote
	// Session with a fresh Turn.
	_ = tm.updateSubagentParticipant(ctx, task, "updated")
	return snapshot, nil
}

func subagentCancelResultPending(result delegation.Result) bool {
	return result.Running || result.State == delegation.StateRunning || result.State == delegation.StateWaitingApproval
}

func subagentCancelResultTerminal(result delegation.Result) bool {
	switch result.State {
	case delegation.StateCompleted,
		delegation.StateFailed,
		delegation.StateCancelled,
		delegation.StateInterrupted,
		delegation.StateUnknownOutcome:
		return true
	default:
		return false
	}
}

func (tm *taskRuntime) persistSubagentCancelPhase(
	ctx context.Context,
	task *subagentTask,
	cancelTurnSeq int64,
	phase subagentCancelPhase,
	reason string,
	result *delegation.Result,
	terminal bool,
) (bool, error) {
	if task == nil {
		return false, nil
	}
	if cancelTurnSeq <= 0 {
		return false, fmt.Errorf("subagent cancellation target generation is required")
	}
	if terminal && (result == nil || !subagentCancelResultTerminal(*result) || result.Running) {
		return false, fmt.Errorf("subagent cancellation terminal persistence requires an explicit terminal result")
	}
	task.activityApplyMu.Lock()
	defer task.activityApplyMu.Unlock()
	task.mu.Lock()
	currentTurnSeq := max(task.turnSeq, 1)
	if currentTurnSeq > cancelTurnSeq || terminal && currentTurnSeq != cancelTurnSeq {
		task.mu.Unlock()
		return false, nil
	}
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
	entry.Metadata[subagentCancelPhaseKey] = string(phase)
	entry.Metadata[subagentCancelTurnSeqKey] = cancelTurnSeq
	entry.Spec[subagentCancelPhaseKey] = string(phase)
	entry.Spec[subagentCancelTurnSeqKey] = cancelTurnSeq
	if terminal {
		// completed means reconciliation observed a terminal result. It does not
		// claim that cancellation caused that result.
		entry.Metadata["state"] = string(entry.State)
		entry.Metadata["running"] = entry.Running
		normalizeSubagentEntryResult(entry, result.Error)
	} else {
		entry.State = taskapi.StateUnknownOutcome
		entry.Running = true
		entry.SupportsInput = false
		entry.Metadata["state"] = string(taskapi.StateUnknownOutcome)
		entry.Metadata["running"] = true
		normalizeSubagentEntryResult(entry, reason)
	}
	if err := tm.persistSpawnEntry(ctx, entry); err != nil {
		return false, err
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
		task.metadata["state"] = string(task.state)
		task.metadata["running"] = task.running
	} else {
		task.state = taskapi.StateUnknownOutcome
		task.running = true
		normalizeSubagentResultForState(&task.result, taskapi.StateUnknownOutcome, reason)
		task.metadata["state"] = string(taskapi.StateUnknownOutcome)
		task.metadata["running"] = true
	}
	task.metadata[subagentCancelPhaseKey] = string(phase)
	task.metadata[subagentCancelTurnSeqKey] = cancelTurnSeq
	task.mu.Unlock()
	return true, nil
}
