package session

import (
	"context"
	"errors"
	"strings"
	"time"
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

// ControlMutationPurpose names an inventoryable Control operation. The purpose
// policy decides whether it may overlap Runtime or requires the matching fence.
type ControlMutationPurpose string

const (
	ControlMutationPurposeApproval      ControlMutationPurpose = "approval"
	ControlMutationPurposeHandoff       ControlMutationPurpose = "handoff"
	ControlMutationPurposeWatchdog      ControlMutationPurpose = "watchdog"
	ControlMutationPurposeCoordinator   ControlMutationPurpose = "coordinator"
	ControlMutationPurposeParticipant   ControlMutationPurpose = "participant"
	ControlMutationPurposeLifecycle     ControlMutationPurpose = "session_lifecycle"
	ControlMutationPurposeConfiguration ControlMutationPurpose = "session_configuration"
	ControlMutationPurposeTest          ControlMutationPurpose = "test"
	ControlMutationPurposeSystemCommit  ControlMutationPurpose = "system_commit"
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

// ContextWithoutRuntimeLease starts a distinct Runtime placement scope while
// preserving cancellation, deadlines, and unrelated context values. Nested
// runtimes must use it before operating on a different Session; a parent
// Session's fence is not valid authority for the nested Session. This does not
// bypass an active store lease because an unguarded mutation still conflicts
// while that lease is active.
func ContextWithoutRuntimeLease(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, mutationGuardContextKey{}, MutationGuard{})
}

// RuntimeMutationGuard returns the Runtime lease fence carried by ctx.
func RuntimeMutationGuard(ctx context.Context) MutationGuard {
	if ctx == nil {
		return MutationGuard{}
	}
	guard, _ := ctx.Value(mutationGuardContextKey{}).(MutationGuard)
	return guard
}

// ControlMutationGuardWithRuntimeLease marks a Control-owned mutation while
// retaining the execution fence carried by ctx. Exclusive Control operations
// such as controller handoff use this form after acquiring the Session's
// execution lease; losing that lease invalidates the mutation.
func ControlMutationGuardWithRuntimeLease(ctx context.Context, purpose ControlMutationPurpose) MutationGuard {
	guard := ControlMutationGuard(purpose)
	runtimeGuard := RuntimeMutationGuard(ctx)
	if runtimeGuard.Authority != MutationAuthorityRuntime {
		return guard
	}
	guard.LeaseID = strings.TrimSpace(runtimeGuard.LeaseID)
	guard.OwnerID = strings.TrimSpace(runtimeGuard.OwnerID)
	guard.FencingToken = runtimeGuard.FencingToken
	return guard
}

// ControlMutationGuard explicitly marks a non-Run Control mutation. Purpose is
// required so Control never becomes an anonymous unfenced writer.
func ControlMutationGuard(purpose ControlMutationPurpose) MutationGuard {
	return MutationGuard{Authority: MutationAuthorityControl, Purpose: ControlMutationPurpose(strings.TrimSpace(string(purpose)))}
}

// ValidateControlMutationGuard reports whether a Control authority guard names
// a supported purpose and carries the complete fence required by that purpose.
func ValidateControlMutationGuard(guard MutationGuard) error {
	if guard.Authority != MutationAuthorityControl {
		return nil
	}
	purpose := ControlMutationPurpose(strings.TrimSpace(string(guard.Purpose)))
	if purpose == "" {
		return &LeaseConflictError{Detail: "control mutation requires a non-empty purpose"}
	}
	if !knownControlMutationPurpose(purpose) {
		return &LeaseConflictError{Detail: "control mutation purpose is unknown"}
	}
	hasLeaseID := strings.TrimSpace(guard.LeaseID) != ""
	hasOwnerID := strings.TrimSpace(guard.OwnerID) != ""
	hasFence := guard.FencingToken != 0
	if (hasLeaseID || hasOwnerID || hasFence) && (!hasLeaseID || !hasOwnerID || !hasFence) {
		return &LeaseConflictError{Detail: "control mutation fence requires lease_id, owner_id, and fencing_token"}
	}
	if !hasLeaseID && controlMutationRequiresFence(purpose) {
		return &LeaseConflictError{Detail: "control mutation purpose requires a matching runtime lease fence"}
	}
	return nil
}

// AuthorizeMutationGuard applies the shared lease-fence decision for one
// mutation. Persistence implementations call it while holding their own
// transaction lock; it does not read or mutate backend state.
func AuthorizeMutationGuard(active SessionLease, guard MutationGuard, now time.Time) error {
	conflict := func(detail string) error {
		return &LeaseConflictError{SessionID: NormalizeSessionRef(active.SessionRef).SessionID, Detail: detail}
	}
	if guard.Authority == MutationAuthorityControl {
		if err := ValidateControlMutationGuard(guard); err != nil {
			var leaseErr *LeaseConflictError
			if errors.As(err, &leaseErr) {
				leaseErr.SessionID = NormalizeSessionRef(active.SessionRef).SessionID
			}
			return err
		}
		hasFence := strings.TrimSpace(guard.LeaseID) != ""
		if hasFence {
			if active.LeaseID == "" || !active.ExpiresAt.After(now) {
				return conflict("control mutation fence is absent or expired")
			}
			if active.LeaseID != strings.TrimSpace(guard.LeaseID) || active.OwnerID != strings.TrimSpace(guard.OwnerID) || active.FencingToken != guard.FencingToken {
				return conflict("control mutation fencing token is stale")
			}
			return nil
		}
		if active.LeaseID != "" && active.ExpiresAt.After(now) && !ControlMutationMayOverlapRuntimeLease(guard.Purpose) {
			return conflict("active execution lease requires a matching control fence")
		}
		return nil
	}
	if guard.Authority != MutationAuthorityRuntime {
		if active.LeaseID == "" {
			return nil
		}
		return conflict("active lease requires explicit mutation authority")
	}
	if active.LeaseID == "" || !active.ExpiresAt.After(now) {
		return conflict("runtime lease is absent or expired")
	}
	if active.LeaseID != strings.TrimSpace(guard.LeaseID) || active.OwnerID != strings.TrimSpace(guard.OwnerID) || active.FencingToken != guard.FencingToken {
		return conflict("runtime fencing token is stale")
	}
	return nil
}

// ControlMutationMayOverlapRuntimeLease reports whether an unfenced Control
// mutation is explicitly safe while a Turn owns the Session execution lease.
// Unknown purposes fail closed during guard validation.
func ControlMutationMayOverlapRuntimeLease(purpose ControlMutationPurpose) bool {
	switch ControlMutationPurpose(strings.TrimSpace(string(purpose))) {
	case ControlMutationPurposeApproval,
		ControlMutationPurposeWatchdog,
		ControlMutationPurposeParticipant,
		ControlMutationPurposeSystemCommit,
		ControlMutationPurposeTest:
		return true
	default:
		return false
	}
}

func knownControlMutationPurpose(purpose ControlMutationPurpose) bool {
	switch purpose {
	case ControlMutationPurposeApproval,
		ControlMutationPurposeHandoff,
		ControlMutationPurposeWatchdog,
		ControlMutationPurposeCoordinator,
		ControlMutationPurposeParticipant,
		ControlMutationPurposeLifecycle,
		ControlMutationPurposeConfiguration,
		ControlMutationPurposeTest,
		ControlMutationPurposeSystemCommit:
		return true
	default:
		return false
	}
}

func controlMutationRequiresFence(purpose ControlMutationPurpose) bool {
	switch purpose {
	case ControlMutationPurposeHandoff, ControlMutationPurposeCoordinator:
		return true
	default:
		return false
	}
}
