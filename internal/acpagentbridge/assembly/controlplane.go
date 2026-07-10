package assembly

import (
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	acpcontroller "github.com/caelis-labs/caelis/internal/acpagentbridge/controller"
	acpsubagent "github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

// ControlPlane bundles the default external ACP controller/subagent control
// plane built from one shared registry. Product-host composition injects the
// exported surfaces into agent-sdk/runtime.
type ControlPlane struct {
	Controllers controller.Backend
	Subagents   subagent.Runner
	Updater     assembly.AgentConfigUpdater
}

// ControlPlaneConfig configures one shared-registry ACP control plane.
type ControlPlaneConfig struct {
	Agents []assembly.AgentConfig
}

// NewControlPlane constructs controller, subagent runner, and updater instances
// backed by the same registry.
func NewControlPlane(cfg ControlPlaneConfig) (*ControlPlane, error) {
	registry, err := acpsubagent.NewRegistry(cfg.Agents)
	if err != nil {
		return nil, err
	}
	runner, err := acpsubagent.NewRunner(acpsubagent.RunnerConfig{Registry: registry})
	if err != nil {
		return nil, err
	}
	manager, err := acpcontroller.NewManager(acpcontroller.Config{Registry: registry})
	if err != nil {
		return nil, err
	}
	return &ControlPlane{
		Controllers: manager,
		Subagents:   runner,
		Updater:     &registryUpdater{registry: registry},
	}, nil
}

type registryUpdater struct {
	registry *acpsubagent.Registry
}

func (u *registryUpdater) UpdateAgents(agents []assembly.AgentConfig) error {
	if u == nil || u.registry == nil {
		return fmt.Errorf("internal/acpagentbridge/assembly: agent config updater is unavailable")
	}
	return u.registry.Replace(agents)
}
