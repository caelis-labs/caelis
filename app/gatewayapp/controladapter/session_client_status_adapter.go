package controladapter

import (
	"context"
	"errors"
	"strings"

	controlclient "github.com/caelis-labs/caelis/control/client"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/google/uuid"
)

// Status uses the addressed Session projection exposed by AppServer.
func (a *SessionClientAdapter) Status(ctx context.Context) (controlstatus.StatusSnapshot, error) {
	if a == nil || a.statusClient == nil {
		return controlstatus.StatusSnapshot{}, errors.New("app/gatewayapp/controladapter: status client is unavailable")
	}
	return a.sessionStatus(ctx, true)
}

// LightweightStatus avoids expensive diagnostics for prompt-bar refreshes.
func (a *SessionClientAdapter) LightweightStatus(ctx context.Context) (controlstatus.StatusSnapshot, error) {
	if a == nil || a.statusClient == nil {
		return controlstatus.StatusSnapshot{}, errors.New("app/gatewayapp/controladapter: status client is unavailable")
	}
	return a.sessionStatus(ctx, false)
}

func (a *SessionClientAdapter) sessionStatus(ctx context.Context, diagnostics bool) (controlstatus.StatusSnapshot, error) {
	state, err := a.activeClientSessionState(ctx)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	status, err := a.statusClient.SessionStatus(ctx, controlclient.StatusRequest{
		SessionID:          state.SessionID,
		Surface:            a.surface,
		IncludeDiagnostics: diagnostics,
	})
	if err == nil && strings.TrimSpace(state.CWD) != "" {
		a.setClientSession(state.SessionID, state.CWD)
	}
	return status, err
}

// WorkspaceDir returns the last typed Session workspace snapshot. The value is
// refreshed by status/inspect and never resolved through a default Stack.
func (a *SessionClientAdapter) WorkspaceDir() string {
	if a == nil {
		return ""
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	return a.workspaceDir
}

func (a *SessionClientAdapter) clientSessionID() string {
	if a == nil {
		return ""
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	return a.sessionID
}

func (a *SessionClientAdapter) setClientSession(sessionID, cwd string) {
	if a == nil {
		return
	}
	a.sessionMu.Lock()
	a.sessionID = strings.TrimSpace(sessionID)
	if strings.TrimSpace(cwd) != "" {
		a.workspaceDir = strings.TrimSpace(cwd)
	}
	a.sessionMu.Unlock()
}

func (a *SessionClientAdapter) ensureClientSession(ctx context.Context) (controlclient.SessionState, error) {
	if a == nil || a.sessionClient == nil {
		return controlclient.SessionState{}, errors.New("app/gatewayapp/controladapter: Session client is unavailable")
	}
	if sessionID := a.clientSessionID(); sessionID != "" {
		return a.sessionClient.InspectSession(ctx, controlclient.StateRequest{SessionID: sessionID})
	}
	a.sessionChangeMu.Lock()
	defer a.sessionChangeMu.Unlock()
	if sessionID := a.clientSessionID(); sessionID != "" {
		return a.sessionClient.InspectSession(ctx, controlclient.StateRequest{SessionID: sessionID})
	}
	result, err := a.sessionClient.CreateSession(ctx, controlclient.CreateSessionRequest{
		WriteBase:          controlclient.WriteBase{OperationID: "session-create-" + uuid.NewString()},
		PreferredSessionID: strings.TrimSpace(a.preferredID),
		WorkspaceKey:       strings.TrimSpace(a.workspaceKey),
		CWD:                strings.TrimSpace(a.workspaceDir),
		Metadata:           map[string]any{"surface": strings.TrimSpace(a.surface)},
	})
	if err != nil {
		return controlclient.SessionState{}, err
	}
	if strings.TrimSpace(result.SessionID) == "" {
		return controlclient.SessionState{}, errors.New("app/gatewayapp/controladapter: create Session returned no Session ID")
	}
	state, err := a.sessionClient.InspectSession(ctx, controlclient.StateRequest{SessionID: result.SessionID})
	if err != nil {
		return controlclient.SessionState{}, err
	}
	a.preferredID = ""
	a.setClientSession(state.SessionID, state.CWD)
	return state, nil
}
