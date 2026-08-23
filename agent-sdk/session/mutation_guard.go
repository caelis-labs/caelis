package session

import (
	"context"
	"errors"
	"strings"
)

// MutationAuthority identifies the explicit authority for a session mutation.
type MutationAuthority string

const (
	// MutationAuthorityRuntime requires a live matching Session fence.
	MutationAuthorityRuntime MutationAuthority = "runtime"
	// MutationAuthorityControl is an explicit Control-owned mutation that does
	// not inherit Runtime fence ownership. Control mutations still require a
	// non-empty purpose so the bypass is inventoryable and never accidental.
	MutationAuthorityControl MutationAuthority = "control"
)

// ControlMutationPurpose names an inventoryable Control operation. The purpose
// policy decides whether it may overlap Runtime or requires the matching fence.
type ControlMutationPurpose string

const (
	ControlMutationPurposeApproval      ControlMutationPurpose = "approval"
	ControlMutationPurposeHandoff       ControlMutationPurpose = "handoff"
	ControlMutationPurposeCoordinator   ControlMutationPurpose = "coordinator"
	ControlMutationPurposeParticipant   ControlMutationPurpose = "participant"
	ControlMutationPurposeLifecycle     ControlMutationPurpose = "session_lifecycle"
	ControlMutationPurposeConfiguration ControlMutationPurpose = "session_configuration"
	ControlMutationPurposeTest          ControlMutationPurpose = "test"
	ControlMutationPurposeSystemCommit  ControlMutationPurpose = "system_commit"
	// ControlMutationPurposeSubagentCompletion owns the asynchronous terminal
	// Task and side-participant commit after the spawning Turn may have ended.
	ControlMutationPurposeSubagentCompletion ControlMutationPurpose = "subagent_completion"
)

// MutationGuard carries the authority and durable fence for one mutation.
type MutationGuard struct {
	Authority    MutationAuthority      `json:"authority,omitempty"`
	Purpose      ControlMutationPurpose `json:"purpose,omitempty"`
	FenceID      string                 `json:"fence_id,omitempty"`
	OwnerID      string                 `json:"owner_id,omitempty"`
	FencingToken uint64                 `json:"fencing_token,omitempty"`
	claimToken   string
}

type mutationGuardContextKey struct{}

// ContextWithRuntimeFence scopes Runtime-owned mutations to one Session fence.
func ContextWithRuntimeFence(ctx context.Context, fence SessionFence) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, mutationGuardContextKey{}, MutationGuard{
		Authority: MutationAuthorityRuntime, FenceID: fence.FenceID, OwnerID: fence.OwnerID, FencingToken: fence.FencingToken,
		claimToken: fence.claimToken,
	})
}

// ContextWithControlMutation replaces any inherited Runtime fence with one
// inventoryable asynchronous Control mutation purpose.
func ContextWithControlMutation(ctx context.Context, purpose ControlMutationPurpose) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, mutationGuardContextKey{}, ControlMutationGuard(purpose))
}

// ContextWithoutRuntimeFence starts a distinct Runtime placement scope while
// preserving cancellation, deadlines, and unrelated context values. Nested
// runtimes must use it before operating on a different Session; a parent
// Session's fence is not valid authority for the nested Session. This does not
// bypass an active store fence because an unguarded mutation still conflicts
// while that fence is active.
func ContextWithoutRuntimeFence(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, mutationGuardContextKey{}, MutationGuard{})
}

// RuntimeMutationGuard returns the Runtime fence carried by ctx.
func RuntimeMutationGuard(ctx context.Context) MutationGuard {
	if ctx == nil {
		return MutationGuard{}
	}
	guard, _ := ctx.Value(mutationGuardContextKey{}).(MutationGuard)
	return guard
}

// ControlMutationGuardWithRuntimeFence marks a Control-owned mutation while
// retaining the execution fence carried by ctx. Exclusive Control operations
// such as controller handoff use this form after acquiring the Session's
// execution fence; losing that fence invalidates the mutation.
func ControlMutationGuardWithRuntimeFence(ctx context.Context, purpose ControlMutationPurpose) MutationGuard {
	guard := ControlMutationGuard(purpose)
	runtimeGuard := RuntimeMutationGuard(ctx)
	if runtimeGuard.Authority != MutationAuthorityRuntime {
		return guard
	}
	guard.FenceID = strings.TrimSpace(runtimeGuard.FenceID)
	guard.OwnerID = strings.TrimSpace(runtimeGuard.OwnerID)
	guard.FencingToken = runtimeGuard.FencingToken
	guard.claimToken = runtimeGuard.claimToken
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
		return &FenceConflictError{Detail: "control mutation requires a non-empty purpose"}
	}
	if !knownControlMutationPurpose(purpose) {
		return &FenceConflictError{Detail: "control mutation purpose is unknown"}
	}
	hasFenceID := strings.TrimSpace(guard.FenceID) != ""
	hasOwnerID := strings.TrimSpace(guard.OwnerID) != ""
	hasFence := guard.FencingToken != 0
	if (hasFenceID || hasOwnerID || hasFence) && (!hasFenceID || !hasOwnerID || !hasFence) {
		return &FenceConflictError{Detail: "control mutation fence requires fence_id, owner_id, and fencing_token"}
	}
	if !hasFenceID && controlMutationRequiresFence(purpose) {
		return &FenceConflictError{Detail: "control mutation purpose requires a matching runtime fence"}
	}
	return nil
}

// AuthorizeMutationGuard applies the shared fence decision for one
// mutation. Persistence implementations call it while holding their own
// transaction lock; it does not read or mutate backend state.
func AuthorizeMutationGuard(active SessionFence, guard MutationGuard) error {
	conflict := func(detail string) error {
		return &FenceConflictError{SessionID: NormalizeSessionRef(active.SessionRef).SessionID, Detail: detail}
	}
	if guard.Authority == MutationAuthorityControl {
		if err := ValidateControlMutationGuard(guard); err != nil {
			var fenceErr *FenceConflictError
			if errors.As(err, &fenceErr) {
				fenceErr.SessionID = NormalizeSessionRef(active.SessionRef).SessionID
			}
			return err
		}
		hasFence := strings.TrimSpace(guard.FenceID) != ""
		if hasFence {
			if !SessionFenceIsHeld(active) {
				return conflict("control mutation fence is absent")
			}
			if !sessionFenceMutationAuthorized(active, guard) {
				return conflict("control mutation fencing token is stale")
			}
			return nil
		}
		if SessionFenceIsHeld(active) && !ControlMutationMayOverlapRuntimeFence(guard.Purpose) {
			return conflict("active execution fence requires a matching control fence")
		}
		return nil
	}
	if guard.Authority != MutationAuthorityRuntime {
		if active.FenceID == "" {
			return nil
		}
		return conflict("active fence requires explicit mutation authority")
	}
	if !SessionFenceIsHeld(active) {
		return conflict("runtime fence is absent")
	}
	if !sessionFenceMutationAuthorized(active, guard) {
		return conflict("runtime fencing token is stale")
	}
	return nil
}

func sessionFenceMutationAuthorized(active SessionFence, guard MutationGuard) bool {
	return active.FenceID == strings.TrimSpace(guard.FenceID) &&
		active.OwnerID == strings.TrimSpace(guard.OwnerID) &&
		active.FencingToken == guard.FencingToken &&
		sessionFenceClaimMatches(active.claimDigest, guard.claimToken)
}

// ControlMutationMayOverlapRuntimeFence reports whether an unfenced Control
// mutation is explicitly safe while a Turn owns the Session execution fence.
// Unknown purposes fail closed during guard validation.
func ControlMutationMayOverlapRuntimeFence(purpose ControlMutationPurpose) bool {
	switch ControlMutationPurpose(strings.TrimSpace(string(purpose))) {
	case ControlMutationPurposeApproval,
		ControlMutationPurposeParticipant,
		ControlMutationPurposeSystemCommit,
		ControlMutationPurposeSubagentCompletion,
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
		ControlMutationPurposeCoordinator,
		ControlMutationPurposeParticipant,
		ControlMutationPurposeLifecycle,
		ControlMutationPurposeConfiguration,
		ControlMutationPurposeTest,
		ControlMutationPurposeSystemCommit,
		ControlMutationPurposeSubagentCompletion:
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
