package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/commanddiag"
)

const (
	commandPhaseIntent        = "command_intent"
	commandPhaseEffectClaimed = "command_effect_claimed"
	commandPhaseRunning       = "command_running"
	commandPhaseStartFailed   = "command_start_failed"
	commandPhaseUnknown       = "command_unknown_outcome"
	commandPhaseCancelClaimed = "command_cancel_claimed"
	commandPhaseCancelUnknown = "command_cancel_unknown_outcome"
	commandPhaseCancelApplied = "command_cancel_effect_applied"
)

func commandTaskID(ref session.SessionRef, parentCall string) (string, error) {
	parentCall = strings.TrimSpace(parentCall)
	if parentCall == "" {
		return randomTaskID()
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(ref.SessionID) + "\x00" + parentCall))
	return hex.EncodeToString(sum[:taskIDRandomBytes]), nil
}

func commandRequestDigest(req taskapi.CommandStartRequest) (string, error) {
	constraints, _ := req.Constraints.(sandbox.Constraints)
	payload := struct {
		Command     string              `json:"command"`
		Workdir     string              `json:"workdir"`
		Timeout     time.Duration       `json:"timeout"`
		ParentCall  string              `json:"parent_call"`
		Constraints sandbox.Constraints `json:"constraints"`
	}{strings.TrimSpace(req.Command), strings.TrimSpace(req.Workdir), req.Timeout, strings.TrimSpace(req.ParentCall), constraints}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (tm *taskRuntime) StartCommand(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	runtime sandbox.Runtime,
	req taskapi.CommandStartRequest,
) (taskapi.Snapshot, error) {
	var (
		outputTask    *commandTask
		pendingOutput strings.Builder
		pendingMu     sync.Mutex
	)
	bindOutputTask := func(task *commandTask) {
		pendingMu.Lock()
		outputTask = task
		buffered := pendingOutput.String()
		pendingOutput.Reset()
		pendingMu.Unlock()
		if task != nil && buffered != "" {
			task.appendOutput(buffered)
		}
	}
	sandboxReq := sandbox.CommandRequest{
		Command: strings.TrimSpace(req.Command),
		Dir:     strings.TrimSpace(req.Workdir),
		Timeout: req.Timeout,
		OnOutput: func(chunk sandbox.OutputChunk) {
			if chunk.Text == "" {
				return
			}
			pendingMu.Lock()
			current := outputTask
			if current == nil {
				pendingOutput.WriteString(chunk.Text)
				pendingMu.Unlock()
				return
			}
			pendingMu.Unlock()
			current.appendOutput(chunk.Text)
		},
	}
	if constraints, ok := req.Constraints.(sandbox.Constraints); ok {
		sandboxReq.Constraints = constraints
		sandboxReq.RouteHint = constraints.Route
		sandboxReq.Backend = constraints.Backend
		sandboxReq.Permission = constraints.Permission
	}
	taskID, err := commandTaskID(ref, req.ParentCall)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	requestDigest, err := commandRequestDigest(req)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	if tm.store != nil {
		if _, ok := tm.store.(taskapi.CASStore); !ok {
			return taskapi.Snapshot{}, errors.New("agent-sdk/runtime: durable command start requires task.CASStore")
		}
	}
	release, claimed := tm.tryClaimSubagentOperation(ref, taskID)
	if !claimed {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: command %q already has an operation in progress", taskID)
	}
	defer release()
	if existing, ok, existingErr := tm.existingCommandForStart(ctx, ref, taskID, requestDigest); existingErr != nil {
		return taskapi.Snapshot{}, existingErr
	} else if ok {
		bindOutputTask(existing)
		return tm.resumeCommandStart(ctx, existing, runtime, sandboxReq, req)
	}
	now := tm.runtime.now()
	createdTask := &commandTask{
		ref: taskapi.Ref{
			TaskID: taskID,
		},
		sessionRef:     session.NormalizeSessionRef(ref),
		command:        strings.TrimSpace(req.Command),
		workdir:        strings.TrimSpace(req.Workdir),
		parentCall:     strings.TrimSpace(req.ParentCall),
		requestDigest:  requestDigest,
		title:          shell.RunCommandToolName + " " + strings.TrimSpace(req.Command),
		createdAt:      now,
		state:          taskapi.StatePrepared,
		running:        false,
		outputCallback: true,
		metadata: map[string]any{
			"task_id": taskID, "task_kind": string(taskapi.KindCommand),
			"state": string(taskapi.StatePrepared), "running": false,
			"command_phase":          commandPhaseIntent,
			"command_request_digest": requestDigest,
			"parent_call":            strings.TrimSpace(req.ParentCall),
		},
		result: map[string]any{"state": string(taskapi.StatePrepared)},
	}
	bindOutputTask(createdTask)
	intent := createdTask.entrySnapshot(now)
	if err := tm.persistTaskEntry(ctx, intent); err != nil {
		return createdTask.snapshotWithoutSession(tm.runtime.now()), err
	}
	applyCommandEntry(createdTask, intent)
	tm.installCommandTask(createdTask)
	return tm.resumeCommandStart(ctx, createdTask, runtime, sandboxReq, req)
}

// resumeCommandStart advances only phases whose external effect has not yet
// been claimed. Once command_effect_claimed is durable, retries must reconcile
// the existing outcome rather than call sandbox.Start again.
func (tm *taskRuntime) resumeCommandStart(
	ctx context.Context,
	task *commandTask,
	runtime sandbox.Runtime,
	sandboxReq sandbox.CommandRequest,
	req taskapi.CommandStartRequest,
) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, errors.New("agent-sdk/runtime: command task is required")
	}
	task.mu.Lock()
	phase := taskStringValue(task.metadata["command_phase"])
	running := task.running
	claim := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if phase != commandPhaseIntent || running {
		return tm.snapshotExistingCommand(ctx, task, req.Yield)
	}
	claim.State = taskapi.StateUnknownOutcome
	claim.Running = true
	claim.Metadata = session.CloneState(claim.Metadata)
	claim.Metadata["state"] = string(taskapi.StateUnknownOutcome)
	claim.Metadata["running"] = true
	claim.Metadata["command_phase"] = commandPhaseEffectClaimed
	claim.Result = map[string]any{
		"state": string(taskapi.StateUnknownOutcome),
		"error": "command start was claimed; external effect outcome is not yet known",
	}
	if err := tm.persistTaskEntry(ctx, claim); err != nil {
		return task.snapshotWithoutSession(tm.runtime.now()), err
	}
	applyCommandEntry(task, claim)
	return tm.startClaimedCommand(ctx, task, runtime, sandboxReq, req)
}

func (tm *taskRuntime) startClaimedCommand(
	ctx context.Context,
	task *commandTask,
	runtime sandbox.Runtime,
	sandboxReq sandbox.CommandRequest,
	req taskapi.CommandStartRequest,
) (taskapi.Snapshot, error) {
	now := tm.runtime.now()
	sessionHandle, err := runtime.Start(ctx, sandboxReq)
	if err != nil {
		return tm.handleCommandStartFailure(ctx, task, sessionHandle, err)
	}
	if sessionHandle == nil {
		return tm.handleCommandStartFailure(ctx, task, nil, errors.New("agent-sdk/runtime: sandbox start returned no session"))
	}
	task.mu.Lock()
	task.session = sessionHandle
	task.ref.SessionID = strings.TrimSpace(sessionHandle.Ref().SessionID)
	task.ref.TerminalID = strings.TrimSpace(sessionHandle.Terminal().TerminalID)
	task.state = taskapi.StateRunning
	task.running = true
	task.metadata["state"] = string(taskapi.StateRunning)
	task.metadata["running"] = true
	task.metadata["session_id"] = task.ref.SessionID
	task.metadata["terminal_id"] = task.ref.TerminalID
	task.metadata["command_phase"] = commandPhaseRunning
	task.result = map[string]any{"state": string(taskapi.StateRunning), "task_id": task.ref.TaskID}
	task.mu.Unlock()
	tm.installCommandTask(task)
	task.mu.Lock()
	runningEntry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if err := tm.persistTaskEntry(ctx, runningEntry); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		terminateErr := sessionHandle.Terminate(cleanupCtx)
		cancel()
		if terminateErr != nil {
			return tm.retainCommandAfterFailedInitialPersistence(ctx, task, err, terminateErr)
		}
		return tm.finalizeCommandStartCleanup(ctx, task, err)
	}
	if req.Observer != nil {
		status, statusErr := sessionHandle.Status(ctx)
		if statusErr != nil {
			status = sandbox.SessionStatus{
				SessionRef:    sessionHandle.Ref(),
				Terminal:      sessionHandle.Terminal(),
				Running:       true,
				SupportsInput: true,
				UpdatedAt:     now,
			}
		}
		task.mu.Lock()
		snapshot := task.snapshotLocked(status)
		task.mu.Unlock()
		req.Observer.ObserveTaskSnapshot(snapshot)
	}
	snapshot, err := tm.waitCommand(ctx, task, req.Yield)
	return snapshot, err
}

func applyCommandEntry(task *commandTask, entry *taskapi.Entry) {
	if task == nil || entry == nil {
		return
	}
	task.mu.Lock()
	task.revision = entry.Revision
	task.lease = taskapi.CloneLease(entry.Lease)
	task.state = entry.State
	task.running = entry.Running
	task.result = session.CloneState(entry.Result)
	task.metadata = session.CloneState(entry.Metadata)
	if sessionID := strings.TrimSpace(entry.Terminal.SessionID); sessionID != "" {
		task.ref.SessionID = sessionID
	}
	if terminalID := strings.TrimSpace(entry.Terminal.TerminalID); terminalID != "" {
		task.ref.TerminalID = terminalID
	}
	task.mu.Unlock()
}

func (tm *taskRuntime) existingCommandForStart(ctx context.Context, ref session.SessionRef, taskID, requestDigest string) (*commandTask, bool, error) {
	if tm.store == nil {
		tm.mu.RLock()
		cached := tm.tasks[taskID]
		tm.mu.RUnlock()
		if cached == nil {
			return nil, false, nil
		}
		cached.mu.Lock()
		storedDigest := cached.requestDigest
		cached.mu.Unlock()
		if storedDigest != requestDigest {
			return nil, false, fmt.Errorf("agent-sdk/runtime: command identity %q was reused with a different request", taskID)
		}
		return cached, true, nil
	}
	entry, err := tm.store.Get(ctx, taskID)
	if err != nil {
		listed, listErr := tm.store.ListSession(ctx, ref)
		if listErr != nil {
			return nil, false, errors.Join(err, listErr)
		}
		for _, candidate := range listed {
			if strings.TrimSpace(candidate.TaskID) == taskID {
				entry = candidate
				break
			}
		}
		if entry == nil {
			return nil, false, nil
		}
	}
	if entry == nil {
		return nil, false, nil
	}
	if !storedTaskEntryMatches(entry, ref, taskapi.KindCommand) {
		return nil, false, fmt.Errorf("agent-sdk/runtime: command identity %q belongs to another session or task kind", taskID)
	}
	if storedDigest := taskSpecString(entry.Spec, "command_request_digest"); storedDigest != requestDigest {
		return nil, false, fmt.Errorf("agent-sdk/runtime: command identity %q was reused with a different request", taskID)
	}
	command, err := tm.commandFromDurableEntry(entry)
	if err != nil {
		return nil, false, err
	}
	tm.installCommandTask(command)
	return command, true, nil
}

func (tm *taskRuntime) snapshotExistingCommand(ctx context.Context, task *commandTask, yield time.Duration) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: command task is required")
	}
	if task.commandOutcomeUnattached() {
		return task.snapshotWithoutSession(tm.runtime.now()), nil
	}
	if task.session == nil {
		return task.snapshotWithoutSession(tm.runtime.now()), nil
	}
	task.mu.Lock()
	running := task.running
	task.mu.Unlock()
	if !running {
		status, err := task.session.Status(ctx)
		if err != nil {
			return task.snapshotWithoutSession(tm.runtime.now()), err
		}
		task.mu.Lock()
		snapshot := task.snapshotLocked(status)
		task.mu.Unlock()
		return snapshot, nil
	}
	return tm.waitCommand(ctx, task, yield)
}

func (tm *taskRuntime) handleCommandStartFailure(ctx context.Context, task *commandTask, handle sandbox.Session, startErr error) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, startErr
	}
	if handle == nil {
		task.mu.Lock()
		task.state = taskapi.StateFailed
		task.running = false
		task.metadata["state"] = string(taskapi.StateFailed)
		task.metadata["running"] = false
		task.metadata["command_phase"] = commandPhaseStartFailed
		task.result = map[string]any{"state": string(taskapi.StateFailed), "error": strings.TrimSpace(startErr.Error())}
		failed := task.entrySnapshot(tm.runtime.now())
		task.mu.Unlock()
		persistErr := tm.persistTaskEntry(context.WithoutCancel(ctx), failed)
		task.mu.Lock()
		task.revision = failed.Revision
		task.lease = taskapi.CloneLease(failed.Lease)
		task.mu.Unlock()
		return task.snapshotWithoutSession(tm.runtime.now()), errors.Join(startErr, persistErr)
	}
	task.mu.Lock()
	task.session = handle
	task.ref.SessionID = strings.TrimSpace(handle.Ref().SessionID)
	task.ref.TerminalID = strings.TrimSpace(handle.Terminal().TerminalID)
	task.metadata["session_id"] = task.ref.SessionID
	task.metadata["terminal_id"] = task.ref.TerminalID
	task.mu.Unlock()
	tm.installCommandTask(task)
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	terminateErr := handle.Terminate(cleanupCtx)
	cancel()
	if terminateErr != nil {
		return tm.retainCommandAfterFailedInitialPersistence(ctx, task, startErr, terminateErr)
	}
	snapshot, persistErr := tm.finalizeCommandStartCleanup(ctx, task, startErr)
	return snapshot, errors.Join(startErr, persistErr)
}

func (tm *taskRuntime) finalizeCommandStartCleanup(ctx context.Context, task *commandTask, cause error) (taskapi.Snapshot, error) {
	reason := "command start state could not be persisted; the sandbox session was terminated"
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		reason = strings.TrimSpace(cause.Error())
	}
	now := tm.runtime.now()
	task.mu.Lock()
	task.state = taskapi.StateFailed
	task.running = false
	task.metadata = map[string]any{
		"task_id": task.ref.TaskID, "task_kind": string(taskapi.KindCommand),
		"state": string(taskapi.StateFailed), "running": false,
		"session_id": task.ref.SessionID, "terminal_id": task.ref.TerminalID,
		"command_phase": commandPhaseStartFailed,
	}
	task.result = map[string]any{"state": string(taskapi.StateFailed), "error": reason}
	entry := task.entrySnapshot(now)
	task.mu.Unlock()
	persistErr := tm.persistTaskEntry(context.WithoutCancel(ctx), entry)
	task.mu.Lock()
	task.revision = entry.Revision
	task.lease = taskapi.CloneLease(entry.Lease)
	status := sandbox.SessionStatus{SessionRef: task.session.Ref(), Terminal: task.session.Terminal(), Running: false, UpdatedAt: now}
	snapshot := task.snapshotLocked(status)
	task.mu.Unlock()
	if persistErr == nil {
		tm.removeCommandTask(task)
	}
	return snapshot, errors.Join(cause, persistErr)
}

func (tm *taskRuntime) installCommandTask(task *commandTask) {
	if tm == nil || task == nil {
		return
	}
	tm.mu.Lock()
	tm.tasks[task.ref.TaskID] = task
	sessionID := strings.TrimSpace(task.sessionRef.SessionID)
	found := false
	for _, id := range tm.order[sessionID] {
		if id == task.ref.TaskID {
			found = true
			break
		}
	}
	if !found {
		tm.order[sessionID] = append(tm.order[sessionID], task.ref.TaskID)
	}
	tm.mu.Unlock()
}

func (tm *taskRuntime) removeCommandTask(task *commandTask) {
	if tm == nil || task == nil {
		return
	}
	tm.mu.Lock()
	if tm.tasks[task.ref.TaskID] == task {
		delete(tm.tasks, task.ref.TaskID)
	}
	tm.mu.Unlock()
}

func (tm *taskRuntime) retainCommandAfterFailedInitialPersistence(
	ctx context.Context,
	task *commandTask,
	persistErr error,
	terminateErr error,
) (taskapi.Snapshot, error) {
	now := tm.runtime.now()
	reason := "initial task persistence and sandbox termination both failed; command outcome is unknown"
	status, statusErr := task.session.Status(context.WithoutCancel(ctx))
	if statusErr != nil {
		status = sandbox.SessionStatus{
			SessionRef:    task.session.Ref(),
			Terminal:      task.session.Terminal(),
			Running:       true,
			SupportsInput: true,
			UpdatedAt:     now,
		}
	}
	status.Running = true
	if status.UpdatedAt.IsZero() {
		status.UpdatedAt = now
	}

	task.mu.Lock()
	task.state = taskapi.StateUnknownOutcome
	task.running = true
	task.metadata = map[string]any{
		"task_id":       task.ref.TaskID,
		"task_kind":     string(taskapi.KindCommand),
		"state":         string(taskapi.StateUnknownOutcome),
		"running":       true,
		"session_id":    task.ref.SessionID,
		"terminal_id":   task.ref.TerminalID,
		"command_phase": commandPhaseUnknown,
	}
	task.result = map[string]any{
		"state": string(taskapi.StateUnknownOutcome),
		"error": reason,
	}
	entry := task.entrySnapshot(now)
	task.mu.Unlock()
	recoveryPersistErr := tm.persistTaskEntry(context.WithoutCancel(ctx), entry)

	task.mu.Lock()
	snapshot := task.snapshotLocked(status)
	task.mu.Unlock()
	return snapshot, errors.Join(persistErr, terminateErr, statusErr, recoveryPersistErr)
}

func (tm *taskRuntime) waitCommand(ctx context.Context, task *commandTask, yield time.Duration) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: task is required")
	}
	if task.commandOutcomeUnattached() {
		return task.snapshotWithoutSession(tm.runtime.now()), nil
	}
	status, err := task.session.Wait(ctx, yield)
	if err != nil {
		return tm.reconcileCommandWaitError(ctx, task, err)
	}
	return tm.completeCommandTaskWithStatus(ctx, task, status)
}

func (tm *taskRuntime) reconcileCommandWaitError(ctx context.Context, task *commandTask, waitErr error) (taskapi.Snapshot, error) {
	fallback := task.snapshotWithoutSession(tm.runtime.now())
	if task == nil || task.session == nil || ctx.Err() != nil {
		return fallback, waitErr
	}
	status, statusErr := task.session.Status(context.WithoutCancel(ctx))
	if statusErr != nil {
		snapshot, persistErr := tm.markCommandUnknown(context.WithoutCancel(ctx), task, errors.Join(waitErr, statusErr))
		return snapshot, errors.Join(waitErr, statusErr, persistErr)
	}
	if status.Running {
		task.mu.Lock()
		snapshot := task.snapshotLocked(status)
		task.mu.Unlock()
		return snapshot, waitErr
	}
	snapshot, completeErr := tm.completeCommandTaskWithStatus(context.WithoutCancel(ctx), task, status)
	if completeErr != nil {
		return snapshot, errors.Join(waitErr, completeErr)
	}
	return snapshot, nil
}

func (tm *taskRuntime) markCommandUnknown(ctx context.Context, task *commandTask, cause error) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, errors.New("agent-sdk/runtime: task is required")
	}
	reason := strings.TrimSpace(errorText(cause))
	if reason == "" {
		reason = "command outcome could not be confirmed"
	}
	task.mu.Lock()
	phase := taskStringValue(task.metadata["command_phase"])
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	entry.State = taskapi.StateUnknownOutcome
	entry.Running = true
	entry.Metadata = session.CloneState(entry.Metadata)
	entry.Metadata["state"] = string(taskapi.StateUnknownOutcome)
	entry.Metadata["running"] = true
	if commandCancelPhase(phase) {
		entry.Metadata["command_phase"] = commandPhaseCancelUnknown
	} else {
		entry.Metadata["command_phase"] = commandPhaseUnknown
	}
	entry.Result = map[string]any{"state": string(taskapi.StateUnknownOutcome), "error": reason}
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return task.snapshotWithoutSession(tm.runtime.now()), err
	}
	applyCommandEntry(task, entry)
	task.mu.Lock()
	status := sandbox.SessionStatus{SessionRef: task.session.Ref(), Terminal: task.session.Terminal(), Running: true, SupportsInput: true, UpdatedAt: tm.runtime.now()}
	snapshot := task.snapshotLocked(status)
	task.mu.Unlock()
	return snapshot, nil
}

func (tm *taskRuntime) completeCommandTaskWithStatus(ctx context.Context, task *commandTask, status sandbox.SessionStatus) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: task is required")
	}
	stdout, stderr, nextStdout, nextStderr, err := task.session.ReadOutput(ctx, task.stdoutCursor, task.stderrCursor)
	if err != nil {
		snapshot, persistErr := tm.markCommandUnknown(context.WithoutCancel(ctx), task, err)
		return snapshot, errors.Join(err, persistErr)
	}

	task.mu.Lock()
	phase := taskStringValue(task.metadata["command_phase"])
	task.stdoutCursor = nextStdout
	task.stderrCursor = nextStderr
	if !task.outputCallback {
		task.appendOutputLocked(terminalDeltaText(string(stdout), string(stderr)))
	}
	outputText := task.output
	outputCursor := task.outputCursorLocked()
	state := stateFromStatus(status)
	if status.Running && commandUnknownWhileRunningPhase(phase) {
		state = taskapi.StateUnknownOutcome
	}
	task.state = state
	task.running = status.Running
	task.metadata = map[string]any{
		"task_id":     task.ref.TaskID,
		"task_kind":   string(taskapi.KindCommand),
		"state":       string(state),
		"running":     status.Running,
		"session_id":  task.ref.SessionID,
		"terminal_id": task.ref.TerminalID,
	}
	if status.Terminal.TerminalID != "" {
		task.metadata["terminal_id"] = status.Terminal.TerminalID
	}
	if status.Running {
		if phase != "" {
			task.metadata["command_phase"] = phase
		}
		latestOutput := compactLatestOutput(task.outputFromCursorLocked(task.modelCursor))
		task.modelCursor = outputCursor
		task.metadata["output_cursor"] = outputCursor
		task.metadata["model_output_cursor"] = task.modelCursor
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
		snapshot := task.snapshotLocked(status)
		entry := task.entrySnapshot(tm.runtime.now())
		task.mu.Unlock()
		if err := tm.persistTaskEntry(ctx, entry); err != nil {
			return snapshot, err
		}
		return snapshot, nil
	}

	result, resultErr := task.session.Result(ctx)
	stdoutText := result.Stdout
	stderrText := result.Stderr
	finalText := terminalFinalText(outputText, stdoutText, stderrText, resultErr)
	finalOutputText := terminalOutputText(outputText, stdoutText, stderrText)
	task.reconcileFinalOutputLocked(finalOutputText)
	task.metadata["output_cursor"] = int64(len([]byte(finalOutputText)))
	task.metadata["model_output_cursor"] = int64(len([]byte(finalOutputText)))
	task.result = map[string]any{
		"state": string(state),
	}
	if taskOutputHasNonBlankLine(finalText) && strings.TrimSpace(finalText) != noOutputPlaceholder {
		task.result["result"] = finalText
	}
	if commandExitCodeAvailable(state, result.ExitCode, resultErr) {
		task.result["exit_code"] = result.ExitCode
	}
	if detail, ok := sandbox.SandboxPermissionDetail(result, resultErr); ok {
		task.result["error"] = detail
		task.result["error_code"] = string(tool.ErrorCodeSandboxDenied)
	} else if resultErr != nil && strings.TrimSpace(finalText) == noOutputPlaceholder && !sandbox.IsCommandExit(resultErr) {
		task.result["error"] = strings.TrimSpace(resultErr.Error())
		if code, _ := tool.ErrorPayload(resultErr)["error_code"].(string); code != "" {
			task.result["error_code"] = code
		}
	}
	if diag, ok := commanddiag.Best(commanddiag.Input{
		ToolName: shell.RunCommandToolName,
		Command:  task.command,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		Error:    firstNonEmpty(strings.TrimSpace(result.Error), errorText(resultErr)),
		ExitCode: result.ExitCode,
		Route:    result.Route,
		Backend:  result.Backend,
	}); ok {
		if hint := strings.TrimSpace(diag.Hint); hint != "" {
			task.result["system_hint"] = hint
		}
	}
	snapshot := task.snapshotLocked(status)
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return snapshot, err
	}
	tm.removeCommandTask(task)
	return snapshot, nil
}

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

func (tm *taskRuntime) lookupCommand(ctx context.Context, ref session.SessionRef, taskID string) (*commandTask, error) {
	tm.mu.RLock()
	task, ok := tm.tasks[strings.TrimSpace(taskID)]
	tm.mu.RUnlock()
	if ok && task != nil {
		if strings.TrimSpace(task.sessionRef.SessionID) != strings.TrimSpace(ref.SessionID) {
			return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
		}
		return task, nil
	}
	if tm.store == nil {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	entry, err := tm.store.Get(ctx, strings.TrimSpace(taskID))
	if err != nil || entry == nil {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	if strings.TrimSpace(entry.Session.SessionID) != strings.TrimSpace(ref.SessionID) {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	if entry.Kind != taskapi.KindCommand {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	entry, err = tm.backfillCanonicalTaskEntry(ctx, ref, entry)
	if err != nil {
		return nil, err
	}
	rehydrated, err := tm.rehydrateCommandTask(entry)
	if err != nil {
		return nil, err
	}
	tm.mu.Lock()
	tm.tasks[rehydrated.ref.TaskID] = rehydrated
	tm.mu.Unlock()
	return rehydrated, nil
}

// lookupCommandCanonical reloads durable command state while the caller holds
// the session-scoped operation claim. A configured store failure is returned
// before any sandbox side effect; cached state is never used as a fallback.
func (tm *taskRuntime) lookupCommandCanonical(ctx context.Context, ref session.SessionRef, taskID string) (*commandTask, error) {
	if tm == nil || tm.store == nil {
		return tm.lookupCommand(ctx, ref, taskID)
	}
	ref = session.NormalizeSessionRef(ref)
	taskID = strings.TrimSpace(taskID)
	entry, err := tm.store.Get(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("agent-sdk/runtime: reload command task %q: %w", taskID, err)
	}
	if !storedTaskEntryMatches(entry, ref, taskapi.KindCommand) {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	entry, err = tm.backfillCanonicalTaskEntry(ctx, ref, entry)
	if err != nil {
		return nil, err
	}
	command, err := tm.commandFromDurableEntry(entry)
	if err != nil {
		return nil, err
	}
	tm.installCommandTask(command)
	return command, nil
}

func (tm *taskRuntime) commandFromDurableEntry(entry *taskapi.Entry) (*commandTask, error) {
	if tm == nil || entry == nil {
		return nil, fmt.Errorf("agent-sdk/runtime: task entry is required")
	}
	tm.mu.RLock()
	current := tm.tasks[strings.TrimSpace(entry.TaskID)]
	tm.mu.RUnlock()
	if current != nil && entry.Running {
		current.mu.Lock()
		if !commandCanAdoptDurableEntryLocked(current, entry) {
			current.mu.Unlock()
			return tm.rehydrateCommandTask(entry)
		}
		current.sessionRef = session.NormalizeSessionRef(entry.Session)
		current.command = taskSpecString(entry.Spec, "command")
		current.workdir = taskSpecString(entry.Spec, "workdir")
		current.parentCall = taskSpecString(entry.Spec, "parent_call")
		current.requestDigest = taskSpecString(entry.Spec, "command_request_digest")
		current.title = strings.TrimSpace(entry.Title)
		current.createdAt = entry.CreatedAt
		current.revision = entry.Revision
		current.lease = taskapi.CloneLease(entry.Lease)
		current.state = entry.State
		current.running = entry.Running
		current.stdoutCursor = entry.StdoutCursor
		current.stderrCursor = entry.StderrCursor
		current.result = session.CloneState(entry.Result)
		current.metadata = session.CloneState(entry.Metadata)
		current.mu.Unlock()
		return current, nil
	}
	return tm.rehydrateCommandTask(entry)
}

func commandCanAdoptDurableEntryLocked(current *commandTask, entry *taskapi.Entry) bool {
	if current == nil || current.session == nil || entry == nil {
		return false
	}
	if commandSessionMatchesEntry(current.session, entry) {
		return true
	}
	phase := taskStringValue(entry.Metadata["command_phase"])
	return strings.TrimSpace(entry.Terminal.SessionID) == "" &&
		(phase == commandPhaseEffectClaimed || phase == commandPhaseUnknown) &&
		current.requestDigest != "" && current.requestDigest == taskSpecString(entry.Spec, "command_request_digest")
}

func commandSessionMatchesEntry(handle sandbox.Session, entry *taskapi.Entry) bool {
	if handle == nil || entry == nil {
		return false
	}
	terminal := handle.Terminal()
	return strings.TrimSpace(terminal.SessionID) != "" &&
		strings.TrimSpace(terminal.SessionID) == strings.TrimSpace(entry.Terminal.SessionID) &&
		strings.TrimSpace(terminal.TerminalID) == strings.TrimSpace(entry.Terminal.TerminalID)
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
			return tm.completeCommandTaskWithStatus(context.WithoutCancel(ctx), task, status)
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
		return tm.completeCommandTaskWithStatus(ctx, task, status)
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
	entry.Running = true
	entry.Metadata = session.CloneState(entry.Metadata)
	entry.Metadata["state"] = string(taskapi.StateUnknownOutcome)
	entry.Metadata["running"] = true
	entry.Metadata["command_phase"] = phase
	entry.Result = map[string]any{"state": string(taskapi.StateUnknownOutcome), "error": strings.TrimSpace(reason)}
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return task.snapshotWithoutSession(tm.runtime.now()), err
	}
	applyCommandEntry(task, entry)
	return task.snapshotWithoutSession(tm.runtime.now()), nil
}

func (t *commandTask) snapshotLocked(status sandbox.SessionStatus) taskapi.Snapshot {
	return taskapi.CloneSnapshot(taskapi.Snapshot{
		Ref:            t.ref,
		Revision:       t.revision,
		Kind:           taskapi.KindCommand,
		Title:          t.title,
		State:          t.state,
		Running:        t.running,
		SupportsInput:  status.SupportsInput,
		SupportsCancel: true,
		CreatedAt:      t.createdAt,
		UpdatedAt:      status.UpdatedAt,
		Lease:          taskapi.CloneLease(t.lease),
		StdoutCursor:   t.stdoutCursor,
		StderrCursor:   t.stderrCursor,
		Result:         session.CloneState(t.result),
		Metadata:       session.CloneState(t.metadata),
		Terminal:       status.Terminal,
	})
}

func (tm *taskRuntime) rehydrateCommandTask(entry *taskapi.Entry) (*commandTask, error) {
	if entry == nil {
		return nil, fmt.Errorf("agent-sdk/runtime: task entry is required")
	}
	seededOutput, seededFromResult := rehydratedCommandOutput(entry)
	task := &commandTask{
		ref: taskapi.Ref{
			TaskID:     strings.TrimSpace(entry.TaskID),
			SessionID:  strings.TrimSpace(entry.Terminal.SessionID),
			TerminalID: strings.TrimSpace(entry.Terminal.TerminalID),
		},
		sessionRef:     session.NormalizeSessionRef(entry.Session),
		command:        taskSpecString(entry.Spec, "command"),
		workdir:        taskSpecString(entry.Spec, "workdir"),
		parentCall:     taskSpecString(entry.Spec, "parent_call"),
		requestDigest:  taskSpecString(entry.Spec, "command_request_digest"),
		title:          strings.TrimSpace(entry.Title),
		createdAt:      entry.CreatedAt,
		revision:       entry.Revision,
		lease:          taskapi.CloneLease(entry.Lease),
		state:          entry.State,
		running:        entry.Running,
		stdoutCursor:   entry.StdoutCursor,
		stderrCursor:   entry.StderrCursor,
		output:         seededOutput,
		outputCallback: seededFromResult,
		result:         session.CloneState(entry.Result),
		metadata:       session.CloneState(entry.Metadata),
	}
	if task.parentCall == "" {
		task.parentCall = taskStringValue(entry.Metadata["parent_call"])
	}
	if task.requestDigest == "" {
		task.requestDigest = taskStringValue(entry.Metadata["command_request_digest"])
	}
	if cursor, ok := taskInt64Value(entry.Metadata["model_output_cursor"]); ok && cursor >= 0 {
		task.modelCursor = cursor
	}
	phase := taskStringValue(entry.Metadata["command_phase"])
	if phase == commandPhaseIntent {
		return task, nil
	}
	if !entry.Running {
		task.session = completedTaskSession{entry: taskapi.CloneEntry(entry)}
		return task, nil
	}
	if strings.TrimSpace(entry.Terminal.SessionID) == "" && (phase == commandPhaseEffectClaimed || phase == commandPhaseUnknown) {
		return task, nil
	}
	backend := entry.Terminal.Backend
	if backend == "" {
		backend = sandbox.BackendHost
	}
	tm.mu.RLock()
	runtime := tm.backends[backend]
	tm.mu.RUnlock()
	if runtime == nil {
		if commandUnknownWhileRunningPhase(phase) {
			return task, nil
		}
		interruptRehydratedCommandTask(task, entry)
		return task, nil
	}
	var (
		session sandbox.Session
		err     error
	)
	if opener, ok := runtime.(sandboxSessionRefOpener); ok && opener != nil {
		session, err = opener.OpenSessionRef(sandbox.SessionRef{
			Backend:   backend,
			SessionID: strings.TrimSpace(entry.Terminal.SessionID),
		})
	} else {
		session, err = runtime.OpenSession(strings.TrimSpace(entry.Terminal.SessionID))
	}
	if err != nil {
		if commandUnknownWhileRunningPhase(phase) {
			return task, nil
		}
		interruptRehydratedCommandTask(task, entry)
		return task, nil
	}
	task.session = session
	return task, nil
}

func interruptRehydratedCommandTask(task *commandTask, entry *taskapi.Entry) {
	if task == nil || entry == nil {
		return
	}
	task.session = completedTaskSession{entry: taskapi.CloneEntry(entry)}
	task.running = false
	task.state = taskapi.StateInterrupted
	if task.result == nil {
		task.result = map[string]any{}
	}
	task.result["state"] = string(taskapi.StateInterrupted)
	task.result["error"] = "task interrupted during resume"
	task.result["result"] = "task interrupted during resume"
}

func rehydratedCommandOutput(entry *taskapi.Entry) (string, bool) {
	if entry == nil || entry.Result == nil {
		return "", false
	}
	text := taskRawStringValue(entry.Result["result"])
	if text == "" {
		return "", false
	}
	if strings.TrimSpace(text) == noOutputPlaceholder && entry.StdoutCursor == 0 && entry.StderrCursor == 0 {
		return "", true
	}
	return text, true
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}

func (t *commandTask) entrySnapshot(now time.Time) *taskapi.Entry {
	if t == nil {
		return nil
	}
	var terminal sandbox.TerminalRef
	if t.session != nil {
		terminal = t.session.Terminal()
	}
	metadata := commandTaskEntryMetadata(t.metadata, t.running)
	metadata["parent_call"] = t.parentCall
	metadata["command_request_digest"] = t.requestDigest
	return &taskapi.Entry{
		TaskID:         t.ref.TaskID,
		Revision:       t.revision,
		Kind:           taskapi.KindCommand,
		Session:        t.sessionRef,
		Title:          t.title,
		State:          t.state,
		Running:        t.running,
		SupportsInput:  true,
		SupportsCancel: true,
		CreatedAt:      t.createdAt,
		UpdatedAt:      now,
		Lease:          taskapi.CloneLease(t.lease),
		StdoutCursor:   t.stdoutCursor,
		StderrCursor:   t.stderrCursor,
		Spec: map[string]any{
			"command":                t.command,
			"workdir":                t.workdir,
			"session_id":             t.ref.SessionID,
			"parent_call":            t.parentCall,
			"command_request_digest": t.requestDigest,
		},
		Result:   commandTaskEntryResult(t.result, t.running),
		Metadata: metadata,
		Terminal: terminal,
	}
}

func (t *commandTask) commandOutcomeUnattached() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	phase := taskStringValue(t.metadata["command_phase"])
	return strings.TrimSpace(t.ref.SessionID) == "" && (phase == commandPhaseEffectClaimed || phase == commandPhaseUnknown)
}

func (t *commandTask) snapshotWithoutSession(now time.Time) taskapi.Snapshot {
	if t == nil {
		return taskapi.Snapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshotLocked(sandbox.SessionStatus{Running: t.running, SupportsInput: false, UpdatedAt: now})
}

func commandTaskEntryResult(result map[string]any, running bool) map[string]any {
	mode := taskapi.ResultPersistenceCanonical
	if !running {
		mode = taskapi.ResultPersistenceDeferred
	}
	return taskapi.SanitizeResultForPersistence(result, mode)
}

func commandTaskEntryMetadata(metadata map[string]any, running bool) map[string]any {
	out := session.CloneState(metadata)
	if running {
		return out
	}
	delete(out, "output_cursor")
	delete(out, "model_output_cursor")
	return out
}

func (t *commandTask) appendOutput(text string) {
	if t == nil || text == "" {
		return
	}
	t.mu.Lock()
	t.appendOutputLocked(text)
	t.mu.Unlock()
}

func (t *commandTask) appendOutputLocked(text string) {
	if t == nil || text == "" {
		return
	}
	raw := []byte(t.output)
	raw = append(raw, text...)
	if commandLiveOutputBufferCapBytes > 0 && len(raw) > commandLiveOutputBufferCapBytes {
		dropped := len(raw) - commandLiveOutputBufferCapBytes
		raw = raw[dropped:]
		t.outputBase += int64(dropped)
		if t.modelCursor < t.outputBase {
			t.modelCursor = t.outputBase
		}
	}
	t.output = string(raw)
	t.outputLive = true
}

func (t *commandTask) outputCursorLocked() int64 {
	if t == nil {
		return 0
	}
	return t.outputBase + int64(len([]byte(t.output)))
}

func (t *commandTask) outputFromCursorLocked(cursor int64) string {
	if t == nil || t.output == "" {
		return ""
	}
	if cursor < t.outputBase {
		cursor = t.outputBase
	}
	return sliceStringFromByteCursor(t.output, cursor-t.outputBase)
}

// reconcileFinalOutputLocked appends only the canonical result suffix that is
// not yet present in the callback-backed stream. A mismatch is left untouched:
// stdout/stderr result grouping is not guaranteed to preserve live interleave
// order, so replacing or appending an unaligned result would duplicate bytes.
func (t *commandTask) reconcileFinalOutputLocked(finalOutput string) bool {
	if t == nil {
		return false
	}
	if t.output == "" && t.outputBase == 0 && t.stdoutCursor == 0 && t.stderrCursor == 0 && strings.TrimSpace(finalOutput) == noOutputPlaceholder {
		return true
	}
	base := t.outputBase
	cursor := t.outputCursorLocked()
	finalCursor := int64(len([]byte(finalOutput)))
	if base < 0 || cursor < base || base > finalCursor || cursor > finalCursor {
		return false
	}
	if finalOutput[base:cursor] != t.output {
		return false
	}
	if cursor < finalCursor {
		t.appendOutputLocked(finalOutput[cursor:])
	}
	return true
}
