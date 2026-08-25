package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

// waitCommandTask observes the sandbox lifecycle without retaining the Task
// mutation claim. Once the sandbox wait finishes, it acquires that claim only
// for authoritative status reconciliation, so concurrent cancel/read/write
// operations remain available during the bounded wait.
func (tm *taskRuntime) waitCommandTask(
	ctx context.Context,
	ref session.SessionRef,
	task *commandTask,
	req taskapi.ControlRequest,
) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: task is required")
	}
	if task.commandOutcomeUnattached() {
		return task.snapshotWithoutSession(tm.runtime.now()), nil
	}
	waitStarted := time.Now()
	status, waitErr := task.session.Wait(ctx, req.Yield)
	if ctxErr := ctx.Err(); ctxErr != nil {
		if waitErr != nil {
			return task.snapshotWithoutSession(tm.runtime.now()), waitErr
		}
		return task.snapshotWithoutSession(tm.runtime.now()), ctxErr
	}
	claimBudget := req.Yield - time.Since(waitStarted)
	if claimBudget < 0 {
		claimBudget = 0
	}
	claimCtx, cancelClaim := context.WithTimeout(ctx, claimBudget)
	defer cancelClaim()
	release, claimed := tm.tryClaimSubagentOperation(ref, task.ref.TaskID)
	var (
		durableFallback    taskapi.Snapshot
		durableFallbackOK  bool
		durableFallbackErr error
	)
	if !claimed && tm.store != nil {
		durableFallback, durableFallbackErr = tm.durableCommandSnapshot(claimCtx, ref, task.ref.TaskID)
		durableFallbackOK = durableFallbackErr == nil
	}
	var claimErr error
	if !claimed {
		release, claimErr = tm.waitForTaskOperationClaim(claimCtx, ref, task.ref.TaskID)
	}
	if claimErr != nil {
		fallback := task.snapshotWithoutSession(tm.runtime.now())
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fallback, errors.Join(waitErr, ctxErr)
		}
		if errors.Is(claimErr, context.DeadlineExceeded) {
			if durableFallbackOK {
				return durableFallback, waitErr
			}
			if tm.store != nil {
				return fallback, errors.Join(waitErr, claimErr, durableFallbackErr)
			}
			return fallback, waitErr
		}
		return fallback, errors.Join(waitErr, claimErr)
	}
	defer release()

	if tm.store != nil {
		current, lookupErr := tm.lookupCommandCanonical(ctx, ref, task.ref.TaskID)
		if lookupErr != nil {
			return task.snapshotWithoutSession(tm.runtime.now()), errors.Join(waitErr, lookupErr)
		}
		task = current
	} else {
		tm.mu.RLock()
		current := tm.tasks[task.ref.TaskID]
		tm.mu.RUnlock()
		if current != nil {
			task = current
		}
	}
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: command task is unavailable after wait")
	}
	tm.mu.RLock()
	current := tm.tasks[task.ref.TaskID]
	tm.mu.RUnlock()
	if current == nil && tm.store == nil {
		settledStatus, statusErr := task.session.Status(context.WithoutCancel(ctx))
		if statusErr == nil {
			status = settledStatus
		}
		task.mu.Lock()
		snapshot := task.snapshotLocked(status)
		task.mu.Unlock()
		return snapshot, nil
	}
	if current != nil {
		task = current
	}
	if currentStatus, statusErr := task.session.Status(ctx); statusErr == nil {
		status = currentStatus
	}
	if _, completed := task.session.(completedTaskSession); completed {
		task.mu.Lock()
		snapshot := task.snapshotLocked(status)
		task.mu.Unlock()
		tm.removeCommandTask(task)
		return snapshot, nil
	}
	if waitErr != nil {
		return tm.reconcileCommandWaitError(ctx, task, waitErr)
	}
	return tm.reconcileCommandStatus(ctx, task, status)
}

func (tm *taskRuntime) durableCommandSnapshot(
	ctx context.Context,
	ref session.SessionRef,
	taskID string,
) (taskapi.Snapshot, error) {
	if tm == nil || tm.store == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: durable Task store is unavailable")
	}
	entry, err := tm.store.Get(ctx, taskID)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	if !storedTaskEntryMatches(entry, ref, taskapi.KindCommand) {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	return commandSnapshotFromTaskEntry(entry), nil
}

// observeCommandWriteOutput gives an interactive Task write a bounded chance
// to return the resulting command output. Output activity deliberately ends
// this write observation; Task wait uses the completion-oriented lifecycle path.
func (tm *taskRuntime) observeCommandWriteOutput(
	ctx context.Context,
	task *commandTask,
	baseline stream.Cursor,
	wait time.Duration,
) error {
	if tm == nil || task == nil || wait <= 0 {
		return nil
	}
	service := newStreamService(tm)
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	snapshot, err := service.await(waitCtx, stream.ReadRequest{
		Ref:    commandTaskStreamRef(task),
		Cursor: baseline,
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}
	if !snapshot.Running || taskWriteOutputQuietPeriod <= 0 {
		return nil
	}

	cursor := stream.CloneCursor(snapshot.Cursor)
	for {
		quietCtx, quietCancel := context.WithTimeout(waitCtx, taskWriteOutputQuietPeriod)
		next, awaitErr := service.await(quietCtx, stream.ReadRequest{
			Ref:    commandTaskStreamRef(task),
			Cursor: cursor,
		})
		quietCancel()
		if awaitErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(awaitErr, context.DeadlineExceeded) {
				return nil
			}
			return awaitErr
		}
		if !next.Running {
			return nil
		}
		cursor = stream.CloneCursor(next.Cursor)
	}
}

func (tm *taskRuntime) snapshotObservedCommand(ctx context.Context, task *commandTask) (taskapi.Snapshot, error) {
	status, err := task.session.Status(ctx)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	return tm.reconcileCommandStatus(ctx, task, status)
}

func (tm *taskRuntime) syncCommandStream(ctx context.Context, task *commandTask) (stream.Cursor, bool, error) {
	cursor, _ := commandTaskStreamCursor(task)
	if _, err := newStreamService(tm).Read(ctx, stream.ReadRequest{
		Ref:    commandTaskStreamRef(task),
		Cursor: cursor,
	}); err != nil {
		return stream.Cursor{}, false, err
	}
	cursor, unread := commandTaskStreamCursor(task)
	return cursor, unread, nil
}

// reconcileCommandStatus dispatches a sandbox status to one of the two
// lifecycle owners. It intentionally contains no observation or finalization
// behavior of its own.
func (tm *taskRuntime) reconcileCommandStatus(
	ctx context.Context,
	task *commandTask,
	status sandbox.SessionStatus,
) (taskapi.Snapshot, error) {
	if status.Running {
		return tm.observeRunningCommand(ctx, task, status)
	}
	return tm.finalizeTerminalCommand(ctx, task, status)
}

// syncCommandOutput advances the sole recovery ingest path. Callback-backed
// commands already committed output atomically and therefore never call
// ReadOutput as a concurrent second writer.
func (tm *taskRuntime) syncCommandOutput(
	ctx context.Context,
	task *commandTask,
	status sandbox.SessionStatus,
) error {
	if task == nil || task.session == nil {
		return fmt.Errorf("agent-sdk/runtime: command task has no observable sandbox session")
	}
	task.outputReadMu.Lock()
	defer task.outputReadMu.Unlock()

	task.mu.Lock()
	backend := task.outputState.backend
	callback := task.outputState.callback
	task.mu.Unlock()
	if callback {
		return nil
	}

	stdout, stderr, nextStdout, nextStderr, err := task.session.ReadOutput(
		ctx,
		backend.stdout,
		backend.stderr,
	)
	if err != nil {
		return err
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.ingestRecoveredOutputLocked(
		stdout,
		stderr,
		backend.stdout,
		backend.stderr,
		nextStdout,
		nextStderr,
		!status.Running,
	)
}

func (tm *taskRuntime) observeRunningCommand(
	ctx context.Context,
	task *commandTask,
	status sandbox.SessionStatus,
) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: task is required")
	}
	if !status.Running {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: running command observation requires a running status")
	}
	if err := tm.syncCommandOutput(ctx, task, status); err != nil {
		snapshot, persistErr := tm.markCommandUnknown(context.WithoutCancel(ctx), task, err)
		return snapshot, errors.Join(err, persistErr)
	}

	task.mu.Lock()
	phase := taskStringValue(task.metadata["command_phase"])
	state := stateFromStatus(status)
	if commandUnknownWhileRunningPhase(phase) {
		state = taskapi.StateUnknownOutcome
	}
	task.state = state
	task.running = true
	task.metadata = map[string]any{
		"task_id":     task.ref.TaskID,
		"task_kind":   string(taskapi.KindCommand),
		"state":       string(state),
		"running":     true,
		"session_id":  task.ref.SessionID,
		"terminal_id": task.ref.TerminalID,
	}
	task.retainParentRelationLocked()
	if status.Terminal.TerminalID != "" {
		task.metadata["terminal_id"] = status.Terminal.TerminalID
	}
	if phase != "" {
		task.metadata["command_phase"] = phase
	}
	outputStartCursor := max(task.outputState.frontier.model, task.outputState.frontier.base)
	outputDelta := task.outputFromCursorLocked(outputStartCursor)
	latestOutput := compactLatestOutput(outputDelta)
	outputCursor := task.outputCursorLocked()
	task.outputState.frontier.model = outputCursor
	task.commitOutputResumeCheckpointLocked()
	task.metadata["output_cursor"] = outputCursor
	task.metadata["model_output_cursor"] = task.outputState.frontier.model
	task.metadata["output_checkpoint_available"] = task.outputState.checkpoint.available
	task.metadata["output_checkpoint_coherent"] = task.outputState.checkpoint.coherent
	task.metadata["output_recovery_gap"] = task.outputState.checkpoint.gap
	task.result = map[string]any{
		"task_id": task.ref.TaskID,
		"state":   string(state),
	}
	if state == taskapi.StateUnknownOutcome {
		task.result["error"] = "command effect outcome is not yet confirmed"
	}
	if taskOutputHasNonBlankLine(latestOutput) {
		task.result["latest_output"] = latestOutput
	}
	observationDelta := ""
	if task.outputState.exact {
		observationDelta = outputDelta
	}
	snapshot := commandObservationSnapshot(task.snapshotLocked(status), outputStartCursor, observationDelta)
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}
