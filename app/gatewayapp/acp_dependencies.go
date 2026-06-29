package gatewayapp

import (
	"fmt"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

type ACPAgentDependencies struct {
	Runtime          agent.Runtime
	Sessions         session.Service
	Resolver         gateway.RuntimeResolver
	ApprovalReviewer gateway.ApprovalReviewer
	Assembly         assembly.ResolvedAssembly
	AppName          string
	UserID           string
}

func (s *Stack) ACPAgentDependencies() (ACPAgentDependencies, error) {
	if s == nil {
		return ACPAgentDependencies{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	var resolver gateway.RuntimeResolver
	var reviewer gateway.ApprovalReviewer
	if s.gateway != nil {
		resolver = s.gateway.Resolver()
		reviewer = s.gateway.ApprovalReviewer()
	}
	deps := ACPAgentDependencies{
		Runtime:          s.engine,
		Sessions:         s.Sessions,
		Resolver:         resolver,
		ApprovalReviewer: reviewer,
		Assembly:         s.runtime.Assembly,
		AppName:          s.AppName,
		UserID:           s.UserID,
	}
	s.mu.RUnlock()
	if deps.Runtime == nil {
		return ACPAgentDependencies{}, fmt.Errorf("gatewayapp: runtime is unavailable")
	}
	if deps.Resolver == nil {
		return ACPAgentDependencies{}, fmt.Errorf("gatewayapp: gateway resolver is unavailable")
	}
	return deps, nil
}
