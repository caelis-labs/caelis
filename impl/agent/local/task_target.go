package local

import (
	"context"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/session"
	taskapi "github.com/OnslaughtSnail/caelis/ports/task"
)

type taskControlTarget interface {
	Wait(context.Context, taskapi.ControlRequest) (taskapi.Snapshot, error)
	Write(context.Context, taskapi.ControlRequest) (taskapi.Snapshot, error)
	Cancel(context.Context, taskapi.ControlRequest) (taskapi.Snapshot, error)
}

func (tm *taskRuntime) control(ctx context.Context, ref session.SessionRef, req taskapi.ControlRequest, fn func(taskControlTarget) (taskapi.Snapshot, error)) (taskapi.Snapshot, error) {
	req = normalizeTaskControlRequest(req)
	target, err := tm.lookupControlTarget(ctx, ref, req.TaskID)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	return fn(target)
}

func normalizeTaskControlRequest(req taskapi.ControlRequest) taskapi.ControlRequest {
	return taskapi.ControlRequest{
		TaskID:         strings.TrimSpace(req.TaskID),
		Yield:          req.Yield,
		Input:          req.Input,
		Source:         strings.TrimSpace(req.Source),
		ContextPrelude: req.ContextPrelude,
	}
}

func (tm *taskRuntime) lookupControlTarget(ctx context.Context, ref session.SessionRef, taskID string) (taskControlTarget, error) {
	if task, err := tm.lookupCommand(ctx, ref, taskID); err == nil {
		return commandControlTarget{runtime: tm, task: task}, nil
	}
	task, err := tm.lookupSubagent(ctx, ref, taskID)
	if err != nil {
		return nil, err
	}
	return subagentControlTarget{runtime: tm, task: task}, nil
}

type commandControlTarget struct {
	runtime *taskRuntime
	task    *commandTask
}

func (t commandControlTarget) Wait(ctx context.Context, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	return t.runtime.waitCommand(ctx, t.task, req.Yield)
}

func (t commandControlTarget) Write(ctx context.Context, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	input := normalizeTaskWriteInput(req.Input)
	if err := t.task.session.WriteInput(ctx, []byte(input)); err != nil {
		return taskapi.Snapshot{}, err
	}
	return t.runtime.waitCommand(ctx, t.task, req.Yield)
}

func (t commandControlTarget) Cancel(ctx context.Context, _ taskapi.ControlRequest) (taskapi.Snapshot, error) {
	if err := t.task.session.Terminate(ctx); err != nil {
		return taskapi.Snapshot{}, err
	}
	return t.runtime.waitCommand(ctx, t.task, taskCancelWait)
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
