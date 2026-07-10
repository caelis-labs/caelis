package session

import "context"

// MutationAuthority identifies the explicit authority for a session mutation.
type MutationAuthority string

const (
	// MutationAuthorityRuntime requires a live matching session lease fence.
	MutationAuthorityRuntime MutationAuthority = "runtime"
	// MutationAuthorityControl is an explicit Control-owned mutation that does
	// not inherit Runtime lease ownership.
	MutationAuthorityControl MutationAuthority = "control"
)

// MutationGuard carries the authority and durable fence for one mutation.
type MutationGuard struct {
	Authority    MutationAuthority `json:"authority,omitempty"`
	LeaseID      string            `json:"lease_id,omitempty"`
	OwnerID      string            `json:"owner_id,omitempty"`
	FencingToken uint64            `json:"fencing_token,omitempty"`
}

type mutationGuardContextKey struct{}

// ContextWithRuntimeLease scopes Runtime-owned mutations to one lease fence.
func ContextWithRuntimeLease(ctx context.Context, lease SessionLease) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, mutationGuardContextKey{}, MutationGuard{
		Authority: MutationAuthorityRuntime, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID, FencingToken: lease.FencingToken,
	})
}

// RuntimeMutationGuard returns the Runtime lease fence carried by ctx.
func RuntimeMutationGuard(ctx context.Context) MutationGuard {
	if ctx == nil {
		return MutationGuard{}
	}
	guard, _ := ctx.Value(mutationGuardContextKey{}).(MutationGuard)
	return guard
}

// ControlMutationGuard explicitly marks a non-Run Control mutation.
func ControlMutationGuard() MutationGuard {
	return MutationGuard{Authority: MutationAuthorityControl}
}
