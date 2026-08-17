package gatewayapp

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

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

// AcquireControlRuntime authorizes and resolves one Session Runtime snapshot.
// activate=false observes a loaded fixed Runtime or builds a disposable current
// workspace composition without retaining it.
func (s *Stack) AcquireControlRuntime(
	ctx context.Context,
	principal appserver.Principal,
	action appserver.Action,
	sessionID string,
	activate bool,
) (*ControlRuntimeLease, error) {
	if s == nil || s.sessionRuntimes == nil || s.composition.sessions == nil {
		return nil, errors.New("gatewayapp: Control Runtime service is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	authorizer := appserver.SessionAuthorizer{Sessions: s.composition.sessions}
	if err := authorizer.Authorize(ctx, principal, action, sessionID); err != nil {
		return nil, err
	}
	runtime, active, release, err := s.sessionRuntimes.acquireControlRuntime(ctx, sessionID, activate)
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

// ControlRuntimeView returns the focused server-side read projection. It is
// intentionally scoped to AppServer adapter packages and is not a product API.
func (l *ControlRuntimeLease) ControlRuntimeView() *ControlRuntimeView {
	if l == nil {
		return nil
	}
	return l.runtime.ControlRuntimeView()
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
