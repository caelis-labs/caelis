package runtime

import (
	"context"
	"errors"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/agentcommunication"
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
		if len(submission.Inputs) > 1 {
			if _, ok := r.sessions.(session.EventBatchService); !ok {
				return errorcode.New(errorcode.Unsupported, "agent-sdk/runtime: batched steering requires atomic Session append")
			}
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
		input := submission.Text
		parts := submission.ContentParts
		if submission.Kind == agent.SubmissionKindAgentCommunication {
			if len(submission.Inputs) > 0 {
				input, parts = "", nil
				for _, item := range submission.Inputs {
					_, itemParts, err := agentcommunication.Prompt(item.Input, item.ContentParts, item.Source)
					if err != nil {
						return errorcode.Wrap(errorcode.InvalidArgument, "agent-sdk/runtime: prepare Agent communication steering", err)
					}
					parts = append(parts, itemParts...)
				}
			} else {
				input, parts, err = agentcommunication.Prompt(input, parts, submission.Actor)
				if err != nil {
					return errorcode.Wrap(errorcode.InvalidArgument, "agent-sdk/runtime: prepare Agent communication steering", err)
				}
			}
		}
		return steerer.SteerController(ctx, controller.ControllerSteerRequest{
			SessionRef: ref, ControllerID: binding.ControllerID,
			ControllerEpoch: binding.EpochID, RemoteSessionID: binding.RemoteSessionID,
			TurnID: turnID, Input: input, DisplayInput: submission.DisplayInput,
			ContentParts: parts,
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
	events, err := buildSteeringInputEvents(activeSession, turnID, submission)
	if err != nil {
		return err
	}
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
