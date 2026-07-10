package gatewayapp

import (
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	acpassembly "github.com/caelis-labs/caelis/internal/acpagentbridge/assembly"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

func injectACPControlPlane(cfg runtime.Config, resolved assembly.ResolvedAssembly) (runtime.Config, *acpassembly.ControlPlane, error) {
	if len(resolved.Agents) == 0 {
		return cfg, nil, nil
	}
	controlPlane, err := acpassembly.NewControlPlane(acpassembly.ControlPlaneConfig{
		Agents: resolved.Agents,
	})
	if err != nil {
		return cfg, nil, err
	}
	cfg.Controllers = controlPlane.Controllers
	cfg.Subagents = controlPlane.Subagents
	return cfg, controlPlane, nil
}
