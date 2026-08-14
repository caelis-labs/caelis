package runtime

import (
	"context"
	"fmt"

	agent "github.com/caelis-labs/caelis/agent-sdk"
)

func (r *Runtime) forwardControllerEvents(ctx context.Context, req agent.ControllerEventForwardRequest) error {
	if r == nil || r.controllerEventForwarder == nil {
		return fmt.Errorf("agent-sdk/runtime: controller event forwarder is not configured")
	}
	if req.Normalize == nil {
		req.Normalize = normalizeEvent
	}
	return r.controllerEventForwarder.ForwardControllerEvents(ctx, req)
}
