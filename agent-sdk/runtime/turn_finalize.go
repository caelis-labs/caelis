package runtime

import (
	"context"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// ownRunFence gives standalone SDK callers the same exclusive producer lifetime
// as Control. An inherited bearer is retained by its existing owner. Acquiring a
// new fence never takes over an old Host or an unresponsive producer.
func (r *Runtime) ownRunFence(ctx context.Context, ref session.SessionRef) (context.Context, func() error, error) {
	if session.RuntimeMutationGuard(ctx).FenceID != "" {
		return ctx, func() error { return nil }, nil
	}
	service, ok := r.sessions.(session.SessionFenceService)
	if !ok {
		return ctx, func() error { return nil }, nil
	}
	fence, err := service.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{SessionRef: ref, OwnerID: r.nextID("sdk-runtime", nil)})
	if err != nil {
		return ctx, func() error { return nil }, err
	}
	return session.ContextWithRuntimeFence(ctx, fence), func() error {
		return service.ReleaseSessionFence(context.WithoutCancel(ctx), session.SessionFenceReleaseRequest(fence))
	}, nil
}

// finalizeTurn runs only after the loop and its actual producers have drained,
// while the original fence still protects the owning Plan.
func (r *Runtime) finalizeTurn(ctx context.Context, ref session.SessionRef, turnID string) error {
	state, err := r.sessions.SnapshotState(context.WithoutCancel(ctx), ref)
	if err != nil {
		return err
	}
	plan, ok := state["plan"].(map[string]any)
	if !ok {
		return nil
	}
	if owner, _ := plan["turn_id"].(string); owner != turnID {
		return nil
	}
	service, ok := r.sessions.(session.EventBatchStateService)
	if !ok {
		return fmt.Errorf("plan cleanup requires atomic Session state commit")
	}
	_, err = service.AppendEventsAndUpdateState(context.WithoutCancel(ctx), session.AppendEventsAndUpdateStateRequest{
		SessionRef: ref, MutationGuard: session.RuntimeMutationGuard(ctx), TransactionID: "turn-plan:" + turnID, MutationDigest: "clear-owned-plan-v1",
		UpdateState: func(_ []*session.Event, current map[string]any) (map[string]any, error) {
			if plan, ok := current["plan"].(map[string]any); ok && plan["turn_id"] == turnID {
				delete(current, "plan")
			}
			return current, nil
		},
	})
	return err
}
