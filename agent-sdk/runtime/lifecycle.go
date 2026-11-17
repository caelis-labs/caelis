package runtime

import (
	"context"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type lifecycleScopeContextKey struct{}

type lifecycleScope struct {
	sessionRef session.SessionRef
	runID      string
	turnID     string
}

func withLifecycleScope(ctx context.Context, scope lifecycleScope) context.Context {
	return context.WithValue(ctx, lifecycleScopeContextKey{}, scope)
}

func lifecycleScopeFromContext(ctx context.Context) lifecycleScope {
	if ctx == nil {
		return lifecycleScope{}
	}
	scope, _ := ctx.Value(lifecycleScopeContextKey{}).(lifecycleScope)
	return scope
}

func (r *Runtime) lifecycleEvent(ctx context.Context, operation agent.LifecycleOperation, name, stepID string) agent.LifecycleEvent {
	scope := lifecycleScopeFromContext(ctx)
	return agent.LifecycleEvent{
		Operation:  operation,
		SessionRef: scope.sessionRef,
		RunID:      scope.runID,
		TurnID:     scope.turnID,
		StepID:     strings.TrimSpace(stepID),
		Name:       strings.TrimSpace(name),
	}
}

func (r *Runtime) traceOperation(ctx context.Context, event agent.LifecycleEvent, next func(context.Context) error) error {
	options := agent.TraceOptions{}
	if r != nil {
		options = r.lifecycle
	}
	return agent.TraceLifecycle(ctx, event, options, next)
}
