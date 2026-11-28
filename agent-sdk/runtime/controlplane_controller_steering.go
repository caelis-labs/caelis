package runtime

import (
	"context"
	"errors"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (r *Runtime) controllerSteeringHandler(
	producerCtx context.Context,
	ref session.SessionRef,
	turnID string,
	handle *runner,
	admission *steeringAdmission,
) func(context.Context, agent.Submission) error {
	return func(ctx context.Context, submission agent.Submission) error {
		if submission.Kind != agent.SubmissionKindConversation && submission.Kind != agent.SubmissionKindAgentCommunication {
			return errorcode.New(errorcode.Unsupported, "agent-sdk/runtime: active main controller accepts only conversation or Agent communication steering")
		}
		steerer, ok := r.controllers.(controller.ControllerSteerer)
		if !ok {
			return controller.ErrControllerSteeringUnsupported
		}
		if err := admission.wait(ctx); err != nil {
			return errorcode.Wrap(errorcode.FailedPrecondition, "agent-sdk/runtime: main-controller steering was not dispatched before Turn admission failed", err)
		}
		currentSession, err := r.sessions.Session(ctx, ref)
		if err != nil {
			return errorcode.Wrap(errorcode.Unavailable, "agent-sdk/runtime: resolve active main-controller steering target", err)
		}
		submission = agent.CloneSubmission(submission)
		binding := session.CloneControllerBinding(currentSession.Controller)
		if binding.Kind != session.ControllerKindACP || binding.ControllerID == "" ||
			binding.EpochID == "" || binding.RemoteSessionID == "" {
			return errorcode.New(errorcode.FailedPrecondition, "agent-sdk/runtime: active main-controller steering target is unavailable")
		}
		events, err := buildSteeringInputEvents(currentSession, turnID, submission)
		if err != nil {
			return err
		}
		events, err = r.prepareHookInputs(producerCtx, events)
		if err != nil {
			return err
		}
		if hookrun.FromContext(producerCtx).TakeEarly() != nil {
			return errorcode.New(errorcode.Unsupported, "external ACP steering cannot enforce an early-response hook decision")
		}
		parts, err := controllerInputParts(ctx, nil, ref.SessionID, events)
		if err != nil {
			return err
		}
		return steerer.SteerController(ctx, controller.ControllerSteerRequest{
			SessionRef: ref, ControllerID: binding.ControllerID,
			ControllerEpoch: binding.EpochID, RemoteSessionID: binding.RemoteSessionID,
			TurnID: turnID, ContentParts: parts,
			Commit: func() error {
				return r.commitControllerSteering(producerCtx, ref, events, handle)
			},
		})
	}
}

func (r *Runtime) commitControllerSteering(
	producerCtx context.Context,
	ref session.SessionRef,
	events []*session.Event,
	handle *runner,
) error {
	if r == nil || r.sessions == nil || handle == nil {
		return errors.New("agent-sdk/runtime: main-controller steering commit is unavailable")
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(producerCtx), steeringCommitTimeout)
	defer cancel()
	if len(events) == 0 {
		return errorcode.New(errorcode.InvalidArgument, "agent-sdk/runtime: main-controller steering input is required")
	}
	persisted, err := r.appendInputEvents(commitCtx, ref, events)
	if err != nil {
		return err
	}
	for _, event := range persisted {
		handle.publishEvent(event)
	}
	return nil
}
