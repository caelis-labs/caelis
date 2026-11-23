package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

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

// finalizeTurn clears only this Turn's Plan after its producers have drained,
// before releasing the original fence.
func (r *Runtime) finalizeTurn(ctx context.Context, ref session.SessionRef, turnID string) error {
	return r.clearTurnPlan(context.WithoutCancel(ctx), ref, turnID, false)
}

// reconcileTurnPlan runs under newly acquired or inherited exclusive ownership,
// before new input or compaction. Fence acquisition/Host transfer, never an empty
// local handle map, establishes that the prior producer can no longer write.
func (r *Runtime) reconcileTurnPlan(ctx context.Context, ref session.SessionRef, turnID string) error {
	return r.clearTurnPlan(ctx, ref, turnID, true)
}

func (r *Runtime) clearTurnPlan(ctx context.Context, ref session.SessionRef, turnID string, orphan bool) error {
	// Most turns have no plan. Only load its source history when state names one.
	state, err := r.sessions.SnapshotState(ctx, ref)
	if err != nil {
		return err
	}
	if _, ok := state["plan"].(map[string]any); !ok {
		return nil
	}
	var original map[string]any
	var identity string
	for {
		loaded, err := r.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref, IncludeTransient: true})
		if err != nil {
			return err
		}
		plan, ok := loaded.State["plan"].(map[string]any)
		if !ok {
			return nil
		}
		owner, _ := plan["turn_id"].(string)
		var sourceID string
		for i := len(loaded.Events) - 1; i >= 0; i-- {
			e := loaded.Events[i]
			if e == nil || e.Type != session.EventTypePlan || !session.IsCanonicalHistoryEvent(e) || e.Scope == nil || owner != "" && e.Scope.TurnID != owner {
				continue
			}
			owner, sourceID = e.Scope.TurnID, e.ID
			break
		}
		if !orphan && owner != turnID {
			return nil
		}
		if session.RuntimeMutationGuard(ctx).FenceID == "" {
			return fmt.Errorf("plan cleanup requires exclusive Session ownership and ended prior producer")
		}
		// Legacy state lacking an owning Turn/fact is identified by its full
		// original value under the same exclusive-fence compatibility rule.
		raw, err := json.Marshal(struct {
			Owner, Source string
			Plan          map[string]any
		}{owner, sourceID, plan})
		if err != nil {
			return err
		}
		currentIdentity := fmt.Sprintf("turn-plan:%s:%x", owner, sha256.Sum256(raw))
		if original == nil {
			original, identity = session.CloneState(plan), currentIdentity
		} else if currentIdentity != identity {
			// A changed Plan/fact is not the operation admitted at the original cut.
			return session.ErrRevisionConflict
		}
		service, ok := r.sessions.(session.EventBatchStateService)
		if !ok {
			return fmt.Errorf("plan cleanup requires atomic Session state commit")
		}
		_, err = service.AppendEventsAndUpdateState(ctx, session.AppendEventsAndUpdateStateRequest{
			SessionRef: ref, MutationGuard: session.RuntimeMutationGuard(ctx), ExpectedRevision: &loaded.Session.Revision,
			TransactionID: identity, MutationDigest: "clear-owned-plan-v2:" + identity,
			UpdateState: func(_ []*session.Event, current map[string]any) (map[string]any, error) {
				if !reflect.DeepEqual(current["plan"], original) {
					return nil, session.ErrRevisionConflict
				}
				delete(current, "plan")
				return current, nil
			},
		})
		if !errors.Is(err, session.ErrRevisionConflict) {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}
