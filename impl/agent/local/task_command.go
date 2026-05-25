package local

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/shell"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	taskapi "github.com/OnslaughtSnail/caelis/ports/task"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func (tm *taskRuntime) StartCommand(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	runtime sandbox.Runtime,
	req taskapi.CommandStartRequest,
) (taskapi.Snapshot, error) {
	var (
		task          *commandTask
		pendingOutput strings.Builder
		pendingMu     sync.Mutex
	)
	sandboxReq := sandbox.CommandRequest{
		Command: req.Command,
		Dir:     req.Workdir,
		Timeout: req.Timeout,
		OnOutput: func(chunk sandbox.OutputChunk) {
			if chunk.Text == "" {
				return
			}
			pendingMu.Lock()
			current := task
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
	sessionHandle, err := runtime.Start(ctx, sandboxReq)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	now := tm.runtime.now()
	taskID := tm.runtime.nextID("task", nil)
	createdTask := &commandTask{
		ref: taskapi.Ref{
			TaskID:     taskID,
			SessionID:  strings.TrimSpace(sessionHandle.Ref().SessionID),
			TerminalID: strings.TrimSpace(sessionHandle.Terminal().TerminalID),
		},
		sessionRef:     session.NormalizeSessionRef(ref),
		session:        sessionHandle,
		command:        strings.TrimSpace(req.Command),
		workdir:        strings.TrimSpace(req.Workdir),
		title:          shell.RunCommandToolName + " " + strings.TrimSpace(req.Command),
		createdAt:      now,
		state:          taskapi.StateRunning,
		running:        true,
		outputCallback: true,
	}
	pendingMu.Lock()
	task = createdTask
	if pending := pendingOutput.String(); pending != "" {
		task.appendOutputLocked(pending)
	}
	pendingMu.Unlock()
	tm.mu.Lock()
	tm.tasks[taskID] = task
	sessionID := strings.TrimSpace(ref.SessionID)
	tm.order[sessionID] = append(tm.order[sessionID], taskID)
	tm.mu.Unlock()
	if err := tm.persistTaskEntry(ctx, task.entrySnapshot(tm.runtime.now())); err != nil {
		return taskapi.Snapshot{}, err
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
	if err != nil {
		return tm.failCommandTaskIfStopped(ctx, task, err)
	}
	return snapshot, nil
}

func (tm *taskRuntime) waitCommand(ctx context.Context, task *commandTask, yield time.Duration) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: task is required")
	}
	status, err := task.session.Wait(ctx, yield)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	return tm.completeCommandTaskWithStatus(ctx, task, status)
}

func (tm *taskRuntime) completeCommandTaskWithStatus(ctx context.Context, task *commandTask, status sandbox.SessionStatus) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: task is required")
	}
	stdout, stderr, nextStdout, nextStderr, err := task.session.ReadOutput(ctx, task.stdoutCursor, task.stderrCursor)
	if err != nil {
		return taskapi.Snapshot{}, err
	}

	task.mu.Lock()
	task.stdoutCursor = nextStdout
	task.stderrCursor = nextStderr
	if !task.outputCallback {
		task.appendOutputLocked(terminalDeltaText(string(stdout), string(stderr)))
	}
	outputText := task.output
	outputCursor := task.outputCursorLocked()
	state := stateFromStatus(status)
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
		latestOutput := compactLatestOutput(task.outputFromCursorLocked(task.modelCursor))
		task.modelCursor = outputCursor
		task.metadata["output_cursor"] = outputCursor
		task.metadata["model_output_cursor"] = task.modelCursor
		task.result = map[string]any{
			"task_id": task.ref.TaskID,
			"state":   string(state),
		}
		if latestOutput != "" {
			task.result["latest_output"] = latestOutput
		}
		snapshot := task.snapshotLocked(status)
		entry := task.entrySnapshot(tm.runtime.now())
		task.mu.Unlock()
		if err := tm.persistTaskEntry(ctx, entry); err != nil {
			return taskapi.Snapshot{}, err
		}
		return snapshot, nil
	}

	result, resultErr := task.session.Result(ctx)
	stdoutText := result.Stdout
	stderrText := result.Stderr
	finalText := terminalFinalText(outputText, stdoutText, stderrText, resultErr)
	task.metadata["output_cursor"] = int64(len([]byte(finalText)))
	task.metadata["model_output_cursor"] = int64(len([]byte(finalText)))
	task.result = map[string]any{
		"result": finalText,
		"state":  string(state),
	}
	if commandExitCodeAvailable(state, result.ExitCode, resultErr) {
		task.result["exit_code"] = result.ExitCode
	}
	if detail, ok := sandbox.SandboxPermissionDetail(result, resultErr); ok {
		task.result["error"] = detail
		task.result["error_code"] = string(tool.ErrorCodeSandboxDenied)
	} else if resultErr != nil && strings.TrimSpace(finalText) == "(no output)" && !plainTerminalExitError(resultErr) {
		task.result["error"] = strings.TrimSpace(resultErr.Error())
		if code, _ := tool.ErrorPayload(resultErr)["error_code"].(string); code != "" {
			task.result["error_code"] = code
		}
	}
	snapshot := task.snapshotLocked(status)
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return taskapi.Snapshot{}, err
	}
	tm.mu.Lock()
	delete(tm.tasks, task.ref.TaskID)
	tm.mu.Unlock()
	return snapshot, nil
}

func (tm *taskRuntime) failCommandTaskIfStopped(ctx context.Context, task *commandTask, cause error) (taskapi.Snapshot, error) {
	if task == nil || task.session == nil {
		return tm.failCommandTask(ctx, task, cause)
	}
	if err := ctx.Err(); err != nil {
		return taskapi.Snapshot{}, cause
	}
	status, statusErr := task.session.Status(context.WithoutCancel(ctx))
	if statusErr == nil && status.Running {
		return taskapi.Snapshot{}, cause
	}
	if statusErr == nil && plainTerminalExitError(cause) {
		snapshot, err := tm.completeCommandTaskWithStatus(context.WithoutCancel(ctx), task, status)
		if err == nil {
			return snapshot, nil
		}
		return tm.failCommandTask(ctx, task, err)
	}
	return tm.failCommandTask(ctx, task, cause)
}

func (tm *taskRuntime) failCommandTask(ctx context.Context, task *commandTask, cause error) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("impl/agent/local: task is required")
	}
	reason := strings.TrimSpace(fmt.Sprint(cause))
	if reason == "" {
		reason = "command task failed"
	}
	state := taskapi.StateFailed
	if errors.Is(cause, context.Canceled) {
		state = taskapi.StateInterrupted
	}
	persistCtx := context.WithoutCancel(ctx)
	if task.session != nil {
		_ = task.session.Terminate(persistCtx)
	}
	now := tm.runtime.now()
	status := sandbox.SessionStatus{
		Running:   false,
		ExitCode:  -1,
		UpdatedAt: now,
	}
	if task.session != nil {
		status.SessionRef = task.session.Ref()
		status.Terminal = task.session.Terminal()
	} else {
		status.SessionRef = sandbox.SessionRef{SessionID: task.ref.SessionID}
		status.Terminal = sandbox.TerminalRef{
			SessionID:  task.ref.SessionID,
			TerminalID: task.ref.TerminalID,
		}
	}

	task.mu.Lock()
	task.state = state
	task.running = false
	task.metadata = map[string]any{
		"task_id":     task.ref.TaskID,
		"task_kind":   string(taskapi.KindCommand),
		"state":       string(state),
		"running":     false,
		"session_id":  task.ref.SessionID,
		"terminal_id": task.ref.TerminalID,
	}
	if status.Terminal.TerminalID != "" {
		task.metadata["terminal_id"] = status.Terminal.TerminalID
	}
	task.result = map[string]any{
		"state":      string(state),
		"error":      reason,
		"error_code": string(tool.ErrorCodeInvalidInput),
		"result":     reason,
	}
	snapshot := task.snapshotLocked(status)
	entry := task.entrySnapshot(now)
	task.mu.Unlock()
	persistErr := tm.persistTaskEntry(persistCtx, entry)
	tm.mu.Lock()
	delete(tm.tasks, task.ref.TaskID)
	tm.mu.Unlock()
	if persistErr != nil {
		return snapshot, persistErr
	}
	return snapshot, nil
}

func (tm *taskRuntime) lookupCommand(ctx context.Context, ref session.SessionRef, taskID string) (*commandTask, error) {
	tm.mu.RLock()
	task, ok := tm.tasks[strings.TrimSpace(taskID)]
	tm.mu.RUnlock()
	if ok && task != nil {
		if strings.TrimSpace(task.sessionRef.SessionID) != strings.TrimSpace(ref.SessionID) {
			return nil, fmt.Errorf("impl/agent/local: task %q not found", taskID)
		}
		return task, nil
	}
	if tm.store == nil {
		return nil, fmt.Errorf("impl/agent/local: task %q not found", taskID)
	}
	entry, err := tm.store.Get(ctx, strings.TrimSpace(taskID))
	if err != nil || entry == nil {
		return nil, fmt.Errorf("impl/agent/local: task %q not found", taskID)
	}
	if strings.TrimSpace(entry.Session.SessionID) != strings.TrimSpace(ref.SessionID) {
		return nil, fmt.Errorf("impl/agent/local: task %q not found", taskID)
	}
	if entry.Kind != taskapi.KindCommand {
		return nil, fmt.Errorf("impl/agent/local: task %q not found", taskID)
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

func (t *commandTask) snapshotLocked(status sandbox.SessionStatus) taskapi.Snapshot {
	return taskapi.CloneSnapshot(taskapi.Snapshot{
		Ref:            t.ref,
		Kind:           taskapi.KindCommand,
		Title:          t.title,
		State:          t.state,
		Running:        t.running,
		SupportsInput:  status.SupportsInput,
		SupportsCancel: true,
		CreatedAt:      t.createdAt,
		UpdatedAt:      status.UpdatedAt,
		StdoutCursor:   t.stdoutCursor,
		StderrCursor:   t.stderrCursor,
		Result:         canonicalTaskResult(t.result),
		Metadata:       maps.Clone(t.metadata),
		Terminal:       status.Terminal,
	})
}

func (tm *taskRuntime) rehydrateCommandTask(entry *taskapi.Entry) (*commandTask, error) {
	if entry == nil {
		return nil, fmt.Errorf("impl/agent/local: task entry is required")
	}
	task := &commandTask{
		ref: taskapi.Ref{
			TaskID:     strings.TrimSpace(entry.TaskID),
			SessionID:  strings.TrimSpace(entry.Terminal.SessionID),
			TerminalID: strings.TrimSpace(entry.Terminal.TerminalID),
		},
		sessionRef:   session.NormalizeSessionRef(entry.Session),
		command:      taskSpecString(entry.Spec, "command"),
		workdir:      taskSpecString(entry.Spec, "workdir"),
		title:        strings.TrimSpace(entry.Title),
		createdAt:    entry.CreatedAt,
		state:        entry.State,
		running:      entry.Running,
		stdoutCursor: entry.StdoutCursor,
		stderrCursor: entry.StderrCursor,
		output:       taskStringValue(entry.Result["result"]),
		result:       maps.Clone(entry.Result),
		metadata:     maps.Clone(entry.Metadata),
	}
	if cursor, ok := taskInt64Value(entry.Metadata["model_output_cursor"]); ok && cursor >= 0 {
		task.modelCursor = cursor
	}
	if !entry.Running {
		task.session = completedTaskSession{entry: taskapi.CloneEntry(entry)}
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
		task.session = completedTaskSession{entry: taskapi.CloneEntry(entry)}
		task.running = false
		task.state = taskapi.StateInterrupted
		task.result = maps.Clone(entry.Result)
		if task.result == nil {
			task.result = map[string]any{}
		}
		task.result["state"] = string(taskapi.StateInterrupted)
		task.result["error"] = "task interrupted during resume"
		task.result["result"] = "task interrupted during resume"
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
		task.session = completedTaskSession{entry: taskapi.CloneEntry(entry)}
		task.running = false
		task.state = taskapi.StateInterrupted
		if task.result == nil {
			task.result = map[string]any{}
		}
		task.result["state"] = string(taskapi.StateInterrupted)
		task.result["error"] = "task interrupted during resume"
		task.result["result"] = "task interrupted during resume"
		return task, nil
	}
	task.session = session
	return task, nil
}

func (t *commandTask) entrySnapshot(now time.Time) *taskapi.Entry {
	if t == nil {
		return nil
	}
	return &taskapi.Entry{
		TaskID:         t.ref.TaskID,
		Kind:           taskapi.KindCommand,
		Session:        t.sessionRef,
		Title:          t.title,
		State:          t.state,
		Running:        t.running,
		SupportsInput:  true,
		SupportsCancel: true,
		CreatedAt:      t.createdAt,
		UpdatedAt:      now,
		HeartbeatAt:    now,
		StdoutCursor:   t.stdoutCursor,
		StderrCursor:   t.stderrCursor,
		Spec: map[string]any{
			"command":    t.command,
			"workdir":    t.workdir,
			"session_id": t.ref.SessionID,
		},
		Result:   canonicalTaskResult(t.result),
		Metadata: maps.Clone(t.metadata),
		Terminal: t.session.Terminal(),
	}
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
