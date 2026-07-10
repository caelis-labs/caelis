package assembly

import (
	"context"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
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
	manager     *acpcontroller.Manager
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
		manager:     manager,
	}, nil
}

func (c *ControlPlane) ControllerStatus(ctx context.Context, ref session.SessionRef) (acpcontroller.ControllerStatus, bool, error) {
	if c == nil || c.manager == nil {
		return acpcontroller.ControllerStatus{}, false, nil
	}
	return c.manager.ControllerStatus(ctx, session.NormalizeSessionRef(ref))
}

func (c *ControlPlane) SetControllerModel(ctx context.Context, req acpcontroller.SetControllerModelRequest) (acpcontroller.ControllerStatus, error) {
	if c == nil || c.manager == nil {
		return acpcontroller.ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/assembly: controller manager is unavailable")
	}
	return c.manager.SetControllerModel(ctx, req)
}

func (c *ControlPlane) SetControllerMode(ctx context.Context, req acpcontroller.SetControllerModeRequest) (acpcontroller.ControllerStatus, error) {
	if c == nil || c.manager == nil {
		return acpcontroller.ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/assembly: controller manager is unavailable")
	}
	return c.manager.SetControllerMode(ctx, req)
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
