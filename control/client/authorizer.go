package controlclient

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

var ErrUnauthorized = errorcode.New(errorcode.PermissionDenied, "controlclient: permission denied")

type Authorizer interface {
	Authorize(context.Context, Principal, Action, string) error
}

// SessionAuthorizer enforces owner-by-principal access to an explicit Session
// ID. Admin is the only role that bypasses owner equality.
type SessionAuthorizer struct {
	Sessions interface {
		Session(context.Context, session.SessionRef) (session.Session, error)
	}
}

func (a SessionAuthorizer) Authorize(ctx context.Context, principal Principal, action Action, sessionID string) error {
	principal.ID = strings.TrimSpace(principal.ID)
	if principal.ID == "" {
		return ErrUnauthorized
	}
	switch action {
	case ActionSessionCreate, ActionSessionList:
		return nil
	}
	if a.Sessions == nil || strings.TrimSpace(sessionID) == "" {
		return ErrUnauthorized
	}
	active, err := a.Sessions.Session(ctx, session.SessionRef{SessionID: strings.TrimSpace(sessionID)})
	if errors.Is(err, session.ErrSessionNotFound) {
		return ErrUnauthorized
	}
	if err != nil {
		if errorcode.CodeOf(err) != errorcode.Unknown {
			return err
		}
		return errorcode.Wrap(errorcode.Internal, "controlclient: load session for authorization", err)
	}
	if !principal.HasRole("admin") && strings.TrimSpace(active.UserID) != principal.ID {
		return ErrUnauthorized
	}
	if action != ActionSessionInspect && action != ActionSessionClose {
		stateReader, ok := a.Sessions.(session.StateReader)
		if !ok {
			return errorcode.New(errorcode.Internal, "controlclient: session lifecycle authorization is unavailable")
		}
		closed, err := IsSessionClosed(ctx, stateReader, active.SessionRef)
		if err != nil {
			return err
		}
		if closed {
			return ErrSessionClosed
		}
	}
	return nil
}

// ProductCommandAuthorizer owns Host product-command authorization while
// delegating Session-scoped commands to the existing Session authorizer.
type ProductCommandAuthorizer struct {
	Sessions Authorizer
}

// ConfigurationAuthorizer is retained for embedded clients compiled against
// the pre-AgentBinding command contract. Remove this source-compatibility alias
// at the next control/client major contract revision.
// Deprecated: use ProductCommandAuthorizer.
type ConfigurationAuthorizer = ProductCommandAuthorizer

func (a ProductCommandAuthorizer) Authorize(ctx context.Context, principal Principal, action Action, sessionID string) error {
	if isHostProductAction(action) {
		if strings.TrimSpace(principal.ID) == "" || strings.TrimSpace(sessionID) != "" {
			return ErrUnauthorized
		}
		return nil
	}
	if a.Sessions == nil {
		return ErrUnauthorized
	}
	return a.Sessions.Authorize(ctx, principal, action, sessionID)
}

func isHostProductAction(action Action) bool {
	switch action {
	case ActionModelConnect, ActionModelUse, ActionModelDelete,
		ActionSandboxBackend, ActionSandboxPrepare, ActionSandboxRepair, ActionSandboxReset, ActionSandboxRefresh,
		ActionAgentBindingBind, ActionAgentBindingReset, ActionAgentRoleCreate, ActionAgentRoleDelete,
		ActionAgentBindingSetSave, ActionAgentBindingSetApply, ActionAgentBindingSetDelete,
		ActionACPAgentPrepare, ActionACPAgentPrepareAuth, ActionACPAgentConnect,
		ActionACPAgentDisconnect,
		ActionPluginMarketplaceAdd, ActionPluginMarketplaceUpdate, ActionPluginMarketplaceRemove,
		ActionPluginAddPath, ActionPluginInstall, ActionPluginEnable, ActionPluginDisable, ActionPluginRemove:
		return true
	default:
		return false
	}
}
