package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

const controlFeedPublishTimeout = 5 * time.Second

const controlFeedCatchUpWarning = "session mutation committed; live feed catch-up failed, reconnect to refresh session state"

// ExecuteControlCommand is the app assembly adapter for already-authorized
// transport-neutral commands. The request's operation ID is forwarded in
// downstream metadata wherever the current gateway contract accepts it.
func (s *Stack) ExecuteControlCommand(ctx context.Context, principal appserver.Principal, action appserver.Action, request any) (result appserver.CommandResult, commandErr error) {
	if s == nil {
		return appserver.CommandResult{}, errors.New("gatewayapp: stack is unavailable")
	}
	if s.composition.isClosing() {
		return appserver.CommandResult{Outcome: appserver.OutcomeRejected},
			appserver.NewOutcomeError(
				appserver.OutcomeRejected,
				errorcode.New(errorcode.Unavailable, "gatewayapp: host is closing"),
			)
	}
	if isHostConfigurationCommandRequest(request) {
		return s.executeConfigurationCommand(ctx, action, request)
	}
	if isHostAgentCommandRequest(request) {
		return s.executeAgentCommand(ctx, action, request)
	}
	if isHostPluginCommandRequest(request) {
		return s.executePluginCommand(ctx, action, request)
	}
	if s.sessionRuntimes == nil {
		return s.composition.executeControlCommand(ctx, principal, action, request)
	}

	if create, ok := request.(appserver.CreateSessionRequest); ok {
		activationCtx, unlock, err := s.sessionRuntimes.lockActivation(ctx)
		if err != nil {
			return appserver.CommandResult{Outcome: appserver.OutcomeRejected},
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
			return appserver.CommandResult{Outcome: appserver.OutcomeRejected},
				classifyControlPreDispatchError(err)
		}
		create.WorkspaceKey = workspace.Key
		create.CWD = workspace.CWD
		result, commandErr = s.composition.executeControlCommand(activationCtx, principal, action, create)
		if commandErr != nil || result.Outcome != appserver.OutcomeCommitted || strings.TrimSpace(result.SessionID) == "" {
			return result, commandErr
		}
		active, err := s.composition.sessions.Session(activationCtx, session.SessionRef{SessionID: result.SessionID})
		if err != nil {
			return result, appserver.NewOutcomeError(appserver.OutcomeUnknown, err)
		}
		if err := s.sessionRuntimes.bindCreatedWorkspaceLocked(active, workspace); err != nil {
			return result, appserver.NewOutcomeError(appserver.OutcomeUnknown, err)
		}
		return result, nil
	}

	sessionID := controlCommandSessionID(request)
	composition := &s.composition
	var newlyActivatedRuntime *sessionRuntime
	var releaseRuntimeUse func()
	var closeControlRuntime func(context.Context) error
	defer func() {
		if releaseRuntimeUse != nil {
			releaseRuntimeUse()
		}
		if closeControlRuntime != nil {
			closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlFeedPublishTimeout)
			closeErr := closeControlRuntime(closeCtx)
			cancel()
			if closeErr == nil {
				return
			}
			if result.Outcome == appserver.OutcomeCommitted {
				result.Detail = firstNonEmpty(result.Detail, "Session configuration committed; disposable Runtime cleanup remains pending")
				return
			}
			commandErr = errors.Join(commandErr, errorcode.Wrap(errorcode.Internal, "gatewayapp: close Session configuration Runtime", closeErr))
		}
	}()
	switch {
	case controlActionConfiguresSession(action):
		runtime, _, closeRuntime, err := s.sessionRuntimes.acquireControlRuntime(ctx, sessionID, false)
		if err != nil {
			return appserver.CommandResult{SessionID: sessionID}, classifyControlPreDispatchError(err)
		}
		if runtime == nil {
			return appserver.CommandResult{SessionID: sessionID}, classifyControlPreDispatchError(errors.New("gatewayapp: Session configuration Runtime is unavailable"))
		}
		closeControlRuntime = closeRuntime
		composition = &runtime.instance.runtimeComposition
	case controlActionActivatesSessionRuntime(action):
		runtime, _, release, activated, err := s.sessionRuntimes.acquireActivatedControlRuntime(ctx, sessionID)
		if err != nil {
			return appserver.CommandResult{SessionID: sessionID}, classifyControlPreDispatchError(err)
		}
		releaseRuntimeUse = func() { _ = release(context.Background()) }
		composition = &runtime.instance.runtimeComposition
		if activated {
			newlyActivatedRuntime = runtime
		}
	case controlActionTargetsActiveRuntime(action):
		runtime, releaseUse, err := s.sessionRuntimes.acquireLoadedRuntime(sessionID)
		if err != nil {
			return appserver.CommandResult{SessionID: sessionID}, classifyControlPreDispatchError(err)
		}
		if runtime == nil {
			coded := errorcode.New(
				errorcode.Conflict,
				"gatewayapp: active Turn Runtime is unavailable",
			)
			return appserver.CommandResult{SessionID: sessionID},
				appserver.NewOutcomeError(appserver.OutcomeConflicted, coded)
		}
		releaseRuntimeUse = releaseUse
		composition = &runtime.instance.runtimeComposition
	case action == appserver.ActionSessionClose:
		runtime, releaseUse, err := s.sessionRuntimes.acquireLoadedRuntime(sessionID)
		if err != nil {
			return appserver.CommandResult{SessionID: sessionID}, classifyControlPreDispatchError(err)
		}
		if runtime != nil {
			releaseRuntimeUse = releaseUse
			composition = &runtime.instance.runtimeComposition
		} else if _, err := s.composition.sessions.Session(ctx, session.SessionRef{SessionID: sessionID}); err != nil {
			return appserver.CommandResult{SessionID: sessionID}, classifyControlPreDispatchError(err)
		}
	}
	result, commandErr = composition.executeControlCommand(ctx, principal, action, request)
	if newlyActivatedRuntime != nil && controlCommandProvesNoEffect(commandErr) {
		if releaseRuntimeUse != nil {
			releaseRuntimeUse()
			releaseRuntimeUse = nil
		}
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlFeedPublishTimeout)
		cleanupErr := s.sessionRuntimes.releaseRejectedActivation(releaseCtx, newlyActivatedRuntime)
		cancel()
		if cleanupErr != nil {
			commandErr = errors.Join(
				commandErr,
				errorcode.Wrap(errorcode.Internal, "gatewayapp: discard rejected Session Runtime activation", cleanupErr),
			)
		}
	}
	if action == appserver.ActionSessionClose &&
		commandErr == nil &&
		result.Outcome == appserver.OutcomeCommitted {
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

func isHostAgentCommandRequest(request any) bool {
	switch request.(type) {
	case appserver.BindAgentBindingRequest,
		appserver.ResetAgentBindingRequest,
		appserver.CreateAgentRoleRequest,
		appserver.DeleteAgentRoleRequest,
		appserver.AgentBindingSetRequest,
		appserver.PrepareACPRequest,
		appserver.PrepareACPAuthenticationRequest,
		appserver.ConnectACPRequest,
		appserver.DisconnectACPRequest:
		return true
	default:
		return false
	}
}

func isHostConfigurationCommandRequest(request any) bool {
	switch request.(type) {
	case appserver.ConnectModelRequest,
		appserver.UseModelRequest,
		appserver.DeleteModelRequest,
		appserver.SandboxRequest:
		return true
	default:
		return false
	}
}

func controlCommandProvesNoEffect(err error) bool {
	var outcomeErr *appserver.OutcomeError
	if !errors.As(err, &outcomeErr) {
		return false
	}
	return outcomeErr.Outcome == appserver.OutcomeRejected ||
		outcomeErr.Outcome == appserver.OutcomeConflicted
}

func (s *runtimeComposition) executeControlCommand(ctx context.Context, principal appserver.Principal, action appserver.Action, request any) (result appserver.CommandResult, commandErr error) {
	if s == nil {
		return appserver.CommandResult{}, errors.New("gatewayapp: stack is unavailable")
	}
	if s.isClosing() {
		return appserver.CommandResult{Outcome: appserver.OutcomeRejected},
			appserver.NewOutcomeError(
				appserver.OutcomeRejected,
				errorcode.New(errorcode.Unavailable, "gatewayapp: host is closing"),
			)
	}
	gw := s.currentGateway()
	if gw == nil {
		return appserver.CommandResult{}, errors.New("gatewayapp: gateway is unavailable")
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
	case appserver.SessionModeRequest,
		appserver.SessionModelRequest,
		appserver.SessionControllerModeRequest,
		appserver.SessionPresentationModeRequest,
		appserver.SessionPresentationConfigRequest:
		return s.executeSessionConfigurationCommand(ctx, action, req)
	case appserver.CreateSessionRequest:
		created, err := s.sessions.StartSession(ctx, session.StartSessionRequest{
			AppName: s.appName, UserID: strings.TrimSpace(principal.ID),
			Workspace:          session.WorkspaceRef{Key: strings.TrimSpace(req.WorkspaceKey), CWD: strings.TrimSpace(req.CWD)},
			PreferredSessionID: strings.TrimSpace(req.PreferredSessionID), Title: strings.TrimSpace(req.Title), Metadata: req.Metadata,
		})
		return sessionCommandResult(created), classifyControlBackendError(err)
	case appserver.CloseSessionRequest:
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
		active, err = s.sessions.Session(ctx, active.SessionRef)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		updated, err := appserver.CloseSession(ctx, s.sessions, active, "closed by control client")
		if err == nil || session.IsCommitted(err) {
			gw.CloseSessionApprovals(active.SessionRef, "session_closed")
		}
		return sessionCommandResult(updated), classifyControlBackendError(err)
	case appserver.PromptRequest:
		active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		result, err := gw.BeginTurn(ctx, kernelimpl.BeginTurnRequest{
			SessionRef:     active.SessionRef,
			RuntimeContext: s.controlRuntimeContext(ctx, active),
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
			out.Target = appserver.TurnTarget{HandleID: result.Handle.HandleID(), RunID: result.Handle.RunID(), TurnID: result.Handle.TurnID()}
		}
		return out, classifyControlBackendError(err)
	case appserver.SteerRequest:
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
	case appserver.CompactSessionRequest:
		active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		err = s.CompactSession(ctx, active.SessionRef)
		return sessionCommandResult(active), classifyControlBackendError(err)
	case appserver.CancelRequest:
		active, err := s.checkControlTurnTarget(ctx, req.WriteBase, req.Target)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		err = gw.Interrupt(ctx, kernelimpl.InterruptRequest{
			SessionRef: active.SessionRef, Reason: req.Reason,
			HandleID: req.Target.HandleID, RunID: req.Target.RunID, TurnID: req.Target.TurnID,
		})
		return sessionCommandResult(active), classifyControlBackendError(err)
	case appserver.ResolveApprovalRequest:
		active, err := s.checkControlApprovalTarget(ctx, req.WriteBase, req.Target, req.ApprovalRequestID)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		err = gw.SubmitActiveTurn(ctx, kernelimpl.SubmitActiveTurnRequest{SessionRef: active.SessionRef, Kind: kernelimpl.SubmissionKindApproval, Approval: &kernelimpl.ApprovalDecision{
			RequestID: eventstream.ApprovalRequestID(req.ApprovalRequestID), Outcome: req.Outcome, OptionID: req.OptionID,
			Approved: req.Approved, Reason: req.Reason, ReviewText: req.ReviewText,
		}})
		return sessionCommandResult(active), classifyControlBackendError(err)
	case appserver.AttachParticipantRequest:
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
	case appserver.StartParticipantRequest:
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
			RuntimeContext: s.controlRuntimeContext(ctx, active),
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
			out.Target = appserver.TurnTarget{HandleID: started.Handle.HandleID(), RunID: started.Handle.RunID(), TurnID: started.Handle.TurnID()}
		}
		return out, classifyControlBackendError(err)
	case appserver.PromptParticipantRequest:
		active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		result, err := gw.PromptParticipant(ctx, kernelimpl.PromptParticipantRequest{
			SessionRef:     active.SessionRef,
			RuntimeContext: s.controlRuntimeContext(ctx, active),
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
			out.Target = appserver.TurnTarget{HandleID: result.Handle.HandleID(), RunID: result.Handle.RunID(), TurnID: result.Handle.TurnID()}
		}
		return out, classifyControlBackendError(err)
	case appserver.CancelParticipantRequest:
		active, err := s.checkControlTurnTarget(ctx, req.WriteBase, req.Target)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		turn, ok := gw.ActiveTurn(active.SessionID)
		if !ok || turn.Kind != kernelimpl.ActiveTurnKindParticipant || strings.TrimSpace(turn.ParticipantID) != strings.TrimSpace(req.ParticipantID) {
			return sessionCommandResult(active), appserver.NewOutcomeError(appserver.OutcomeConflicted, errors.New("controlclient: active turn is not the requested participant turn"))
		}
		err = gw.Interrupt(ctx, kernelimpl.InterruptRequest{
			SessionRef: active.SessionRef, Reason: req.Reason,
			HandleID: req.Target.HandleID, RunID: req.Target.RunID, TurnID: req.Target.TurnID,
			Kind: kernelimpl.ActiveTurnKindParticipant, ParticipantID: req.ParticipantID,
		})
		return sessionCommandResult(active), classifyControlBackendError(err)
	case appserver.DetachParticipantRequest:
		active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		updated, err := gw.DetachParticipant(ctx, kernelimpl.DetachParticipantRequest{SessionRef: active.SessionRef, ParticipantID: req.ParticipantID, Source: req.Source})
		return sessionCommandResult(updated), classifyControlBackendError(err)
	case appserver.HandoffRequest:
		active, err := s.checkControlCommandCAS(ctx, req.WriteBase)
		if err != nil {
			return sessionCommandResult(active), classifyControlBackendError(err)
		}
		updated, err := gw.HandoffController(ctx, kernelimpl.HandoffControllerRequest{
			SessionRef:              active.SessionRef,
			ExpectedRevision:        req.ExpectedRevision,
			ExpectedControllerEpoch: req.ExpectedControllerEpoch,
			Kind:                    req.Kind,
			Agent:                   req.Agent,
			Source:                  req.Source,
			Reason:                  req.Reason,
		})
		return sessionCommandResult(updated), classifyControlBackendError(err)
	default:
		return appserver.CommandResult{}, fmt.Errorf("gatewayapp: unsupported control command %q (%T)", action, request)
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
	case appserver.CloseSessionRequest:
		return strings.TrimSpace(typed.SessionID)
	case appserver.CompactSessionRequest:
		return strings.TrimSpace(typed.SessionID)
	case appserver.PromptRequest:
		return strings.TrimSpace(typed.SessionID)
	case appserver.SteerRequest:
		return strings.TrimSpace(typed.SessionID)
	case appserver.CancelRequest:
		return strings.TrimSpace(typed.SessionID)
	case appserver.ResolveApprovalRequest:
		return strings.TrimSpace(typed.SessionID)
	case appserver.AttachParticipantRequest:
		return strings.TrimSpace(typed.SessionID)
	case appserver.StartParticipantRequest:
		return strings.TrimSpace(typed.SessionID)
	case appserver.PromptParticipantRequest:
		return strings.TrimSpace(typed.SessionID)
	case appserver.CancelParticipantRequest:
		return strings.TrimSpace(typed.SessionID)
	case appserver.DetachParticipantRequest:
		return strings.TrimSpace(typed.SessionID)
	case appserver.HandoffRequest:
		return strings.TrimSpace(typed.SessionID)
	case appserver.SessionModeRequest:
		return strings.TrimSpace(typed.SessionID)
	case appserver.SessionModelRequest:
		return strings.TrimSpace(typed.SessionID)
	case appserver.SessionControllerModeRequest:
		return strings.TrimSpace(typed.SessionID)
	case appserver.SessionPresentationModeRequest:
		return strings.TrimSpace(typed.SessionID)
	case appserver.SessionPresentationConfigRequest:
		return strings.TrimSpace(typed.SessionID)
	default:
		return ""
	}
}

func controlActionConfiguresSession(action appserver.Action) bool {
	switch action {
	case appserver.ActionSessionApprovalMode,
		appserver.ActionSessionModel,
		appserver.ActionSessionControllerMode,
		appserver.ActionSessionPresentationMode,
		appserver.ActionSessionPresentationConfig:
		return true
	default:
		return false
	}
}

func controlActionActivatesSessionRuntime(action appserver.Action) bool {
	switch action {
	case appserver.ActionPrompt,
		appserver.ActionSessionCompact,
		appserver.ActionParticipantAttach,
		appserver.ActionParticipantStart,
		appserver.ActionParticipantPrompt,
		appserver.ActionParticipantDetach,
		appserver.ActionControllerHandoff:
		return true
	default:
		return false
	}
}

func controlActionTargetsActiveRuntime(action appserver.Action) bool {
	switch action {
	case appserver.ActionSteer,
		appserver.ActionCancel,
		appserver.ActionApprovalResolve,
		appserver.ActionParticipantCancel:
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
	return appserver.NewOutcomeError(appserver.OutcomeRejected, err)
}

func (s *runtimeComposition) controlRuntimeContext(fallback context.Context, active session.Session) context.Context {
	runtimeCtx := fallback
	if s != nil && s.lifecycleCtx != nil {
		runtimeCtx = s.lifecycleCtx
	}
	// Accepted Control turns outlive their admission request, so their
	// cancellation parent is the Stack lifecycle. Preserve only the negotiated
	// Agent-message transport from the request context: it is a trusted host
	// capability required by ACP child turns, not arbitrary request state.
	if sender := agentmessage.SenderFromContext(fallback); sender != nil {
		runtimeCtx = agentmessage.WithSender(runtimeCtx, sender)
		return runtimeCtx
	}
	// Hosted child turns execute on a detached Session Runtime. Bind the
	// Host-owned parent/sibling mailbox so SendMessage does not treat that
	// child as the main Agent.
	if sender := s.hostedChildMessageSender(active); sender != nil {
		runtimeCtx = agentmessage.WithSender(runtimeCtx, sender)
	}
	return runtimeCtx
}

func (s *runtimeComposition) resolveControlParticipantPlacement(ctx context.Context, profileID, effort string) (sdkplacement.Placement, error) {
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
			return sdkplacement.Placement{}, appserver.NewOutcomeError(appserver.OutcomeRejected, coded)
		}
		return sdkplacement.Placement{}, err
	}
	return resolved, nil
}

func (s *runtimeComposition) resolveControlHandlePlacement(ctx context.Context, handle agentbinding.Handle) (sdkplacement.Placement, error) {
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
	return sdkplacement.Placement{}, appserver.NewOutcomeError(appserver.OutcomeRejected, coded)
}

func (s *runtimeComposition) checkControlCommandCAS(ctx context.Context, base appserver.WriteBase) (session.Session, error) {
	return s.checkControlCommandCASMode(ctx, base, false)
}

func (s *runtimeComposition) checkControlCommandCASAllowClosed(ctx context.Context, base appserver.WriteBase) (session.Session, error) {
	return s.checkControlCommandCASMode(ctx, base, true)
}

func (s *runtimeComposition) checkControlCommandCASMode(ctx context.Context, base appserver.WriteBase, allowClosed bool) (session.Session, error) {
	active, err := s.sessions.Session(ctx, session.SessionRef{SessionID: strings.TrimSpace(base.SessionID)})
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
		closed, err := appserver.IsSessionClosed(ctx, s.sessions, active.SessionRef)
		if err != nil {
			return active, err
		}
		if closed {
			return active, appserver.ErrSessionClosed
		}
	}
	return active, nil
}

func (s *runtimeComposition) checkControlTurnTarget(ctx context.Context, base appserver.WriteBase, target appserver.TurnTarget) (session.Session, error) {
	active, err := s.checkControlCommandCAS(ctx, base)
	if err != nil {
		return active, err
	}
	turn, ok := s.currentGateway().ActiveTurn(active.SessionID)
	if !ok || turn.HandleID != strings.TrimSpace(target.HandleID) || turn.RunID != strings.TrimSpace(target.RunID) || turn.TurnID != strings.TrimSpace(target.TurnID) {
		return active, appserver.NewOutcomeError(appserver.OutcomeConflicted, errors.New("controlclient: live turn target changed"))
	}
	return active, nil
}

func (s *runtimeComposition) checkControlApprovalTarget(ctx context.Context, base appserver.WriteBase, target appserver.TurnTarget, requestID string) (session.Session, error) {
	active, err := s.checkControlCommandCAS(ctx, base)
	if err != nil {
		return active, err
	}
	turn, ok := s.currentGateway().ApprovalTarget(active.SessionID, eventstream.ApprovalRequestID(strings.TrimSpace(requestID)))
	if !ok || turn.HandleID != strings.TrimSpace(target.HandleID) || turn.RunID != strings.TrimSpace(target.RunID) || turn.TurnID != strings.TrimSpace(target.TurnID) {
		return active, appserver.NewOutcomeError(appserver.OutcomeConflicted, errors.New("controlclient: approval turn target changed"))
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
			return appserver.NewOutcomeError(appserver.OutcomeConflicted, errors.New("controlclient: another turn started while closing the session"))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func sessionCommandResult(active session.Session) appserver.CommandResult {
	return appserver.CommandResult{Outcome: appserver.OutcomeCommitted, SessionID: active.SessionID, Revision: active.Revision}
}

func classifyControlBackendError(err error) error {
	if err == nil {
		return nil
	}
	var outcomeErr *appserver.OutcomeError
	if errors.As(err, &outcomeErr) {
		return err
	}
	var gatewayErr *kernelimpl.Error
	if errors.As(err, &gatewayErr) {
		switch gatewayErr.Kind {
		case kernelimpl.KindValidation:
			coded := errorcode.Wrap(errorcode.InvalidArgument, gatewayErr.Error(), err)
			return appserver.NewOutcomeError(appserver.OutcomeRejected, coded)
		case kernelimpl.KindConflict, kernelimpl.KindApproval:
			coded := errorcode.Wrap(errorcode.Conflict, "gatewayapp: command conflict", err)
			return appserver.NewOutcomeError(appserver.OutcomeConflicted, coded)
		case kernelimpl.KindUnavailable:
			coded := errorcode.Wrap(errorcode.Unavailable, gatewayErr.Error(), err)
			return appserver.NewOutcomeError(appserver.OutcomeRejected, coded)
		}
	}
	if errors.Is(err, session.ErrRevisionConflict) || errors.Is(err, session.ErrLeaseConflict) {
		coded := errorcode.Wrap(errorcode.Conflict, "gatewayapp: session conflict", err)
		return appserver.NewOutcomeError(appserver.OutcomeConflicted, coded)
	}
	if session.IsCommitted(err) {
		return nil
	}
	// Only an explicitly typed rejected error proves that no effect committed.
	// Ordinary backend failures remain unknown so the operation ledger cannot
	// expire their idempotency guard and replay a possible external effect.
	return appserver.NewOutcomeError(appserver.OutcomeUnknown, err)
}
