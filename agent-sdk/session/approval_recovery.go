package session

import (
	"context"
	"strings"
)

// PendingApproval identifies one durable permission request that has no later
// settlement event in its Session.
type PendingApproval struct {
	SessionRef SessionRef
	// Revision is the Session revision observed in the same Store critical
	// section as Request. Recovery must pass it back as ExpectedRevision when
	// attempting a settlement.
	Revision uint64
	Request  *Event
}

// SettlePendingApprovalRequest conditionally appends one approval settlement.
// The request identity and expected revision must come from the same
// PendingApproval snapshot. Stores check that exact request is still pending,
// the revision still matches, and the mutation guard is authorized in one
// atomic persistence critical section before appending Settlement.
type SettlePendingApprovalRequest struct {
	SessionRef             SessionRef
	ExpectedRevision       *uint64
	MutationGuard          MutationGuard
	ApprovalRequestID      string
	ExpectedRequestEventID string
	ExpectedRequestSeq     uint64
	Settlement             *Event
}

// SettlePendingApprovalResult reports the outcome of one conditional approval
// settlement. Settled is false, without a mutation, when the expected request
// has already been settled or replaced.
type SettlePendingApprovalResult struct {
	Settled bool
	Event   *Event
}

// ApprovalRecoveryReader scans Store-maintained pending-approval indexes.
// Implementations may page the Session namespace and rebuild a legacy missing
// index from canonical events in per-Session transactions before returning.
type ApprovalRecoveryReader interface {
	PendingApprovals(context.Context) ([]PendingApproval, error)
}

// ApprovalRecoverySettler atomically settles one still-pending approval
// candidate. Implementations return ErrRevisionConflict when the same request
// remains pending at a different Session revision; callers may retry with the
// Actual revision carried by RevisionConflictError. A stale request identity is
// an idempotent no-op reported through Settled=false.
type ApprovalRecoverySettler interface {
	SettlePendingApproval(context.Context, SettlePendingApprovalRequest) (SettlePendingApprovalResult, error)
}

// ApprovalRecoveryService combines pending approval discovery with atomic
// conditional settlement.
type ApprovalRecoveryService interface {
	ApprovalRecoveryReader
	ApprovalRecoverySettler
}

// ValidateSettlePendingApprovalRequest validates the reusable conditional
// settlement contract before a Store enters its persistence transaction.
func ValidateSettlePendingApprovalRequest(req SettlePendingApprovalRequest) error {
	requestID := strings.TrimSpace(req.ApprovalRequestID)
	if NormalizeSessionRef(req.SessionRef).SessionID == "" || requestID == "" ||
		req.ExpectedRevision == nil || strings.TrimSpace(req.ExpectedRequestEventID) == "" || req.ExpectedRequestSeq == 0 {
		return ErrInvalidTransaction
	}
	if req.Settlement == nil || req.Settlement.Type != EventTypeLifecycle || req.Settlement.Lifecycle == nil ||
		ProtocolPermissionOf(req.Settlement) != nil ||
		strings.TrimSpace(req.Settlement.ApprovalRequestID) != requestID {
		return ErrInvalidEvent
	}
	return nil
}

// PendingApprovalMatches reports whether current is the exact request identity
// named by req. Store implementations use this only while holding the same
// persistence critical section that protects the conditional append; this pure
// helper is not a caller-side substitute for ApprovalRecoverySettler.
func PendingApprovalMatches(current *Event, req SettlePendingApprovalRequest) bool {
	if current == nil {
		return false
	}
	return strings.TrimSpace(current.ApprovalRequestID) == strings.TrimSpace(req.ApprovalRequestID) &&
		strings.TrimSpace(current.ID) == strings.TrimSpace(req.ExpectedRequestEventID) &&
		current.Seq == req.ExpectedRequestSeq &&
		ProtocolPermissionOf(current) != nil
}
