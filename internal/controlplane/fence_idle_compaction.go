package controlplane

import (
	"context"
	"errors"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// releaseFence never invents a second transition identity after an uncertain
// write. An observed replacement can end cleanup, but cannot create a window.
func (g *sessionFenceGuard) releaseFence(fence session.SessionFence) error {
	handoff := g.idleHandoff
	if handoff == nil {
		return releaseSessionFence(g.fences, fence)
	}
	store, ok := g.fences.(session.IdleCompactionStore)
	if !ok {
		err := releaseSessionFence(g.fences, fence)
		if err == nil {
			g.completeIdleHandoff(session.IdleCompactionWindow{}, nil)
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	window, err := store.ReleaseSessionFenceForIdleCompaction(ctx, handoff.Release)
	if err == nil || session.IsCommitted(err) {
		g.completeIdleHandoff(window, nil)
		return nil
	}
	if errors.Is(err, session.ErrIdleCompactionRevoked) {
		releaseErr := releaseSessionFence(g.fences, fence)
		if releaseErr == nil {
			g.completeIdleHandoff(session.IdleCompactionWindow{}, err)
		}
		return releaseErr
	}
	current, readErr := g.fences.SessionFence(ctx, fence.SessionRef)
	if readErr == nil && (current.FenceID != fence.FenceID || current.FencingToken != fence.FencingToken || current.OwnerID != fence.OwnerID) {
		// Retry the exact transition once for authoritative window readback. If a
		// later owner revoked it, completion releases resources without dispatch.
		window, again := store.ReleaseSessionFenceForIdleCompaction(ctx, handoff.Release)
		if again == nil || session.IsCommitted(again) {
			g.completeIdleHandoff(window, nil)
		} else if errors.Is(again, session.ErrIdleCompactionRevoked) || errors.Is(again, session.ErrFenceConflict) {
			g.completeIdleHandoff(session.IdleCompactionWindow{}, again)
		} else {
			return errors.Join(err, again)
		}
		return nil
	}
	return errors.Join(err, readErr)
}

func (g *sessionFenceGuard) completeIdleHandoff(window session.IdleCompactionWindow, err error) {
	handoff := g.idleHandoff
	g.idleHandoff = nil
	if handoff != nil && handoff.Complete != nil {
		handoff.Complete(window, err)
	}
}
