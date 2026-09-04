package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
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
		return taskapi.Snapshot{}, fmt.Errorf("task is required")
	}
	waitStarted := time.Now()
	commandSession := task.observableCommandSession()
	commandYield := req.Yield
	if commandSession == nil {
		var fallback taskapi.Snapshot
		var err error
		task, commandSession, fallback, err = tm.commandWaitTarget(ctx, ref, task, commandWaitBudget(req.Yield, waitStarted))
		if err != nil {
			return fallback, err
		}
		if commandSession == nil {
			return fallback, nil
		}
		commandYield = commandWaitBudget(req.Yield, waitStarted)
	}
	status, waitErr := commandSession.Wait(ctx, commandYield)
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
		return taskapi.Snapshot{}, fmt.Errorf("command Task is unavailable after wait")
	}
	tm.mu.RLock()
	current := tm.tasks[task.ref.TaskID]
	tm.mu.RUnlock()
	if current == nil && tm.store == nil {
		settledStatus, statusErr := commandSession.Status(context.WithoutCancel(ctx))
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
	commandSession = task.observableCommandSession()
	if commandSession == nil {
		fallback := task.snapshotWithoutSession(tm.runtime.now())
		return fallback, waitErr
	}
	if currentStatus, statusErr := commandSession.Status(ctx); statusErr == nil {
		status = currentStatus
	}
	if _, completed := commandSession.(completedTaskSession); completed {
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

// commandWaitTarget waits only for an in-flight command operation to publish
// its sandbox Session. It releases the short operation claim before the
// lifecycle wait so cancellation and other command controls remain available.
// A durable intent without a live producer is an observable prepared state;
// Task wait must not resume the command or create a sandbox side effect.
func (tm *taskRuntime) commandWaitTarget(
	ctx context.Context,
	ref session.SessionRef,
	task *commandTask,
	yield time.Duration,
) (*commandTask, sandbox.Session, taskapi.Snapshot, error) {
	if task == nil {
		return nil, nil, taskapi.Snapshot{}, fmt.Errorf("task is required")
	}
	if commandSession := task.observableCommandSession(); commandSession != nil {
		return task, commandSession, taskapi.Snapshot{}, nil
	}

	taskID := task.ref.TaskID
	fallback := task.snapshotWithoutSession(tm.runtime.now())
	claimCtx, cancelClaim := context.WithTimeout(ctx, max(yield, 0))
	defer cancelClaim()
	release, claimed := tm.tryClaimSubagentOperation(ref, taskID)
	var (
		durableFallback    taskapi.Snapshot
		durableFallbackOK  bool
		durableFallbackErr error
	)
	if !claimed && tm.store != nil {
		durableFallback, durableFallbackErr = tm.durableCommandSnapshot(claimCtx, ref, taskID)
		durableFallbackOK = durableFallbackErr == nil
	}
	var claimErr error
	if !claimed {
		release, claimErr = tm.waitForTaskOperationClaim(claimCtx, ref, taskID)
	}
	if claimErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return task, nil, fallback, ctxErr
		}
		if errors.Is(claimErr, context.DeadlineExceeded) {
			if durableFallbackOK {
				return task, nil, durableFallback, nil
			}
			if tm.store != nil {
				return task, nil, fallback, errors.Join(claimErr, durableFallbackErr)
			}
			return task, nil, fallback, nil
		}
		return task, nil, fallback, claimErr
	}
	defer release()

	if tm.store != nil {
		current, lookupErr := tm.lookupCommandCanonical(ctx, ref, taskID)
		if lookupErr != nil {
			return task, nil, fallback, lookupErr
		}
		task = current
	} else {
		tm.mu.RLock()
		current := tm.tasks[taskID]
		tm.mu.RUnlock()
		if current != nil {
			task = current
		}
	}
	if task == nil {
		return nil, nil, taskapi.Snapshot{}, fmt.Errorf("command Task is unavailable before wait")
	}
	fallback = task.snapshotWithoutSession(tm.runtime.now())
	return task, task.observableCommandSession(), fallback, nil
}

func commandWaitBudget(yield time.Duration, started time.Time) time.Duration {
	remaining := yield - time.Since(started)
	return max(remaining, 0)
}

func (tm *taskRuntime) durableCommandSnapshot(
	ctx context.Context,
	ref session.SessionRef,
	taskID string,
) (taskapi.Snapshot, error) {
	if tm == nil || tm.store == nil {
		return taskapi.Snapshot{}, fmt.Errorf("durable Task store is unavailable")
	}
	entry, err := tm.store.Get(ctx, taskID)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	if !storedTaskEntryMatches(entry, ref, taskapi.KindCommand) {
		return taskapi.Snapshot{}, fmt.Errorf("task %q was not found", taskID)
	}
	return commandSnapshotFromTaskEntry(entry), nil
}

// observeCommandWriteOutput gives an interactive Task write a bounded chance
// to return the resulting command output. Output activity deliberately ends
// this write observation; Task wait uses the completion-oriented lifecycle path.
func (tm *taskRuntime) observeCommandWriteOutput(
	ctx context.Context,
	task *commandTask,
	baseline int64,
	wait time.Duration,
) error {
	if tm == nil || task == nil || wait <= 0 {
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	cursor, running, err := tm.awaitCommandOutputAdvance(waitCtx, task, baseline)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}
	if !running || taskWriteOutputQuietPeriod <= 0 {
		return nil
	}
	for {
		quietCtx, quietCancel := context.WithTimeout(waitCtx, taskWriteOutputQuietPeriod)
		next, nextRunning, awaitErr := tm.awaitCommandOutputAdvance(quietCtx, task, cursor)
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
		if !nextRunning {
			return nil
		}
		cursor = next
	}
}

func (tm *taskRuntime) awaitCommandOutputAdvance(ctx context.Context, task *commandTask, cursor int64) (int64, bool, error) {
	if task == nil || task.session == nil {
		return cursor, false, fmt.Errorf("command Task has no observable sandbox Session")
	}
	task.mu.Lock()
	if task.outputCursorLocked() > cursor || !task.running {
		next, running := task.outputCursorLocked(), task.running
		task.mu.Unlock()
		return next, running, nil
	}
	wake, _ := task.commandOutputChangeWaiterLocked(cursor)
	backend := task.outputState.backend.outputCursor()
	callback := task.outputState.callback
	commandSession := task.session
	task.mu.Unlock()

	type backendResult struct {
		observation sandbox.OutputObservation
		err         error
	}
	backendCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan backendResult, 1)
	go func() {
		observation, err := commandSession.AwaitOutput(backendCtx, backend)
		results <- backendResult{observation: observation, err: err}
	}()

	select {
	case <-ctx.Done():
		return cursor, true, ctx.Err()
	case <-wake:
	case result := <-results:
		if result.err != nil {
			return cursor, true, result.err
		}
		if !callback {
			if err := tm.syncCommandOutput(ctx, task, result.observation.Status); err != nil {
				return cursor, result.observation.Status.Running, err
			}
		}
	}
	task.mu.Lock()
	next, running := task.outputCursorLocked(), task.running
	task.mu.Unlock()
	return next, running, nil
}

func (tm *taskRuntime) snapshotObservedCommand(ctx context.Context, task *commandTask) (taskapi.Snapshot, error) {
	status, err := task.session.Status(ctx)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	return tm.reconcileCommandStatus(ctx, task, status)
}

func (tm *taskRuntime) syncCommandStream(ctx context.Context, task *commandTask) (int64, bool, error) {
	if task == nil || task.session == nil {
		return 0, false, fmt.Errorf("command Task has no observable sandbox Session")
	}
	status, err := task.session.Status(ctx)
	if err != nil {
		return 0, false, err
	}
	if err := tm.syncCommandOutput(ctx, task, status); err != nil {
		return 0, false, err
	}
	task.mu.Lock()
	cursor := task.outputCursorLocked()
	unread := task.outputState.frontier.model < cursor
	task.mu.Unlock()
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
		return fmt.Errorf("command Task has no observable sandbox session")
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
		return taskapi.Snapshot{}, fmt.Errorf("task is required")
	}
	if !status.Running {
		return taskapi.Snapshot{}, fmt.Errorf("running command observation requires a running status")
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
