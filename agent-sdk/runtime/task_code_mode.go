package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

type codeModeProducerLifetimeKey struct{}
type codeModeTask struct {
	mu         sync.Mutex
	entry      *taskapi.Entry
	sessionRef session.SessionRef
	handle     string
	done       chan struct{}
	cancel     context.CancelFunc
	result     tool.Result
	err        error
	persistErr error
	// Reuse Task's model-output frontier; Surface output has independent cursors.
	frontier commandObservationFrontier
	observer output.Observer
}

func (t runtimeCodeModeTool) RuntimeTaskResultSource(toolbinding.Token) bool { return true }

func (tm *taskRuntime) startCodeMode(ctx context.Context, owner runtimeCodeModeTool, call tool.Call, source codemode.Source, limits codemode.Limits) (tool.Result, error) {
	var args struct {
		Yield *int `json:"yield_time_ms"`
	}
	if err := json.Unmarshal(call.Input, &args); err != nil {
		return tool.Result{}, err
	}
	yield := defaultCommandYield
	if args.Yield != nil {
		if *args.Yield < 0 || *args.Yield > int(taskWaitMaxYield/time.Millisecond) {
			return tool.Result{}, tool.NewError(tool.ErrorCodeInvalidInput, "yield_time_ms must be between 0 and 60000")
		}
		yield = time.Duration(*args.Yield) * time.Millisecond
	}
	taskID, err := commandTaskID(owner.ref, "code-mode/"+call.Execution.TurnID+"/"+call.Execution.ItemID+"/"+call.ID)
	if err != nil {
		return tool.Result{}, err
	}
	release, err := tm.waitForTaskOperationClaim(ctx, owner.ref, taskID)
	if err != nil {
		return tool.Result{}, err
	}
	if tm.store != nil {
		if _, ok := tm.store.(taskapi.CASStore); !ok {
			release()
			return tool.Result{}, errors.New("durable js_exec requires task.CASStore")
		}
		if existing, getErr := tm.store.Get(ctx, taskID); getErr == nil && existing != nil {
			release()
			return taskSnapshotToolResult(call, owner.Definition(), codeModeSnapshot(existing)), nil
		}
	}
	handle, err := tm.reserveTaskHandle(ctx, owner.active, owner.ref, taskapi.KindCodeMode, "script")
	if err != nil {
		release()
		return tool.Result{}, err
	}
	now := tm.runtime.now()
	entry := &taskapi.Entry{TaskID: taskID, Handle: handle, Session: owner.ref, Kind: taskapi.KindCodeMode, Title: codemode.ToolName, State: taskapi.StateRunning, Running: true, SupportsCancel: true, CreatedAt: now, UpdatedAt: now, Spec: map[string]any{"parent_call": call.ID, "parent_tool": call.Name, "source_path": source.Path, "source_sha256": source.SHA256}, Metadata: map[string]any{"parent_call": call.ID, "parent_tool": call.Name, "turn_id": call.Execution.TurnID}}
	// This context is registered under the original Run producer owner. It must
	// outlive the foreground observation, not the original Run's authority.
	workCtx, cancel := context.WithCancel(ctx)
	task := &codeModeTask{entry: entry, sessionRef: owner.ref, handle: handle, done: make(chan struct{}), cancel: cancel, observer: output.Nop()}
	if err = tm.persistTaskEntry(ctx, entry); err != nil {
		release()
		cancel()
		return tool.Result{}, err
	}
	if tm.runtime.taskOutput != nil {
		task.observer = tm.runtime.taskOutput.BindTaskOutput(workCtx, output.Binding{SessionID: owner.ref.SessionID, TaskID: taskID, Kind: output.TaskKindCodeMode, StartsAtTaskOrigin: true})
	}
	tm.mu.Lock()
	if tm.codeTasks == nil {
		tm.codeTasks = map[string]*codeModeTask{}
	}
	tm.codeTasks[taskID] = task
	tm.mu.Unlock()
	release()
	if transfer, ok := ctx.Value(codeModeProducerLifetimeKey{}).(func(<-chan struct{}, func() error)); ok {
		transfer(task.done, func() error { task.mu.Lock(); defer task.mu.Unlock(); return task.persistErr })
	}
	go func() {
		result, executeErr := owner.execute(workCtx, call, source, limits)
		tm.finishCodeModeTask(workCtx, task, result, executeErr)
	}()
	timer := time.NewTimer(yield)
	defer timer.Stop()
	select {
	case <-task.done:
		task.mu.Lock()
		result, err := task.result, task.err
		task.frontier.model = int64(len(taskStringValue(task.entry.Result["tool_result_json"])))
		snapshot := codeModeSnapshot(task.entry)
		snapshot.Metadata["model_output_cursor"] = task.frontier.model
		if result.Metadata == nil {
			result.Metadata = map[string]any{}
		}
		for key, value := range taskToolMeta(snapshot) {
			result.Metadata[key] = value
		}
		task.mu.Unlock()
		return result, err
	case <-timer.C:
		task.mu.Lock()
		snapshot := codeModeSnapshot(task.entry)
		task.mu.Unlock()
		return taskSnapshotToolResult(call, owner.Definition(), snapshot), nil
	case <-ctx.Done():
		cancel()
		<-task.done
		task.mu.Lock()
		result, err := task.result, task.err
		task.mu.Unlock()
		return result, errors.Join(err, ctx.Err())
	}
}

// The existing Task mutation claim serializes terminal CAS with canonical
// cursor checkpoints. Observations never hold this claim while waiting.
func (tm *taskRuntime) finishCodeModeTask(ctx context.Context, task *codeModeTask, result tool.Result, executeErr error) {
	// Pre-execution failures have no VM settlement observation. Preserve that
	// failed completion on the same original Task result, without changing its
	// cumulative error or lifecycle outcome.
	if executeErr != nil && result.Metadata[codemode.MetadataScriptCompletion] == nil {
		if result.Metadata == nil {
			result.Metadata = map[string]any{}
		}
		result.Metadata[codemode.MetadataScriptCompletion] = "failed"
	}
	raw, marshalErr := json.Marshal(result)
	executeErr = errors.Join(executeErr, marshalErr)
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), approvalResolutionRecoveryTimeout)
	task.mu.Lock()
	taskID := task.entry.TaskID
	task.mu.Unlock()
	release, persistErr := tm.waitForTaskOperationClaim(cleanup, task.sessionRef, taskID)
	state := taskapi.StateUnknownOutcome
	if persistErr == nil {
		task.mu.Lock()
		next := taskapi.CloneEntry(task.entry)
		task.mu.Unlock()
		next.Running = false
		next.UpdatedAt = tm.runtime.now()
		next.State = taskapi.StateCompleted
		switch {
		case result.Metadata["code_mode_outcome"] == codemode.OutcomeUnknown:
			next.State = taskapi.StateUnknownOutcome
		case ctx.Err() != nil:
			next.State = taskapi.StateCancelled
		case executeErr != nil || result.IsError:
			next.State = taskapi.StateFailed
		}
		next.Result = map[string]any{"tool_result_json": string(raw)}
		if executeErr != nil {
			next.Result["error"] = executeErr.Error()
		}
		persistErr = tm.persistTaskEntry(cleanup, next)
		if persistErr == nil {
			task.mu.Lock()
			task.entry = next
			task.mu.Unlock()
			state = next.State
		}
	}
	stop()
	if persistErr != nil {
		// Keep the last committed entry authoritative. Report producer closure and
		// failed durability without publishing an uncommitted completed snapshot.
		result.IsError = true
		result.Content = append(result.Content, model.NewTextPart("script terminal status could not be persisted; preserve original receipts and do not replay effects: "+persistErr.Error()))
		raw, _ = json.Marshal(result)
	}
	task.mu.Lock()
	task.result = result
	task.err = errors.Join(executeErr, persistErr)
	task.persistErr = persistErr
	observer := task.observer
	task.mu.Unlock()
	if release != nil {
		release()
	}
	_ = observer.ObserveTaskOutput(context.WithoutCancel(ctx), output.Event{Text: string(raw), State: string(state), Closed: true, ProducerClosed: true})
	task.cancel()
	close(task.done)
}

func codeModeSnapshot(entry *taskapi.Entry) taskapi.Snapshot {
	return taskapi.Snapshot{Ref: taskapi.Ref{TaskID: entry.TaskID, SessionID: entry.Session.SessionID}, Handle: entry.Handle, Revision: entry.Revision, Kind: entry.Kind, Title: entry.Title, State: entry.State, Running: entry.Running, SupportsCancel: entry.Running, CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt, Result: session.CloneState(entry.Result), Metadata: session.CloneState(entry.Metadata)}
}

type codeModeControlTarget struct {
	runtime *taskRuntime
	task    *codeModeTask
}

func (t codeModeControlTarget) Read(ctx context.Context, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	t.task.mu.Lock()
	defer t.task.mu.Unlock()
	snapshot := codeModeSnapshot(t.task.entry)
	if t.task.persistErr != nil {
		return snapshot, t.task.persistErr
	}
	raw := taskStringValue(snapshot.Result["tool_result_json"])
	if req.Source == "agent_tool" && req.Principal == session.ActorKindTool {
		if t.task.frontier.model >= int64(len(raw)) {
			delete(snapshot.Result, "tool_result_json")
		} else {
			t.task.frontier.model = int64(len(raw))
		}
		if snapshot.Metadata == nil {
			snapshot.Metadata = map[string]any{}
		}
		snapshot.Metadata["output_cursor"] = t.task.frontier.model
		snapshot.Metadata["model_output_cursor"] = t.task.frontier.model
	}
	return snapshot, nil
}
func (t codeModeControlTarget) Wait(ctx context.Context, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	timer := time.NewTimer(max(req.Yield, 0))
	defer timer.Stop()
	select {
	case <-t.task.done:
	case <-timer.C:
	case <-ctx.Done():
		return taskapi.Snapshot{}, ctx.Err()
	}
	return t.Read(ctx, req)
}
func (t codeModeControlTarget) Write(context.Context, taskapi.ControlRequest) (taskapi.Snapshot, error) {
	return taskapi.Snapshot{}, tool.NewError(errorcode.Unsupported, "js_exec Tasks do not accept input; read/wait/cancel the original handle")
}
func (t codeModeControlTarget) Cancel(ctx context.Context, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	t.task.cancel()
	select {
	case <-t.task.done:
	case <-ctx.Done():
		return taskapi.Snapshot{}, ctx.Err()
	}
	return t.Read(ctx, req)
}

func (tm *taskRuntime) codeModeTarget(ctx context.Context, ref session.SessionRef, id string) (taskControlTarget, error) {
	tm.mu.RLock()
	task := tm.codeTasks[id]
	tm.mu.RUnlock()
	if task != nil && task.sessionRef.SessionID == ref.SessionID {
		return codeModeControlTarget{runtime: tm, task: task}, nil
	}
	if tm.store == nil {
		return nil, fmt.Errorf("script Task is unavailable")
	}
	entry, err := tm.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if entry == nil || entry.Session.SessionID != ref.SessionID || entry.Kind != taskapi.KindCodeMode {
		return nil, fmt.Errorf("script Task is unavailable")
	}
	// Recovery preserves records and receipts. It never recreates a worker/stack.
	if entry.Running {
		return nil, tool.NewError(errorcode.FailedPrecondition, "original Run producer is unavailable; script will not be replayed")
	}
	done := make(chan struct{})
	close(done)
	task = &codeModeTask{entry: entry, sessionRef: ref, handle: entry.Handle, done: done, cancel: func() {}, observer: output.Nop()}
	task.frontier.model, _ = taskInt64Value(entry.Metadata["model_output_cursor"])
	tm.mu.Lock()
	if tm.codeTasks == nil {
		tm.codeTasks = map[string]*codeModeTask{}
	}
	tm.codeTasks[id] = task
	tm.mu.Unlock()
	return codeModeControlTarget{runtime: tm, task: task}, nil
}

// Canonical delivery advances the existing Task frontier without replacing its
// original selected result or producer state with an observation envelope.
func (tm *taskRuntime) syncCanonicalCodeModeCursor(ctx context.Context, ref session.SessionRef, event *session.Event) error {
	id := taskRuntimeMetaString(event.Meta, "task", "task_id")
	section := taskRuntimeMetaSection(event.Meta, "task")
	cursor, ok := taskInt64Value(section["model_output_cursor"])
	if !ok || cursor < 0 {
		return nil
	}
	return tm.syncCodeModeCursor(ctx, ref, id, cursor)
}

func (tm *taskRuntime) syncCodeModeCursor(ctx context.Context, ref session.SessionRef, id string, cursor int64) error {
	if cursor < 0 || tm.store == nil {
		return nil
	}
	release, err := tm.waitForTaskOperationClaim(ctx, ref, id)
	if err != nil {
		return err
	}
	defer release()
	entry, err := tm.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if !storedTaskEntryMatches(entry, ref, taskapi.KindCodeMode) {
		return nil
	}
	previous, _ := taskInt64Value(entry.Metadata["model_output_cursor"])
	if cursor <= previous {
		return nil
	}
	if entry.Metadata == nil {
		entry.Metadata = map[string]any{}
	}
	entry.Metadata["model_output_cursor"] = cursor
	return tm.persistTaskEntry(ctx, entry)
}
