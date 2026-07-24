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
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
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

	commandStreamOutputCursorMeta = "stream_output_cursor"
	commandStreamEventCursorMeta  = "stream_event_cursor"
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
		pendingOutput []sandbox.OutputChunk
		pendingMu     sync.Mutex
	)
	bindOutputTask := func(task *commandTask) {
		pendingMu.Lock()
		outputTask = task
		buffered := append([]sandbox.OutputChunk(nil), pendingOutput...)
		pendingOutput = nil
		pendingMu.Unlock()
		if task != nil {
			for _, chunk := range buffered {
				task.appendSandboxOutput(chunk)
			}
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
				pendingOutput = append(pendingOutput, chunk)
				pendingMu.Unlock()
				return
			}
			pendingMu.Unlock()
			current.appendSandboxOutput(chunk)
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
	handle, err := tm.reserveTaskHandle(ctx, activeSession, ref, taskapi.KindCommand, "command")
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	now := tm.runtime.now()
	createdTask := &commandTask{
		ref: taskapi.Ref{
			TaskID: taskID,
		},
		handle:        handle,
		sessionRef:    session.NormalizeSessionRef(ref),
		command:       strings.TrimSpace(req.Command),
		workdir:       strings.TrimSpace(req.Workdir),
		parentCall:    strings.TrimSpace(req.ParentCall),
		requestDigest: requestDigest,
		title:         shell.RunCommandToolName + " " + strings.TrimSpace(req.Command),
		createdAt:     now,
		state:         taskapi.StatePrepared,
		running:       false,
		outputState: commandOutputState{
			callback: true,
			exact:    true,
			checkpoint: commandOutputCheckpoint{
				available: true,
				coherent:  true,
			},
			resume: commandOutputCheckpoint{
				available: true,
				coherent:  true,
			},
		},
		metadata: map[string]any{
			"task_id": taskID, "handle": handle, "task_kind": string(taskapi.KindCommand),
			"state": string(taskapi.StatePrepared), "running": false,
			"command_phase":               commandPhaseIntent,
			"command_request_digest":      requestDigest,
			"parent_call":                 strings.TrimSpace(req.ParentCall),
			"output_checkpoint_available": true,
			"output_checkpoint_coherent":  true,
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
	claim.State = taskapi.StateRunning
	claim.Running = true
	claim.Metadata = session.CloneState(claim.Metadata)
	claim.Metadata["state"] = string(taskapi.StateRunning)
	claim.Metadata["running"] = true
	claim.Metadata["command_phase"] = commandPhaseEffectClaimed
	claim.Result = map[string]any{
		"state": string(taskapi.StateRunning),
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
	stateChanged := task.state != entry.State || task.running != entry.Running
	task.revision = entry.Revision
	task.handle = firstNonEmpty(entry.Handle, taskSpecString(entry.Spec, "handle"), taskStringValue(entry.Metadata["handle"]))
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
	if stateChanged {
		task.notifyCommandStreamChangeLocked()
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
		task.notifyCommandStreamChangeLocked()
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
	task.notifyCommandStreamChangeLocked()
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
	tm.rememberTaskHandleLocked(task.sessionRef.SessionID, task.handle)
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
	status.Running = false
	status.SupportsInput = false
	if status.UpdatedAt.IsZero() {
		status.UpdatedAt = now
	}

	task.mu.Lock()
	task.state = taskapi.StateUnknownOutcome
	task.running = false
	task.metadata = map[string]any{
		"task_id":       task.ref.TaskID,
		"task_kind":     string(taskapi.KindCommand),
		"state":         string(taskapi.StateUnknownOutcome),
		"running":       false,
		"session_id":    task.ref.SessionID,
		"terminal_id":   task.ref.TerminalID,
		"command_phase": commandPhaseUnknown,
	}
	task.result = map[string]any{
		"state": string(taskapi.StateUnknownOutcome),
		"error": reason,
	}
	task.notifyCommandStreamChangeLocked()
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
	return tm.reconcileCommandStatus(ctx, task, status)
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
	snapshot, completeErr := tm.reconcileCommandStatus(context.WithoutCancel(ctx), task, status)
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
	entry.Running = false
	entry.Metadata = session.CloneState(entry.Metadata)
	entry.Metadata["state"] = string(taskapi.StateUnknownOutcome)
	entry.Metadata["running"] = false
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
	status := sandbox.SessionStatus{SessionRef: task.session.Ref(), Terminal: task.session.Terminal(), Running: false, SupportsInput: false, UpdatedAt: tm.runtime.now()}
	snapshot := task.snapshotLocked(status)
	task.mu.Unlock()
	return snapshot, nil
}
