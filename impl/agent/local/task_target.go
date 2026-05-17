package local

import (
	"context"

	"github.com/OnslaughtSnail/caelis/ports/session"
	taskapi "github.com/OnslaughtSnail/caelis/ports/task"
)

type taskControlTarget interface {
	Wait(context.Context, taskapi.ControlRequest) (taskapi.Snapshot, error)
	Write(context.Context, taskapi.ControlRequest) (taskapi.Snapshot, error)
	Cancel(context.Context, taskapi.ControlRequest) (taskapi.Snapshot, error)
}

func (tm *taskRuntime) control(ctx context.Context, ref session.SessionRef, req taskapi.ControlRequest, fn func(taskControlTarget) (taskapi.Snapshot, error)) (taskapi.Snapshot, error) {
	target, err := tm.lookupControlTarget(ctx, ref, req.TaskID)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	return fn(target)
}

func (tm *taskRuntime) lookupControlTarget(ctx context.Context, ref session.SessionRef, taskID string) (taskControlTarget, error) {
	if task, err := tm.lookupBash(ctx, ref, taskID); err == nil {
		return bashControlTarget{runtime: tm, task: task}, nil
	}
	task, err := tm.lookupSubagent(ctx, ref, taskID)
	if err != nil {
		return nil, err
	}
	return subagentControlTarget{runtime: tm, task: task}, nil
}

type bashControlTarget struct {
	runtime *taskRuntime
	task    *bashTask
}

func (t bashControlTarget) Wait(ctx context.Context, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	return t.runtime.waitBash(ctx, t.task, req.Yield)
}

func (t bashControlTarget) Write(ctx context.Context, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	input := normalizeTaskWriteInput(req.Input)
	if err := t.task.session.WriteInput(ctx, []byte(input)); err != nil {
		return taskapi.Snapshot{}, err
	}
	return t.runtime.waitBash(ctx, t.task, req.Yield)
}

func (t bashControlTarget) Cancel(ctx context.Context, _ taskapi.ControlRequest) (taskapi.Snapshot, error) {
	if err := t.task.session.Terminate(ctx); err != nil {
		return taskapi.Snapshot{}, err
	}
	return t.runtime.waitBash(ctx, t.task, taskCancelWait)
}

type subagentControlTarget struct {
	runtime *taskRuntime
	task    *subagentTask
}

func (t subagentControlTarget) Wait(ctx context.Context, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	if err := t.runtime.authorizeSubagentControl(t.task, req.Source, "wait"); err != nil {
		return taskapi.Snapshot{}, err
	}
	return t.runtime.waitSubagent(ctx, t.task, req.Yield)
}

func (t subagentControlTarget) Write(ctx context.Context, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	return t.runtime.continueSubagent(ctx, t.task, req)
}

func (t subagentControlTarget) Cancel(ctx context.Context, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	if err := t.runtime.authorizeSubagentControl(t.task, req.Source, "cancel"); err != nil {
		return taskapi.Snapshot{}, err
	}
	return t.runtime.cancelSubagent(ctx, t.task)
}
