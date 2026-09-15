package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type taskStartPermitKey struct{}

// The permit is process-local execution authority, never reconstructed from
// tool metadata, the durable Task association, or an approval receipt.
type taskStartPermit struct {
	taskID string
	work   *taskContinuation
}

func (t runtimeCommandTool) submitApproval(ctx context.Context, call tool.Call, approval taskApproval, execute approvedToolCall) (tool.Result, error) {
	runtime, ok := sandboxRuntimeFromTool(t.base)
	if !ok || runtime == nil {
		return tool.Result{}, errors.New("command sandbox is unavailable")
	}
	req, err := t.commandRequest(call, runtime)
	if err != nil {
		return tool.Result{}, err
	}
	if approval.started.IsZero() {
		approval.started = time.Now()
	}
	deadline := approval.started.Add(req.Yield)
	taskID, err := commandTaskID(t.sessionRef, call.ID)
	if err != nil {
		return tool.Result{}, err
	}
	digest, err := commandRequestDigest(req)
	if err != nil {
		return tool.Result{}, err
	}
	if t.tasks.store != nil {
		if _, ok := t.tasks.store.(taskapi.CASStore); !ok {
			return tool.Result{}, errors.New("durable command approval requires task.CASStore")
		}
	}
	release, err := t.tasks.waitForTaskOperationClaim(ctx, t.sessionRef, taskID)
	if err != nil {
		return tool.Result{}, err
	}
	execution := commandExecutionForRequest(req)
	execution.Approval = &commandApprovalRef{RunID: approval.request.RunID, TurnID: approval.request.TurnID, TokenID: "command-approval-" + taskID}
	task, created, err := t.tasks.prepareCommandClaimed(ctx, t.session, t.sessionRef, taskID, digest, req, execution)
	if err != nil {
		release()
		if task != nil {
			return taskSnapshotToolResult(call, t.Definition(), task.snapshotWithoutSession(time.Now())), err
		}
		return tool.Result{}, err
	}
	if !created {
		release()
		return t.observeSubmittedCommand(ctx, call, task, deadline)
	}
	scope := taskScopeFromContext(approval.owner)
	work, err := scope.register(approval.owner, taskID, approval.generation)
	if err != nil {
		state, code := taskapi.StateFailed, "task_capacity"
		if errors.Is(err, errTaskAuthorizationChanged) || approval.owner.Err() != nil {
			state, code = taskapi.StateCancelled, "approval_cancelled"
		}
		persistErr := t.tasks.finishUnstartedCommandClaimed(ctx, task, state, code, err.Error())
		release()
		return taskSnapshotToolResult(call, t.Definition(), task.snapshotWithoutSession(time.Now())), persistErr
	}
	task.mu.Lock()
	task.continuation = work
	task.mu.Unlock()
	t.tasks.bindCommandOutput(approval.owner, task, true)
	task.emitApprovalState()
	release()
	if req.Observer != nil {
		req.Observer.ObserveTaskSnapshot(task.snapshotWithoutSession(time.Now()))
	}
	// Only the Task output binding survives the invocation. A returned call's
	// progress observer must not receive the continuation's final tool result.
	call = tool.CloneCall(call)
	call.Observer = nil
	go func() {
		err := t.runApprovalContinuation(task, work, call, approval, execute)
		scope.complete(taskID, work, err)
	}()
	select {
	case <-work.admitted:
	case <-work.done:
	case <-ctx.Done():
		return tool.Result{}, ctx.Err()
	}
	return t.observeSubmittedCommand(ctx, call, task, deadline)
}

func (t runtimeCommandTool) observeSubmittedCommand(ctx context.Context, call tool.Call, task *commandTask, deadline time.Time) (tool.Result, error) {
	snapshot, err := t.tasks.waitCommandTask(ctx, t.sessionRef, task, taskapi.ControlRequest{Yield: max(time.Until(deadline), 0)})
	return taskSnapshotToolResult(call, t.Definition(), snapshot), err
}

func (t runtimeCommandTool) runApprovalContinuation(task *commandTask, work *taskContinuation, call tool.Call, approval taskApproval, execute approvedToolCall) error {
	request := approval.request
	request.OperationTaskID = task.ref.TaskID
	task.mu.Lock()
	request.PauseTokenID = task.execution.Approval.TokenID
	task.mu.Unlock()
	var admissionErr error
	request.OnAdmission = func(admissionCtx context.Context) error {
		work.admit.Do(func() {
			admissionErr = t.tasks.recordCommandApprovalAdmission(work.ctx, task, admissionCtx)
			if admissionErr == nil {
				close(work.admitted)
			}
		})
		return admissionErr
	}
	response, err := approval.resolve(work.ctx, request)
	var executionFailure error
	state, code, reason := taskapi.StateFailed, "approval_unavailable", ""
	switch {
	case context.Cause(work.ctx) != nil:
		state, code, reason = taskapi.StateCancelled, "approval_cancelled", context.Cause(work.ctx).Error()
	case err != nil:
		reason = err.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			code = "approval_expired"
		}
	case !response.Approved:
		code, reason = "approval_denied", firstNonEmpty(strings.TrimSpace(response.Reason), "approval was denied")
	default:
		permitCtx := context.WithValue(work.ctx, taskStartPermitKey{}, taskStartPermit{taskID: task.ref.TaskID, work: work})
		task.mu.Lock()
		deadline := task.execution.Approval.Deadline
		task.mu.Unlock()
		if !deadline.IsZero() {
			var cancel context.CancelFunc
			permitCtx, cancel = context.WithDeadline(permitCtx, deadline)
			defer cancel()
		}
		result, executeErr := execute(permitCtx, call)
		if executeErr == nil && !result.IsError {
			return nil
		}
		// A failed journal/claim before Start still has a durable pending intent.
		// Once claimed, the existing command owner retains its actual outcome.
		executionFailure = executeErr
		reason, code = firstNonEmpty(errorText(executeErr), "command could not start"), "command_start_failed"
		if context.Cause(work.ctx) != nil {
			state, code, reason = taskapi.StateCancelled, "approval_cancelled", context.Cause(work.ctx).Error()
			//nolint:errorlint // A joined persistence failure must still fail the Run.
			if executeErr == context.Canceled || executeErr == context.DeadlineExceeded {
				executionFailure = nil
			}
		} else {
			task.mu.Lock()
			deadline := task.execution.Approval.Deadline
			task.mu.Unlock()
			if !deadline.IsZero() && !time.Now().Before(deadline) {
				code, reason = "approval_expired", "approval expired before command execution"
			}
		}
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(work.ctx), 5*time.Second)
	defer cancel()
	release, claimErr := t.tasks.waitForTaskOperationClaim(cleanup, t.sessionRef, task.ref.TaskID)
	if claimErr != nil {
		return claimErr
	}
	defer release()
	return errors.Join(executionFailure, t.tasks.finishUnstartedCommandClaimed(cleanup, task, state, code, reason))
}

// Waiting for approval or process startup observes the Task's state signal,
// without retaining the mutation claim needed by cancel and the producer.
func (t *commandTask) waitForCommandProducer(ctx context.Context, budget time.Duration) bool {
	waitCtx, cancel := context.WithTimeout(ctx, max(budget, 0))
	defer cancel()
	for {
		t.mu.Lock()
		work := t.continuation
		ready := t.session != nil || taskapi.IsTerminalState(t.state) || work == nil
		if t.outputChanged == nil {
			t.outputChanged = make(chan struct{})
		}
		changed := t.outputChanged
		t.mu.Unlock()
		if ready {
			return true
		}
		select {
		case <-changed:
		case <-work.done:
			return true
		case <-waitCtx.Done():
			return false
		}
	}
}

func (tm *taskRuntime) recordCommandApprovalAdmission(ctx context.Context, task *commandTask, admissionCtx context.Context) error {
	release, err := tm.waitForTaskOperationClaim(ctx, task.sessionRef, task.ref.TaskID)
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	task.mu.Lock()
	entry := task.entrySnapshot(tm.runtime.now())
	spec := task.execution
	if spec.Approval == nil || task.state != taskapi.StateWaitingApproval {
		task.mu.Unlock()
		return errors.New("command approval is no longer pending")
	}
	association := *spec.Approval
	task.mu.Unlock()
	association.Deadline, _ = admissionCtx.Deadline()
	spec.Approval = &association
	entry.Spec["execution"] = spec.value()
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return err
	}
	applyCommandEntry(task, entry)
	return nil
}

func (tm *taskRuntime) finishUnstartedCommandClaimed(ctx context.Context, task *commandTask, state taskapi.State, code, reason string) error {
	task.mu.Lock()
	if taskStringValue(task.metadata["command_phase"]) != commandPhaseWaitingApproval || taskapi.IsTerminalState(task.state) {
		task.mu.Unlock()
		return nil
	}
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	entry.State, entry.Running, entry.SupportsInput = state, false, false
	entry.Metadata["state"], entry.Metadata["running"] = string(state), false
	entry.Result = map[string]any{"state": string(state), "error": reason, "error_code": code}
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return fmt.Errorf("settle unstarted command: %w", err)
	}
	applyCommandEntry(task, entry)
	task.mu.Lock()
	task.emitOutputTerminalLocked(state, sandbox.SessionStatus{UpdatedAt: tm.runtime.now()}, false)
	task.mu.Unlock()
	return nil
}

func (t *commandTask) emitApprovalState() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.outputObserver != nil {
		_ = t.outputObserver.ObserveTaskOutput(context.Background(), output.Event{State: string(t.state), Running: true, OccurredAt: time.Now()})
	}
}
