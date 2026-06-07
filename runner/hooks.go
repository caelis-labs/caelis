package runner

import (
	"context"

	"github.com/OnslaughtSnail/caelis/agent"
)

func runBeforeInvocationHooks(ctx context.Context, hooks []agent.Hook, evt agent.InvocationHook) error {
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		if err := hook.BeforeInvocation(ctx, evt); err != nil {
			return err
		}
	}
	return nil
}

func runAfterInvocationHooks(ctx context.Context, hooks []agent.Hook, evt agent.InvocationHookResult) error {
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		if err := hook.AfterInvocation(ctx, evt); err != nil {
			return err
		}
	}
	return nil
}

func cloneMapAny(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
