package runtime

import (
	"context"
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

// SubmitChildInput resolves one topology address to an exact delegated child
// endpoint, installs the Task output observer, and then invokes the optional
// provider-neutral runner capability. It does not claim or mutate a Task
// operation on input admission.
func (r *Runtime) SubmitChildInput(
	ctx context.Context,
	ref session.SessionRef,
	raw agent.ChildInputCommand,
) (agent.ChildInputResult, error) {
	if r == nil || r.sessions == nil || r.tasks == nil {
		return agent.ChildInputResult{}, errorcode.New(errorcode.FailedPrecondition, "agent-sdk/runtime: child input service is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	command := agent.CloneChildInputCommand(raw)
	if command.Target == "" || !session.ActorRefHasIdentity(command.Source) {
		return agent.ChildInputResult{}, errorcode.New(errorcode.InvalidArgument, "agent-sdk/runtime: child input target and trusted source are required")
	}
	ref = session.NormalizeSessionRef(ref)
	active, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return agent.ChildInputResult{}, err
	}
	source, sourceBinding, err := resolveTrustedChildInputSource(active, command.Source)
	if err != nil {
		return agent.ChildInputResult{}, err
	}
	binding, err := resolveChildInputBinding(active, command.Target)
	if err != nil {
		return agent.ChildInputResult{}, err
	}
	if sourceBinding != nil && strings.TrimSpace(sourceBinding.ID) == strings.TrimSpace(binding.ID) {
		return agent.ChildInputResult{}, errorcode.New(errorcode.Conflict, "agent-sdk/runtime: a child endpoint cannot steer itself")
	}
	target, err := childEndpointFromBinding(binding)
	if err != nil {
		return agent.ChildInputResult{}, err
	}
	task, err := r.tasks.lookupSubagent(ctx, ref, target.EndpointKey)
	if err != nil {
		return agent.ChildInputResult{}, err
	}
	if err := validateTaskActivityTarget(task, target); err != nil {
		return agent.ChildInputResult{}, errorcode.Wrap(errorcode.Conflict, "agent-sdk/runtime: child binding is stale", err)
	}
	observer := newSubagentActivityObserver(r.tasks, target.EndpointKey)
	if endpointBinder, ok := task.runner.(agent.ChildEndpointBinder); ok && endpointBinder != nil {
		task.mu.Lock()
		spawn := agent.SubagentSpawnContext{
			SessionRef: session.NormalizeSessionRef(ref), Session: session.CloneSession(active),
			CWD: strings.TrimSpace(active.CWD), TaskID: strings.TrimSpace(task.ref.TaskID),
			Handle: strings.TrimSpace(task.handle), Role: subagentParticipantRole(task),
			ParentCallID: taskStringValue(task.metadata["parent_call"]), Mode: strings.TrimSpace(task.mode),
			ApprovalMode: strings.TrimSpace(task.approvalMode), Streams: r.tasks,
			ActivityObserver: observer, ActivityAfterCursor: task.activityDurableCursor,
		}
		task.mu.Unlock()
		spawn.ApprovalRequester = newSubagentApprovalRequester(r, spawn.Mode, nil, active, ref)
		if err := endpointBinder.BindChildEndpoint(ctx, target, spawn); err != nil {
			return agent.ChildInputResult{}, err
		}
	}
	runner, ok := task.runner.(agent.ChildInputRunner)
	if !ok || runner == nil {
		return agent.ChildInputResult{}, errorcode.New(errorcode.Unsupported, "agent-sdk/runtime: child runner does not support Agent communication")
	}
	binder, ok := task.runner.(agent.ChildActivityObserverBinder)
	if !ok || binder == nil {
		return agent.ChildInputResult{}, errorcode.New(errorcode.FailedPrecondition, "agent-sdk/runtime: child runner has no output observer binding")
	}
	if err := binder.BindChildActivityObserver(ctx, target, subagentActivityCursor(task), observer); err != nil {
		return agent.ChildInputResult{}, err
	}
	return runner.SubmitChildInput(ctx, agent.ChildInputRequest{
		Target: target, Source: source, Input: command.Input,
		DisplayInput: command.DisplayInput, ContentParts: command.ContentParts,
	})
}

func resolveTrustedChildInputSource(
	active session.Session,
	raw session.ActorRef,
) (session.ActorRef, *session.ParticipantBinding, error) {
	source := session.CloneActorRef(raw)
	switch source.Kind {
	case session.ActorKindController:
		if strings.TrimSpace(active.Controller.ControllerID) == "" ||
			strings.TrimSpace(source.ID) != strings.TrimSpace(active.Controller.ControllerID) {
			return session.ActorRef{}, nil, errorcode.New(errorcode.PermissionDenied, "agent-sdk/runtime: child input controller source is stale")
		}
		return session.ControllerExecutor(active.Controller), nil, nil
	case session.ActorKindParticipant:
		for i := range active.Participants {
			binding := &active.Participants[i]
			if strings.TrimSpace(source.ID) != strings.TrimSpace(binding.ID) &&
				strings.TrimSpace(source.ID) != strings.TrimSpace(binding.SessionID) {
				continue
			}
			trusted := session.ParticipantExecutor(*binding)
			return trusted, binding, nil
		}
		return session.ActorRef{}, nil, errorcode.New(errorcode.PermissionDenied, "agent-sdk/runtime: child input participant source is detached")
	default:
		return session.ActorRef{}, nil, errorcode.New(errorcode.PermissionDenied, "agent-sdk/runtime: child input source must be the Session controller or an attached participant")
	}
}

func resolveChildInputBinding(active session.Session, target string) (session.ParticipantBinding, error) {
	target = strings.TrimSpace(target)
	handle := taskapi.NormalizeHandle(target)
	var matched *session.ParticipantBinding
	for i := range active.Participants {
		binding := &active.Participants[i]
		if binding.Kind != session.ParticipantKindSubagent {
			continue
		}
		match := target == strings.TrimSpace(binding.ID) ||
			target == strings.TrimSpace(binding.SessionID) ||
			target == strings.TrimSpace(binding.DelegationID) ||
			(handle != "" && handle == taskapi.NormalizeHandle(binding.Label))
		if !match {
			continue
		}
		if matched != nil {
			return session.ParticipantBinding{}, errorcode.New(errorcode.Conflict, fmt.Sprintf("agent-sdk/runtime: child input target %q is ambiguous", target))
		}
		clone := session.CloneParticipantBinding(*binding)
		matched = &clone
	}
	if matched == nil {
		return session.ParticipantBinding{}, errorcode.New(errorcode.NotFound, fmt.Sprintf("agent-sdk/runtime: child input target %q is not attached", target))
	}
	return *matched, nil
}

func childEndpointFromBinding(binding session.ParticipantBinding) (agent.ChildEndpointRef, error) {
	if binding.Kind != session.ParticipantKindSubagent || strings.TrimSpace(binding.ID) == "" ||
		strings.TrimSpace(binding.SessionID) == "" || strings.TrimSpace(binding.DelegationID) == "" {
		return agent.ChildEndpointRef{}, errorcode.New(errorcode.Conflict, "agent-sdk/runtime: child participant binding is incomplete")
	}
	role, err := normalizeSubagentParticipantRole(binding.Role)
	if err != nil {
		return agent.ChildEndpointRef{}, errorcode.Wrap(errorcode.Conflict, "agent-sdk/runtime: child participant role is invalid", err)
	}
	frozen := placement.Normalize(binding.Placement)
	if err := placement.Validate(frozen); err != nil {
		return agent.ChildEndpointRef{}, errorcode.Wrap(errorcode.Conflict, "agent-sdk/runtime: child participant placement is invalid", err)
	}
	return agent.NormalizeChildEndpointRef(agent.ChildEndpointRef{
		ParticipantID: strings.TrimSpace(binding.ID),
		SessionID:     strings.TrimSpace(binding.SessionID),
		EndpointKey:   strings.TrimSpace(binding.DelegationID),
		Role:          role,
		Placement:     frozen,
	}), nil
}
