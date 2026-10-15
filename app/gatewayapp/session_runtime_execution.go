package gatewayapp

import (
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/controlplane"
)

// buildSessionExecution is the common execution assembly for a Session. Its
// caller supplies the already resolved model/tool/policy configuration; this
// owner only binds the existing Runtime, controller recovery and execution fence.
// The bundle owns every successfully allocated resource even on a later error.
func (s *runtimeComposition) buildSessionExecution(bundle *gatewayRuntimeBundle, config runtime.Config, contextRouter *controlplane.ContextRouter) (*controlplane.FencedRuntime, *controlplane.SessionControl, error) {
	coordinator, err := controlplane.NewCoordinator(controlplane.CoordinatorConfig{
		Sessions: s.sessions, Controllers: config.Controllers, Context: contextRouter,
		FenceOwnerID:     strings.TrimSpace(s.authorities.fenceOwnerID),
		LifecycleContext: s.authorities.lifecycleCtx, Diagnostics: s.authorities.diagnostics,
	})
	if err != nil {
		return nil, nil, err
	}
	config.ControllerRecovery = coordinator
	engine, err := runtime.New(config)
	if err != nil {
		return nil, nil, err
	}
	bundle.Engine = engine
	fences, ok := s.sessions.(session.SessionFenceService)
	if !ok {
		return nil, nil, fmt.Errorf("gatewayapp: production session service does not support execution fences")
	}
	fenced, err := controlplane.NewFencedRuntime(controlplane.FencedRuntimeConfig{
		Runtime: engine, Fences: fences, OwnerID: strings.TrimSpace(s.authorities.fenceOwnerID),
		LifecycleContext: s.authorities.lifecycleCtx, Diagnostics: s.authorities.diagnostics,
	})
	if err != nil {
		return nil, nil, err
	}
	bundle.Placement = fenced
	control, err := controlplane.NewSessionControl(controlplane.SessionControlConfig{Controllers: coordinator, Participants: fenced})
	return fenced, control, err
}
