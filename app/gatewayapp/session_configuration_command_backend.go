package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/control/modelcatalog"
	controller "github.com/caelis-labs/caelis/internal/acpagentbridge/controller"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/kernel"
)

const (
	stateControllerConfigEpoch       = "gateway.controller_config_epoch"
	stateControllerConfigModel       = "gateway.controller_config_model"
	stateControllerConfigReasoning   = "gateway.controller_config_reasoning_effort"
	stateControllerConfigMode        = "gateway.controller_config_mode"
	stateControllerConfigOperationID = "gateway.controller_config_operation_id"
)

func (s *Stack) executeSessionConfigurationCommand(
	ctx context.Context,
	action controlclient.Action,
	request any,
) (controlclient.CommandResult, error) {
	switch req := request.(type) {
	case controlclient.SessionModeRequest:
		if action != controlclient.ActionSessionApprovalMode {
			return sessionCommandResult(session.Session{}), sessionConfigurationRejected("unexpected approval-mode action")
		}
		return s.configureSessionApprovalMode(ctx, req)
	case controlclient.SessionModelRequest:
		if action != controlclient.ActionSessionModel {
			return sessionCommandResult(session.Session{}), sessionConfigurationRejected("unexpected model action")
		}
		return s.configureSessionModel(ctx, req)
	case controlclient.SessionControllerModeRequest:
		if action != controlclient.ActionSessionControllerMode {
			return sessionCommandResult(session.Session{}), sessionConfigurationRejected("unexpected controller-mode action")
		}
		return s.configureSessionControllerMode(ctx, req)
	case controlclient.SessionPresentationModeRequest:
		if action != controlclient.ActionSessionPresentationMode {
			return sessionCommandResult(session.Session{}), sessionConfigurationRejected("unexpected presentation-mode action")
		}
		return s.configureSessionPresentationMode(ctx, req)
	case controlclient.SessionPresentationConfigRequest:
		if action != controlclient.ActionSessionPresentationConfig {
			return sessionCommandResult(session.Session{}), sessionConfigurationRejected("unexpected presentation-config action")
		}
		return s.configureSessionPresentation(ctx, req)
	default:
		return controlclient.CommandResult{}, sessionConfigurationRejected("invalid Session configuration request")
	}
}

func (s *Stack) configureSessionControllerMode(ctx context.Context, req controlclient.SessionControllerModeRequest) (controlclient.CommandResult, error) {
	active, err := s.checkSessionConfigurationCAS(ctx, req.WriteBase)
	if err != nil {
		return sessionCommandResult(active), classifyControlBackendError(err)
	}
	if err := s.rejectSessionConfigurationDuringTurn(active.SessionID); err != nil {
		return sessionCommandResult(active), sessionConfigurationConflict(err)
	}
	if err := s.processSecurityPosture().validateSessionModeMutation(); err != nil {
		return sessionCommandResult(active), sessionConfigurationRejectedError(err)
	}
	if active.Controller.Kind != session.ControllerKindACP {
		return sessionCommandResult(active), sessionConfigurationRejected("Session is not bound to an external ACP controller")
	}
	if _, found, err := s.ACPControllerStatus(ctx, active.SessionRef); err != nil {
		return sessionCommandResult(active), sessionConfigurationRejectedError(err)
	} else if !found {
		return sessionCommandResult(active), sessionConfigurationRejected("active ACP controller Runtime is unavailable")
	}
	_, err = s.setACPControllerMode(ctx, controller.SetControllerModeRequest{
		SessionRef:              active.SessionRef,
		ExpectedControllerEpoch: req.ExpectedControllerEpoch,
		Mode:                    strings.TrimSpace(req.Mode),
	})
	if err != nil {
		if controller.ConfigurationEffectStarted(err) {
			return sessionCommandResult(active), controlclient.NewOutcomeError(controlclient.OutcomeUnknown, err)
		}
		return sessionCommandResult(active), sessionConfigurationRejectedError(err)
	}
	updated, stateErr := s.updateSessionStateAtRevision(ctx, active.SessionRef, active.Revision, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next[stateControllerConfigEpoch] = strings.TrimSpace(active.Controller.EpochID)
		next[stateControllerConfigMode] = strings.TrimSpace(req.Mode)
		next[stateControllerConfigOperationID] = strings.TrimSpace(req.OperationID)
		return next, nil
	})
	return sessionCommandResult(updated), classifyACPSelectionStateError("mode", stateErr)
}

func (s *Stack) configureSessionApprovalMode(ctx context.Context, req controlclient.SessionModeRequest) (controlclient.CommandResult, error) {
	active, err := s.checkSessionConfigurationCAS(ctx, req.WriteBase)
	if err != nil {
		return sessionCommandResult(active), classifyControlBackendError(err)
	}
	if err := s.rejectSessionConfigurationDuringTurn(active.SessionID); err != nil {
		return sessionCommandResult(active), sessionConfigurationConflict(err)
	}
	if err := s.processSecurityPosture().validateSessionModeMutation(); err != nil {
		return sessionCommandResult(active), sessionConfigurationRejectedError(err)
	}
	mode, err := normalizeSessionMode(req.Mode)
	if err != nil {
		return sessionCommandResult(active), sessionConfigurationRejectedError(err)
	}
	updated, err := s.updateSessionStateAtRevision(ctx, active.SessionRef, active.Revision, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next[kernel.StateCurrentApprovalMode] = mode
		return next, nil
	})
	return sessionCommandResult(updated), classifyControlBackendError(err)
}

func (s *Stack) configureSessionModel(ctx context.Context, req controlclient.SessionModelRequest) (controlclient.CommandResult, error) {
	active, err := s.checkSessionConfigurationCAS(ctx, req.WriteBase)
	if err != nil {
		return sessionCommandResult(active), classifyControlBackendError(err)
	}
	if err := s.rejectSessionConfigurationDuringTurn(active.SessionID); err != nil {
		return sessionCommandResult(active), sessionConfigurationConflict(err)
	}
	if active.Controller.Kind == session.ControllerKindACP {
		return s.configureACPControllerModel(ctx, active, req)
	}
	catalog := s.lookup
	if s.modelCatalog != nil {
		catalog = s.modelCatalog
	}
	if catalog == nil || s.lookup == nil {
		return sessionCommandResult(active), sessionConfigurationRejected("model catalog is unavailable")
	}
	configured, err := catalog.ResolveConfig(req.Model)
	if err != nil {
		return sessionCommandResult(active), sessionConfigurationRejectedError(err)
	}
	reasoning := modelcatalog.NormalizeReasoningEffort(req.ReasoningEffort)
	if strings.TrimSpace(req.ReasoningEffort) != "" && reasoning == "" {
		return sessionCommandResult(active), sessionConfigurationRejected("reasoning effort is invalid")
	}
	if reasoning != "" && !modelConfigSupportsReasoningEffort(configured, reasoning) {
		return sessionCommandResult(active), sessionConfigurationRejected(fmt.Sprintf("model %q does not support reasoning effort %q", configured.ID, reasoning))
	}
	// The App catalog decides whether a model may be selected. Once accepted,
	// pin its hydrated configuration into this Runtime so a later App deletion
	// cannot interrupt the active Session.
	var finishPin func(bool)
	if s.modelCatalog != nil {
		finishPin, err = s.lookup.beginPinnedUpsert(configured)
		if err != nil {
			return sessionCommandResult(active), sessionConfigurationRejectedError(err)
		}
	}
	updated, err := s.updateSessionStateAtRevision(ctx, active.SessionRef, active.Revision, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next[kernel.StateCurrentModelAlias] = configured.ID
		if reasoning == "" {
			delete(next, kernel.StateCurrentReasoningEffort)
		} else {
			next[kernel.StateCurrentReasoningEffort] = reasoning
		}
		return next, nil
	})
	if finishPin != nil {
		finishPin(err == nil || session.IsCommitted(err))
	}
	return sessionCommandResult(updated), classifyControlBackendError(err)
}

func (s *Stack) configureACPControllerModel(
	ctx context.Context,
	active session.Session,
	req controlclient.SessionModelRequest,
) (controlclient.CommandResult, error) {
	status, found, err := s.ACPControllerStatus(ctx, active.SessionRef)
	if err != nil {
		return sessionCommandResult(active), sessionConfigurationRejectedError(err)
	}
	if !found {
		return sessionCommandResult(active), sessionConfigurationRejected("active ACP controller Runtime is unavailable")
	}
	_ = status // The Manager performs option validation immediately before dispatch.
	_, err = s.setACPControllerModel(ctx, controller.SetControllerModelRequest{
		SessionRef:              active.SessionRef,
		ExpectedControllerEpoch: req.ExpectedControllerEpoch,
		Model:                   strings.TrimSpace(req.Model),
		ReasoningEffort:         strings.TrimSpace(req.ReasoningEffort),
	})
	if err != nil {
		if controller.ConfigurationEffectStarted(err) {
			return sessionCommandResult(active), controlclient.NewOutcomeError(controlclient.OutcomeUnknown, err)
		}
		return sessionCommandResult(active), sessionConfigurationRejectedError(err)
	}
	updated, stateErr := s.updateSessionStateAtRevision(ctx, active.SessionRef, active.Revision, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next[stateControllerConfigEpoch] = strings.TrimSpace(active.Controller.EpochID)
		next[stateControllerConfigModel] = strings.TrimSpace(req.Model)
		if effort := strings.TrimSpace(req.ReasoningEffort); effort == "" {
			delete(next, stateControllerConfigReasoning)
		} else {
			next[stateControllerConfigReasoning] = effort
		}
		next[stateControllerConfigOperationID] = strings.TrimSpace(req.OperationID)
		return next, nil
	})
	return sessionCommandResult(updated), classifyACPSelectionStateError("model", stateErr)
}

func classifyACPSelectionStateError(selection string, err error) error {
	if err == nil || session.IsCommitted(err) {
		return nil
	}
	return controlclient.NewOutcomeError(
		controlclient.OutcomeUnknown,
		fmt.Errorf("gatewayapp: ACP %s changed but durable Session selection could not be proven: %w", strings.TrimSpace(selection), err),
	)
}

func (s *Stack) configureSessionPresentationMode(ctx context.Context, req controlclient.SessionPresentationModeRequest) (controlclient.CommandResult, error) {
	active, err := s.checkSessionConfigurationCAS(ctx, req.WriteBase)
	if err != nil {
		return sessionCommandResult(active), classifyControlBackendError(err)
	}
	if err := s.rejectSessionConfigurationDuringTurn(active.SessionID); err != nil {
		return sessionCommandResult(active), sessionConfigurationConflict(err)
	}
	if err := s.processSecurityPosture().validateSessionModeMutation(); err != nil {
		return sessionCommandResult(active), sessionConfigurationRejectedError(err)
	}
	resolved := s.resolvedSessionAssembly()
	mode, ok := assembly.LookupMode(resolved, req.Mode)
	if !ok {
		return sessionCommandResult(active), sessionConfigurationRejected(fmt.Sprintf("presentation mode %q is not declared", strings.TrimSpace(req.Mode)))
	}
	updated, err := s.updateSessionStateAtRevision(ctx, active.SessionRef, active.Revision, func(state map[string]any) (map[string]any, error) {
		return assembly.SetCurrentModeID(state, mode.ID), nil
	})
	return sessionCommandResult(updated), classifyControlBackendError(err)
}

func (s *Stack) configureSessionPresentation(ctx context.Context, req controlclient.SessionPresentationConfigRequest) (controlclient.CommandResult, error) {
	active, err := s.checkSessionConfigurationCAS(ctx, req.WriteBase)
	if err != nil {
		return sessionCommandResult(active), classifyControlBackendError(err)
	}
	if err := s.rejectSessionConfigurationDuringTurn(active.SessionID); err != nil {
		return sessionCommandResult(active), sessionConfigurationConflict(err)
	}
	configID := strings.TrimSpace(req.ConfigID)
	if isReservedPresentationConfigID(configID) {
		return sessionCommandResult(active), sessionConfigurationRejected(fmt.Sprintf("presentation config %q requires its dedicated command", configID))
	}
	resolved := s.resolvedSessionAssembly()
	option, ok := assembly.LookupConfigSelectOption(resolved, configID, req.Value)
	if !ok {
		return sessionCommandResult(active), sessionConfigurationRejected(fmt.Sprintf("presentation config %q value %q is not declared", configID, strings.TrimSpace(req.Value)))
	}
	updated, err := s.updateSessionStateAtRevision(ctx, active.SessionRef, active.Revision, func(state map[string]any) (map[string]any, error) {
		return assembly.SetCurrentConfigValue(state, configID, option.Value), nil
	})
	return sessionCommandResult(updated), classifyControlBackendError(err)
}

func (s *Stack) checkSessionConfigurationCAS(ctx context.Context, base controlclient.WriteBase) (session.Session, error) {
	active, err := s.checkControlCommandCAS(ctx, base)
	if err != nil {
		return active, err
	}
	expectedEpoch := strings.TrimSpace(base.ExpectedControllerEpoch)
	actualEpoch := strings.TrimSpace(active.Controller.EpochID)
	if expectedEpoch != actualEpoch {
		return active, fmt.Errorf(
			"gatewayapp: expected controller epoch %q, actual %q: %w",
			expectedEpoch,
			actualEpoch,
			session.ErrRevisionConflict,
		)
	}
	return active, nil
}

func (s *Stack) rejectSessionConfigurationDuringTurn(sessionID string) error {
	if gateway := s.currentGateway(); gateway != nil {
		if _, ok := gateway.ActiveTurn(strings.TrimSpace(sessionID)); ok {
			return errors.New("gatewayapp: Session configuration cannot change while a Turn is active")
		}
	}
	return nil
}

func (s *Stack) resolvedSessionAssembly() assembly.ResolvedAssembly {
	if s == nil {
		return assembly.ResolvedAssembly{}
	}
	s.mu.RLock()
	resolved := assembly.CloneResolvedAssembly(s.runtime.Assembly)
	s.mu.RUnlock()
	return resolved
}

func (s *Stack) setACPControllerModel(ctx context.Context, req controller.SetControllerModelRequest) (controller.ControllerStatus, error) {
	if s == nil {
		return controller.ControllerStatus{}, errors.New("gatewayapp: runtime engine unavailable")
	}
	s.mu.RLock()
	controlPlane := s.acpControlPlane
	s.mu.RUnlock()
	if controlPlane == nil {
		return controller.ControllerStatus{}, errors.New("gatewayapp: ACP control plane unavailable")
	}
	return controlPlane.SetControllerModel(ctx, req)
}

func (s *Stack) setACPControllerMode(ctx context.Context, req controller.SetControllerModeRequest) (controller.ControllerStatus, error) {
	if s == nil {
		return controller.ControllerStatus{}, errors.New("gatewayapp: runtime engine unavailable")
	}
	s.mu.RLock()
	controlPlane := s.acpControlPlane
	s.mu.RUnlock()
	if controlPlane == nil {
		return controller.ControllerStatus{}, errors.New("gatewayapp: ACP control plane unavailable")
	}
	return controlPlane.SetControllerMode(ctx, req)
}

func isReservedPresentationConfigID(configID string) bool {
	switch strings.ToLower(strings.TrimSpace(configID)) {
	case "mode", "model", "reasoning_effort":
		return true
	default:
		return false
	}
}

func sessionConfigurationRejected(detail string) error {
	return sessionConfigurationRejectedError(errorcode.New(errorcode.InvalidArgument, "gatewayapp: "+strings.TrimSpace(detail)))
}

func sessionConfigurationRejectedError(err error) error {
	if errorcode.CodeOf(err) == errorcode.Unknown {
		err = errorcode.Wrap(errorcode.FailedPrecondition, "gatewayapp: Session configuration rejected", err)
	}
	return controlclient.NewOutcomeError(controlclient.OutcomeRejected, err)
}

func sessionConfigurationConflict(err error) error {
	if errorcode.CodeOf(err) == errorcode.Unknown {
		err = errorcode.Wrap(errorcode.Conflict, "gatewayapp: Session configuration conflict", err)
	}
	return controlclient.NewOutcomeError(controlclient.OutcomeConflicted, err)
}
