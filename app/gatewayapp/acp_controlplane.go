package gatewayapp

import (
	"context"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	sdksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	acpassembly "github.com/caelis-labs/caelis/internal/acpagentbridge/assembly"
	acpsubagent "github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

// LoadHistory resolves the current external ACP control plane only when one
// selected child overlay asks for provider-owned Session history.
func (s *Stack) LoadHistory(ctx context.Context, req sdksubagent.HistoryRequest) (session.LoadedSession, error) {
	if s == nil {
		return session.LoadedSession{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.mu.RLock()
	controlPlane := s.acpControlPlane
	s.mu.RUnlock()
	if controlPlane == nil {
		return session.LoadedSession{}, fmt.Errorf("gatewayapp: ACP subagent history loader is unavailable")
	}
	return controlPlane.LoadHistory(ctx, req)
}

func (s *Stack) delegationPlacementResolver(runtimeCfg stackRuntimeConfig) acpsubagent.PlacementResolver {
	return func(_ context.Context, _ sdksubagent.SpawnContext, req delegation.TargetRequest) (acpsubagent.AgentConfig, error) {
		return s.resolveDelegationPlacement(req, runtimeCfg)
	}
}

func injectACPControlPlane(
	cfg runtime.Config,
	resolved assembly.ResolvedAssembly,
	placementResolver acpsubagent.PlacementResolver,
	sessionPreparer acpsubagent.SessionPreparer,
) (runtime.Config, *acpassembly.ControlPlane, error) {
	if len(resolved.Agents) == 0 {
		return cfg, nil, nil
	}
	controlPlane, err := acpassembly.NewControlPlane(acpassembly.ControlPlaneConfig{
		Agents:            resolved.Agents,
		PlacementResolver: placementResolver,
		SessionPreparer:   sessionPreparer,
	})
	if err != nil {
		return cfg, nil, err
	}
	cfg.Controllers = controlPlane.Controllers
	cfg.Subagents = controlPlane.Subagents
	return cfg, controlPlane, nil
}
