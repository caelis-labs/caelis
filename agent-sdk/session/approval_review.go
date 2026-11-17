package session

import "strings"

// ResolvedApprovalReview returns a completed reviewed approval decision for
// client projection. Pending requests, cancellations and unreviewed decisions
// remain internal. The returned record is borrowed and must not be modified.
// Its display eligibility never makes the approval fact part of model context.
func ResolvedApprovalReview(event *Event) *ApprovalRecord {
	if !IsJournal(event) || EventTypeOf(event) != EventTypeLifecycle || event.Approval == nil {
		return nil
	}
	token := event.Approval
	if token == nil || token.Status != ApprovalRecordResolved || strings.TrimSpace(token.ReviewText) == "" {
		return nil
	}
	if strings.TrimSpace(token.ToolCallID) == "" && (token.Approval == nil || token.Approval.Context == nil) {
		return nil
	}
	return token
}
