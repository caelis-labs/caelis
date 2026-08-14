package controlclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (s *CommandService) ConfigureSessionMode(ctx context.Context, principal Principal, req SessionModeRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionSessionApprovalMode, req.WriteBase, "session/configuration/approval-mode", req)
}

func (s *CommandService) UseSessionModel(ctx context.Context, principal Principal, req SessionModelRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionSessionModel, req.WriteBase, "session/configuration/model", req)
}

func (s *CommandService) ConfigureSessionControllerMode(ctx context.Context, principal Principal, req SessionControllerModeRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionSessionControllerMode, req.WriteBase, "session/configuration/controller-mode", req)
}

func (s *CommandService) ConfigureSessionPresentationMode(ctx context.Context, principal Principal, req SessionPresentationModeRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionSessionPresentationMode, req.WriteBase, "session/configuration/presentation-mode", req)
}

func (s *CommandService) ConfigureSessionPresentation(ctx context.Context, principal Principal, req SessionPresentationConfigRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionSessionPresentationConfig, req.WriteBase, "session/configuration/presentation-config", req)
}

func (s *CommandService) ConnectModel(ctx context.Context, principal Principal, req ConnectModelRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionModelConnect, req.WriteBase, "host/configuration/model-catalog", req)
}

func (s *CommandService) UseModel(ctx context.Context, principal Principal, req UseModelRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionModelUse, req.WriteBase, "host/configuration/model-default", req)
}

func (s *CommandService) DeleteModel(ctx context.Context, principal Principal, req DeleteModelRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionModelDelete, req.WriteBase, "host/configuration/model-catalog", req)
}

func (s *CommandService) SetSandboxBackend(ctx context.Context, principal Principal, req SandboxRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionSandboxBackend, req.WriteBase, "host/configuration/sandbox-backend", req)
}

func (s *CommandService) PrepareSandbox(ctx context.Context, principal Principal, req SandboxRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionSandboxPrepare, req.WriteBase, "host/configuration/sandbox-lifecycle/prepare", req)
}

func (s *CommandService) RepairSandbox(ctx context.Context, principal Principal, req SandboxRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionSandboxRepair, req.WriteBase, "host/configuration/sandbox-lifecycle/repair", req)
}

func (s *CommandService) ResetSandbox(ctx context.Context, principal Principal, req SandboxRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionSandboxReset, req.WriteBase, "host/configuration/sandbox-lifecycle/reset", req)
}

func (s *CommandService) RefreshSandbox(ctx context.Context, principal Principal, req SandboxRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionSandboxRefresh, req.WriteBase, "host/configuration/sandbox-lifecycle/refresh", req)
}

func validateSandboxCommandRequest(action Action, req SandboxRequest) error {
	switch action {
	case ActionSandboxBackend, ActionSandboxPrepare, ActionSandboxRepair, ActionSandboxReset, ActionSandboxRefresh:
	default:
		return fmt.Errorf("controlclient: unsupported sandbox action %q", action)
	}
	if strings.TrimSpace(req.SessionID) != "" {
		return errors.New("controlclient: Host sandbox mutation must not address a Session")
	}
	if req.ExpectedRevision == nil {
		return errors.New("controlclient: Host sandbox mutation expected_revision is required")
	}
	if strings.TrimSpace(req.ExpectedControllerEpoch) != "" {
		return errors.New("controlclient: Host sandbox mutation must not address a controller epoch")
	}
	if action == ActionSandboxBackend && strings.TrimSpace(req.Backend) == "" {
		return errors.New("controlclient: sandbox backend is required")
	}
	if action != ActionSandboxBackend && strings.TrimSpace(req.Backend) != "" {
		return errors.New("controlclient: sandbox lifecycle mutation must not include a backend")
	}
	return nil
}

func validateHostModelWrite(base WriteBase) error {
	if strings.TrimSpace(base.SessionID) != "" {
		return errors.New("controlclient: Host model mutation must not address a Session")
	}
	if base.ExpectedRevision == nil {
		return errors.New("controlclient: Host model mutation expected_revision is required")
	}
	if strings.TrimSpace(base.ExpectedControllerEpoch) != "" {
		return errors.New("controlclient: Host model mutation must not address a controller epoch")
	}
	return nil
}

func validateConnectModelRequest(req ConnectModelRequest) error {
	if err := validateHostModelWrite(req.WriteBase); err != nil {
		return err
	}
	if strings.TrimSpace(req.Config.Provider) == "" || strings.TrimSpace(req.Config.Model) == "" {
		return errors.New("controlclient: model provider and model are required")
	}
	return nil
}

func validateUseModelRequest(req UseModelRequest) error {
	if err := validateHostModelWrite(req.WriteBase); err != nil {
		return err
	}
	if strings.TrimSpace(req.Model) == "" {
		return errors.New("controlclient: model is required")
	}
	return nil
}

func validateDeleteModelRequest(req DeleteModelRequest) error {
	if err := validateHostModelWrite(req.WriteBase); err != nil {
		return err
	}
	if strings.TrimSpace(req.Model) == "" {
		return errors.New("controlclient: model is required")
	}
	return nil
}

func validateSessionConfigurationWrite(base WriteBase) error {
	if strings.TrimSpace(base.SessionID) == "" {
		return errors.New("controlclient: Session configuration requires a Session ID")
	}
	if base.ExpectedRevision == nil {
		return errors.New("controlclient: Session configuration expected_revision is required")
	}
	return nil
}

func validateSessionModeRequest(req SessionModeRequest) error {
	if err := validateSessionConfigurationWrite(req.WriteBase); err != nil {
		return err
	}
	if strings.TrimSpace(req.Mode) == "" {
		return errors.New("controlclient: Session mode is required")
	}
	return nil
}

func validateSessionModelRequest(req SessionModelRequest) error {
	if err := validateSessionConfigurationWrite(req.WriteBase); err != nil {
		return err
	}
	if strings.TrimSpace(req.Model) == "" {
		return errors.New("controlclient: Session model is required")
	}
	return nil
}

func validateSessionControllerModeRequest(req SessionControllerModeRequest) error {
	if err := validateSessionConfigurationWrite(req.WriteBase); err != nil {
		return err
	}
	if strings.TrimSpace(req.ExpectedControllerEpoch) == "" {
		return errors.New("controlclient: external controller mode requires expected_controller_epoch")
	}
	if strings.TrimSpace(req.Mode) == "" {
		return errors.New("controlclient: external controller mode is required")
	}
	return nil
}

func validateSessionPresentationModeRequest(req SessionPresentationModeRequest) error {
	if err := validateSessionConfigurationWrite(req.WriteBase); err != nil {
		return err
	}
	if strings.TrimSpace(req.Mode) == "" {
		return errors.New("controlclient: Session presentation mode is required")
	}
	return nil
}

func validateSessionPresentationConfigRequest(req SessionPresentationConfigRequest) error {
	if err := validateSessionConfigurationWrite(req.WriteBase); err != nil {
		return err
	}
	if strings.TrimSpace(req.ConfigID) == "" || strings.TrimSpace(req.Value) == "" {
		return errors.New("controlclient: Session presentation config ID and value are required")
	}
	return nil
}

var _ ConfigurationCommandService = (*CommandService)(nil)
