package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	internalcontrolclient "github.com/caelis-labs/caelis/internal/controlclient"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
	controlport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

const controlFeedPublishTimeout = 5 * time.Second

const controlFeedCatchUpWarning = "session mutation committed; live feed catch-up failed, reconnect to refresh session state"

// ExecuteControlCommand is the app assembly adapter for already-authorized
// transport-neutral commands. The request's operation ID is forwarded in
// downstream metadata wherever the current gateway contract accepts it.
func (s *Stack) ExecuteControlCommand(ctx context.Context, principal controlport.Principal, action controlport.Action, request any) (result controlport.CommandResult, commandErr error) {
	if s == nil {
		return controlport.CommandResult{}, errors.New("gatewayapp: stack is unavailable")
	}
	gw := s.currentGateway()
	if gw == nil {
		return controlport.CommandResult{}, errors.New("gatewayapp: gateway is unavailable")
	}
	defer func() {
		if commandErr != nil || strings.TrimSpace(result.SessionID) == "" {
			return
		}
		if s.controlFeeds == nil {
			result.Detail = controlFeedCatchUpWarning
			return
		}
		feed, err := s.controlFeeds.Session(session.SessionRef{SessionID: result.SessionID})
		if err != nil {
			result.Detail = controlFeedCatchUpWarning
			return
		}
		publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlFeedPublishTimeout)
		defer cancel()
		if err := feed.Prime(publishCtx); err != nil {
			result.Detail = controlFeedCatchUpWarning
		}
	}()
	switch req := request.(type) {
	case controlport.CreateSessionRequest:
		created, err := s.Sessions.StartSession(ctx, session.StartSessionRequest{
			AppName: s.AppName, UserID: strings.TrimSpace(principal.ID),
			Workspace:          session.WorkspaceRef{Key: strings.TrimSpace(req.WorkspaceKey), CWD: strings.TrimSpace(req.CWD)},
			PreferredSessionID: strings.TrimSpace(req.PreferredSessionID), Title: strings.TrimSpace(req.Title), Metadata: req.Metadata,
		})
		return sessionCommandResult(created), classifyControlBackendError(err)
	case controlport.CloseSessionRequest:
		active, err := s.checkControlCommandCASAllowClosed(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		if turn, ok := gw.ActiveTurn(active.SessionID); ok {
			err = gw.Interrupt(ctx, gateway.InterruptRequest{
				SessionRef: active.SessionRef, Reason: "session closed by control client",
				HandleID: turn.HandleID, RunID: turn.RunID, TurnID: turn.TurnID,
				Kind: turn.Kind, ParticipantID: turn.ParticipantID,
			})
			if err != nil {
				return sessionCommandResult(active), classifyControlBackendError(err)
			}
			if err := waitControlTurnStopped(ctx, gw, turn); err != nil {
				return sessionCommandResult(active), classifyControlBackendError(err)
			}
		}
		active, err = s.Sessions.Session(ctx, active.SessionRef)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		updated, err := internalcontrolclient.CloseSession(ctx, s.Sessions, active, "closed by control client")
		if err == nil || session.IsCommitted(err) {
			gw.CloseSessionApprovals(active.SessionRef, "session_closed")
		}
		return sessionCommandResult(updated), classifyControlBackendError(err)
	case controlport.PromptRequest:
		active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		result, err := gw.BeginTurn(ctx, gateway.BeginTurnRequest{
			SessionRef: active.SessionRef, Input: req.Input, DisplayInput: req.DisplayInput, Surface: "control-client",
			Metadata: map[string]any{"operation_id": req.OperationID},
		})
		if err == nil && result.Handle != nil {
			s.attachControlClientHandle(result.Handle)
		}
		out := sessionCommandResult(result.Session)
		if result.Handle != nil {
			out.Target = controlport.TurnTarget{HandleID: result.Handle.HandleID(), RunID: result.Handle.RunID(), TurnID: result.Handle.TurnID()}
		}
		return out, classifyControlBackendError(err)
	case controlport.SteerRequest:
		active, err := s.checkControlTurnTarget(ctx, req.WriteBase, req.Target)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		err = gw.SubmitActiveTurn(ctx, gateway.SubmitActiveTurnRequest{SessionRef: active.SessionRef, Kind: gateway.SubmissionKindConversation, Text: req.Input, DisplayText: req.DisplayInput, Metadata: map[string]any{"operation_id": req.OperationID}})
		return sessionCommandResult(active), classifyControlBackendError(err)
	case controlport.CancelRequest:
		active, err := s.checkControlTurnTarget(ctx, req.WriteBase, req.Target)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		err = gw.Interrupt(ctx, gateway.InterruptRequest{
			SessionRef: active.SessionRef, Reason: req.Reason,
			HandleID: req.Target.HandleID, RunID: req.Target.RunID, TurnID: req.Target.TurnID,
		})
		return sessionCommandResult(active), classifyControlBackendError(err)
	case controlport.ResolveApprovalRequest:
		active, err := s.checkControlApprovalTarget(ctx, req.WriteBase, req.Target, req.ApprovalRequestID)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		err = gw.SubmitActiveTurn(ctx, gateway.SubmitActiveTurnRequest{SessionRef: active.SessionRef, Kind: gateway.SubmissionKindApproval, Approval: &gateway.ApprovalDecision{
			RequestID: eventstream.ApprovalRequestID(req.ApprovalRequestID), Outcome: req.Outcome, OptionID: req.OptionID,
			Approved: req.Approved, Reason: req.Reason, ReviewText: req.ReviewText,
		}})
		return sessionCommandResult(active), classifyControlBackendError(err)
	case controlport.AttachParticipantRequest:
		active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		updated, err := gw.AttachParticipant(ctx, gateway.AttachParticipantRequest{SessionRef: active.SessionRef, Agent: req.Agent, Role: req.Role, Label: req.Label, Source: req.Source})
		return sessionCommandResult(updated), classifyControlBackendError(err)
	case controlport.PromptParticipantRequest:
		active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		result, err := gw.PromptParticipant(ctx, gateway.PromptParticipantRequest{SessionRef: active.SessionRef, ParticipantID: req.ParticipantID, Input: req.Input, DisplayInput: req.DisplayInput, Source: "control-client"})
		if err == nil && result.Handle != nil {
			s.attachControlClientHandle(result.Handle)
		}
		out := sessionCommandResult(result.Session)
		if result.Handle != nil {
			out.Target = controlport.TurnTarget{HandleID: result.Handle.HandleID(), RunID: result.Handle.RunID(), TurnID: result.Handle.TurnID()}
		}
		return out, classifyControlBackendError(err)
	case controlport.CancelParticipantRequest:
		active, err := s.checkControlTurnTarget(ctx, req.WriteBase, req.Target)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		turn, ok := gw.ActiveTurn(active.SessionID)
		if !ok || turn.Kind != gateway.ActiveTurnKindParticipant || strings.TrimSpace(turn.ParticipantID) != strings.TrimSpace(req.ParticipantID) {
			return sessionCommandResult(active), controlport.NewOutcomeError(controlport.OutcomeConflicted, errors.New("controlclient: active turn is not the requested participant turn"))
		}
		err = gw.Interrupt(ctx, gateway.InterruptRequest{
			SessionRef: active.SessionRef, Reason: req.Reason,
			HandleID: req.Target.HandleID, RunID: req.Target.RunID, TurnID: req.Target.TurnID,
			Kind: gateway.ActiveTurnKindParticipant, ParticipantID: req.ParticipantID,
		})
		return sessionCommandResult(active), classifyControlBackendError(err)
	case controlport.DetachParticipantRequest:
		active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		updated, err := gw.DetachParticipant(ctx, gateway.DetachParticipantRequest{SessionRef: active.SessionRef, ParticipantID: req.ParticipantID, Source: req.Source})
		return sessionCommandResult(updated), classifyControlBackendError(err)
	case controlport.HandoffRequest:
		active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		updated, err := gw.HandoffController(ctx, gateway.HandoffControllerRequest{SessionRef: active.SessionRef, Kind: req.Kind, Agent: req.Agent, Source: req.Source, Reason: req.Reason})
		return sessionCommandResult(updated), classifyControlBackendError(err)
	default:
		return controlport.CommandResult{}, fmt.Errorf("gatewayapp: unsupported control command %q (%T)", action, request)
	}
}

func (s *Stack) checkControlCommandCAS(ctx context.Context, base controlport.WriteBase) (session.Session, error) {
	return s.checkControlCommandCASMode(ctx, base, false)
}

func (s *Stack) checkControlCommandCASAllowClosed(ctx context.Context, base controlport.WriteBase) (session.Session, error) {
	return s.checkControlCommandCASMode(ctx, base, true)
}

func (s *Stack) checkControlCommandCASMode(ctx context.Context, base controlport.WriteBase, allowClosed bool) (session.Session, error) {
	active, err := s.Sessions.Session(ctx, session.SessionRef{SessionID: strings.TrimSpace(base.SessionID)})
	if err != nil {
		return session.Session{}, err
	}
	if err := session.CheckExpectedRevision(active, base.ExpectedRevision); err != nil {
		return active, err
	}
	if expected := strings.TrimSpace(base.ExpectedControllerEpoch); expected != "" && strings.TrimSpace(active.Controller.EpochID) != expected {
		return active, fmt.Errorf("controlclient: expected controller epoch %q, actual %q: %w", expected, active.Controller.EpochID, session.ErrRevisionConflict)
	}
	if !allowClosed {
		closed, err := internalcontrolclient.IsSessionClosed(ctx, s.Sessions, active.SessionRef)
		if err != nil {
			return active, err
		}
		if closed {
			return active, internalcontrolclient.ErrSessionClosed
		}
	}
	return active, nil
}

func (s *Stack) checkControlTurnTarget(ctx context.Context, base controlport.WriteBase, target controlport.TurnTarget) (session.Session, error) {
	active, err := s.checkControlCommandCAS(ctx, base)
	if err != nil {
		return active, err
	}
	turn, ok := s.currentGateway().ActiveTurn(active.SessionID)
	if !ok || turn.HandleID != strings.TrimSpace(target.HandleID) || turn.RunID != strings.TrimSpace(target.RunID) || turn.TurnID != strings.TrimSpace(target.TurnID) {
		return active, controlport.NewOutcomeError(controlport.OutcomeConflicted, errors.New("controlclient: live turn target changed"))
	}
	return active, nil
}

func (s *Stack) checkControlApprovalTarget(ctx context.Context, base controlport.WriteBase, target controlport.TurnTarget, requestID string) (session.Session, error) {
	active, err := s.checkControlCommandCAS(ctx, base)
	if err != nil {
		return active, err
	}
	turn, ok := s.currentGateway().ApprovalTarget(active.SessionID, eventstream.ApprovalRequestID(strings.TrimSpace(requestID)))
	if !ok || turn.HandleID != strings.TrimSpace(target.HandleID) || turn.RunID != strings.TrimSpace(target.RunID) || turn.TurnID != strings.TrimSpace(target.TurnID) {
		return active, controlport.NewOutcomeError(controlport.OutcomeConflicted, errors.New("controlclient: approval turn target changed"))
	}
	return active, nil
}

func waitControlTurnStopped(ctx context.Context, gw *kernelimpl.Gateway, target gateway.ActiveTurnState) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, ok := gw.ActiveTurn(target.SessionRef.SessionID)
		if !ok {
			return nil
		}
		if current.HandleID != target.HandleID || current.RunID != target.RunID || current.TurnID != target.TurnID {
			return controlport.NewOutcomeError(controlport.OutcomeConflicted, errors.New("controlclient: another turn started while closing the session"))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func sessionCommandResult(active session.Session) controlport.CommandResult {
	return controlport.CommandResult{Outcome: controlport.OutcomeCommitted, SessionID: active.SessionID, Revision: active.Revision}
}

func classifyControlBackendError(err error) error {
	if err == nil {
		return nil
	}
	var outcomeErr *controlport.OutcomeError
	if errors.As(err, &outcomeErr) {
		return err
	}
	var gatewayErr *gateway.Error
	if errors.As(err, &gatewayErr) {
		switch gatewayErr.Kind {
		case gateway.KindValidation:
			coded := errorcode.Wrap(errorcode.InvalidArgument, gatewayErr.Error(), err)
			return controlport.NewOutcomeError(controlport.OutcomeRejected, coded)
		case gateway.KindConflict, gateway.KindApproval:
			coded := errorcode.Wrap(errorcode.Conflict, "gatewayapp: command conflict", err)
			return controlport.NewOutcomeError(controlport.OutcomeConflicted, coded)
		}
	}
	if errors.Is(err, session.ErrRevisionConflict) || errors.Is(err, session.ErrLeaseConflict) {
		coded := errorcode.Wrap(errorcode.Conflict, "gatewayapp: session conflict", err)
		return controlport.NewOutcomeError(controlport.OutcomeConflicted, coded)
	}
	if session.IsCommitted(err) {
		return nil
	}
	// Only an explicitly typed rejected error proves that no effect committed.
	// Ordinary backend failures remain unknown so the operation ledger cannot
	// expire their idempotency guard and replay a possible external effect.
	return controlport.NewOutcomeError(controlport.OutcomeUnknown, err)
}
