package controladapter

import (
	"context"
	"errors"
	"strings"

	controlclient "github.com/caelis-labs/caelis/control/client"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func (a *SessionClientAdapter) CycleSessionMode(ctx context.Context) (controlstatus.StatusSnapshot, error) {
	return a.configureSessionMode(ctx, "", true)
}

func (a *SessionClientAdapter) SetSessionMode(ctx context.Context, mode string) (controlstatus.StatusSnapshot, error) {
	return a.configureSessionMode(ctx, mode, false)
}

func (a *SessionClientAdapter) configureSessionMode(ctx context.Context, mode string, cycle bool) (controlstatus.StatusSnapshot, error) {
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	return a.configClient.ConfigureSessionMode(ctx, controlclient.SessionModeRequest{
		SessionID: state.SessionID, Mode: strings.TrimSpace(mode), Cycle: cycle, Surface: a.surface,
	})
}

func (a *SessionClientAdapter) Connect(ctx context.Context, config controlprompt.ConnectConfig) (controlstatus.StatusSnapshot, error) {
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	return a.configClient.ConnectModel(ctx, controlclient.ConnectModelRequest{
		SessionID: state.SessionID, Surface: a.surface, Config: config,
	})
}

func (a *SessionClientAdapter) UseModel(ctx context.Context, model string, effort ...string) (controlstatus.StatusSnapshot, error) {
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	reasoning := ""
	if len(effort) > 0 {
		reasoning = strings.TrimSpace(effort[0])
	}
	return a.configClient.UseModel(ctx, controlclient.UseModelRequest{
		SessionID: state.SessionID, Surface: a.surface, Model: strings.TrimSpace(model), ReasoningEffort: reasoning,
	})
}

func (a *SessionClientAdapter) DeleteModel(ctx context.Context, model string) error {
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return err
	}
	return a.configClient.DeleteModel(ctx, controlclient.DeleteModelRequest{
		SessionID: state.SessionID, Surface: a.surface, Model: strings.TrimSpace(model),
	})
}

func (a *SessionClientAdapter) SetSandboxBackend(ctx context.Context, backend string) (controlstatus.StatusSnapshot, error) {
	return a.configureSandbox(ctx, "set", backend)
}

func (a *SessionClientAdapter) PrepareSandbox(ctx context.Context) (controlstatus.StatusSnapshot, error) {
	return a.configureSandbox(ctx, "prepare", "")
}

func (a *SessionClientAdapter) RepairSandbox(ctx context.Context) (controlstatus.StatusSnapshot, error) {
	return a.configureSandbox(ctx, "repair", "")
}

// RefreshSandbox runs the host-owned background refresh through the typed
// configuration capability after authorizing the active Session.
func (a *SessionClientAdapter) RefreshSandbox(ctx context.Context) error {
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return err
	}
	return a.configClient.RefreshSandbox(ctx, controlclient.SandboxRequest{
		SessionID: state.SessionID, Surface: a.surface,
	})
}

func (a *SessionClientAdapter) configureSandbox(ctx context.Context, action, backend string) (controlstatus.StatusSnapshot, error) {
	if a == nil || a.configClient == nil {
		return controlstatus.StatusSnapshot{}, errors.New("app/gatewayapp/controladapter: configuration client is unavailable")
	}
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	request := controlclient.SandboxRequest{SessionID: state.SessionID, Surface: a.surface, Backend: strings.TrimSpace(backend)}
	switch action {
	case "set":
		return a.configClient.SetSandboxBackend(ctx, request)
	case "prepare":
		return a.configClient.PrepareSandbox(ctx, request)
	case "repair":
		return a.configClient.RepairSandbox(ctx, request)
	default:
		return controlstatus.StatusSnapshot{}, errors.New("app/gatewayapp/controladapter: unknown sandbox action")
	}
}
