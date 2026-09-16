package session

import "strings"

// ResolvedApprovalReview returns a journal's completed reviewed decision for
// client projection. Pending pauses, cancellations and unreviewed decisions
// remain internal. The returned token is borrowed and must not be modified.
// Its display eligibility never makes the journal part of model context.
func ResolvedApprovalReview(event *Event) *PauseToken {
	if !IsJournal(event) || EventTypeOf(event) != EventTypeLifecycle || event.Journal == nil || event.Journal.Kind != JournalKindPauseToken {
		return nil
	}
	token := event.Journal.PauseToken
	if token == nil || token.Status != PauseTokenResolved || strings.TrimSpace(token.ToolCallID) == "" || strings.TrimSpace(token.ReviewText) == "" {
		return nil
	}
	return token
}
