package runtime

import (
	"context"
	"errors"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
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
		if submission.Kind != agent.SubmissionKindConversation {
			return errorcode.New(errorcode.Unsupported, "agent-sdk/runtime: active main controller accepts only conversation steering")
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
		return steerer.SteerController(ctx, controller.ControllerSteerRequest{
			SessionRef: ref, ControllerID: binding.ControllerID,
			ControllerEpoch: binding.EpochID, RemoteSessionID: binding.RemoteSessionID,
			TurnID: turnID, Input: submission.Text, DisplayInput: submission.DisplayInput,
			ContentParts: submission.ContentParts,
			Commit: func() error {
				return r.commitControllerSteering(producerCtx, currentSession, ref, turnID, submission, handle)
			},
		})
	}
}

func (r *Runtime) commitControllerSteering(
	producerCtx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	turnID string,
	submission agent.Submission,
	handle *runner,
) error {
	if r == nil || r.sessions == nil || handle == nil {
		return errors.New("agent-sdk/runtime: main-controller steering commit is unavailable")
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(producerCtx), steeringCommitTimeout)
	defer cancel()
	event := buildInputEvent(
		activeSession, turnID, submission.Text, submission.DisplayInput,
		submission.ContentParts, submission.Actor, nil,
	)
	if event == nil {
		return errorcode.New(errorcode.InvalidArgument, "agent-sdk/runtime: main-controller steering input is required")
	}
	// The initial Turn input owns turn-input:<turn>. Steering has no delivery
	// identity and each accepted call is an independent ordinary user event.
	event.IdempotencyKey = ""
	persisted, err := r.sessions.AppendEvent(commitCtx, session.AppendEventRequest{
		SessionRef: ref, MutationGuard: session.RuntimeMutationGuard(producerCtx), Event: event,
	})
	if err != nil {
		return err
	}
	handle.publishEvent(persisted)
	return nil
}
