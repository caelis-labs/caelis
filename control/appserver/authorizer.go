package appserver

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
)

var ErrUnauthorized = errorcode.New(errorcode.PermissionDenied, "controlclient: permission denied")

type Authorizer interface {
	Authorize(context.Context, Principal, Action, string) error
}

// SessionAuthorizer enforces canonical owner equality and application bindings.
// An application credential cannot acquire another Session through metadata, a
// client-selected ID, or the ordinary Host principal's administrative role.
type SessionAuthorizer struct {
	Sessions interface {
		Session(context.Context, session.SessionRef) (session.Session, error)
	}
	Applications *application.Store
}

func (a SessionAuthorizer) Authorize(ctx context.Context, p Principal, action Action, sessionID string) error {
	p.ID = strings.TrimSpace(p.ID)
	if p.ID == "" {
		return ErrUnauthorized
	}
	scoped := p.ApplicationID != "" || p.ConnectionID != ""
	if action == ActionApplicationCreate {
		scope, err := ApplicationScope(p)
		if err != nil || a.Applications == nil {
			return ErrUnauthorized
		}
		return a.Applications.CheckActive(ctx, scope)
	}
	switch action {
	case ActionSessionCreate, ActionSessionList:
		if scoped {
			return ErrUnauthorized
		}
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
	if !p.HasRole("admin") && strings.TrimSpace(active.UserID) != p.ID {
		return ErrUnauthorized
	}
	if sessionvisibility.IsRetiredSession(active) {
		return errorcode.New(errorcode.Unsupported, "controlclient: legacy Bot Mode has been removed; stored data is not migrated or deleted")
	}
	var binding application.Binding
	bound := false
	if a.Applications != nil {
		binding, err = a.Applications.BindingForSession(ctx, active.SessionID)
		if err == nil {
			bound = true
		} else if !errors.Is(err, application.ErrNotFound) {
			return err
		}
	}
	if scoped {
		scope, scopeErr := ApplicationScope(p)
		if scopeErr != nil || !bound || binding.Scope != scope || active.UserID != scope.PrincipalID {
			return ErrUnauthorized
		}
		switch action {
		case ActionSessionInspect:
			return nil
		case ActionApplicationPrompt, ActionCancel, ActionApprovalResolve, ActionSessionClose:
			if err = a.Applications.CheckActive(ctx, scope); err != nil {
				return err
			}
		default:
			return ErrUnauthorized
		}
		if binding.Archived && action != ActionSessionClose {
			return ErrSessionClosed
		}
	} else if bound {
		if !p.HasRole(RoleSystemSessionRuntime) || action != ActionSessionInspect {
			return ErrUnauthorized
		}
	} else if action == ActionApplicationPrompt || active.Metadata[sessionvisibility.MetadataSystemManagedAgent] == application.MetadataKind {
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
// delegating Session-scoped commands to the Session authorizer.
type ProductCommandAuthorizer struct{ Sessions Authorizer }

// ConfigurationAuthorizer is retained for embedded clients compiled against
// the pre-AgentBinding command contract. Remove this source-compatibility alias
// at the next control/appserver major contract revision.
// Deprecated: use ProductCommandAuthorizer.
type ConfigurationAuthorizer = ProductCommandAuthorizer

func (a ProductCommandAuthorizer) Authorize(ctx context.Context, p Principal, action Action, sessionID string) error {
	if isHostProductAction(action) {
		if p.ApplicationID != "" || p.ConnectionID != "" || strings.TrimSpace(p.ID) == "" || strings.TrimSpace(sessionID) != "" {
			return ErrUnauthorized
		}
		return nil
	}
	if a.Sessions == nil {
		return ErrUnauthorized
	}
	return a.Sessions.Authorize(ctx, p, action, sessionID)
}
func isHostProductAction(action Action) bool {
	switch action {
	case ActionModelConnect, ActionModelUse, ActionModelDelete,
		ActionSandboxBackend, ActionSandboxPrepare, ActionSandboxRepair, ActionSandboxReset, ActionSandboxRefresh,
		ActionWorkspaceTrust,
		ActionAgentBindingBind, ActionAgentBindingReset, ActionAgentRoleCreate, ActionAgentRoleDelete,
		ActionAgentBindingSetSave, ActionAgentBindingSetApply, ActionAgentBindingSetDelete,
		ActionACPAgentPrepare, ActionACPAgentPrepareAuth, ActionACPAgentConnect, ActionACPAgentDisconnect,
		ActionPluginMarketplaceAdd, ActionPluginMarketplaceUpdate, ActionPluginMarketplaceRemove,
		ActionPluginAddPath, ActionPluginInstall, ActionPluginEnable, ActionPluginDisable, ActionPluginRemove:
		return true
	default:
		return false
	}
}
