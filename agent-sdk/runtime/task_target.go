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

type taskControlIdentity struct {
	taskID string
	kind   taskapi.Kind
}

func (tm *taskRuntime) control(ctx context.Context, ref session.SessionRef, req taskapi.ControlRequest, fn func(taskControlTarget, taskapi.ControlRequest) (taskapi.Snapshot, error)) (taskapi.Snapshot, error) {
	req = normalizeTaskControlRequest(req)
	if err := validateTaskControlPrincipal(req.Principal); err != nil {
		return taskapi.Snapshot{}, err
	}
	identity, err := tm.resolveControlIdentity(ctx, ref, req.TaskID)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	release, claimed := tm.tryClaimSubagentOperation(ref, identity.taskID)
	if !claimed {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: task %q already has an operation in progress", identity.taskID)
	}
	defer release()
	target, err := tm.lookupControlTargetClaimed(ctx, ref, identity)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	if subagent, ok := target.(subagentControlTarget); ok {
		if err := tm.recoverPendingSubagentControlClaimed(ctx, subagent.task); err != nil {
			return taskapi.Snapshot{}, err
		}
	}
	return fn(target, req)
}

func (tm *taskRuntime) resolveControlIdentity(ctx context.Context, ref session.SessionRef, lookupID string) (taskControlIdentity, error) {
	lookupID = strings.TrimSpace(lookupID)
	ref = session.NormalizeSessionRef(ref)
	if lookupID == "" {
		return taskControlIdentity{}, fmt.Errorf("agent-sdk/runtime: task id is required")
	}
	tm.mu.RLock()
	if command := tm.tasks[lookupID]; command != nil && strings.TrimSpace(command.sessionRef.SessionID) == strings.TrimSpace(ref.SessionID) {
		tm.mu.RUnlock()
		return taskControlIdentity{taskID: lookupID, kind: taskapi.KindCommand}, nil
	}
	if subagent := tm.subagents[lookupID]; subagent != nil && strings.TrimSpace(subagent.sessionRef.SessionID) == strings.TrimSpace(ref.SessionID) {
		tm.mu.RUnlock()
		return taskControlIdentity{taskID: lookupID, kind: taskapi.KindSubagent}, nil
	}
	handle := normalizeSubagentHandle(lookupID)
	var matchedID string
	for taskID, candidate := range tm.subagents {
		if candidate == nil || strings.TrimSpace(candidate.sessionRef.SessionID) != strings.TrimSpace(ref.SessionID) || normalizeSubagentHandle(candidate.handle) != handle {
			continue
		}
		if matchedID != "" && matchedID != taskID {
			tm.mu.RUnlock()
			return taskControlIdentity{}, fmt.Errorf("agent-sdk/runtime: subagent handle %q is ambiguous; use the task id", lookupID)
		}
		matchedID = taskID
	}
	tm.mu.RUnlock()
	if matchedID != "" {
		return taskControlIdentity{taskID: matchedID, kind: taskapi.KindSubagent}, nil
	}
	if tm.store == nil {
		return taskControlIdentity{}, fmt.Errorf("agent-sdk/runtime: task %q not found", lookupID)
	}
	entry, err := tm.store.Get(ctx, lookupID)
	if err != nil || entry == nil {
		entry, err = tm.lookupStoredSubagentByHandle(ctx, ref, lookupID)
	}
	if err != nil || entry == nil || strings.TrimSpace(entry.Session.SessionID) != strings.TrimSpace(ref.SessionID) {
		return taskControlIdentity{}, fmt.Errorf("agent-sdk/runtime: task %q not found", lookupID)
	}
	return taskControlIdentity{taskID: strings.TrimSpace(entry.TaskID), kind: entry.Kind}, nil
}

func (tm *taskRuntime) lookupControlTargetClaimed(ctx context.Context, ref session.SessionRef, identity taskControlIdentity) (taskControlTarget, error) {
	switch identity.kind {
	case taskapi.KindCommand:
		task, err := tm.lookupCommandCanonical(ctx, ref, identity.taskID)
		if err != nil {
			return nil, err
		}
		return commandControlTarget{runtime: tm, task: task}, nil
	case taskapi.KindSubagent:
		task, err := tm.lookupSubagentCanonical(ctx, ref, identity.taskID)
		if err != nil {
			return nil, err
		}
		return subagentControlTarget{runtime: tm, task: task}, nil
	default:
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", identity.taskID)
	}
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
	if t.task.commandOutcomeUnattached() {
		return t.task.snapshotWithoutSession(t.runtime.runtime.now()), nil
	}
	input := normalizeTaskWriteInput(req.Input)
	if err := t.task.session.WriteInput(ctx, []byte(input)); err != nil {
		return taskapi.Snapshot{}, err
	}
	return t.runtime.waitCommand(ctx, t.task, req.Yield)
}

func (t commandControlTarget) Cancel(ctx context.Context, _ taskapi.ControlRequest) (taskapi.Snapshot, error) {
	return t.runtime.cancelCommandClaimed(ctx, t.task)
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
	return t.runtime.continueSubagentClaimed(ctx, t.task, req)
}

func (t subagentControlTarget) Cancel(ctx context.Context, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	if err := t.runtime.authorizeSubagentControl(t.task, req.Principal, "cancel"); err != nil {
		return taskapi.Snapshot{}, err
	}
	return t.runtime.cancelSubagent(ctx, t.task)
}
