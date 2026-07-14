package controlclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	controlport "github.com/caelis-labs/caelis/ports/controlclient"
)

type CommandServiceConfig struct {
	Authorizer Authorizer
	Operations OperationStore
	Backend    controlport.CommandBackend
}

type CommandService struct{ config CommandServiceConfig }

const operationCompletionTimeout = 5 * time.Second

func NewCommandService(config CommandServiceConfig) (*CommandService, error) {
	if config.Authorizer == nil || config.Operations == nil || config.Backend == nil {
		return nil, errors.New("controlclient: command service dependencies are required")
	}
	return &CommandService{config: config}, nil
}

func (s *CommandService) CreateSession(ctx context.Context, principal controlport.Principal, req controlport.CreateSessionRequest) (controlport.CommandResult, error) {
	return s.execute(ctx, principal, controlport.ActionSessionCreate, req.WriteBase, createSessionTarget(req), req)
}
func (s *CommandService) CloseSession(ctx context.Context, principal controlport.Principal, req controlport.CloseSessionRequest) (controlport.CommandResult, error) {
	return s.execute(ctx, principal, controlport.ActionSessionClose, req.WriteBase, req.SessionID, req)
}
func (s *CommandService) Prompt(ctx context.Context, principal controlport.Principal, req controlport.PromptRequest) (controlport.CommandResult, error) {
	return s.execute(ctx, principal, controlport.ActionPrompt, req.WriteBase, req.SessionID, req)
}
func (s *CommandService) Steer(ctx context.Context, principal controlport.Principal, req controlport.SteerRequest) (controlport.CommandResult, error) {
	return s.execute(ctx, principal, controlport.ActionSteer, req.WriteBase, turnTargetKey(req.Target), req)
}
func (s *CommandService) Cancel(ctx context.Context, principal controlport.Principal, req controlport.CancelRequest) (controlport.CommandResult, error) {
	return s.execute(ctx, principal, controlport.ActionCancel, req.WriteBase, turnTargetKey(req.Target), req)
}
func (s *CommandService) ResolveApproval(ctx context.Context, principal controlport.Principal, req controlport.ResolveApprovalRequest) (controlport.CommandResult, error) {
	return s.execute(ctx, principal, controlport.ActionApprovalResolve, req.WriteBase, req.ApprovalRequestID+":"+turnTargetKey(req.Target), req)
}
func (s *CommandService) AttachParticipant(ctx context.Context, principal controlport.Principal, req controlport.AttachParticipantRequest) (controlport.CommandResult, error) {
	return s.execute(ctx, principal, controlport.ActionParticipantAttach, req.WriteBase, req.Agent, req)
}
func (s *CommandService) PromptParticipant(ctx context.Context, principal controlport.Principal, req controlport.PromptParticipantRequest) (controlport.CommandResult, error) {
	return s.execute(ctx, principal, controlport.ActionParticipantPrompt, req.WriteBase, req.ParticipantID, req)
}
func (s *CommandService) CancelParticipant(ctx context.Context, principal controlport.Principal, req controlport.CancelParticipantRequest) (controlport.CommandResult, error) {
	return s.execute(ctx, principal, controlport.ActionParticipantCancel, req.WriteBase, req.ParticipantID+":"+turnTargetKey(req.Target), req)
}
func (s *CommandService) DetachParticipant(ctx context.Context, principal controlport.Principal, req controlport.DetachParticipantRequest) (controlport.CommandResult, error) {
	return s.execute(ctx, principal, controlport.ActionParticipantDetach, req.WriteBase, req.ParticipantID, req)
}
func (s *CommandService) Handoff(ctx context.Context, principal controlport.Principal, req controlport.HandoffRequest) (controlport.CommandResult, error) {
	return s.execute(ctx, principal, controlport.ActionControllerHandoff, req.WriteBase, string(req.Kind)+":"+req.Agent, req)
}

func (s *CommandService) execute(ctx context.Context, principal controlport.Principal, action controlport.Action, base controlport.WriteBase, target string, request any) (controlport.CommandResult, error) {
	operationID := strings.TrimSpace(base.OperationID)
	sessionID := strings.TrimSpace(base.SessionID)
	if operationID == "" {
		err := errorcode.New(errorcode.InvalidArgument, "controlclient: operation_id is required")
		return commandFailure(operationID, sessionID, controlport.OutcomeRejected, publicCommandDetail(err, controlport.OutcomeRejected)), err
	}
	if err := validateCommandRequest(action, request); err != nil {
		coded := errorcode.Wrap(errorcode.InvalidArgument, err.Error(), err)
		return commandFailure(operationID, sessionID, controlport.OutcomeRejected, publicCommandDetail(coded, controlport.OutcomeRejected)), coded
	}
	if err := s.config.Authorizer.Authorize(ctx, principal, action, sessionID); err != nil {
		return commandFailure(operationID, sessionID, controlport.OutcomeRejected, publicCommandDetail(err, controlport.OutcomeRejected)), err
	}
	digest, err := requestDigest(request)
	if err != nil {
		coded := errorcode.Wrap(errorcode.InvalidArgument, err.Error(), err)
		return commandFailure(operationID, sessionID, controlport.OutcomeRejected, publicCommandDetail(coded, controlport.OutcomeRejected)), coded
	}
	intent := OperationIntent{
		PrincipalID: strings.TrimSpace(principal.ID), OperationID: operationID, Action: action,
		SessionID: sessionID, Target: strings.TrimSpace(target), Digest: digest,
	}
	record, created, err := s.config.Operations.Begin(ctx, intent)
	if errors.Is(err, ErrOperationConflict) {
		return commandFailure(operationID, sessionID, controlport.OutcomeConflicted, publicCommandDetail(err, controlport.OutcomeConflicted)), err
	}
	if err != nil {
		coded := internalCommandError("controlclient: begin operation", err)
		return commandFailure(operationID, sessionID, controlport.OutcomeRejected, publicCommandDetail(coded, controlport.OutcomeRejected)), coded
	}
	if !created {
		if record.Result != nil {
			return *record.Result, nil
		}
		// An intent without a result may still be executing in this or another
		// process. Report the conservative outcome without claiming completion;
		// only the creator may persist the eventual result.
		unknown := commandFailure(operationID, sessionID, controlport.OutcomeUnknown, "operation intent exists without a provable result")
		return unknown, nil
	}

	result, dispatchErr := s.config.Backend.ExecuteControlCommand(ctx, principal, action, request)
	result.OperationID = operationID
	if result.SessionID == "" {
		result.SessionID = sessionID
	}
	if dispatchErr != nil {
		result = resultForBackendError(result, dispatchErr)
	} else if !result.Outcome.Valid() {
		result.Outcome = controlport.OutcomeCommitted
	}
	completionCtx, cancelCompletion := operationCompletionContext(ctx)
	defer cancelCompletion()
	if _, completeErr := s.config.Operations.Complete(completionCtx, intent, result); completeErr != nil {
		coded := internalCommandError("controlclient: complete operation", completeErr)
		return commandFailure(operationID, result.SessionID, controlport.OutcomeUnknown, publicCommandDetail(coded, controlport.OutcomeUnknown)), coded
	}
	return result, dispatchErr
}

func operationCompletionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Once Backend has returned, Control knows the exact effect result. Client
	// cancellation must not strand the durable ledger at intent-only/unknown and
	// make a later retry unable to distinguish a committed effect from one that
	// never ran. Bound the detached write so a broken store still returns.
	return context.WithTimeout(context.WithoutCancel(ctx), operationCompletionTimeout)
}

func resultForBackendError(result controlport.CommandResult, err error) controlport.CommandResult {
	var outcomeErr *controlport.OutcomeError
	if errors.As(err, &outcomeErr) && outcomeErr.Outcome.Valid() {
		result.Outcome = outcomeErr.Outcome
	} else {
		result.Outcome = controlport.OutcomeRejected
	}
	result.Detail = publicCommandDetail(err, result.Outcome)
	return result
}

func internalCommandError(message string, err error) error {
	if err == nil || errorcode.CodeOf(err) != errorcode.Unknown {
		return err
	}
	return errorcode.Wrap(errorcode.Internal, message, err)
}

func publicCommandDetail(err error, outcome controlport.Outcome) string {
	switch errorcode.CodeOf(err) {
	case errorcode.InvalidArgument:
		return strings.TrimSpace(err.Error())
	case errorcode.Unauthenticated:
		return "authentication required"
	case errorcode.PermissionDenied:
		return "permission denied"
	case errorcode.AlreadyExists, errorcode.Conflict, errorcode.FailedPrecondition:
		return "conflict"
	case errorcode.UnknownOutcome:
		return "effect outcome cannot be proven"
	}
	switch outcome {
	case controlport.OutcomeUnknown:
		return "effect outcome cannot be proven"
	case controlport.OutcomeConflicted:
		return "conflict"
	case controlport.OutcomeRejected:
		return "command rejected"
	default:
		return ""
	}
}

func commandFailure(operationID, sessionID string, outcome controlport.Outcome, detail string) controlport.CommandResult {
	return controlport.CommandResult{OperationID: operationID, SessionID: sessionID, Outcome: outcome, Detail: strings.TrimSpace(detail)}
}

func validateCommandRequest(action controlport.Action, request any) error {
	switch typed := request.(type) {
	case controlport.CreateSessionRequest:
		return nil
	case controlport.CloseSessionRequest:
		return requireSession(typed.SessionID)
	case controlport.PromptRequest:
		if err := requireSession(typed.SessionID); err != nil {
			return err
		}
		if strings.TrimSpace(typed.Input) == "" {
			return errors.New("controlclient: prompt input is required")
		}
	case controlport.SteerRequest:
		if err := requireSessionAndTurn(typed.SessionID, typed.Target); err != nil {
			return err
		}
		if strings.TrimSpace(typed.Input) == "" {
			return errors.New("controlclient: steer input is required")
		}
	case controlport.CancelRequest:
		return requireSessionAndTurn(typed.SessionID, typed.Target)
	case controlport.ResolveApprovalRequest:
		if err := requireSessionAndTurn(typed.SessionID, typed.Target); err != nil {
			return err
		}
		if strings.TrimSpace(typed.ApprovalRequestID) == "" {
			return errors.New("controlclient: approval_request_id is required")
		}
	case controlport.AttachParticipantRequest:
		if err := requireSession(typed.SessionID); err != nil {
			return err
		}
		if strings.TrimSpace(typed.Agent) == "" {
			return errors.New("controlclient: participant agent is required")
		}
	case controlport.PromptParticipantRequest:
		if err := requireSession(typed.SessionID); err != nil {
			return err
		}
		if strings.TrimSpace(typed.ParticipantID) == "" || strings.TrimSpace(typed.Input) == "" {
			return errors.New("controlclient: participant id and input are required")
		}
	case controlport.CancelParticipantRequest:
		if err := requireSessionAndTurn(typed.SessionID, typed.Target); err != nil {
			return err
		}
		if strings.TrimSpace(typed.ParticipantID) == "" {
			return errors.New("controlclient: participant id is required")
		}
	case controlport.DetachParticipantRequest:
		if err := requireSession(typed.SessionID); err != nil {
			return err
		}
		if strings.TrimSpace(typed.ParticipantID) == "" {
			return errors.New("controlclient: participant id is required")
		}
	case controlport.HandoffRequest:
		if err := requireSession(typed.SessionID); err != nil {
			return err
		}
		if typed.Kind == "" {
			return errors.New("controlclient: controller kind is required")
		}
	default:
		return fmt.Errorf("controlclient: unsupported request for %s", action)
	}
	return nil
}

func requireSession(sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("controlclient: session id is required")
	}
	return nil
}

func requireSessionAndTurn(sessionID string, target controlport.TurnTarget) error {
	if err := requireSession(sessionID); err != nil {
		return err
	}
	if strings.TrimSpace(target.HandleID) == "" || strings.TrimSpace(target.RunID) == "" || strings.TrimSpace(target.TurnID) == "" {
		return errors.New("controlclient: explicit handle, run, and turn target is required")
	}
	return nil
}

func turnTargetKey(target controlport.TurnTarget) string {
	return strings.TrimSpace(target.HandleID) + ":" + strings.TrimSpace(target.RunID) + ":" + strings.TrimSpace(target.TurnID)
}

func createSessionTarget(req controlport.CreateSessionRequest) string {
	return strings.TrimSpace(req.PreferredSessionID) + ":" + strings.TrimSpace(req.WorkspaceKey)
}
