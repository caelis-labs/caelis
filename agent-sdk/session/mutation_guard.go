package session

import (
	"context"
	"strings"
)

// MutationAuthority identifies the explicit authority for a session mutation.
type MutationAuthority string

const (
	// MutationAuthorityRuntime requires a live matching session lease fence.
	MutationAuthorityRuntime MutationAuthority = "runtime"
	// MutationAuthorityControl is an explicit Control-owned mutation that does
	// not inherit Runtime lease ownership. Control mutations still require a
	// non-empty purpose so the bypass is inventoryable and never accidental.
	MutationAuthorityControl MutationAuthority = "control"
)

// ControlMutationPurpose names the Control operation that is intentionally
// allowed to mutate outside a Runtime lease fence.
type ControlMutationPurpose string

const (
	ControlMutationPurposeApproval     ControlMutationPurpose = "approval"
	ControlMutationPurposeHandoff      ControlMutationPurpose = "handoff"
	ControlMutationPurposeWatchdog     ControlMutationPurpose = "watchdog"
	ControlMutationPurposeCoordinator  ControlMutationPurpose = "coordinator"
	ControlMutationPurposeTest         ControlMutationPurpose = "test"
	ControlMutationPurposeSystemCommit ControlMutationPurpose = "system_commit"
)

// MutationGuard carries the authority and durable fence for one mutation.
type MutationGuard struct {
	Authority    MutationAuthority      `json:"authority,omitempty"`
	Purpose      ControlMutationPurpose `json:"purpose,omitempty"`
	LeaseID      string                 `json:"lease_id,omitempty"`
	OwnerID      string                 `json:"owner_id,omitempty"`
	FencingToken uint64                 `json:"fencing_token,omitempty"`
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

// ControlMutationGuard explicitly marks a non-Run Control mutation. Purpose is
// required so Control never becomes an anonymous unfenced writer.
func ControlMutationGuard(purpose ControlMutationPurpose) MutationGuard {
	return MutationGuard{Authority: MutationAuthorityControl, Purpose: ControlMutationPurpose(strings.TrimSpace(string(purpose)))}
}

// ValidateControlMutationGuard reports whether a Control authority guard has a
// non-empty purpose. Stores call this before accepting an unfenced write.
func ValidateControlMutationGuard(guard MutationGuard) error {
	if guard.Authority != MutationAuthorityControl {
		return nil
	}
	if strings.TrimSpace(string(guard.Purpose)) == "" {
		return &LeaseConflictError{Detail: "control mutation requires a non-empty purpose"}
	}
	return nil
}
