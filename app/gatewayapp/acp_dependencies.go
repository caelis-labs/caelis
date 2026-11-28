package gatewayapp

import (
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

// AppServerPresentationDependencies provides Host-owned services and inputs
// needed by the AppServer composition root.
type AppServerPresentationDependencies struct {
	Shell         *appserver.ShellService
	UIPreferences appserver.UIPreferencesStore
	Sessions      session.ManagedService
	Assembly      assembly.ResolvedAssembly
	AppName       string
	UserID        string
}

// PresentationDependencies returns the inputs owned by AppServer presentation
// assembly without exposing the Host Runtime or Stack to surfaces.
func (s *Stack) PresentationDependencies() (AppServerPresentationDependencies, error) {
	if s == nil {
		return AppServerPresentationDependencies{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.composition.mu.RLock()
	deps := AppServerPresentationDependencies{
		Shell:         s.controlShell,
		Sessions:      s.composition.sessions,
		UIPreferences: s.composition.authorities.store,
		Assembly:      assembly.CloneResolvedAssembly(s.composition.activeRuntime.Assembly),
		AppName:       s.composition.authorities.appName,
		UserID:        s.composition.authorities.userID,
	}
	s.composition.mu.RUnlock()
	return deps, nil
}
