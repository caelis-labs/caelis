package session

import (
	"context"
	"strings"
	"sync"
)

// TurnAdmission grants exclusive execution on an existing Session. BeginTurn
// rejects a competing producer, while readers keep seeing committed history.
// The caller must end the Turn after all producers have stopped, including on
// cancellation and failed startup. The returned context scopes its writes.
type TurnAdmission interface {
	BeginTurn(context.Context, SessionRef, string) (context.Context, func() error, error)
}

// TurnFenceBackend enforces writer ownership and validates a fence delegated
// by a host before its producer runs.
type TurnFenceBackend interface {
	MutationGuardValidator
	AcquireSessionFence(context.Context, AcquireSessionFenceRequest) (SessionFence, error)
	ReleaseSessionFence(context.Context, ReleaseSessionFenceRequest) error
}

// BeginFencedTurn implements TurnAdmission for services backed by Session
// fences. A host-delegated claim admits one producer at a time and stays owned
// by the host. Standalone execution acquires and releases its own durable claim.
func BeginFencedTurn(ctx context.Context, backend TurnFenceBackend, ref SessionRef, owner string) (context.Context, func() error, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	guard := RuntimeMutationGuard(ctx)
	if guard.FenceID != "" {
		if err := backend.ValidateMutationGuard(ctx, ref, guard); err != nil {
			return nil, nil, err
		}
		if guard.Authority != MutationAuthorityRuntime || guard.turnInUse == nil || !guard.turnInUse.CompareAndSwap(false, true) {
			return nil, nil, &FenceConflictError{SessionID: ref.SessionID, Detail: "execution claim already in use or unavailable"}
		}
		return ctx, sync.OnceValue(func() error { guard.turnInUse.Store(false); return nil }), nil
	}
	req := AcquireSessionFenceRequest{SessionRef: ref, OwnerID: owner}
	fence, err := backend.AcquireSessionFence(ctx, req)
	if err != nil && !IsCommitted(err) {
		return nil, nil, err
	}
	if !acquiredTurnFenceMatches(ref, owner, fence) {
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, &FenceConflictError{SessionID: ref.SessionID, Detail: "acquisition did not return the requested execution claim"}
	}
	fence.turnInUse.Store(true)
	return ContextWithRuntimeFence(ctx, fence), func() error {
		return backend.ReleaseSessionFence(context.WithoutCancel(ctx), SessionFenceReleaseRequest(fence))
	}, nil
}

func acquiredTurnFenceMatches(ref SessionRef, owner string, fence SessionFence) bool {
	want, got := NormalizeSessionRef(ref), NormalizeSessionRef(fence.SessionRef)
	return want.SessionID != "" && want.SessionID == got.SessionID &&
		(want.AppName == "" || want.AppName == got.AppName) &&
		(want.UserID == "" || want.UserID == got.UserID) &&
		(want.WorkspaceKey == "" || want.WorkspaceKey == got.WorkspaceKey) &&
		fence.OwnerID == strings.TrimSpace(owner) && fence.FenceID != "" && fence.FencingToken != 0 &&
		fence.turnInUse != nil && SessionFenceHasClaim(fence)
}
