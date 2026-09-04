package runtime

import (
	"context"
	"fmt"

	agent "github.com/caelis-labs/caelis/agent-sdk"
)

func (r *Runtime) beginControllerEvents(ctx context.Context, req agent.ControllerEventForwardRequest) (agent.ControllerEventSession, error) {
	if r == nil || r.controllerEventForwarder == nil {
		return nil, fmt.Errorf("agent-sdk/runtime: controller event forwarder is not configured")
	}
	if req.Normalize == nil {
		req.Normalize = normalizeEvent
	}
	return r.controllerEventForwarder.BeginControllerEvents(ctx, req)
}
