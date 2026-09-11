package gatewayapp

import (
	"context"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sdksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

// subagentHistoryService routes recovery to the same Session Runtime that owns
// child input. A Task subscription retains that Runtime independently of the
// parent Session feed, so the loaded connection can continue producing output.
type subagentHistoryService struct {
	registry *sessionRuntimeRegistry
}

func (s *subagentHistoryService) LoadHistory(ctx context.Context, req sdksubagent.HistoryRequest) (session.LoadedSession, error) {
	if s.registry == nil {
		return session.LoadedSession{}, fmt.Errorf("gatewayapp: child Runtime registry is unavailable")
	}
	runtime, _, release, err := s.registry.acquireControlRuntime(ctx, req.Reconnect.Spawn.SessionRef.SessionID, true)
	if err != nil {
		return session.LoadedSession{}, err
	}
	if release != nil {
		defer func() { _ = release(context.WithoutCancel(ctx)) }()
	}
	return runtime.instance.LoadHistory(ctx, req)
}

func (s *subagentHistoryService) retainObservation(ref session.SessionRef) (func(), error) {
	if s.registry == nil {
		return nil, fmt.Errorf("gatewayapp: child Runtime registry is unavailable")
	}
	return s.registry.retainObservation(ref)
}
