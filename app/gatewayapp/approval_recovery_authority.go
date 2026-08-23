package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type approvalRecoveryFenceReplacer struct {
	fences session.PriorHostFenceService
}

func (a approvalRecoveryFenceReplacer) ReplacePriorHostFence(
	ctx context.Context,
	req session.AcquireSessionFenceRequest,
) (session.SessionFence, error) {
	return a.fences.ReplacePriorHostSessionFence(ctx, req)
}
