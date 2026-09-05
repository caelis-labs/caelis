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
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
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
		return agent.ChildInputResult{}, errorcode.New(errorcode.FailedPrecondition, "Target Agent messaging is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	command := agent.CloneChildInputCommand(raw)
	if command.Target == "" || !session.ActorRefHasIdentity(command.Source) {
		return agent.ChildInputResult{}, errorcode.New(errorcode.InvalidArgument, "Target Agent and Source Agent identities are required")
	}
	r.participantMu.Lock()
	defer r.participantMu.Unlock()
	ref = session.NormalizeSessionRef(ref)
	active, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return agent.ChildInputResult{}, err
	}
	source, sourceBinding, err := resolveTrustedChildInputSource(active, command.Source)
	if err != nil {
		return agent.ChildInputResult{}, err
	}
	return r.submitChildInputLocked(ctx, ref, active, command, source, sourceBinding)
}

// SubmitParticipantInput admits one child-originated input against the exact
// durable participant binding that created its source-bound sender. Participant
// attach, detach, compensation, and input admission share participantMu, so the
// current binding is reloaded and remains authoritative until either dispatch
// is rejected or the selected endpoint owns the input.
//
// Parent Turn admission remains outside the topology Runtime and is supplied as
// a narrow callback; sibling resolution and dispatch stay inside this Runtime.
func (r *Runtime) SubmitParticipantInput(
	ctx context.Context,
	ref session.SessionRef,
	expectedSource session.ParticipantBinding,
	raw agent.AgentInput,
	submitParent func(context.Context, session.Session, session.ActorRef, agent.AgentInput) error,
) error {
	if r == nil || r.sessions == nil {
		return errorcode.New(errorcode.FailedPrecondition, "Agent messaging is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	input := agent.CloneAgentInput(raw)
	if input.Target == "" || input.Input == "" && len(input.ContentParts) == 0 {
		return errorcode.New(errorcode.InvalidArgument, "Message target and content are required")
	}
	expectedSource = session.CloneParticipantBinding(expectedSource)
	if expectedSource.ID == "" || expectedSource.SessionID == "" || expectedSource.DelegationID == "" {
		return errorcode.New(errorcode.InvalidArgument, "Source Agent binding is incomplete")
	}

	r.participantMu.Lock()
	defer r.participantMu.Unlock()
	ref = session.NormalizeSessionRef(ref)
	active, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return err
	}
	source, sourceBinding, err := resolveExactParticipantInputSource(active, expectedSource)
	if err != nil {
		return err
	}
	if strings.EqualFold(input.Target, agent.AgentInputParent) {
		if submitParent == nil {
			return errorcode.New(errorcode.FailedPrecondition, "Main Agent messaging is unavailable")
		}
		return submitParent(ctx, active, source, input)
	}
	if r.tasks == nil {
		return errorcode.New(errorcode.FailedPrecondition, "Target Agent messaging is unavailable")
	}
	_, err = r.submitChildInputLocked(ctx, ref, active, agent.ChildInputCommand{
		Target: input.Target, Source: source, Input: input.Input,
		DisplayInput: input.DisplayInput, ContentParts: input.ContentParts,
	}, source, sourceBinding)
	return err
}

func (r *Runtime) submitChildInputLocked(
	ctx context.Context,
	ref session.SessionRef,
	active session.Session,
	command agent.ChildInputCommand,
	source session.ActorRef,
	sourceBinding *session.ParticipantBinding,
) (agent.ChildInputResult, error) {
	binding, err := resolveChildInputBinding(active, command.Target)
	if err != nil {
		return agent.ChildInputResult{}, err
	}
	if sourceBinding != nil && strings.TrimSpace(sourceBinding.ID) == strings.TrimSpace(binding.ID) {
		return agent.ChildInputResult{}, errorcode.New(errorcode.Conflict, "An Agent cannot send a message to itself")
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
		return agent.ChildInputResult{}, errorcode.Wrap(errorcode.Conflict, "Target Agent binding changed", err)
	}
	activityID, outputObserver, completion, err := r.prepareChildTaskOutput(ctx, task)
	if err != nil {
		return agent.ChildInputResult{}, err
	}
	if endpointBinder, ok := task.runner.(agent.ChildEndpointBinder); ok && endpointBinder != nil {
		task.mu.Lock()
		spawn := agent.SubagentSpawnContext{
			SessionRef: session.NormalizeSessionRef(ref), Session: session.CloneSession(active),
			CWD: strings.TrimSpace(active.CWD), TaskID: strings.TrimSpace(task.ref.TaskID), ActivityID: activityID,
			Handle: strings.TrimSpace(task.handle), Role: subagentParticipantRole(task),
			ParentCallID: taskStringValue(task.metadata["parent_call"]), Mode: strings.TrimSpace(task.mode),
			ApprovalMode: strings.TrimSpace(task.approvalMode), Output: outputObserver, Completion: completion,
		}
		task.mu.Unlock()
		spawn.ApprovalRequester = newSubagentApprovalRequester(r, spawn.Mode, nil, active, ref)
		if err := endpointBinder.BindChildEndpoint(ctx, target, spawn); err != nil {
			return agent.ChildInputResult{}, err
		}
	}
	runner, ok := task.runner.(agent.ChildInputRunner)
	if !ok || runner == nil {
		return agent.ChildInputResult{}, errorcode.New(errorcode.Unsupported, "Target Agent cannot receive follow-up messages")
	}
	return runner.SubmitChildInput(ctx, agent.ChildInputRequest{
		Target: target, Source: source, ActivityID: activityID, Output: outputObserver, Completion: completion, Input: command.Input,
		DisplayInput: command.DisplayInput, ContentParts: command.ContentParts,
	})
}

func (r *Runtime) prepareChildTaskOutput(ctx context.Context, task *subagentTask) (string, output.Observer, delegation.CompletionSink, error) {
	if r == nil || r.tasks == nil || task == nil {
		return "", output.Nop(), nil, errorcode.New(errorcode.FailedPrecondition, "Target Agent Task is unavailable")
	}
	task.mu.Lock()
	activityID := strings.TrimSpace(task.activityID)
	running := task.running
	turnSeq := max(task.turnSeq, 1)
	if !running {
		turnSeq++
		activityID = r.nextID("activity", nil)
	}
	ref, taskRef := task.sessionRef, task.ref
	task.mu.Unlock()
	observer := r.tasks.bindSubagentOutput(ctx, ref, taskRef.TaskID, taskRef.TerminalID, activityID, false)
	completion := newSubagentCompletionSink(ctx, r.tasks, taskRef.TaskID, turnSeq)
	if !running {
		activity := &childTaskActivity{
			runtime: r.tasks, ctx: session.ContextWithControlMutation(context.WithoutCancel(ctx), session.ControlMutationPurposeSubagentActivity),
			ref: ref, taskID: taskRef.TaskID,
			activityID: activityID, turnSeq: turnSeq, observer: observer,
		}
		observer = activity
		completion.activity = activity
		completion.observedTerminal = true
	}
	return activityID, observer, completion, nil
}

func resolveExactParticipantInputSource(
	active session.Session,
	expected session.ParticipantBinding,
) (session.ActorRef, *session.ParticipantBinding, error) {
	expected = session.CloneParticipantBinding(expected)
	for i := range active.Participants {
		binding := &active.Participants[i]
		if strings.TrimSpace(binding.ID) != expected.ID ||
			binding.Kind != expected.Kind ||
			strings.TrimSpace(binding.SessionID) != expected.SessionID ||
			strings.TrimSpace(binding.DelegationID) != expected.DelegationID ||
			strings.TrimSpace(binding.AttachmentGeneration) != expected.AttachmentGeneration {
			continue
		}
		trusted := session.ParticipantExecutor(*binding)
		return trusted, binding, nil
	}
	return session.ActorRef{}, nil, errorcode.New(errorcode.PermissionDenied, "Source Agent is detached or replaced")
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
			return session.ActorRef{}, nil, errorcode.New(errorcode.PermissionDenied, "Source Agent controller identity is stale")
		}
		return session.ParentCommunicationActor(), nil, nil
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
		return session.ActorRef{}, nil, errorcode.New(errorcode.PermissionDenied, "Source Agent is detached")
	default:
		return session.ActorRef{}, nil, errorcode.New(errorcode.PermissionDenied, "Source Agent must be the Session controller or an attached participant")
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
			return session.ParticipantBinding{}, errorcode.New(errorcode.Conflict, fmt.Sprintf("Target Agent %q is ambiguous", target))
		}
		clone := session.CloneParticipantBinding(*binding)
		matched = &clone
	}
	if matched == nil {
		return session.ParticipantBinding{}, errorcode.New(errorcode.NotFound, fmt.Sprintf("Target Agent %q is not attached", target))
	}
	return *matched, nil
}

func childEndpointFromBinding(binding session.ParticipantBinding) (agent.ChildEndpointRef, error) {
	if binding.Kind != session.ParticipantKindSubagent || strings.TrimSpace(binding.ID) == "" ||
		strings.TrimSpace(binding.SessionID) == "" || strings.TrimSpace(binding.DelegationID) == "" {
		return agent.ChildEndpointRef{}, errorcode.New(errorcode.Conflict, "Target Agent binding is incomplete")
	}
	role, err := normalizeSubagentParticipantRole(binding.Role)
	if err != nil {
		return agent.ChildEndpointRef{}, errorcode.Wrap(errorcode.Conflict, "Target Agent role is invalid", err)
	}
	frozen := placement.Normalize(binding.Placement)
	if err := placement.Validate(frozen); err != nil {
		return agent.ChildEndpointRef{}, errorcode.Wrap(errorcode.Conflict, "Target Agent placement is invalid", err)
	}
	return agent.NormalizeChildEndpointRef(agent.ChildEndpointRef{
		ParticipantID: strings.TrimSpace(binding.ID),
		SessionID:     strings.TrimSpace(binding.SessionID),
		EndpointKey:   strings.TrimSpace(binding.DelegationID),
		Role:          role,
		Placement:     frozen,
	}), nil
}
