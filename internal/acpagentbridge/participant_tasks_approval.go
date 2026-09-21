package acpagentbridge

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/acppermission"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

const maxApprovalResolveRetries = 5

func (o *acpParticipantTasks) requestApproval(ctx context.Context, active *appserver.ActiveApproval) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	wire, err := acppermission.EncodePermissionRequest(session.SessionRef{SessionID: o.sessionID}, active.Permission, nil)
	if err != nil {
		return err
	}
	payload, err := acppermission.DecodePermissionRequest(wire)
	if err != nil {
		return err
	}
	request, err := sdkPermissionRequestFromSchema(wire)
	if err != nil {
		return err
	}
	response, err := o.callbacks.RequestPermission(ctx, request)
	if err != nil {
		return err
	}
	decision := approvalDecisionFromACPResponse(active.RequestID, payload, response)
	return o.resolveApprovalWithRetry(ctx, active, decision)
}

func (o *acpParticipantTasks) resolveApprovalWithRetry(
	ctx context.Context,
	active *appserver.ActiveApproval,
	decision controlprompt.ApprovalDecision,
) error {
	var lastErr error
	for attempt := 0; attempt < maxApprovalResolveRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, pending, err := o.verifyActiveApprovalPending(ctx, active)
		if err != nil {
			return err
		}
		if !pending {
			return nil
		}

		result, err := o.agent.sessionClient.ResolveApproval(ctx, appserver.ResolveApprovalRequest{
			WriteBase: appserver.WriteBase{
				OperationID:             newACPSessionOperationID("background-approval"),
				SessionID:               o.sessionID,
				ExpectedRevision:        &current.Revision,
				ExpectedControllerEpoch: current.Controller.EpochID,
			},
			Target:            active.Target,
			ApprovalRequestID: string(active.RequestID),
			Outcome:           decision.Outcome,
			OptionID:          decision.OptionID,
			Approved:          decision.Approved,
			Reason:            decision.Reason,
			ReviewText:        decision.ReviewText,
		})

		cmdErr := appserver.CommandMutationError(result, err)
		if cmdErr == nil {
			return nil
		}
		lastErr = cmdErr

		if isApprovalRevisionConflict(result, err) {
			// A confirmed revision conflict indicates the session advanced before this mutation
			// was committed. Verify whether the target approval is still pending and unchanged.
			_, stillPending, verifyErr := o.verifyActiveApprovalPending(ctx, active)
			if verifyErr != nil {
				return verifyErr
			}
			if !stillPending {
				return nil
			}
			// Confirmed same RequestID, owner target, and pending state: retry with updated revision.
			continue
		}

		// For unknown outcome or transport failure, inspect the session before deciding.
		// If the active approval is already cleared, the mutation or a concurrent turn succeeded.
		_, stillPending, verifyErr := o.verifyActiveApprovalPending(ctx, active)
		if verifyErr == nil && !stillPending {
			return nil
		}

		// If still pending or verification failed, do not blindly resend with a new OperationID
		// because the previous request may still be in-flight or commit asynchronously.
		return cmdErr
	}
	return lastErr
}

func (o *acpParticipantTasks) verifyActiveApprovalPending(
	ctx context.Context,
	active *appserver.ActiveApproval,
) (*appserver.SessionState, bool, error) {
	state, err := o.agent.sessionClient.InspectSession(ctx, appserver.StateRequest{SessionID: o.sessionID})
	if err != nil {
		return nil, false, err
	}
	return &state, sameParticipantApproval(state.Approval.Active, active), nil
}

func isApprovalRevisionConflict(result appserver.CommandResult, err error) bool {
	if result.Outcome.Valid() {
		return result.Outcome == appserver.OutcomeConflicted
	}
	var outcomeErr *appserver.OutcomeError
	if errors.As(err, &outcomeErr) && outcomeErr != nil && outcomeErr.Outcome.Valid() {
		return outcomeErr.Outcome == appserver.OutcomeConflicted
	}
	var receiptErr *appserver.CommandReceiptError
	if errors.As(err, &receiptErr) && receiptErr != nil && receiptErr.Receipt.Outcome.Valid() {
		return receiptErr.Receipt.Outcome == appserver.OutcomeConflicted
	}
	return errors.Is(err, session.ErrRevisionConflict)
}
