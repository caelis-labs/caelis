package session

import "context"

// PendingApproval identifies one durable permission request that has no later
// settlement event in its Session.
type PendingApproval struct {
	SessionRef SessionRef
	Request    *Event
}

// ApprovalRecoveryReader scans the Store-maintained pending-approval index in
// one consistent operation. Implementations may rebuild legacy missing index
// data from canonical events before returning.
type ApprovalRecoveryReader interface {
	PendingApprovals(context.Context) ([]PendingApproval, error)
}
