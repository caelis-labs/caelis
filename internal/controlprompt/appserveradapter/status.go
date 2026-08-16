package appserveradapter

import (
	"context"
	"errors"
	"strings"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/google/uuid"
)

// Status uses the selected Session projection when available and otherwise
// returns the Host/workspace projection exposed by AppServer.
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
	sessionID := a.clientSessionID()
	status, err := a.addressedStatus(ctx, sessionID, diagnostics)
	if err != nil && sessionID != "" {
		return a.addressedStatus(ctx, "", diagnostics)
	}
	return status, err
}

func (a *SessionClientAdapter) addressedStatus(
	ctx context.Context,
	sessionID string,
	diagnostics bool,
) (controlstatus.StatusSnapshot, error) {
	return a.statusClient.SessionStatus(ctx, appserver.StatusRequest{
		SessionID:          strings.TrimSpace(sessionID),
		WorkspaceKey:       strings.TrimSpace(a.workspaceKey),
		CWD:                a.WorkspaceDir(),
		Surface:            strings.TrimSpace(a.surface),
		IncludeDiagnostics: diagnostics,
	})
}

// WorkspaceDir returns the workspace selected by Session lifecycle operations.
// Read-only status and inspection calls must not change the active Session.
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

func (a *SessionClientAdapter) ensureSessionForMainPrompt(ctx context.Context) (appserver.SessionState, error) {
	return a.ensureClientSessionForWork(ctx, "session-main-prompt-")
}

func (a *SessionClientAdapter) ensureSessionForParticipantStart(ctx context.Context) (appserver.SessionState, error) {
	return a.ensureClientSessionForWork(ctx, "session-participant-prompt-")
}

// ensureClientSessionForWork contains only the mechanical product Session
// creation transaction. Callers retain ownership of the policy deciding which
// work-bearing path is allowed to invoke it.
func (a *SessionClientAdapter) ensureClientSessionForWork(ctx context.Context, operationPrefix string) (appserver.SessionState, error) {
	if a == nil || a.sessionClient == nil {
		return appserver.SessionState{}, errors.New("app/gatewayapp/controladapter: Session client is unavailable")
	}
	a.sessionChangeMu.Lock()
	defer a.sessionChangeMu.Unlock()
	if sessionID := a.clientSessionID(); sessionID != "" {
		state, err := a.inspectWorkSession(ctx, sessionID)
		if err == nil {
			err = a.ensureSessionPresence(state)
		}
		return state, err
	}
	result, err := a.sessionClient.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: operationPrefix + uuid.NewString()},
		PreferredSessionID: strings.TrimSpace(a.preferredID),
		WorkspaceKey:       strings.TrimSpace(a.workspaceKey),
		CWD:                strings.TrimSpace(a.workspaceDir),
		Metadata:           map[string]any{"surface": strings.TrimSpace(a.surface)},
	})
	if err != nil {
		return appserver.SessionState{}, err
	}
	if strings.TrimSpace(result.SessionID) == "" {
		return appserver.SessionState{}, errors.New("app/gatewayapp/controladapter: create Session returned no Session ID")
	}
	state, err := a.sessionClient.InspectSession(ctx, appserver.StateRequest{SessionID: result.SessionID})
	if err != nil {
		return appserver.SessionState{}, err
	}
	if err := a.validateTUISessionController(state); err != nil {
		return appserver.SessionState{}, err
	}
	a.preferredID = ""
	a.setClientSession(state.SessionID, state.CWD)
	if err := a.ensureSessionPresence(state); err != nil {
		return appserver.SessionState{}, err
	}
	return state, nil
}

func (a *SessionClientAdapter) ensureSessionPresence(state appserver.SessionState) error {
	if !a.tracksSessionPresence() {
		return nil
	}
	a.activeMu.Lock()
	presence := a.presence
	a.activeMu.Unlock()
	if presence != nil {
		select {
		case <-presence.done:
			a.replaceSessionPresence(nil)
		default:
			return nil
		}
	}
	presence, err := a.openSessionPresence(state)
	if err != nil {
		return err
	}
	a.replaceSessionPresence(presence)
	return nil
}

func (a *SessionClientAdapter) inspectWorkSession(ctx context.Context, sessionID string) (appserver.SessionState, error) {
	state, err := a.sessionClient.InspectSession(ctx, appserver.StateRequest{SessionID: sessionID})
	if err != nil {
		return appserver.SessionState{}, err
	}
	if err := a.validateTUISessionController(state); err != nil {
		return appserver.SessionState{}, err
	}
	return state, nil
}
