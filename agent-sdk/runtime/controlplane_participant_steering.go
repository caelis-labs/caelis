package runtime

import (
	"context"
	"errors"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const steeringCommitTimeout = 5 * time.Second

type steeringAdmission struct {
	done chan struct{}
	once sync.Once

	mu  sync.Mutex
	err error
}

func newSteeringAdmission() *steeringAdmission {
	return &steeringAdmission{done: make(chan struct{})}
}

func (a *steeringAdmission) resolve(err error) {
	if a == nil {
		return
	}
	a.once.Do(func() {
		a.mu.Lock()
		a.err = err
		a.mu.Unlock()
		close(a.done)
	})
}

func (a *steeringAdmission) wait(ctx context.Context) error {
	if a == nil {
		return controller.ErrNotActive
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.done:
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.err
}

func (r *Runtime) participantSteeringHandler(
	producerCtx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	binding session.ParticipantBinding,
	turnID string,
	handle *runner,
	admission *steeringAdmission,
) func(context.Context, agent.Submission) error {
	return func(ctx context.Context, submission agent.Submission) error {
		if submission.Kind != agent.SubmissionKindConversation {
			return errorcode.New(errorcode.Unsupported, "agent-sdk/runtime: active participant accepts only conversation steering")
		}
		steerer, ok := r.controllers.(controller.ParticipantSteerer)
		if !ok {
			return controller.ErrParticipantSteeringUnsupported
		}
		if err := admission.wait(ctx); err != nil {
			return errorcode.Wrap(errorcode.FailedPrecondition, "agent-sdk/runtime: participant steering was not dispatched before prompt admission failed", err)
		}
		submission = agent.CloneSubmission(submission)
		return steerer.SteerParticipant(ctx, controller.ParticipantSteerRequest{
			SessionRef:    ref,
			TurnID:        turnID,
			ParticipantID: binding.ID,
			Input:         submission.Text,
			DisplayInput:  submission.DisplayInput,
			ContentParts:  submission.ContentParts,
			Commit: func() error {
				return r.commitParticipantSteering(
					producerCtx,
					activeSession,
					ref,
					binding,
					turnID,
					submission,
					handle,
				)
			},
		})
	}
}

func (r *Runtime) commitParticipantSteering(
	producerCtx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	binding session.ParticipantBinding,
	turnID string,
	submission agent.Submission,
	handle *runner,
) error {
	if r == nil || r.sessions == nil || handle == nil {
		return errors.New("agent-sdk/runtime: participant steering commit is unavailable")
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(producerCtx), steeringCommitTimeout)
	defer cancel()
	event := participantPromptUserEvent(
		activeSession,
		binding,
		turnID,
		"user_side_agent",
		submission.Text,
		submission.DisplayInput,
		"",
		"",
		submission.ContentParts,
		r.now(),
	)
	if event == nil {
		return errorcode.New(errorcode.InvalidArgument, "agent-sdk/runtime: participant steering input is required")
	}
	persisted, err := r.sessions.AppendEvent(commitCtx, session.AppendEventRequest{
		SessionRef:    ref,
		MutationGuard: session.RuntimeMutationGuard(producerCtx),
		Event:         event,
	})
	if err != nil {
		return err
	}
	handle.publishEvent(persisted)
	return nil
}
