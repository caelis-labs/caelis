package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

type taskControlTarget interface {
	Wait(context.Context, taskapi.ControlRequest) (taskapi.Snapshot, error)
	Write(context.Context, taskapi.ControlRequest) (taskapi.Snapshot, error)
	Cancel(context.Context, taskapi.ControlRequest) (taskapi.Snapshot, error)
}

func (tm *taskRuntime) control(ctx context.Context, ref session.SessionRef, req taskapi.ControlRequest, fn func(taskControlTarget, taskapi.ControlRequest) (taskapi.Snapshot, error)) (taskapi.Snapshot, error) {
	req = normalizeTaskControlRequest(req)
	if err := validateTaskControlPrincipal(req.Principal); err != nil {
		return taskapi.Snapshot{}, err
	}
	target, err := tm.lookupControlTarget(ctx, ref, req.TaskID)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	return fn(target, req)
}

func normalizeTaskControlRequest(req taskapi.ControlRequest) taskapi.ControlRequest {
	return taskapi.ControlRequest{
		TaskID:         strings.TrimSpace(req.TaskID),
		Yield:          req.Yield,
		Input:          req.Input,
		Principal:      req.Principal,
		Source:         strings.TrimSpace(req.Source),
		ContextPrelude: req.ContextPrelude,
	}
}

func validateTaskControlPrincipal(principal session.ActorKind) error {
	switch principal {
	case session.ActorKindUser, session.ActorKindController, session.ActorKindParticipant, session.ActorKindTool, session.ActorKindSystem:
		return nil
	default:
		return fmt.Errorf("agent-sdk/runtime: unsupported control principal %q", principal)
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
	if err := t.runtime.authorizeSubagentControl(t.task, req.Principal, "wait"); err != nil {
		return taskapi.Snapshot{}, err
	}
	return t.runtime.waitSubagent(ctx, t.task, req.Yield)
}

func (t subagentControlTarget) Write(ctx context.Context, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	return t.runtime.continueSubagent(ctx, t.task, req)
}

func (t subagentControlTarget) Cancel(ctx context.Context, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	if err := t.runtime.authorizeSubagentControl(t.task, req.Principal, "cancel"); err != nil {
		return taskapi.Snapshot{}, err
	}
	return t.runtime.cancelSubagent(ctx, t.task)
}
