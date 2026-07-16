package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	sdksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	acpassembly "github.com/caelis-labs/caelis/internal/acpagentbridge/assembly"
	acpsubagent "github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

func (s *Stack) delegationPlacementResolver(runtimeCfg stackRuntimeConfig) acpsubagent.PlacementResolver {
	return func(_ context.Context, _ sdksubagent.SpawnContext, req delegation.TargetRequest) (acpsubagent.AgentConfig, error) {
		return s.resolveDelegationPlacement(req, runtimeCfg)
	}
}

func injectACPControlPlane(
	cfg runtime.Config,
	resolved assembly.ResolvedAssembly,
	placementResolver acpsubagent.PlacementResolver,
) (runtime.Config, *acpassembly.ControlPlane, error) {
	if len(resolved.Agents) == 0 {
		return cfg, nil, nil
	}
	controlPlane, err := acpassembly.NewControlPlane(acpassembly.ControlPlaneConfig{
		Agents:            resolved.Agents,
		PlacementResolver: placementResolver,
	})
	if err != nil {
		return cfg, nil, err
	}
	cfg.Controllers = controlPlane.Controllers
	cfg.Subagents = controlPlane.Subagents
	return cfg, controlPlane, nil
}
