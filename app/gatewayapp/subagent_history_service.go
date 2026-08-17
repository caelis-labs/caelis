package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sdksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

// subagentHistoryService is the focused provider-owned child history reader
// used by the Control Task stream fallback. It retains no concrete Host.
type subagentHistoryService struct {
	composition *runtimeComposition
}

func (s subagentHistoryService) LoadHistory(ctx context.Context, req sdksubagent.HistoryRequest) (session.LoadedSession, error) {
	return s.composition.LoadHistory(ctx, req)
}
