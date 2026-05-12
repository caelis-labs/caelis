package gatewayapp

import (
	"fmt"

	"github.com/OnslaughtSnail/caelis/impl/agent/acp"
	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

type ACPAgentDependencies struct {
	Runtime  agent.Runtime
	Sessions session.Service
	Gateway  *kernel.Gateway
	Assembly assembly.ResolvedAssembly
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

func (s *Stack) ACPAgent() (*acp.RuntimeAgent, error) {
	deps, err := s.ACPAgentDependencies()
	if err != nil {
		return nil, err
	}
	return acp.NewGatewayAgent(acp.GatewayAgentConfig{
		Runtime:  deps.Runtime,
		Sessions: deps.Sessions,
		Gateway:  deps.Gateway,
		Assembly: deps.Assembly,
		AppName:  deps.AppName,
		UserID:   deps.UserID,
		SurfaceBuilder: func(req acp.SurfaceRequest) acp.Surface {
			return s.ACPSurface(req.Modes, req.UseFallbackModes, req.Config)
		},
	})
}
