package appserveradapter

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
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
	workspace := a.workspaceAddress()
	return a.statusClient.SessionStatus(ctx, appserver.StatusRequest{
		SessionID:          strings.TrimSpace(sessionID),
		WorkspaceKey:       workspace.Key,
		CWD:                workspace.CWD,
		Surface:            strings.TrimSpace(a.surface),
		IncludeDiagnostics: diagnostics,
	})
}

// WorkspaceDir returns the workspace selected by Session lifecycle operations.
// ResetSession retains that workspace. Read-only status and inspection calls
// must not change the active Session.
func (a *SessionClientAdapter) WorkspaceDir() string {
	return a.workspaceAddress().CWD
}

// workspaceAddress returns the workspace key and CWD that one Host read must
// use. Both halves come from a single lock acquisition and are only ever
// replaced together, so a read can never pair one workspace's key with another
// workspace's CWD. The Host rejects such a mixed address as a workspace-identity
// conflict.
func (a *SessionClientAdapter) workspaceAddress() session.WorkspaceRef {
	if a == nil {
		return session.WorkspaceRef{}
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	return a.workspace
}

// sessionWorkspaceAddress is the workspace address a Session reports for itself.
// It is the only authority for addressing that Session's workspace.
func sessionWorkspaceAddress(state appserver.SessionState) session.WorkspaceRef {
	return session.WorkspaceRef{
		Key: strings.TrimSpace(state.WorkspaceKey),
		CWD: strings.TrimSpace(state.CWD),
	}
}

func (a *SessionClientAdapter) clientSessionID() string {
	if a == nil {
		return ""
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	return a.sessionID
}

// setClientSession commits one Session selection. A selection that reports a
// workspace replaces the whole address pair - key and CWD together - so Host
// reads keep describing one workspace. Clearing the selection leaves the
// current workspace, which stays the address subsequent reads and Session
// creation use.
func (a *SessionClientAdapter) setClientSession(sessionID string, workspace session.WorkspaceRef) {
	if a == nil {
		return
	}
	workspace.Key = strings.TrimSpace(workspace.Key)
	workspace.CWD = strings.TrimSpace(workspace.CWD)
	a.sessionMu.Lock()
	a.sessionID = strings.TrimSpace(sessionID)
	if workspace.Key != "" || workspace.CWD != "" {
		a.workspace = workspace
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
		return state, err
	}
	if a.requireExistingSession {
		// Fail closed instead of allocating an ordinary workspace Session. A
		// Bot conversation must be selected before any work is admitted.
		return appserver.SessionState{}, appserver.NewOutcomeError(
			appserver.OutcomeRejected,
			errors.New("app/gatewayapp/controladapter: select a Bot conversation before sending input"),
		)
	}
	workspace := a.workspaceAddress()
	result, err := a.sessionClient.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: operationPrefix + uuid.NewString()},
		PreferredSessionID: strings.TrimSpace(a.preferredID),
		WorkspaceKey:       workspace.Key,
		CWD:                workspace.CWD,
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
	a.setClientSession(state.SessionID, sessionWorkspaceAddress(state))
	return state, nil
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
