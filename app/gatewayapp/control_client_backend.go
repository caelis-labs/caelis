package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlclient "github.com/caelis-labs/caelis/control/client"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

const controlFeedPublishTimeout = 5 * time.Second

const controlFeedCatchUpWarning = "session mutation committed; live feed catch-up failed, reconnect to refresh session state"

// ExecuteControlCommand is the app assembly adapter for already-authorized
// transport-neutral commands. The request's operation ID is forwarded in
// downstream metadata wherever the current gateway contract accepts it.
func (s *Stack) ExecuteControlCommand(ctx context.Context, principal controlclient.Principal, action controlclient.Action, request any) (result controlclient.CommandResult, commandErr error) {
	if s == nil {
		return controlclient.CommandResult{}, errors.New("gatewayapp: stack is unavailable")
	}
	if s.isClosing() {
		return controlclient.CommandResult{Outcome: controlclient.OutcomeRejected},
			controlclient.NewOutcomeError(
				controlclient.OutcomeRejected,
				errorcode.New(errorcode.Unavailable, "gatewayapp: host is closing"),
			)
	}
	if s.sessionRuntimes == nil {
		return s.executeControlCommand(ctx, principal, action, request)
	}

	if create, ok := request.(controlclient.CreateSessionRequest); ok {
		activationCtx, unlock, err := s.sessionRuntimes.lockActivation(ctx)
		if err != nil {
			return controlclient.CommandResult{Outcome: controlclient.OutcomeRejected},
				classifyControlPreDispatchError(err)
		}
		defer unlock()
		workspace, err := s.sessionRuntimes.resolveCreateWorkspaceLocked(
			activationCtx,
			principal,
			session.WorkspaceRef{Key: create.WorkspaceKey, CWD: create.CWD},
			create.PreferredSessionID,
		)
		if err != nil {
			return controlclient.CommandResult{Outcome: controlclient.OutcomeRejected},
				classifyControlPreDispatchError(err)
		}
		create.WorkspaceKey = workspace.Key
		create.CWD = workspace.CWD
		result, commandErr = s.executeControlCommand(activationCtx, principal, action, create)
		if commandErr != nil || result.Outcome != controlclient.OutcomeCommitted || strings.TrimSpace(result.SessionID) == "" {
			return result, commandErr
		}
		active, err := s.Sessions.Session(activationCtx, session.SessionRef{SessionID: result.SessionID})
		if err != nil {
			return result, controlclient.NewOutcomeError(controlclient.OutcomeUnknown, err)
		}
		if err := s.sessionRuntimes.bindCreatedWorkspaceLocked(active, workspace); err != nil {
			return result, controlclient.NewOutcomeError(controlclient.OutcomeUnknown, err)
		}
		return result, nil
	}

	sessionID := controlCommandSessionID(request)
	runtimeStack := s
	var releaseRuntimeUse func()
	defer func() {
		if releaseRuntimeUse != nil {
			releaseRuntimeUse()
		}
	}()
	defaultSession := s.sessionRuntimes.defaultSession(sessionID)
	switch {
	case controlActionActivatesSessionRuntime(action) && !defaultSession:
		runtime, _, err := s.sessionRuntimes.activateSession(ctx, sessionID)
		if err != nil {
			return controlclient.CommandResult{SessionID: sessionID}, classifyControlPreDispatchError(err)
		}
		releaseRuntimeUse, err = s.sessionRuntimes.acquireRuntimeUse(runtime)
		if err != nil {
			return controlclient.CommandResult{SessionID: sessionID}, classifyControlPreDispatchError(err)
		}
		runtimeStack = runtime.stack
	case controlActionTargetsActiveRuntime(action) && !defaultSession:
		runtime, releaseUse, err := s.sessionRuntimes.acquireLoadedRuntime(sessionID)
		if err != nil {
			return controlclient.CommandResult{SessionID: sessionID}, classifyControlPreDispatchError(err)
		}
		if runtime == nil {
			coded := errorcode.New(
				errorcode.Conflict,
				"gatewayapp: active Turn Runtime is unavailable",
			)
			return controlclient.CommandResult{SessionID: sessionID},
				controlclient.NewOutcomeError(controlclient.OutcomeConflicted, coded)
		}
		releaseRuntimeUse = releaseUse
		runtimeStack = runtime.stack
	case action == controlclient.ActionSessionClose && !defaultSession:
		runtime, releaseUse, err := s.sessionRuntimes.acquireLoadedRuntime(sessionID)
		if err != nil {
			return controlclient.CommandResult{SessionID: sessionID}, classifyControlPreDispatchError(err)
		}
		if runtime != nil {
			releaseRuntimeUse = releaseUse
			runtimeStack = runtime.stack
		} else if _, err := s.Sessions.Session(ctx, session.SessionRef{SessionID: sessionID}); err != nil {
			return controlclient.CommandResult{SessionID: sessionID}, classifyControlPreDispatchError(err)
		}
	}
	result, commandErr = runtimeStack.executeControlCommand(ctx, principal, action, request)
	if action == controlclient.ActionSessionClose &&
		commandErr == nil &&
		result.Outcome == controlclient.OutcomeCommitted {
		if releaseRuntimeUse != nil {
			releaseRuntimeUse()
			releaseRuntimeUse = nil
		}
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlFeedPublishTimeout)
		defer cancel()
		if err := s.sessionRuntimes.releaseSession(releaseCtx, sessionID); err != nil {
			result.Detail = "session closed; execution Runtime cleanup remains pending"
		}
	}
	return result, commandErr
}

func (s *Stack) executeControlCommand(ctx context.Context, principal controlclient.Principal, action controlclient.Action, request any) (result controlclient.CommandResult, commandErr error) {
	if s == nil {
		return controlclient.CommandResult{}, errors.New("gatewayapp: stack is unavailable")
	}
	if s.isClosing() {
		return controlclient.CommandResult{Outcome: controlclient.OutcomeRejected},
			controlclient.NewOutcomeError(
				controlclient.OutcomeRejected,
				errorcode.New(errorcode.Unavailable, "gatewayapp: host is closing"),
			)
	}
	gw := s.currentGateway()
	if gw == nil {
		return controlclient.CommandResult{}, errors.New("gatewayapp: gateway is unavailable")
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
	case controlclient.CreateSessionRequest:
		created, err := s.Sessions.StartSession(ctx, session.StartSessionRequest{
			AppName: s.AppName, UserID: strings.TrimSpace(principal.ID),
			Workspace:          session.WorkspaceRef{Key: strings.TrimSpace(req.WorkspaceKey), CWD: strings.TrimSpace(req.CWD)},
			PreferredSessionID: strings.TrimSpace(req.PreferredSessionID), Title: strings.TrimSpace(req.Title), Metadata: req.Metadata,
		})
		return sessionCommandResult(created), classifyControlBackendError(err)
	case controlclient.CloseSessionRequest:
		active, err := s.checkControlCommandCASAllowClosed(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		if turn, ok := gw.ActiveTurn(active.SessionID); ok {
			err = gw.Interrupt(ctx, kernelimpl.InterruptRequest{
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
		updated, err := controlclient.CloseSession(ctx, s.Sessions, active, "closed by control client")
		if err == nil || session.IsCommitted(err) {
			gw.CloseSessionApprovals(active.SessionRef, "session_closed")
		}
		return sessionCommandResult(updated), classifyControlBackendError(err)
	case controlclient.PromptRequest:
		active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		result, err := gw.BeginTurn(ctx, kernelimpl.BeginTurnRequest{
			SessionRef:     active.SessionRef,
			RuntimeContext: s.controlRuntimeContext(ctx),
			Input:          req.Input,
			DisplayInput:   req.DisplayInput,
			ContentParts:   req.ContentParts,
			Surface:        "control-client",
			Metadata:       map[string]any{"operation_id": req.OperationID},
		})
		if err == nil && result.Handle != nil {
			s.attachControlClientHandle(result.Handle)
		}
		out := sessionCommandResult(result.Session)
		if result.Handle != nil {
			out.Target = controlclient.TurnTarget{HandleID: result.Handle.HandleID(), RunID: result.Handle.RunID(), TurnID: result.Handle.TurnID()}
		}
		return out, classifyControlBackendError(err)
	case controlclient.SteerRequest:
		active, err := s.checkControlTurnTarget(ctx, req.WriteBase, req.Target)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		err = gw.SubmitActiveTurn(ctx, kernelimpl.SubmitActiveTurnRequest{
			SessionRef: active.SessionRef, Kind: kernelimpl.SubmissionKindConversation,
			Text: req.Input, DisplayText: req.DisplayInput, ContentParts: req.ContentParts,
			Metadata: map[string]any{"operation_id": req.OperationID},
		})
		return sessionCommandResult(active), classifyControlBackendError(err)
	case controlclient.CompactSessionRequest:
		active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		err = s.CompactSession(ctx, active.SessionRef)
		return sessionCommandResult(active), classifyControlBackendError(err)
	case controlclient.CancelRequest:
		active, err := s.checkControlTurnTarget(ctx, req.WriteBase, req.Target)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		err = gw.Interrupt(ctx, kernelimpl.InterruptRequest{
			SessionRef: active.SessionRef, Reason: req.Reason,
			HandleID: req.Target.HandleID, RunID: req.Target.RunID, TurnID: req.Target.TurnID,
		})
		return sessionCommandResult(active), classifyControlBackendError(err)
	case controlclient.ResolveApprovalRequest:
		active, err := s.checkControlApprovalTarget(ctx, req.WriteBase, req.Target, req.ApprovalRequestID)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		err = gw.SubmitActiveTurn(ctx, kernelimpl.SubmitActiveTurnRequest{SessionRef: active.SessionRef, Kind: kernelimpl.SubmissionKindApproval, Approval: &kernelimpl.ApprovalDecision{
			RequestID: eventstream.ApprovalRequestID(req.ApprovalRequestID), Outcome: req.Outcome, OptionID: req.OptionID,
			Approved: req.Approved, Reason: req.Reason, ReviewText: req.ReviewText,
		}})
		return sessionCommandResult(active), classifyControlBackendError(err)
	case controlclient.AttachParticipantRequest:
		active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		participantPlacement, err := s.resolveControlParticipantPlacement(ctx, req.ProfileID, req.Effort)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		updated, err := gw.AttachParticipant(ctx, kernelimpl.AttachParticipantRequest{
			SessionRef: active.SessionRef,
			Role:       req.Role,
			Label:      req.Label,
			Source:     req.Source,
			Placement:  participantPlacement,
		})
		return sessionCommandResult(updated), classifyControlBackendError(err)
	case controlclient.StartParticipantRequest:
		active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		handle := agentbinding.NormalizeHandle(agentbinding.Handle(req.Handle))
		placement, err := s.resolveControlHandlePlacement(ctx, handle)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		agentName := strings.TrimSpace(placement.Agent)
		if placement.Kind == sdkplacement.KindModel {
			agentName = string(handle)
		}
		startReq := kernelimpl.StartParticipantRequest{
			SessionRef:     active.SessionRef,
			RuntimeContext: s.controlRuntimeContext(ctx),
			Agent:          agentName,
			Role:           req.Role,
			Label:          req.Label,
			Placement:      placement,
			Input:          req.Input,
			DisplayInput:   req.DisplayInput,
			DisplayAddress: req.DisplayAddress,
			DisplayTitle:   req.DisplayTitle,
			ContentParts:   req.ContentParts,
			Source:         req.Source,
			DetachSource:   req.DetachSource,
		}
		if req.Transient {
			startReq.Lifecycle = kernelimpl.ParticipantLifecycleTransient
		}
		started, err := gw.StartParticipant(ctx, startReq)
		if err == nil && started.Handle != nil {
			s.attachControlClientHandle(started.Handle)
		}
		out := sessionCommandResult(started.Session)
		out.ParticipantID = controlParticipantID(started.Session.Participants, req.Label, req.Source)
		if started.Handle != nil {
			out.Target = controlclient.TurnTarget{HandleID: started.Handle.HandleID(), RunID: started.Handle.RunID(), TurnID: started.Handle.TurnID()}
		}
		return out, classifyControlBackendError(err)
	case controlclient.PromptParticipantRequest:
		active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		result, err := gw.PromptParticipant(ctx, kernelimpl.PromptParticipantRequest{
			SessionRef:     active.SessionRef,
			RuntimeContext: s.controlRuntimeContext(ctx),
			ParticipantID:  req.ParticipantID,
			Input:          req.Input,
			DisplayInput:   req.DisplayInput,
			DisplayAddress: req.DisplayAddress,
			DisplayTitle:   req.DisplayTitle,
			ContentParts:   req.ContentParts,
			Source:         firstNonEmpty(strings.TrimSpace(req.Source), "control-client"),
		})
		if err == nil && result.Handle != nil {
			s.attachControlClientHandle(result.Handle)
		}
		out := sessionCommandResult(result.Session)
		out.ParticipantID = strings.TrimSpace(req.ParticipantID)
		if result.Handle != nil {
			out.Target = controlclient.TurnTarget{HandleID: result.Handle.HandleID(), RunID: result.Handle.RunID(), TurnID: result.Handle.TurnID()}
		}
		return out, classifyControlBackendError(err)
	case controlclient.CancelParticipantRequest:
		active, err := s.checkControlTurnTarget(ctx, req.WriteBase, req.Target)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		turn, ok := gw.ActiveTurn(active.SessionID)
		if !ok || turn.Kind != kernelimpl.ActiveTurnKindParticipant || strings.TrimSpace(turn.ParticipantID) != strings.TrimSpace(req.ParticipantID) {
			return sessionCommandResult(active), controlclient.NewOutcomeError(controlclient.OutcomeConflicted, errors.New("controlclient: active turn is not the requested participant turn"))
		}
		err = gw.Interrupt(ctx, kernelimpl.InterruptRequest{
			SessionRef: active.SessionRef, Reason: req.Reason,
			HandleID: req.Target.HandleID, RunID: req.Target.RunID, TurnID: req.Target.TurnID,
			Kind: kernelimpl.ActiveTurnKindParticipant, ParticipantID: req.ParticipantID,
		})
		return sessionCommandResult(active), classifyControlBackendError(err)
	case controlclient.DetachParticipantRequest:
		active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		updated, err := gw.DetachParticipant(ctx, kernelimpl.DetachParticipantRequest{SessionRef: active.SessionRef, ParticipantID: req.ParticipantID, Source: req.Source})
		return sessionCommandResult(updated), classifyControlBackendError(err)
	case controlclient.HandoffRequest:
		active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		updated, err := gw.HandoffController(ctx, kernelimpl.HandoffControllerRequest{SessionRef: active.SessionRef, Kind: req.Kind, Agent: req.Agent, Source: req.Source, Reason: req.Reason})
		return sessionCommandResult(updated), classifyControlBackendError(err)
	default:
		return controlclient.CommandResult{}, fmt.Errorf("gatewayapp: unsupported control command %q (%T)", action, request)
	}
}

func controlParticipantID(participants []session.ParticipantBinding, label, source string) string {
	label = strings.TrimSpace(label)
	source = strings.TrimSpace(source)
	for i := len(participants) - 1; i >= 0; i-- {
		participant := participants[i]
		if label != "" && !strings.EqualFold(strings.TrimSpace(participant.Label), label) {
			continue
		}
		if source != "" && strings.TrimSpace(participant.Source) != source {
			continue
		}
		if id := strings.TrimSpace(participant.ID); id != "" {
			return id
		}
	}
	return ""
}

func controlCommandSessionID(request any) string {
	switch typed := request.(type) {
	case controlclient.CloseSessionRequest:
		return strings.TrimSpace(typed.SessionID)
	case controlclient.CompactSessionRequest:
		return strings.TrimSpace(typed.SessionID)
	case controlclient.PromptRequest:
		return strings.TrimSpace(typed.SessionID)
	case controlclient.SteerRequest:
		return strings.TrimSpace(typed.SessionID)
	case controlclient.CancelRequest:
		return strings.TrimSpace(typed.SessionID)
	case controlclient.ResolveApprovalRequest:
		return strings.TrimSpace(typed.SessionID)
	case controlclient.AttachParticipantRequest:
		return strings.TrimSpace(typed.SessionID)
	case controlclient.StartParticipantRequest:
		return strings.TrimSpace(typed.SessionID)
	case controlclient.PromptParticipantRequest:
		return strings.TrimSpace(typed.SessionID)
	case controlclient.CancelParticipantRequest:
		return strings.TrimSpace(typed.SessionID)
	case controlclient.DetachParticipantRequest:
		return strings.TrimSpace(typed.SessionID)
	case controlclient.HandoffRequest:
		return strings.TrimSpace(typed.SessionID)
	default:
		return ""
	}
}

func controlActionActivatesSessionRuntime(action controlclient.Action) bool {
	switch action {
	case controlclient.ActionPrompt,
		controlclient.ActionSessionCompact,
		controlclient.ActionParticipantAttach,
		controlclient.ActionParticipantStart,
		controlclient.ActionParticipantPrompt,
		controlclient.ActionParticipantDetach,
		controlclient.ActionControllerHandoff:
		return true
	default:
		return false
	}
}

func controlActionTargetsActiveRuntime(action controlclient.Action) bool {
	switch action {
	case controlclient.ActionSteer,
		controlclient.ActionCancel,
		controlclient.ActionApprovalResolve,
		controlclient.ActionParticipantCancel:
		return true
	default:
		return false
	}
}

func classifyControlPreDispatchError(err error) error {
	if err == nil {
		return nil
	}
	if errorcode.CodeOf(err) == errorcode.Unknown {
		err = errorcode.Wrap(errorcode.Internal, "gatewayapp: resolve control Runtime", err)
	}
	return controlclient.NewOutcomeError(controlclient.OutcomeRejected, err)
}

func (s *Stack) controlRuntimeContext(fallback context.Context) context.Context {
	if s != nil && s.lifecycleCtx != nil {
		return s.lifecycleCtx
	}
	return fallback
}

func (s *Stack) resolveControlParticipantPlacement(ctx context.Context, profileID, effort string) (sdkplacement.Placement, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return sdkplacement.Placement{}, err
	}
	resolved, err := s.resolveParticipantPlacement(ctx, profileID, effort)
	if err != nil {
		var selectionErr *controlplacement.ParticipantSelectionError
		if errors.As(err, &selectionErr) {
			coded := errorcode.Wrap(errorcode.InvalidArgument, "gatewayapp: invalid participant placement", err)
			return sdkplacement.Placement{}, controlclient.NewOutcomeError(controlclient.OutcomeRejected, coded)
		}
		return sdkplacement.Placement{}, err
	}
	return resolved, nil
}

func (s *Stack) resolveControlHandlePlacement(ctx context.Context, handle agentbinding.Handle) (sdkplacement.Placement, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return sdkplacement.Placement{}, err
	}
	snapshot, err := s.placementSnapshot(ctx)
	if err != nil {
		return sdkplacement.Placement{}, err
	}
	handle = agentbinding.NormalizeHandle(handle)
	purpose, err := controlplacement.PurposeForHandle(
		agentbinding.CatalogFor(snapshot.placement.Bindings),
		handle,
	)
	if err == nil {
		placement, resolveErr := controlplacement.ResolveHandle(
			snapshot.placement,
			controlplacement.HandleRequest{Handle: handle, Purpose: purpose},
		)
		if resolveErr == nil {
			return placement, nil
		}
		err = resolveErr
	}
	coded := errorcode.Wrap(errorcode.FailedPrecondition, "gatewayapp: participant handle is unavailable", err)
	return sdkplacement.Placement{}, controlclient.NewOutcomeError(controlclient.OutcomeRejected, coded)
}

func (s *Stack) checkControlCommandCAS(ctx context.Context, base controlclient.WriteBase) (session.Session, error) {
	return s.checkControlCommandCASMode(ctx, base, false)
}

func (s *Stack) checkControlCommandCASAllowClosed(ctx context.Context, base controlclient.WriteBase) (session.Session, error) {
	return s.checkControlCommandCASMode(ctx, base, true)
}

func (s *Stack) checkControlCommandCASMode(ctx context.Context, base controlclient.WriteBase, allowClosed bool) (session.Session, error) {
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
		closed, err := controlclient.IsSessionClosed(ctx, s.Sessions, active.SessionRef)
		if err != nil {
			return active, err
		}
		if closed {
			return active, controlclient.ErrSessionClosed
		}
	}
	return active, nil
}

func (s *Stack) checkControlTurnTarget(ctx context.Context, base controlclient.WriteBase, target controlclient.TurnTarget) (session.Session, error) {
	active, err := s.checkControlCommandCAS(ctx, base)
	if err != nil {
		return active, err
	}
	turn, ok := s.currentGateway().ActiveTurn(active.SessionID)
	if !ok || turn.HandleID != strings.TrimSpace(target.HandleID) || turn.RunID != strings.TrimSpace(target.RunID) || turn.TurnID != strings.TrimSpace(target.TurnID) {
		return active, controlclient.NewOutcomeError(controlclient.OutcomeConflicted, errors.New("controlclient: live turn target changed"))
	}
	return active, nil
}

func (s *Stack) checkControlApprovalTarget(ctx context.Context, base controlclient.WriteBase, target controlclient.TurnTarget, requestID string) (session.Session, error) {
	active, err := s.checkControlCommandCAS(ctx, base)
	if err != nil {
		return active, err
	}
	turn, ok := s.currentGateway().ApprovalTarget(active.SessionID, eventstream.ApprovalRequestID(strings.TrimSpace(requestID)))
	if !ok || turn.HandleID != strings.TrimSpace(target.HandleID) || turn.RunID != strings.TrimSpace(target.RunID) || turn.TurnID != strings.TrimSpace(target.TurnID) {
		return active, controlclient.NewOutcomeError(controlclient.OutcomeConflicted, errors.New("controlclient: approval turn target changed"))
	}
	return active, nil
}

func waitControlTurnStopped(ctx context.Context, gw *kernelimpl.Gateway, target kernelimpl.ActiveTurnState) error {
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
			return controlclient.NewOutcomeError(controlclient.OutcomeConflicted, errors.New("controlclient: another turn started while closing the session"))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func sessionCommandResult(active session.Session) controlclient.CommandResult {
	return controlclient.CommandResult{Outcome: controlclient.OutcomeCommitted, SessionID: active.SessionID, Revision: active.Revision}
}

func classifyControlBackendError(err error) error {
	if err == nil {
		return nil
	}
	var outcomeErr *controlclient.OutcomeError
	if errors.As(err, &outcomeErr) {
		return err
	}
	var gatewayErr *kernelimpl.Error
	if errors.As(err, &gatewayErr) {
		switch gatewayErr.Kind {
		case kernelimpl.KindValidation:
			coded := errorcode.Wrap(errorcode.InvalidArgument, gatewayErr.Error(), err)
			return controlclient.NewOutcomeError(controlclient.OutcomeRejected, coded)
		case kernelimpl.KindConflict, kernelimpl.KindApproval:
			coded := errorcode.Wrap(errorcode.Conflict, "gatewayapp: command conflict", err)
			return controlclient.NewOutcomeError(controlclient.OutcomeConflicted, coded)
		case kernelimpl.KindUnavailable:
			coded := errorcode.Wrap(errorcode.Unavailable, gatewayErr.Error(), err)
			return controlclient.NewOutcomeError(controlclient.OutcomeRejected, coded)
		}
	}
	if errors.Is(err, session.ErrRevisionConflict) || errors.Is(err, session.ErrLeaseConflict) {
		coded := errorcode.Wrap(errorcode.Conflict, "gatewayapp: session conflict", err)
		return controlclient.NewOutcomeError(controlclient.OutcomeConflicted, coded)
	}
	if session.IsCommitted(err) {
		return nil
	}
	// Only an explicitly typed rejected error proves that no effect committed.
	// Ordinary backend failures remain unknown so the operation ledger cannot
	// expire their idempotency guard and replay a possible external effect.
	return controlclient.NewOutcomeError(controlclient.OutcomeUnknown, err)
}
