package gatewayapp

import (
	"context"
	"fmt"

	"github.com/OnslaughtSnail/caelis/acp"
	"github.com/OnslaughtSnail/caelis/acpbridge/agentruntime"
	bridgeassembly "github.com/OnslaughtSnail/caelis/acpbridge/assembly"
	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

// NewACPAgent exposes this stack as a standard ACP agent server while keeping
// runtime, session storage, model resolution, and assembly-owned modes/config
// on the same gatewayapp path used by TUI and headless entrypoints.
func (s *Stack) NewACPAgent() (*agentruntime.RuntimeAgent, error) {
	if s == nil {
		return nil, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	rt := s.engine
	assembly := s.runtime.Assembly
	appName := s.AppName
	userID := s.UserID
	s.mu.RUnlock()
	if rt == nil {
		return nil, fmt.Errorf("gatewayapp: runtime is unavailable")
	}
	modes, configs := bridgeassembly.ProvidersFromAssembly(bridgeassembly.ProviderConfig{
		AppName:  appName,
		UserID:   userID,
		Assembly: assembly,
		Sessions: s.Sessions,
	})
	return agentruntime.New(agentruntime.Config{
		Runtime:  rt,
		Sessions: s.Sessions,
		BuildAgentSpec: func(ctx context.Context, session sdksession.Session, req acp.PromptRequest) (sdkruntime.AgentSpec, error) {
			resolver := s.Gateway.Resolver()
			if resolver == nil {
				return sdkruntime.AgentSpec{}, fmt.Errorf("gatewayapp: resolver not available")
			}
			resolved, err := resolver.ResolveTurn(ctx, appgateway.TurnIntent{
				SessionRef: session.SessionRef,
				Surface:    "acp",
			})
			if err != nil {
				return sdkruntime.AgentSpec{}, err
			}
			return resolved.RunRequest.AgentSpec, nil
		},
		Modes:     modes,
		Config:    configs,
		AppName:   appName,
		UserID:    userID,
		AgentInfo: &acp.Implementation{Name: appName},
	})
}
