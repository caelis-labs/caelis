package gatewayapp

import (
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

// AppServerPresentationDependencies is the narrow host snapshot required to
// assemble protocol-neutral presentation providers. Runtime execution and Task
// streams remain behind their dedicated AppServer services.
type AppServerPresentationDependencies struct {
	Sessions session.Service
	Assembly assembly.ResolvedAssembly
	AppName  string
	UserID   string
}

// PresentationDependencies returns the inputs owned by AppServer presentation
// assembly without exposing the Host Runtime or Stack to surfaces.
func (s *Stack) PresentationDependencies() (AppServerPresentationDependencies, error) {
	if s == nil {
		return AppServerPresentationDependencies{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	deps := AppServerPresentationDependencies{
		Sessions: s.Sessions,
		Assembly: assembly.CloneResolvedAssembly(s.runtime.Assembly),
		AppName:  s.AppName,
		UserID:   s.UserID,
	}
	s.mu.RUnlock()
	return deps, nil
}
