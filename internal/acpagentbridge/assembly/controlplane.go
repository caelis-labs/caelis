package assembly

import (
	"context"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	acpcontroller "github.com/caelis-labs/caelis/internal/acpagentbridge/controller"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/endpoint"
	acpsubagent "github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

// ControlPlane bundles the default external ACP controller/subagent control
// plane built from one shared registry. Product-host composition injects the
// exported surfaces into agent-sdk/runtime.
type ControlPlane struct {
	Controllers controller.Backend
	Subagents   subagent.Runner
	registry    *acpsubagent.Registry
	manager     *acpcontroller.Manager
	runner      *acpsubagent.Runner
}

// ControlPlaneConfig configures one shared-registry ACP control plane.
type ControlPlaneConfig struct {
	Agents            []assembly.AgentConfig
	PlacementResolver acpsubagent.PlacementResolver
	SessionPreparer   acpsubagent.SessionPreparer
	EndpointResolver  endpoint.Resolver
}

// NewControlPlane constructs controller and subagent runner instances backed
// by the same registry.
func NewControlPlane(cfg ControlPlaneConfig) (*ControlPlane, error) {
	registry, err := acpsubagent.NewRegistry(cfg.Agents)
	if err != nil {
		return nil, err
	}
	runner, err := acpsubagent.NewRunner(acpsubagent.RunnerConfig{
		Registry:          registry,
		PlacementResolver: cfg.PlacementResolver,
		SessionPreparer:   cfg.SessionPreparer,
		EndpointResolver:  cfg.EndpointResolver,
	})
	if err != nil {
		return nil, err
	}
	manager, err := acpcontroller.NewManager(acpcontroller.Config{Registry: registry, EndpointResolver: cfg.EndpointResolver})
	if err != nil {
		return nil, err
	}
	return &ControlPlane{
		Controllers: manager,
		Subagents:   runner,
		registry:    registry,
		manager:     manager,
		runner:      runner,
	}, nil
}

// Quiesce drains the Host-owned external child producer boundary.
func (c *ControlPlane) Quiesce(ctx context.Context) error {
	if c == nil || c.runner == nil {
		return nil
	}
	return c.runner.Quiesce(ctx)
}

// LoadHistory delegates one read-only provider session/load to the shared
// subagent runner without activating a Runtime.
func (c *ControlPlane) LoadHistory(ctx context.Context, req subagent.HistoryRequest) (session.LoadedSession, error) {
	if c == nil || c.runner == nil {
		return session.LoadedSession{}, fmt.Errorf("internal/acpagentbridge/assembly: subagent history loader is unavailable")
	}
	return c.runner.LoadHistory(ctx, req)
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

// UpdateAgents replaces the registry snapshot shared by controller and
// subagent backends without exposing the mutable registry authority.
func (c *ControlPlane) UpdateAgents(agents []assembly.AgentConfig) error {
	if c == nil || c.registry == nil {
		return fmt.Errorf("internal/acpagentbridge/assembly: agent registry is unavailable")
	}
	return c.registry.Replace(agents)
}
