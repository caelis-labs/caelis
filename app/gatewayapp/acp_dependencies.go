package gatewayapp

import (
	"fmt"

	"github.com/OnslaughtSnail/caelis/gateway"
	sdkplugin "github.com/OnslaughtSnail/caelis/sdk/plugin"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

type ACPAgentDependencies struct {
	Runtime  sdkruntime.Runtime
	Sessions sdksession.Service
	Gateway  *gateway.Gateway
	Assembly sdkplugin.ResolvedAssembly
	AppName  string
	UserID   string
}

func (s *Stack) ACPAgentDependencies() (ACPAgentDependencies, error) {
	if s == nil {
		return ACPAgentDependencies{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	deps := ACPAgentDependencies{
		Runtime:  s.engine,
		Sessions: s.Sessions,
		Gateway:  s.Gateway,
		Assembly: s.runtime.Assembly,
		AppName:  s.AppName,
		UserID:   s.UserID,
	}
	s.mu.RUnlock()
	if deps.Runtime == nil {
		return ACPAgentDependencies{}, fmt.Errorf("gatewayapp: runtime is unavailable")
	}
	if deps.Gateway == nil {
		return ACPAgentDependencies{}, fmt.Errorf("gatewayapp: gateway is unavailable")
	}
	return deps, nil
}
