package gatewayapp

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

// ControlSessionReader is the read-only durable Session capability available
// to Host-private AppServer assemblers.
type ControlSessionReader interface {
	session.Reader
	session.StateReader
}

// ControlRuntimeLease is private app composition glue exposed to sibling
// AppServer adapters. Presentation surfaces must consume focused clients and
// never receive this Runtime handle.
type ControlRuntimeLease struct {
	runtime *runtimeComposition
	session session.Session
	release func(context.Context) error

	closeOnce sync.Once
	closeErr  error
}

// ControlRuntimeService is the focused Host-private lifecycle authority used
// by AppServer adapters to acquire an authorized Session Runtime snapshot. It
// retains the registry and Session authority, never the concrete Host Stack.
type ControlRuntimeService struct {
	registry *sessionRuntimeRegistry
	sessions session.Service
}

// ControlRuntimes returns the focused Runtime lease authority for AppServer
// composition.
func (s *Stack) ControlRuntimes() ControlRuntimeService {
	if s == nil {
		return ControlRuntimeService{}
	}
	return ControlRuntimeService{registry: s.sessionRuntimes, sessions: s.composition.sessions}
}

// Acquire authorizes and resolves one Session Runtime snapshot.
func (s ControlRuntimeService) Acquire(
	ctx context.Context,
	principal appserver.Principal,
	action appserver.Action,
	sessionID string,
	activate bool,
) (*ControlRuntimeLease, error) {
	if s.registry == nil || s.sessions == nil {
		return nil, errors.New("gatewayapp: Control Runtime service is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	authorizer := appserver.SessionAuthorizer{Sessions: s.sessions}
	if err := authorizer.Authorize(ctx, principal, action, sessionID); err != nil {
		return nil, err
	}
	runtime, active, release, err := s.registry.acquireControlRuntime(ctx, sessionID, activate)
	if err != nil {
		return nil, err
	}
	if runtime == nil || runtime.instance == nil {
		if release != nil {
			_ = release(context.Background())
		}
		return nil, errors.New("gatewayapp: Control Runtime snapshot is unavailable")
	}
	return &ControlRuntimeLease{runtime: &runtime.instance.runtimeComposition, session: session.CloneSession(active), release: release}, nil
}

// SessionReads returns the durable Session reads for this authorized Runtime.
func (l *ControlRuntimeLease) SessionReads() ControlSessionReader {
	if l == nil || l.runtime == nil {
		return nil
	}
	return l.runtime.sessions
}

// AppName returns the application identity pinned to this Runtime.
func (l *ControlRuntimeLease) AppName() string {
	if l == nil || l.runtime == nil {
		return ""
	}
	return l.runtime.authorities.appName
}

// UserID returns the Session owner identity pinned to this Runtime.
func (l *ControlRuntimeLease) UserID() string {
	if l == nil || l.runtime == nil {
		return ""
	}
	return l.runtime.authorities.userID
}

// Workspace returns the immutable workspace address pinned to this Runtime.
func (l *ControlRuntimeLease) Workspace() session.WorkspaceRef {
	if l == nil || l.runtime == nil {
		return session.WorkspaceRef{}
	}
	return l.runtime.workspace
}

// KernelReads returns the focused live execution projection.
func (l *ControlRuntimeLease) KernelReads() KernelReadService {
	if l == nil {
		return KernelReadService{}
	}
	return KernelReadService{composition: l.runtime}
}

// Status returns the focused Runtime status service.
func (l *ControlRuntimeLease) Status() StatusService {
	if l == nil || l.runtime == nil {
		return StatusService{}
	}
	return l.runtime.Status()
}

// Agents returns the focused Runtime Agent read service.
func (l *ControlRuntimeLease) Agents() AgentService {
	if l == nil || l.runtime == nil {
		return AgentService{}
	}
	return l.runtime.Agents()
}

// Models returns the focused Runtime model read service.
func (l *ControlRuntimeLease) Models() ModelService {
	if l == nil || l.runtime == nil {
		return ModelService{}
	}
	return l.runtime.Models()
}

// Skills returns the focused Runtime skill read service.
func (l *ControlRuntimeLease) Skills() SkillService {
	if l == nil || l.runtime == nil {
		return SkillService{}
	}
	return l.runtime.Skills()
}

// PluginReads returns the focused read-only Runtime Plugin service. Mutations
// remain on principal-bound AppServer commands.
func (l *ControlRuntimeLease) PluginReads() PluginReadService {
	if l == nil || l.runtime == nil {
		return PluginReadService{}
	}
	return l.runtime.pluginReads()
}

// Session returns the authorized durable Session snapshot.
func (l *ControlRuntimeLease) Session() session.Session {
	if l == nil {
		return session.Session{}
	}
	return session.CloneSession(l.session)
}

// Close releases a retained Runtime use or destroys a disposable composition.
func (l *ControlRuntimeLease) Close(ctx context.Context) error {
	if l == nil || l.release == nil {
		return nil
	}
	l.closeOnce.Do(func() { l.closeErr = l.release(ctx) })
	return l.closeErr
}
