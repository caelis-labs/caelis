package control

import (
	"context"
	"strings"
)

// AgentStatusProvider is the narrow service subset needed to inspect the
// current controller.
type AgentStatusProvider interface {
	AgentStatus(context.Context) (AgentStatusSnapshot, error)
}

// ActiveACPStatus returns the current agent status and whether the active
// controller is an ACP controller.
func ActiveACPStatus(ctx context.Context, service AgentStatusProvider) (AgentStatusSnapshot, bool) {
	if service == nil {
		return AgentStatusSnapshot{}, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	status, err := service.AgentStatus(ctx)
	if err != nil {
		return AgentStatusSnapshot{}, false
	}
	return status, strings.EqualFold(strings.TrimSpace(status.ControllerKind), "acp")
}

// ACPControllerActive reports whether the active controller is ACP-backed.
func ACPControllerActive(ctx context.Context, service AgentStatusProvider) bool {
	_, active := ActiveACPStatus(ctx, service)
	return active
}
