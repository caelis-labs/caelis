package appserveradapter

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
)

// interruptObservedSession samples a Session CAS immediately before dispatch.
// It never creates a Session or converts a Session interrupt to an exact Turn cancel.
func (a *SessionClientAdapter) interruptObservedSession(ctx context.Context, sessionID string) error {
	return cancelTurnWithTimeout(ctx, func(cancelCtx context.Context, _ string) error {
		return a.dispatchSessionInterrupt(cancelCtx, sessionID)
	})
}

func (a *SessionClientAdapter) dispatchSessionInterrupt(ctx context.Context, sessionID string) error {
	if a.sessionClient == nil {
		return noActiveTurnSubmissionError()
	}
	info, err := a.sessionClient.Initialize(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(info.Capabilities, appserver.CapabilitySessionInterrupt) {
		return errorcode.New(errorcode.Unsupported, "Control Host does not support Session interrupts")
	}
	w := &a.sessionInterrupt
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	request, pending := w.pending[sessionID]
	if !pending {
		state, err := a.sessionClient.InspectSession(ctx, appserver.StateRequest{SessionID: sessionID})
		if err != nil {
			return err
		}
		if a.clientSessionID() != sessionID {
			return noActiveTurnSubmissionError()
		}
		revision := state.Revision
		request = appserver.SessionInterruptRequest{
			WriteBase: appserver.WriteBase{
				OperationID: "session-interrupt-" + uuid.NewString(), SessionID: sessionID,
				ExpectedRevision: &revision, ExpectedControllerEpoch: state.Controller.EpochID,
			},
			Reason: "tui interrupt",
		}
		if w.pending == nil {
			w.pending = make(map[string]appserver.SessionInterruptRequest)
		}
		w.pending[sessionID] = request
	}
	if a.clientSessionID() != sessionID {
		return noActiveTurnSubmissionError()
	}
	result, err := a.sessionClient.InterruptSession(ctx, request)
	outcome := result.Outcome
	var outcomeErr *appserver.OutcomeError
	if !outcome.Valid() && errors.As(err, &outcomeErr) {
		outcome = outcomeErr.Outcome
	}
	switch outcome {
	case appserver.OutcomeCommitted, appserver.OutcomeAccepted, appserver.OutcomeRejected, appserver.OutcomeConflicted:
		delete(w.pending, sessionID)
	}

	return appserver.CommandMutationError(result, err)
}

// sessionInterruptWrite retains an uncertain operation's original CAS and ID.
// A later explicit retry observes its receipt instead of cancelling newer work.
type sessionInterruptWrite struct {
	mu      sync.Mutex
	pending map[string]appserver.SessionInterruptRequest
}
